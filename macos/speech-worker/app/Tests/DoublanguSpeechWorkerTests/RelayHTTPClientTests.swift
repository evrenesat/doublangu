import Foundation
import Network
import XCTest

@testable import DoublanguSpeechWorker

/// Minimal HTTP/1.1 loopback server for exercising the relay transport against
/// real sockets, including redirect and bounded-read behavior.
final class LoopbackHTTPServer: @unchecked Sendable {
  private let listener: NWListener
  private let queue = DispatchQueue(label: "doublangu.loopback-http")
  private let lock = NSLock()
  private var handler: (@Sendable (Data) -> Data)?
  private var receivedRequests = 0

  init() throws {
    listener = try NWListener(using: .tcp, on: .any)
  }

  var boundPort: UInt16 {
    let deadline = Date().addingTimeInterval(5)
    while Date() < deadline, (listener.port?.rawValue ?? 0) == 0 {
      if case .failed = listener.state { break }
      usleep(10_000)
    }
    return listener.port?.rawValue ?? 0
  }

  var requestsReceived: Int {
    lock.lock()
    defer { lock.unlock() }
    return receivedRequests
  }

  func start(handler: @escaping @Sendable (Data) -> Data) throws {
    lock.lock()
    self.handler = handler
    lock.unlock()
    listener.newConnectionHandler = { [weak self] connection in
      self?.accept(connection)
    }
    listener.start(queue: queue)
  }

  func stop() { listener.cancel() }

  private func accept(_ connection: NWConnection) {
    connection.start(queue: queue)
    receive(connection, buffer: Data())
  }

  private func receive(_ connection: NWConnection, buffer: Data) {
    connection.receive(minimumIncompleteLength: 1, maximumLength: 1 << 16) {
      [weak self] data, _, complete, error in
      guard let self else {
        connection.cancel()
        return
      }
      var buffer = buffer
      if let data { buffer.append(data) }
      if let headerEnd = buffer.range(of: Data("\r\n\r\n".utf8)) {
        let headerText = String(
          decoding: buffer[..<headerEnd.lowerBound], as: UTF8.self
        ).lowercased()
        let contentLength =
          headerText.split(separator: "\r\n").first { $0.hasPrefix("content-length:") }
          .flatMap {
            Int($0.dropFirst("content-length:".count).trimmingCharacters(in: .whitespaces))
          } ?? 0
        if buffer.count - headerEnd.upperBound >= contentLength {
          self.lock.lock()
          self.receivedRequests += 1
          let handler = self.handler
          self.lock.unlock()
          let response = handler?(buffer) ?? Data()
          connection.send(
            content: response,
            completion: .contentProcessed { _ in connection.cancel() })
          return
        }
      }
      if complete || error != nil {
        connection.cancel()
        return
      }
      self.receive(connection, buffer: buffer)
    }
  }
}

func httpResponse(status: Int, reason: String, body: Data, extraHeaders: String = "") -> Data {
  Data(
    "HTTP/1.1 \(status) \(reason)\r\nContent-Length: \(body.count)\r\nContent-Type: application/json\r\n\(extraHeaders)\r\n"
      .utf8
  ) + body
}

@MainActor
final class RelayHTTPClientTests: XCTestCase {
  private func makeClient(
    _ server: LoopbackHTTPServer, timeout: TimeInterval = 5
  ) -> RelayHTTPClient {
    RelayHTTPClient(
      target: RelayTarget(
        baseURL: URL(string: "http://127.0.0.1:\(server.boundPort)/v1")!, timeout: timeout))
  }

  private func requestLease() -> RelayChatLease {
    if case .chat(let lease) = testRelayChatLease().relay! { return lease }
    fatalError("unreachable")
  }

  func testChatCompletionSendsOpenAICompatibleRequestAndParsesResponse() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    let expectedSchema =
      "{\"name\":\"doublangu_stage_artifact\",\"strict\":true,\"schema\":{\"type\":\"object\"}}"
    try server.start { request in
      let text = String(decoding: request, as: UTF8.self)
      XCTAssertTrue(text.hasPrefix("POST /v1/chat/completions "))
      XCTAssertTrue(text.lowercased().contains("authorization: bearer sk-local-test"))
      XCTAssertTrue(text.lowercased().contains("accept: application/json"))
      guard let headerEnd = request.range(of: Data("\r\n\r\n".utf8)) else {
        return httpResponse(status: 400, reason: "Bad", body: Data())
      }
      let payload =
        try! JSONSerialization.jsonObject(
          with: request[headerEnd.upperBound...]) as! [String: Any]
      XCTAssertEqual(payload["model"] as? String, "Qwen3.5-2B-MLX-8bit")
      XCTAssertEqual(payload["stream"] as? Bool, false)
      XCTAssertEqual(payload["max_tokens"] as? Int, 16_384)
      XCTAssertEqual(payload["temperature"] as? Double, 0.0)
      let messages = payload["messages"] as? [[String: Any]] ?? []
      XCTAssertEqual(messages.first?["role"] as? String, "user")
      let format = payload["response_format"] as! [String: Any]
      XCTAssertEqual(format["type"] as? String, "json_schema")
      let schema = format["json_schema"] as! [String: Any]
      XCTAssertEqual(schema["name"] as? String, "doublangu_stage_artifact")
      XCTAssertEqual(schema["strict"] as? Bool, true)
      XCTAssertEqual(
        (schema["schema"] as? [String: Any])?["type"] as? String, "object")
      let response =
        "{\"id\":\"chatcmpl-7\",\"model\":\"Qwen3.5-2B-MLX-8bit\",\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"{\\\"x\\\":1}\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15,\"model_load_duration\":0.5},\"total_time\":2.5}"
      return httpResponse(status: 200, reason: "OK", body: Data(response.utf8))
    }
    let client = makeClient(server)

    let completion = try await client.chatCompletion(requestLease(), apiKey: "sk-local-test")

    XCTAssertEqual(completion.id, "chatcmpl-7")
    XCTAssertEqual(completion.reportedModel, "Qwen3.5-2B-MLX-8bit")
    XCTAssertEqual(completion.content, "{\"x\":1}")
    XCTAssertEqual(completion.finishReason, "stop")
    XCTAssertEqual(completion.usage.promptTokens, 12)
    XCTAssertEqual(completion.usage.totalTokens, 15)
    XCTAssertEqual(completion.timing.modelLoadDuration, 0.5)
    XCTAssertEqual(completion.timing.totalTime, 2.5)
  }

  func testListModelsReturnsUniqueNonEmptyIDsInOrder() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in
      let response =
        "{\"data\":[{\"id\":\"Qwen3.6-35B-A3B-UD-MLX-4bit\"},{\"id\":\"Qwen3.5-2B-MLX-8bit\"},{\"id\":\"Qwen3.6-35B-A3B-UD-MLX-4bit\"}]}"
      return httpResponse(status: 200, reason: "OK", body: Data(response.utf8))
    }
    let client = makeClient(server)

    let models = try await client.listModels(apiKey: "sk-local-test")

    XCTAssertEqual(models, ["Qwen3.6-35B-A3B-UD-MLX-4bit", "Qwen3.5-2B-MLX-8bit"])
  }

  func testChatEnvelopeValidationFailures() async throws {
    func response(for body: String) async throws -> RelayHTTPError {
      let server = try LoopbackHTTPServer()
      defer { server.stop() }
      try server.start { _ in httpResponse(status: 200, reason: "OK", body: Data(body.utf8)) }
      let client = makeClient(server)
      do {
        _ = try await client.chatCompletion(requestLease(), apiKey: "k")
        return RelayHTTPError.invalidResponse(excerpt: "no_error")
      } catch let error as RelayHTTPError {
        return error
      }
    }

    guard
      case .invalidResponse = try await response(
        for:
          "{\"choices\":[{\"message\":{\"content\":\"a\"},\"finish_reason\":\"stop\"},{\"message\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}]}"
      )
    else { return XCTFail("expected two-choice rejection") }
    guard
      case .invalidResponse = try await response(
        for: "{\"choices\":[{\"message\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}")
    else { return XCTFail("expected empty-content rejection") }
    guard
      case .invalidResponse = try await response(
        for: "{\"choices\":[{\"message\":{\"content\":\"a\"},\"finish_reason\":\"length\"}]}")
    else { return XCTFail("expected finish-reason rejection") }
    guard
      case .invalidResponse = try await response(
        for:
          "{\"choices\":[{\"message\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":0,\"total_tokens\":0}}"
      )
    else { return XCTFail("expected usage rejection") }
    guard
      case .invalidResponse = try await response(
        for:
          "{\"choices\":[{\"message\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}],\"model\":\"\(String(repeating: "m", count: 257))\"}"
      )
    else { return XCTFail("expected oversized model rejection") }
  }

  func testNon2xxStatusesSurfaceAsHTTPError() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in httpResponse(status: 401, reason: "Unauthorized", body: Data()) }
    let client = makeClient(server)

    do {
      _ = try await client.listModels(apiKey: "k")
      XCTFail("expected error")
    } catch let error as RelayHTTPError {
      XCTAssertEqual(error, .http(status: 401, excerpt: ""))
    }
  }

  func testStructuredErrorCodeClassifiesModelUnknown() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in
      httpResponse(
        status: 404, reason: "Not Found",
        body: Data(
          "{\"error\":{\"code\":\"model_not_found\",\"message\":\"nope\"}}".utf8))
    }
    let client = makeClient(server)

    do {
      _ = try await client.chatCompletion(requestLease(), apiKey: "k")
      XCTFail("expected modelUnknown")
    } catch let error as RelayHTTPError {
      XCTAssertEqual(error, .modelUnknown)
    }
  }

  func testMessageBasedModelUnknownRequiresExactModelAndPhrase() async throws {
    func classify(_ body: String, status: Int) async throws -> RelayHTTPError {
      let server = try LoopbackHTTPServer()
      defer { server.stop() }
      try server.start { _ in httpResponse(status: status, reason: "Err", body: Data(body.utf8)) }
      let client = makeClient(server)
      do {
        _ = try await client.chatCompletion(requestLease(), apiKey: "k")
        return RelayHTTPError.invalidResponse(excerpt: "no_error")
      } catch let error as RelayHTTPError {
        return error
      }
    }

    guard
      case .modelUnknown = try await classify(
        "{\"error\":{\"message\":\"model Qwen3.5-2B-MLX-8bit not found\"}}", status: 400)
    else { return XCTFail("expected modelUnknown") }
    guard
      case .modelUnknown = try await classify(
        "{\"error\":{\"message\":\"unknown model: Qwen3.5-2B-MLX-8bit\"}}", status: 404)
    else { return XCTFail("expected modelUnknown") }
    guard
      case .http(let status, _) = try await classify(
        "{\"error\":{\"message\":\"model other-model not found\"}}", status: 400)
    else { return XCTFail("expected plain http error") }
    XCTAssertEqual(status, 400)
  }

  func testRedirectsAreRejectedAndNotFollowed() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in
      httpResponse(
        status: 302, reason: "Found", body: Data(),
        extraHeaders: "Location: http://127.0.0.1:9/v1/models\r\n")
    }
    let client = makeClient(server)

    do {
      _ = try await client.listModels(apiKey: "k")
      XCTFail("expected redirect rejection")
    } catch let error as RelayHTTPError {
      guard case .http(let status, _) = error else { return XCTFail("unexpected \(error)") }
      XCTAssertEqual(status, 302)
    }
    XCTAssertEqual(server.requestsReceived, 1)
  }

  func testOversizedResponsesAreRejected() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { request in
      let text = String(decoding: request.prefix(40), as: UTF8.self)
      if text.contains("/models") {
        return httpResponse(status: 200, reason: "OK", body: Data(count: 2_097_153))
      }
      return httpResponse(status: 200, reason: "OK", body: Data())
    }
    let client = makeClient(server)

    do {
      _ = try await client.listModels(apiKey: "k")
      XCTFail("expected oversized rejection")
    } catch let error as RelayHTTPError {
      XCTAssertEqual(error, .oversized)
    }
  }

  func testErrorExcerptsAreBoundedAndKeyRedacted() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in
      httpResponse(
        status: 500, reason: "Internal", body: Data("boom secret-key-123 boom".utf8))
    }
    let client = makeClient(server)

    do {
      _ = try await client.listModels(apiKey: "secret-key-123")
      XCTFail("expected http error")
    } catch let error as RelayHTTPError {
      XCTAssertFalse(error.excerpt.contains("secret-key-123"))
      XCTAssertTrue(error.excerpt.contains("[redacted]"))
    }
  }

  func testLocalTimeoutSurfacesAsTimedOut() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in
      usleep(1_500_000)
      return httpResponse(status: 200, reason: "OK", body: Data("{\"data\":[]}".utf8))
    }
    let client = makeClient(server, timeout: 0.5)

    do {
      _ = try await client.listModels(apiKey: "k")
      XCTFail("expected timeout")
    } catch let error as RelayHTTPError {
      XCTAssertEqual(error, .timedOut)
    }
  }

  func testTaskCancellationCancelsTheUnderlyingRequest() async throws {
    let server = try LoopbackHTTPServer()
    defer { server.stop() }
    try server.start { _ in
      usleep(1_500_000)
      return httpResponse(status: 200, reason: "OK", body: Data("{\"data\":[]}".utf8))
    }
    let client = makeClient(server, timeout: 10)

    let task = Task {
      try await client.listModels(apiKey: "k")
    }
    try await Task.sleep(nanoseconds: 150_000_000)
    task.cancel()
    do {
      _ = try await task.value
      XCTFail("expected cancellation")
    } catch let error as RelayHTTPError {
      XCTAssertEqual(error, .canceled)
    }
  }
}

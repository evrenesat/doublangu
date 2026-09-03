import Foundation
import XCTest

@testable import DoublanguSpeechWorker

@MainActor
final class RelayLoopTests: XCTestCase {
  func testFailureMatrixMapsLocalErrorsToWorkerCodes() {
    func assertMapping(
      _ error: RelayHTTPError, code: String, retry: Bool, file: StaticString = #filePath,
      line: UInt = #line
    ) {
      let mapped = RelayLoop.failure(for: error)
      XCTAssertEqual(mapped.code, code, file: file, line: line)
      XCTAssertEqual(mapped.retry, retry, file: file, line: line)
    }
    assertMapping(.cannotConnect, code: "v1.relay_unreachable", retry: true)
    assertMapping(.connectionLost, code: "v1.relay_unreachable", retry: true)
    assertMapping(.http(status: 503, excerpt: ""), code: "v1.relay_unreachable", retry: true)
    assertMapping(.http(status: 429, excerpt: ""), code: "v1.relay_unreachable", retry: true)
    assertMapping(.http(status: 408, excerpt: ""), code: "v1.relay_unreachable", retry: true)
    assertMapping(.timedOut, code: "v1.relay_unreachable", retry: false)
    assertMapping(.http(status: 401, excerpt: ""), code: "v1.relay_auth", retry: false)
    assertMapping(.http(status: 403, excerpt: ""), code: "v1.relay_auth", retry: false)
    assertMapping(.apiKeyMissing, code: "v1.relay_auth", retry: false)
    assertMapping(.modelUnknown, code: "v1.relay_model_unknown", retry: false)
    assertMapping(.canceled, code: "v1.relay_canceled", retry: false)
    assertMapping(.oversized, code: "v1.relay_invalid_response", retry: false)
    assertMapping(
      .invalidResponse(excerpt: "x"), code: "v1.relay_invalid_response", retry: false)
    assertMapping(.http(status: 400, excerpt: ""), code: "v1.relay_invalid_response", retry: false)
  }

  func testChatSuccessUploadsEchoedRequestIDAndReturnsToIdle() async throws {
    let harness = Harness()
    harness.client.leaseResponses = [.chat]
    harness.http.chatResult = testChatCompletion(content: "{\"ok\":true}")

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.client.completeRelayCalls.count == 1)

    XCTAssertEqual(harness.client.completeRelayCalls.first?.jobID, testRelayJobID)
    let result = try StrictJSON.decode(
      RelayChatResult.self, from: harness.client.completeRelayCalls.first!.result)
    XCTAssertEqual(result.requestID, testRelayRequestID)
    XCTAssertEqual(result.content, "{\"ok\":true}")
    XCTAssertEqual(result.providerRequestID, "chatcmpl-123")
    XCTAssertNil(harness.client.lastLeaseRequest?.capabilities)
    XCTAssertEqual(harness.client.lastLeaseRequest?.llmRelayCapabilities?.count, 1)
    XCTAssertEqual(harness.client.fails.count, 0)
    try await waitUntil(harness.loop.status == .idle)
  }

  func testListModelsSuccessUploadsEmptyCatalogAsModels() async throws {
    let harness = Harness()
    harness.client.leaseResponses = [.models]
    harness.http.modelsResult = []

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.client.completeRelayCalls.count == 1)

    let result = try StrictJSON.decode(
      RelayModelsResult.self, from: harness.client.completeRelayCalls.first!.result)
    XCTAssertEqual(result.requestID, testRelayRequestID)
    XCTAssertEqual(result.models, [])
  }

  func testLongCallSurvivesMultipleHeartbeatRenewals() async throws {
    let harness = Harness(heartbeatInterval: 0.02)
    harness.client.leaseResponses = [.chat]
    harness.http.chatDelay = 0.1

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.client.completeRelayCalls.count == 1)

    try await waitUntil(harness.client.heartbeatCount >= 3)
    XCTAssertNil(harness.client.fails.first)
  }

  func testServerCancellationCancelsLocalWorkAndAcknowledges() async throws {
    let harness = Harness(heartbeatInterval: 0.02)
    harness.client.leaseResponses = [.chat]
    harness.http.chatHangs = true
    harness.client.cancelRequestedOnHeartbeat = 2

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.client.fails.count == 1, timeout: 5)

    XCTAssertEqual(harness.client.fails.first?.errorCode, "v1.relay_canceled")
    XCTAssertEqual(harness.client.fails.first?.retry, false)
    XCTAssertEqual(harness.client.completeRelayCalls.count, 0)
  }

  func testStaleLease409StopsLocalWorkWithoutAcknowledgement() async throws {
    let harness = Harness(heartbeatInterval: 0.02)
    harness.client.leaseResponses = [.chat]
    harness.http.chatHangs = true
    harness.client.heartbeatFailure = .http(status: 409)

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil({ !harness.http.isBusy }, timeout: 5)

    try await Task.sleep(nanoseconds: 100_000_000)
    XCTAssertEqual(harness.client.completeRelayCalls.count, 0)
    XCTAssertEqual(harness.client.fails.count, 0)
  }

  func testAuthFailureStopsRelayLane() async throws {
    let harness = Harness()
    harness.client.leaseFailure = .http(status: 401)

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.loop.status == .failed(code: "worker_authentication"))

    let leasesAfterStop = harness.client.leaseCalls
    try await Task.sleep(nanoseconds: 300_000_000)
    XCTAssertEqual(harness.client.leaseCalls, leasesAfterStop)
  }

  func testProtocolSubsetFailureBecomesRequiresReenrollment() async throws {
    let harness = Harness()
    harness.client.leaseFailure = .http(status: 400)

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.loop.status == .requiresReenrollment)

    let leasesAfterStop = harness.client.leaseCalls
    try await Task.sleep(nanoseconds: 300_000_000)
    XCTAssertEqual(harness.client.leaseCalls, leasesAfterStop)
  }

  func testMalformedLeaseBodyBecomesRequiresReenrollment() async throws {
    let harness = Harness()
    harness.client.leaseResponses = [.chat]
    harness.client.leaseRawBody = "{\"protocol_version\":\"speech-worker.v1\"}"

    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.loop.status == .requiresReenrollment)
  }

  func testLocalFailuresAreReportedWithCorrectRetryFlags() async throws {
    let cases: [(RelayHTTPError, String, Bool)] = [
      (.cannotConnect, "v1.relay_unreachable", true),
      (.timedOut, "v1.relay_unreachable", false),
      (.http(status: 502, excerpt: ""), "v1.relay_unreachable", true),
      (.http(status: 401, excerpt: ""), "v1.relay_auth", false),
      (.modelUnknown, "v1.relay_model_unknown", false),
      (.invalidResponse(excerpt: "junk"), "v1.relay_invalid_response", false),
      (.oversized, "v1.relay_invalid_response", false),
    ]
    for (error, code, retry) in cases {
      let harness = Harness()
      harness.client.leaseResponses = [.chat]
      harness.http.chatFailure = error

      harness.loop.start()
      try await waitUntil(harness.client.fails.count == 1, timeout: 5)
      harness.loop.stop()

      XCTAssertEqual(harness.client.fails.first?.errorCode, code)
      XCTAssertEqual(harness.client.fails.first?.retry, retry)
      XCTAssertEqual(harness.client.completeRelayCalls.count, 0)
    }
  }

  func testCompletionUploadFailureReturnsThroughBackoffWithoutSpoolWrites() async throws {
    let root = temporaryRoot("relay-no-spool")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let harness = Harness()
    harness.client.leaseResponses = [.chat]
    harness.client.completeRelayFailure = .transport

    harness.loop.start()
    try await waitUntil(harness.loop.status == .offline, timeout: 5)
    harness.loop.stop()

    XCTAssertEqual(harness.client.fails.count, 0)
    let spoolContents = try FileManager.default.contentsOfDirectory(atPath: paths.spoolRoot.path)
    XCTAssertTrue(spoolContents.isEmpty)
    XCTAssertEqual(
      try FileManager.default.contentsOfDirectory(atPath: paths.stateRoot.path).count, 0)
  }

  func testTTSKeepsLeasingWhileRelayCompletesAndAfterRelayStops() async throws {
    let root = temporaryRoot("relay-parallel-tts")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let configuration = SpeechWorkerConfiguration.default(paths: paths)
    let ttsClient = CountingTTSClient()
    let supervisor = ChatterboxSupervisor(
      paths: paths, configuration: configuration, process: FakeWorkerProcess(),
      identityProvider: FixedIdentityProvider(), portAllocator: FixedPortAllocator(),
      requester: RecordingHTTPRequester(responses: []), preparationOverride: {})
    let ttsLoop = LeaseLoop(
      client: ttsClient, configuration: configuration, paths: paths,
      journal: JobJournalStore(paths: paths),
      chatterbox: ChatterboxRenderer(
        supervisor: supervisor, configuration: configuration, paths: paths),
      sleepActivity: RecordingSleepActivity())

    let harness = Harness()
    harness.client.leaseResponses = [.chat]
    harness.http.chatHangs = true

    ttsLoop.start()
    defer { ttsLoop.stop() }
    try await waitUntil(ttsClient.leaseCalls >= 1)
    harness.loop.start()
    defer { harness.loop.stop() }
    try await waitUntil(harness.loop.status == .completing, timeout: 5)

    let duringCount = ttsClient.leaseCalls
    try await Task.sleep(nanoseconds: 1_500_000_000)
    XCTAssertGreaterThan(ttsClient.leaseCalls, duringCount)

    harness.loop.stop()
    try await waitUntil(harness.client.fails.count == 1, timeout: 5)
    XCTAssertEqual(harness.client.fails.first?.errorCode, "v1.relay_canceled")

    let afterStop = ttsClient.leaseCalls
    try await Task.sleep(nanoseconds: 1_200_000_000)
    XCTAssertGreaterThan(ttsClient.leaseCalls, afterStop)
    XCTAssertEqual(ttsLoop.status, .ready)
  }

  private func waitUntil(
    _ condition: @autoclosure @escaping () -> Bool, timeout: TimeInterval = 5,
    file: StaticString = #filePath, line: UInt = #line
  ) async throws {
    try await waitUntil({ condition() }, timeout: timeout, file: file, line: line)
  }

  private func waitUntil(
    _ condition: () -> Bool, timeout: TimeInterval = 5, file: StaticString = #filePath,
    line: UInt = #line
  ) async throws {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
      if condition() { return }
      try await Task.sleep(nanoseconds: 20_000_000)
    }
    if !condition() {
      XCTFail("condition not met within \(timeout)s", file: file, line: line)
    }
  }
}

@MainActor
private final class Harness {
  let client: RelayRecordingClient
  let http: FakeRelayHTTP
  let loop: RelayLoop

  init(heartbeatInterval: TimeInterval = 30) {
    client = RelayRecordingClient()
    http = FakeRelayHTTP()
    loop = RelayLoop(
      client: client, http: http, keyProvider: { "sk-test" },
      heartbeatInterval: heartbeatInterval)
  }
}

enum LeaseScript {
  case chat
  case models
}

final class RelayRecordingClient: WorkerClienting, @unchecked Sendable {
  private let lock = NSLock()
  private(set) var leaseCalls = 0
  private(set) var heartbeatCount = 0
  private(set) var fails: [(errorCode: String, retry: Bool)] = []
  private(set) var completeRelayCalls: [(jobID: String, result: Data)] = []
  private(set) var lastLeaseRequest: LeaseRequest?

  var leaseResponses: [LeaseScript] = []
  var leaseFailure: WorkerClientError?
  var leaseRawBody: String?
  var heartbeatFailure: WorkerClientError?
  var cancelRequestedOnHeartbeat: Int = 0
  var completeRelayFailure: WorkerClientError?

  func enroll(
    name _: String, capabilities _: [WorkerCapability], softwareVersion _: String,
    enrollmentToken _: String, llmRelayCapabilities _: [LLMRelayCapability]?
  ) async throws -> EnrollmentResponse {
    throw WorkerClientError.invalidResponse
  }

  func lease(_ request: LeaseRequest) async throws -> LeaseResponse? {
    let call = lock.withLock {
      leaseCalls += 1
      lastLeaseRequest = request
      return leaseResponses.isEmpty ? nil : leaseResponses.removeFirst()
    }
    if let leaseFailure { throw leaseFailure }
    guard let next = call else { return nil }
    if let raw = leaseRawBody {
      return try StrictJSON.decode(
        LeaseResponse.self, from: Data(raw.utf8), limit: LeaseResponse.maxEncodedBytes)
    }
    switch next {
    case .chat:
      return testRelayChatLease()
    case .models:
      return testRelayModelsLease()
    }
  }

  func heartbeat(jobID _: String, leaseToken _: String, attempt _: Int, progressPercent _: Int)
    async throws -> HeartbeatResponse
  {
    let count = lock.withLock {
      heartbeatCount += 1
      return heartbeatCount
    }
    if let heartbeatFailure { throw heartbeatFailure }
    let shouldCancel = cancelRequestedOnHeartbeat > 0 && count >= cancelRequestedOnHeartbeat
    let cancelFlag = shouldCancel ? "true" : "false"
    return try StrictJSON.decode(
      HeartbeatResponse.self,
      from: Data(
        "{\"protocol_version\":\"speech-worker.v1\",\"cancel_requested\":\(cancelFlag),\"lease_expires_at\":\"2026-09-01T12:00:00Z\",\"progress_percent\":50}"
          .utf8))
  }

  func completeSpeech(
    jobID _: String, metadata _: CompletionMetadata, audioURL _: URL
  ) async throws {
    throw WorkerClientError.invalidResponse
  }

  func completeRelay(jobID: String, attempt _: Int, leaseToken _: String, result: Data)
    async throws
  {
    lock.withLock { completeRelayCalls.append((jobID, result)) }
    if let completeRelayFailure { throw completeRelayFailure }
  }

  func fail(
    jobID _: String, leaseToken _: String, attempt _: Int, errorCode: String, retry: Bool
  ) async throws {
    lock.withLock { fails.append((errorCode, retry)) }
  }
}

final class FakeRelayHTTP: RelayHTTPClienting, @unchecked Sendable {
  private let lock = NSLock()
  private var busy = false

  var chatResult = testChatCompletion()
  var modelsResult: [String] = ["m1", "m2"]
  var chatFailure: RelayHTTPError?
  var chatDelay: TimeInterval = 0
  var chatHangs = false

  var isBusy: Bool {
    lock.withLock { busy }
  }

  func chatCompletion(_ lease: RelayChatLease, apiKey: String) async throws -> RelayChatCompletion {
    lock.withLock { busy = true }
    defer { lock.withLock { busy = false } }
    try await delayOrHang()
    if let chatFailure { throw chatFailure }
    return chatResult
  }

  func listModels(apiKey _: String) async throws -> [String] {
    lock.withLock { busy = true }
    defer { lock.withLock { busy = false } }
    try await delayOrHang()
    return modelsResult
  }

  private func delayOrHang() async throws {
    if chatHangs {
      while !Task.isCancelled {
        try await Task.sleep(nanoseconds: 20_000_000)
      }
      throw CancellationError()
    }
    if chatDelay > 0 {
      try await Task.sleep(nanoseconds: UInt64(chatDelay * 1_000_000_000))
    }
  }
}

final class CountingTTSClient: WorkerClienting, @unchecked Sendable {
  private let lock = NSLock()
  private(set) var leaseCalls = 0

  func enroll(
    name _: String, capabilities _: [WorkerCapability], softwareVersion _: String,
    enrollmentToken _: String, llmRelayCapabilities _: [LLMRelayCapability]?
  ) async throws -> EnrollmentResponse {
    throw WorkerClientError.invalidResponse
  }

  func lease(_: LeaseRequest) async throws -> LeaseResponse? {
    lock.withLock { leaseCalls += 1 }
    return nil
  }

  func heartbeat(jobID _: String, leaseToken _: String, attempt _: Int, progressPercent _: Int)
    async throws -> HeartbeatResponse
  {
    throw WorkerClientError.invalidResponse
  }

  func completeSpeech(jobID _: String, metadata _: CompletionMetadata, audioURL _: URL) async throws
  {
    throw WorkerClientError.invalidResponse
  }

  func completeRelay(jobID _: String, attempt _: Int, leaseToken _: String, result _: Data)
    async throws
  {
    throw WorkerClientError.invalidResponse
  }

  func fail(
    jobID _: String, leaseToken _: String, attempt _: Int, errorCode _: String, retry _: Bool
  ) async throws {}
}

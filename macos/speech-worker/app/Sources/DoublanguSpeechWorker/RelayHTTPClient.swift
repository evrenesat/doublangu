import Foundation

/// The validated local relay destination. Derived only from a `RelayConfig`
/// that already passed `validate()`; server jobs can never influence it.
public struct RelayTarget: Equatable, Sendable {
  public let baseURL: URL
  public let timeout: TimeInterval

  public init(baseURL: URL, timeout: TimeInterval) {
    self.baseURL = baseURL
    self.timeout = timeout
  }
}

public struct RelayChatCompletion: Equatable, Sendable {
  public let id: String
  public let reportedModel: String
  public let content: String
  public let finishReason: String
  public let usage: RelayUsage
  public let timing: RelayTiming

  public init(
    id: String, reportedModel: String, content: String, finishReason: String, usage: RelayUsage,
    timing: RelayTiming
  ) {
    self.id = id
    self.reportedModel = reportedModel
    self.content = content
    self.finishReason = finishReason
    self.usage = usage
    self.timing = timing
  }
}

/// Typed local transport failures. Mapping to worker failure codes is the
/// RelayLoop's responsibility (plan §5.8).
public enum RelayHTTPError: Error, Equatable, Sendable {
  case canceled
  case timedOut
  case cannotConnect
  case connectionLost
  case modelUnknown
  case http(status: Int, excerpt: String)
  case oversized
  case invalidResponse(excerpt: String)
  case apiKeyMissing

  public var excerpt: String {
    switch self {
    case .http(_, let excerpt): return excerpt
    case .invalidResponse(let excerpt): return excerpt
    default: return ""
    }
  }
}

public protocol RelayHTTPClienting: AnyObject, Sendable {
  func chatCompletion(_ request: RelayChatLease, apiKey: String) async throws -> RelayChatCompletion
  func listModels(apiKey: String) async throws -> [String]
}

final class RedirectRejector: NSObject, URLSessionDataDelegate, @unchecked Sendable {
  func urlSession(
    _: URLSession, task _: URLSessionTask, willPerformHTTPRedirection _: HTTPURLResponse,
    newRequest _: URLRequest, completionHandler: @escaping (URLRequest?) -> Void
  ) {
    // Reject redirects so the Authorization header can never follow to another
    // origin; the original 3xx response is delivered instead.
    completionHandler(nil)
  }
}

public final class RelayHTTPClient: RelayHTTPClienting, @unchecked Sendable {
  private let session: URLSession
  private let target: RelayTarget

  public init(target: RelayTarget) {
    self.target = target
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForRequest = target.timeout
    configuration.timeoutIntervalForResource = target.timeout
    configuration.waitsForConnectivity = false
    session = URLSession(
      configuration: configuration, delegate: RedirectRejector(), delegateQueue: nil)
  }

  deinit { session.finishTasksAndInvalidate() }

  public func chatCompletion(_ lease: RelayChatLease, apiKey: String) async throws
    -> RelayChatCompletion
  {
    guard !apiKey.isEmpty else { throw RelayHTTPError.apiKeyMissing }
    let body = try StrictJSON.encode(
      OpenAIChatRequestBody(
        model: lease.model, messages: lease.messages, stream: false,
        maxTokens: lease.options.maxOutputTokens,
        temperature: Double(lease.options.temperatureMilli) / 1_000.0,
        responseFormat: lease.responseFormat))
    var urlRequest = URLRequest(url: target.baseURL.appendingPathComponent("chat/completions"))
    urlRequest.httpMethod = "POST"
    urlRequest.httpBody = body
    urlRequest.timeoutInterval = target.timeout
    applyHeaders(to: &urlRequest, apiKey: apiKey)
    let (data, status) = try await boundedRead(urlRequest)
    guard status == 200 else {
      throw Self.classify(status: status, body: data, model: lease.model, apiKey: apiKey)
    }
    return try Self.decodeChatCompletion(data)
  }

  public func listModels(apiKey: String) async throws -> [String] {
    guard !apiKey.isEmpty else { throw RelayHTTPError.apiKeyMissing }
    var urlRequest = URLRequest(url: target.baseURL.appendingPathComponent("models"))
    urlRequest.httpMethod = "GET"
    urlRequest.timeoutInterval = target.timeout
    applyHeaders(to: &urlRequest, apiKey: apiKey)
    let (data, status) = try await boundedRead(urlRequest)
    guard status == 200 else {
      throw Self.classify(status: status, body: data, model: nil, apiKey: apiKey)
    }
    return try Self.decodeModels(data)
  }

  private func applyHeaders(to request: inout URLRequest, apiKey: String) {
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    request.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
  }

  /// Reads at most 2 MiB + 1 byte; larger bodies are rejected as oversized.
  private func boundedRead(_ request: URLRequest) async throws -> (Data, Int) {
    do {
      let (bytes, response) = try await session.bytes(for: request)
      guard let http = response as? HTTPURLResponse else {
        throw RelayHTTPError.invalidResponse(excerpt: "")
      }
      var data = Data()
      data.reserveCapacity(RelayLimits.maxPayloadBytes)
      var count = 0
      for try await byte in bytes {
        count += 1
        if count > RelayLimits.maxPayloadBytes { throw RelayHTTPError.oversized }
        data.append(byte)
      }
      return (data, http.statusCode)
    } catch let error as RelayHTTPError {
      throw error
    } catch is CancellationError {
      throw RelayHTTPError.canceled
    } catch let error as URLError {
      throw Self.mapURLError(error)
    } catch {
      throw RelayHTTPError.invalidResponse(excerpt: "")
    }
  }

  static func mapURLError(_ error: URLError) -> RelayHTTPError {
    switch error.code {
    case .cancelled: return .canceled
    case .timedOut: return .timedOut
    case .networkConnectionLost: return .connectionLost
    default: return .cannotConnect
    }
  }

  /// Deliberately narrow unknown-model recognition per plan §5.8: a structured
  /// OpenAI-style `error.code`, or a 400/404 message naming the exact model id
  /// plus "not found"/"unknown model". Never uploads the provider error body.
  static func classify(status: Int, body: Data, model: String?, apiKey: String) -> RelayHTTPError {
    if status == 401 || status == 403 { return .http(status: status, excerpt: "") }
    let excerpt = Self.sanitize(body: body, apiKey: apiKey)
    if case .object(let root)? = try? JSONDecoder().decode(RelayJSONValue.self, from: body),
      case .object(let error)? = root["error"]
    {
      if case .string(let code)? = error["code"],
        ["model_not_found", "unknown_model", "model_not_found_error"].contains(code)
      {
        return .modelUnknown
      }
      if status == 400 || status == 404, let model, !model.isEmpty,
        case .string(let message)? = error["message"], message.contains(model),
        message.contains("not found") || message.contains("unknown model")
      {
        return .modelUnknown
      }
    }
    return .http(status: status, excerpt: excerpt)
  }

  static func decodeChatCompletion(_ data: Data) throws -> RelayChatCompletion {
    func invalid(_ reason: String) -> RelayHTTPError {
      .invalidResponse(excerpt: reason)
    }
    guard case .object(let root)? = try? JSONDecoder().decode(RelayJSONValue.self, from: data)
    else { throw invalid("not_an_object") }
    guard let choices = root["choices"]?.arrayValue else { throw invalid("choices_missing") }
    guard choices.count == 1 else { throw invalid("choices_count_\(choices.count)") }
    guard case .object(let choice)? = choices.first else { throw invalid("choice_not_object") }
    guard case .object(let message)? = choice["message"] else { throw invalid("message_missing") }
    guard case .string(let content)? = message["content"], !content.isEmpty else {
      throw invalid("content_empty")
    }
    guard content.utf8.count <= RelayLimits.maxContentBytes else {
      throw invalid("content_oversized")
    }
    let finishReason: String
    switch choice["finish_reason"] {
    case .string("stop"): finishReason = "stop"
    case .string(""), .null, .none: finishReason = ""
    default: throw invalid("finish_reason_invalid")
    }
    func bounded(_ value: RelayJSONValue?, fallback: String) throws -> String {
      switch value {
      case .string(let string):
        guard string.utf8.count <= RelayLimits.maxModelBytes else {
          throw invalid("string_oversized")
        }
        return string
      case .null, .none: return fallback
      default: throw invalid("string_not_string")
      }
    }
    let id = try bounded(root["id"], fallback: "")
    let reportedModel = try bounded(root["model"], fallback: "")
    var usage = RelayUsage(promptTokens: 0, completionTokens: 0, totalTokens: 0)
    var usageTiming = RelayTiming()
    if case .object(let usageObject)? = root["usage"] {
      usage = try Self.decodeUsage(usageObject)
      usageTiming = RelayTiming.copyKnownKeys(from: usageObject)
    }
    let timing = RelayTiming.copyKnownKeys(from: root).merging(usageTiming)
    return RelayChatCompletion(
      id: id, reportedModel: reportedModel, content: content, finishReason: finishReason,
      usage: usage, timing: timing)
  }

  private static func decodeUsage(_ object: [String: RelayJSONValue]) throws -> RelayUsage {
    func token(_ key: String) throws -> Int {
      guard case .number(let value)? = object[key] else {
        throw RelayHTTPError.invalidResponse(excerpt: "usage_missing_\(key)")
      }
      guard value.isFinite, value >= 0, value == value.rounded() else {
        throw RelayHTTPError.invalidResponse(excerpt: "usage_invalid_\(key)")
      }
      return Int(value)
    }
    return RelayUsage(
      promptTokens: try token("prompt_tokens"),
      completionTokens: try token("completion_tokens"),
      totalTokens: try token("total_tokens"))
  }

  static func decodeModels(_ data: Data) throws -> [String] {
    guard case .object(let root)? = try? JSONDecoder().decode(RelayJSONValue.self, from: data),
      let entries = root["data"]?.arrayValue
    else { throw RelayHTTPError.invalidResponse(excerpt: "models_not_list") }
    var seen = Set<String>()
    var models: [String] = []
    for entry in entries {
      guard case .object(let item) = entry, case .string(let id) = item["id"],
        !id.isEmpty, id.utf8.count <= RelayLimits.maxModelBytes
      else { throw RelayHTTPError.invalidResponse(excerpt: "models_entry_invalid") }
      if seen.insert(id).inserted {
        if models.count == RelayLimits.maxModels { break }
        models.append(id)
      }
    }
    return models
  }

  /// Bounded, API-key-redacted excerpt for local diagnostics only.
  static func sanitize(body: Data, apiKey: String, limit: Int = 512) -> String {
    var text = String(decoding: body.prefix(limit), as: UTF8.self)
    if !apiKey.isEmpty { text = text.replacingOccurrences(of: apiKey, with: "[redacted]") }
    return text
  }
}

struct OpenAIChatRequestBody: Encodable {
  let model: String
  let messages: [RelayChatMessage]
  let stream: Bool
  let maxTokens: Int
  let temperature: Double
  let responseFormat: RelayResponseFormat

  private enum CodingKeys: String, CodingKey {
    case model, messages, stream
    case maxTokens = "max_tokens"
    case temperature
    case responseFormat = "response_format"
  }
}

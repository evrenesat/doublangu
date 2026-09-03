import Foundation

/// Shared relay wire bounds mirroring backend plan §5.1.
public enum RelayLimits {
  public static let maxPayloadBytes = 2_097_152
  public static let maxContentBytes = 1_048_576
  public static let maxModelBytes = 256
  public static let maxMessages = 16
  public static let maxModels = 4096
  public static let knownTimingKeys: Set<String> = [
    "model_load_duration", "time_to_first_token", "prompt_eval_duration",
    "generation_duration", "total_time",
  ]
}

public func isValidULID(_ value: String) -> Bool {
  value.count == 26 && value.allSatisfy { "0123456789ABCDEFGHJKMNPQRSTVWXYZ".contains($0) }
}

/// Minimal recursive JSON value used for pass-through fields such as the
/// `json_schema.schema` object and lenient OpenAI-compatible local responses.
public enum RelayJSONValue: Codable, Equatable, Sendable {
  case null
  case bool(Bool)
  case number(Double)
  case string(String)
  case array([RelayJSONValue])
  case object([String: RelayJSONValue])

  public init(from decoder: Decoder) throws {
    let container = try decoder.singleValueContainer()
    if let value = try? container.decode(Bool.self) {
      self = .bool(value)
    } else if let value = try? container.decode(Double.self) {
      self = .number(value)
    } else if let value = try? container.decode(String.self) {
      self = .string(value)
    } else if let value = try? container.decode([String: RelayJSONValue].self) {
      self = .object(value)
    } else if let value = try? container.decode([RelayJSONValue].self) {
      self = .array(value)
    } else if container.decodeNil() {
      self = .null
    } else {
      throw ProtocolError.invalidJSON
    }
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.singleValueContainer()
    switch self {
    case .null: try container.encodeNil()
    case .bool(let value): try container.encode(value)
    case .number(let value): try container.encode(value)
    case .string(let value): try container.encode(value)
    case .array(let value): try container.encode(value)
    case .object(let value): try container.encode(value)
    }
  }

  public var objectValue: [String: RelayJSONValue]? {
    if case .object(let value) = self { return value }
    return nil
  }

  public var arrayValue: [RelayJSONValue]? {
    if case .array(let value) = self { return value }
    return nil
  }

  public var stringValue: String? {
    if case .string(let value) = self { return value }
    return nil
  }
}

public struct LLMRelayCapability: Codable, Equatable, Sendable {
  public let maxCompletionBytes: Int64

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case maxCompletionBytes = "max_completion_bytes"
  }

  public init(maxCompletionBytes: Int64 = WorkerConstants.relayMaxCompletionBytes) {
    self.maxCompletionBytes = maxCompletionBytes
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    maxCompletionBytes = try c.decode(Int64.self, forKey: .maxCompletionBytes)
    guard maxCompletionBytes > 0, maxCompletionBytes <= Int64(RelayLimits.maxPayloadBytes) else {
      throw ProtocolError.invalidValue("relay_capability")
    }
  }
}

public struct RelayChatMessage: Codable, Equatable, Sendable {
  public let role: String
  public let content: String

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case role, content
  }

  public init(role: String, content: String) {
    self.role = role
    self.content = content
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    role = try c.decode(String.self, forKey: .role)
    content = try c.decode(String.self, forKey: .content)
    try validate()
  }

  public func validate() throws {
    guard ["user", "assistant"].contains(role), !content.isEmpty else {
      throw ProtocolError.invalidValue("relay_message")
    }
  }
}

public struct RelayChatOptions: Codable, Equatable, Sendable {
  public let temperatureMilli: Int
  public let maxOutputTokens: Int

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case temperatureMilli = "temperature_milli"
    case maxOutputTokens = "max_output_tokens"
  }

  public init(temperatureMilli: Int, maxOutputTokens: Int) {
    self.temperatureMilli = temperatureMilli
    self.maxOutputTokens = maxOutputTokens
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    temperatureMilli = try c.decode(Int.self, forKey: .temperatureMilli)
    maxOutputTokens = try c.decode(Int.self, forKey: .maxOutputTokens)
    try validate()
  }

  public func validate() throws {
    guard (0...2_000).contains(temperatureMilli), (1_024...65_536).contains(maxOutputTokens)
    else { throw ProtocolError.invalidValue("relay_options") }
  }
}

public struct RelayCompletionLimits: Codable, Equatable, Sendable {
  public let maxCompletionBytes: Int64

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case maxCompletionBytes = "max_completion_bytes"
  }

  public init(maxCompletionBytes: Int64) {
    self.maxCompletionBytes = maxCompletionBytes
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    maxCompletionBytes = try c.decode(Int64.self, forKey: .maxCompletionBytes)
    guard maxCompletionBytes > 0, maxCompletionBytes <= Int64(RelayLimits.maxPayloadBytes) else {
      throw ProtocolError.invalidValue("relay_limits")
    }
  }
}

public struct RelayJSONSchema: Codable, Equatable, Sendable {
  public let name: String
  public let strict: Bool
  public let schema: RelayJSONValue

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case name, strict, schema
  }

  public init(name: String, strict: Bool, schema: RelayJSONValue) {
    self.name = name
    self.strict = strict
    self.schema = schema
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    name = try c.decode(String.self, forKey: .name)
    strict = try c.decode(Bool.self, forKey: .strict)
    schema = try c.decode(RelayJSONValue.self, forKey: .schema)
    try validate()
  }

  public func validate() throws {
    guard !name.isEmpty, name.utf8.count <= RelayLimits.maxModelBytes, schema.objectValue != nil
    else { throw ProtocolError.invalidValue("relay_json_schema") }
  }
}

public struct RelayResponseFormat: Codable, Equatable, Sendable {
  public let type: String
  public let jsonSchema: RelayJSONSchema

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case type
    case jsonSchema = "json_schema"
  }

  public init(jsonSchema: RelayJSONSchema) {
    type = "json_schema"
    self.jsonSchema = jsonSchema
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    type = try c.decode(String.self, forKey: .type)
    jsonSchema = try c.decode(RelayJSONSchema.self, forKey: .jsonSchema)
    try validate()
  }

  public func validate() throws {
    guard type == "json_schema" else { throw ProtocolError.invalidValue("relay_response_format") }
    try jsonSchema.validate()
  }
}

public struct RelayChatLease: Codable, Equatable, Sendable {
  public let requestID: String
  public let model: String
  public let messages: [RelayChatMessage]
  public let responseFormat: RelayResponseFormat
  public let options: RelayChatOptions
  public let limits: RelayCompletionLimits

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case requestID = "request_id"
    case model, messages
    case responseFormat = "response_format"
    case options, limits
  }

  public init(
    requestID: String, model: String, messages: [RelayChatMessage],
    responseFormat: RelayResponseFormat, options: RelayChatOptions,
    limits: RelayCompletionLimits
  ) {
    self.requestID = requestID
    self.model = model
    self.messages = messages
    self.responseFormat = responseFormat
    self.options = options
    self.limits = limits
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    requestID = try c.decode(String.self, forKey: .requestID)
    model = try c.decode(String.self, forKey: .model)
    messages = try c.decode([RelayChatMessage].self, forKey: .messages)
    responseFormat = try c.decode(RelayResponseFormat.self, forKey: .responseFormat)
    options = try c.decode(RelayChatOptions.self, forKey: .options)
    limits = try c.decode(RelayCompletionLimits.self, forKey: .limits)
    try validate()
  }

  public func validate() throws {
    guard isValidULID(requestID), !model.isEmpty, model.utf8.count <= RelayLimits.maxModelBytes,
      (1...RelayLimits.maxMessages).contains(messages.count)
    else { throw ProtocolError.invalidValue("relay_chat_lease") }
    for message in messages { try message.validate() }
    try responseFormat.validate()
    try options.validate()
  }
}

public struct RelayListModelsLease: Codable, Equatable, Sendable {
  public let requestID: String
  public let limits: RelayCompletionLimits

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case requestID = "request_id"
    case limits
  }

  public init(requestID: String, limits: RelayCompletionLimits) {
    self.requestID = requestID
    self.limits = limits
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    requestID = try c.decode(String.self, forKey: .requestID)
    limits = try c.decode(RelayCompletionLimits.self, forKey: .limits)
    try validate()
  }

  public func validate() throws {
    guard isValidULID(requestID) else { throw ProtocolError.invalidValue("relay_models_lease") }
  }
}

/// The `relay` lease object: the request payload minus
/// `protocol_version`/`operation`, discriminated by the lease `operation`.
/// Decoding therefore happens through `LeaseResponse`, which knows the
/// operation; a standalone `init(from:)` cannot discriminate and fails.
public enum RelayLease: Codable, Equatable, Sendable {
  case chat(RelayChatLease)
  case models(RelayListModelsLease)

  public var operation: String {
    switch self {
    case .chat: return "chat_completion"
    case .models: return "list_models"
    }
  }

  public var requestID: String {
    switch self {
    case .chat(let lease): return lease.requestID
    case .models(let lease): return lease.requestID
    }
  }

  public var maxCompletionBytes: Int64 {
    switch self {
    case .chat(let lease): return lease.limits.maxCompletionBytes
    case .models(let lease): return lease.limits.maxCompletionBytes
    }
  }

  public init(from decoder: Decoder) throws {
    throw ProtocolError.invalidValue("relay_lease_requires_operation")
  }

  public func encode(to encoder: Encoder) throws {
    switch self {
    case .chat(let lease): try lease.encode(to: encoder)
    case .models(let lease): try lease.encode(to: encoder)
    }
  }
}

public struct RelayUsage: Codable, Equatable, Sendable {
  public let promptTokens: Int
  public let completionTokens: Int
  public let totalTokens: Int

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case promptTokens = "prompt_tokens"
    case completionTokens = "completion_tokens"
    case totalTokens = "total_tokens"
  }

  public init(promptTokens: Int, completionTokens: Int, totalTokens: Int) {
    self.promptTokens = promptTokens
    self.completionTokens = completionTokens
    self.totalTokens = totalTokens
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    promptTokens = try c.decode(Int.self, forKey: .promptTokens)
    completionTokens = try c.decode(Int.self, forKey: .completionTokens)
    totalTokens = try c.decode(Int.self, forKey: .totalTokens)
    guard promptTokens >= 0, completionTokens >= 0, totalTokens >= 0 else {
      throw ProtocolError.invalidValue("relay_usage")
    }
  }
}

/// Only the known OMLX timing keys are copied, and only when they decode as
/// finite JSON numbers; nil fields are omitted from the encoded result.
public struct RelayTiming: Codable, Equatable, Sendable {
  public var modelLoadDuration: Double?
  public var timeToFirstToken: Double?
  public var promptEvalDuration: Double?
  public var generationDuration: Double?
  public var totalTime: Double?

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case modelLoadDuration = "model_load_duration"
    case timeToFirstToken = "time_to_first_token"
    case promptEvalDuration = "prompt_eval_duration"
    case generationDuration = "generation_duration"
    case totalTime = "total_time"
  }

  public init() {}

  public init(from decoder: Decoder) throws {
    // Every field is optional, so an empty timing object stays decodable.
    try StrictCoding.checkKeys(
      decoder, CodingKeys.self, optional: Set(CodingKeys.allCases.map(\.stringValue)))
    let c = try decoder.container(keyedBy: CodingKeys.self)
    modelLoadDuration = try c.decodeIfPresent(Double.self, forKey: .modelLoadDuration)
    timeToFirstToken = try c.decodeIfPresent(Double.self, forKey: .timeToFirstToken)
    promptEvalDuration = try c.decodeIfPresent(Double.self, forKey: .promptEvalDuration)
    generationDuration = try c.decodeIfPresent(Double.self, forKey: .generationDuration)
    totalTime = try c.decodeIfPresent(Double.self, forKey: .totalTime)
  }

  public static func copyKnownKeys(from object: [String: RelayJSONValue]) -> RelayTiming {
    var timing = RelayTiming()
    func finite(_ key: String) -> Double? {
      guard case .number(let value)? = object[key], value.isFinite else { return nil }
      return value
    }
    timing.modelLoadDuration = finite("model_load_duration")
    timing.timeToFirstToken = finite("time_to_first_token")
    timing.promptEvalDuration = finite("prompt_eval_duration")
    timing.generationDuration = finite("generation_duration")
    timing.totalTime = finite("total_time")
    return timing
  }

  public func merging(_ other: RelayTiming) -> RelayTiming {
    var merged = self
    if merged.modelLoadDuration == nil { merged.modelLoadDuration = other.modelLoadDuration }
    if merged.timeToFirstToken == nil { merged.timeToFirstToken = other.timeToFirstToken }
    if merged.promptEvalDuration == nil { merged.promptEvalDuration = other.promptEvalDuration }
    if merged.generationDuration == nil { merged.generationDuration = other.generationDuration }
    if merged.totalTime == nil { merged.totalTime = other.totalTime }
    return merged
  }
}

public struct RelayChatResult: Codable, Equatable, Sendable {
  public let requestID: String
  public let content: String
  public let reportedModel: String
  public let providerRequestID: String
  public let finishReason: String
  public let usage: RelayUsage
  public let timing: RelayTiming

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case requestID = "request_id"
    case content
    case reportedModel = "reported_model"
    case providerRequestID = "provider_request_id"
    case finishReason = "finish_reason"
    case usage, timing
  }

  public init(
    requestID: String, content: String, reportedModel: String, providerRequestID: String,
    finishReason: String, usage: RelayUsage, timing: RelayTiming
  ) {
    self.requestID = requestID
    self.content = content
    self.reportedModel = reportedModel
    self.providerRequestID = providerRequestID
    self.finishReason = finishReason
    self.usage = usage
    self.timing = timing
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    requestID = try c.decode(String.self, forKey: .requestID)
    content = try c.decode(String.self, forKey: .content)
    reportedModel = try c.decode(String.self, forKey: .reportedModel)
    providerRequestID = try c.decode(String.self, forKey: .providerRequestID)
    finishReason = try c.decode(String.self, forKey: .finishReason)
    usage = try c.decode(RelayUsage.self, forKey: .usage)
    timing = try c.decode(RelayTiming.self, forKey: .timing)
    try validate()
  }

  public func validate() throws {
    guard isValidULID(requestID), !content.isEmpty,
      content.utf8.count <= RelayLimits.maxContentBytes,
      reportedModel.utf8.count <= RelayLimits.maxModelBytes,
      providerRequestID.utf8.count <= RelayLimits.maxModelBytes,
      finishReason == "stop" || finishReason.isEmpty
    else { throw ProtocolError.invalidValue("relay_chat_result") }
  }
}

public struct RelayModelsResult: Codable, Equatable, Sendable {
  public let requestID: String
  public let models: [String]

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case requestID = "request_id"
    case models
  }

  public init(requestID: String, models: [String]) {
    self.requestID = requestID
    self.models = models
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    requestID = try c.decode(String.self, forKey: .requestID)
    models = try c.decode([String].self, forKey: .models)
    try validate()
  }

  public func validate() throws {
    guard isValidULID(requestID), models.count <= RelayLimits.maxModels,
      models.allSatisfy({ !$0.isEmpty && $0.utf8.count <= RelayLimits.maxModelBytes }),
      Set(models).count == models.count
    else { throw ProtocolError.invalidValue("relay_models_result") }
  }
}

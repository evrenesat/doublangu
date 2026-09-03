import Foundation

public enum ProtocolError: Error, Equatable, LocalizedError, Sendable {
  case invalidJSON
  case unexpectedKeys
  case invalidValue(String)
  case response(status: Int, body: String)

  public var errorDescription: String? {
    switch self {
    case .invalidJSON:
      "protocol_invalid_json"
    case .unexpectedKeys:
      "protocol_unexpected_keys"
    case .invalidValue:
      "protocol_invalid_value"
    case .response(let status, _):
      "protocol_http_\(status)"
    }
  }
}

public struct DynamicCodingKey: CodingKey, Hashable, Sendable {
  public let stringValue: String
  public let intValue: Int?

  public init?(stringValue: String) {
    self.stringValue = stringValue
    intValue = nil
  }

  public init?(intValue: Int) {
    stringValue = String(intValue)
    self.intValue = intValue
  }
}

enum StrictCoding {
  static func checkKeys<D: Decoder>(_ decoder: D, expected: Set<String>, optional: Set<String> = [])
    throws
  {
    let container = try decoder.container(keyedBy: DynamicCodingKey.self)
    let actual = Set(container.allKeys.map(\.stringValue))
    guard actual.isSubset(of: expected), expected.subtracting(optional).isSubset(of: actual) else {
      throw ProtocolError.unexpectedKeys
    }
  }

  static func checkKeys<D: Decoder, K: CodingKey & CaseIterable>(
    _ decoder: D, _ keys: K.Type, optional: Set<String> = []
  ) throws
  where K.AllCases.Element == K {
    try checkKeys(decoder, expected: Set(keys.allCases.map(\.stringValue)), optional: optional)
  }
}

public enum JSONDuplicateKeyDetector {
  public static func containsDuplicateKeys(_ data: Data) -> Bool {
    var scanner = Scanner(bytes: Array(data))
    guard scanner.parseValue(), scanner.isAtEnd else { return true }
    return scanner.duplicate
  }

  private struct Scanner {
    let bytes: [UInt8]
    var index = 0
    var duplicate = false

    var isAtEnd: Bool {
      var copy = self
      copy.skipWhitespace()
      return copy.index == copy.bytes.count
    }

    init(bytes: [UInt8]) { self.bytes = bytes }

    mutating func parseValue() -> Bool {
      skipWhitespace()
      guard index < bytes.count else { return false }
      switch bytes[index] {
      case 123:
        return parseObject()
      case 91:
        return parseArray()
      case 34:
        return parseString() != nil
      case 116:
        return consume([116, 114, 117, 101])
      case 102:
        return consume([102, 97, 108, 115, 101])
      case 110:
        return consume([110, 117, 108, 108])
      default:
        return parseNumber()
      }
    }

    mutating func parseObject() -> Bool {
      guard consumeByte(123) else { return false }
      skipWhitespace()
      if consumeByte(125) { return true }
      var keys = Set<String>()
      while index < bytes.count {
        skipWhitespace()
        guard let key = parseString() else { return false }
        if !keys.insert(key).inserted { duplicate = true }
        skipWhitespace()
        guard consumeByte(58), parseValue() else { return false }
        skipWhitespace()
        if consumeByte(125) { return true }
        guard consumeByte(44) else { return false }
      }
      return false
    }

    mutating func parseArray() -> Bool {
      guard consumeByte(91) else { return false }
      skipWhitespace()
      if consumeByte(93) { return true }
      while index < bytes.count {
        guard parseValue() else { return false }
        skipWhitespace()
        if consumeByte(93) { return true }
        guard consumeByte(44) else { return false }
      }
      return false
    }

    mutating func parseString() -> String? {
      guard consumeByte(34) else { return nil }
      let start = index - 1
      var escaped = false
      while index < bytes.count {
        let byte = bytes[index]
        index += 1
        if byte == 34 && !escaped {
          let raw = Data(bytes[start..<index])
          return try? JSONDecoder().decode(String.self, from: raw)
        }
        if byte == 92 && !escaped {
          escaped = true
        } else {
          escaped = false
        }
      }
      return nil
    }

    mutating func parseNumber() -> Bool {
      let start = index
      while index < bytes.count, ![9, 10, 13, 32, 44, 93, 125].contains(bytes[index]) {
        index += 1
      }
      return index > start
    }

    mutating func consume(_ value: [UInt8]) -> Bool {
      guard bytes.dropFirst(index).prefix(value.count).elementsEqual(value) else { return false }
      index += value.count
      return true
    }

    mutating func consumeByte(_ value: UInt8) -> Bool {
      guard index < bytes.count, bytes[index] == value else { return false }
      index += 1
      return true
    }

    mutating func skipWhitespace() {
      while index < bytes.count, [9, 10, 13, 32].contains(bytes[index]) { index += 1 }
    }
  }
}

public enum StrictJSON {
  public static func decode<T: Decodable>(
    _ type: T.Type, from data: Data, limit: Int = 1_048_576
  ) throws -> T {
    guard data.count <= limit, !JSONDuplicateKeyDetector.containsDuplicateKeys(data) else {
      throw ProtocolError.invalidJSON
    }
    do {
      return try JSONDecoder().decode(type, from: data)
    } catch let error as ProtocolError {
      throw error
    } catch {
      throw ProtocolError.invalidJSON
    }
  }

  public static func encode<T: Encodable>(_ value: T) throws -> Data {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]
    return try encoder.encode(value)
  }
}

public struct WorkerCapability: Codable, Equatable, Sendable {
  public let engine: String
  public let languages: [String]
  public let unitKinds: [String]
  public let maxBytes: Int64
  public let maxDurationMS: Int64

  public init(
    engine: String, languages: [String], unitKinds: [String], maxBytes: Int64, maxDurationMS: Int64
  ) {
    self.engine = engine
    self.languages = languages
    self.unitKinds = unitKinds
    self.maxBytes = maxBytes
    self.maxDurationMS = maxDurationMS
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case engine, languages
    case unitKinds = "unit_kinds"
    case maxBytes = "max_bytes"
    case maxDurationMS = "max_duration_ms"
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    engine = try c.decode(String.self, forKey: .engine)
    languages = try c.decode([String].self, forKey: .languages)
    unitKinds = try c.decode([String].self, forKey: .unitKinds)
    maxBytes = try c.decode(Int64.self, forKey: .maxBytes)
    maxDurationMS = try c.decode(Int64.self, forKey: .maxDurationMS)
    try validate()
  }

  public func validate() throws {
    guard ["avspeech", "chatterbox"].contains(engine), !languages.isEmpty, !unitKinds.isEmpty,
      languages.count <= 32, unitKinds.count <= 8, languages.allSatisfy({ !$0.isEmpty }),
      unitKinds.allSatisfy({ ["word", "phrase", "sentence", "*"].contains($0) }),
      maxBytes >= 0, maxDurationMS >= 0
    else { throw ProtocolError.invalidValue("capability") }
  }
}

public struct SpeechProfile: Codable, Equatable, Sendable {
  public let id: String?
  public let engine: String
  public let modelRevision: String
  public let language: String
  public let voiceIdentifier: String
  public let referenceAudioHash: String
  public let speedMilli: Int
  public let pitchCents: Int
  public let mappingVersion: String
  public let mimeType: String
  public let codec: String
  public let sampleRateHz: Int
  public let channels: Int
  public let active: Bool
  public let createdAt: String?
  public let updatedAt: String?

  public init(
    id: String? = nil, engine: String, modelRevision: String, language: String,
    voiceIdentifier: String, referenceAudioHash: String, speedMilli: Int, pitchCents: Int,
    mappingVersion: String, mimeType: String = "audio/mp4", codec: String = "aac-lc",
    sampleRateHz: Int = 24000, channels: Int = 1, active: Bool = true,
    createdAt: String? = nil, updatedAt: String? = nil
  ) {
    self.id = id
    self.engine = engine
    self.modelRevision = modelRevision
    self.language = language
    self.voiceIdentifier = voiceIdentifier
    self.referenceAudioHash = referenceAudioHash
    self.speedMilli = speedMilli
    self.pitchCents = pitchCents
    self.mappingVersion = mappingVersion
    self.mimeType = mimeType
    self.codec = codec
    self.sampleRateHz = sampleRateHz
    self.channels = channels
    self.active = active
    self.createdAt = createdAt
    self.updatedAt = updatedAt
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case id, engine
    case modelRevision = "model_revision"
    case language
    case voiceIdentifier = "voice_identifier"
    case referenceAudioHash = "reference_audio_hash"
    case speedMilli = "speed_milli"
    case pitchCents = "pitch_cents"
    case mappingVersion = "mapping_version"
    case mimeType = "mime_type"
    case codec
    case sampleRateHz = "sample_rate_hz"
    case channels, active
    case createdAt = "created_at"
    case updatedAt = "updated_at"
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(
      decoder, CodingKeys.self, optional: ["id", "created_at", "updated_at"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    id = try c.decodeIfPresent(String.self, forKey: .id)
    engine = try c.decode(String.self, forKey: .engine)
    modelRevision = try c.decode(String.self, forKey: .modelRevision)
    language = try c.decode(String.self, forKey: .language)
    voiceIdentifier = try c.decode(String.self, forKey: .voiceIdentifier)
    referenceAudioHash = try c.decode(String.self, forKey: .referenceAudioHash)
    speedMilli = try c.decode(Int.self, forKey: .speedMilli)
    pitchCents = try c.decode(Int.self, forKey: .pitchCents)
    mappingVersion = try c.decode(String.self, forKey: .mappingVersion)
    mimeType = try c.decode(String.self, forKey: .mimeType)
    codec = try c.decode(String.self, forKey: .codec)
    sampleRateHz = try c.decode(Int.self, forKey: .sampleRateHz)
    channels = try c.decode(Int.self, forKey: .channels)
    active = try c.decode(Bool.self, forKey: .active)
    createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt)
    updatedAt = try c.decodeIfPresent(String.self, forKey: .updatedAt)
    try validate()
  }

  public func validate() throws {
    guard ["avspeech", "chatterbox"].contains(engine), !modelRevision.isEmpty, language == "nl",
      !voiceIdentifier.isEmpty, speedMilli > 0, !mappingVersion.isEmpty, mimeType == "audio/mp4",
      codec == "aac-lc", sampleRateHz == 24000, channels == 1
    else { throw ProtocolError.invalidValue("profile") }
    if engine == "avspeech" && !referenceAudioHash.isEmpty {
      throw ProtocolError.invalidValue("avspeech_reference")
    }
    if engine == "chatterbox" && referenceAudioHash.count != 64 {
      throw ProtocolError.invalidValue("chatterbox_reference")
    }
  }

  public func matchesByteAffecting(_ other: SpeechProfile) -> Bool {
    engine == other.engine && modelRevision == other.modelRevision && language == other.language
      && voiceIdentifier == other.voiceIdentifier && referenceAudioHash == other.referenceAudioHash
      && speedMilli == other.speedMilli && pitchCents == other.pitchCents
      && mappingVersion == other.mappingVersion && mimeType == other.mimeType
      && codec == other.codec
      && sampleRateHz == other.sampleRateHz && channels == other.channels && active == other.active
  }
}

public struct WorkerInfo: Codable, Equatable, Sendable {
  public let id: String
  public let name: String
  public let protocolVersion: String
  public let revokedAt: String
  public let lastSeenAt: String
  public let capabilities: [WorkerCapability]
  public let softwareVersion: String
  public let llmRelayCapabilities: [LLMRelayCapability]?
  public let relayLastSeenAt: String?
  public let createdAt: String
  public let updatedAt: String

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case id, name
    case protocolVersion = "protocol_version"
    case revokedAt = "revoked_at"
    case lastSeenAt = "last_seen_at"
    case capabilities
    case softwareVersion = "software_version"
    case llmRelayCapabilities = "llm_relay_capabilities"
    case relayLastSeenAt = "relay_last_seen_at"
    case createdAt = "created_at"
    case updatedAt = "updated_at"
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(
      decoder, CodingKeys.self, optional: ["llm_relay_capabilities", "relay_last_seen_at"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    id = try c.decode(String.self, forKey: .id)
    name = try c.decode(String.self, forKey: .name)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    revokedAt = try c.decode(String.self, forKey: .revokedAt)
    lastSeenAt = try c.decode(String.self, forKey: .lastSeenAt)
    capabilities = try c.decode([WorkerCapability].self, forKey: .capabilities)
    softwareVersion = try c.decode(String.self, forKey: .softwareVersion)
    llmRelayCapabilities = try c.decodeIfPresent(
      [LLMRelayCapability].self, forKey: .llmRelayCapabilities)
    relayLastSeenAt = try c.decodeIfPresent(String.self, forKey: .relayLastSeenAt)
    createdAt = try c.decode(String.self, forKey: .createdAt)
    updatedAt = try c.decode(String.self, forKey: .updatedAt)
  }
}

public struct EnrollmentResponse: Codable, Equatable, Sendable {
  public let worker: WorkerInfo
  public let workerToken: String
  public let protocolVersion: String

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case worker
    case workerToken = "worker_token"
    case protocolVersion = "protocol_version"
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    worker = try c.decode(WorkerInfo.self, forKey: .worker)
    workerToken = try c.decode(String.self, forKey: .workerToken)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
  }
}

public struct AudioLimits: Codable, Equatable, Sendable {
  public let maxBytes: Int64
  public let maxDurationMS: Int64

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case maxBytes = "max_bytes"
    case maxDurationMS = "max_duration_ms"
  }

  public init(maxBytes: Int64, maxDurationMS: Int64) {
    self.maxBytes = maxBytes
    self.maxDurationMS = maxDurationMS
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    maxBytes = try c.decode(Int64.self, forKey: .maxBytes)
    maxDurationMS = try c.decode(Int64.self, forKey: .maxDurationMS)
    guard maxBytes > 0, maxDurationMS > 0 else { throw ProtocolError.invalidValue("limits") }
  }
}

private func isLowercaseHexDigest(_ value: String, length: Int = 64) -> Bool {
  guard value.utf8.count == length else { return false }
  return value.utf8.allSatisfy { byte in
    (byte >= 48 && byte <= 57) || (byte >= 97 && byte <= 102)
  }
}

public struct SpeechLeaseDetails: Equatable, Sendable {
  public let renderID: String
  public let requestHash: String
  public let speechUnitID: String
  public let language: String
  public let unitKind: String
  public let spokenText: String
  public let contextPronunciationKey: String
  public let profile: SpeechProfile
  public let limits: AudioLimits

  public init(
    renderID: String, requestHash: String, speechUnitID: String, language: String,
    unitKind: String, spokenText: String, contextPronunciationKey: String,
    profile: SpeechProfile, limits: AudioLimits
  ) {
    self.renderID = renderID
    self.requestHash = requestHash
    self.speechUnitID = speechUnitID
    self.language = language
    self.unitKind = unitKind
    self.spokenText = spokenText
    self.contextPronunciationKey = contextPronunciationKey
    self.profile = profile
    self.limits = limits
  }
}

public struct LeaseResponse: Codable, Equatable, Sendable {
  /// Relay lease responses carry ~2 MiB payloads (JSON schema pass-through), so
  /// the decode bound is the payload hard limit plus envelope slack.
  public static let maxEncodedBytes = RelayLimits.maxPayloadBytes + 131_072

  public let protocolVersion: String
  public let jobID: String
  public let attempt: Int
  public let leaseToken: String
  public let leaseExpiresAt: String
  public let jobType: String
  public let speech: SpeechLeaseDetails?
  public let operation: String?
  public let relay: RelayLease?

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case protocolVersion = "protocol_version"
    case jobID = "job_id"
    case attempt
    case leaseToken = "lease_token"
    case leaseExpiresAt = "lease_expires_at"
    case jobType = "job_type"
    case renderID = "render_id"
    case requestHash = "request_hash"
    case speechUnitID = "speech_unit_id"
    case language
    case unitKind = "unit_kind"
    case spokenText = "spoken_text"
    case contextPronunciationKey = "context_pronunciation_key"
    case profile, limits
    case operation, relay
  }

  static let speechKeyNames: Set<String> = [
    "render_id", "request_hash", "speech_unit_id", "language", "unit_kind", "spoken_text",
    "context_pronunciation_key", "profile", "limits",
  ]
  static let relayKeyNames: Set<String> = ["operation", "relay"]

  public init(
    protocolVersion: String, jobID: String, attempt: Int, leaseToken: String,
    leaseExpiresAt: String, jobType: String, speech: SpeechLeaseDetails? = nil,
    operation: String? = nil, relay: RelayLease? = nil
  ) {
    self.protocolVersion = protocolVersion
    self.jobID = jobID
    self.attempt = attempt
    self.leaseToken = leaseToken
    self.leaseExpiresAt = leaseExpiresAt
    self.jobType = jobType
    self.speech = speech
    self.operation = operation
    self.relay = relay
  }

  public init(from decoder: Decoder) throws {
    let c = try decoder.container(keyedBy: CodingKeys.self)
    jobType = try c.decode(String.self, forKey: .jobType)
    let isRelay = jobType == "llm.relay.v1"
    let isSpeech = ["tts.avspeech.v1", "tts.chatterbox.v3"].contains(jobType)
    guard isRelay || isSpeech else { throw ProtocolError.invalidValue("lease_job_type") }
    // Unknown keys are rejected in both branches; speech-only keys are tolerated
    // (but ignored) on relay leases because the server emits them zero-valued.
    var expected = Set(CodingKeys.allCases.map(\.stringValue))
    var optional: Set<String> = []
    if isRelay {
      optional = Self.speechKeyNames
    } else {
      expected.subtract(Self.relayKeyNames)
    }
    try StrictCoding.checkKeys(
      decoder, expected: expected, optional: optional.union(["worker_id"]))
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    jobID = try c.decode(String.self, forKey: .jobID)
    attempt = try c.decode(Int.self, forKey: .attempt)
    leaseToken = try c.decode(String.self, forKey: .leaseToken)
    leaseExpiresAt = try c.decode(String.self, forKey: .leaseExpiresAt)
    if isSpeech {
      speech = SpeechLeaseDetails(
        renderID: try c.decode(String.self, forKey: .renderID),
        requestHash: try c.decode(String.self, forKey: .requestHash),
        speechUnitID: try c.decode(String.self, forKey: .speechUnitID),
        language: try c.decode(String.self, forKey: .language),
        unitKind: try c.decode(String.self, forKey: .unitKind),
        spokenText: try c.decode(String.self, forKey: .spokenText),
        contextPronunciationKey: try c.decode(String.self, forKey: .contextPronunciationKey),
        profile: try c.decode(SpeechProfile.self, forKey: .profile),
        limits: try c.decode(AudioLimits.self, forKey: .limits))
      operation = nil
      relay = nil
    } else {
      speech = nil
      operation = try c.decode(String.self, forKey: .operation)
      switch operation {
      case "chat_completion":
        relay = try .chat(c.decode(RelayChatLease.self, forKey: .relay))
      case "list_models":
        relay = try .models(c.decode(RelayListModelsLease.self, forKey: .relay))
      default:
        throw ProtocolError.invalidValue("relay_operation")
      }
    }
    try validate()
  }

  public func encode(to encoder: Encoder) throws {
    var c = encoder.container(keyedBy: CodingKeys.self)
    try c.encode(protocolVersion, forKey: .protocolVersion)
    try c.encode(jobID, forKey: .jobID)
    try c.encode(attempt, forKey: .attempt)
    try c.encode(leaseToken, forKey: .leaseToken)
    try c.encode(leaseExpiresAt, forKey: .leaseExpiresAt)
    try c.encode(jobType, forKey: .jobType)
    if let speech {
      try c.encode(speech.renderID, forKey: .renderID)
      try c.encode(speech.requestHash, forKey: .requestHash)
      try c.encode(speech.speechUnitID, forKey: .speechUnitID)
      try c.encode(speech.language, forKey: .language)
      try c.encode(speech.unitKind, forKey: .unitKind)
      try c.encode(speech.spokenText, forKey: .spokenText)
      try c.encode(speech.contextPronunciationKey, forKey: .contextPronunciationKey)
      try c.encode(speech.profile, forKey: .profile)
      try c.encode(speech.limits, forKey: .limits)
    }
    try c.encodeIfPresent(operation, forKey: .operation)
    try c.encodeIfPresent(relay, forKey: .relay)
  }

  public func validate() throws {
    guard protocolVersion == WorkerConstants.protocolVersion, attempt >= 1 && attempt <= 3,
      !leaseToken.isEmpty, !leaseExpiresAt.isEmpty
    else { throw ProtocolError.invalidValue("lease") }
    switch jobType {
    case "tts.avspeech.v1", "tts.chatterbox.v3":
      guard let speech, operation == nil, relay == nil,
        isLowercaseHexDigest(speech.requestHash),
        speech.language == "nl", ["word", "phrase", "sentence"].contains(speech.unitKind),
        !speech.spokenText.isEmpty,
        (jobType == "tts.avspeech.v1" && speech.profile.engine == "avspeech"
          && speech.unitKind != "sentence")
          || (jobType == "tts.chatterbox.v3" && speech.profile.engine == "chatterbox"
            && speech.unitKind == "sentence")
      else { throw ProtocolError.invalidValue("lease") }
      try speech.profile.validate()
    case "llm.relay.v1":
      guard speech == nil, let operation, let relay else {
        throw ProtocolError.invalidValue("lease")
      }
      guard operation == relay.operation else {
        throw ProtocolError.invalidValue("lease_operation")
      }
      if case .chat(let chat) = relay { try chat.validate() }
    default:
      throw ProtocolError.invalidValue("lease_job_type")
    }
  }
}

public struct EnrollRequest: Codable, Equatable, Sendable {
  public let name: String
  public let protocolVersion: String
  public let capabilities: [WorkerCapability]
  public let softwareVersion: String
  public let llmRelayCapabilities: [LLMRelayCapability]?

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case name
    case protocolVersion = "protocol_version"
    case capabilities
    case softwareVersion = "software_version"
    case llmRelayCapabilities = "llm_relay_capabilities"
  }

  public init(
    name: String, capabilities: [WorkerCapability], softwareVersion: String,
    llmRelayCapabilities: [LLMRelayCapability]? = nil
  ) {
    self.name = name
    protocolVersion = WorkerConstants.protocolVersion
    self.capabilities = capabilities
    self.softwareVersion = softwareVersion
    self.llmRelayCapabilities = llmRelayCapabilities
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self, optional: ["llm_relay_capabilities"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    name = try c.decode(String.self, forKey: .name)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    capabilities = try c.decode([WorkerCapability].self, forKey: .capabilities)
    softwareVersion = try c.decode(String.self, forKey: .softwareVersion)
    llmRelayCapabilities = try c.decodeIfPresent(
      [LLMRelayCapability].self, forKey: .llmRelayCapabilities)
  }
}

/// Exactly one lane per lease request; the public initializer makes mixed-lane
/// capability payloads unrepresentable.
public enum LeaseLane: Equatable, Sendable {
  case speech([WorkerCapability])
  case relay(LLMRelayCapability)
}

public struct LeaseRequest: Codable, Equatable, Sendable {
  public let protocolVersion: String
  public let capabilities: [WorkerCapability]?
  public let llmRelayCapabilities: [LLMRelayCapability]?

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case protocolVersion = "protocol_version"
    case capabilities
    case llmRelayCapabilities = "llm_relay_capabilities"
  }

  public init(lane: LeaseLane) {
    protocolVersion = WorkerConstants.protocolVersion
    switch lane {
    case .speech(let capabilities):
      self.capabilities = capabilities
      llmRelayCapabilities = nil
    case .relay(let capability):
      capabilities = nil
      llmRelayCapabilities = [capability]
    }
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(
      decoder, CodingKeys.self, optional: ["capabilities", "llm_relay_capabilities"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    capabilities = try c.decodeIfPresent([WorkerCapability].self, forKey: .capabilities)
    llmRelayCapabilities = try c.decodeIfPresent(
      [LLMRelayCapability].self, forKey: .llmRelayCapabilities)
    guard (capabilities?.isEmpty == false) != (llmRelayCapabilities?.isEmpty == false) else {
      throw ProtocolError.invalidValue("lease_lane")
    }
  }

  public func encode(to encoder: Encoder) throws {
    var c = encoder.container(keyedBy: CodingKeys.self)
    try c.encode(protocolVersion, forKey: .protocolVersion)
    try c.encodeIfPresent(capabilities, forKey: .capabilities)
    try c.encodeIfPresent(llmRelayCapabilities, forKey: .llmRelayCapabilities)
  }
}

public struct HeartbeatRequest: Codable, Equatable, Sendable {
  public let protocolVersion: String
  public let attempt: Int
  public let progressPercent: Int
  private enum CodingKeys: String, CodingKey, CaseIterable {
    case protocolVersion = "protocol_version"
    case attempt
    case progressPercent = "progress_percent"
  }
  public init(attempt: Int, progressPercent: Int) {
    protocolVersion = WorkerConstants.protocolVersion
    self.attempt = attempt
    self.progressPercent = progressPercent
  }
  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    attempt = try c.decode(Int.self, forKey: .attempt)
    progressPercent = try c.decode(Int.self, forKey: .progressPercent)
  }
}

public struct HeartbeatResponse: Codable, Equatable, Sendable {
  public let protocolVersion: String
  public let cancelRequested: Bool
  public let leaseExpiresAt: String
  public let progressPercent: Int
  private enum CodingKeys: String, CodingKey, CaseIterable {
    case protocolVersion = "protocol_version"
    case cancelRequested = "cancel_requested"
    case leaseExpiresAt = "lease_expires_at"
    case progressPercent = "progress_percent"
  }
  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    cancelRequested = try c.decode(Bool.self, forKey: .cancelRequested)
    leaseExpiresAt = try c.decode(String.self, forKey: .leaseExpiresAt)
    progressPercent = try c.decode(Int.self, forKey: .progressPercent)
  }
}

public struct FailureRequest: Codable, Equatable, Sendable {
  public let protocolVersion: String
  public let attempt: Int
  public let errorCode: String
  public let retry: Bool
  private enum CodingKeys: String, CodingKey, CaseIterable {
    case protocolVersion = "protocol_version"
    case attempt
    case errorCode = "error_code"
    case retry
  }
  public init(attempt: Int, errorCode: String, retry: Bool) {
    protocolVersion = WorkerConstants.protocolVersion
    self.attempt = attempt
    self.errorCode = errorCode
    self.retry = retry
  }
  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    attempt = try c.decode(Int.self, forKey: .attempt)
    errorCode = try c.decode(String.self, forKey: .errorCode)
    retry = try c.decode(Bool.self, forKey: .retry)
  }
}

public struct ArtifactMetadata: Codable, Equatable, Sendable {
  public let requestHash: String
  public let sha256: String
  public let sizeBytes: Int64
  public let mimeType: String
  public let codec: String
  public let sampleRateHz: Int
  public let channels: Int
  public let durationMS: Int64
  private enum CodingKeys: String, CodingKey, CaseIterable {
    case requestHash = "request_hash"
    case sha256
    case sizeBytes = "size_bytes"
    case mimeType = "mime_type"
    case codec
    case sampleRateHz = "sample_rate_hz"
    case channels
    case durationMS = "duration_ms"
  }
  public init(requestHash: String, sha256: String, sizeBytes: Int64, durationMS: Int64) {
    self.requestHash = requestHash
    self.sha256 = sha256
    self.sizeBytes = sizeBytes
    mimeType = "audio/mp4"
    codec = "aac-lc"
    sampleRateHz = 24000
    channels = 1
    self.durationMS = durationMS
  }
  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    requestHash = try c.decode(String.self, forKey: .requestHash)
    sha256 = try c.decode(String.self, forKey: .sha256)
    sizeBytes = try c.decode(Int64.self, forKey: .sizeBytes)
    mimeType = try c.decode(String.self, forKey: .mimeType)
    codec = try c.decode(String.self, forKey: .codec)
    sampleRateHz = try c.decode(Int.self, forKey: .sampleRateHz)
    channels = try c.decode(Int.self, forKey: .channels)
    durationMS = try c.decode(Int64.self, forKey: .durationMS)
  }
}

public struct CompletionMetadata: Codable, Equatable, Sendable {
  public let protocolVersion: String
  public let attempt: Int
  public let leaseToken: String
  public let artifact: ArtifactMetadata?
  private enum CodingKeys: String, CodingKey, CaseIterable {
    case protocolVersion = "protocol_version"
    case attempt
    case leaseToken = "lease_token"
    case artifact
  }
  public init(attempt: Int, leaseToken: String, artifact: ArtifactMetadata?) {
    protocolVersion = WorkerConstants.protocolVersion
    self.attempt = attempt
    self.leaseToken = leaseToken
    self.artifact = artifact
  }
  public init(from decoder: Decoder) throws {
    // `artifact` is optional on the wire: relay completions carry a result part
    // instead, and the job type decides whether it was actually required.
    try StrictCoding.checkKeys(decoder, CodingKeys.self, optional: ["artifact"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    attempt = try c.decode(Int.self, forKey: .attempt)
    leaseToken = try c.decode(String.self, forKey: .leaseToken)
    artifact = try c.decodeIfPresent(ArtifactMetadata.self, forKey: .artifact)
  }
}

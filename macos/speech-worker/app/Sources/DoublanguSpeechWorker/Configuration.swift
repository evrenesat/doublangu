import CryptoKit
import Foundation

public enum WorkerConstants {
  public static let bundleIdentifier = "io.evren.doublangu.speech-worker"
  public static let productName = "Doublangu worker"
  public static let protocolVersion = "speech-worker.v1"
  public static let avSpeechModelRevision = "macos-15.7.9-build-24G830"
  public static let avSpeechVoiceIdentifier = "com.apple.voice.compact.nl-NL.Xander"
  public static let avSpeechMappingVersion = "avspeech-rate-postprocess.v1"
  public static let chatterboxModelRepository = "mlx-community/chatterbox-multilingual-v3"
  public static let chatterboxModelRevision = "03565773edd72e949572557597af8063bb49a18a"
  public static let chatterboxTokenizerRepository = "mlx-community/S3TokenizerV2"
  public static let chatterboxTokenizerRevision = "e0c9886f0e1c35ae85b1f27277416fb1"
  public static let chatterboxVoiceIdentifier = "doublangu-nl-reference-v1"
  public static let chatterboxMappingVersion = "mlx-audio-chatterbox.v1"
  public static let referenceAudioSHA256 =
    "1dd25cc2ea1aa8314af2ce2f062eb44beeb662482516177e098f58f6b6ce10f5"
  public static let audioNormalizationVersion = "audio-normalization.v1"
  public static let appVersion = "0.2.0"
  public static let relayMaxCompletionBytes: Int64 = 2_097_152
  public static let relayDefaultBaseURL = URL(string: "http://127.0.0.1:8899/v1")!
  public static let relayDefaultTimeoutSeconds = 540
  public static let relayMinimumTimeoutSeconds = 30
  public static let minimumFreeBytes: Int64 = 12 * 1024 * 1024 * 1024
  public static let recommendedFreeBytes: Int64 = 20 * 1024 * 1024 * 1024
  public static let maxSpoolBytes: Int64 = 512 * 1024 * 1024
  public static let chatterboxIdleSeconds: TimeInterval = 600
  public static let portRange = 38_700...38_799

  public static let avSpeechProfile = SpeechProfile(
    engine: "avspeech", modelRevision: avSpeechModelRevision, language: "nl",
    voiceIdentifier: avSpeechVoiceIdentifier, referenceAudioHash: "", speedMilli: 1000,
    pitchCents: 0, mappingVersion: avSpeechMappingVersion
  )
  public static let chatterboxProfile = SpeechProfile(
    engine: "chatterbox", modelRevision: chatterboxModelRevision, language: "nl",
    voiceIdentifier: chatterboxVoiceIdentifier, referenceAudioHash: referenceAudioSHA256,
    speedMilli: 1000, pitchCents: 0, mappingVersion: chatterboxMappingVersion
  )
}

public enum RelayConfigError: Error, Equatable, LocalizedError, Sendable {
  case invalidURL
  case invalidTimeout
  case missingAPIKey

  public var errorDescription: String? {
    switch self {
    case .invalidURL: "relay_invalid_url"
    case .invalidTimeout: "relay_invalid_timeout"
    case .missingAPIKey: "relay_missing_api_key"
    }
  }
}

public struct RelayConfig: Codable, Equatable, Sendable {
  public var enabled: Bool
  public var baseURL: URL
  public var requestTimeoutSeconds: Int

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case enabled
    case baseURL = "base_url"
    case requestTimeoutSeconds = "request_timeout_seconds"
  }

  public init(
    enabled: Bool = false, baseURL: URL = WorkerConstants.relayDefaultBaseURL,
    requestTimeoutSeconds: Int = WorkerConstants.relayDefaultTimeoutSeconds
  ) {
    self.enabled = enabled
    self.baseURL = baseURL
    self.requestTimeoutSeconds = requestTimeoutSeconds
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    enabled = try c.decode(Bool.self, forKey: .enabled)
    baseURL = try c.decode(URL.self, forKey: .baseURL)
    requestTimeoutSeconds = try c.decode(Int.self, forKey: .requestTimeoutSeconds)
    try validate()
  }

  public func encode(to encoder: Encoder) throws {
    var c = encoder.container(keyedBy: CodingKeys.self)
    try c.encode(enabled, forKey: .enabled)
    try c.encode(baseURL, forKey: .baseURL)
    try c.encode(requestTimeoutSeconds, forKey: .requestTimeoutSeconds)
  }

  public func validate() throws {
    guard requestTimeoutSeconds >= WorkerConstants.relayMinimumTimeoutSeconds,
      requestTimeoutSeconds <= WorkerConstants.relayDefaultTimeoutSeconds
    else { throw RelayConfigError.invalidTimeout }
    guard let components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false),
      let scheme = components.scheme?.lowercased(), let host = components.host
    else { throw RelayConfigError.invalidURL }
    guard components.user == nil, components.password == nil, components.query == nil,
      components.fragment == nil, components.path == "/v1", scheme == "https" || scheme == "http",
      components.port.map({ (1...65_535).contains($0) }) ?? true
    else { throw RelayConfigError.invalidURL }
    if scheme == "http" {
      guard Self.isLiteralLoopback(host) else { throw RelayConfigError.invalidURL }
    }
  }

  /// Plain HTTP is accepted only for literal loopback addresses so DNS can never
  /// retarget the local model server; `localhost` is deliberately excluded.
  static func isLiteralLoopback(_ host: String) -> Bool {
    var normalized = host
    if normalized.hasPrefix("["), normalized.hasSuffix("]"), normalized.count >= 4 {
      normalized = String(normalized.dropFirst().dropLast())
    }
    if normalized == "::1" { return true }
    let parts = normalized.split(separator: ".", omittingEmptySubsequences: false)
    guard parts.count == 4, parts[0] == "127" else { return false }
    return parts.dropFirst().allSatisfy { part in
      !part.isEmpty && part.count <= 3 && part.allSatisfy(\.isNumber)
        && (Int(part).map { $0 <= 255 } ?? false)
    }
  }
}

public struct SpeechWorkerConfiguration: Codable, Equatable, Sendable {
  /// Deployment-specific; set through worker settings and persisted as
  /// `base_url`. `nil` means "not configured yet".
  public var baseURL: URL?
  public var protocolVersion: String
  public var workerID: String?
  public var workerName: String
  public var avSpeechProfile: SpeechProfile
  public var chatterboxProfile: SpeechProfile
  public var modelRepository: String
  public var modelRevision: String
  public var tokenizerRepository: String
  public var tokenizerRevision: String
  public var referenceAudioPath: String
  public var referenceAudioHash: String
  public var audioNormalizationVersion: String
  public var portRangeStart: Int
  public var portRangeEnd: Int
  public var oneJobCapacity: Int
  public var maxSpoolBytes: Int64
  public var chatterboxIdleSeconds: TimeInterval
  public var relay: RelayConfig

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case baseURL = "base_url"
    case protocolVersion = "protocol_version"
    case workerID = "worker_id"
    case workerName = "worker_name"
    case avSpeechProfile = "avspeech_profile"
    case chatterboxProfile = "chatterbox_profile"
    case modelRepository = "model_repository"
    case modelRevision = "model_revision"
    case tokenizerRepository = "tokenizer_repository"
    case tokenizerRevision = "tokenizer_revision"
    case referenceAudioPath = "reference_audio_path"
    case referenceAudioHash = "reference_audio_hash"
    case audioNormalizationVersion = "audio_normalization_version"
    case portRangeStart = "port_range_start"
    case portRangeEnd = "port_range_end"
    case oneJobCapacity = "one_job_capacity"
    case maxSpoolBytes = "max_spool_bytes"
    case chatterboxIdleSeconds = "chatterbox_idle_seconds"
    case relay
  }

  public static func `default`(paths: AppPaths) -> SpeechWorkerConfiguration {
    SpeechWorkerConfiguration(
      baseURL: nil, protocolVersion: WorkerConstants.protocolVersion,
      workerID: nil, workerName: Host.current().localizedName ?? "Doublangu Mac",
      avSpeechProfile: WorkerConstants.avSpeechProfile,
      chatterboxProfile: WorkerConstants.chatterboxProfile,
      modelRepository: WorkerConstants.chatterboxModelRepository,
      modelRevision: WorkerConstants.chatterboxModelRevision,
      tokenizerRepository: WorkerConstants.chatterboxTokenizerRepository,
      tokenizerRevision: WorkerConstants.chatterboxTokenizerRevision,
      referenceAudioPath: paths.referenceURL.path,
      referenceAudioHash: WorkerConstants.referenceAudioSHA256,
      audioNormalizationVersion: WorkerConstants.audioNormalizationVersion,
      portRangeStart: WorkerConstants.portRange.lowerBound,
      portRangeEnd: WorkerConstants.portRange.upperBound,
      oneJobCapacity: 1, maxSpoolBytes: WorkerConstants.maxSpoolBytes,
      chatterboxIdleSeconds: WorkerConstants.chatterboxIdleSeconds,
      relay: RelayConfig()
    )
  }

  public init(
    baseURL: URL?, protocolVersion: String, workerID: String?, workerName: String,
    avSpeechProfile: SpeechProfile, chatterboxProfile: SpeechProfile,
    modelRepository: String, modelRevision: String, tokenizerRepository: String,
    tokenizerRevision: String, referenceAudioPath: String, referenceAudioHash: String,
    audioNormalizationVersion: String, portRangeStart: Int, portRangeEnd: Int,
    oneJobCapacity: Int, maxSpoolBytes: Int64, chatterboxIdleSeconds: TimeInterval,
    relay: RelayConfig = RelayConfig()
  ) {
    self.baseURL = baseURL
    self.protocolVersion = protocolVersion
    self.workerID = workerID
    self.workerName = workerName
    self.avSpeechProfile = avSpeechProfile
    self.chatterboxProfile = chatterboxProfile
    self.modelRepository = modelRepository
    self.modelRevision = modelRevision
    self.tokenizerRepository = tokenizerRepository
    self.tokenizerRevision = tokenizerRevision
    self.referenceAudioPath = referenceAudioPath
    self.referenceAudioHash = referenceAudioHash
    self.audioNormalizationVersion = audioNormalizationVersion
    self.portRangeStart = portRangeStart
    self.portRangeEnd = portRangeEnd
    self.oneJobCapacity = oneJobCapacity
    self.maxSpoolBytes = maxSpoolBytes
    self.chatterboxIdleSeconds = chatterboxIdleSeconds
    self.relay = relay
  }

  public init(from decoder: Decoder) throws {
    // `base_url` and `relay` are the optional keys so fresh installs can be
    // written before a server URL exists and installed v0.1 config files keep
    // decoding; every previously required key stays required and unknown keys
    // stay rejected.
    try StrictCoding.checkKeys(
      decoder, CodingKeys.self, optional: ["base_url", "worker_id", "relay"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    baseURL = try c.decodeIfPresent(URL.self, forKey: .baseURL)
    protocolVersion = try c.decode(String.self, forKey: .protocolVersion)
    workerID = try c.decodeIfPresent(String.self, forKey: .workerID)
    workerName = try c.decode(String.self, forKey: .workerName)
    avSpeechProfile = try c.decode(SpeechProfile.self, forKey: .avSpeechProfile)
    chatterboxProfile = try c.decode(SpeechProfile.self, forKey: .chatterboxProfile)
    modelRepository = try c.decode(String.self, forKey: .modelRepository)
    modelRevision = try c.decode(String.self, forKey: .modelRevision)
    tokenizerRepository = try c.decode(String.self, forKey: .tokenizerRepository)
    tokenizerRevision = try c.decode(String.self, forKey: .tokenizerRevision)
    referenceAudioPath = try c.decode(String.self, forKey: .referenceAudioPath)
    referenceAudioHash = try c.decode(String.self, forKey: .referenceAudioHash)
    audioNormalizationVersion = try c.decode(String.self, forKey: .audioNormalizationVersion)
    portRangeStart = try c.decode(Int.self, forKey: .portRangeStart)
    portRangeEnd = try c.decode(Int.self, forKey: .portRangeEnd)
    oneJobCapacity = try c.decode(Int.self, forKey: .oneJobCapacity)
    maxSpoolBytes = try c.decode(Int64.self, forKey: .maxSpoolBytes)
    chatterboxIdleSeconds = try c.decode(TimeInterval.self, forKey: .chatterboxIdleSeconds)
    relay = try c.decodeIfPresent(RelayConfig.self, forKey: .relay) ?? RelayConfig()
    try validate()
  }

  public func validate(paths: AppPaths? = nil) throws {
    // The server base URL is deployment-specific and user-configured: any
    // HTTPS target is accepted, plus plain HTTP for literal loopback hosts so
    // a locally hosted server can be used in development. Only the shape is
    // enforced here; the protocol pins the rest of the identity.
    if let baseURL {
      guard let components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false),
        let scheme = components.scheme?.lowercased(), let host = components.host, !host.isEmpty,
        scheme == "https" || scheme == "http",
        components.user == nil, components.password == nil, components.query == nil,
        components.fragment == nil,
        components.port.map({ (1...65_535).contains($0) }) ?? true
      else { throw ConfigurationError.invalid }
      if scheme == "http" {
        guard RelayConfig.isLiteralLoopback(host) else { throw ConfigurationError.invalid }
      }
    }
    guard protocolVersion == WorkerConstants.protocolVersion, !workerName.isEmpty,
      workerName.count <= 120,
      avSpeechProfile.matchesByteAffecting(WorkerConstants.avSpeechProfile),
      chatterboxProfile.matchesByteAffecting(WorkerConstants.chatterboxProfile),
      modelRepository == WorkerConstants.chatterboxModelRepository,
      modelRevision == WorkerConstants.chatterboxModelRevision,
      tokenizerRepository == WorkerConstants.chatterboxTokenizerRepository,
      tokenizerRevision == WorkerConstants.chatterboxTokenizerRevision,
      referenceAudioHash == WorkerConstants.referenceAudioSHA256,
      audioNormalizationVersion == WorkerConstants.audioNormalizationVersion,
      portRangeStart >= 1024, portRangeEnd >= portRangeStart, portRangeEnd - portRangeStart <= 999,
      oneJobCapacity == 1, maxSpoolBytes == WorkerConstants.maxSpoolBytes,
      chatterboxIdleSeconds == WorkerConstants.chatterboxIdleSeconds,
      (try? relay.validate()) != nil
    else { throw ConfigurationError.invalid }
    if let paths, referenceAudioPath != paths.referenceURL.path { throw ConfigurationError.invalid }
  }

  /// Loads and validates the on-disk config. A valid v0.1 file without a `relay`
  /// block is transparently rewritten once with the default relay block so the
  /// next decode is stable. Throws on unknown keys or invalid values like any
  /// other strict decode.
  public static func loadFromDisk(paths: AppPaths) throws -> SpeechWorkerConfiguration {
    let data = try Data(contentsOf: paths.configURL)
    let config = try StrictJSON.decode(Self.self, from: data)
    try config.validate(paths: paths)
    if !hasRelayBlock(data) {
      try paths.writePrivate(StrictJSON.encode(config), to: paths.configURL)
    }
    return config
  }

  private static func hasRelayBlock(_ data: Data) -> Bool {
    guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
      return true
    }
    return object["relay"] != nil
  }

  public func capabilities() -> [WorkerCapability] {
    [
      WorkerCapability(
        engine: "avspeech", languages: ["nl"], unitKinds: ["word", "phrase"], maxBytes: 2 << 20,
        maxDurationMS: 30_000),
      WorkerCapability(
        engine: "chatterbox", languages: ["nl"], unitKinds: ["sentence"], maxBytes: 64 << 20,
        maxDurationMS: 180_000),
    ]
  }
}

public enum ConfigurationError: Error, Equatable, LocalizedError, Sendable {
  case invalid
  case invalidServerURL
  case storageGate(required: Int64, available: Int64?)
  case referenceMissing
  case referenceHashMismatch
  case runtimeNotReady

  public var errorDescription: String? {
    switch self {
    case .invalid: "configuration_invalid"
    case .invalidServerURL: "server_url_invalid"
    case .storageGate: "storage_gate_failed"
    case .referenceMissing: "reference_audio_missing"
    case .referenceHashMismatch: "reference_audio_hash_mismatch"
    case .runtimeNotReady: "runtime_not_ready"
    }
  }
}

public struct SetupReceipt: Codable, Equatable, Sendable {
  public let schemaVersion: Int
  public let pythonVersion: String
  public let mlxAudioVersion: String
  public let mlxVersion: String
  public let modelRepository: String
  public let modelRevision: String
  public let tokenizerRepository: String
  public let tokenizerRevision: String
  public let referenceAudioSHA256: String
  public let runtimeReceipt: String
  public let verifiedAtUTC: String

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case pythonVersion = "python_version"
    case mlxAudioVersion = "mlx_audio_version"
    case mlxVersion = "mlx_version"
    case modelRepository = "model_repository"
    case modelRevision = "model_revision"
    case tokenizerRepository = "tokenizer_repository"
    case tokenizerRevision = "tokenizer_revision"
    case referenceAudioSHA256 = "reference_audio_sha256"
    case runtimeReceipt = "runtime_receipt"
    case verifiedAtUTC = "verified_at_utc"
  }

  public init(
    schemaVersion: Int = 1, pythonVersion: String, mlxAudioVersion: String, mlxVersion: String,
    modelRepository: String, modelRevision: String, tokenizerRepository: String,
    tokenizerRevision: String, referenceAudioSHA256: String, runtimeReceipt: String,
    verifiedAtUTC: String
  ) {
    self.schemaVersion = schemaVersion
    self.pythonVersion = pythonVersion
    self.mlxAudioVersion = mlxAudioVersion
    self.mlxVersion = mlxVersion
    self.modelRepository = modelRepository
    self.modelRevision = modelRevision
    self.tokenizerRepository = tokenizerRepository
    self.tokenizerRevision = tokenizerRevision
    self.referenceAudioSHA256 = referenceAudioSHA256
    self.runtimeReceipt = runtimeReceipt
    self.verifiedAtUTC = verifiedAtUTC
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    schemaVersion = try c.decode(Int.self, forKey: .schemaVersion)
    pythonVersion = try c.decode(String.self, forKey: .pythonVersion)
    mlxAudioVersion = try c.decode(String.self, forKey: .mlxAudioVersion)
    mlxVersion = try c.decode(String.self, forKey: .mlxVersion)
    modelRepository = try c.decode(String.self, forKey: .modelRepository)
    modelRevision = try c.decode(String.self, forKey: .modelRevision)
    tokenizerRepository = try c.decode(String.self, forKey: .tokenizerRepository)
    tokenizerRevision = try c.decode(String.self, forKey: .tokenizerRevision)
    referenceAudioSHA256 = try c.decode(String.self, forKey: .referenceAudioSHA256)
    runtimeReceipt = try c.decode(String.self, forKey: .runtimeReceipt)
    verifiedAtUTC = try c.decode(String.self, forKey: .verifiedAtUTC)
    try validate()
  }

  public func validate() throws {
    guard schemaVersion == 1, pythonVersion == "3.12.11", mlxAudioVersion == "0.4.7",
      mlxVersion == "0.32.2",
      modelRepository == WorkerConstants.chatterboxModelRepository,
      modelRevision == WorkerConstants.chatterboxModelRevision,
      tokenizerRepository == WorkerConstants.chatterboxTokenizerRepository,
      tokenizerRevision == WorkerConstants.chatterboxTokenizerRevision,
      referenceAudioSHA256 == WorkerConstants.referenceAudioSHA256,
      runtimeReceipt.hasPrefix("sha256:"),
      Self.isDigest(String(runtimeReceipt.dropFirst(7))), verifiedAtUTC.hasSuffix("Z")
    else { throw ConfigurationError.runtimeNotReady }
  }

  private static func isDigest(_ value: String) -> Bool {
    value.utf8.count == 64
      && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
  }
}

public func sha256Hex(_ data: Data) -> String {
  SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

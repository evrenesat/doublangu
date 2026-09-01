import Foundation

public struct ReleaseManifest: Codable, Equatable, Sendable {
  public let schemaVersion: Int
  public let appVersion: String
  public let bundleID: String
  public let applicationCommit: String
  public let pythonVersion: String
  public let mlxAudioVersion: String
  public let modelRepository: String
  public let modelRevision: String
  public let tokenizerRepository: String
  public let tokenizerRevision: String
  public let referenceAudioSHA256: String
  public let runtimeReceipt: String
  public let minimumMacOS: String
  public let architecture: String
  public let builtAtUTC: String

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case appVersion = "app_version"
    case bundleID = "bundle_id"
    case applicationCommit = "application_commit"
    case pythonVersion = "python_version"
    case mlxAudioVersion = "mlx_audio_version"
    case modelRepository = "model_repository"
    case modelRevision = "model_revision"
    case tokenizerRepository = "tokenizer_repository"
    case tokenizerRevision = "tokenizer_revision"
    case referenceAudioSHA256 = "reference_audio_sha256"
    case runtimeReceipt = "runtime_receipt"
    case minimumMacOS = "minimum_macos"
    case architecture
    case builtAtUTC = "built_at_utc"
  }

  public init(
    schemaVersion: Int = 1, appVersion: String, bundleID: String = WorkerConstants.bundleIdentifier,
    applicationCommit: String, pythonVersion: String, mlxAudioVersion: String,
    modelRepository: String, modelRevision: String, tokenizerRepository: String,
    tokenizerRevision: String, referenceAudioSHA256: String, runtimeReceipt: String,
    minimumMacOS: String = "14.0", architecture: String = "arm64", builtAtUTC: String
  ) {
    self.schemaVersion = schemaVersion
    self.appVersion = appVersion
    self.bundleID = bundleID
    self.applicationCommit = applicationCommit
    self.pythonVersion = pythonVersion
    self.mlxAudioVersion = mlxAudioVersion
    self.modelRepository = modelRepository
    self.modelRevision = modelRevision
    self.tokenizerRepository = tokenizerRepository
    self.tokenizerRevision = tokenizerRevision
    self.referenceAudioSHA256 = referenceAudioSHA256
    self.runtimeReceipt = runtimeReceipt
    self.minimumMacOS = minimumMacOS
    self.architecture = architecture
    self.builtAtUTC = builtAtUTC
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let c = try decoder.container(keyedBy: CodingKeys.self)
    schemaVersion = try c.decode(Int.self, forKey: .schemaVersion)
    appVersion = try c.decode(String.self, forKey: .appVersion)
    bundleID = try c.decode(String.self, forKey: .bundleID)
    applicationCommit = try c.decode(String.self, forKey: .applicationCommit)
    pythonVersion = try c.decode(String.self, forKey: .pythonVersion)
    mlxAudioVersion = try c.decode(String.self, forKey: .mlxAudioVersion)
    modelRepository = try c.decode(String.self, forKey: .modelRepository)
    modelRevision = try c.decode(String.self, forKey: .modelRevision)
    tokenizerRepository = try c.decode(String.self, forKey: .tokenizerRepository)
    tokenizerRevision = try c.decode(String.self, forKey: .tokenizerRevision)
    referenceAudioSHA256 = try c.decode(String.self, forKey: .referenceAudioSHA256)
    runtimeReceipt = try c.decode(String.self, forKey: .runtimeReceipt)
    minimumMacOS = try c.decode(String.self, forKey: .minimumMacOS)
    architecture = try c.decode(String.self, forKey: .architecture)
    builtAtUTC = try c.decode(String.self, forKey: .builtAtUTC)
    try validate()
  }

  public func validate() throws {
    guard schemaVersion == 1, !appVersion.isEmpty, bundleID == WorkerConstants.bundleIdentifier,
      Self.isDigest(applicationCommit, length: 40) || applicationCommit == "unknown",
      pythonVersion == "3.12.11", mlxAudioVersion == "0.4.7",
      modelRepository == WorkerConstants.chatterboxModelRepository,
      Self.isDigest(modelRevision, length: 40),
      modelRevision == WorkerConstants.chatterboxModelRevision,
      tokenizerRepository == WorkerConstants.chatterboxTokenizerRepository,
      Self.isDigest(tokenizerRevision, length: 32),
      tokenizerRevision == WorkerConstants.chatterboxTokenizerRevision,
      referenceAudioSHA256 == WorkerConstants.referenceAudioSHA256,
      runtimeReceipt.hasPrefix("sha256:"),
      Self.isDigest(String(runtimeReceipt.dropFirst(7)), length: 64), minimumMacOS == "14.0",
      architecture == "arm64", builtAtUTC.hasSuffix("Z")
    else { throw ReleaseManifestError.invalid }
  }

  public static func load(from url: URL) throws -> ReleaseManifest {
    let data = try Data(contentsOf: url)
    guard data.count <= 1_048_576, !JSONDuplicateKeyDetector.containsDuplicateKeys(data) else {
      throw ReleaseManifestError.invalid
    }
    return try StrictJSON.decode(Self.self, from: data)
  }

  private static func isDigest(_ value: String, length: Int) -> Bool {
    value.utf8.count == length
      && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
  }
}

public enum ReleaseManifestError: Error, Equatable, LocalizedError, Sendable {
  case invalid
  public var errorDescription: String? { "release_manifest_invalid" }
}

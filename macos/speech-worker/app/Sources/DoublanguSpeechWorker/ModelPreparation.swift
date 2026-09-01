import AVFAudio
import AVFoundation
import Foundation

public struct ReferenceAudioInfo: Equatable, Sendable {
  public let sha256: String
  public let sampleRateHz: Double
  public let channels: Int
  public let durationSeconds: Double
  public let sizeBytes: Int64
}

public struct ModelReceipt: Codable, Equatable, Sendable {
  public let schemaVersion: Int
  public let modelRepository: String
  public let modelRevision: String
  public let tokenizerRepository: String
  public let tokenizerRevision: String
  public let modelTreeSHA256: String
  public let tokenizerTreeSHA256: String

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case modelRepository = "model_repository"
    case modelRevision = "model_revision"
    case tokenizerRepository = "tokenizer_repository"
    case tokenizerRevision = "tokenizer_revision"
    case modelTreeSHA256 = "model_tree_sha256"
    case tokenizerTreeSHA256 = "tokenizer_tree_sha256"
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self)
    let container = try decoder.container(keyedBy: CodingKeys.self)
    schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    modelRepository = try container.decode(String.self, forKey: .modelRepository)
    modelRevision = try container.decode(String.self, forKey: .modelRevision)
    tokenizerRepository = try container.decode(String.self, forKey: .tokenizerRepository)
    tokenizerRevision = try container.decode(String.self, forKey: .tokenizerRevision)
    modelTreeSHA256 = try container.decode(String.self, forKey: .modelTreeSHA256)
    tokenizerTreeSHA256 = try container.decode(String.self, forKey: .tokenizerTreeSHA256)
    try validate()
  }

  public func validate() throws {
    guard schemaVersion == 1,
      modelRepository == WorkerConstants.chatterboxModelRepository,
      modelRevision == WorkerConstants.chatterboxModelRevision,
      tokenizerRepository == WorkerConstants.chatterboxTokenizerRepository,
      tokenizerRevision == WorkerConstants.chatterboxTokenizerRevision,
      Self.isDigest(modelTreeSHA256), Self.isDigest(tokenizerTreeSHA256)
    else { throw PreparationError.modelReceiptInvalid }
  }

  private static func isDigest(_ value: String) -> Bool {
    value.utf8.count == 64
      && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
  }
}

public enum PreparationError: Error, Equatable, LocalizedError, Sendable {
  case referenceMissing
  case referenceNotCanonical
  case referenceHashMismatch
  case storageGate
  case runtimeMissing
  case modelDownloadFailed
  case modelReceiptInvalid
  case processFailed

  public var errorDescription: String? {
    switch self {
    case .referenceMissing: return "reference_audio_missing"
    case .referenceNotCanonical: return "reference_audio_not_canonical"
    case .referenceHashMismatch: return "reference_audio_hash_mismatch"
    case .storageGate: return "storage_gate_failed"
    case .runtimeMissing: return "runtime_missing"
    case .modelDownloadFailed: return "model_download_failed"
    case .modelReceiptInvalid: return "model_receipt_invalid"
    case .processFailed: return "model_preparation_process_failed"
    }
  }
}

public enum ReferenceAudio {
  public static func inspect(
    url: URL, expectedSHA256: String = WorkerConstants.referenceAudioSHA256
  ) throws -> ReferenceAudioInfo {
    guard FileManager.default.fileExists(atPath: url.path) else {
      throw PreparationError.referenceMissing
    }
    let data = try Data(contentsOf: url, options: [.mappedIfSafe])
    let audio = try AVAudioFile(forReading: url)
    let format = audio.processingFormat
    guard format.sampleRate == 24_000, format.channelCount == 1,
      url.pathExtension.lowercased() == "wav"
    else { throw PreparationError.referenceNotCanonical }
    let info = ReferenceAudioInfo(
      sha256: sha256Hex(data), sampleRateHz: format.sampleRate, channels: Int(format.channelCount),
      durationSeconds: Double(audio.length) / format.sampleRate, sizeBytes: Int64(data.count))
    guard info.sha256 == expectedSHA256 else { throw PreparationError.referenceHashMismatch }
    return info
  }

  public static func installCanonical(from source: URL, to destination: URL) throws
    -> ReferenceAudioInfo
  {
    let info = try inspect(url: source)
    let data = try Data(contentsOf: source, options: [.mappedIfSafe])
    try AppPaths().writePrivate(data, to: destination)
    return info
  }
}

@MainActor
public final class ModelPreparationRunner {
  private let paths: AppPaths
  private let configuration: SpeechWorkerConfiguration
  private let fileManager: FileManager
  private let log: ((String) -> Void)?

  public init(
    paths: AppPaths, configuration: SpeechWorkerConfiguration, fileManager: FileManager = .default,
    log: ((String) -> Void)? = nil
  ) {
    self.paths = paths
    self.configuration = configuration
    self.fileManager = fileManager
    self.log = log
  }

  public func verifyReferenceAndStorage() throws -> ReferenceAudioInfo {
    guard let free = paths.freeBytes(), free >= WorkerConstants.minimumFreeBytes else {
      throw PreparationError.storageGate
    }
    return try ReferenceAudio.inspect(
      url: paths.referenceURL, expectedSHA256: configuration.referenceAudioHash)
  }

  public func prepareModel() async throws -> SetupReceipt {
    _ = try verifyReferenceAndStorage()
    guard fileManager.isExecutableFile(atPath: paths.bundledPythonURL.path),
      fileManager.fileExists(
        atPath: paths.bundledRuntimeRoot.appendingPathComponent("prepare_model.py").path)
    else { throw PreparationError.runtimeMissing }
    try paths.ensureDirectories()
    let modelPath = paths.modelURL(revision: configuration.modelRevision)
    let modelReceipt = paths.modelReceiptURL
    let executable = paths.bundledPythonURL
    let arguments = [
      paths.bundledRuntimeRoot.appendingPathComponent("prepare_model.py").path,
      "--model-revision", configuration.modelRevision,
      "--tokenizer-revision", configuration.tokenizerRevision,
      "--model-path", modelPath.path,
      "--cache-path", paths.downloadCacheRoot.path,
      "--receipt-path", modelReceipt.path,
    ]
    let environment = [
      "PYTHONNOUSERSITE": "1", "PYTHONDONTWRITEBYTECODE": "1",
      "HF_HOME": paths.downloadCacheRoot.path,
      "PYTHONPATH": paths.bundledRuntimeRoot.appendingPathComponent(
        "venv/lib/python3.12/site-packages"
      ).path,
    ]
    let terminationStatus: Int32
    do {
      terminationStatus = try await Task.detached(priority: .userInitiated) {
        let process = Process()
        process.executableURL = executable
        process.arguments = arguments
        process.environment = environment
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        process.waitUntilExit()
        return process.terminationStatus
      }.value
    } catch {
      throw PreparationError.modelDownloadFailed
    }
    guard terminationStatus == 0 else { throw PreparationError.modelDownloadFailed }
    guard
      fileManager.fileExists(atPath: modelPath.appendingPathComponent("model.safetensors").path),
      fileManager.fileExists(atPath: modelReceipt.path),
      let modelData = try? Data(contentsOf: modelReceipt),
      let modelReceiptValue = try? StrictJSON.decode(ModelReceipt.self, from: modelData),
      modelReceiptValue.modelRevision == configuration.modelRevision
    else { throw PreparationError.modelReceiptInvalid }
    try fileManager.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: modelReceipt.path)
    let runtimeHash = sha256Hex(try Data(contentsOf: paths.bundledLockURL))
    let receipt = SetupReceipt(
      pythonVersion: "3.12.11", mlxAudioVersion: "0.4.7", mlxVersion: "0.32.2",
      modelRepository: configuration.modelRepository, modelRevision: configuration.modelRevision,
      tokenizerRepository: configuration.tokenizerRepository,
      tokenizerRevision: configuration.tokenizerRevision,
      referenceAudioSHA256: configuration.referenceAudioHash,
      runtimeReceipt: "sha256:\(runtimeHash)",
      verifiedAtUTC: ISO8601DateFormatter().string(from: Date()))
    try paths.writePrivate(StrictJSON.encode(receipt), to: paths.setupReceiptURL)
    try receipt.validate()
    log?("model_preparation_completed")
    return receipt
  }
}

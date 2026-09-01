import Foundation

public enum JournalPhase: String, Codable, Equatable, Sendable {
  case rendering
  case ready
  case uploading
}

public struct JobJournalEntry: Codable, Equatable, Sendable {
  public let jobID: String
  public let attempt: Int
  public let leaseToken: String
  public let renderID: String
  public let requestHash: String
  public let jobType: String
  public let speechUnitID: String
  public let language: String
  public let unitKind: String
  public let spokenText: String
  public let contextPronunciationKey: String
  public let leaseExpiresAt: String
  public let limits: AudioLimits
  public let profile: SpeechProfile
  public let partialPath: String
  public let readyPath: String
  public var phase: JournalPhase
  public var artifact: ArtifactMetadata?
  public var updatedAtUTC: String
  public var terminalReason: String?

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case jobID = "job_id"
    case attempt
    case leaseToken = "lease_token"
    case renderID = "render_id"
    case requestHash = "request_hash"
    case jobType = "job_type"
    case speechUnitID = "speech_unit_id"
    case language
    case unitKind = "unit_kind"
    case spokenText = "spoken_text"
    case contextPronunciationKey = "context_pronunciation_key"
    case leaseExpiresAt = "lease_expires_at"
    case limits
    case profile
    case partialPath = "partial_path"
    case readyPath = "ready_path"
    case phase, artifact
    case updatedAtUTC = "updated_at_utc"
    case terminalReason = "terminal_reason"
  }

  public init(
    jobID: String, attempt: Int, leaseToken: String, renderID: String, requestHash: String,
    jobType: String, speechUnitID: String, language: String, unitKind: String, spokenText: String,
    contextPronunciationKey: String, leaseExpiresAt: String, limits: AudioLimits,
    profile: SpeechProfile, partialPath: String, readyPath: String, phase: JournalPhase,
    artifact: ArtifactMetadata? = nil,
    updatedAtUTC: String = ISO8601DateFormatter().string(from: Date()),
    terminalReason: String? = nil
  ) {
    self.jobID = jobID
    self.attempt = attempt
    self.leaseToken = leaseToken
    self.renderID = renderID
    self.requestHash = requestHash
    self.jobType = jobType
    self.speechUnitID = speechUnitID
    self.language = language
    self.unitKind = unitKind
    self.spokenText = spokenText
    self.contextPronunciationKey = contextPronunciationKey
    self.leaseExpiresAt = leaseExpiresAt
    self.limits = limits
    self.profile = profile
    self.partialPath = partialPath
    self.readyPath = readyPath
    self.phase = phase
    self.artifact = artifact
    self.updatedAtUTC = updatedAtUTC
    self.terminalReason = terminalReason
  }

  public init(from decoder: Decoder) throws {
    try StrictCoding.checkKeys(decoder, CodingKeys.self, optional: ["artifact", "terminal_reason"])
    let c = try decoder.container(keyedBy: CodingKeys.self)
    jobID = try c.decode(String.self, forKey: .jobID)
    attempt = try c.decode(Int.self, forKey: .attempt)
    leaseToken = try c.decode(String.self, forKey: .leaseToken)
    renderID = try c.decode(String.self, forKey: .renderID)
    requestHash = try c.decode(String.self, forKey: .requestHash)
    jobType = try c.decode(String.self, forKey: .jobType)
    speechUnitID = try c.decode(String.self, forKey: .speechUnitID)
    language = try c.decode(String.self, forKey: .language)
    unitKind = try c.decode(String.self, forKey: .unitKind)
    spokenText = try c.decode(String.self, forKey: .spokenText)
    contextPronunciationKey = try c.decode(String.self, forKey: .contextPronunciationKey)
    leaseExpiresAt = try c.decode(String.self, forKey: .leaseExpiresAt)
    limits = try c.decode(AudioLimits.self, forKey: .limits)
    profile = try c.decode(SpeechProfile.self, forKey: .profile)
    partialPath = try c.decode(String.self, forKey: .partialPath)
    readyPath = try c.decode(String.self, forKey: .readyPath)
    phase = try c.decode(JournalPhase.self, forKey: .phase)
    artifact = try c.decodeIfPresent(ArtifactMetadata.self, forKey: .artifact)
    updatedAtUTC = try c.decode(String.self, forKey: .updatedAtUTC)
    terminalReason = try c.decodeIfPresent(String.self, forKey: .terminalReason)
  }
}

public enum JournalError: Error, Equatable, LocalizedError, Sendable {
  case invalid
  case spoolFull
  case malformed(String)
  public var errorDescription: String? {
    switch self {
    case .invalid: return "journal_invalid"
    case .spoolFull: return "journal_full"
    case .malformed: return "journal_malformed"
    }
  }
}

public final class JobJournalStore: @unchecked Sendable {
  public let paths: AppPaths
  public let maximumBytes: Int64
  private let fileManager: FileManager
  private let lock = NSLock()

  public init(
    paths: AppPaths, maximumBytes: Int64 = WorkerConstants.maxSpoolBytes,
    fileManager: FileManager = .default
  ) {
    self.paths = paths
    self.maximumBytes = maximumBytes
    self.fileManager = fileManager
  }

  public func save(_ entry: JobJournalEntry) throws {
    lock.lock()
    defer { lock.unlock() }
    try paths.ensureParent(paths.journalURL(jobID: entry.jobID))
    let data = try StrictJSON.encode(entry)
    try atomicWriteLocked(data, to: paths.journalURL(jobID: entry.jobID))
  }

  public func load(jobID: String) throws -> JobJournalEntry? {
    lock.lock()
    defer { lock.unlock() }
    let url = paths.journalURL(jobID: jobID)
    guard fileManager.fileExists(atPath: url.path) else { return nil }
    do { return try StrictJSON.decode(JobJournalEntry.self, from: Data(contentsOf: url)) } catch {
      throw JournalError.malformed(jobID)
    }
  }

  public func list() throws -> [JobJournalEntry] {
    lock.lock()
    defer { lock.unlock() }
    guard
      let urls = try? fileManager.contentsOfDirectory(
        at: paths.spoolRoot, includingPropertiesForKeys: nil, options: [.skipsHiddenFiles])
    else { return [] }
    return try urls.filter { $0.pathExtension == "json" }.sorted {
      $0.lastPathComponent < $1.lastPathComponent
    }.map { url in
      do { return try StrictJSON.decode(JobJournalEntry.self, from: Data(contentsOf: url)) } catch {
        throw JournalError.malformed(url.lastPathComponent)
      }
    }
  }

  public func remove(jobID: String) throws {
    lock.lock()
    defer { lock.unlock() }
    for url in [
      paths.journalURL(jobID: jobID), paths.partialURL(jobID: jobID), paths.readyURL(jobID: jobID),
    ] where fileManager.fileExists(atPath: url.path) { try fileManager.removeItem(at: url) }
  }

  public func removeJournalOnly(jobID: String) throws {
    lock.lock()
    defer { lock.unlock() }
    let url = paths.journalURL(jobID: jobID)
    if fileManager.fileExists(atPath: url.path) { try fileManager.removeItem(at: url) }
  }

  public func spoolBytes() -> Int64 {
    lock.lock()
    defer { lock.unlock() }
    guard
      let urls = try? fileManager.contentsOfDirectory(
        at: paths.spoolRoot, includingPropertiesForKeys: [.fileSizeKey],
        options: [.skipsHiddenFiles])
    else { return 0 }
    return urls.reduce(Int64(0)) { total, url in
      total + Int64((try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
    }
  }

  public func isFull(reserving additionalBytes: Int64 = 0) -> Bool {
    spoolBytes() + max(0, additionalBytes) >= maximumBytes
  }

  private func atomicWriteLocked(_ data: Data, to url: URL) throws {
    let temporary = url.deletingLastPathComponent().appendingPathComponent(
      ".\(url.lastPathComponent).partial.\(UUID().uuidString)")
    FileManager.default.createFile(
      atPath: temporary.path, contents: nil,
      attributes: [.posixPermissions: NSNumber(value: Int16(0o600))])
    let handle = try FileHandle(forWritingTo: temporary)
    try handle.write(contentsOf: data)
    try handle.synchronize()
    try handle.close()
    try fileManager.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: temporary.path)
    if fileManager.fileExists(atPath: url.path) {
      _ = try fileManager.replaceItemAt(url, withItemAt: temporary)
    } else {
      try fileManager.moveItem(at: temporary, to: url)
    }
  }
}

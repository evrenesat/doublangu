import Foundation

func parseLeaseExpiry(_ value: String) -> Date? {
  let fractional = ISO8601DateFormatter()
  fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
  if let date = fractional.date(from: value) { return date }
  return ISO8601DateFormatter().date(from: value)
}

public enum WorkerLoopStatus: Equatable, Sendable {
  case stopped
  case ready
  case loadingModel
  case rendering(jobType: String)
  case uploading(jobType: String)
  case offline
  case spoolFull
  case profileMismatch
  case failed(code: String)

  public var label: String {
    switch self {
    case .stopped: return "Stopped"
    case .ready: return "Ready"
    case .loadingModel: return "Loading model"
    case .rendering(let jobType): return "Rendering \(jobType)"
    case .uploading(let jobType): return "Uploading \(jobType)"
    case .offline: return "Offline / backing off"
    case .spoolFull: return "Spool full"
    case .profileMismatch: return "Profile mismatch"
    case .failed: return "Failed"
    }
  }
}

public final class CancellationFlag: @unchecked Sendable {
  private let lock = NSLock()
  private var value = false
  private var serverRequested = false
  public init() {}
  public var isCancelled: Bool {
    lock.lock()
    defer { lock.unlock() }
    return value
  }
  public var isServerCancellation: Bool {
    lock.lock()
    defer { lock.unlock() }
    return serverRequested
  }
  public func cancel(server: Bool = false) {
    lock.lock()
    value = true
    serverRequested = serverRequested || server
    lock.unlock()
  }
}

@MainActor
public final class LeaseLoop {
  public private(set) var status: WorkerLoopStatus = .stopped
  public private(set) var lastServerContact: Date?
  public private(set) var currentJobType: String?
  public var statusChanged: ((WorkerLoopStatus) -> Void)?
  public var log: ((String) -> Void)?

  private let client: WorkerClienting
  private let configuration: SpeechWorkerConfiguration
  private let paths: AppPaths
  private let journal: JobJournalStore
  private let avSpeech: AVSpeechRenderer
  private let chatterbox: ChatterboxRenderer
  private let postprocessor: AudioPostprocessor
  private let sleepActivity: SleepActivityManaging
  private let clock: () -> Date
  private let fileManager: FileManager
  private var task: Task<Void, Never>?
  private var heartbeatTask: Task<Void, Never>?
  private var currentCancellation: CancellationFlag?
  private var running = false
  private var failureBackoff: TimeInterval = 1

  public init(
    client: WorkerClienting,
    configuration: SpeechWorkerConfiguration,
    paths: AppPaths,
    journal: JobJournalStore,
    avSpeech: AVSpeechRenderer = AVSpeechRenderer(),
    chatterbox: ChatterboxRenderer,
    postprocessor: AudioPostprocessor = AudioPostprocessor(),
    sleepActivity: SleepActivityManaging = SystemSleepActivity(),
    clock: @escaping () -> Date = Date.init,
    fileManager: FileManager = .default
  ) {
    self.client = client
    self.configuration = configuration
    self.paths = paths
    self.journal = journal
    self.avSpeech = avSpeech
    self.chatterbox = chatterbox
    self.postprocessor = postprocessor
    self.sleepActivity = sleepActivity
    self.clock = clock
    self.fileManager = fileManager
  }

  public func start() {
    guard task == nil else { return }
    running = true
    setStatus(.ready)
    task = Task { [weak self] in
      await self?.run()
    }
  }

  public func stop() {
    running = false
    currentCancellation?.cancel()
    if currentJobType == "tts.chatterbox.v3" { chatterbox.supervisor.cancelGeneration() }
    heartbeatTask?.cancel()
    heartbeatTask = nil
    task?.cancel()
    task = nil
    setStatus(.stopped)
  }

  public func cancelCurrentJob() {
    currentCancellation?.cancel()
    if currentJobType == "tts.chatterbox.v3" { chatterbox.supervisor.cancelGeneration() }
  }

  private func run() async {
    while running && !Task.isCancelled {
      do {
        try await recoverJournals()
        failureBackoff = 1
        break
      } catch is CancellationError {
        break
      } catch let error as WorkerClientError {
        log?("journal_recovery_failed")
        handleClientError(error)
      } catch {
        log?("journal_recovery_failed")
        setStatus(.failed(code: "journal_recovery_failed"))
      }
      let delay = failureBackoff
      failureBackoff = min(300, failureBackoff * 2)
      try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
    }
    while running && !Task.isCancelled {
      if !(await retryPendingUploads()) {
        let delay = failureBackoff
        failureBackoff = min(300, failureBackoff * 2)
        try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
        continue
      }
      if journal.isFull() {
        setStatus(.spoolFull)
        try? await Task.sleep(nanoseconds: 5_000_000_000)
        continue
      }
      do {
        setStatus(.ready)
        guard let lease = try await client.lease(capabilities: configuration.capabilities()) else {
          try? await Task.sleep(nanoseconds: 1_000_000_000)
          continue
        }
        lastServerContact = clock()
        failureBackoff = 1
        try await process(lease: lease)
      } catch is CancellationError {
        break
      } catch let error as ProtocolError {
        setStatus(.profileMismatch)
        log?(error.localizedDescription)
        running = false
      } catch let error as WorkerClientError {
        handleClientError(error)
        if !running { break }
        let delay = failureBackoff
        failureBackoff = min(300, failureBackoff * 2)
        try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
      } catch {
        setStatus(.failed(code: sanitizedCode(error)))
        try? await Task.sleep(nanoseconds: UInt64(failureBackoff * 1_000_000_000))
        failureBackoff = min(300, failureBackoff * 2)
      }
    }
    if running && status != .stopped { setStatus(.stopped) }
  }

  private func recoverJournals() async throws {
    for entry in try journal.list() {
      guard running else { return }
      switch entry.phase {
      case .ready, .uploading:
        guard fileManager.fileExists(atPath: entry.readyPath) else {
          try journal.remove(jobID: entry.jobID)
          continue
        }
        _ = await uploadPending(entry)
      case .rendering:
        let lease = LeaseResponse(
          protocolVersion: configuration.protocolVersion, jobID: entry.jobID,
          attempt: entry.attempt, leaseToken: entry.leaseToken,
          leaseExpiresAt: entry.leaseExpiresAt, jobType: entry.jobType, renderID: entry.renderID,
          requestHash: entry.requestHash, speechUnitID: entry.speechUnitID,
          language: entry.language, unitKind: entry.unitKind, spokenText: entry.spokenText,
          contextPronunciationKey: entry.contextPronunciationKey, profile: entry.profile,
          limits: entry.limits)
        do {
          let heartbeat = try await client.heartbeat(
            jobID: entry.jobID, leaseToken: entry.leaseToken, attempt: entry.attempt,
            progressPercent: 1)
          lastServerContact = clock()
          if heartbeat.cancelRequested {
            try journal.remove(jobID: entry.jobID)
            continue
          }
          let renewedLease = LeaseResponse(
            protocolVersion: lease.protocolVersion, jobID: lease.jobID, attempt: lease.attempt,
            leaseToken: lease.leaseToken, leaseExpiresAt: heartbeat.leaseExpiresAt,
            jobType: lease.jobType, renderID: lease.renderID, requestHash: lease.requestHash,
            speechUnitID: lease.speechUnitID, language: lease.language, unitKind: lease.unitKind,
            spokenText: lease.spokenText, contextPronunciationKey: lease.contextPronunciationKey,
            profile: lease.profile, limits: lease.limits)
          try await process(lease: renewedLease, existingEntry: entry)
        } catch let error as WorkerClientError {
          if case .http(let status) = error, status == 409 {
            try journal.remove(jobID: entry.jobID)
          } else {
            throw error
          }
        }
      }
    }
  }

  private func process(lease: LeaseResponse, existingEntry: JobJournalEntry? = nil) async throws {
    try validate(lease)
    var entry =
      existingEntry
      ?? JobJournalEntry(
        jobID: lease.jobID, attempt: lease.attempt, leaseToken: lease.leaseToken,
        renderID: lease.renderID,
        requestHash: lease.requestHash, jobType: lease.jobType, speechUnitID: lease.speechUnitID,
        language: lease.language, unitKind: lease.unitKind,
        spokenText: lease.spokenText, contextPronunciationKey: lease.contextPronunciationKey,
        leaseExpiresAt: lease.leaseExpiresAt,
        limits: lease.limits, profile: lease.profile,
        partialPath: paths.partialURL(jobID: lease.jobID).path,
        readyPath: paths.readyURL(jobID: lease.jobID).path, phase: .rendering
      )
    if existingEntry == nil { try journal.save(entry) }
    let flag = CancellationFlag()
    currentCancellation = flag
    currentJobType = lease.jobType
    sleepActivity.acquire()
    defer {
      sleepActivity.release()
      currentCancellation = nil
      currentJobType = nil
    }
    let heartbeat = startHeartbeat(lease: lease, flag: flag)
    heartbeatTask = heartbeat
    defer {
      heartbeat.cancel()
      heartbeatTask = nil
    }
    do {
      if entry.phase == .rendering {
        setStatus(
          lease.jobType == "tts.chatterbox.v3" ? .loadingModel : .rendering(jobType: lease.jobType))
        let artifact = try await render(lease: lease, entry: entry, flag: flag)
        guard !flag.isCancelled else { throw CancellationError() }
        if fileManager.fileExists(atPath: entry.readyPath) {
          try fileManager.removeItem(atPath: entry.readyPath)
        }
        try fileManager.moveItem(atPath: entry.partialPath, toPath: entry.readyPath)
        entry.phase = .ready
        entry.artifact = artifact
        entry.updatedAtUTC = isoNow()
        try journal.save(entry)
      }
      guard entry.artifact != nil, fileManager.fileExists(atPath: entry.readyPath) else {
        throw JournalError.invalid
      }
      setStatus(.uploading(jobType: lease.jobType))
      var uploading = entry
      uploading.phase = .uploading
      uploading.updatedAtUTC = isoNow()
      try journal.save(uploading)
      _ = await upload(entry)
    } catch is CancellationError {
      if flag.isServerCancellation {
        var canceled = entry
        canceled.terminalReason = "cancel_requested"
        canceled.updatedAtUTC = isoNow()
        try? journal.save(canceled)
        try? journal.remove(jobID: entry.jobID)
      } else if entry.phase == .rendering {
        try? fileManager.removeItem(atPath: entry.partialPath)
      }
    } catch {
      try? fileManager.removeItem(atPath: entry.partialPath)
      let code = sanitizedCode(error)
      do {
        try await client.fail(
          jobID: entry.jobID, leaseToken: entry.leaseToken, attempt: entry.attempt, errorCode: code,
          retry: true)
        try journal.remove(jobID: entry.jobID)
      } catch {
        log?("failure_acknowledgement_unavailable")
        throw error
      }
    }
  }

  private func render(lease: LeaseResponse, entry: JobJournalEntry, flag: CancellationFlag)
    async throws -> ArtifactMetadata
  {
    let partial = URL(fileURLWithPath: entry.partialPath)
    try paths.ensureParent(partial)
    if lease.jobType == "tts.avspeech.v1" {
      let source = paths.spoolRoot.appendingPathComponent("\(lease.jobID).source.wav")
      defer { try? fileManager.removeItem(at: source) }
      _ = try await avSpeech.render(
        text: lease.spokenText, profile: lease.profile, outputURL: source, limits: lease.limits,
        cancellation: { flag.isCancelled })
      guard !flag.isCancelled else { throw CancellationError() }
      return try postprocessor.process(
        inputURL: source, outputURL: partial, requestHash: lease.requestHash,
        unitKind: lease.unitKind, limits: lease.limits)
    }
    return try await chatterbox.render(
      lease: lease, partialURL: partial, requestHash: lease.requestHash,
      cancellation: { flag.isCancelled })
  }

  private func startHeartbeat(lease: LeaseResponse, flag: CancellationFlag) -> Task<Void, Never> {
    Task { [weak self] in
      while !Task.isCancelled && !(flag.isCancelled) {
        try? await Task.sleep(nanoseconds: 30_000_000_000)
        guard !Task.isCancelled, !flag.isCancelled else { return }
        do {
          let response = try await self?.client.heartbeat(
            jobID: lease.jobID, leaseToken: lease.leaseToken, attempt: lease.attempt,
            progressPercent: 50)
          await MainActor.run {
            self?.lastServerContact = self?.clock()
            if response?.cancelRequested == true { flag.cancel(server: true) }
          }
        } catch let error as WorkerClientError {
          if case .http(let status) = error, status == 409 {
            flag.cancel(server: true)
          } else {
            await MainActor.run { self?.log?("heartbeat_failed") }
          }
        } catch {
          await MainActor.run { self?.log?("heartbeat_failed") }
        }
      }
    }
  }

  private func validate(_ lease: LeaseResponse) throws {
    try lease.validate()
    guard validULID(lease.jobID), validULID(lease.renderID), validULID(lease.speechUnitID),
      lease.profile.id.map(validULID) == true,
      lease.profile.matchesByteAffecting(
        lease.jobType == "tts.avspeech.v1"
          ? configuration.avSpeechProfile : configuration.chatterboxProfile),
      requestHash(for: lease) == lease.requestHash,
      lease.limits.maxBytes == expectedLimits(for: lease.unitKind).maxBytes,
      lease.limits.maxDurationMS == expectedLimits(for: lease.unitKind).maxDurationMS,
      parseLeaseExpiry(lease.leaseExpiresAt).map({ $0 > clock() }) == true
    else { throw ProtocolError.invalidValue("lease_identity") }
  }

  private func expectedLimits(for unitKind: String) -> AudioLimits {
    switch unitKind {
    case "sentence": return AudioLimits(maxBytes: 64 << 20, maxDurationMS: 180_000)
    case "phrase": return AudioLimits(maxBytes: 2 << 20, maxDurationMS: 30_000)
    default: return AudioLimits(maxBytes: 2 << 20, maxDurationMS: 15_000)
    }
  }

  private func requestHash(for lease: LeaseResponse) -> String {
    var data = Data()
    func append(_ value: String) {
      var length = UInt64(value.utf8.count).bigEndian
      withUnsafeBytes(of: &length) { data.append(contentsOf: $0) }
      data.append(contentsOf: value.utf8)
    }
    append("doublangu.audio-request.v1")
    append(lease.spokenText)
    append(lease.language)
    append(lease.unitKind)
    append(lease.contextPronunciationKey)
    append(lease.profile.engine)
    append(lease.profile.modelRevision)
    append(lease.profile.language)
    append(lease.profile.voiceIdentifier)
    append(lease.profile.referenceAudioHash)
    var speed = Int64(lease.profile.speedMilli).bigEndian
    withUnsafeBytes(of: &speed) { data.append(contentsOf: $0) }
    var pitch = Int64(lease.profile.pitchCents).bigEndian
    withUnsafeBytes(of: &pitch) { data.append(contentsOf: $0) }
    append(lease.profile.mappingVersion)
    append(lease.profile.codec)
    append(lease.profile.mimeType)
    var rate = Int64(lease.profile.sampleRateHz).bigEndian
    withUnsafeBytes(of: &rate) { data.append(contentsOf: $0) }
    var channels = Int64(lease.profile.channels).bigEndian
    withUnsafeBytes(of: &channels) { data.append(contentsOf: $0) }
    append(WorkerConstants.audioNormalizationVersion)
    return sha256Hex(data)
  }

  private func validULID(_ value: String) -> Bool {
    value.count == 26 && value.allSatisfy { "0123456789ABCDEFGHJKMNPQRSTVWXYZ".contains($0) }
  }

  private func handleClientError(_ error: WorkerClientError) {
    switch error {
    case .http(let status) where status == 401 || status == 403:
      setStatus(.failed(code: "worker_authentication"))
      running = false
    case .http(let status) where status >= 500:
      setStatus(.offline)
    case .transport:
      setStatus(.offline)
    default:
      setStatus(.failed(code: error.localizedDescription))
    }
  }

  private func setStatus(_ value: WorkerLoopStatus) {
    status = value
    statusChanged?(value)
  }

  private func sanitizedCode(_ error: Error) -> String {
    if let error = error as? ChatterboxError {
      return "v1.\(error.errorDescription ?? "chatterbox_failed")"
    }
    if let error = error as? AVSpeechRenderError {
      return "v1.\(error.errorDescription ?? "avspeech_failed")"
    }
    if let error = error as? AudioProcessingError {
      return "v1.\(error.errorDescription ?? "audio_failed")"
    }
    return "v1.audio_generation_failed"
  }

  private func isoNow() -> String { ISO8601DateFormatter().string(from: clock()) }
  private func retryPendingUploads() async -> Bool {
    guard let entries = try? journal.list() else {
      setStatus(.failed(code: "journal_recovery_failed"))
      running = false
      return false
    }
    for entry in entries where entry.phase == .ready || entry.phase == .uploading {
      if !(await uploadPending(entry)) { return false }
    }
    return true
  }

  private func uploadPending(_ entry: JobJournalEntry) async -> Bool {
    sleepActivity.acquire()
    defer { sleepActivity.release() }
    return await upload(entry)
  }

  @discardableResult
  private func upload(_ entry: JobJournalEntry) async -> Bool {
    guard let artifact = entry.artifact else {
      setStatus(.failed(code: "journal_invalid"))
      running = false
      return false
    }
    setStatus(.uploading(jobType: entry.jobType))
    do {
      try await client.complete(
        jobID: entry.jobID,
        metadata: CompletionMetadata(
          attempt: entry.attempt, leaseToken: entry.leaseToken, artifact: artifact),
        audioURL: URL(fileURLWithPath: entry.readyPath))
      lastServerContact = clock()
      try journal.remove(jobID: entry.jobID)
      if entry.jobType == "tts.chatterbox.v3" { chatterbox.scheduleIdleUnload() }
      return true
    } catch let error as WorkerClientError {
      if case .http(let status) = error, status == 409 {
        try? journal.remove(jobID: entry.jobID)
        return true
      }
      setStatus(statusFor(error))
      if shouldStopFor(error) { running = false }
      return false
    } catch {
      setStatus(.offline)
      return false
    }
  }

  private func shouldStopFor(_ error: WorkerClientError) -> Bool {
    switch error {
    case .transport: return false
    case .http(let status): return status < 500 && status != 409
    default: return true
    }
  }

  private func statusFor(_ error: WorkerClientError) -> WorkerLoopStatus {
    switch error {
    case .transport: return .offline
    case .http(let status) where status >= 500: return .offline
    default: return .failed(code: error.localizedDescription)
    }
  }
}

import Foundation
import XCTest

@testable import DoublanguSpeechWorker

@MainActor
final class LeaseLoopTests: XCTestCase {
  func testLeaseExpiryAcceptsServerMillisecondsAndWholeSeconds() {
    XCTAssertNotNil(parseLeaseExpiry("2026-09-01T18:58:36.104Z"))
    XCTAssertNotNil(parseLeaseExpiry("2026-09-01T18:58:36Z"))
    XCTAssertNil(parseLeaseExpiry("not-a-timestamp"))
  }

  func testTransientCompletionRetainsReadyArtifactAndRetriesWithoutRenderingAgain() async throws {
    let root = temporaryRoot("lease-loop-upload-retry")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let configuration = SpeechWorkerConfiguration.default(paths: paths)
    let audio = Data("ready-audio".utf8)
    try audio.write(to: paths.readyURL(jobID: testJobID), options: .atomic)
    let artifact = ArtifactMetadata(
      requestHash: String(repeating: "a", count: 64),
      sha256: sha256Hex(audio),
      sizeBytes: Int64(audio.count),
      durationMS: 100
    )
    let entry = JobJournalEntry(
      jobID: testJobID,
      attempt: 1,
      leaseToken: String(repeating: "t", count: 40),
      renderID: testRenderID,
      requestHash: String(repeating: "a", count: 64),
      jobType: "tts.chatterbox.v3",
      speechUnitID: testSpeechUnitID,
      language: "nl",
      unitKind: "sentence",
      spokenText: "Dit is een zin.",
      contextPronunciationKey: "",
      leaseExpiresAt: ISO8601DateFormatter().string(from: Date().addingTimeInterval(90)),
      limits: AudioLimits(maxBytes: 64 << 20, maxDurationMS: 180_000),
      profile: configuration.chatterboxProfile,
      partialPath: paths.partialURL(jobID: testJobID).path,
      readyPath: paths.readyURL(jobID: testJobID).path,
      phase: .ready,
      artifact: artifact
    )
    let journal = JobJournalStore(paths: paths)
    try journal.save(entry)

    let client = RecordingWorkerClient(transientCompletionFailures: 1)
    let process = FakeWorkerProcess()
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: configuration,
      process: process,
      identityProvider: FixedIdentityProvider(),
      portAllocator: FixedPortAllocator(),
      requester: RecordingHTTPRequester(responses: []),
      preparationOverride: {}
    )
    let loop = LeaseLoop(
      client: client,
      configuration: configuration,
      paths: paths,
      journal: journal,
      chatterbox: ChatterboxRenderer(
        supervisor: supervisor, configuration: configuration, paths: paths),
      sleepActivity: RecordingSleepActivity()
    )

    loop.start()
    while client.completeCallCount < 2 {
      try await Task.sleep(nanoseconds: 10_000_000)
    }
    loop.stop()

    XCTAssertEqual(client.completeCallCount, 2)
    XCTAssertLessThanOrEqual(client.leaseCallCount, 1)
    XCTAssertEqual(process.launchCount, 0)
    XCTAssertNil(try journal.load(jobID: testJobID))
    XCTAssertFalse(FileManager.default.fileExists(atPath: paths.readyURL(jobID: testJobID).path))
  }

  func testStartupOfflineRenderingJournalBlocksLeaseAcquisition() async throws {
    let root = temporaryRoot("lease-loop-recovery-gate")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let configuration = SpeechWorkerConfiguration.default(paths: paths)
    let lease = testLease(configuration: configuration)
    let journal = JobJournalStore(paths: paths)
    try journal.save(
      JobJournalEntry(
        jobID: lease.jobID,
        attempt: lease.attempt,
        leaseToken: lease.leaseToken,
        renderID: lease.renderID,
        requestHash: lease.requestHash,
        jobType: lease.jobType,
        speechUnitID: lease.speechUnitID,
        language: lease.language,
        unitKind: lease.unitKind,
        spokenText: lease.spokenText,
        contextPronunciationKey: lease.contextPronunciationKey,
        leaseExpiresAt: lease.leaseExpiresAt,
        limits: lease.limits,
        profile: lease.profile,
        partialPath: paths.partialURL(jobID: lease.jobID).path,
        readyPath: paths.readyURL(jobID: lease.jobID).path,
        phase: .rendering
      ))

    let client = RecordingWorkerClient(
      transientCompletionFailures: 0, heartbeatFailure: .transport)
    let process = FakeWorkerProcess()
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: configuration,
      process: process,
      identityProvider: FixedIdentityProvider(),
      portAllocator: FixedPortAllocator(),
      requester: RecordingHTTPRequester(responses: []),
      preparationOverride: {}
    )
    let loop = LeaseLoop(
      client: client,
      configuration: configuration,
      paths: paths,
      journal: journal,
      chatterbox: ChatterboxRenderer(
        supervisor: supervisor, configuration: configuration, paths: paths),
      sleepActivity: RecordingSleepActivity()
    )

    loop.start()
    defer { loop.stop() }
    for _ in 0..<100 where client.heartbeatCallCount == 0 {
      try await Task.sleep(nanoseconds: 10_000_000)
    }
    XCTAssertGreaterThan(client.heartbeatCallCount, 0)
    try await Task.sleep(nanoseconds: 50_000_000)
    XCTAssertEqual(client.leaseCallCount, 0)
  }
}

private final class RecordingWorkerClient: WorkerClienting, @unchecked Sendable {
  private let lock = NSLock()
  private let transientCompletionFailures: Int
  private let heartbeatFailure: WorkerClientError?
  private var completions = 0
  private var leases = 0
  private var heartbeats = 0

  init(transientCompletionFailures: Int, heartbeatFailure: WorkerClientError? = nil) {
    self.transientCompletionFailures = transientCompletionFailures
    self.heartbeatFailure = heartbeatFailure
  }

  var completeCallCount: Int {
    lock.lock()
    defer { lock.unlock() }
    return completions
  }

  var leaseCallCount: Int {
    lock.lock()
    defer { lock.unlock() }
    return leases
  }

  var heartbeatCallCount: Int {
    lock.lock()
    defer { lock.unlock() }
    return heartbeats
  }

  func enroll(
    name _: String, capabilities _: [WorkerCapability], softwareVersion _: String,
    enrollmentToken _: String
  ) async throws -> EnrollmentResponse {
    throw WorkerClientError.invalidResponse
  }

  func lease(capabilities _: [WorkerCapability]) async throws -> LeaseResponse? {
    lock.withLock { leases += 1 }
    return nil
  }

  func heartbeat(jobID _: String, leaseToken _: String, attempt _: Int, progressPercent _: Int)
    async throws -> HeartbeatResponse
  {
    lock.withLock { heartbeats += 1 }
    if let heartbeatFailure { throw heartbeatFailure }
    return try StrictJSON.decode(
      HeartbeatResponse.self,
      from: Data(
        "{\"protocol_version\":\"speech-worker.v1\",\"cancel_requested\":false,\"lease_expires_at\":\"2026-09-01T12:00:00Z\",\"progress_percent\":50}"
          .utf8)
    )
  }

  func complete(jobID _: String, metadata _: CompletionMetadata, audioURL _: URL) async throws {
    let call = lock.withLock {
      completions += 1
      return completions
    }
    if call <= transientCompletionFailures { throw WorkerClientError.transport }
  }

  func fail(
    jobID _: String, leaseToken _: String, attempt _: Int, errorCode _: String, retry _: Bool
  ) async throws {}
}

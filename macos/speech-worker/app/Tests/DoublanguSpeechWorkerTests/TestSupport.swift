import Foundation
import XCTest

@testable import DoublanguSpeechWorker

let testJobID = "01J00000000000000000000001"
let testRenderID = "01J00000000000000000000002"
let testSpeechUnitID = "01J00000000000000000000003"
let testProfileID = "01J00000000000000000000004"

func temporaryRoot(_ name: String) -> URL {
  FileManager.default.temporaryDirectory.appendingPathComponent(
    "doublangu-\(name)-\(UUID().uuidString)", isDirectory: true)
}

func testProfile(engine: String = "avspeech", id: String? = testProfileID) -> SpeechProfile {
  if engine == "chatterbox" {
    return SpeechProfile(
      id: id,
      engine: "chatterbox",
      modelRevision: WorkerConstants.chatterboxModelRevision,
      language: "nl",
      voiceIdentifier: WorkerConstants.chatterboxVoiceIdentifier,
      referenceAudioHash: WorkerConstants.referenceAudioSHA256,
      speedMilli: 1000,
      pitchCents: 0,
      mappingVersion: WorkerConstants.chatterboxMappingVersion
    )
  }
  return SpeechProfile(
    id: id,
    engine: "avspeech",
    modelRevision: WorkerConstants.avSpeechModelRevision,
    language: "nl",
    voiceIdentifier: WorkerConstants.avSpeechVoiceIdentifier,
    referenceAudioHash: "",
    speedMilli: 1000,
    pitchCents: 0,
    mappingVersion: WorkerConstants.avSpeechMappingVersion
  )
}

func testLease(
  configuration: SpeechWorkerConfiguration, jobType: String = "tts.avspeech.v1",
  unitKind: String = "word", expiresAt: String? = nil,
  requestHash: String = String(repeating: "a", count: 64)
) -> LeaseResponse {
  let profile =
    jobType == "tts.chatterbox.v3" ? configuration.chatterboxProfile : configuration.avSpeechProfile
  return LeaseResponse(
    protocolVersion: WorkerConstants.protocolVersion,
    jobID: testJobID,
    attempt: 1,
    leaseToken: String(repeating: "t", count: 40),
    leaseExpiresAt: expiresAt ?? ISO8601DateFormatter().string(from: Date().addingTimeInterval(90)),
    jobType: jobType,
    renderID: testRenderID,
    requestHash: requestHash,
    speechUnitID: testSpeechUnitID,
    language: "nl",
    unitKind: unitKind,
    spokenText: unitKind == "sentence" ? "Dit is een zin." : "hallo",
    contextPronunciationKey: "",
    profile: profile,
    limits: AudioLimits(
      maxBytes: unitKind == "sentence" ? 64 << 20 : 2 << 20,
      maxDurationMS: unitKind == "sentence" ? 180_000 : unitKind == "phrase" ? 30_000 : 15_000)
  )
}

final class RecordingHTTPRequester: HTTPRequesting, @unchecked Sendable {
  private let lock = NSLock()
  private var queuedResponses: [HTTPResponse]
  private var recordedRequests: [URLRequest] = []

  init(responses: [HTTPResponse]) { queuedResponses = responses }

  var requests: [URLRequest] {
    lock.lock()
    defer { lock.unlock() }
    return recordedRequests
  }

  func send(_ request: URLRequest) async throws -> HTTPResponse {
    lock.withLock {
      recordedRequests.append(request)
      return queuedResponses.isEmpty ? HTTPResponse(statusCode: 500) : queuedResponses.removeFirst()
    }
  }
}

@MainActor
final class FakeWorkerProcess: WorkerProcessManaging {
  private(set) var processIdentifier: Int32 = 4242
  private(set) var executablePath: String?
  var startIdentity: String? = "test-start"
  private(set) var isRunning = false
  var terminationHandler: (() -> Void)?
  private(set) var launchCount = 0
  private(set) var terminateCount = 0
  private(set) var killCount = 0
  var shouldFailLaunch = false
  private(set) var arguments: [String] = []
  private(set) var environment: [String: String] = [:]

  func launch(
    executableURL: URL, arguments: [String], environment: [String: String], workingDirectory: URL?
  ) throws {
    if shouldFailLaunch { throw CocoaError(.fileNoSuchFile) }
    launchCount += 1
    executablePath = executableURL.path
    self.arguments = arguments
    self.environment = environment
    isRunning = true
  }

  func terminate() {
    terminateCount += 1
    isRunning = false
  }

  func kill() {
    killCount += 1
    isRunning = false
  }
}

final class FixedIdentityProvider: ProcessIdentityProviding, @unchecked Sendable {
  var identityValue: ProcessIdentity?
  init(identity: ProcessIdentity? = nil) { identityValue = identity }
  func identity(for _: Int32) -> ProcessIdentity? { identityValue }
}

final class FixedPortAllocator: LoopbackPortAllocating, @unchecked Sendable {
  let port: Int
  init(port: Int = 38_700) { self.port = port }
  func allocate(range: ClosedRange<Int>) throws -> Int {
    guard range.contains(port) else { throw ChatterboxError.portUnavailable }
    return port
  }
}

final class RecordingSleepActivity: SleepActivityManaging, @unchecked Sendable {
  private(set) var acquireCount = 0
  private(set) var releaseCount = 0
  func acquire() { acquireCount += 1 }
  func release() { releaseCount += 1 }
}

final class RecordingLoginItem: LoginItemManaging, @unchecked Sendable {
  var isEnabled = false
  func setEnabled(_ enabled: Bool) throws { isEnabled = enabled }
}

func fakeWAV() -> Data {
  var data = Data(repeating: 0, count: 64)
  data.replaceSubrange(0..<4, with: Data("RIFF".utf8))
  data.replaceSubrange(8..<12, with: Data("WAVE".utf8))
  return data
}

func fileMode(_ url: URL) -> Int16? {
  (try? FileManager.default.attributesOfItem(atPath: url.path)[.posixPermissions] as? NSNumber).map
  { Int16(truncating: $0) }
}

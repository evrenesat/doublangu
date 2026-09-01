import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class ProtocolAndPathsTests: XCTestCase {
  func testConfigurationRoundTripsWithOptionalIdentityFields() throws {
    let root = temporaryRoot("configuration")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    let configuration = SpeechWorkerConfiguration.default(paths: paths)

    let decoded = try StrictJSON.decode(
      SpeechWorkerConfiguration.self, from: StrictJSON.encode(configuration))

    XCTAssertEqual(decoded, configuration)
    XCTAssertEqual(decoded.baseURL.absoluteString, "https://nlrn.evren.io/beta")
    XCTAssertEqual(decoded.avSpeechProfile.voiceIdentifier, WorkerConstants.avSpeechVoiceIdentifier)
    XCTAssertEqual(
      decoded.chatterboxProfile.referenceAudioHash, WorkerConstants.referenceAudioSHA256)
  }

  func testStrictJSONRejectsUnknownAndDuplicateKeys() throws {
    let valid =
      "{\"engine\":\"avspeech\",\"languages\":[\"nl\"],\"unit_kinds\":[\"word\"],\"max_bytes\":1,\"max_duration_ms\":1}"
    XCTAssertNoThrow(try StrictJSON.decode(WorkerCapability.self, from: Data(valid.utf8)))

    let unknown = valid.replacingOccurrences(of: "}", with: ",\"extra\":true}")
    XCTAssertThrowsError(try StrictJSON.decode(WorkerCapability.self, from: Data(unknown.utf8)))

    let duplicate = valid.replacingOccurrences(
      of: "\"engine\":\"avspeech\"", with: "\"engine\":\"avspeech\",\"engine\":\"avspeech\"")
    XCTAssertTrue(JSONDuplicateKeyDetector.containsDuplicateKeys(Data(duplicate.utf8)))
    XCTAssertThrowsError(try StrictJSON.decode(WorkerCapability.self, from: Data(duplicate.utf8)))
  }

  func testLeaseDecodeAcceptsLowercaseSHA256WithDigits() throws {
    let configuration = SpeechWorkerConfiguration.default(
      paths: AppPaths(rootOverride: temporaryRoot("lease-digest")))
    let digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    let encoded = try StrictJSON.encode(
      testLease(configuration: configuration, requestHash: digest))

    let decoded = try StrictJSON.decode(LeaseResponse.self, from: encoded)

    XCTAssertEqual(decoded.requestHash, digest)
  }

  func testPathsAndJournalUsePrivateJobScopedFiles() throws {
    let root = temporaryRoot("journal")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()

    XCTAssertEqual(paths.journalURL(jobID: testJobID).lastPathComponent, "\(testJobID).json")
    XCTAssertEqual(paths.partialURL(jobID: testJobID).lastPathComponent, "\(testJobID).partial")
    XCTAssertEqual(paths.readyURL(jobID: testJobID).lastPathComponent, "\(testJobID).ready")
    XCTAssertEqual(fileMode(paths.spoolRoot), 0o700)

    let entry = JobJournalEntry(
      jobID: testJobID,
      attempt: 1,
      leaseToken: String(repeating: "t", count: 40),
      renderID: testRenderID,
      requestHash: String(repeating: "a", count: 64),
      jobType: "tts.avspeech.v1",
      speechUnitID: testSpeechUnitID,
      language: "nl",
      unitKind: "word",
      spokenText: "hallo",
      contextPronunciationKey: "",
      leaseExpiresAt: "2026-09-01T12:00:00Z",
      limits: AudioLimits(maxBytes: 2 << 20, maxDurationMS: 15_000),
      profile: testProfile(),
      partialPath: paths.partialURL(jobID: testJobID).path,
      readyPath: paths.readyURL(jobID: testJobID).path,
      phase: .rendering
    )
    let store = JobJournalStore(paths: paths)
    try store.save(entry)

    XCTAssertEqual(try store.load(jobID: testJobID), entry)
    XCTAssertEqual(try store.list(), [entry])
    XCTAssertEqual(fileMode(paths.journalURL(jobID: testJobID)), 0o600)
    XCTAssertEqual(
      store.spoolBytes(), Int64(try Data(contentsOf: paths.journalURL(jobID: testJobID)).count))
    try store.remove(jobID: testJobID)
    XCTAssertNil(try store.load(jobID: testJobID))
  }

  func testLogRotationKeepsBoundedPrivateFiles() throws {
    let root = temporaryRoot("logs")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let rotation = LogRotation(logURL: paths.logURL, maximumBytes: 4, retainedFiles: 2)

    try rotation.append(Data("12345".utf8))
    try rotation.append(Data("67890".utf8))

    XCTAssertTrue(FileManager.default.fileExists(atPath: rotation.rotatedURL(1).path))
    XCTAssertEqual(fileMode(paths.logURL), 0o600)
    XCTAssertEqual(fileMode(paths.logsRoot), 0o700)
  }
}

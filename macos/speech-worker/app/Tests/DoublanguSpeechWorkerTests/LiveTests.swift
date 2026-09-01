import AVFAudio
import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class AVSpeechLiveTests: XCTestCase {
  func testAVSpeechLive() async throws {
    try requireEnvironment("DOUBLANGU_TEST_AVSPEECH_LIVE")
    try XCTSkipUnless(
      AVSpeechSynthesisVoice(identifier: WorkerConstants.avSpeechVoiceIdentifier) != nil,
      "Xander is not installed on this Mac")
    let root = temporaryRoot("avspeech-live")
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let sourceURL = root.appendingPathComponent("source.wav")
    let outputURL = root.appendingPathComponent("result.partial")
    let renderer = AVSpeechRenderer()

    _ = try await renderer.render(
      text: "goedemorgen",
      profile: WorkerConstants.avSpeechProfile,
      outputURL: sourceURL,
      limits: AudioLimits(maxBytes: 2 << 20, maxDurationMS: 15_000)
    )
    let artifact = try AudioPostprocessor().process(
      inputURL: sourceURL,
      outputURL: outputURL,
      requestHash: String(repeating: "a", count: 64),
      unitKind: "word",
      limits: AudioLimits(maxBytes: 2 << 20, maxDurationMS: 15_000)
    )
    XCTAssertGreaterThan(artifact.durationMS, 0)
    XCTAssertEqual(artifact.sampleRateHz, 24_000)
    XCTAssertEqual(artifact.channels, 1)
    XCTAssertGreaterThan(artifact.sizeBytes, 0)
  }
}

@MainActor
final class ChatterboxLiveTests: XCTestCase {
  func testChatterboxLive() async throws {
    try requireEnvironment("DOUBLANGU_TEST_CHATTERBOX_LIVE")
    let paths = AppPaths()
    let configuration = SpeechWorkerConfiguration.default(paths: paths)
    let runtimeRoot = runtimeRootForLiveTest()
    let pythonURL = runtimeRoot.appendingPathComponent("venv/bin/python")
    try XCTSkipUnless(
      FileManager.default.isExecutableFile(atPath: pythonURL.path),
      "build-runtime.sh must provide the bundled runtime")
    try XCTSkipUnless(
      FileManager.default.fileExists(atPath: paths.referenceURL.path),
      "the verified reference WAV is not installed")
    try XCTSkipUnless(
      FileManager.default.fileExists(
        atPath: paths.modelURL(revision: configuration.modelRevision).path),
      "the pinned model is not prepared")
    let supervisor = ChatterboxSupervisor(
      paths: paths, configuration: configuration, runtimeRoot: runtimeRoot, pythonURL: pythonURL)

    let wav = try await supervisor.generate(text: "Dit is een Nederlandse zin.")
    XCTAssertEqual(String(decoding: wav[0..<4], as: UTF8.self), "RIFF")
    XCTAssertEqual(String(decoding: wav[8..<12], as: UTF8.self), "WAVE")
    await supervisor.unloadForIdle()
    XCTAssertNil(supervisor.receipt)
    XCTAssertEqual(supervisor.state, .unloaded)
  }

  func testIdleUnloadLive() async throws {
    try requireEnvironment("DOUBLANGU_TEST_IDLE_UNLOAD_LIVE")
    let paths = AppPaths()
    let configuration = SpeechWorkerConfiguration.default(paths: paths)
    let runtimeRoot = runtimeRootForLiveTest()
    let pythonURL = runtimeRoot.appendingPathComponent("venv/bin/python")
    try XCTSkipUnless(
      FileManager.default.isExecutableFile(atPath: pythonURL.path),
      "build-runtime.sh must provide the bundled runtime")
    try XCTSkipUnless(
      FileManager.default.fileExists(atPath: paths.referenceURL.path),
      "the verified reference WAV is not installed")
    try XCTSkipUnless(
      FileManager.default.fileExists(
        atPath: paths.modelURL(revision: configuration.modelRevision).path),
      "the pinned model is not prepared")
    let supervisor = ChatterboxSupervisor(
      paths: paths, configuration: configuration, runtimeRoot: runtimeRoot, pythonURL: pythonURL)
    _ = try await supervisor.generate(text: "Een korte testzin.")
    await supervisor.unloadForIdle()
    XCTAssertNil(supervisor.receipt)
    XCTAssertEqual(supervisor.state, .unloaded)
  }
}

final class ServerIntegrationLiveTests: XCTestCase {
  func testServerIntegrationLive() async throws {
    try requireEnvironment("DOUBLANGU_TEST_SERVER_LIVE")
    try XCTSkipUnless(
      ProcessInfo.processInfo.environment["DOUBLANGU_TEST_SERVER_CONFIRM_LEASE"] == "1",
      "set DOUBLANGU_TEST_SERVER_CONFIRM_LEASE=1 to allow a live lease claim")
    let username = try requiredEnvironment("DOUBLANGU_TEST_PERIMETER_USERNAME")
    let password = try requiredEnvironment("DOUBLANGU_TEST_PERIMETER_PASSWORD")
    let workerToken = try requiredEnvironment("DOUBLANGU_TEST_WORKER_TOKEN")
    let secrets = MemorySecretStore()
    try secrets.write(username, account: KeychainAccount.perimeterUsername)
    try secrets.write(password, account: KeychainAccount.perimeterPassword)
    try secrets.write(workerToken, account: KeychainAccount.workerToken)
    let client = WorkerClient(secrets: secrets)
    _ = try await client.lease(
      capabilities: SpeechWorkerConfiguration.default(paths: AppPaths()).capabilities())
  }
}

private func requireEnvironment(_ name: String) throws {
  try XCTSkipUnless(
    ProcessInfo.processInfo.environment[name] == "1", "set \(name)=1 to enable this live test")
}

private func requiredEnvironment(_ name: String) throws -> String {
  guard let value = ProcessInfo.processInfo.environment[name], !value.isEmpty else {
    throw XCTSkip("set \(name) for this live test")
  }
  return value
}

private func runtimeRootForLiveTest() -> URL {
  if let path = ProcessInfo.processInfo.environment["DOUBLANGU_RUNTIME_PATH"] {
    return URL(fileURLWithPath: path, isDirectory: true)
  }
  return URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent()
    .deletingLastPathComponent()
    .deletingLastPathComponent()
    .appendingPathComponent(".build/runtime", isDirectory: true)
}

import Foundation
import XCTest

@testable import DoublanguSpeechWorker

@MainActor
final class ChatterboxSupervisorTests: XCTestCase {
  func testSupervisorStartsExactLoopbackChildAndUnloadsIt() async throws {
    let root = temporaryRoot("supervisor")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let runtimeRoot = root.appendingPathComponent("runtime", isDirectory: true)
    let pythonURL = runtimeRoot.appendingPathComponent("venv/bin/python")
    try FileManager.default.createDirectory(
      at: pythonURL.deletingLastPathComponent(), withIntermediateDirectories: true)
    FileManager.default.createFile(atPath: pythonURL.path, contents: Data("#!/bin/sh\n".utf8))
    try FileManager.default.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o755))], ofItemAtPath: pythonURL.path)
    FileManager.default.createFile(
      atPath: runtimeRoot.appendingPathComponent("server.py").path, contents: Data())

    let process = FakeWorkerProcess()
    let identity = FixedIdentityProvider(
      identity: ProcessIdentity(executablePath: pythonURL.path, startIdentity: "test-start"))
    let requester = RecordingHTTPRequester(responses: [
      HTTPResponse(statusCode: 200), HTTPResponse(statusCode: 200, body: fakeWAV()),
      HTTPResponse(statusCode: 200), HTTPResponse(statusCode: 200, body: fakeWAV()),
    ])
    let configuration = SpeechWorkerConfiguration.default(paths: paths)
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: configuration,
      process: process,
      identityProvider: identity,
      portAllocator: FixedPortAllocator(),
      requester: requester,
      runtimeRoot: runtimeRoot,
      pythonURL: pythonURL,
      preparationOverride: {}
    )

    let first = try await supervisor.generate(text: "Een Nederlandse zin.")
    let second = try await supervisor.generate(text: "Nog een zin.")

    XCTAssertEqual(first, fakeWAV())
    XCTAssertEqual(second, fakeWAV())
    XCTAssertEqual(process.launchCount, 1)
    XCTAssertEqual(process.arguments.first, runtimeRoot.appendingPathComponent("server.py").path)
    XCTAssertTrue(process.arguments.contains("--host"))
    XCTAssertTrue(process.arguments.contains("127.0.0.1"))
    XCTAssertTrue(process.arguments.contains(configuration.modelRevision))
    XCTAssertTrue(process.arguments.contains(configuration.referenceAudioPath))
    XCTAssertEqual(
      process.environment,
      [
        "PYTHONNOUSERSITE": "1", "PYTHONDONTWRITEBYTECODE": "1",
        "HF_HOME": paths.downloadCacheRoot.path,
        "PYTHONPATH": runtimeRoot.appendingPathComponent(
          "venv/lib/python3.12/site-packages"
        ).path,
      ])
    XCTAssertEqual(supervisor.state, .ready(port: 38_700))
    XCTAssertTrue(FileManager.default.fileExists(atPath: paths.workerReceiptURL.path))

    let requests = requester.requests
    XCTAssertEqual(requests.count, 4)
    XCTAssertEqual(requests[0].url?.path, "/health")
    XCTAssertEqual(requests[1].url?.path, "/generate")
    let decoded = try StrictJSON.decode(
      ChatterboxGenerateRequest.self, from: requests[1].httpBody ?? Data())
    XCTAssertEqual(decoded.text, "Een Nederlandse zin.")
    XCTAssertEqual(decoded.langCode, "nl")
    XCTAssertEqual(decoded.modelRevision, configuration.modelRevision)
    XCTAssertEqual(decoded.referenceAudioPath, configuration.referenceAudioPath)

    await supervisor.unloadForIdle()
    XCTAssertEqual(process.terminateCount, 1)
    XCTAssertEqual(process.killCount, 0)
    XCTAssertFalse(process.isRunning)
    XCTAssertEqual(supervisor.state, .unloaded)
    XCTAssertNil(supervisor.receipt)
    XCTAssertFalse(FileManager.default.fileExists(atPath: paths.workerReceiptURL.path))
  }

  func testSupervisorWillNotTerminateAChangedProcessIdentity() async throws {
    let root = temporaryRoot("supervisor-identity")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    let (runtimeRoot, pythonURL) = try makeRuntime(root: root)
    let process = FakeWorkerProcess()
    let identity = FixedIdentityProvider(
      identity: ProcessIdentity(executablePath: pythonURL.path, startIdentity: "test-start"))
    let requester = RecordingHTTPRequester(responses: [HTTPResponse(statusCode: 200)])
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: .default(paths: paths),
      process: process,
      identityProvider: identity,
      portAllocator: FixedPortAllocator(),
      requester: requester,
      runtimeRoot: runtimeRoot,
      pythonURL: pythonURL,
      preparationOverride: {}
    )

    _ = try await supervisor.ensureReady()
    identity.identityValue = ProcessIdentity(
      executablePath: pythonURL.path, startIdentity: "different-start")
    await supervisor.unloadForIdle()

    XCTAssertEqual(process.terminateCount, 0)
    XCTAssertTrue(process.isRunning)
    process.kill()
  }

  func testSupervisorOwnsTheObservedExecutableBehindThePythonWrapper() async throws {
    let root = temporaryRoot("supervisor-wrapper-identity")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    let (runtimeRoot, pythonURL) = try makeRuntime(root: root)
    let process = FakeWorkerProcess()
    let observedExecutable = "/private/runtime/python3.12"
    let identity = FixedIdentityProvider(
      identity: ProcessIdentity(
        executablePath: observedExecutable, startIdentity: "wrapper-child-start"))
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: .default(paths: paths),
      process: process,
      identityProvider: identity,
      portAllocator: FixedPortAllocator(),
      requester: RecordingHTTPRequester(responses: [HTTPResponse(statusCode: 200)]),
      runtimeRoot: runtimeRoot,
      pythonURL: pythonURL,
      preparationOverride: {}
    )

    _ = try await supervisor.ensureReady()
    XCTAssertEqual(supervisor.receipt?.executablePath, observedExecutable)

    await supervisor.unloadForIdle()
    XCTAssertEqual(process.terminateCount, 1)
    XCTAssertFalse(process.isRunning)
  }

  func testRepeatedLaunchFailuresEnterBoundedSuppression() async throws {
    let root = temporaryRoot("supervisor-backoff")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    let (runtimeRoot, pythonURL) = try makeRuntime(root: root)
    let process = FakeWorkerProcess()
    process.shouldFailLaunch = true
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: .default(paths: paths),
      process: process,
      identityProvider: FixedIdentityProvider(
        identity: ProcessIdentity(executablePath: pythonURL.path, startIdentity: "test-start")),
      portAllocator: FixedPortAllocator(),
      requester: RecordingHTTPRequester(responses: []),
      runtimeRoot: runtimeRoot,
      pythonURL: pythonURL,
      preparationOverride: {}
    )

    await assertThrowsErrorAsync(
      try await supervisor.ensureReady(), expected: ChatterboxError.launchFailed)
    await assertThrowsErrorAsync(
      try await supervisor.ensureReady(), expected: ChatterboxError.launchFailed)
    await assertThrowsErrorAsync(
      try await supervisor.ensureReady(), expected: ChatterboxError.suppressed)
    if case .suppressed = supervisor.state {
    } else {
      XCTFail("supervisor did not enter suppression")
    }
  }

  func testCancellationFlagDistinguishesOwnerStopFromServerCancellation() {
    let flag = CancellationFlag()
    flag.cancel()
    XCTAssertTrue(flag.isCancelled)
    XCTAssertFalse(flag.isServerCancellation)

    let serverFlag = CancellationFlag()
    serverFlag.cancel(server: true)
    XCTAssertTrue(serverFlag.isCancelled)
    XCTAssertTrue(serverFlag.isServerCancellation)
  }

  func testTaskCancellationStopsOwnedChildDuringGeneration() async throws {
    let root = temporaryRoot("supervisor-cancel")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    let (runtimeRoot, pythonURL) = try makeRuntime(root: root)
    let process = FakeWorkerProcess()
    let requester = BlockingGenerationRequester()
    let supervisor = ChatterboxSupervisor(
      paths: paths,
      configuration: .default(paths: paths),
      process: process,
      identityProvider: FixedIdentityProvider(
        identity: ProcessIdentity(executablePath: pythonURL.path, startIdentity: "test-start")
      ),
      portAllocator: FixedPortAllocator(),
      requester: requester,
      runtimeRoot: runtimeRoot,
      pythonURL: pythonURL,
      preparationOverride: {}
    )

    let task = Task { @MainActor in
      do {
        _ = try await supervisor.generate(text: "Een zin die wordt geannuleerd.")
        return false
      } catch is CancellationError {
        return true
      } catch {
        return false
      }
    }
    while !requester.generationStarted {
      await Task.yield()
    }
    task.cancel()
    let canceled = await task.value
    XCTAssertTrue(canceled)
    for _ in 0..<100 where process.terminateCount == 0 {
      try await Task.sleep(nanoseconds: 10_000_000)
    }
    XCTAssertEqual(process.terminateCount, 1)
  }

  private func makeRuntime(root: URL) throws -> (URL, URL) {
    let runtimeRoot = root.appendingPathComponent("runtime", isDirectory: true)
    let pythonURL = runtimeRoot.appendingPathComponent("venv/bin/python")
    try FileManager.default.createDirectory(
      at: pythonURL.deletingLastPathComponent(), withIntermediateDirectories: true)
    FileManager.default.createFile(atPath: pythonURL.path, contents: Data("#!/bin/sh\n".utf8))
    try FileManager.default.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o755))], ofItemAtPath: pythonURL.path)
    FileManager.default.createFile(
      atPath: runtimeRoot.appendingPathComponent("server.py").path, contents: Data())
    return (runtimeRoot, pythonURL)
  }
}

private final class BlockingGenerationRequester: HTTPRequesting, @unchecked Sendable {
  private let lock = NSLock()
  private var callCount = 0
  private var started = false

  var generationStarted: Bool {
    lock.withLock { started }
  }

  func send(_ request: URLRequest) async throws -> HTTPResponse {
    let call = lock.withLock {
      callCount += 1
      return callCount
    }
    if call == 1 {
      return HTTPResponse(statusCode: 200)
    }
    lock.withLock { started = true }
    try await Task.sleep(nanoseconds: 3_600_000_000_000)
    return HTTPResponse(statusCode: 200, body: fakeWAV())
  }
}

@MainActor
private func assertThrowsErrorAsync<T: Error>(
  _ expression: @autoclosure () async throws -> some Any, expected: T
) async {
  do {
    _ = try await expression()
    XCTFail("expected (expected)")
  } catch let error as ChatterboxError {
    XCTAssertEqual(error, expected as? ChatterboxError)
  } catch {
    XCTFail("unexpected error: \(error)")
  }
}

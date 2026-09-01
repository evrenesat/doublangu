import Darwin
import Foundation

public enum ChatterboxState: Equatable, Sendable {
  case stopped
  case starting
  case ready(port: Int)
  case unloaded
  case suppressed(until: Date)
}

public protocol LoopbackPortAllocating: AnyObject, Sendable {
  func allocate(range: ClosedRange<Int>) throws -> Int
}

public final class SystemLoopbackPortAllocator: LoopbackPortAllocating, @unchecked Sendable {
  public init() {}

  public func allocate(range: ClosedRange<Int>) throws -> Int {
    let descriptor = Darwin.socket(AF_INET, SOCK_STREAM, 0)
    guard descriptor >= 0 else { throw ChatterboxError.portUnavailable }
    defer { _ = Darwin.close(descriptor) }
    var address = sockaddr_in()
    address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    address.sin_family = sa_family_t(AF_INET)
    address.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
    for port in range {
      address.sin_port = in_port_t(port).bigEndian
      let result = withUnsafePointer(to: &address) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
          Darwin.bind(descriptor, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
      }
      if result == 0 { return port }
    }
    throw ChatterboxError.portUnavailable
  }
}

@MainActor
public final class ChatterboxSupervisor {
  public private(set) var state: ChatterboxState = .stopped
  public private(set) var receipt: ProcessReceipt?
  public private(set) var lastEvent: String?

  public let paths: AppPaths
  public let configuration: SpeechWorkerConfiguration
  private let process: WorkerProcessManaging
  private let identityProvider: ProcessIdentityProviding
  private let portAllocator: LoopbackPortAllocating
  private let requester: HTTPRequesting
  private let fileManager: FileManager
  private let clock: () -> Date
  private let runtimeRoot: URL
  private let pythonURL: URL
  private let preparationOverride: (() throws -> Void)?
  private var port: Int?
  private var idleTask: Task<Void, Never>?
  private var failureCount = 0
  private var suppressedUntil: Date?
  private let allowUnpreparedModel: Bool

  public init(
    paths: AppPaths,
    configuration: SpeechWorkerConfiguration,
    process: WorkerProcessManaging? = nil,
    identityProvider: ProcessIdentityProviding = SystemProcessIdentityProvider(),
    portAllocator: LoopbackPortAllocating = SystemLoopbackPortAllocator(),
    requester: HTTPRequesting = URLSessionRequester(),
    fileManager: FileManager = .default,
    clock: @escaping () -> Date = Date.init,
    allowUnpreparedModel: Bool = false,
    runtimeRoot: URL? = nil,
    pythonURL: URL? = nil,
    preparationOverride: (() throws -> Void)? = nil
  ) {
    self.paths = paths
    self.configuration = configuration
    self.process = process ?? SystemWorkerProcess(logURL: paths.logURL)
    self.identityProvider = identityProvider
    self.portAllocator = portAllocator
    self.requester = requester
    self.fileManager = fileManager
    self.clock = clock
    self.allowUnpreparedModel = allowUnpreparedModel
    self.runtimeRoot = runtimeRoot ?? paths.bundledRuntimeRoot
    self.pythonURL =
      pythonURL
      ?? (runtimeRoot ?? paths.bundledRuntimeRoot).appendingPathComponent("venv/bin/python")
    self.preparationOverride = preparationOverride
    self.process.terminationHandler = { [weak self] in
      self?.handleTermination()
    }
    reapStaleChildReceipt()
  }

  public func ensureReady() async throws -> Int {
    if let suppressedUntil, clock() < suppressedUntil {
      state = .suppressed(until: suppressedUntil)
      throw ChatterboxError.suppressed
    }
    if let port, process.isRunning, receiptMatchesCurrentProcess() {
      cancelIdleUnload()
      if try await waitForHealth(port: port, timeout: 3) {
        state = .ready(port: port)
        return port
      }
      stopCurrentProcessIfOwned()
    }
    try verifyPreparation()
    state = .starting
    cancelIdleUnload()
    let selectedPort = try portAllocator.allocate(
      range: configuration.portRangeStart...configuration.portRangeEnd)
    let executable = pythonURL
    guard fileManager.isExecutableFile(atPath: executable.path) else {
      throw recordStartFailure(.runtimeNotReady)
    }
    let arguments = [
      runtimeRoot.appendingPathComponent("server.py").path, "--host", "127.0.0.1", "--port",
      "\(selectedPort)", "--model-path", paths.modelURL(revision: configuration.modelRevision).path,
      "--model-revision", configuration.modelRevision, "--reference-audio",
      configuration.referenceAudioPath, "--tokenizer-revision", configuration.tokenizerRevision,
    ]
    let environment = [
      "PYTHONNOUSERSITE": "1", "PYTHONDONTWRITEBYTECODE": "1",
      "HF_HOME": paths.downloadCacheRoot.path,
      "PYTHONPATH": runtimeRoot.appendingPathComponent(
        "venv/lib/python3.12/site-packages"
      ).path,
    ]
    do {
      try process.launch(
        executableURL: executable, arguments: arguments, environment: environment,
        workingDirectory: runtimeRoot)
    } catch {
      throw recordStartFailure(.launchFailed)
    }
    port = selectedPort
    let observedIdentity = identityProvider.identity(for: process.processIdentifier)
    let startIdentity = observedIdentity?.startIdentity ?? process.startIdentity ?? ""
    receipt = ProcessReceipt(
      pid: process.processIdentifier,
      executablePath: observedIdentity?.executablePath ?? executable.path,
      startIdentity: startIdentity,
      appVersion: WorkerConstants.appVersion
    )
    try? paths.writePrivate(StrictJSON.encode(receipt!), to: paths.workerReceiptURL)
    if try await waitForHealth(port: selectedPort, timeout: 30) {
      failureCount = 0
      suppressedUntil = nil
      state = .ready(port: selectedPort)
      lastEvent = "child_ready"
      return selectedPort
    }
    stopCurrentProcessIfOwned()
    throw recordStartFailure(.healthTimeout)
  }

  public func generate(
    text: String, cancellation: @escaping @Sendable () -> Bool = { Task.isCancelled }
  ) async throws -> Data {
    let selectedPort = try await ensureReady()
    if cancellation() { throw CancellationError() }
    guard !text.isEmpty else { throw ChatterboxError.invalidRequest }
    let body = try StrictJSON.encode(
      ChatterboxGenerateRequest(
        text: text, modelRevision: configuration.modelRevision,
        referenceAudioPath: configuration.referenceAudioPath))
    var request = URLRequest(url: URL(string: "http://127.0.0.1:\(selectedPort)/generate")!)
    request.httpMethod = "POST"
    request.httpBody = body
    request.timeoutInterval = 180
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    let requestSnapshot = HTTPRequestSnapshot(request)
    let response = try await withTaskCancellationHandler(
      operation: {
        try await withThrowingTaskGroup(of: HTTPResponse.self) { group in
          let requester = self.requester
          group.addTask { try await requester.send(requestSnapshot.makeRequest()) }
          group.addTask {
            while true {
              if cancellation() {
                await self.abortGeneration()
                throw CancellationError()
              }
              try await Task.sleep(nanoseconds: 100_000_000)
            }
          }
          defer { group.cancelAll() }
          return try await group.next()!
        }
      },
      onCancel: { [weak self] in
        Task { @MainActor [weak self] in
          self?.abortGeneration()
        }
      })
    guard response.statusCode == 200 else {
      throw response.statusCode == 409
        ? ChatterboxError.rejected : ChatterboxError.http(response.statusCode)
    }
    guard response.body.count > 44, response.body.count <= 64 * 1024 * 1024,
      String(decoding: response.body[0..<4], as: UTF8.self) == "RIFF",
      String(decoding: response.body[8..<12], as: UTF8.self) == "WAVE"
    else { throw ChatterboxError.invalidAudio }
    if cancellation() { throw CancellationError() }
    return response.body
  }

  public func scheduleIdleUnload() {
    idleTask?.cancel()
    idleTask = Task { [weak self] in
      do {
        try await Task.sleep(
          nanoseconds: UInt64(
            max(1, self?.configuration.chatterboxIdleSeconds ?? 600) * 1_000_000_000))
      } catch { return }
      guard !Task.isCancelled else { return }
      await self?.unloadForIdle()
    }
  }

  public func cancelIdleUnload() {
    idleTask?.cancel()
    idleTask = nil
  }

  public func restart() async throws {
    cancelIdleUnload()
    stopCurrentProcessIfOwned()
    try await Task.sleep(nanoseconds: 50_000_000)
    _ = try await ensureReady()
  }

  public func unloadForIdle() async {
    idleTask = nil
    guard process.isRunning, receiptMatchesCurrentProcess() else {
      if !process.isRunning {
        receipt = nil
        port = nil
        try? fileManager.removeItem(at: paths.workerReceiptURL)
        state = .unloaded
      }
      return
    }
    process.terminate()
    let terminated = await waitForExit(seconds: 10)
    if !terminated, receiptMatchesCurrentProcess() {
      process.kill()
      _ = await waitForExit(seconds: 2)
    }
    guard !process.isRunning else {
      lastEvent = "model_unload_failed"
      return
    }
    receipt = nil
    port = nil
    try? fileManager.removeItem(at: paths.workerReceiptURL)
    state = .unloaded
    lastEvent = "model_unloaded_idle"
  }

  public func stop() {
    cancelIdleUnload()
    stopCurrentProcessIfOwned()
    receipt = nil
    port = nil
    try? fileManager.removeItem(at: paths.workerReceiptURL)
    state = .stopped
  }

  public func cancelGeneration() {
    abortGeneration()
  }

  private func verifyPreparation() throws {
    if let preparationOverride {
      try preparationOverride()
      return
    }
    guard configuration.referenceAudioPath == paths.referenceURL.path else {
      throw ChatterboxError.referenceInvalid
    }
    do {
      _ = try ReferenceAudio.inspect(
        url: paths.referenceURL, expectedSHA256: configuration.referenceAudioHash)
    } catch {
      throw ChatterboxError.referenceInvalid
    }
    if !allowUnpreparedModel {
      guard
        fileManager.fileExists(atPath: paths.modelURL(revision: configuration.modelRevision).path),
        fileManager.fileExists(atPath: paths.setupReceiptURL.path),
        fileManager.fileExists(atPath: paths.modelReceiptURL.path)
      else { throw ChatterboxError.runtimeNotReady }
      guard let data = try? Data(contentsOf: paths.setupReceiptURL),
        let receipt = try? StrictJSON.decode(SetupReceipt.self, from: data),
        receipt.modelRevision == configuration.modelRevision
      else { throw ChatterboxError.runtimeNotReady }
      guard let modelData = try? Data(contentsOf: paths.modelReceiptURL),
        let modelReceipt = try? StrictJSON.decode(ModelReceipt.self, from: modelData),
        modelReceipt.modelRevision == configuration.modelRevision
      else { throw ChatterboxError.runtimeNotReady }
    }
  }

  private func waitForHealth(port: Int, timeout: TimeInterval) async throws -> Bool {
    let deadline = clock().addingTimeInterval(timeout)
    while clock() < deadline {
      if Task.isCancelled { throw CancellationError() }
      var request = URLRequest(url: URL(string: "http://127.0.0.1:\(port)/health")!)
      request.httpMethod = "GET"
      request.timeoutInterval = 2
      if let response = try? await requester.send(request), response.statusCode == 200 {
        return true
      }
      try await Task.sleep(nanoseconds: 200_000_000)
    }
    return false
  }

  private func waitForExit(seconds: TimeInterval) async -> Bool {
    let deadline = clock().addingTimeInterval(seconds)
    while process.isRunning && clock() < deadline {
      try? await Task.sleep(nanoseconds: 100_000_000)
    }
    return !process.isRunning
  }

  private func receiptMatchesCurrentProcess() -> Bool {
    guard let receipt, process.isRunning, process.processIdentifier == receipt.pid,
      receipt.appVersion == WorkerConstants.appVersion
    else { return false }
    guard let identity = identityProvider.identity(for: receipt.pid) else {
      return process.executablePath == receipt.executablePath
        && process.startIdentity == receipt.startIdentity
    }
    return identity.executablePath == receipt.executablePath
      && identity.startIdentity == receipt.startIdentity
  }

  private func stopCurrentProcessIfOwned() {
    guard receiptMatchesCurrentProcess() else { return }
    process.terminate()
  }

  private func abortGeneration() {
    stopCurrentProcessIfOwned()
    lastEvent = "generation_cancelled"
  }

  private func handleTermination() {
    guard receipt != nil else { return }
    receipt = nil
    port = nil
    try? fileManager.removeItem(at: paths.workerReceiptURL)
    state = .stopped
    lastEvent = "child_exited"
  }

  private func reapStaleChildReceipt() {
    guard fileManager.fileExists(atPath: paths.workerReceiptURL.path) else { return }
    defer { try? fileManager.removeItem(at: paths.workerReceiptURL) }
    guard let data = try? Data(contentsOf: paths.workerReceiptURL),
      let stale = try? StrictJSON.decode(ProcessReceipt.self, from: data), stale.pid > 0,
      stale.appVersion == WorkerConstants.appVersion,
      let identity = identityProvider.identity(for: stale.pid),
      identity.executablePath == stale.executablePath,
      identity.startIdentity == stale.startIdentity
    else { return }
    _ = Darwin.kill(stale.pid, SIGTERM)
  }

  private func recordStartFailure(_ error: ChatterboxError) -> ChatterboxError {
    failureCount += 1
    if failureCount >= 2 {
      let delay = min(300.0, pow(2.0, Double(failureCount - 2)) * 30.0)
      suppressedUntil = clock().addingTimeInterval(delay)
      state = .suppressed(until: suppressedUntil!)
    }
    lastEvent = error.errorDescription
    return error
  }
}

public struct ChatterboxGenerateRequest: Codable, Equatable, Sendable {
  public let text: String
  public let langCode: String
  public let modelRevision: String
  public let referenceAudioPath: String
  private enum CodingKeys: String, CodingKey, CaseIterable {
    case text
    case langCode = "lang_code"
    case modelRevision = "model_revision"
    case referenceAudioPath = "reference_audio_path"
  }
  public init(text: String, modelRevision: String, referenceAudioPath: String) {
    self.text = text
    langCode = "nl"
    self.modelRevision = modelRevision
    self.referenceAudioPath = referenceAudioPath
  }
}

public enum ChatterboxError: Error, Equatable, LocalizedError, Sendable {
  case portUnavailable
  case runtimeNotReady
  case referenceInvalid
  case launchFailed
  case healthTimeout
  case suppressed
  case invalidRequest
  case invalidAudio
  case rejected
  case http(Int)
  public var errorDescription: String? {
    switch self {
    case .portUnavailable: return "chatterbox_port_unavailable"
    case .runtimeNotReady: return "chatterbox_runtime_not_ready"
    case .referenceInvalid: return "chatterbox_reference_invalid"
    case .launchFailed: return "chatterbox_launch_failed"
    case .healthTimeout: return "chatterbox_health_timeout"
    case .suppressed: return "chatterbox_suppressed"
    case .invalidRequest: return "chatterbox_invalid_request"
    case .invalidAudio: return "chatterbox_invalid_audio"
    case .rejected: return "chatterbox_rejected"
    case .http(let status): return "chatterbox_http_\(status)"
    }
  }
}

private struct HTTPRequestSnapshot: Sendable {
  let url: URL
  let method: String?
  let body: Data?
  let timeout: TimeInterval
  let headers: [String: String]

  init(_ request: URLRequest) {
    url = request.url ?? URL(string: "http://127.0.0.1")!
    method = request.httpMethod
    body = request.httpBody
    timeout = request.timeoutInterval
    headers = request.allHTTPHeaderFields ?? [:]
  }

  func makeRequest() -> URLRequest {
    var request = URLRequest(url: url)
    request.httpMethod = method
    request.httpBody = body
    request.timeoutInterval = timeout
    for (key, value) in headers { request.setValue(value, forHTTPHeaderField: key) }
    return request
  }
}

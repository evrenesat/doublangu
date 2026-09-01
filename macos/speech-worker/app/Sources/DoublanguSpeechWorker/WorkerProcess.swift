import Darwin
import Foundation
import ServiceManagement

@MainActor
public protocol WorkerProcessManaging: AnyObject {
  var processIdentifier: Int32 { get }
  var executablePath: String? { get }
  var startIdentity: String? { get }
  var isRunning: Bool { get }
  var terminationHandler: (() -> Void)? { get set }
  func launch(
    executableURL: URL, arguments: [String], environment: [String: String], workingDirectory: URL?)
    throws
  func terminate()
  func kill()
}

@MainActor
public final class SystemWorkerProcess: WorkerProcessManaging {
  private var process = Process()
  private var launchGeneration = UUID()
  private let logRotation: LogRotation
  private var outputHandle: FileHandle?
  public var terminationHandler: (() -> Void)?
  public init(logURL: URL) { logRotation = LogRotation(logURL: logURL) }
  public var processIdentifier: Int32 { process.processIdentifier }
  public var executablePath: String? { process.executableURL?.path }
  public private(set) var startIdentity: String?
  public var isRunning: Bool { process.isRunning }

  public func launch(
    executableURL: URL, arguments: [String], environment: [String: String], workingDirectory: URL?
  ) throws {
    try logRotation.rotateIfNeeded()
    if !FileManager.default.fileExists(atPath: logRotation.logURL.path) {
      try logRotation.append(Data())
    }
    let launchedProcess = Process()
    let generation = UUID()
    process = launchedProcess
    launchGeneration = generation
    launchedProcess.executableURL = executableURL
    launchedProcess.arguments = arguments
    launchedProcess.environment = environment
    launchedProcess.currentDirectoryURL = workingDirectory
    outputHandle = try FileHandle(forWritingTo: logRotation.logURL)
    try outputHandle?.seekToEnd()
    launchedProcess.standardOutput = outputHandle
    launchedProcess.standardError = outputHandle
    launchedProcess.terminationHandler = { [weak self] _ in
      Task { @MainActor [weak self] in
        guard let self, self.launchGeneration == generation else { return }
        self.terminationHandler?()
      }
    }
    try launchedProcess.run()
    startIdentity = "\(launchedProcess.processIdentifier):\(Date().timeIntervalSince1970)"
  }

  public func terminate() {
    process.terminate()
    try? outputHandle?.close()
    outputHandle = nil
  }
  public func kill() {
    if process.isRunning { _ = Darwin.kill(process.processIdentifier, SIGKILL) }
    try? outputHandle?.close()
    outputHandle = nil
  }
}

public struct ProcessIdentity: Equatable, Sendable {
  public let executablePath: String
  public let startIdentity: String
  public init(executablePath: String, startIdentity: String) {
    self.executablePath = executablePath
    self.startIdentity = startIdentity
  }
}

public protocol ProcessIdentityProviding: AnyObject, Sendable {
  func identity(for pid: Int32) -> ProcessIdentity?
}

public final class SystemProcessIdentityProvider: ProcessIdentityProviding, @unchecked Sendable {
  public init() {}
  public func identity(for pid: Int32) -> ProcessIdentity? {
    guard pid > 0 else { return nil }
    let path = shellPS(pid: pid, format: "comm=")?.trimmingCharacters(in: .whitespacesAndNewlines)
    let start = shellPS(pid: pid, format: "lstart=")?.trimmingCharacters(
      in: .whitespacesAndNewlines)
    guard let path, let start, !path.isEmpty, !start.isEmpty else { return nil }
    return ProcessIdentity(executablePath: path, startIdentity: start)
  }

  private func shellPS(pid: Int32, format: String) -> String? {
    let process = Process()
    let pipe = Pipe()
    process.executableURL = URL(fileURLWithPath: "/bin/ps")
    process.arguments = ["-p", "\(pid)", "-o", format]
    process.standardOutput = pipe
    process.standardError = FileHandle.nullDevice
    try? process.run()
    process.waitUntilExit()
    return String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)
  }
}

public struct ProcessReceipt: Codable, Equatable, Sendable {
  public let pid: Int32
  public let executablePath: String
  public let startIdentity: String
  public let appVersion: String
  public init(pid: Int32, executablePath: String, startIdentity: String, appVersion: String) {
    self.pid = pid
    self.executablePath = executablePath
    self.startIdentity = startIdentity
    self.appVersion = appVersion
  }
}

public protocol LoginItemManaging: AnyObject, Sendable {
  var isEnabled: Bool { get }
  func setEnabled(_ enabled: Bool) throws
}

public final class MainAppLoginItemManager: LoginItemManaging, @unchecked Sendable {
  public init() {}
  public var isEnabled: Bool { SMAppService.mainApp.status == .enabled }
  public func setEnabled(_ enabled: Bool) throws {
    if enabled {
      try SMAppService.mainApp.register()
    } else {
      try SMAppService.mainApp.unregister()
    }
  }
}

public protocol SleepActivityManaging: AnyObject, Sendable {
  func acquire()
  func release()
}

public final class SystemSleepActivity: SleepActivityManaging, @unchecked Sendable {
  private var activity: NSObjectProtocol?
  private let lock = NSLock()
  public init() {}
  public func acquire() {
    lock.lock()
    defer { lock.unlock() }
    guard activity == nil else { return }
    activity = ProcessInfo.processInfo.beginActivity(
      options: [.idleSystemSleepDisabled, .automaticTerminationDisabled],
      reason: "Doublangu speech worker is processing audio")
  }
  public func release() {
    lock.lock()
    defer { lock.unlock() }
    if let activity {
      ProcessInfo.processInfo.endActivity(activity)
      self.activity = nil
    }
  }
}

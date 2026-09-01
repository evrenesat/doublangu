import AppKit
import Combine
import Foundation
import SwiftUI

public enum AppStatus: Equatable, Sendable {
  case loading
  case setupRequired
  case enrollmentRequired
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
    case .loading: return "Starting"
    case .setupRequired: return "Setup required"
    case .enrollmentRequired: return "Enrollment required"
    case .stopped: return "Stopped"
    case .ready: return "Ready"
    case .loadingModel: return "Loading model"
    case .rendering(let jobType): return "Rendering \(jobType)"
    case .uploading(let jobType): return "Uploading \(jobType)"
    case .offline: return "Offline / backing off"
    case .spoolFull: return "Spool full"
    case .profileMismatch: return "Profile mismatch"
    case .failed(let code): return "Failed: \(code)"
    }
  }
}

@MainActor
public final class AppState: ObservableObject {
  @Published public private(set) var status: AppStatus = .loading
  @Published public private(set) var configuration: SpeechWorkerConfiguration?
  @Published public private(set) var setupReceipt: SetupReceipt?
  @Published public private(set) var referenceReady = false
  @Published public private(set) var modelReady = false
  @Published public private(set) var hasWorkerToken = false
  @Published public private(set) var hasPerimeterCredentials = false
  @Published public private(set) var lastServerContact: Date?
  @Published public private(set) var currentJobType: String?
  @Published public private(set) var launchAtLogin = false
  @Published public private(set) var lastError: String?

  public let paths: AppPaths
  public let keychain: SecretStore
  private let loginItem: LoginItemManaging
  private var leaseLoop: LeaseLoop?
  private var chatterboxSupervisor: ChatterboxSupervisor?
  private var loadTask: Task<Void, Never>?

  public init(
    paths: AppPaths = AppPaths(), keychain: SecretStore = KeychainStore(),
    loginItem: LoginItemManaging = MainAppLoginItemManager()
  ) {
    self.paths = paths
    self.keychain = keychain
    self.loginItem = loginItem
    loadTask = Task { [weak self] in await self?.load() }
  }

  deinit { loadTask?.cancel() }

  public func load() async {
    do {
      try paths.ensureDirectories()
      let config: SpeechWorkerConfiguration
      if let data = try? Data(contentsOf: paths.configURL) {
        config = try StrictJSON.decode(SpeechWorkerConfiguration.self, from: data)
        try config.validate(paths: paths)
      } else {
        config = .default(paths: paths)
        try paths.writePrivate(StrictJSON.encode(config), to: paths.configURL)
      }
      configuration = config
      setupReceipt = try? StrictJSON.decode(
        SetupReceipt.self, from: Data(contentsOf: paths.setupReceiptURL))
      referenceReady =
        (try? ReferenceAudio.inspect(
          url: paths.referenceURL, expectedSHA256: config.referenceAudioHash)) != nil
      modelReady = isModelReady(config)
      hasWorkerToken =
        ((try? keychain.read(account: KeychainAccount.workerToken)) ?? nil)?.isEmpty == false
      let username = (try? keychain.read(account: KeychainAccount.perimeterUsername)) ?? nil
      let password = (try? keychain.read(account: KeychainAccount.perimeterPassword)) ?? nil
      hasPerimeterCredentials = username?.isEmpty == false && password?.isEmpty == false
      launchAtLogin = loginItem.isEnabled
      updateInitialStatus()
      if launchAtLogin { start() }
    } catch {
      lastError = "configuration_invalid"
      status = .failed(code: "configuration_invalid")
    }
  }

  public func savePerimeterCredentials(username: String, password: String) throws {
    guard !username.isEmpty, !password.isEmpty else { throw KeychainError.invalidData }
    try keychain.write(username, account: KeychainAccount.perimeterUsername)
    try keychain.write(password, account: KeychainAccount.perimeterPassword)
    hasPerimeterCredentials = true
  }

  public func installReference(from sourceURL: URL) throws {
    guard let config = configuration else { throw ConfigurationError.invalid }
    let info = try ReferenceAudio.inspect(url: sourceURL, expectedSHA256: config.referenceAudioHash)
    let data = try Data(contentsOf: sourceURL, options: [.mappedIfSafe])
    try paths.writePrivate(data, to: paths.referenceURL)
    referenceReady = info.sha256 == config.referenceAudioHash
    updateInitialStatus()
  }

  public func prepareModel() async {
    guard let config = configuration else { return }
    status = .loadingModel
    do {
      let runner = ModelPreparationRunner(
        paths: paths, configuration: config,
        log: { [weak self] message in self?.lastError = message })
      let receipt = try await runner.prepareModel()
      setupReceipt = receipt
      modelReady = true
      lastError = nil
      updateInitialStatus()
    } catch {
      lastError = (error as? LocalizedError)?.errorDescription ?? "model_preparation_failed"
      status = .failed(code: lastError ?? "model_preparation_failed")
    }
  }

  public func enroll(enrollmentToken: String) async {
    guard let config = configuration, !enrollmentToken.isEmpty else { return }
    do {
      let client = WorkerClient(baseURL: config.baseURL, secrets: keychain)
      let response = try await client.enroll(
        name: config.workerName, capabilities: config.capabilities(),
        softwareVersion: WorkerConstants.appVersion, enrollmentToken: enrollmentToken)
      try keychain.write(response.workerToken, account: KeychainAccount.workerToken)
      var updated = config
      updated.workerID = response.worker.id
      updated.workerName = response.worker.name
      try paths.writePrivate(StrictJSON.encode(updated), to: paths.configURL)
      configuration = updated
      hasWorkerToken = true
      lastError = nil
      updateInitialStatus()
    } catch {
      lastError = (error as? LocalizedError)?.errorDescription ?? "enrollment_failed"
      status = .failed(code: lastError ?? "enrollment_failed")
    }
  }

  public func replaceEnrollment() {
    stop()
    try? keychain.delete(account: KeychainAccount.workerToken)
    if var config = configuration {
      config.workerID = nil
      try? paths.writePrivate(try StrictJSON.encode(config), to: paths.configURL)
      configuration = config
    }
    hasWorkerToken = false
    updateInitialStatus()
  }

  public func start() {
    guard let config = configuration else { return }
    guard referenceReady && modelReady else {
      status = .setupRequired
      return
    }
    guard config.workerID != nil && hasWorkerToken && hasPerimeterCredentials else {
      status = .enrollmentRequired
      return
    }
    if leaseLoop != nil { return }
    let client = WorkerClient(baseURL: config.baseURL, secrets: keychain)
    let supervisor = ChatterboxSupervisor(paths: paths, configuration: config)
    let renderer = ChatterboxRenderer(supervisor: supervisor, configuration: config, paths: paths)
    let loop = LeaseLoop(
      client: client, configuration: config, paths: paths, journal: JobJournalStore(paths: paths),
      chatterbox: renderer)
    loop.statusChanged = { [weak self, weak loop] value in
      self?.status = AppStatus(workerStatus: value)
      self?.lastServerContact = loop?.lastServerContact
      self?.currentJobType = loop?.currentJobType
    }
    loop.log = { [weak self] message in self?.lastError = message }
    chatterboxSupervisor = supervisor
    leaseLoop = loop
    loop.start()
  }

  public func stop() {
    leaseLoop?.stop()
    leaseLoop = nil
    chatterboxSupervisor?.stop()
    chatterboxSupervisor = nil
    if status != .setupRequired && status != .enrollmentRequired { status = .stopped }
  }

  public func restartChatterbox() async {
    guard let supervisor = chatterboxSupervisor else { return }
    do {
      try await supervisor.restart()
      lastError = nil
    } catch { lastError = "chatterbox_restart_failed" }
  }

  public func toggleLaunchAtLogin(_ enabled: Bool) {
    do {
      try loginItem.setEnabled(enabled)
      launchAtLogin = loginItem.isEnabled
    } catch { lastError = "launch_at_login_failed" }
  }

  public func revealLogs() {
    try? paths.ensureDirectories()
    NSWorkspace.shared.activateFileViewerSelecting([paths.logURL])
  }

  public func verifySetup() {
    guard let config = configuration else { return }
    do {
      _ = try ReferenceAudio.inspect(
        url: paths.referenceURL, expectedSHA256: config.referenceAudioHash)
      referenceReady = true
      modelReady = isModelReady(config)
      updateInitialStatus()
    } catch { status = .setupRequired }
  }

  private func updateInitialStatus() {
    guard leaseLoop == nil else { return }
    if !referenceReady || !modelReady {
      status = .setupRequired
    } else if configuration?.workerID == nil || !hasWorkerToken || !hasPerimeterCredentials {
      status = .enrollmentRequired
    } else {
      status = .stopped
    }
  }

  private func isModelReady(_ config: SpeechWorkerConfiguration) -> Bool {
    guard let setupReceipt, setupReceipt.modelRevision == config.modelRevision,
      FileManager.default.fileExists(
        atPath: paths.modelURL(revision: config.modelRevision).appendingPathComponent(
          "model.safetensors"
        ).path),
      let modelData = try? Data(contentsOf: paths.modelReceiptURL),
      let modelReceipt = try? StrictJSON.decode(ModelReceipt.self, from: modelData),
      modelReceipt.modelRevision == config.modelRevision
    else { return false }
    return true
  }
}

extension AppStatus {
  fileprivate init(workerStatus: WorkerLoopStatus) {
    switch workerStatus {
    case .stopped: self = .stopped
    case .ready: self = .ready
    case .loadingModel: self = .loadingModel
    case .rendering(let jobType): self = .rendering(jobType: jobType)
    case .uploading(let jobType): self = .uploading(jobType: jobType)
    case .offline: self = .offline
    case .spoolFull: self = .spoolFull
    case .profileMismatch: self = .profileMismatch
    case .failed(let code): self = .failed(code: code)
    }
  }
}

import AppKit
import Combine
import Foundation
import SwiftUI

public enum AppStatus: Equatable, Sendable {
  case loading
  case setupRequired
  case serverURLRequired
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
    case .serverURLRequired: return "Server URL required"
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

public enum RelayTestOutcome: Equatable, Sendable {
  case success(models: [String])
  case invalidConfig
  case missingKey
  case authFailure
  case unreachable
  case invalidResponse

  public var label: String {
    switch self {
    case .success(let models):
      let shown = models.prefix(8).joined(separator: ", ")
      let more = models.count > 8 ? " …" : ""
      return models.isEmpty ? "Reachable, no models" : "\(models.count) models: \(shown)\(more)"
    case .invalidConfig: return "Relay configuration invalid"
    case .missingKey: return "No API key stored"
    case .authFailure: return "Authentication failed"
    case .unreachable: return "Relay target unreachable"
    case .invalidResponse: return "Invalid response"
    }
  }
}

@MainActor
public final class AppState: ObservableObject {
  @Published public private(set) var status: AppStatus = .loading
  @Published public private(set) var relayStatus: RelayLoop.Status = .off
  /// The user's run request, independent of per-lane prerequisites: true
  /// between `start()` and `stop()`. The menu's Start/Stop action reflects
  /// this, and relay configuration mutations only (re)start the relay lane
  /// while it is true.
  @Published public private(set) var workerRunning = false
  @Published public private(set) var configuration: SpeechWorkerConfiguration?
  @Published public private(set) var setupReceipt: SetupReceipt?
  @Published public private(set) var referenceReady = false
  @Published public private(set) var modelReady = false
  @Published public private(set) var hasWorkerToken = false
  @Published public private(set) var hasPerimeterCredentials = false
  @Published public private(set) var hasRelayAPIKey = false
  @Published public private(set) var lastServerContact: Date?
  @Published public private(set) var currentJobType: String?
  @Published public private(set) var launchAtLogin = false
  @Published public private(set) var lastError: String?

  public let paths: AppPaths
  public let keychain: SecretStore
  private let loginItem: LoginItemManaging
  private var leaseLoop: LeaseLoop?
  var relayLoop: RelayLoop?
  /// Test hook: when set, the relay lane leases through this client instead
  /// of constructing a real network client.
  var relayClientOverride: WorkerClienting?
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
      if FileManager.default.fileExists(atPath: paths.configURL.path) {
        // Transparently rewrites a v0.1 file once with the default relay block.
        config = try SpeechWorkerConfiguration.loadFromDisk(paths: paths)
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
      hasRelayAPIKey =
        ((try? keychain.read(account: KeychainAccount.relayAPIKey)) ?? nil)?
        .isEmpty == false
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
    guard let serverURL = config.baseURL else {
      lastError = "server_url_missing"
      status = .serverURLRequired
      return
    }
    do {
      let client = WorkerClient(baseURL: serverURL, secrets: keychain)
      // v0.2 always advertises relay *support* at enrollment, independent of the
      // enabled toggle, so later toggling never requires re-enrollment.
      let response = try await client.enroll(
        name: config.workerName, capabilities: config.capabilities(),
        softwareVersion: WorkerConstants.appVersion, enrollmentToken: enrollmentToken,
        llmRelayCapabilities: [LLMRelayCapability()])
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
    workerRunning = true
    guard let config = configuration else { return }
    let identityReady = config.workerID != nil && hasWorkerToken && hasPerimeterCredentials
    // The lanes are evaluated independently: missing speech setup must not
    // block the relay lane, and relay misconfiguration must not block TTS.
    guard referenceReady && modelReady else {
      status = .setupRequired
      restartRelayLane(identityReady: identityReady && workerRunning)
      return
    }
    guard let serverURL = config.baseURL else {
      status = .serverURLRequired
      restartRelayLane(identityReady: identityReady && workerRunning)
      return
    }
    guard identityReady else {
      status = .enrollmentRequired
      restartRelayLane(identityReady: identityReady && workerRunning)
      return
    }
    if leaseLoop == nil {
      let client = WorkerClient(baseURL: serverURL, secrets: keychain)
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
    restartRelayLane(identityReady: identityReady && workerRunning)
  }

  public func stop() {
    workerRunning = false
    leaseLoop?.stop()
    leaseLoop = nil
    chatterboxSupervisor?.stop()
    chatterboxSupervisor = nil
    relayLoop?.stop()
    relayLoop = nil
    if status != .setupRequired && status != .enrollmentRequired
      && status != .serverURLRequired
    {
      status = .stopped
    }
    relayStatus = desiredRelayStatus()
  }

  /// Persists a new server base URL and rebuilds the lanes against it. An
  /// empty string clears the URL; the worker then reports the missing setup.
  public func saveServerURL(_ urlString: String) throws {
    guard var config = configuration else { throw ConfigurationError.invalid }
    let trimmed = urlString.trimmingCharacters(in: .whitespacesAndNewlines)
    let url: URL?
    if trimmed.isEmpty {
      url = nil
    } else {
      guard let parsed = URL(string: trimmed),
        let scheme = parsed.scheme?.lowercased(), scheme == "https" || scheme == "http"
      else { throw ConfigurationError.invalidServerURL }
      url = parsed
    }
    config.baseURL = url
    try config.validate(paths: paths)
    try paths.writePrivate(StrictJSON.encode(config), to: paths.configURL)
    // Both lanes reach the Doublangu server, so both must be rebuilt.
    let wasRunning = workerRunning
    leaseLoop?.stop()
    leaseLoop = nil
    chatterboxSupervisor?.stop()
    chatterboxSupervisor = nil
    configuration = config
    lastError = nil
    if wasRunning {
      start()
    } else {
      updateInitialStatus()
    }
  }

  public func saveRelayConfiguration(
    enabled: Bool, baseURLString: String, requestTimeoutSeconds: Int, apiKeyIfChanged: String?
  ) throws {
    guard var config = configuration else { throw ConfigurationError.invalid }
    guard let baseURL = URL(string: baseURLString) else { throw RelayConfigError.invalidURL }
    let relay = RelayConfig(
      enabled: enabled, baseURL: baseURL, requestTimeoutSeconds: requestTimeoutSeconds)
    try relay.validate()
    if let apiKey = apiKeyIfChanged, !apiKey.isEmpty {
      // Keychain first: a stored key must exist before the config can enable.
      try keychain.write(apiKey, account: KeychainAccount.relayAPIKey)
    }
    if enabled,
      ((try? keychain.read(account: KeychainAccount.relayAPIKey)) ?? nil)?.isEmpty != false
    {
      throw RelayConfigError.missingAPIKey
    }
    config.relay = relay
    try paths.writePrivate(StrictJSON.encode(config), to: paths.configURL)
    configuration = config
    hasRelayAPIKey =
      ((try? keychain.read(account: KeychainAccount.relayAPIKey)) ?? nil)?.isEmpty == false
    restartRelayLaneIfNeeded()
  }

  public func clearRelayAPIKey() throws {
    try keychain.delete(account: KeychainAccount.relayAPIKey)
    if var config = configuration {
      config.relay.enabled = false
      try paths.writePrivate(StrictJSON.encode(config), to: paths.configURL)
      configuration = config
    }
    hasRelayAPIKey = false
    restartRelayLaneIfNeeded()
  }

  /// Calls the local relay target directly; never contacts the Doublangu server
  /// and never logs or displays the key.
  public func testRelayConnection() async -> RelayTestOutcome {
    guard let config = configuration, (try? config.relay.validate()) != nil else {
      return .invalidConfig
    }
    guard let apiKey = try? keychain.read(account: KeychainAccount.relayAPIKey),
      !apiKey.isEmpty
    else { return .missingKey }
    let http = RelayHTTPClient(
      target: RelayTarget(
        baseURL: config.relay.baseURL, timeout: TimeInterval(config.relay.requestTimeoutSeconds)))
    do {
      return .success(models: try await http.listModels(apiKey: apiKey))
    } catch let error as RelayHTTPError {
      switch error {
      case .http(let status, _) where status == 401 || status == 403: return .authFailure
      case .cannotConnect, .connectionLost, .timedOut: return .unreachable
      default: return .invalidResponse
      }
    } catch {
      return .invalidResponse
    }
  }

  private func restartRelayLaneIfNeeded() {
    guard let config = configuration else { return }
    let identityReady = config.workerID != nil && hasWorkerToken && hasPerimeterCredentials
    // A stopped worker stays stopped: configuration changes only refresh the
    // published relay status until the user starts the worker again.
    restartRelayLane(identityReady: identityReady && workerRunning)
  }

  private func restartRelayLane(identityReady: Bool) {
    relayLoop?.stop()
    relayLoop = nil
    guard identityReady, let config = configuration, config.relay.enabled else {
      relayStatus = desiredRelayStatus()
      return
    }
    guard
      let serverURL = config.baseURL, (try? config.relay.validate()) != nil, hasRelayAPIKey
    else {
      relayStatus = .misconfigured
      return
    }
    let client =
      relayClientOverride ?? WorkerClient(baseURL: serverURL, secrets: keychain)
    let http = RelayHTTPClient(
      target: RelayTarget(
        baseURL: config.relay.baseURL, timeout: TimeInterval(config.relay.requestTimeoutSeconds)))
    let keychain = self.keychain
    let loop = RelayLoop(
      client: client, http: http,
      keyProvider: { (try? keychain.read(account: KeychainAccount.relayAPIKey)) ?? nil })
    loop.statusChanged = { [weak self] value in self?.relayStatus = value }
    loop.log = { [weak self] message in self?.lastError = message }
    relayLoop = loop
    loop.start()
  }

  private func desiredRelayStatus() -> RelayLoop.Status {
    guard let config = configuration, config.relay.enabled else { return .off }
    guard config.baseURL != nil, (try? config.relay.validate()) != nil, hasRelayAPIKey else {
      return .misconfigured
    }
    return .off
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
    if relayLoop == nil { relayStatus = desiredRelayStatus() }
    guard leaseLoop == nil else { return }
    if !referenceReady || !modelReady {
      status = .setupRequired
    } else if configuration?.baseURL == nil {
      status = .serverURLRequired
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

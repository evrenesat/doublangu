import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class RelayConfigTests: XCTestCase {
  func testV01ConfigWithoutRelayMigratesWithDefaults() throws {
    let root = temporaryRoot("relay-migration")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let legacy = try legacyV01ConfigData(paths: paths)
    try legacy.write(to: paths.configURL, options: .atomic)

    let config = try SpeechWorkerConfiguration.loadFromDisk(paths: paths)

    XCTAssertEqual(config.relay, RelayConfig())
    XCTAssertEqual(config.relay.baseURL.absoluteString, "http://127.0.0.1:8899/v1")
    XCTAssertEqual(config.relay.requestTimeoutSeconds, 540)
    XCTAssertFalse(config.relay.enabled)

    // The rewrite adds the explicit default relay block exactly once.
    let rewritten = try Data(contentsOf: paths.configURL)
    XCTAssertTrue(String(decoding: rewritten, as: UTF8.self).contains("\"relay\""))
    let secondPass = try SpeechWorkerConfiguration.loadFromDisk(paths: paths)
    XCTAssertEqual(secondPass, config)
  }

  func testUnknownConfigKeyStillFails() throws {
    let root = temporaryRoot("relay-unknown-key")
    defer { try? FileManager.default.removeItem(at: root) }
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    let full = try JSONSerialization.jsonObject(with: legacyV01ConfigData(paths: paths))
    var dictionary = try XCTUnwrap(full as? [String: Any])
    dictionary["bogus_key"] = 1
    let data = try JSONSerialization.data(withJSONObject: dictionary, options: [.sortedKeys])
    try data.write(to: paths.configURL, options: .atomic)

    XCTAssertThrowsError(try SpeechWorkerConfiguration.loadFromDisk(paths: paths))
  }

  func testRelayURLVariants() throws {
    func accepts(_ string: String) -> Bool {
      guard let url = URL(string: string) else { return false }
      return (try? RelayConfig(baseURL: url).validate()) != nil
    }

    XCTAssertTrue(accepts("http://127.0.0.1:8899/v1"))
    XCTAssertTrue(accepts("http://127.0.0.0/v1"))
    XCTAssertTrue(accepts("http://127.255.0.9/v1"))
    XCTAssertTrue(accepts("http://[::1]:8899/v1"))
    XCTAssertTrue(accepts("https://models.example.internal/v1"))
    XCTAssertTrue(accepts("https://100.64.0.7:8443/v1"))

    XCTAssertFalse(accepts("http://localhost:8899/v1"))
    XCTAssertFalse(accepts("http://localhost/v1"))
    XCTAssertFalse(accepts("http://192.168.1.10/v1"))
    XCTAssertFalse(accepts("http://10.0.0.5/v1"))
    XCTAssertFalse(accepts("http://127.0.0.1:8899/v1/extra"))
    XCTAssertFalse(accepts("http://127.0.0.1:8899/v1?x=1"))
    XCTAssertFalse(accepts("http://127.0.0.1:8899/v1#frag"))
    XCTAssertFalse(accepts("http://user:pass@127.0.0.1:8899/v1"))
    XCTAssertFalse(accepts("ftp://127.0.0.1:8899/v1"))
    XCTAssertFalse(accepts("https://models.example.internal/"))
    XCTAssertFalse(accepts("http://127.0.0.1:99999/v1"))
  }

  func testRelayTimeoutBounds() throws {
    func accepts(_ timeout: Int) -> Bool {
      (try? RelayConfig(requestTimeoutSeconds: timeout).validate()) != nil
    }
    XCTAssertFalse(accepts(29))
    XCTAssertTrue(accepts(30))
    XCTAssertTrue(accepts(540))
    XCTAssertFalse(accepts(541))
  }

  func testAPIKeyIsNeverPartOfEncodedConfig() throws {
    let paths = AppPaths(rootOverride: temporaryRoot("relay-key-encoding"))
    defer { try? FileManager.default.removeItem(at: paths.applicationSupportRoot) }
    var config = SpeechWorkerConfiguration.default(paths: paths)
    config.relay.enabled = true
    let encoded = String(decoding: try StrictJSON.encode(config), as: UTF8.self)
    XCTAssertTrue(encoded.contains("\"relay\""))
    XCTAssertFalse(encoded.contains("sk-secret-value"))
    XCTAssertFalse(encoded.contains("api_key"))
    XCTAssertEqual(
      try StrictJSON.decode(SpeechWorkerConfiguration.self, from: try StrictJSON.encode(config)),
      config)
  }

  func testLoopbackHostRecognition() throws {
    XCTAssertTrue(RelayConfig.isLiteralLoopback("127.0.0.1"))
    XCTAssertTrue(RelayConfig.isLiteralLoopback("127.0.0.0"))
    XCTAssertTrue(RelayConfig.isLiteralLoopback("127.255.0.9"))
    XCTAssertTrue(RelayConfig.isLiteralLoopback("::1"))
    XCTAssertTrue(RelayConfig.isLiteralLoopback("[::1]"))
    XCTAssertFalse(RelayConfig.isLiteralLoopback("localhost"))
    XCTAssertFalse(RelayConfig.isLiteralLoopback("128.0.0.1"))
    XCTAssertFalse(RelayConfig.isLiteralLoopback("127.0.0"))
    XCTAssertFalse(RelayConfig.isLiteralLoopback("127.0.0.0.1"))
    XCTAssertFalse(RelayConfig.isLiteralLoopback("fe80::1"))
    XCTAssertFalse(RelayConfig.isLiteralLoopback("example.internal"))
  }

  private func legacyV01ConfigData(paths: AppPaths) throws -> Data {
    let config = SpeechWorkerConfiguration.default(paths: paths)
    let full = try JSONSerialization.jsonObject(with: StrictJSON.encode(config))
    var object = try XCTUnwrap(full as? [String: Any])
    object.removeValue(forKey: "relay")
    return try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])
  }
}

@MainActor
final class AppStateRelayTests: XCTestCase {
  private func makeAppState(
    root: URL, keychain: MemorySecretStore = MemorySecretStore()
  ) async -> AppState {
    let paths = AppPaths(rootOverride: root)
    let appState = AppState(
      paths: paths, keychain: keychain, loginItem: RecordingLoginItem())
    let deadline = Date().addingTimeInterval(5)
    while appState.configuration == nil && Date() < deadline {
      try? await Task.sleep(nanoseconds: 20_000_000)
    }
    return appState
  }

  func testSavePersistsRelayConfigAndStoresKeyOnlyInKeychain() async throws {
    let root = temporaryRoot("relay-appstate-save")
    defer { try? FileManager.default.removeItem(at: root) }
    let keychain = MemorySecretStore()
    let appState = await makeAppState(root: root, keychain: keychain)
    try appState.saveServerURL("https://server.example.com/beta")

    try appState.saveRelayConfiguration(
      enabled: true, baseURLString: "http://127.0.0.1:8899/v1", requestTimeoutSeconds: 540,
      apiKeyIfChanged: "sk-secret-value")

    XCTAssertTrue(appState.configuration?.relay.enabled ?? false)
    XCTAssertTrue(appState.hasRelayAPIKey)
    XCTAssertEqual(appState.relayStatus, .off)  // no worker identity yet, so not running
    let file = String(decoding: try Data(contentsOf: appState.paths.configURL), as: UTF8.self)
    XCTAssertFalse(file.contains("sk-secret-value"))
    XCTAssertEqual(try keychain.read(account: KeychainAccount.relayAPIKey), "sk-secret-value")
  }

  func testEnablingWithoutStoredKeyThrows() async throws {
    let root = temporaryRoot("relay-appstate-nokey")
    defer { try? FileManager.default.removeItem(at: root) }
    let appState = await makeAppState(root: root)

    XCTAssertThrowsError(
      try appState.saveRelayConfiguration(
        enabled: true, baseURLString: "http://127.0.0.1:8899/v1", requestTimeoutSeconds: 540,
        apiKeyIfChanged: nil)
    ) { error in
      XCTAssertEqual(error as? RelayConfigError, .missingAPIKey)
    }
  }

  func testInvalidURLAndTimeoutAreRejected() async throws {
    let root = temporaryRoot("relay-appstate-invalid")
    defer { try? FileManager.default.removeItem(at: root) }
    let appState = await makeAppState(root: root)

    XCTAssertThrowsError(
      try appState.saveRelayConfiguration(
        enabled: true, baseURLString: "http://192.168.1.10/v1", requestTimeoutSeconds: 540,
        apiKeyIfChanged: "k")
    ) { error in
      XCTAssertEqual(error as? RelayConfigError, .invalidURL)
    }
    XCTAssertThrowsError(
      try appState.saveRelayConfiguration(
        enabled: true, baseURLString: "http://127.0.0.1:8899/v1", requestTimeoutSeconds: 10,
        apiKeyIfChanged: "k")
    ) { error in
      XCTAssertEqual(error as? RelayConfigError, .invalidTimeout)
    }
  }

  func testClearKeyDisablesRelayAndDeletesKeychainEntry() async throws {
    let root = temporaryRoot("relay-appstate-clear")
    defer { try? FileManager.default.removeItem(at: root) }
    let keychain = MemorySecretStore()
    let appState = await makeAppState(root: root, keychain: keychain)
    try appState.saveRelayConfiguration(
      enabled: true, baseURLString: "http://127.0.0.1:8899/v1", requestTimeoutSeconds: 540,
      apiKeyIfChanged: "sk-clear-me")

    try appState.clearRelayAPIKey()

    XCTAssertFalse(appState.configuration?.relay.enabled ?? true)
    XCTAssertFalse(appState.hasRelayAPIKey)
    XCTAssertEqual(appState.relayStatus, .off)
    XCTAssertNil(try keychain.read(account: KeychainAccount.relayAPIKey))
    let file = String(decoding: try Data(contentsOf: appState.paths.configURL), as: UTF8.self)
    XCTAssertFalse(file.contains("\"enabled\":true"))
  }

  func testTestConnectionRequiresStoredKeyBeforeCalling() async throws {
    let root = temporaryRoot("relay-appstate-test")
    defer { try? FileManager.default.removeItem(at: root) }
    let appState = await makeAppState(root: root)

    // Default relay config is valid, so the missing key is what fails the test.
    let outcome = await appState.testRelayConnection()
    XCTAssertEqual(outcome, .missingKey)
  }

  func testSaveServerURLPersistsValidatesAndClears() async throws {
    let root = temporaryRoot("appstate-server-url")
    defer { try? FileManager.default.removeItem(at: root) }
    let appState = await makeAppState(root: root)

    try appState.saveServerURL("https://server.example.com/beta")
    XCTAssertEqual(
      appState.configuration?.baseURL?.absoluteString, "https://server.example.com/beta")
    XCTAssertTrue(
      String(decoding: try Data(contentsOf: appState.paths.configURL), as: UTF8.self)
        .contains("server.example.com"))

    for bad in ["notaurl", "ftp://server.example.com"] {
      XCTAssertThrowsError(try appState.saveServerURL(bad)) { error in
        XCTAssertEqual(error as? ConfigurationError, .invalidServerURL)
      }
    }

    try appState.saveServerURL("   ")
    XCTAssertNil(appState.configuration?.baseURL)
    XCTAssertFalse(
      String(decoding: try Data(contentsOf: appState.paths.configURL), as: UTF8.self)
        .contains("server.example.com"))
  }

  func testStopLeavesRelayOffAndClearsStatus() async throws {
    let root = temporaryRoot("relay-appstate-stop")
    defer { try? FileManager.default.removeItem(at: root) }
    let appState = await makeAppState(root: root)
    try appState.saveServerURL("https://server.example.com/beta")
    try appState.saveRelayConfiguration(
      enabled: true, baseURLString: "http://127.0.0.1:8899/v1", requestTimeoutSeconds: 540,
      apiKeyIfChanged: "k")

    appState.stop()

    XCTAssertEqual(appState.relayStatus, .off)
    XCTAssertEqual(appState.status, .setupRequired)
  }

  /// Regression: start → stop → save enabled relay config must not restart
  /// relay leasing; the user's stopped state wins until they start again.
  func testStopPreventsRelayRestartOnRelayConfigSave() async throws {
    let (appState, fake) = try await makeRunningRelayAppState()

    // Positive control: the relay lane is leasing while running.
    XCTAssertGreaterThan(fake.leaseCalls, 0)
    XCTAssertEqual(appState.relayStatus, .idle)

    appState.stop()
    XCTAssertFalse(appState.workerRunning)
    XCTAssertNil(appState.relayLoop)
    XCTAssertEqual(appState.relayStatus, .off)
    let leasesAtStop = fake.leaseCalls

    try appState.saveRelayConfiguration(
      enabled: true, baseURLString: "http://127.0.0.1:8899/v1", requestTimeoutSeconds: 540,
      apiKeyIfChanged: nil)

    XCTAssertFalse(appState.workerRunning)
    XCTAssertNil(appState.relayLoop)
    XCTAssertEqual(appState.relayStatus, .off)
    try await Task.sleep(nanoseconds: 400_000_000)
    XCTAssertEqual(fake.leaseCalls, leasesAtStop)
  }

  /// Relay-only running (speech setup unavailable) is still a running worker:
  /// the run intent is set, and stopping tears the relay lane down.
  func testRelayOnlyRunningWorkerCanBeStoppedWhileSpeechSetupRequired() async throws {
    let (appState, fake) = try await makeRunningRelayAppState()

    XCTAssertEqual(appState.status, .setupRequired)
    XCTAssertTrue(appState.workerRunning)
    XCTAssertEqual(appState.relayStatus, .idle)
    XCTAssertNotNil(appState.relayLoop)

    let leasesAtStop = fake.leaseCalls
    appState.stop()

    XCTAssertFalse(appState.workerRunning)
    XCTAssertNil(appState.relayLoop)
    XCTAssertEqual(appState.relayStatus, .off)
    XCTAssertEqual(appState.status, .setupRequired)
    try await Task.sleep(nanoseconds: 400_000_000)
    XCTAssertEqual(fake.leaseCalls, leasesAtStop)
  }

  /// Seeds an enrolled config (worker id) with enabled relay plus a complete
  /// Keychain identity, then starts the worker with a lease-counting relay
  /// client override so no network traffic leaves the process.
  private func makeRunningRelayAppState() async throws
    -> (AppState, RelayRecordingClient)
  {
    let root = temporaryRoot("relay-run-intent")
    let paths = AppPaths(rootOverride: root)
    try paths.ensureDirectories()
    var config = SpeechWorkerConfiguration.default(paths: paths)
    config.baseURL = URL(string: "https://server.example.com/beta")
    config.workerID = testRelayJobID
    config.relay = RelayConfig(
      enabled: true, baseURL: URL(string: "http://127.0.0.1:8899/v1")!,
      requestTimeoutSeconds: 540)
    try StrictJSON.encode(config).write(to: paths.configURL, options: .atomic)
    let keychain = MemorySecretStore()
    try keychain.write("perimeter-user", account: KeychainAccount.perimeterUsername)
    try keychain.write("perimeter-pass", account: KeychainAccount.perimeterPassword)
    try keychain.write(String(repeating: "w", count: 40), account: KeychainAccount.workerToken)
    try keychain.write("sk-test", account: KeychainAccount.relayAPIKey)

    let appState = await makeAppState(root: root, keychain: keychain)
    let fake = RelayRecordingClient()
    appState.relayClientOverride = fake
    appState.start()
    let deadline = Date().addingTimeInterval(5)
    while Date() < deadline, !(appState.relayStatus == .idle && fake.leaseCalls > 0) {
      try await Task.sleep(nanoseconds: 20_000_000)
    }
    return (appState, fake)
  }
}

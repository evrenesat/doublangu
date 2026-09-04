import SwiftUI

public struct SettingsView: View {
  @ObservedObject private var appState: AppState
  @State private var serverURLText = ""
  @State private var serverSeeded = false
  @State private var serverMessage: String?
  public init(appState: AppState) { self.appState = appState }

  public var body: some View {
    TabView {
      SetupView(appState: appState).tabItem { Label("Setup", systemImage: "checkmark.shield") }
      Form {
        Section("Server") {
          TextField("Server base URL (https…)", text: $serverURLText)
          HStack {
            Button("Save server URL") { saveServerURL() }
            if let serverMessage {
              Text(serverMessage).font(.caption).foregroundStyle(.secondary)
            }
          }
          Text(
            "Base URL of the Doublangu deployment, including any path prefix, e.g. https://host.example/prefix."
          )
          .font(.caption)
          .foregroundStyle(.secondary)
        }
        Section("Worker") {
          LabeledContent("Status", value: appState.status.label)
          LabeledContent("Worker", value: appState.configuration?.workerName ?? "Not enrolled")
          Toggle(
            "Launch at login",
            isOn: Binding(
              get: { appState.launchAtLogin }, set: { appState.toggleLaunchAtLogin($0) }))
        }
        Section("Diagnostics") {
          Button("Reveal private log") { appState.revealLogs() }
          Button("Stop worker", role: .destructive) { appState.stop() }
        }
      }
      .tabItem { Label("Worker", systemImage: "waveform") }
      .onAppear { seedServerURL() }
      .onChange(of: appState.configuration) { _, _ in seedServerURL() }
      RelaySettingsView(appState: appState).tabItem { Label("Relay", systemImage: "cpu") }
    }
    .frame(width: 600, height: 480)
  }

  private func seedServerURL() {
    guard !serverSeeded, let configuration = appState.configuration else { return }
    serverURLText = configuration.baseURL?.absoluteString ?? ""
    serverSeeded = true
  }

  private func saveServerURL() {
    serverMessage = nil
    do {
      try appState.saveServerURL(serverURLText)
      serverURLText = appState.configuration?.baseURL?.absoluteString ?? ""
      serverMessage = "Server URL saved"
    } catch {
      serverMessage = (error as? LocalizedError)?.errorDescription ?? "server_url_save_failed"
    }
  }
}

public struct RelaySettingsView: View {
  @ObservedObject private var appState: AppState
  @State private var enabled = false
  @State private var baseURLText = WorkerConstants.relayDefaultBaseURL.absoluteString
  @State private var timeoutText = String(WorkerConstants.relayDefaultTimeoutSeconds)
  @State private var apiKey = ""
  @State private var message: String?
  @State private var testOutcome: RelayTestOutcome?
  @State private var testing = false
  @State private var seeded = false

  public init(appState: AppState) { self.appState = appState }

  public var body: some View {
    Form {
      Section("LLM relay") {
        LabeledContent("Status", value: appState.relayStatus.label)
        Toggle("Enable relay", isOn: $enabled)
        TextField("Base URL", text: $baseURLText)
        TextField("Request timeout (seconds, 30–540)", text: $timeoutText)
        SecureField("API key", text: $apiKey)
        LabeledContent(
          "API key",
          value: appState.hasRelayAPIKey ? "Stored in Keychain" : "No API key stored")
        HStack {
          Button("Save") { save() }
          Button("Clear key", role: .destructive) { clearKey() }
          Button("Test connection") {
            testing = true
            Task {
              testOutcome = await appState.testRelayConnection()
              testing = false
            }
          }
          if testing { ProgressView().controlSize(.small) }
        }
      }
      Section("Connection test") {
        LabeledContent(
          "Last result",
          value: testOutcome?.label ?? "Not run")
        Text(
          "The test calls the local model server directly; it never creates a Doublangu job and never logs the key."
        )
        .font(.caption)
        .foregroundStyle(.secondary)
      }
      if let message { Text(message).font(.caption).foregroundStyle(.secondary) }
      if let error = appState.lastError { Text(error).font(.caption).foregroundStyle(.orange) }
    }
    .formStyle(.grouped)
    .padding()
    .frame(width: 560)
    .onAppear { seedFromConfiguration() }
    .onChange(of: appState.configuration) { _, _ in seedFromConfiguration() }
  }

  private func seedFromConfiguration() {
    guard !seeded, let relay = appState.configuration?.relay else { return }
    enabled = relay.enabled
    baseURLText = relay.baseURL.absoluteString
    timeoutText = String(relay.requestTimeoutSeconds)
    seeded = true
  }

  private func save() {
    message = nil
    do {
      try appState.saveRelayConfiguration(
        enabled: enabled, baseURLString: baseURLText.trimmingCharacters(in: .whitespaces),
        requestTimeoutSeconds: Int(timeoutText) ?? 0,
        apiKeyIfChanged: apiKey.isEmpty ? nil : apiKey)
      apiKey = ""
      message = "Relay configuration saved"
    } catch {
      message = (error as? LocalizedError)?.errorDescription ?? "relay_save_failed"
    }
  }

  private func clearKey() {
    message = nil
    do {
      try appState.clearRelayAPIKey()
      enabled = appState.configuration?.relay.enabled ?? false
      message = "API key removed; relay disabled"
    } catch {
      message = "relay_key_clear_failed"
    }
  }
}

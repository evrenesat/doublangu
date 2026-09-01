import SwiftUI

public struct SettingsView: View {
  @ObservedObject private var appState: AppState
  public init(appState: AppState) { self.appState = appState }

  public var body: some View {
    TabView {
      SetupView(appState: appState).tabItem { Label("Setup", systemImage: "checkmark.shield") }
      Form {
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
      }.tabItem { Label("Worker", systemImage: "waveform") }
    }
    .frame(width: 600, height: 480)
  }
}

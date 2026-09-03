import SwiftUI

public struct MenuContentView: View {
  @ObservedObject private var appState: AppState
  @Environment(\.openSettings) private var openSettings

  public init(appState: AppState) { self.appState = appState }

  public var body: some View {
    VStack(alignment: .leading, spacing: 10) {
      Text(WorkerConstants.productName).font(.headline)
      Text("Speech: \(appState.status.label)").foregroundStyle(color(for: appState.status))
      Text("Relay: \(appState.relayStatus.label)").foregroundStyle(
        relayColor(for: appState.relayStatus))
      if let jobType = appState.currentJobType {
        Text(jobType).font(.caption).foregroundStyle(.secondary)
      }
      if let contact = appState.lastServerContact {
        Text("Last contact \(contact.formatted(date: .omitted, time: .shortened))").font(.caption)
          .foregroundStyle(.secondary)
      }
      Divider()
      if appState.status == .setupRequired || appState.status == .enrollmentRequired {
        Button("Open Setup") { openSettings() }
      }
      // Run intent drives Start/Stop so a relay-only worker (speech setup
      // still missing) can always be stopped from the menu.
      Button(appState.workerRunning ? "Stop Worker" : "Start Worker") {
        if appState.workerRunning { appState.stop() } else { appState.start() }
      }
      if appState.workerRunning {
        Button("Restart Chatterbox") { Task { await appState.restartChatterbox() } }
      }
      Button("Worker Settings…") { openSettings() }
      Button("Reveal Private Log") { appState.revealLogs() }
      Button("Quit") {
        appState.stop()
        NSApplication.shared.terminate(nil)
      }
    }
    .padding(14)
    .frame(width: 270)
  }

  private func color(for status: AppStatus) -> Color {
    switch status {
    case .ready: return .green
    case .offline, .failed, .profileMismatch: return .orange
    case .setupRequired, .enrollmentRequired: return .yellow
    default: return .primary
    }
  }

  private func relayColor(for status: RelayLoop.Status) -> Color {
    switch status {
    case .idle: return .green
    case .offline, .failed, .misconfigured: return .orange
    case .requiresReenrollment: return .yellow
    default: return .primary
    }
  }
}

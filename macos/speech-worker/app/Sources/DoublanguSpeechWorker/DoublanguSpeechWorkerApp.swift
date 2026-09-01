import SwiftUI

@main
public struct DoublanguSpeechWorkerApp: App {
  @StateObject private var appState = AppState()

  public init() {}

  public var body: some Scene {
    MenuBarExtra(WorkerConstants.productName, systemImage: "waveform") {
      MenuContentView(appState: appState)
    }
    Settings {
      SettingsView(appState: appState)
    }
  }
}

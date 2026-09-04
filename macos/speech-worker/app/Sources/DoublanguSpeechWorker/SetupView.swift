import SwiftUI
import UniformTypeIdentifiers

public struct SetupView: View {
  @ObservedObject private var appState: AppState
  @State private var enrollmentToken = ""
  @State private var showingImporter = false
  @State private var message: String?

  public init(appState: AppState) { self.appState = appState }

  public var body: some View {
    Form {
      Section("Private local setup") {
        Text(
          "Use the verified 24 kHz mono WAV at the planned Reference path. Credentials and the one-time enrollment token are never written to config or logs."
        )
        .font(.caption)
        LabeledContent(
          "Reference", value: appState.referenceReady ? "Verified" : "Missing or mismatched")
        Button("Choose verified reference WAV") { showingImporter = true }
        LabeledContent("Model", value: appState.modelReady ? "Verified" : "Not prepared")
        Button("Prepare pinned Chatterbox model") { Task { await appState.prepareModel() } }
      }
      Section("Worker enrollment") {
        Text(
          "Paste a one-time enrollment token generated on the Doublangu server's Workers settings page."
        )
        .font(.caption)
        SecureField("One-time enrollment token", text: $enrollmentToken)
        Button("Enroll this Mac") {
          let token = enrollmentToken
          enrollmentToken = ""
          Task { await appState.enroll(enrollmentToken: token) }
        }
        if appState.hasWorkerToken {
          Button("Replace enrollment", role: .destructive) { appState.replaceEnrollment() }
        }
      }
      if let message { Text(message).font(.caption).foregroundStyle(.secondary) }
      if let error = appState.lastError { Text(error).font(.caption).foregroundStyle(.orange) }
    }
    .formStyle(.grouped)
    .padding()
    .frame(width: 560)
    .fileImporter(isPresented: $showingImporter, allowedContentTypes: [UTType.wav]) { result in
      if case .success(let url) = result {
        do {
          try appState.installReference(from: url)
          message = "Reference verified"
        } catch { message = "Reference was not the verified canonical WAV" }
      }
    }
  }
}

import Foundation

public struct ChatterboxRenderer {
  public let supervisor: ChatterboxSupervisor
  private let postprocessor: AudioPostprocessor
  private let configuration: SpeechWorkerConfiguration
  private let paths: AppPaths

  @MainActor
  public init(
    supervisor: ChatterboxSupervisor, configuration: SpeechWorkerConfiguration, paths: AppPaths,
    postprocessor: AudioPostprocessor = AudioPostprocessor()
  ) {
    self.supervisor = supervisor
    self.configuration = configuration
    self.paths = paths
    self.postprocessor = postprocessor
  }

  @MainActor
  public func render(
    speech: SpeechLeaseDetails, partialURL: URL, requestHash: String,
    cancellation: @escaping @Sendable () -> Bool = { Task.isCancelled }
  ) async throws -> ArtifactMetadata {
    guard speech.profile.matchesByteAffecting(configuration.chatterboxProfile),
      speech.profile.engine == "chatterbox", speech.language == "nl",
      speech.unitKind == "sentence",
      speech.profile.referenceAudioHash == configuration.referenceAudioHash,
      speech.requestHash == requestHash
    else { throw ProtocolError.invalidValue("chatterbox_profile") }
    let audio = try await supervisor.generate(text: speech.spokenText, cancellation: cancellation)
    if cancellation() { throw CancellationError() }
    let wavURL = partialURL.deletingPathExtension().appendingPathExtension("source.wav")
    try audio.write(to: wavURL, options: .atomic)
    defer { try? FileManager.default.removeItem(at: wavURL) }
    return try postprocessor.process(
      inputURL: wavURL, outputURL: partialURL, requestHash: requestHash,
      unitKind: speech.unitKind,
      limits: speech.limits)
  }

  @MainActor
  public func scheduleIdleUnload() {
    supervisor.scheduleIdleUnload()
  }
}

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
    lease: LeaseResponse, partialURL: URL, requestHash: String,
    cancellation: @escaping @Sendable () -> Bool = { Task.isCancelled }
  ) async throws -> ArtifactMetadata {
    guard lease.profile.matchesByteAffecting(configuration.chatterboxProfile),
      lease.profile.engine == "chatterbox", lease.language == "nl", lease.unitKind == "sentence",
      lease.profile.referenceAudioHash == configuration.referenceAudioHash,
      lease.requestHash == requestHash
    else { throw ProtocolError.invalidValue("chatterbox_profile") }
    let audio = try await supervisor.generate(text: lease.spokenText, cancellation: cancellation)
    if cancellation() { throw CancellationError() }
    let wavURL = partialURL.deletingPathExtension().appendingPathExtension("source.wav")
    try audio.write(to: wavURL, options: .atomic)
    defer { try? FileManager.default.removeItem(at: wavURL) }
    return try postprocessor.process(
      inputURL: wavURL, outputURL: partialURL, requestHash: requestHash, unitKind: lease.unitKind,
      limits: lease.limits)
  }

  @MainActor
  public func scheduleIdleUnload() {
    supervisor.scheduleIdleUnload()
  }
}

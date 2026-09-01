import AVFAudio
import Foundation

public final class AVSpeechRenderer: NSObject, @unchecked Sendable {
  private final class Session: @unchecked Sendable {
    let outputURL: URL
    let deadline: Date
    let continuation: CheckedContinuation<URL, Error>
    let lock = NSLock()
    var file: AVAudioFile?
    var finished = false
    var canceled = false

    init(outputURL: URL, deadline: Date, continuation: CheckedContinuation<URL, Error>) {
      self.outputURL = outputURL
      self.deadline = deadline
      self.continuation = continuation
    }

    func markCanceled() {
      lock.lock()
      canceled = true
      lock.unlock()
    }

    func isCanceled() -> Bool {
      lock.lock()
      defer { lock.unlock() }
      return canceled
    }

    func hasAudioFile() -> Bool {
      lock.lock()
      defer { lock.unlock() }
      return file != nil
    }

    func append(_ buffer: AVAudioPCMBuffer) throws {
      lock.lock()
      defer { lock.unlock() }
      if file == nil {
        try FileManager.default.createDirectory(
          at: outputURL.deletingLastPathComponent(),
          withIntermediateDirectories: true,
          attributes: [.posixPermissions: NSNumber(value: Int16(0o700))]
        )
        file = try AVAudioFile(
          forWriting: outputURL,
          settings: buffer.format.settings,
          commonFormat: buffer.format.commonFormat,
          interleaved: buffer.format.isInterleaved
        )
      }
      try file?.write(from: buffer)
    }

    func finish(_ result: Result<URL, Error>) {
      lock.lock()
      guard !finished else {
        lock.unlock()
        return
      }
      finished = true
      file = nil
      lock.unlock()
      continuation.resume(with: result)
    }
  }

  private let lock = NSLock()
  private var synthesizer: AVSpeechSynthesizer?
  private var session: Session?

  public override init() { super.init() }

  public func render(
    text: String, profile: SpeechProfile, outputURL: URL, limits: AudioLimits,
    cancellation: @escaping @Sendable () -> Bool = { Task.isCancelled }
  ) async throws -> URL {
    guard profile.matchesByteAffecting(WorkerConstants.avSpeechProfile),
      profile.engine == "avspeech", profile.language == "nl", !text.isEmpty
    else { throw ProtocolError.invalidValue("avspeech_profile") }
    try Task.checkCancellation()
    return try await withTaskCancellationHandler {
      try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<URL, Error>) in
        let timeout = max(5.0, Double(limits.maxDurationMS) / 1000.0 + 15.0)
        let active = Session(
          outputURL: outputURL, deadline: Date().addingTimeInterval(timeout),
          continuation: continuation)
        lock.lock()
        session = active
        lock.unlock()
        DispatchQueue.main.async { [weak self] in
          self?.start(text: text, profile: profile, session: active, cancellation: cancellation)
        }
        Task { [weak self, weak active] in
          try? await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
          guard let active, !Task.isCancelled else { return }
          active.finish(.failure(AVSpeechRenderError.timedOut))
          self?.cancel(active)
        }
      }
    } onCancel: { [weak self] in
      self?.cancelActive()
    }
  }

  public func cancelActive() {
    lock.lock()
    let active = session
    lock.unlock()
    active?.markCanceled()
    if let active { cancel(active) }
  }

  private func start(
    text: String, profile: SpeechProfile, session: Session,
    cancellation: @escaping @Sendable () -> Bool
  ) {
    guard !cancellation(), !session.isCanceled() else {
      session.finish(.failure(CancellationError()))
      clear(session)
      return
    }
    guard let voice = AVSpeechSynthesisVoice(identifier: WorkerConstants.avSpeechVoiceIdentifier)
    else {
      session.finish(.failure(AVSpeechRenderError.voiceUnavailable))
      clear(session)
      return
    }
    let utterance = AVSpeechUtterance(string: text)
    utterance.voice = voice
    utterance.rate = min(
      0.95, max(0.01, AVSpeechUtteranceDefaultSpeechRate * Float(profile.speedMilli) / 1000.0))
    utterance.pitchMultiplier = min(2.0, max(0.5, pow(2.0, Float(profile.pitchCents) / 1200.0)))
    let voiceSynthesizer = AVSpeechSynthesizer()
    lock.lock()
    synthesizer = voiceSynthesizer
    lock.unlock()
    voiceSynthesizer.write(utterance) { [weak self, weak session] buffer in
      guard let self, let session else { return }
      if cancellation() || session.isCanceled() {
        session.finish(.failure(CancellationError()))
        self.cancel(session)
        return
      }
      if Date() > session.deadline {
        session.finish(.failure(AVSpeechRenderError.timedOut))
        self.cancel(session)
        return
      }
      guard let pcm = buffer as? AVAudioPCMBuffer else {
        session.finish(.failure(AVSpeechRenderError.invalidBuffer))
        self.clear(session)
        return
      }
      if pcm.frameLength == 0 {
        if !session.hasAudioFile() {
          session.finish(.failure(AVSpeechRenderError.empty))
          self.clear(session)
          return
        }
        session.finish(.success(session.outputURL))
        self.clear(session)
        return
      }
      do {
        try session.append(pcm)
      } catch {
        session.finish(.failure(AVSpeechRenderError.writeFailed))
        self.cancel(session)
      }
    }
  }

  private func cancel(_ session: Session) {
    lock.lock()
    let current = self.session
    let synth = synthesizer
    lock.unlock()
    guard current === session else { return }
    synth?.stopSpeaking(at: .immediate)
    session.finish(.failure(CancellationError()))
    clear(session)
  }

  private func clear(_ session: Session) {
    lock.lock()
    defer { lock.unlock() }
    guard self.session === session else { return }
    synthesizer = nil
    self.session = nil
  }
}

public enum AVSpeechRenderError: Error, Equatable, LocalizedError, Sendable {
  case voiceUnavailable
  case invalidBuffer
  case writeFailed
  case empty
  case timedOut
  public var errorDescription: String? {
    switch self {
    case .voiceUnavailable: return "avspeech_voice_unavailable"
    case .invalidBuffer: return "avspeech_invalid_buffer"
    case .writeFailed: return "avspeech_write_failed"
    case .empty: return "avspeech_empty"
    case .timedOut: return "avspeech_timeout"
    }
  }
}

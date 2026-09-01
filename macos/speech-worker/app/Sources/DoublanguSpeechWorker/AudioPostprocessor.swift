@preconcurrency import AVFAudio
import AVFoundation
import Foundation

private final class ConversionInput: @unchecked Sendable {
  let buffer: AVAudioPCMBuffer
  var supplied = false

  init(buffer: AVAudioPCMBuffer) {
    self.buffer = buffer
  }
}

public struct AudioPostprocessor: Sendable {
  public init() {}

  public func process(
    inputURL: URL, outputURL: URL, requestHash: String, unitKind: String, limits: AudioLimits
  ) throws -> ArtifactMetadata {
    let sourceInput = try AVAudioFile(forReading: inputURL)
    let sourceFormat = sourceInput.processingFormat
    guard sourceFormat.sampleRate > 0, sourceFormat.channelCount > 0, sourceInput.length > 0 else {
      throw AudioProcessingError.empty
    }
    guard
      let source = AVAudioPCMBuffer(
        pcmFormat: sourceFormat, frameCapacity: AVAudioFrameCount(sourceInput.length))
    else { throw AudioProcessingError.invalid }
    try sourceInput.read(into: source)
    let targetFormat = AVAudioFormat(
      commonFormat: .pcmFormatFloat32, sampleRate: 24_000, channels: 1, interleaved: false)!
    let frameCapacity =
      AVAudioFrameCount(
        ceil(Double(source.frameLength) * targetFormat.sampleRate / sourceFormat.sampleRate)) + 2
    guard
      let converted = AVAudioPCMBuffer(
        pcmFormat: targetFormat, frameCapacity: max(frameCapacity, 1))
    else { throw AudioProcessingError.invalid }
    guard let converter = AVAudioConverter(from: sourceFormat, to: targetFormat) else {
      throw AudioProcessingError.invalid
    }
    let conversionInput = ConversionInput(buffer: source)
    var conversionError: NSError?
    let status = converter.convert(to: converted, error: &conversionError) { _, statusPointer in
      if conversionInput.supplied {
        statusPointer.pointee = .endOfStream
        return nil
      }
      conversionInput.supplied = true
      statusPointer.pointee = .haveData
      return conversionInput.buffer
    }
    guard conversionError == nil, status == .haveData || status == .inputRanDry,
      converted.frameLength > 0
    else { throw AudioProcessingError.invalid }
    guard let samples = converted.floatChannelData?[0] else { throw AudioProcessingError.invalid }
    let count = Int(converted.frameLength)
    var values = Array(UnsafeBufferPointer(start: samples, count: count))
    guard values.allSatisfy({ $0.isFinite }) else { throw AudioProcessingError.nonFinite }
    guard values.allSatisfy({ abs($0) < 0.99999 }) else { throw AudioProcessingError.clipped }
    let threshold: Float = 0.0031622777
    guard let first = values.firstIndex(where: { abs($0) >= threshold }),
      let last = values.lastIndex(where: { abs($0) >= threshold })
    else { throw AudioProcessingError.empty }
    let padding = Int((unitKind == "sentence" ? 100 : 50) * 24)
    let lower = max(0, first - padding)
    let upper = min(values.count, last + padding + 1)
    values = Array(values[lower..<upper])
    let peak = values.reduce(Float(0)) { max($0, abs($1)) }
    if peak > 0.8912509 {
      let scale = 0.8912509 / peak
      values = values.map { $0 * scale }
    }
    guard !values.isEmpty else { throw AudioProcessingError.empty }
    try FileManager.default.createDirectory(
      at: outputURL.deletingLastPathComponent(), withIntermediateDirectories: true,
      attributes: [.posixPermissions: NSNumber(value: Int16(0o700))])
    guard
      let outputBuffer = AVAudioPCMBuffer(
        pcmFormat: targetFormat, frameCapacity: AVAudioFrameCount(values.count))
    else { throw AudioProcessingError.invalid }
    outputBuffer.frameLength = AVAudioFrameCount(values.count)
    guard let outputSamples = outputBuffer.floatChannelData?[0] else {
      throw AudioProcessingError.invalid
    }
    values.withUnsafeBufferPointer { outputSamples.update(from: $0.baseAddress!, count: $0.count) }
    let settings: [String: Any] = [
      AVFormatIDKey: kAudioFormatMPEG4AAC, AVSampleRateKey: 24_000, AVNumberOfChannelsKey: 1,
      AVEncoderBitRateKey: 48_000, AVEncoderAudioQualityKey: AVAudioQuality.high.rawValue,
    ]
    let encodedURL = outputURL.deletingLastPathComponent().appendingPathComponent(
      ".\(outputURL.lastPathComponent).m4a")
    if FileManager.default.fileExists(atPath: encodedURL.path) {
      try FileManager.default.removeItem(at: encodedURL)
    }
    defer { try? FileManager.default.removeItem(at: encodedURL) }
    do {
      let output = try AVAudioFile(
        forWriting: encodedURL, settings: settings, commonFormat: .pcmFormatFloat32,
        interleaved: false)
      try output.write(from: outputBuffer)
    }
    if FileManager.default.fileExists(atPath: outputURL.path) {
      try FileManager.default.removeItem(at: outputURL)
    }
    try FileManager.default.moveItem(at: encodedURL, to: outputURL)
    let syncHandle = try FileHandle(forUpdating: outputURL)
    try syncHandle.synchronize()
    try syncHandle.close()
    let attributes = try FileManager.default.attributesOfItem(atPath: outputURL.path)
    let size = (attributes[.size] as? NSNumber)?.int64Value ?? 0
    let data = try Data(contentsOf: outputURL, options: [.mappedIfSafe])
    guard data.count >= 8, String(decoding: data[4..<8], as: UTF8.self) == "ftyp", size > 0 else {
      throw AudioProcessingError.invalid
    }
    let durationMS = Int64((Double(values.count) / 24_000.0 * 1000.0).rounded())
    guard durationMS > 0, durationMS <= limits.maxDurationMS, size <= limits.maxBytes else {
      throw AudioProcessingError.exceedsLimits
    }
    try FileManager.default.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: outputURL.path)
    return ArtifactMetadata(
      requestHash: requestHash, sha256: sha256Hex(data), sizeBytes: size, durationMS: durationMS)
  }
}

public enum AudioProcessingError: Error, Equatable, LocalizedError, Sendable {
  case empty
  case invalid
  case nonFinite
  case clipped
  case exceedsLimits
  public var errorDescription: String? {
    switch self {
    case .empty: return "audio_empty"
    case .invalid: return "audio_invalid"
    case .nonFinite: return "audio_nonfinite"
    case .clipped: return "audio_clipped"
    case .exceedsLimits: return "audio_exceeds_limits"
    }
  }
}

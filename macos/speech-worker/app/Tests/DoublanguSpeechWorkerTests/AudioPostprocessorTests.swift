import AVFAudio
import AVFoundation
import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class AudioPostprocessorTests: XCTestCase {
  func testPostprocessorProducesMono24kAACM4AFromPCM() throws {
    let root = temporaryRoot("audio")
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let inputURL = root.appendingPathComponent("input.wav")
    let outputURL = root.appendingPathComponent("job.partial")
    try writeFixtureWAV(to: inputURL, sampleRate: 44_100, channels: 1, duration: 0.8)

    let limits = AudioLimits(maxBytes: 2 << 20, maxDurationMS: 15_000)
    let artifact = try AudioPostprocessor().process(
      inputURL: inputURL,
      outputURL: outputURL,
      requestHash: String(repeating: "a", count: 64),
      unitKind: "word",
      limits: limits
    )

    let encoded = try AVAudioFile(forReading: outputURL)
    let data = try Data(contentsOf: outputURL)
    XCTAssertEqual(encoded.processingFormat.sampleRate, 24_000, accuracy: 0.01)
    XCTAssertEqual(encoded.processingFormat.channelCount, 1)
    XCTAssertEqual(artifact.mimeType, "audio/mp4")
    XCTAssertEqual(artifact.codec, "aac-lc")
    XCTAssertEqual(artifact.sampleRateHz, 24_000)
    XCTAssertEqual(artifact.channels, 1)
    XCTAssertEqual(artifact.sizeBytes, Int64(data.count))
    XCTAssertEqual(artifact.sha256, sha256Hex(data))
    XCTAssertGreaterThan(artifact.durationMS, 0)
    XCTAssertEqual(fileMode(outputURL), 0o600)
    XCTAssertEqual(String(decoding: data[4..<8], as: UTF8.self), "ftyp")
  }

  func testPostprocessorRejectsDigitallyClippedInput() throws {
    let root = temporaryRoot("clipped-audio")
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let inputURL = root.appendingPathComponent("input.wav")
    let outputURL = root.appendingPathComponent("job.partial")
    try writeFixtureWAV(to: inputURL, sampleRate: 24_000, channels: 1, duration: 0.5, peak: 1.0)

    XCTAssertThrowsError(
      try AudioPostprocessor().process(
        inputURL: inputURL,
        outputURL: outputURL,
        requestHash: String(repeating: "a", count: 64),
        unitKind: "word",
        limits: AudioLimits(maxBytes: 2 << 20, maxDurationMS: 15_000)
      )
    ) { error in
      XCTAssertEqual(error as? AudioProcessingError, .clipped)
    }
  }

  private func writeFixtureWAV(
    to url: URL, sampleRate: Double, channels: AVAudioChannelCount, duration: Double,
    peak: Float = 0.2
  ) throws {
    let format = try XCTUnwrap(
      AVAudioFormat(
        commonFormat: .pcmFormatFloat32, sampleRate: sampleRate, channels: channels,
        interleaved: false))
    let frameCount = AVAudioFrameCount(sampleRate * duration)
    let buffer = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frameCount))
    buffer.frameLength = frameCount
    let channelData = try XCTUnwrap(buffer.floatChannelData)
    for channel in 0..<Int(channels) {
      for frame in 0..<Int(frameCount) {
        let silence = frame < Int(sampleRate * 0.1) || frame >= Int(sampleRate * 0.7)
        channelData[channel][frame] =
          silence ? 0 : peak * sin(Float(frame) * 2 * .pi * 220 / Float(sampleRate))
      }
    }
    let file = try AVAudioFile(
      forWriting: url, settings: format.settings, commonFormat: .pcmFormatFloat32,
      interleaved: false)
    try file.write(from: buffer)
  }
}

import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class ReleaseManifestTests: XCTestCase {
  func testPinnedManifestDecodesStrictly() throws {
    let manifest = ReleaseManifest(
      appVersion: WorkerConstants.appVersion,
      applicationCommit: "unknown",
      pythonVersion: "3.12.11",
      mlxAudioVersion: "0.4.7",
      modelRepository: WorkerConstants.chatterboxModelRepository,
      modelRevision: WorkerConstants.chatterboxModelRevision,
      tokenizerRepository: WorkerConstants.chatterboxTokenizerRepository,
      tokenizerRevision: WorkerConstants.chatterboxTokenizerRevision,
      referenceAudioSHA256: WorkerConstants.referenceAudioSHA256,
      runtimeReceipt: "sha256:\(String(repeating: "a", count: 64))",
      builtAtUTC: "2026-09-01T00:00:00Z"
    )
    let decoded = try StrictJSON.decode(ReleaseManifest.self, from: StrictJSON.encode(manifest))
    XCTAssertEqual(decoded, manifest)
  }

  func testManifestRejectsUnknownFieldsAndBadReceipt() throws {
    let manifest = ReleaseManifest(
      appVersion: WorkerConstants.appVersion,
      applicationCommit: "unknown",
      pythonVersion: "3.12.11",
      mlxAudioVersion: "0.4.7",
      modelRepository: WorkerConstants.chatterboxModelRepository,
      modelRevision: WorkerConstants.chatterboxModelRevision,
      tokenizerRepository: WorkerConstants.chatterboxTokenizerRepository,
      tokenizerRevision: WorkerConstants.chatterboxTokenizerRevision,
      referenceAudioSHA256: WorkerConstants.referenceAudioSHA256,
      runtimeReceipt: "sha256:\(String(repeating: "a", count: 64))",
      builtAtUTC: "2026-09-01T00:00:00Z"
    )
    var object =
      try JSONSerialization.jsonObject(with: StrictJSON.encode(manifest)) as! [String: Any]
    object["unexpected"] = true
    XCTAssertThrowsError(
      try StrictJSON.decode(
        ReleaseManifest.self, from: JSONSerialization.data(withJSONObject: object)))
    object.removeValue(forKey: "unexpected")
    object["runtime_receipt"] = "not-a-receipt"
    XCTAssertThrowsError(
      try StrictJSON.decode(
        ReleaseManifest.self, from: JSONSerialization.data(withJSONObject: object)))
  }
}

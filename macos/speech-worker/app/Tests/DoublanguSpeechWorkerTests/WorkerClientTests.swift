import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class WorkerClientTests: XCTestCase {
  func testClientPreservesBetaPrefixAndAuthenticationHeaders() async throws {
    let heartbeat =
      "{\"protocol_version\":\"speech-worker.v1\",\"cancel_requested\":false,\"lease_expires_at\":\"2026-09-01T12:00:00Z\",\"progress_percent\":50}"
    let requester = RecordingHTTPRequester(responses: [
      HTTPResponse(statusCode: 200, body: Data(heartbeat.utf8)), HTTPResponse(statusCode: 200),
    ])
    let secrets = MemorySecretStore()
    try secrets.write("user", account: KeychainAccount.perimeterUsername)
    try secrets.write("password", account: KeychainAccount.perimeterPassword)
    try secrets.write(String(repeating: "w", count: 40), account: KeychainAccount.workerToken)
    let client = WorkerClient(
      baseURL: URL(string: "https://nlrn.evren.io/beta")!, secrets: secrets, requester: requester)

    _ = try await client.heartbeat(
      jobID: testJobID, leaseToken: String(repeating: "l", count: 40), attempt: 1,
      progressPercent: 50)
    let artifactData = Data("RIFFtest".utf8)
    let metadata = CompletionMetadata(
      attempt: 1,
      leaseToken: String(repeating: "l", count: 40),
      artifact: ArtifactMetadata(
        requestHash: String(repeating: "a", count: 64), sha256: sha256Hex(artifactData),
        sizeBytes: Int64(artifactData.count), durationMS: 100)
    )
    try await client.complete(jobID: testJobID, metadata: metadata, audio: artifactData)

    let requests = requester.requests
    XCTAssertEqual(requests.count, 2)
    XCTAssertEqual(requests[0].url?.path, "/beta/api/v1/speech-worker/jobs/\(testJobID)/heartbeat")
    XCTAssertEqual(requests[1].url?.path, "/beta/api/v1/speech-worker/jobs/\(testJobID)/complete")
    XCTAssertEqual(
      requests[0].value(forHTTPHeaderField: "Authorization"), "Basic dXNlcjpwYXNzd29yZA==")
    XCTAssertEqual(
      requests[0].value(forHTTPHeaderField: "X-Doublangu-Worker-Token"),
      String(repeating: "w", count: 40))
    XCTAssertEqual(
      requests[0].value(forHTTPHeaderField: "X-Doublangu-Lease-Token"),
      String(repeating: "l", count: 40))
    XCTAssertTrue(
      requests[1].value(forHTTPHeaderField: "Content-Type")?.hasPrefix(
        "multipart/form-data; boundary=") == true)
    XCTAssertTrue(
      String(decoding: requests[1].httpBody ?? Data(), as: UTF8.self).contains("name=\"metadata\""))
    XCTAssertTrue(
      String(decoding: requests[1].httpBody ?? Data(), as: UTF8.self).contains("name=\"audio\""))
  }

  func testLeaseMapsNoWorkToNil() async throws {
    let requester = RecordingHTTPRequester(responses: [HTTPResponse(statusCode: 204)])
    let secrets = MemorySecretStore()
    try secrets.write("user", account: KeychainAccount.perimeterUsername)
    try secrets.write("password", account: KeychainAccount.perimeterPassword)
    try secrets.write(String(repeating: "w", count: 40), account: KeychainAccount.workerToken)
    let client = WorkerClient(secrets: secrets, requester: requester)

    let lease = try await client.lease(
      capabilities: SpeechWorkerConfiguration.default(
        paths: AppPaths(rootOverride: temporaryRoot("lease"))
      ).capabilities())
    XCTAssertNil(lease)
    XCTAssertEqual(requester.requests.first?.url?.path, "/beta/api/v1/speech-worker/lease")
  }
}

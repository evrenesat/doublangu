import Foundation
import XCTest

@testable import DoublanguSpeechWorker

final class RelayProtocolTests: XCTestCase {
  private func chatRelayObject(
    requestID: String = testRelayRequestID, model: String = "Qwen3.5-2B-MLX-8bit",
    messages: String? = nil, responseFormat: String? = nil, options: String? = nil,
    limits: String = "\"limits\":{\"max_completion_bytes\":2097152}"
  ) -> String {
    let messageList =
      messages
      ?? "[{\"role\":\"user\",\"content\":\"Hallo\"},{\"role\":\"assistant\",\"content\":\"Dag\"}]"
    let format =
      responseFormat
      ?? "{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"doublangu_stage_artifact\",\"strict\":true,\"schema\":{\"type\":\"object\"}}}"
    let optionList = options ?? "{\"temperature_milli\":0,\"max_output_tokens\":16384}"
    return
      "{\"request_id\":\"\(requestID)\",\"model\":\"\(model)\",\"messages\":\(messageList),\"response_format\":\(format),\"options\":\(optionList),\(limits)}"
  }

  /// Builds a wire-shaped lease response the way the server emits it: the common
  /// and zeroed speech fields stay present, `operation`/`relay` are appended.
  private func leaseWireJSON(
    operation: String? = "chat_completion", relay: String,
    jobType: String = "llm.relay.v1"
  ) -> Data {
    var json =
      "{\"protocol_version\":\"speech-worker.v1\",\"job_id\":\"\(testRelayJobID)\",\"attempt\":1,\"lease_token\":\"\(String(repeating: "l", count: 40))\",\"lease_expires_at\":\"2026-09-01T12:00:00Z\",\"job_type\":\"\(jobType)\",\"render_id\":\"\",\"request_hash\":\"\",\"speech_unit_id\":\"\",\"language\":\"\",\"unit_kind\":\"\",\"spoken_text\":\"\",\"context_pronunciation_key\":\"\",\"profile\":{},\"limits\":{}"
    if let operation {
      json += ",\"operation\":\"\(operation)\""
    }
    json += ",\"relay\":\(relay)}"
    return Data(json.utf8)
  }

  private func decodeLease(_ data: Data) throws -> LeaseResponse {
    try StrictJSON.decode(LeaseResponse.self, from: data, limit: LeaseResponse.maxEncodedBytes)
  }

  func testStrictChatRelayLeaseDecodesAndIgnoresZeroedSpeechFields() throws {
    let lease = try decodeLease(leaseWireJSON(relay: chatRelayObject()))

    XCTAssertEqual(lease.jobType, "llm.relay.v1")
    XCTAssertNil(lease.speech)
    XCTAssertEqual(lease.operation, "chat_completion")
    guard case .chat(let chat) = lease.relay else { return XCTFail("expected chat payload") }
    XCTAssertEqual(chat.requestID, testRelayRequestID)
    XCTAssertEqual(chat.model, "Qwen3.5-2B-MLX-8bit")
    XCTAssertEqual(chat.messages.count, 2)
    XCTAssertEqual(chat.options.maxOutputTokens, 16_384)
    XCTAssertEqual(chat.limits.maxCompletionBytes, 2_097_152)
    XCTAssertNoThrow(try lease.validate())
  }

  func testStrictModelsRelayLeaseDecodes() throws {
    let lease = try decodeLease(
      leaseWireJSON(
        operation: "list_models",
        relay:
          "{\"request_id\":\"\(testRelayRequestID)\",\"limits\":{\"max_completion_bytes\":2097152}}"
      ))

    guard case .models(let models) = lease.relay else { return XCTFail("expected models payload") }
    XCTAssertEqual(models.requestID, testRelayRequestID)
    XCTAssertNoThrow(try lease.validate())
  }

  func testUnknownKeyInsideRelayPayloadIsRejected() {
    let relay = chatRelayObject().dropLast().appending(",\"surprise\":1}").description
    XCTAssertThrowsError(try decodeLease(leaseWireJSON(relay: relay)))
  }

  func testDuplicateKeysAreRejected() {
    let duplicated = chatRelayObject().replacingOccurrences(
      of: "\"request_id\":\"\(testRelayRequestID)\"",
      with: "\"request_id\":\"\(testRelayRequestID)\",\"request_id\":\"\(testRelayRequestID)\"")
    let data = leaseWireJSON(relay: duplicated)
    XCTAssertTrue(JSONDuplicateKeyDetector.containsDuplicateKeys(data))
    XCTAssertThrowsError(try decodeLease(data))
  }

  func testInvalidMessageRoleIsRejected() {
    let badRole = leaseWireJSON(
      relay: chatRelayObject(messages: "[{\"role\":\"system\",\"content\":\"nope\"}]"))
    XCTAssertThrowsError(try decodeLease(badRole))
  }

  func testTooManyMessagesAreRejected() {
    let seventeen = (0..<17).map { _ in "{\"role\":\"user\",\"content\":\"x\"}" }.joined(
      separator: ",")
    let tooMany = leaseWireJSON(relay: chatRelayObject(messages: "[\(seventeen)]"))
    XCTAssertThrowsError(try decodeLease(tooMany))
  }

  func testMessageContentMustBeNonEmpty() {
    let empty = leaseWireJSON(
      relay: chatRelayObject(messages: "[{\"role\":\"user\",\"content\":\"\"}]"))
    XCTAssertThrowsError(try decodeLease(empty))
  }

  func testInvalidOptionsAndLimitsAreRejected() {
    let hotTemperature = leaseWireJSON(
      relay: chatRelayObject(options: "{\"temperature_milli\":2001,\"max_output_tokens\":16384}"))
    XCTAssertThrowsError(try decodeLease(hotTemperature))
    let fewTokens = leaseWireJSON(
      relay: chatRelayObject(options: "{\"temperature_milli\":0,\"max_output_tokens\":1023}"))
    XCTAssertThrowsError(try decodeLease(fewTokens))
    let oversizedLimit = leaseWireJSON(
      relay: chatRelayObject(limits: "\"limits\":{\"max_completion_bytes\":2097153}"))
    XCTAssertThrowsError(try decodeLease(oversizedLimit))
  }

  func testInvalidResponseFormatWrapperIsRejected() {
    let badType = leaseWireJSON(
      relay: chatRelayObject(
        responseFormat:
          "{\"type\":\"text\",\"json_schema\":{\"name\":\"n\",\"strict\":true,\"schema\":{}}}"))
    XCTAssertThrowsError(try decodeLease(badType))

    let missingSchema = leaseWireJSON(
      relay: chatRelayObject(
        responseFormat:
          "{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"n\",\"strict\":true}}"))
    XCTAssertThrowsError(try decodeLease(missingSchema))

    // The contract fixes the wrapper: exact artifact name and strict=true.
    let wrongName = leaseWireJSON(
      relay: chatRelayObject(
        responseFormat:
          "{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"other_artifact\",\"strict\":true,\"schema\":{}}}"
      ))
    XCTAssertThrowsError(try decodeLease(wrongName))

    let notStrict = leaseWireJSON(
      relay: chatRelayObject(
        responseFormat:
          "{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"doublangu_stage_artifact\",\"strict\":false,\"schema\":{}}}"
      ))
    XCTAssertThrowsError(try decodeLease(notStrict))
  }

  func testWrongOperationAndPayloadCombinationsAreRejected() {
    let chatKeysWithModelsOperation = leaseWireJSON(
      operation: "list_models", relay: chatRelayObject())
    XCTAssertThrowsError(try decodeLease(chatKeysWithModelsOperation))

    let missingChatKeys = leaseWireJSON(
      operation: "chat_completion",
      relay:
        "{\"request_id\":\"\(testRelayRequestID)\",\"limits\":{\"max_completion_bytes\":2097152}}")
    XCTAssertThrowsError(try decodeLease(missingChatKeys))

    let unknownOperation = leaseWireJSON(operation: "embeddings", relay: chatRelayObject())
    XCTAssertThrowsError(try decodeLease(unknownOperation))

    let missingOperation = leaseWireJSON(operation: nil, relay: chatRelayObject())
    XCTAssertThrowsError(try decodeLease(missingOperation))
  }

  func testUnknownJobTypeFailsProtocolValidation() {
    let data = leaseWireJSON(relay: chatRelayObject(), jobType: "llm.relay.v2")
    XCTAssertThrowsError(try decodeLease(data))
  }

  func testOperationPayloadMismatchFailsValidation() throws {
    let mismatched = LeaseResponse(
      protocolVersion: WorkerConstants.protocolVersion, jobID: testRelayJobID, attempt: 1,
      leaseToken: String(repeating: "l", count: 40),
      leaseExpiresAt: ISO8601DateFormatter().string(from: Date().addingTimeInterval(90)),
      jobType: "llm.relay.v1", operation: "list_models",
      relay: .models(
        RelayListModelsLease(
          requestID: testRelayRequestID,
          limits: RelayCompletionLimits(maxCompletionBytes: 2_097_152))))
    XCTAssertNoThrow(try mismatched.validate())

    let chat = testRelayChatLease()
    let flipped = LeaseResponse(
      protocolVersion: chat.protocolVersion, jobID: chat.jobID, attempt: chat.attempt,
      leaseToken: chat.leaseToken, leaseExpiresAt: chat.leaseExpiresAt, jobType: chat.jobType,
      operation: "list_models", relay: chat.relay)
    XCTAssertThrowsError(try flipped.validate())
  }

  func testMixedLeaseCapabilitiesAreImpossibleThroughThePublicInitializer() throws {
    let speech = LeaseRequest(
      lane: .speech(
        SpeechWorkerConfiguration.default(paths: AppPaths(rootOverride: temporaryRoot("lane")))
          .capabilities()))
    let relay = LeaseRequest(lane: .relay(LLMRelayCapability()))

    let speechData = try StrictJSON.encode(speech)
    let relayData = try StrictJSON.encode(relay)
    let speechJSON = String(decoding: speechData, as: UTF8.self)
    let relayJSON = String(decoding: relayData, as: UTF8.self)
    XCTAssertFalse(speechJSON.contains("llm_relay_capabilities"))
    XCTAssertFalse(relayJSON.contains("\"capabilities\""))
    XCTAssertTrue(relayJSON.contains("llm_relay_capabilities"))

    XCTAssertEqual(try StrictJSON.decode(LeaseRequest.self, from: speechData), speech)
    XCTAssertEqual(try StrictJSON.decode(LeaseRequest.self, from: relayData), relay)
  }

  func testCurrentTTSLeaseStillDecodesAndValidates() throws {
    let configuration = SpeechWorkerConfiguration.default(
      paths: AppPaths(rootOverride: temporaryRoot("tts-lease")))
    let lease = testLease(configuration: configuration)

    let decoded = try decodeLease(try StrictJSON.encode(lease))

    XCTAssertEqual(decoded, lease)
    XCTAssertNil(decoded.operation)
    XCTAssertNil(decoded.relay)
    XCTAssertNoThrow(try decoded.validate())
  }

  func testTTSLeaseWithRelayKeysIsRejected() throws {
    let configuration = SpeechWorkerConfiguration.default(
      paths: AppPaths(rootOverride: temporaryRoot("tts-lease-relay")))
    let encoded = String(
      decoding: try StrictJSON.encode(testLease(configuration: configuration)), as: UTF8.self)
    let polluted = encoded.dropLast().appending(",\"operation\":\"chat_completion\"}").description
    XCTAssertThrowsError(try decodeLease(Data(polluted.utf8)))
  }

  func testRelayLeaseRoundTripKeepsFlatWireShape() throws {
    let lease = testRelayChatLease()
    let decoded = try decodeLease(try StrictJSON.encode(lease))
    XCTAssertEqual(decoded, lease)
    let json = String(decoding: try StrictJSON.encode(decoded), as: UTF8.self)
    XCTAssertTrue(json.contains("\"operation\":\"chat_completion\""))
    XCTAssertTrue(json.contains("\"request_id\":\"\(testRelayRequestID)\""))
  }

  func testChatResultValidationAndBound() throws {
    let result = RelayChatResult(
      requestID: testRelayRequestID,
      content: String(repeating: "a", count: RelayLimits.maxContentBytes),
      reportedModel: "m", providerRequestID: "chatcmpl-1", finishReason: "stop",
      usage: RelayUsage(promptTokens: 1, completionTokens: 1, totalTokens: 2), timing: RelayTiming()
    )
    XCTAssertNoThrow(try result.validate())

    let oversized = RelayChatResult(
      requestID: testRelayRequestID,
      content: String(repeating: "a", count: RelayLimits.maxContentBytes + 1),
      reportedModel: "m", providerRequestID: "", finishReason: "stop",
      usage: RelayUsage(promptTokens: 0, completionTokens: 0, totalTokens: 0), timing: RelayTiming()
    )
    XCTAssertThrowsError(try oversized.validate())

    let badFinish = RelayChatResult(
      requestID: testRelayRequestID, content: "x", reportedModel: "", providerRequestID: "",
      finishReason: "length",
      usage: RelayUsage(promptTokens: 0, completionTokens: 0, totalTokens: 0), timing: RelayTiming()
    )
    XCTAssertThrowsError(try badFinish.validate())
  }

  func testModelsResultRejectsDuplicatesAndInvalidIDs() {
    let ok = RelayModelsResult(requestID: testRelayRequestID, models: ["m1", "m2"])
    XCTAssertNoThrow(try ok.validate())
    let duplicate = RelayModelsResult(requestID: testRelayRequestID, models: ["m1", "m1"])
    XCTAssertThrowsError(try duplicate.validate())
    let empty = RelayModelsResult(requestID: testRelayRequestID, models: [""])
    XCTAssertThrowsError(try empty.validate())
    let tooMany = RelayModelsResult(
      requestID: testRelayRequestID,
      models: (0...RelayLimits.maxModels).map { "m\($0)" })
    XCTAssertThrowsError(try tooMany.validate())
    let none = RelayModelsResult(requestID: testRelayRequestID, models: [])
    XCTAssertNoThrow(try none.validate())
  }

  func testRelayCompletionMetadataOmitsArtifact() throws {
    let metadata = CompletionMetadata(
      attempt: 2, leaseToken: String(repeating: "l", count: 40), artifact: nil)
    let json = String(decoding: try StrictJSON.encode(metadata), as: UTF8.self)
    XCTAssertFalse(json.contains("artifact"))
    let decoded = try StrictJSON.decode(
      CompletionMetadata.self, from: try StrictJSON.encode(metadata))
    XCTAssertNil(decoded.artifact)
    XCTAssertEqual(decoded.attempt, 2)
  }

  func testSpeechCompletionMetadataStillCarriesArtifact() throws {
    let artifact = ArtifactMetadata(
      requestHash: String(repeating: "a", count: 64), sha256: String(repeating: "b", count: 64),
      sizeBytes: 12, durationMS: 100)
    let metadata = CompletionMetadata(
      attempt: 1, leaseToken: String(repeating: "l", count: 40), artifact: artifact)
    let decoded = try StrictJSON.decode(
      CompletionMetadata.self, from: try StrictJSON.encode(metadata))
    XCTAssertEqual(decoded.artifact, artifact)
  }

  func testEnrollRequestEncodesRelaySupportCapability() throws {
    let withRelay = EnrollRequest(
      name: "Mac", capabilities: [], softwareVersion: "0.2.0",
      llmRelayCapabilities: [LLMRelayCapability()])
    let json = String(decoding: try StrictJSON.encode(withRelay), as: UTF8.self)
    XCTAssertTrue(json.contains("\"llm_relay_capabilities\":[{\"max_completion_bytes\":2097152}]"))

    let withoutRelay = EnrollRequest(name: "Mac", capabilities: [], softwareVersion: "0.1.0")
    let legacy = String(decoding: try StrictJSON.encode(withoutRelay), as: UTF8.self)
    XCTAssertFalse(legacy.contains("llm_relay_capabilities"))
    XCTAssertEqual(
      try StrictJSON.decode(EnrollRequest.self, from: try StrictJSON.encode(withRelay)), withRelay)
  }

  func testWorkerInfoToleratesOptionalRelayFields() throws {
    let base =
      "{\"id\":\"\(testRelayJobID)\",\"name\":\"Mac\",\"protocol_version\":\"speech-worker.v1\",\"revoked_at\":\"\",\"last_seen_at\":\"\",\"capabilities\":[],\"software_version\":\"0.2.0\",\"created_at\":\"\",\"updated_at\":\"\"}"
    XCTAssertNoThrow(try StrictJSON.decode(WorkerInfo.self, from: Data(base.utf8)))

    let withRelay =
      base.dropLast().appending(
        ",\"llm_relay_capabilities\":[{\"max_completion_bytes\":2097152}],\"relay_last_seen_at\":\"2026-09-03T10:00:00Z\"}"
      ).description
    let decoded = try StrictJSON.decode(WorkerInfo.self, from: Data(withRelay.utf8))
    XCTAssertEqual(decoded.llmRelayCapabilities?.first?.maxCompletionBytes, 2_097_152)
    XCTAssertEqual(decoded.relayLastSeenAt, "2026-09-03T10:00:00Z")
  }

  func testRelayCompletionMultipartContainsMetadataAndResultWithoutAudio() async throws {
    let (requester, client) = makeWorkerClient()
    let result = try StrictJSON.encode(
      RelayModelsResult(requestID: testRelayRequestID, models: ["m1"]))

    try await client.completeRelay(
      jobID: testRelayJobID, attempt: 1, leaseToken: String(repeating: "l", count: 40),
      result: result)

    let body = String(decoding: requester.requests.first?.httpBody ?? Data(), as: UTF8.self)
    XCTAssertTrue(body.contains("name=\"metadata\""))
    XCTAssertTrue(body.contains("name=\"result\""))
    XCTAssertFalse(body.contains("name=\"audio\""))
    XCTAssertFalse(body.contains("\"artifact\""))
  }

  func testRelayCompletionRejectsOversizedResultBeforeNetworkIO() async throws {
    let (requester, client) = makeWorkerClient()

    do {
      try await client.completeRelay(
        jobID: testRelayJobID, attempt: 1, leaseToken: String(repeating: "l", count: 40),
        result: Data(count: RelayLimits.maxPayloadBytes + 1))
      XCTFail("expected oversized rejection")
    } catch let error as WorkerClientError {
      XCTAssertEqual(error, .invalidPayload)
    }
    XCTAssertTrue(requester.requests.isEmpty)
  }

  func testSpeechCompletionMultipartContainsMetadataAndAudioWithoutResult() async throws {
    let (requester, client) = makeWorkerClient()
    let artifact = ArtifactMetadata(
      requestHash: String(repeating: "a", count: 64), sha256: String(repeating: "b", count: 64),
      sizeBytes: 8, durationMS: 10)

    try await client.completeSpeech(
      jobID: testJobID,
      metadata: CompletionMetadata(
        attempt: 1, leaseToken: String(repeating: "l", count: 40), artifact: artifact),
      audio: Data("RIFFtest".utf8))

    let body = String(decoding: requester.requests.first?.httpBody ?? Data(), as: UTF8.self)
    XCTAssertTrue(body.contains("name=\"metadata\""))
    XCTAssertTrue(body.contains("name=\"audio\""))
    XCTAssertFalse(body.contains("name=\"result\""))
    XCTAssertTrue(body.contains("\"artifact\""))
  }

  func testLeaseDecodeLimitAccommodatesLargeSchemas() throws {
    let bigSchema = leaseWireJSON(
      relay: chatRelayObject(
        responseFormat:
          "{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"doublangu_stage_artifact\",\"strict\":true,\"schema\":{\"x\":\"\(String(repeating: "s", count: 1_200_000))\"}}}"
      ))
    XCTAssertNoThrow(try decodeLease(bigSchema))
  }

  private func makeWorkerClient() -> (RecordingHTTPRequester, WorkerClient) {
    let requester = RecordingHTTPRequester(responses: [HTTPResponse(statusCode: 200)])
    let secrets = MemorySecretStore()
    try? secrets.write("u", account: KeychainAccount.perimeterUsername)
    try? secrets.write("p", account: KeychainAccount.perimeterPassword)
    try? secrets.write(String(repeating: "w", count: 40), account: KeychainAccount.workerToken)
    return (requester, WorkerClient(secrets: secrets, requester: requester))
  }
}

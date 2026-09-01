import Foundation

public struct HTTPResponse: Sendable {
  public let statusCode: Int
  public let headers: [String: String]
  public let body: Data

  public init(statusCode: Int, headers: [String: String] = [:], body: Data = Data()) {
    self.statusCode = statusCode
    self.headers = headers
    self.body = body
  }
}

public protocol HTTPRequesting: AnyObject, Sendable {
  func send(_ request: URLRequest) async throws -> HTTPResponse
}

public final class URLSessionRequester: HTTPRequesting, @unchecked Sendable {
  private let session: URLSession
  public init() {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForRequest = 35
    configuration.timeoutIntervalForResource = 90
    configuration.waitsForConnectivity = false
    session = URLSession(configuration: configuration)
  }

  public func send(_ request: URLRequest) async throws -> HTTPResponse {
    do {
      let (data, response) = try await session.data(for: request)
      guard let http = response as? HTTPURLResponse else { throw WorkerClientError.invalidResponse }
      var headers: [String: String] = [:]
      for (key, value) in http.allHeaderFields {
        headers[String(describing: key).lowercased()] = String(describing: value)
      }
      return HTTPResponse(statusCode: http.statusCode, headers: headers, body: data)
    } catch is CancellationError {
      throw CancellationError()
    } catch let error as WorkerClientError {
      throw error
    } catch {
      throw WorkerClientError.transport
    }
  }
}

public enum WorkerClientError: Error, Equatable, LocalizedError, Sendable {
  case credentialsMissing
  case invalidResponse
  case transport
  case http(status: Int)
  case invalidPayload

  public var errorDescription: String? {
    switch self {
    case .credentialsMissing: "worker_credentials_missing"
    case .invalidResponse: "worker_invalid_response"
    case .transport: "worker_transport_error"
    case .http(let status): "worker_http_\(status)"
    case .invalidPayload: "worker_invalid_payload"
    }
  }
}

public protocol WorkerClienting: AnyObject, Sendable {
  func enroll(
    name: String, capabilities: [WorkerCapability], softwareVersion: String, enrollmentToken: String
  ) async throws -> EnrollmentResponse
  func lease(capabilities: [WorkerCapability]) async throws -> LeaseResponse?
  func heartbeat(jobID: String, leaseToken: String, attempt: Int, progressPercent: Int) async throws
    -> HeartbeatResponse
  func complete(jobID: String, metadata: CompletionMetadata, audioURL: URL) async throws
  func fail(jobID: String, leaseToken: String, attempt: Int, errorCode: String, retry: Bool)
    async throws
}

public final class WorkerClient: WorkerClienting, @unchecked Sendable {
  public let baseURL: URL
  private let secrets: SecretStore
  private let requester: HTTPRequesting

  public init(
    baseURL: URL = WorkerConstants.baseURL, secrets: SecretStore,
    requester: HTTPRequesting = URLSessionRequester()
  ) {
    self.baseURL = baseURL
    self.secrets = secrets
    self.requester = requester
  }

  public func enroll(
    name: String, capabilities: [WorkerCapability], softwareVersion: String, enrollmentToken: String
  ) async throws -> EnrollmentResponse {
    var request = try makeRequest(
      path: "api/v1/speech-worker/enroll", method: "POST",
      body: StrictJSON.encode(
        EnrollRequest(name: name, capabilities: capabilities, softwareVersion: softwareVersion)))
    request.setValue(enrollmentToken, forHTTPHeaderField: "X-Doublangu-Enrollment-Token")
    let response = try await send(request, accepted: [201])
    return try StrictJSON.decode(EnrollmentResponse.self, from: response.body)
  }

  public func lease(capabilities: [WorkerCapability]) async throws -> LeaseResponse? {
    let body = try StrictJSON.encode(LeaseRequest(capabilities: capabilities))
    let response = try await send(
      try makeRequest(
        path: "api/v1/speech-worker/lease", method: "POST", body: body, workerToken: true),
      accepted: [200, 204])
    if response.statusCode == 204 { return nil }
    return try StrictJSON.decode(LeaseResponse.self, from: response.body)
  }

  public func heartbeat(jobID: String, leaseToken: String, attempt: Int, progressPercent: Int)
    async throws -> HeartbeatResponse
  {
    let body = try StrictJSON.encode(
      HeartbeatRequest(attempt: attempt, progressPercent: progressPercent))
    let request = try makeRequest(
      path: "api/v1/speech-worker/jobs/\(jobID)/heartbeat", method: "POST", body: body,
      workerToken: true, leaseToken: leaseToken)
    return try StrictJSON.decode(
      HeartbeatResponse.self, from: try await send(request, accepted: [200]).body)
  }

  public func complete(jobID: String, metadata: CompletionMetadata, audioURL: URL) async throws {
    let audio = try Data(contentsOf: audioURL, options: [.mappedIfSafe])
    try await complete(jobID: jobID, metadata: metadata, audio: audio)
  }

  public func complete(jobID: String, metadata: CompletionMetadata, audio: Data) async throws {
    let boundary = "DoublanguBoundary-\(UUID().uuidString)"
    var body = Data()
    body.appendUTF8("--\(boundary)\r\nContent-Disposition: form-data; name=\"metadata\"\r\n\r\n")
    body.append(try StrictJSON.encode(metadata))
    body.appendUTF8("\r\n")
    body.appendUTF8(
      "--\(boundary)\r\nContent-Disposition: form-data; name=\"audio\"; filename=\"audio.m4a\"\r\nContent-Type: audio/mp4\r\n\r\n"
    )
    body.append(audio)
    body.appendUTF8("\r\n--\(boundary)--\r\n")
    var request = try makeRequest(
      path: "api/v1/speech-worker/jobs/\(jobID)/complete", method: "POST", body: body,
      workerToken: true)
    request.setValue(
      "multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
    _ = try await send(request, accepted: [200])
  }

  public func fail(jobID: String, leaseToken: String, attempt: Int, errorCode: String, retry: Bool)
    async throws
  {
    let body = try StrictJSON.encode(
      FailureRequest(attempt: attempt, errorCode: errorCode, retry: retry))
    let request = try makeRequest(
      path: "api/v1/speech-worker/jobs/\(jobID)/fail", method: "POST", body: body,
      workerToken: true, leaseToken: leaseToken)
    _ = try await send(request, accepted: [200])
  }

  private func makeRequest(
    path: String, method: String, body: Data, workerToken: Bool = false, leaseToken: String? = nil
  ) throws -> URLRequest {
    guard let username = try secrets.read(account: KeychainAccount.perimeterUsername),
      let password = try secrets.read(account: KeychainAccount.perimeterPassword),
      !username.isEmpty, !password.isEmpty
    else { throw WorkerClientError.credentialsMissing }
    let url = baseURL.appendingPathComponent(path)
    var request = URLRequest(url: url)
    request.httpMethod = method
    request.httpBody = body
    request.timeoutInterval = method == "POST" && path.hasSuffix("/lease") ? 35 : 90
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    request.setValue(
      "Basic \(Data("\(username):\(password)".utf8).base64EncodedString())",
      forHTTPHeaderField: "Authorization")
    if workerToken {
      guard let token = try secrets.read(account: KeychainAccount.workerToken), !token.isEmpty
      else { throw WorkerClientError.credentialsMissing }
      request.setValue(token, forHTTPHeaderField: "X-Doublangu-Worker-Token")
    }
    if let leaseToken {
      request.setValue(leaseToken, forHTTPHeaderField: "X-Doublangu-Lease-Token")
    }
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    return request
  }

  private func send(_ request: URLRequest, accepted: Set<Int>) async throws -> HTTPResponse {
    let response = try await requester.send(request)
    guard accepted.contains(response.statusCode) else {
      throw WorkerClientError.http(status: response.statusCode)
    }
    return response
  }
}

extension Data {
  fileprivate mutating func appendUTF8(_ value: String) { append(contentsOf: value.utf8) }
}

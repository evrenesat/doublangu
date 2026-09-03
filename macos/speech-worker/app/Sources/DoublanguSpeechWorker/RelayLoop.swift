import Foundation

/// Serial relay lane mirroring only the network mechanics of `LeaseLoop`
/// (plan §10): one job at a time, relay-only lease requests, server long poll,
/// 30 s heartbeats during a job, offline backoff, and cancellation handling.
/// There is no journal, spool, speech dependency, or local result persistence.
@MainActor
public final class RelayLoop {
  public enum Status: Equatable, Sendable {
    case off
    case misconfigured
    case idle
    case completing
    case offline
    case requiresReenrollment
    case failed(code: String)

    public var label: String {
      switch self {
      case .off: return "Off"
      case .misconfigured: return "Misconfigured"
      case .idle: return "Ready"
      case .completing: return "Completing LLM request"
      case .offline: return "Offline / backing off"
      case .requiresReenrollment: return "Re-enrollment required"
      case .failed(let code): return "Failed: \(code)"
      }
    }
  }

  enum RelayOutcome {
    case chat(RelayChatResult)
    case models(RelayModelsResult)

    func encodedData() throws -> Data {
      switch self {
      case .chat(let result):
        try result.validate()
        return try StrictJSON.encode(result)
      case .models(let result):
        try result.validate()
        return try StrictJSON.encode(result)
      }
    }
  }

  public private(set) var status: Status = .off
  public var statusChanged: ((Status) -> Void)?
  public var log: ((String) -> Void)?

  private let client: WorkerClienting
  private let http: RelayHTTPClienting
  private let keyProvider: @Sendable () -> String?
  private let heartbeatInterval: TimeInterval
  private var task: Task<Void, Never>?
  private var heartbeatTask: Task<Void, Never>?
  private var jobTask: Task<RelayOutcome, Error>?
  private var currentCancellation: CancellationFlag?
  private var currentLease: (jobID: String, attempt: Int, leaseToken: String)?
  private var staleLease = false
  private var running = false
  private var failureBackoff: TimeInterval = 1

  public init(
    client: WorkerClienting,
    http: RelayHTTPClienting,
    keyProvider: @escaping @Sendable () -> String?,
    heartbeatInterval: TimeInterval = 30
  ) {
    self.client = client
    self.http = http
    self.keyProvider = keyProvider
    self.heartbeatInterval = heartbeatInterval
  }

  public func start() {
    guard task == nil else { return }
    running = true
    setStatus(.idle)
    task = Task { [weak self] in
      await self?.run()
    }
  }

  public func stop() {
    let lease = currentLease
    let wasStale = staleLease
    running = false
    heartbeatTask?.cancel()
    heartbeatTask = nil
    jobTask?.cancel()
    jobTask = nil
    currentCancellation?.cancel()
    // Best-effort cancel acknowledgement while a valid lease is still held; a
    // stale lease means the server already stopped accepting results.
    if let lease, !wasStale {
      let client = self.client
      Task.detached {
        try? await client.fail(
          jobID: lease.jobID, leaseToken: lease.leaseToken, attempt: lease.attempt,
          errorCode: "v1.relay_canceled", retry: false)
      }
    }
    task?.cancel()
    task = nil
    currentLease = nil
    staleLease = false
    setStatus(.off)
  }

  private func run() async {
    while running && !Task.isCancelled {
      do {
        setStatus(.idle)
        guard
          let lease = try await client.lease(
            LeaseRequest(lane: .relay(LLMRelayCapability())))
        else {
          failureBackoff = 1
          try? await Task.sleep(nanoseconds: 1_000_000_000)
          continue
        }
        failureBackoff = 1
        try await process(lease: lease)
      } catch is CancellationError {
        break
      } catch let error as ProtocolError {
        // Malformed lease or protocol mismatch: the server does not consider
        // this worker relay-compatible, so the lane stops for re-enrollment.
        setStatus(.requiresReenrollment)
        log?(error.localizedDescription)
        running = false
      } catch let error as WorkerClientError {
        handleClientError(error)
        if !running { break }
        await backoffSleep()
      } catch {
        log?(error.localizedDescription)
        setStatus(.failed(code: "relay_failed"))
        await backoffSleep()
      }
    }
    if running { setStatus(.off) }
  }

  private func process(lease: LeaseResponse) async throws {
    guard let payload = lease.relay else { throw ProtocolError.invalidValue("lease_relay") }
    currentLease = (lease.jobID, lease.attempt, lease.leaseToken)
    staleLease = false
    defer { currentLease = nil }
    setStatus(.completing)
    let flag = CancellationFlag()
    currentCancellation = flag
    defer { currentCancellation = nil }
    let heartbeat = startHeartbeat(lease: lease, flag: flag)
    defer { heartbeat.cancel() }
    do {
      let outcome = try await runLocal(payload)
      guard !flag.isCancelled else { throw CancellationError() }
      let data = try outcome.encodedData()
      let bound = min(Int(payload.maxCompletionBytes), RelayLimits.maxPayloadBytes)
      guard data.count <= bound else { throw RelayHTTPError.oversized }
      try await client.completeRelay(
        jobID: lease.jobID, attempt: lease.attempt, leaseToken: lease.leaseToken, result: data)
      setStatus(.idle)
    } catch is CancellationError {
      // A stale lease (409) means the server already stopped accepting results;
      // only a live lease gets a best-effort cancel acknowledgement.
      if flag.isServerCancellation && !staleLease {
        await acknowledge(lease: lease, code: "v1.relay_canceled", retry: false)
        setStatus(.idle)
      }
    } catch let error as RelayHTTPError {
      let (code, retry) = Self.failure(for: error)
      log?(code)
      await acknowledge(lease: lease, code: code, retry: retry)
      setStatus(code == "v1.relay_unreachable" ? .offline : .idle)
    }
  }

  /// Runs the local model call on a dedicated task so a server cancellation or
  /// lane stop can cancel the underlying URLSession work directly.
  private func runLocal(_ payload: RelayLease) async throws -> RelayOutcome {
    guard let key = keyProvider(), !key.isEmpty else { throw RelayHTTPError.apiKeyMissing }
    let http = self.http
    let job = Task<RelayOutcome, Error> {
      switch payload {
      case .chat(let chat):
        let completion = try await http.chatCompletion(chat, apiKey: key)
        return .chat(
          RelayChatResult(
            requestID: chat.requestID, content: completion.content,
            reportedModel: completion.reportedModel, providerRequestID: completion.id,
            finishReason: completion.finishReason, usage: completion.usage,
            timing: completion.timing))
      case .models(let models):
        let list = try await http.listModels(apiKey: key)
        return .models(RelayModelsResult(requestID: models.requestID, models: list))
      }
    }
    jobTask = job
    defer { jobTask = nil }
    return try await job.value
  }

  private func startHeartbeat(lease: LeaseResponse, flag: CancellationFlag) -> Task<Void, Never> {
    let interval = UInt64(heartbeatInterval * 1_000_000_000)
    return Task { [weak self] in
      while !Task.isCancelled && !flag.isCancelled {
        try? await Task.sleep(nanoseconds: interval)
        guard !Task.isCancelled, !flag.isCancelled else { return }
        do {
          let response = try await self?.client.heartbeat(
            jobID: lease.jobID, leaseToken: lease.leaseToken, attempt: lease.attempt,
            progressPercent: 50)
          guard let response else { return }
          if response.cancelRequested {
            self?.cancelCurrentWork(flag: flag, stale: false)
          }
        } catch let error as WorkerClientError {
          if case .http(let status) = error, status == 409 {
            self?.cancelCurrentWork(flag: flag, stale: true)
          } else {
            await MainActor.run { self?.log?("heartbeat_failed") }
          }
        } catch {
          await MainActor.run { self?.log?("heartbeat_failed") }
        }
      }
    }
  }

  private func cancelCurrentWork(flag: CancellationFlag, stale: Bool) {
    flag.cancel(server: true)
    if stale { staleLease = true }
    jobTask?.cancel()
  }

  private func acknowledge(lease: LeaseResponse, code: String, retry: Bool) async {
    do {
      try await client.fail(
        jobID: lease.jobID, leaseToken: lease.leaseToken, attempt: lease.attempt, errorCode: code,
        retry: retry)
    } catch {
      // A refused acknowledgement (for example 409 on an already-canceled job)
      // is tolerated; the server's lease-expiry recovery remains authoritative.
      log?("failure_acknowledgement_unavailable")
    }
  }

  /// Maps typed local transport failures to the relay failure-code/retry matrix.
  static func failure(for error: RelayHTTPError) -> (code: String, retry: Bool) {
    switch error {
    case .cannotConnect, .connectionLost:
      return ("v1.relay_unreachable", true)
    case .timedOut:
      return ("v1.relay_unreachable", false)
    case .http(let status, _) where status == 408 || status == 429 || status >= 500:
      return ("v1.relay_unreachable", true)
    case .http(let status, _) where status == 401 || status == 403:
      return ("v1.relay_auth", false)
    case .apiKeyMissing:
      return ("v1.relay_auth", false)
    case .modelUnknown:
      return ("v1.relay_model_unknown", false)
    case .canceled:
      return ("v1.relay_canceled", false)
    case .http, .oversized, .invalidResponse:
      return ("v1.relay_invalid_response", false)
    }
  }

  private func handleClientError(_ error: WorkerClientError) {
    switch error {
    case .http(let status) where status == 400:
      setStatus(.requiresReenrollment)
      running = false
    case .http(let status) where status == 401 || status == 403:
      setStatus(.failed(code: "worker_authentication"))
      running = false
    case .http(let status) where status >= 500:
      setStatus(.offline)
    case .transport:
      setStatus(.offline)
    default:
      setStatus(.failed(code: error.localizedDescription))
    }
  }

  private func backoffSleep() async {
    let delay = failureBackoff
    failureBackoff = min(300, failureBackoff * 2)
    try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
  }

  private func setStatus(_ value: Status) {
    status = value
    statusChanged?(value)
  }
}

package workers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/llmrelay"
	"doublangu/internal/media"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

func relayTestService(t *testing.T) (context.Context, *store.DB, *Service) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mediaStore, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return context.Background(), db, NewService(db, mediaStore)
}

func enrollRelayWorker(t *testing.T, service *Service, ctx context.Context, withRelay bool) (*Worker, string) {
	t.Helper()
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := EnrollInput{Name: "relay-mac", ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities(), SoftwareVersion: "0.2"}
	if withRelay {
		input.LLMRelayCapabilities = []llmrelay.RelayCapability{{MaxCompletionBytes: llmrelay.RelayCapabilityBytes}}
	}
	_, token, err := service.Enroll(ctx, enrollment.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	authed, err := service.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return authed, token
}

func seedRelayJob(t *testing.T, db *store.DB, operation string) (library.ULID, []byte) {
	t.Helper()
	relay := llmrelay.NewService(db)
	requestID := library.NewULID()
	var payloadBytes []byte
	var hash string
	var err error
	if operation == llmrelay.OperationListModels {
		payloadBytes, hash, err = llmrelay.BuildListModels(requestID)
	} else {
		payloadBytes, hash, err = llmrelay.BuildChatCompletion(requestID, "qwen-test",
			[]llmrelay.Message{{Role: "user", Content: "Vertaal: de bank"}},
			json.RawMessage(`{"type":"object"}`), 0, 16384)
	}
	if err != nil {
		t.Fatal(err)
	}
	job, err := relay.Enqueue(context.Background(), payloadBytes, hash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	return job.ID, payloadBytes
}

func relayResultFor(t *testing.T, payload []byte) []byte {
	t.Helper()
	decoded, err := llmrelay.DecodeRelayPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	var result []byte
	if decoded.Operation == llmrelay.OperationListModels {
		result, err = json.Marshal(map[string]any{"request_id": decoded.RequestID, "models": []string{"qwen-test"}})
	} else {
		result, err = json.Marshal(map[string]any{
			"request_id": decoded.RequestID, "content": `{"translation":"de bank"}`,
			"reported_model": "qwen-test", "provider_request_id": "chatcmpl-1",
			"finish_reason": "stop",
			"usage":         map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
			"timing":        map[string]any{},
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func relayLease(t *testing.T, service *Service, ctx context.Context, worker *Worker) *LeaseResponse {
	t.Helper()
	lease, err := service.Lease(ctx, worker, LeaseRequest{
		ProtocolVersion:      speech.ProtocolVersion,
		LLMRelayCapabilities: []llmrelay.RelayCapability{{MaxCompletionBytes: llmrelay.RelayCapabilityBytes}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func relayPresence(t *testing.T, db *store.DB, workerID string) string {
	t.Helper()
	var seen string
	if err := db.QueryRow(context.Background(), `SELECT relay_last_seen_at FROM speech_worker WHERE id = ?`, workerID).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	return seen
}

func TestRelayEnrollAndLaneValidation(t *testing.T) {
	ctx, _, service := relayTestService(t)
	worker, _ := enrollRelayWorker(t, service, ctx, true)
	if len(worker.LLMRelayCapabilities) != 1 {
		t.Fatalf("enrolled relay capabilities = %+v", worker.LLMRelayCapabilities)
	}
	// Mixed-lane lease requests are rejected.
	_, err := service.Lease(ctx, worker, LeaseRequest{
		ProtocolVersion:      speech.ProtocolVersion,
		Capabilities:         workerCapabilities(),
		LLMRelayCapabilities: []llmrelay.RelayCapability{{MaxCompletionBytes: llmrelay.RelayCapabilityBytes}},
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("mixed lane = %v", err)
	}
	// Empty lane requests are rejected.
	_, err = service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("empty lane = %v", err)
	}
	// Wrong relay byte bound is rejected.
	_, err = service.Lease(ctx, worker, LeaseRequest{
		ProtocolVersion:      speech.ProtocolVersion,
		LLMRelayCapabilities: []llmrelay.RelayCapability{{MaxCompletionBytes: 1}},
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("wrong relay bound = %v", err)
	}
	// Two relay entries are rejected.
	_, err = service.Lease(ctx, worker, LeaseRequest{
		ProtocolVersion: speech.ProtocolVersion,
		LLMRelayCapabilities: []llmrelay.RelayCapability{
			{MaxCompletionBytes: llmrelay.RelayCapabilityBytes},
			{MaxCompletionBytes: llmrelay.RelayCapabilityBytes},
		},
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("two relay entries = %v", err)
	}
	// A worker without enrolled relay support cannot take the relay lane.
	plain, _ := enrollRelayWorker(t, service, ctx, false)
	_, err = service.Lease(ctx, plain, LeaseRequest{
		ProtocolVersion:      speech.ProtocolVersion,
		LLMRelayCapabilities: []llmrelay.RelayCapability{{MaxCompletionBytes: llmrelay.RelayCapabilityBytes}},
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("unenrolled relay lane = %v", err)
	}
}

func TestRelayLeaseResponseShape(t *testing.T) {
	ctx, db, service := relayTestService(t)
	worker, _ := enrollRelayWorker(t, service, ctx, true)
	_, payload := seedRelayJob(t, db, llmrelay.OperationChatCompletion)
	lease := relayLease(t, service, ctx, worker)
	if lease.JobType != jobs.LLMRelayJobType || lease.Operation != llmrelay.OperationChatCompletion {
		t.Fatalf("relay lease = %+v", lease)
	}
	if len(lease.Relay) == 0 {
		t.Fatal("relay lease carries no payload")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(lease.Relay, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["protocol_version"]; ok {
		t.Error("relay payload must exclude protocol_version")
	}
	if _, ok := raw["operation"]; ok {
		t.Error("relay payload must exclude operation")
	}
	if _, ok := raw["request_id"]; !ok {
		t.Error("relay payload must carry request_id")
	}
	// TTS fields stay zero for relay leases.
	if lease.RenderID.String() != "00000000000000000000000000" || lease.SpokenText != "" || lease.RequestHash != "" {
		t.Fatalf("relay lease leaks TTS fields: %+v", lease)
	}
	_ = payload
}

func TestRelayPresenceOnlyFromRelayLane(t *testing.T) {
	ctx, db, service := relayTestService(t)
	worker, token := enrollRelayWorker(t, service, ctx, true)
	if got := relayPresence(t, db, worker.ID.String()); got != "" {
		t.Fatalf("fresh enrollment presence = %q", got)
	}
	// Relay lease marks presence.
	_, payload := seedRelayJob(t, db, llmrelay.OperationChatCompletion)
	lease := relayLease(t, service, ctx, worker)
	afterLease := relayPresence(t, db, worker.ID.String())
	if afterLease == "" {
		t.Fatal("relay lease must mark presence")
	}
	// TTS heartbeat on other work must not touch relay presence.
	seedWorkerArticle(t, db)
	ttsLease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(ctx, worker, ttsLease.JobID, ttsLease.LeaseToken, HeartbeatInput{ProtocolVersion: speech.ProtocolVersion, Attempt: ttsLease.Attempt}); err != nil {
		t.Fatal(err)
	}
	if got := relayPresence(t, db, worker.ID.String()); got != afterLease {
		t.Fatalf("TTS heartbeat moved relay presence: %q -> %q", afterLease, got)
	}
	// Relay heartbeat refreshes it.
	if _, err := service.Heartbeat(ctx, worker, lease.JobID, lease.LeaseToken, HeartbeatInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt}); err != nil {
		t.Fatal(err)
	}
	_ = token
	_ = payload
}

func TestRelayCompleteAndFail(t *testing.T) {
	ctx, db, service := relayTestService(t)
	worker, _ := enrollRelayWorker(t, service, ctx, true)
	_, payload := seedRelayJob(t, db, llmrelay.OperationChatCompletion)
	lease := relayLease(t, service, ctx, worker)
	metadata := CompleteMetadata{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, LeaseToken: lease.LeaseToken}
	result := relayResultFor(t, payload)
	// Missing result is rejected.
	if err := service.Complete(ctx, worker, lease.JobID, metadata, nil, nil); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("missing result = %v", err)
	}
	// Audio alongside a relay result is rejected.
	if err := service.Complete(ctx, worker, lease.JobID, metadata, []byte{1}, result); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("audio with result = %v", err)
	}
	// Artifact alongside a relay result is rejected.
	artifact := speech.ArtifactMetadata{RequestHash: "x"}
	withArtifact := metadata
	withArtifact.Artifact = &artifact
	if err := service.Complete(ctx, worker, lease.JobID, withArtifact, nil, result); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("artifact with result = %v", err)
	}
	// Wrong request echo is rejected.
	wrong, _ := json.Marshal(map[string]any{"request_id": "other", "content": "x", "reported_model": "", "provider_request_id": "", "finish_reason": "stop", "usage": map[string]any{}, "timing": map[string]any{}})
	if err := service.Complete(ctx, worker, lease.JobID, metadata, nil, wrong); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("wrong echo = %v", err)
	}
	// Success persists the result.
	if err := service.Complete(ctx, worker, lease.JobID, metadata, nil, result); err != nil {
		t.Fatalf("relay complete = %v", err)
	}
	var stored string
	if err := db.QueryRow(ctx, `SELECT result_json FROM llm_relay_result WHERE job_id = ?`, lease.JobID.String()).Scan(&stored); err != nil {
		t.Fatalf("result row: %v", err)
	}
	// Same bytes are idempotent.
	if err := service.Complete(ctx, worker, lease.JobID, metadata, nil, result); err != nil {
		t.Fatalf("duplicate same = %v", err)
	}
	// Different bytes are rejected and the first result stays.
	decoded, _ := llmrelay.DecodeRelayPayload(payload)
	alt, _ := json.Marshal(map[string]any{
		"request_id": decoded.RequestID, "content": `{"translation":"anders"}`,
		"reported_model": "qwen-test", "provider_request_id": "chatcmpl-2",
		"finish_reason": "stop", "usage": map[string]any{}, "timing": map[string]any{},
	})
	if err := service.Complete(ctx, worker, lease.JobID, metadata, nil, alt); err == nil {
		t.Fatal("different second result must be rejected")
	} else {
		var nondeterministic *llmrelay.NondeterministicError
		if !errors.As(err, &nondeterministic) {
			t.Fatalf("wrong duplicate error: %v", err)
		}
	}
}

func TestRelayFailMatrix(t *testing.T) {
	ctx, db, service := relayTestService(t)
	worker, _ := enrollRelayWorker(t, service, ctx, true)
	failOnce := func(code string, retry bool) error {
		seedRelayJob(t, db, llmrelay.OperationChatCompletion)
		lease := relayLease(t, service, ctx, worker)
		return service.Fail(ctx, worker, lease.JobID, lease.LeaseToken, FailInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, ErrorCode: code, Retry: retry})
	}
	if err := failOnce(llmrelay.CodeUnreachable, true); err != nil {
		t.Fatalf("transient unreachable with retry = %v", err)
	}
	if err := failOnce(llmrelay.CodeUnreachable, false); err != nil {
		t.Fatalf("unreachable without retry = %v", err)
	}
	if err := failOnce(llmrelay.CodeAuth, false); err != nil {
		t.Fatalf("auth without retry = %v", err)
	}
	if err := failOnce(llmrelay.CodeAuth, true); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("auth with retry = %v", err)
	}
	if err := failOnce(llmrelay.CodeModelUnknown, true); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("model-unknown with retry = %v", err)
	}
	if err := failOnce("v1.something_else", false); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("unknown relay code = %v", err)
	}
	// Existing TTS failure behavior is unchanged: any well-formed v1 code.
	seedWorkerArticle(t, db)
	ttsLease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Fail(ctx, worker, ttsLease.JobID, ttsLease.LeaseToken, FailInput{ProtocolVersion: speech.ProtocolVersion, Attempt: ttsLease.Attempt, ErrorCode: "v1.something_else", Retry: false}); err != nil {
		t.Fatalf("tts arbitrary code = %v", err)
	}
}

func TestConcurrentTTSandRelayLeases(t *testing.T) {
	ctx, db, service := relayTestService(t)
	worker, _ := enrollRelayWorker(t, service, ctx, true)
	seedWorkerArticle(t, db)
	seedRelayJob(t, db, llmrelay.OperationChatCompletion)
	ttsLease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	relay := relayLease(t, service, ctx, worker)
	if ttsLease.JobID == relay.JobID {
		t.Fatal("lanes must hold distinct leases")
	}
	if ttsLease.JobType == relay.JobType {
		t.Fatal("lanes must hold distinct job types")
	}
	var owners []string
	rows, err := db.Query(ctx, `SELECT DISTINCT lease_owner FROM job WHERE id IN (?, ?)`, ttsLease.JobID.String(), relay.JobID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
	}
	if len(owners) != 1 || owners[0] != worker.ID.String() {
		t.Fatalf("both leases must belong to one worker, got %v", owners)
	}
}

func TestRelayMalformedLeaseAndStaleToken(t *testing.T) {
	ctx, db, service := relayTestService(t)
	worker, _ := enrollRelayWorker(t, service, ctx, true)
	seedRelayJob(t, db, llmrelay.OperationChatCompletion)
	// A relay lease whose payload owner does not match is malformed.
	lease, err := service.jobs.ClaimMatching(ctx, jobs.TargetMacOS, worker.ID.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE job SET owner_id = 'tampered' WHERE id = ?`, lease.ID.String()); err != nil {
		t.Fatal(err)
	}
	tampered, err := service.jobs.Get(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.leaseResponse(ctx, &jobs.Lease{Job: *tampered, LeaseToken: lease.LeaseToken}); !errors.Is(err, ErrMalformedJob) {
		t.Fatalf("wrong owner lease = %v", err)
	}
	if !relayJobMatches(*tampered) {
		t.Log("matcher correctly skips the tampered job")
	} else {
		t.Fatal("matcher must skip jobs whose owner disagrees with the payload")
	}
	// A stale lease token is rejected on relay complete and fail.
	_, payload := seedRelayJob(t, db, llmrelay.OperationChatCompletion)
	fresh := relayLease(t, service, ctx, worker)
	metadata := CompleteMetadata{ProtocolVersion: speech.ProtocolVersion, Attempt: fresh.Attempt, LeaseToken: "stale-token"}
	if err := service.Complete(ctx, worker, fresh.JobID, metadata, nil, relayResultFor(t, payload)); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale complete = %v", err)
	}
	if err := service.Fail(ctx, worker, fresh.JobID, "stale-token", FailInput{ProtocolVersion: speech.ProtocolVersion, Attempt: fresh.Attempt, ErrorCode: llmrelay.CodeAuth}); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale fail = %v", err)
	}
}

func TestV01WorkerBackwardCompatibility(t *testing.T) {
	ctx, db, service := relayTestService(t)
	// v0.1 enrollment carries no relay capability.
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worker, _, err := service.Enroll(ctx, enrollment.Token, EnrollInput{Name: "v01", ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities(), SoftwareVersion: "0.1"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"llm_relay", "relay", "operation"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("v0.1 enroll response leaks %q: %s", key, encoded)
		}
	}
	seedWorkerArticle(t, db)
	lease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"operation"`, `"relay"`, "llm_relay"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("v0.1 lease response leaks %q: %s", key, encoded)
		}
	}
	if _, err := service.Heartbeat(ctx, worker, lease.JobID, lease.LeaseToken, HeartbeatInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt}); err != nil {
		t.Fatal(err)
	}
	data := fakeM4A()
	artifact := artifactFor(data, lease.RequestHash)
	if err := service.Complete(ctx, worker, lease.JobID, CompleteMetadata{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, LeaseToken: lease.LeaseToken, Artifact: &artifact}, data, nil); err != nil {
		t.Fatal(err)
	}
	if got := relayPresence(t, db, worker.ID.String()); got != "" {
		t.Fatalf("TTS traffic marked relay presence: %q", got)
	}
}

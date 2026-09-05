package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/llmrelay"
	"doublangu/internal/localworker"
	"doublangu/internal/media"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

func ailocalsEntry(id, category string, params any) localworker.CapabilityEntry {
	return localworker.CapabilityEntry{ID: id, Category: category, Parameters: params}
}

func appleParams() *localworker.TtsCapabilityParameters {
	return &localworker.TtsCapabilityParameters{
		Engine: "avspeech", Languages: []string{"nl-NL"},
		UnitKinds: []string{"word"}, MaxBytes: 2 << 20, MaxDurationMS: 15000,
	}
}

func chatterboxParams() *localworker.TtsCapabilityParameters {
	return &localworker.TtsCapabilityParameters{
		Engine: "chatterbox", Languages: []string{"nl"},
		UnitKinds: []string{"sentence"}, MaxBytes: 2 << 20, MaxDurationMS: 30000,
	}
}

func relayParams() *localworker.RelayCapabilityParameters {
	return &localworker.RelayCapabilityParameters{
		MaxCompletionBytes: localworker.PayloadMaxBytes,
		Operations:         []string{"chat_completion", "list_models"},
	}
}

func newAilocalsTestService(t *testing.T) (*store.DB, *Service, context.Context) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	seedWorkerArticle(t, db)
	mediaStore, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return db, NewService(db, mediaStore), context.Background()
}

func ailocalsEnroll(t *testing.T, service *Service, ctx context.Context, name string, entries ...localworker.CapabilityEntry) (*Worker, string) {
	t.Helper()
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := &localworker.EnrollRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		WorkerName:      name,
		SoftwareVersion: "0.1.0",
		Capabilities:    entries,
	}
	outcome, err := service.EnrollAilocals(ctx, enrollment.Token, request)
	if err != nil {
		t.Fatalf("EnrollAilocals(%s): %v", name, err)
	}
	worker, err := service.AuthenticateAilocals(ctx, outcome.Token)
	if err != nil {
		t.Fatalf("AuthenticateAilocals: %v", err)
	}
	return worker, outcome.Token
}

func TestAilocalsEnrollmentGrantsAndSingleClient(t *testing.T) {
	db, service, ctx := newAilocalsTestService(t)
	_ = db

	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := &localworker.EnrollRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		WorkerName:      "Fixture Mac",
		SoftwareVersion: "0.1.0",
		Capabilities: []localworker.CapabilityEntry{
			ailocalsEntry(localworker.CapabilityAppleSpeech, "tts", appleParams()),
			ailocalsEntry(localworker.CapabilityChatterbox, "tts", chatterboxParams()),
		},
	}
	if _, err := service.EnrollAilocals(ctx, enrollment.Token, request); err != nil {
		t.Fatalf("enroll failed: %v", err)
	}

	// A second common enrollment is refused and leaves the new token unused.
	second, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnrollAilocals(ctx, second.Token, request); err == nil {
		t.Fatal("second common enrollment must be refused")
	} else {
		var protocolErr *localworker.Error
		if !errors.As(err, &protocolErr) || protocolErr.Code != localworker.CodeClientAlreadyEnrolled {
			t.Fatalf("second enrollment error = %v", err)
		}
	}
	// The music capability is rejected even when the Mac offers it globally.
	third, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	music := &localworker.EnrollRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		WorkerName:      "Music Mac",
		SoftwareVersion: "0.1.0",
		Capabilities: []localworker.CapabilityEntry{
			ailocalsEntry(localworker.CapabilityACE, "music", nil),
		},
	}
	if _, err := service.EnrollAilocals(ctx, third.Token, music); err == nil {
		t.Fatal("music capability must be rejected")
	}

	// A legacy worker credential is not valid on common routes.
	legacy, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, legacyToken, err := service.Enroll(ctx, legacy.Token, EnrollInput{
		Name: "legacy", ProtocolVersion: speech.ProtocolVersion,
		Capabilities: workerCapabilities(), SoftwareVersion: "0.1",
	})
	if err != nil {
		t.Fatalf("legacy enroll: %v", err)
	}
	if _, err := service.AuthenticateAilocals(ctx, legacyToken); err == nil {
		t.Fatal("legacy credentials must be rejected on common routes")
	}
}

func TestAilocalsPresenceEnforcesGrantsAndDrivesRelayAvailability(t *testing.T) {
	_, service, ctx := newAilocalsTestService(t)
	worker, _ := ailocalsEnroll(t, service, ctx, "All services Mac",
		ailocalsEntry(localworker.CapabilityAppleSpeech, "tts", appleParams()),
		ailocalsEntry(localworker.CapabilityChatterbox, "tts", chatterboxParams()),
		ailocalsEntry(localworker.CapabilityRelay, "llm", relayParams()),
	)

	paused := "user_paused"
	ready := &localworker.PresenceRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		Capabilities: []localworker.PresenceEntry{
			{ID: localworker.CapabilityAppleSpeech, State: localworker.PresenceReady, Accepting: true, ActiveJobs: 0},
			{ID: localworker.CapabilityChatterbox, State: localworker.PresenceBusy, Accepting: false, ActiveJobs: 1},
			{ID: localworker.CapabilityRelay, State: localworker.PresenceReady, Accepting: true, ActiveJobs: 0},
		},
	}
	if _, err := service.PresenceAilocals(ctx, worker, ready); err != nil {
		t.Fatalf("presence rejected: %v", err)
	}
	if !service.relay.Available(ctx) {
		t.Fatal("relay must be available while common presence advertises it ready")
	}

	pausedSnapshot := &localworker.PresenceRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		Capabilities: []localworker.PresenceEntry{
			{ID: localworker.CapabilityAppleSpeech, State: localworker.PresenceReady, Accepting: true, ActiveJobs: 0},
			{ID: localworker.CapabilityChatterbox, State: localworker.PresenceBusy, Accepting: false, ActiveJobs: 1},
			{ID: localworker.CapabilityRelay, State: localworker.PresencePaused, Accepting: false, ActiveJobs: 0, Reason: &paused},
		},
	}
	if _, err := service.PresenceAilocals(ctx, worker, pausedSnapshot); err != nil {
		t.Fatalf("paused presence rejected: %v", err)
	}
	if service.relay.Available(ctx) {
		t.Fatal("paused relay must not count as available")
	}

}

func TestAilocalsPresenceRefusesOutsideGrant(t *testing.T) {
	_, service, ctx := newAilocalsTestService(t)
	appleOnly, _ := ailocalsEnroll(t, service, ctx, "Apple-only Mac",
		ailocalsEntry(localworker.CapabilityAppleSpeech, "tts", appleParams()),
	)
	outside := &localworker.PresenceRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		Capabilities: []localworker.PresenceEntry{
			{ID: localworker.CapabilityRelay, State: localworker.PresenceReady, Accepting: true, ActiveJobs: 0},
		},
	}
	if _, err := service.PresenceAilocals(ctx, appleOnly, outside); err == nil {
		t.Fatal("outside-grant presence must be refused")
	} else {
		var protocolErr *localworker.Error
		if !errors.As(err, &protocolErr) || protocolErr.Code != localworker.CodeUnsupportedCap {
			t.Fatalf("outside-grant error = %v", err)
		}
	}
	// Leasing is equally fenced by the persisted grant.
	if _, err := service.LeaseAilocals(ctx, appleOnly, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityChatterbox,
		WaitSeconds:     0,
	}); err == nil {
		t.Fatal("outside-grant lease must be refused")
	} else {
		var protocolErr *localworker.Error
		if !errors.As(err, &protocolErr) || protocolErr.Code != localworker.CodeUnsupportedCap {
			t.Fatalf("outside-grant lease error = %v", err)
		}
	}
}

func TestAilocalsLeaseTTSUsesExactDomainFieldsAndBusyRules(t *testing.T) {
	_, service, ctx := newAilocalsTestService(t)
	worker, token := ailocalsEnroll(t, service, ctx, "Chatterbox Mac",
		ailocalsEntry(localworker.CapabilityChatterbox, "tts", chatterboxParams()),
	)
	_ = token

	// The seeded article queues a chatterbox narration job; wait=0 claims
	// it with exactly one attempt.
	lease, err := service.LeaseAilocals(ctx, worker, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityChatterbox,
		WaitSeconds:     0,
	})
	if err != nil {
		t.Fatalf("lease failed: %v", err)
	}
	if lease == nil {
		t.Fatal("expected a chatterbox lease")
	}
	if lease.ProtocolVersion != localworker.ProtocolVersion || lease.CapabilityID != localworker.CapabilityChatterbox {
		t.Fatalf("lease envelope = %+v", lease)
	}
	if lease.DeadlineAt != nil {
		t.Fatal("DL leases must not invent a deadline")
	}
	decoded, err := localworker.DecodePayload(lease.PayloadBase64, lease.PayloadSHA256)
	if err != nil {
		t.Fatalf("lease payload invalid: %v", err)
	}
	var payload localworker.TtsPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("payload is not a TTS payload: %v", err)
	}
	if payload.RequestHash == "" || payload.RenderID == "" || payload.Profile.Engine != "chatterbox" {
		t.Fatalf("payload domain fields = %+v", payload)
	}
	if payload.Profile.MIMEType != speech.AudioMIME || payload.Profile.SampleRateHz != speech.AudioSampleRate {
		t.Fatalf("payload profile identity = %+v", payload.Profile)
	}

	// A second active lease for the same capability is worker_busy.
	if _, err := service.LeaseAilocals(ctx, worker, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityChatterbox,
		WaitSeconds:     0,
	}); err == nil {
		t.Fatal("second concurrent lease must be refused")
	} else {
		var protocolErr *localworker.Error
		if !errors.As(err, &protocolErr) || protocolErr.Code != localworker.CodeWorkerBusy {
			t.Fatalf("busy error = %v", err)
		}
	}
}

func TestAilocalsRelayLeaseCarriesExactStoredBytesAndCompletes(t *testing.T) {
	db, service, ctx := newAilocalsTestService(t)
	worker, _ := ailocalsEnroll(t, service, ctx, "Relay Mac",
		ailocalsEntry(localworker.CapabilityRelay, "llm", relayParams()),
	)

	// wait=0 with no relay work performs one claim attempt and returns nil.
	if lease, err := service.LeaseAilocals(ctx, worker, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityRelay,
		WaitSeconds:     0,
	}); err != nil || lease != nil {
		t.Fatalf("empty relay lease = %+v err=%v", lease, err)
	}

	requestID := library.NewULID()
	schema := json.RawMessage(`{"type":"object"}`)
	messages := []llmrelay.Message{{Role: "user", Content: "Geef de metadata voor de demozin."}}
	relayPayload, relayHash, err := llmrelay.BuildChatCompletion(requestID, "test-model", messages, schema, 500, 2048)
	if err != nil {
		t.Fatalf("build relay payload: %v", err)
	}
	if _, err := service.relay.Enqueue(ctx, relayPayload, relayHash, requestID.String()); err != nil {
		t.Fatalf("enqueue relay job: %v", err)
	}

	lease, err := service.LeaseAilocals(ctx, worker, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityRelay,
		WaitSeconds:     0,
	})
	if err != nil {
		t.Fatalf("relay lease failed: %v", err)
	}
	if lease == nil {
		t.Fatal("expected a relay lease")
	}
	decoded, err := localworker.DecodePayload(lease.PayloadBase64, lease.PayloadSHA256)
	if err != nil {
		t.Fatalf("relay lease payload invalid: %v", err)
	}
	var storedPayload string
	if err := db.QueryRow(ctx, `SELECT payload_json FROM job WHERE id = ?`, library.ULID(lease.JobID)).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	if string(decoded) != storedPayload {
		t.Fatal("relay lease payload must be the exact stored job bytes")
	}
	envelope, err := localworker.DecodeRelayPayload(decoded)
	if err != nil || envelope.Operation != "chat_completion" {
		t.Fatalf("relay envelope = %+v err=%v", envelope, err)
	}

	// Complete with a valid chat result; the digest is stored atomically.
	result := map[string]any{
		"request_id":          requestID.String(),
		"content":             `{"title":"demo"}`,
		"reported_model":      "test-model",
		"provider_request_id": "req-1",
		"finish_reason":       "stop",
		"usage": map[string]any{
			"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3,
		},
		"timing": map[string]any{"total_time": 1.5},
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(resultBytes)
	metadata := &localworker.CompleteMetadata{
		ProtocolVersion: localworker.ProtocolVersion,
		Attempt:         lease.Attempt,
		ResultSHA256:    hex.EncodeToString(digest[:]),
	}
	if err := service.CompleteAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, metadata, nil, resultBytes); err != nil {
		t.Fatalf("relay complete failed: %v", err)
	}
	var jobState, storedDigest string
	if err := db.QueryRow(ctx, `SELECT state, ailocals_result_sha256 FROM job WHERE id = ?`, lease.JobID).Scan(&jobState, &storedDigest); err != nil {
		t.Fatal(err)
	}
	if jobState != jobs.StateSucceeded || storedDigest != metadata.ResultSHA256 {
		t.Fatalf("relay completion state = %s digest = %q", jobState, storedDigest)
	}

	// Identical retry after success: same digest and exact bytes succeed.
	if err := service.CompleteAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, metadata, nil, resultBytes); err != nil {
		t.Fatalf("identical retry must succeed: %v", err)
	}
	altered := append([]byte(nil), resultBytes...)
	altered = append(altered, []byte(" ")...)
	alteredMetadata := &localworker.CompleteMetadata{
		ProtocolVersion: localworker.ProtocolVersion,
		Attempt:         lease.Attempt,
		ResultSHA256:    hex.EncodeToString(func() []byte { sum := sha256.Sum256(altered); return sum[:] }()),
	}
	if err := service.CompleteAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, alteredMetadata, nil, altered); err == nil {
		t.Fatal("altered result must conflict")
	} else {
		var protocolErr *localworker.Error
		if !errors.As(err, &protocolErr) || protocolErr.Code != localworker.CodeResultConflict {
			t.Fatalf("altered result error = %v", err)
		}
	}
}

func TestAilocalsFailureMappingAndForeignWorkerIsolation(t *testing.T) {
	db, service, ctx := newAilocalsTestService(t)
	worker, _ := ailocalsEnroll(t, service, ctx, "Isolated Mac",
		ailocalsEntry(localworker.CapabilityChatterbox, "tts", chatterboxParams()),
	)
	lease, err := service.LeaseAilocals(ctx, worker, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityChatterbox,
		WaitSeconds:     0,
	})
	if err != nil || lease == nil {
		t.Fatalf("lease = %+v err=%v", lease, err)
	}

	// A legacy worker identity cannot acknowledge a common lease.
	legacyWorker, _ := ailocalsEnrollForLegacyLane(t, service, ctx)
	if err := service.FailAilocals(ctx, legacyWorker, library.ULID(lease.JobID), lease.LeaseToken, &localworker.FailRequest{
		ProtocolVersion: localworker.ProtocolVersion, Attempt: lease.Attempt,
		Code: "canceled", Retryable: false,
	}); err == nil {
		t.Fatal("foreign worker failure must be refused")
	}

	// Relay-specific failure codes are invalid for TTS workloads.
	if err := service.FailAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, &localworker.FailRequest{
		ProtocolVersion: localworker.ProtocolVersion, Attempt: lease.Attempt,
		Code: "relay_unreachable", Retryable: true,
	}); err == nil {
		t.Fatal("relay failure code must fail validation for TTS")
	}

	if err := service.FailAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, &localworker.FailRequest{
		ProtocolVersion: localworker.ProtocolVersion, Attempt: lease.Attempt,
		Code: "resource_exhausted", Retryable: true,
	}); err != nil {
		t.Fatalf("resource_exhausted failure rejected: %v", err)
	}
	var errorCode string
	if err := db.QueryRow(ctx, `SELECT error_code FROM job WHERE id = ?`, lease.JobID).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "v1.resource_exhausted" {
		t.Fatalf("mapped product code = %q", errorCode)
	}
}

func TestAilocalsTTSCompletionWritesDigestAtomicallyAndRetriesIdempotently(t *testing.T) {
	db, service, ctx := newAilocalsTestService(t)
	worker, _ := ailocalsEnroll(t, service, ctx, "TTS Mac",
		ailocalsEntry(localworker.CapabilityChatterbox, "tts", chatterboxParams()),
	)
	lease, err := service.LeaseAilocals(ctx, worker, &localworker.LeaseRequest{
		ProtocolVersion: localworker.ProtocolVersion,
		CapabilityID:    localworker.CapabilityChatterbox,
		WaitSeconds:     0,
	})
	if err != nil || lease == nil {
		t.Fatalf("lease = %+v err=%v", lease, err)
	}
	decoded, err := localworker.DecodePayload(lease.PayloadBase64, lease.PayloadSHA256)
	if err != nil {
		t.Fatal(err)
	}
	var payload localworker.TtsPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	data := fakeM4A()
	artifact := artifactFor(data, payload.RequestHash)
	artifactBody, err := json.Marshal(map[string]any{"artifact": artifact})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifactBody)
	metadata := &localworker.CompleteMetadata{
		ProtocolVersion: localworker.ProtocolVersion,
		Attempt:         lease.Attempt,
		ResultSHA256:    hex.EncodeToString(digest[:]),
	}
	if err := service.CompleteAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, metadata, data, artifactBody); err != nil {
		t.Fatalf("TTS completion failed: %v", err)
	}
	var jobState, storedDigest string
	if err := db.QueryRow(ctx, `SELECT state, ailocals_result_sha256 FROM job WHERE id = ?`, lease.JobID).Scan(&jobState, &storedDigest); err != nil {
		t.Fatal(err)
	}
	if jobState != jobs.StateSucceeded || storedDigest != metadata.ResultSHA256 {
		t.Fatalf("state = %s digest = %q", jobState, storedDigest)
	}
	// Identical retry after success succeeds; altered bytes conflict.
	if err := service.CompleteAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, metadata, data, artifactBody); err != nil {
		t.Fatalf("identical retry must succeed: %v", err)
	}
	if err := service.CompleteAilocals(ctx, worker, library.ULID(lease.JobID), lease.LeaseToken, metadata, data[:3], artifactBody); err == nil {
		t.Fatal("mismatched artifact bytes must be refused")
	}
}

func ailocalsEnrollForLegacyLane(t *testing.T, service *Service, ctx context.Context) (*Worker, string) {
	t.Helper()
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worker, token, err := service.Enroll(ctx, enrollment.Token, EnrollInput{
		Name: "legacy-lane", ProtocolVersion: speech.ProtocolVersion,
		Capabilities: workerCapabilities(), SoftwareVersion: "0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker, token
}

func TestAilocalsTTSResultPartIsStrict(t *testing.T) {
	if _, err := decodeAilocalsTtsResult([]byte(`{"artifact":{},"extra":1}`)); err == nil {
		t.Fatal("unknown fields in the result part must be rejected")
	}
	if _, err := decodeAilocalsTtsResult([]byte(`{"nope":1}`)); err == nil {
		t.Fatal("a missing artifact must be rejected")
	}
	if _, err := decodeAilocalsTtsResult([]byte(strings.Repeat("x", 3))); err == nil {
		t.Fatal("a non-JSON result part must be rejected")
	}
}

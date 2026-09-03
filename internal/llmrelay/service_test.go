package llmrelay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

func openRelayTestDB(t *testing.T) (*store.DB, *Service) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, NewService(db)
}

func seedRelayWorker(t *testing.T, db *store.DB, present bool) string {
	t.Helper()
	lastSeen := ""
	if present {
		lastSeen = store.NowUTC()
	}
	id := library.NewULID().String()
	if _, err := db.Exec(context.Background(), `INSERT INTO speech_worker (id, name, protocol_version, token_hash, llm_relay_capabilities_json, relay_last_seen_at) VALUES (?, 'relay-mac', 'speech-worker.v1', ?, '[{"max_completion_bytes":2097152}]', ?)`, id, "hash-"+id, lastSeen); err != nil {
		t.Fatal(err)
	}
	return id
}

func relayChatPayload(t *testing.T) (requestID library.ULID, payload []byte, inputHash string) {
	t.Helper()
	requestID = library.NewULID()
	var err error
	payload, inputHash, err = BuildChatCompletion(requestID, "qwen-test", testMessages(), testSchema(), 0, 16384)
	if err != nil {
		t.Fatal(err)
	}
	return requestID, payload, inputHash
}

func relayChatResultJSON(t *testing.T, requestID library.ULID) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"request_id": requestID.String(), "content": `{"translation":"de bank"}`,
		"reported_model": "qwen-test", "provider_request_id": "chatcmpl-1",
		"finish_reason": "stop",
		"usage":         map[string]any{"prompt_tokens": 4, "completion_tokens": 8, "total_tokens": 12},
		"timing":        map[string]any{"generation_duration": 1.25},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func claimRelay(t *testing.T, svc *Service, workerID string) *jobs.Lease {
	t.Helper()
	lease, err := svc.jobs.ClaimMatching(context.Background(), jobs.TargetMacOS, workerID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

// backgroundClaimComplete claims and completes one relay job, reporting every
// outcome on done. It never calls t.Fatal: testing helpers must not fail
// from a non-test goroutine.
func backgroundClaimComplete(svc *Service, db *store.DB, jobID library.ULID, workerID string, resultJSON []byte, done chan<- error) {
	lease, err := svc.jobs.ClaimMatching(context.Background(), jobs.TargetMacOS, workerID, nil)
	if err != nil {
		done <- err
		return
	}
	current, err := svc.jobs.Get(context.Background(), jobID)
	if err != nil {
		done <- err
		return
	}
	done <- db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		return svc.CompleteTx(context.Background(), tx, *current, lease.AttemptCount, lease.LeaseToken, resultJSON)
	})
}

func TestEnqueueIdentity(t *testing.T) {
	_, svc := openRelayTestDB(t)
	ctx := context.Background()
	_, payloadOne, hashOne := relayChatPayload(t)
	jobOne, err := svc.Enqueue(ctx, payloadOne, hashOne, library.NewULID().String())
	if err != nil {
		t.Fatal(err)
	}
	// A fresh relay request ULID produces a fresh job even for identical turns.
	_, payloadTwo, hashTwo := relayChatPayload(t)
	if hashOne == hashTwo {
		t.Fatal("fresh request ids must produce distinct input hashes")
	}
	jobTwo, err := svc.Enqueue(ctx, payloadTwo, hashTwo, library.NewULID().String())
	if err != nil {
		t.Fatal(err)
	}
	if jobOne.ID == jobTwo.ID {
		t.Fatal("distinct relay requests must not share a job")
	}
	// Generic idempotency is unchanged: the same bytes return the same row.
	same, err := svc.Enqueue(ctx, payloadOne, hashOne, library.NewULID().String())
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != jobOne.ID {
		t.Fatal("identical payload must return the existing job")
	}
	if jobOne.JobType != jobs.LLMRelayJobType || jobOne.OwnerType != OwnerType {
		t.Fatalf("job identity wrong: %+v", jobOne)
	}
}

func TestWaitSuccess(t *testing.T) {
	db, svc := openRelayTestDB(t)
	ctx := context.Background()
	workerID := seedRelayWorker(t, db, true)
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	resultJSON := relayChatResultJSON(t, requestID)
	done := make(chan error, 1)
	go backgroundClaimComplete(svc, db, job.ID, workerID, resultJSON, done)
	stored, err := svc.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stored.Operation != OperationChatCompletion {
		t.Fatalf("operation = %q", stored.Operation)
	}
	decoded, err := DecodeChatResult([]byte(stored.ResultJSON), requestID.String(), MaxCompletionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Content != `{"translation":"de bank"}` || decoded.ProviderRequestID != "chatcmpl-1" {
		t.Fatalf("stored result mismatch: %+v", decoded)
	}
}

func TestWaitFailFastOffline(t *testing.T) {
	_, svc := openRelayTestDB(t)
	ctx := context.Background()
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	if svc.Available(ctx) {
		t.Fatal("no worker enrolled: relay must be unavailable")
	}
	if _, err := svc.Wait(ctx, job.ID); err == nil {
		t.Fatal("offline relay must fail fast")
	} else {
		var relayErr *Error
		if !errors.As(err, &relayErr) || relayErr.Code != CodeUnavailable {
			t.Fatalf("wrong fail-fast error: %v", err)
		}
	}
	current, err := svc.jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.StateCanceled {
		t.Fatalf("abandoned queued relay must be canceled, got %q", current.State)
	}
}

func TestWaitQueuedExitsWhenPresenceLost(t *testing.T) {
	db, svc := openRelayTestDB(t)
	ctx := context.Background()
	seedRelayWorker(t, db, true)
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Available(ctx) {
		t.Fatal("seeded worker must be available")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = db.Exec(context.Background(), `UPDATE speech_worker SET relay_last_seen_at = ''`)
	}()
	if _, err := svc.Wait(ctx, job.ID); err == nil {
		t.Fatal("lost presence must end the wait")
	} else {
		var relayErr *Error
		if !errors.As(err, &relayErr) || relayErr.Code != CodeUnavailable {
			t.Fatalf("wrong presence-loss error: %v", err)
		}
	}
}

func TestWaitParentCancel(t *testing.T) {
	db, svc := openRelayTestDB(t)
	seedRelayWorker(t, db, true)
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(context.Background(), payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if _, err := svc.Wait(ctx, job.ID); err == nil {
		t.Fatal("canceled parent must end the wait")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation must be preserved, got %v", err)
	}
	current, err := svc.jobs.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.StateCanceled || current.ErrorCode != CodeParentCanceled {
		t.Fatalf("child must be canceled with the parent code, got %+v", current)
	}
}

func TestWaitTerminalFailure(t *testing.T) {
	db, svc := openRelayTestDB(t)
	ctx := context.Background()
	workerID := seedRelayWorker(t, db, true)
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	lease := claimRelay(t, svc, workerID)
	if err := svc.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, CodeAuth, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Wait(ctx, job.ID); err == nil {
		t.Fatal("terminal failure must surface")
	} else {
		var terminal *TerminalError
		if !errors.As(err, &terminal) || terminal.Code != CodeAuth {
			t.Fatalf("wrong terminal error: %v", err)
		}
	}
}

func TestWaitRecoversExpiredLease(t *testing.T) {
	db, svc := openRelayTestDB(t)
	ctx := context.Background()
	workerID := seedRelayWorker(t, db, true)
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	lease := claimRelay(t, svc, workerID)
	if _, err := db.Exec(ctx, `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, job.ID.String()); err != nil {
		t.Fatal(err)
	}
	resultJSON := relayChatResultJSON(t, requestID)
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			retry, err := svc.jobs.ClaimMatching(context.Background(), jobs.TargetMacOS, workerID, nil)
			if err == nil {
				current, err := svc.jobs.Get(context.Background(), job.ID)
				if err != nil {
					done <- err
					return
				}
				done <- db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
					return svc.CompleteTx(context.Background(), tx, *current, retry.AttemptCount, retry.LeaseToken, resultJSON)
				})
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		done <- errors.New("retry lease never became available")
	}()
	stored, err := svc.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stored.Operation != OperationChatCompletion {
		t.Fatalf("operation = %q", stored.Operation)
	}
	if lease.AttemptCount != 1 {
		t.Fatalf("first attempt = %d", lease.AttemptCount)
	}
	final, err := svc.jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.AttemptCount != 2 || final.State != jobs.StateSucceeded {
		t.Fatalf("expired lease must retry once, got %+v", final)
	}
}

func TestCompleteDuplicateSameAndDifferent(t *testing.T) {
	db, svc := openRelayTestDB(t)
	ctx := context.Background()
	workerID := seedRelayWorker(t, db, true)
	requestID, payload, inputHash := relayChatPayload(t)
	job, err := svc.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	lease := claimRelay(t, svc, workerID)
	resultJSON := relayChatResultJSON(t, requestID)
	complete := func(token string, result []byte) error {
		current, err := svc.jobs.Get(ctx, job.ID)
		if err != nil {
			return err
		}
		return db.WithTransaction(ctx, func(tx *sql.Tx) error {
			return svc.CompleteTx(ctx, tx, *current, lease.AttemptCount, token, result)
		})
	}
	if err := complete(lease.LeaseToken, resultJSON); err != nil {
		t.Fatal(err)
	}
	// Same canonical bytes are idempotent.
	if err := complete(lease.LeaseToken, resultJSON); err != nil {
		t.Fatalf("duplicate same result must be idempotent: %v", err)
	}
	// Different bytes are rejected and the first result stays.
	other, err := json.Marshal(map[string]any{
		"request_id": requestID.String(), "content": `{"translation":"other"}`,
		"reported_model": "qwen-test", "provider_request_id": "chatcmpl-2",
		"finish_reason": "stop",
		"usage":         map[string]any{},
		"timing":        map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := complete(lease.LeaseToken, other); err == nil {
		t.Fatal("different second result must be rejected")
	} else {
		var nondeterministic *NondeterministicError
		if !errors.As(err, &nondeterministic) {
			t.Fatalf("wrong duplicate error: %v", err)
		}
	}
	stored, err := svc.GetResult(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeChatResult([]byte(stored.ResultJSON), requestID.String(), MaxCompletionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProviderRequestID != "chatcmpl-1" {
		t.Fatalf("first result must stay authoritative: %+v", decoded)
	}
}

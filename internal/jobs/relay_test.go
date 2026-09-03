package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

func relaySpec(key string) Spec {
	return Spec{
		JobType: LLMRelayJobType, ExecutionTarget: TargetMacOS,
		OwnerType: "llm_relay", OwnerID: library.NewULID().String(),
		IdempotencyKey: "llm.relay:" + key, InputHash: key,
		PayloadJSON: `{"protocol_version":"speech-worker.v1","operation":"list_models"}`,
	}
}

func TestRelayJobTypeEnqueues(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)
	job, err := s.Enqueue(ctx, relaySpec("relay-enqueue-1"))
	if err != nil {
		t.Fatal(err)
	}
	if job.JobType != LLMRelayJobType || job.State != StateQueued {
		t.Fatalf("relay job mismatch: %+v", job)
	}
}

func TestRecoverExpiredJobRetryBackoff(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)
	job, err := s.Enqueue(ctx, relaySpec("relay-backoff-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMatching(ctx, TargetMacOS, "worker-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, job.ID.String()); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	recovered, err := s.RecoverExpiredJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("expired relay job must be recovered")
	}
	current, err := s.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != StateQueued || current.ErrorCode != LeaseExpiredErrorCode {
		t.Fatalf("recovered job mismatch: %+v", current)
	}
	available, err := time.Parse("2006-01-02T15:04:05.000Z", current.AvailableAt)
	if err != nil {
		t.Fatal(err)
	}
	// First attempt uses the 5 s backoff exactly.
	if delay := available.Sub(before); delay < 4*time.Second || delay > 6*time.Second {
		t.Fatalf("first retry backoff = %v, want ~5s", delay)
	}
	// The reclaimed job waits out its backoff, so force it available and
	// claim it to hold a live lease, which must not be recovered.
	if _, err := db.Exec(ctx, `UPDATE job SET available_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, job.ID.String()); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ClaimMatching(ctx, TargetMacOS, "worker-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = s.RecoverExpiredJob(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered {
		t.Fatal("live lease must not be recovered")
	}
}

func TestRecoverExpiredJobTerminal(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var terminalCalls int
	plain := NewStore(db, func(_ context.Context, _ *sql.Tx, job Job) error {
		terminalCalls++
		if job.JobType != LLMRelayJobType || job.ErrorCode != LeaseExpiredErrorCode {
			t.Errorf("terminal callback job mismatch: %+v", job)
		}
		return nil
	})
	job, err := plain.Enqueue(ctx, relaySpec("relay-terminal-1"))
	if err != nil {
		t.Fatal(err)
	}
	// Drive the job through all three attempts, expiring each lease. Later
	// claims wait out the 5 s / 30 s backoff, so force availability.
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := db.Exec(ctx, `UPDATE job SET available_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, job.ID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := plain.ClaimMatching(ctx, TargetMacOS, "worker-1", nil); err != nil {
			t.Fatalf("claim %d: %v", attempt, err)
		}
		if _, err := db.Exec(ctx, `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, job.ID.String()); err != nil {
			t.Fatal(err)
		}
		recovered, err := plain.RecoverExpiredJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !recovered {
			t.Fatalf("attempt %d must be recovered", attempt)
		}
	}
	current, err := plain.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Three claims drove attempt_count to 3, so the third expiry is terminal.
	if current.State != StateFailed || current.ErrorCode != LeaseExpiredErrorCode || current.AttemptCount != 3 {
		t.Fatalf("terminal job mismatch: %+v", current)
	}
	if terminalCalls != 1 {
		t.Fatalf("terminal callback calls = %d, want 1", terminalCalls)
	}
	// A terminal or missing job reports false without error.
	recovered, err := plain.RecoverExpiredJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered {
		t.Fatal("terminal job must not be recovered again")
	}
	recovered, err = plain.RecoverExpiredJob(ctx, library.NewULID())
	if err != nil {
		t.Fatal(err)
	}
	if recovered {
		t.Fatal("missing job must not be recovered")
	}
	if _, err := plain.RecoverExpiredJob(ctx, library.ULID("")); err == nil {
		t.Fatal("zero id must fail validation")
	} else {
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("wrong error type: %v", err)
		}
	}
}

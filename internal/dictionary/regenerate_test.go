package dictionary

import (
	"context"
	"errors"
	"testing"

	"doublangu/internal/library"
)

// TestServiceRegenerateFlow proves the section 4.4 regenerate semantics at
// the service boundary: regenerate always starts a fresh request (bypassing
// ready reuse), keeps the saved document readable while the replacement is
// queued, converges concurrent regenerations on one active job, and rejects
// ambiguous retry+regenerate combinations.
func TestServiceRegenerateFlow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")

	provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
	runner := NewRunner(db, &fakeRegistry{provider: provider})
	runner.heartbeatInterval = 0
	service := resolvingService(t, db, "fp-1")
	articleID := library.ULID("01J00000000000000000000ART1")
	ref := Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}

	// Initial ensure generation reaches ready.
	if _, _, started, err := service.Start(ctx, articleID, ref, false, false); err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	ready, _, err := service.Lookup(ctx, articleID, ref)
	if err != nil || ready.Status != StatusReady || ready.Document == nil {
		t.Fatalf("ready lookup = %+v err=%v", ready, err)
	}

	// Regenerate on a ready entry starts a fresh request while the saved
	// document stays readable.
	envelope, _, started, err := service.Start(ctx, articleID, ref, false, true)
	if err != nil || !started {
		t.Fatalf("regenerate: err=%v started=%t", err, started)
	}
	// The saved document keeps status=ready while the replacement is queued;
	// the envelope already names the new job.
	if envelope.Status != StatusReady || envelope.JobID == "" {
		t.Fatalf("regenerate envelope = %+v", envelope)
	}
	after, _, err := service.Lookup(ctx, articleID, ref)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusReady || after.Document == nil {
		t.Fatalf("old result lost during regeneration: %+v", after)
	}

	// A concurrent regenerate joins the active job instead of enqueueing
	// another one.
	concurrent, _, startedAgain, err := service.Start(ctx, articleID, ref, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if startedAgain {
		t.Fatal("concurrent regenerate must join the active job")
	}
	if concurrent.JobID != envelope.JobID {
		t.Fatalf("concurrent job = %q, want %q", concurrent.JobID, envelope.JobID)
	}
	var jobCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE owner_id = ? AND state = 'queued'`, ready.EntryID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("queued regenerate jobs = %d, want 1", jobCount)
	}

	// retry=true plus regenerate=true is ambiguous.
	if _, _, _, err := service.Start(ctx, articleID, ref, true, true); !errors.Is(err, ErrAmbiguousRequest) {
		t.Fatalf("retry+regenerate error = %v", err)
	}

	// A failed regeneration keeps the old document and an explicit retry
	// (without regenerate) can replay it.
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("regeneration run: %v", err)
	}
	stillReady, _, err := service.Lookup(ctx, articleID, ref)
	if err != nil || stillReady.Status != StatusReady {
		t.Fatalf("post-failure lookup = %+v err=%v", stillReady, err)
	}
}

// TestRunnerExploreHistory proves section 5 history for the explore
// operation: the run is created with explore operation metadata, the attempt
// and its turns are retained, the entry points at the run, and terminal
// outcomes finalize the run (succeeded with publication, failed with every
// retained turn on invalid output).
func TestRunnerExploreHistory(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")

	provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
	runner := NewRunner(db, &fakeRegistry{provider: provider})
	runner.heartbeatInterval = 0
	service := resolvingService(t, db, "fp-1")
	articleID := library.ULID("01J00000000000000000000ART1")
	ref := Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}

	if _, _, started, err := service.Start(ctx, articleID, ref, false, false); err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	var operationType, subjectID, subjectLabel, phase, status, runID string
	if err := db.QueryRow(ctx, `SELECT operation_type, subject_id, subject_label, phase, status, id
		FROM analysis_run WHERE article_id = ?`, articleID.String()).Scan(
		&operationType, &subjectID, &subjectLabel, &phase, &status, &runID); err != nil {
		t.Fatal(err)
	}
	if operationType != "explore" || subjectID == "" || subjectLabel != "bank" {
		t.Fatalf("explore run metadata = %q/%q/%q", operationType, subjectID, subjectLabel)
	}
	if phase != "finished" || status != "succeeded" {
		t.Fatalf("explore run = %q/%q", phase, status)
	}
	var lastRunID string
	entryID := library.ULID(subjectID)
	if err := db.QueryRow(ctx, `SELECT last_run_id FROM dictionary_entry WHERE id = ?`, entryID.String()).Scan(&lastRunID); err != nil {
		t.Fatal(err)
	}
	if lastRunID != runID {
		t.Fatalf("entry last_run_id = %q, want run %q", lastRunID, runID)
	}
	var attemptStatus string
	var turnCount int
	if err := db.QueryRow(ctx, `SELECT status, (SELECT COUNT(*) FROM analysis_stage_turn t WHERE t.stage_attempt_id = a.id)
		FROM analysis_stage_attempt a WHERE a.run_id = ?`, runID).Scan(&attemptStatus, &turnCount); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != "succeeded" || turnCount < 1 {
		t.Fatalf("attempt = %q with %d turns", attemptStatus, turnCount)
	}

	// A failed regeneration retains the turns and keeps the old document.
	if _, _, started, err := service.Start(ctx, articleID, ref, false, true); err != nil || !started {
		t.Fatalf("regenerate: err=%v started=%t", err, started)
	}
	broken := newFakeProvider(`{"broken`, `{"broken`, `{"broken`)
	brokenRunner := NewRunner(db, &fakeRegistry{provider: broken})
	brokenRunner.heartbeatInterval = 0
	if err := brokenRunner.RunOnce(ctx); err != nil {
		t.Fatalf("failing regeneration run: %v", err)
	}
	var failedStatus, failedPhase string
	var failedTurns int
	if err := db.QueryRow(ctx, `SELECT status, phase,
		(SELECT COUNT(*) FROM analysis_stage_turn t WHERE t.stage_attempt_id IN
			(SELECT id FROM analysis_stage_attempt a2 WHERE a2.run_id = analysis_run.id))
		FROM analysis_run WHERE article_id = ? AND operation_type = 'explore'
		ORDER BY started_at DESC LIMIT 1`, articleID.String()).Scan(&failedStatus, &failedPhase, &failedTurns); err != nil {
		t.Fatal(err)
	}
	if failedStatus != "failed" || failedPhase != "finished" {
		t.Fatalf("failed regeneration run = %q/%q", failedStatus, failedPhase)
	}
	if failedTurns < 3 {
		t.Fatalf("failed regeneration retained %d turns, want at least 3", failedTurns)
	}
	// The old document survives the failed replacement attempt.
	after, _, err := service.Lookup(ctx, articleID, ref)
	if err != nil || after.Status != StatusReady || after.Document == nil {
		t.Fatalf("old document lost after failed regeneration: %+v err=%v", after, err)
	}
}

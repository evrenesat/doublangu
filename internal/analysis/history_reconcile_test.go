package analysis

import (
	"context"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

// seedReconcileRun inserts one article, one terminal job, and one analysis
// run pointing at that job in the given status.
func seedReconcileRun(t *testing.T, db *store.DB, ctx context.Context, name, jobID, jobState, runStatus string) library.ULID {
	t.Helper()
	article, err := reader.NewArticle(name, "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticle(ctx, &article); err != nil {
		t.Fatal(err)
	}
	parsedJobID := library.ULID(jobID)
	if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state)
		VALUES (?, 'reader.analysis.v2', 'server', 'article', ?, ?, 'hash-'+?, '{"v":1}', ?)`,
		jobID, article.ID.String(), "idem-"+jobID, jobID, jobState); err != nil {
		t.Fatal(err)
	}
	run, err := NewHistoryStore(db).StartRun(ctx, RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: parsedJobID,
		AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
		PromptVersion: "prompt", RequestedModel: "model", RequestedEffort: "low",
		ProviderID: "provider", TotalParagraphs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != runStatus {
		t.Fatalf("seeded run status = %q, want %q", run.Status, runStatus)
	}
	return run.ID
}

// TestStartRunRecordsOperationMetadataAndPhase proves the operation metadata
// columns and the phase lifecycle: runs start running and finish finished.
func TestStartRunRecordsOperationMetadataAndPhase(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	article, err := reader.NewArticle("Meta", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticle(ctx, &article); err != nil {
		t.Fatal(err)
	}

	// An article run defaults to the article_analysis operation.
	history := NewHistoryStore(db)
	run, err := history.StartRun(ctx, RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: library.NewULID(),
		AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
		PromptVersion: "prompt", RequestedModel: "model", RequestedEffort: "low",
		ProviderID: "provider", TotalParagraphs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var operationType, subjectID, subjectLabel, phase string
	if err := db.QueryRow(ctx, `SELECT operation_type, subject_id, subject_label, phase FROM analysis_run WHERE id = ?`,
		run.ID.String()).Scan(&operationType, &subjectID, &subjectLabel, &phase); err != nil {
		t.Fatal(err)
	}
	if operationType != "article_analysis" || subjectID != "" || subjectLabel != "" || phase != "running" {
		t.Fatalf("legacy run metadata = %q/%q/%q/%q", operationType, subjectID, subjectLabel, phase)
	}

	// On-demand operations name their type and subject explicitly.
	subjectRun, err := history.StartRun(ctx, RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: library.NewULID(),
		AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
		PromptVersion: "prompt", RequestedModel: "model", RequestedEffort: "low",
		ProviderID: "provider", TotalParagraphs: 1,
		OperationType: "explore", SubjectID: "entry-1", SubjectLabel: "huis",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT operation_type, subject_id, subject_label, phase FROM analysis_run WHERE id = ?`,
		subjectRun.ID.String()).Scan(&operationType, &subjectID, &subjectLabel, &phase); err != nil {
		t.Fatal(err)
	}
	if operationType != "explore" || subjectID != "entry-1" || subjectLabel != "huis" {
		t.Fatalf("explore run metadata = %q/%q/%q", operationType, subjectID, subjectLabel)
	}

	// Finishing marks the phase finished.
	if err := history.FinishRun(ctx, subjectRun.ID, RunFinish{Status: "succeeded", DurationMS: 5}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT phase FROM analysis_run WHERE id = ?`, subjectRun.ID.String()).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "finished" {
		t.Fatalf("finished run phase = %q", phase)
	}
}

// TestFinishStageAttemptWritesErrorPhase proves the attempt error_phase is
// validated, stored, and recoverable.
func TestFinishStageAttemptWritesErrorPhase(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	article, err := reader.NewArticle("Phase", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticle(ctx, &article); err != nil {
		t.Fatal(err)
	}
	history := NewHistoryStore(db)
	run, err := history.StartRun(ctx, RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: library.NewULID(),
		AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
		PromptVersion: "prompt", RequestedModel: "model", RequestedEffort: "low",
		ProviderID: "provider", TotalParagraphs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := history.StartStageAttempt(ctx, StageAttempt{
		RunID: run.ID.String(), BlockIndex: 0, StageID: "linguistic_analysis",
		ProviderID: "provider", ModelID: "model-a", ContractVersion: "contract", PromptVersion: "prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.FinishStageAttempt(ctx, attempt.ID, StageAttemptFinish{
		Status: "failed", ErrorCode: "v1.analysis_provider_unavailable", ErrorPhase: "nonsense",
	}); err == nil {
		t.Fatal("invalid error phase accepted")
	}
	if err := history.FinishStageAttempt(ctx, attempt.ID, StageAttemptFinish{
		Status: "failed", ErrorCode: "v1.analysis_provider_unavailable", ErrorPhase: "provider",
		ErrorDetail: "codex app-server socket unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	var errorPhase string
	if err := db.QueryRow(ctx, `SELECT error_phase FROM analysis_stage_attempt WHERE id = ?`, attempt.ID).Scan(&errorPhase); err != nil {
		t.Fatal(err)
	}
	if errorPhase != "provider" {
		t.Fatalf("stored error phase = %q", errorPhase)
	}
}

// TestReconcileTerminalJobRuns proves abandoned runs finalize as interrupted
// with phase finished while partial records survive, running jobs stay
// untouched, and finished runs are never rewritten.
func TestReconcileTerminalJobRuns(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	abandoned := seedReconcileRun(t, db, ctx, "Abandoned", library.NewULID().String(), "failed", "running")
	stillQueued := seedReconcileRun(t, db, ctx, "Queued", library.NewULID().String(), "queued", "running")
	succeeded := seedReconcileRun(t, db, ctx, "Done", library.NewULID().String(), "queued", "running")
	if err := NewHistoryStore(db).FinishRun(ctx, succeeded, RunFinish{Status: "succeeded", DurationMS: 1}); err != nil {
		t.Fatal(err)
	}

	reconciled, err := NewHistoryStore(db).ReconcileTerminalJobRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled %d runs, want 1", reconciled)
	}

	var status, errorCode, phase string
	if err := db.QueryRow(ctx, `SELECT status, error_code, phase FROM analysis_run WHERE id = ?`, abandoned.String()).Scan(&status, &errorCode, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errorCode != "v1.analysis_interrupted" || phase != "finished" {
		t.Fatalf("abandoned run = %q/%q/%q", status, errorCode, phase)
	}
	if err := db.QueryRow(ctx, `SELECT status, phase FROM analysis_run WHERE id = ?`, stillQueued.String()).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "running" || phase != "running" {
		t.Fatalf("queued-job run = %q/%q, want untouched running", status, phase)
	}
	if err := db.QueryRow(ctx, `SELECT status FROM analysis_run WHERE id = ?`, succeeded.String()).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("finished run rewritten to %q", status)
	}

	// Repeated reconciliation is idempotent.
	reconciled, err = NewHistoryStore(db).ReconcileTerminalJobRuns(ctx)
	if err != nil || reconciled != 0 {
		t.Fatalf("second reconciliation = %d err=%v, want 0/nil", reconciled, err)
	}
}

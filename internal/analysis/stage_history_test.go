package analysis

import (
	"context"
	"strings"
	"testing"
	"time"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

func startFixtureRun(t *testing.T, ctx context.Context, history *HistoryStore) Run {
	t.Helper()
	articleID := library.NewULID()
	if _, err := history.db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, 'T', 'nl', 'en', 'draft')`, articleID.String()); err != nil {
		t.Fatal(err)
	}
	run, err := history.StartRun(ctx, RunStart{
		ArticleID: articleID, ArticleTitle: "T", JobID: library.NewULID(),
		AttemptCount: 1, ContentHash: "hash", ContractVersion: "reader.analysis.v3",
		PromptVersion: "reader-analysis-pipeline.v1", RequestedModel: "model-a",
		RequestedEffort: "low", ProviderID: "codex-app-server", TotalParagraphs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStageHistoryAttemptsTurnsAndRecovery(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	history := NewHistoryStore(db)
	run := startFixtureRun(t, ctx, history)

	attempt, err := history.StartStageAttempt(ctx, StageAttempt{
		RunID: run.ID.String(), BlockIndex: 0, StageID: "linguistic_analysis",
		ProviderID: "codex-app-server", ProviderType: "codex_app_server",
		ConfigFingerprint: "fp", ModelID: "model-a", ContractVersion: "reader.linguistic.v1",
		PromptVersion: "reader-linguistic-prompt.v1", InputHash: "input-hash",
		UpstreamHash: "", CacheDisposition: "miss",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.ID == "" {
		t.Fatal("no attempt id")
	}
	// A duplicate attempt for the same run/block/stage must fail.
	if _, err := history.StartStageAttempt(ctx, StageAttempt{
		RunID: run.ID.String(), BlockIndex: 0, StageID: "linguistic_analysis",
		ProviderID: "codex-app-server", ProviderType: "codex_app_server",
		ConfigFingerprint: "fp", ModelID: "model-a", ContractVersion: "reader.linguistic.v1",
		PromptVersion: "reader-linguistic-prompt.v1", InputHash: "other",
	}); err == nil {
		t.Fatal("duplicate stage attempt accepted")
	}
	turn := StageTurn{
		AttemptID: attempt.ID, TurnIndex: 0, TurnKind: "initial",
		Prompt: "analyze", OutputSchema: "{}", CompletedResponse: `{"version":"reader.linguistic.v1"}`,
		ResponseHash: "hash", Status: "completed", CompletionMetadata: "{}",
	}
	if err := history.AppendStageTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	turn.TurnIndex = 1
	turn.TurnKind = "corrective"
	turn.CompletedResponse = ""
	if err := history.AppendStageTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := history.FinishStageAttempt(ctx, attempt.ID, StageAttemptFinish{
		Status: "succeeded", ReportedModel: "model-a", UsageJSON: `{"total_tokens":10}`,
		DurationMS: 12,
	}); err != nil {
		t.Fatal(err)
	}
	// An oversized prompt must be rejected at the retention bound.
	if err := history.AppendStageTurn(ctx, StageTurn{
		AttemptID: attempt.ID, TurnIndex: 2, TurnKind: "corrective",
		Prompt: strings.Repeat("x", stagePromptLimitBytes+1), OutputSchema: "{}",
	}); err == nil || !strings.Contains(err.Error(), "exceeds the retention bound") {
		t.Fatalf("oversized prompt error = %v", err)
	}

	// Interrupted recovery: a second attempt left running becomes failed.
	interrupted, err := history.StartStageAttempt(ctx, StageAttempt{
		RunID: run.ID.String(), BlockIndex: 1, StageID: "translation",
		ProviderID: "mac-omlx", ProviderType: "openai_compatible",
		ConfigFingerprint: "fp2", ModelID: "model-b", ContractVersion: "reader.translation.v1",
		PromptVersion: "reader-translation-prompt.v1", InputHash: "in", UpstreamHash: "up",
		StartedAt: time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.RecoverInterruptedStageAttempts(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := history.ListStageAttempts(ctx, run.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v", attempts)
	}
	var interruptedStatus string
	for _, item := range attempts {
		if item.ID == interrupted.ID {
			interruptedStatus = item.Status
		}
	}
	if interruptedStatus != "failed" {
		t.Fatalf("interrupted attempt status = %q", interruptedStatus)
	}
	var failedCode string
	if err := db.QueryRow(ctx, `SELECT error_code FROM analysis_stage_attempt WHERE id = ?`, interrupted.ID).Scan(&failedCode); err != nil {
		t.Fatal(err)
	}
	if failedCode != "v1.analysis_interrupted" {
		t.Fatalf("interrupted code = %q", failedCode)
	}
}

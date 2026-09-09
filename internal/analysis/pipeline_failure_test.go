package analysis

import (
	"context"
	"errors"
	"testing"

	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

// failingSessionProvider fails every OpenSession call, modeling a provider
// that cannot even start a session (pre-turn failure with zero turns).
type failingSessionProvider struct {
	descriptor annotator.ProviderDescriptor
	openErr    error
}

func (p *failingSessionProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *failingSessionProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return nil, errors.New("not used")
}
func (p *failingSessionProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return nil, p.openErr
}

// TestPipelineRunnerPreTurnFailureRetainsZeroTurnAttempt proves an
// open-session failure records a failed attempt with zero turns and a miss
// disposition — clearly distinguishable from an exact-cache reuse — with the
// provider error phase and stable code retained.
func TestPipelineRunnerPreTurnFailureRetainsZeroTurnAttempt(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	first, err := reader.NewArticle("Pre-turn", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, pipelineProfileForTest(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE job SET max_attempts = 1 WHERE owner_id = ?`, first.ID.String()); err != nil {
		t.Fatal(err)
	}
	broken := &failingSessionProvider{
		descriptor: annotator.ProviderDescriptor{ID: "ling-provider", Type: config.ProviderTypeCodexAppServer, Enabled: true, ConfigFingerprint: "fp"},
		openErr:    errors.New("codex app-server socket unavailable"),
	}
	healthy := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": broken, "tr-provider": healthy,
	}})
	if runErr := runner.RunOnce(ctx); runErr != nil {
		t.Logf("run error = %v", runErr)
	}

	// The attempt row: failed with the provider error phase and zero turns.
	var attemptID, status, errorPhase, cacheDisposition string
	var turnCount int
	if err := db.QueryRow(ctx, `SELECT a.id, a.status, a.error_phase, a.cache_disposition,
		(SELECT COUNT(*) FROM analysis_stage_turn t WHERE t.stage_attempt_id = a.id)
		FROM analysis_stage_attempt a WHERE a.run_id IN (SELECT id FROM analysis_run WHERE article_id = ?)
		AND a.stage_id = 'linguistic_analysis'`, first.ID.String()).Scan(&attemptID, &status, &errorPhase, &cacheDisposition, &turnCount); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if status != "failed" || errorPhase != "provider" {
		t.Fatalf("attempt = %q/%q, want failed/provider", status, errorPhase)
	}
	if turnCount != 0 {
		t.Fatalf("zero-turn attempt recorded %d turns", turnCount)
	}
	// Zero turns plus a miss disposition can never be mistaken for a cache hit.
	if cacheDisposition != "miss" {
		t.Fatalf("cache disposition = %q, want miss", cacheDisposition)
	}
	var errorCode string
	if err := db.QueryRow(ctx, `SELECT error_code FROM job WHERE owner_id = ?`, first.ID.String()).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "v1.analysis_provider_unavailable" {
		t.Fatalf("job error = %q", errorCode)
	}
}

// TestPipelineRunnerRecorderPersistsTurnsDuringExecution proves turn records
// are persisted promptly during execution (not only after the whole stage
// succeeds): a stage that exhausts its corrections still leaves every turn
// row in history.
func TestPipelineRunnerRecorderPersistsTurnsDuringExecution(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	alwaysInvalid := `{"version":"reader.analysis.v3"}`
	first, err := reader.NewArticle("Recorder", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, pipelineProfileForTest(t)); err != nil {
		t.Fatal(err)
	}
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	linguisticProvider.overrideResponses = []string{alwaysInvalid, alwaysInvalid, alwaysInvalid}
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	// The linguistic stage exhausts corrections and fails the run; the
	// translation stage never runs.
	_ = runner.RunOnce(ctx)

	var initialTurns, correctiveTurns int
	if err := db.QueryRow(ctx, `SELECT
		SUM(CASE WHEN turn_kind = 'initial' THEN 1 ELSE 0 END),
		SUM(CASE WHEN turn_kind = 'corrective' THEN 1 ELSE 0 END)
		FROM analysis_stage_turn WHERE stage_attempt_id IN
		(SELECT id FROM analysis_stage_attempt WHERE stage_id = 'linguistic_analysis')`).Scan(&initialTurns, &correctiveTurns); err != nil {
		t.Fatal(err)
	}
	if initialTurns != 1 || correctiveTurns != 2 {
		t.Fatalf("retained turns = %d initial + %d corrective, want 1 + 2", initialTurns, correctiveTurns)
	}
	// Every rejected turn retains its exact validation error in history.
	var emptyValidation int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_turn WHERE stage_attempt_id IN
		(SELECT id FROM analysis_stage_attempt WHERE stage_id = 'linguistic_analysis')
		AND (validation_error IS NULL OR TRIM(validation_error) = '')`).Scan(&emptyValidation); err != nil {
		t.Fatal(err)
	}
	if emptyValidation != 0 {
		t.Fatalf("%d rejected turns have empty validation_error, want all 3 nonempty", emptyValidation)
	}
}

package analysis

import (
	"context"
	"encoding/json"
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
	// An oversized schema is rejected by the same storage invariant.
	if err := history.AppendStageTurn(ctx, StageTurn{
		AttemptID: attempt.ID, TurnIndex: 2, TurnKind: "corrective",
		Prompt: "retry", OutputSchema: strings.Repeat("x", stageSchemaLimitBytes+1),
	}); err == nil || !strings.Contains(err.Error(), "exceeds the retention bound") {
		t.Fatalf("oversized schema error = %v", err)
	}

	// A turn with an oversized provider error persists the excerpt with its
	// flag, and both travel through run detail.
	if err := history.AppendStageTurn(ctx, StageTurn{
		AttemptID: attempt.ID, TurnIndex: 2, TurnKind: "corrective",
		Prompt: "retry", OutputSchema: "{}", CompletedResponse: "",
		ProviderError:      strings.Repeat("e", stageExcerptLimitBytes+10),
		CompletionMetadata: `{"model":"m"}`, Status: "failed",
	}); err != nil {
		t.Fatalf("append flagged turn: %v", err)
	}
	detailRun, err := history.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	var flagged StageTurnSummary
	for _, item := range detailRun.StageAttempts {
		for _, turn := range item.Turns {
			if turn.TurnIndex == 2 {
				flagged = turn
			}
		}
	}
	if len(flagged.ProviderError) != stageExcerptLimitBytes || !flagged.ProviderErrorTruncated {
		t.Fatalf("turn flag round-trip = %+v", flagged)
	}
	if flagged.MetadataTruncated || flagged.ValidationTruncated || flagged.StderrTruncated {
		t.Fatalf("unset turn flags set: %+v", flagged)
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

// TestGetRunReturnsSnapshotAndFailurePair proves a translation failure
// round-trips the exact immutable profile snapshot plus the authoritative
// failed stage/provider through run detail, even with no attempt rows.
func TestGetRunReturnsSnapshotAndFailurePair(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	history := NewHistoryStore(db)
	run := startFixtureRun(t, ctx, history)

	snapshot := `{"id":"profile-1","name":"Mixed","bindings":[` +
		`{"stage_id":"linguistic_analysis","provider_id":"codex-app-server","provider_type":"codex_app_server",` +
		`"provider_config_fingerprint":"fp-a","model_id":"model-a","options":{},"options_hash":"o",` +
		`"contract_version":"reader.linguistic.v1","prompt_version":"reader-linguistic-prompt.v1"},` +
		`{"stage_id":"translation","provider_id":"mac-omlx","provider_type":"openai_compatible",` +
		`"provider_config_fingerprint":"fp-b","model_id":"model-b","options":{},"options_hash":"o",` +
		`"contract_version":"reader.translation.v1","prompt_version":"reader-translation-prompt.v1"}]}`
	if err := history.SetRunPipelineProvenance(ctx, run.ID.String(), snapshot,
		"reader-analysis-pipeline.v1", "profile-1", "Mixed", "snapshot-hash"); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if err := history.SetRunPipelineFailure(ctx, run.ID.String(), "translation", "mac-omlx"); err != nil {
		t.Fatalf("failure: %v", err)
	}

	loaded, err := history.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if loaded.ProfileSnapshot == nil {
		t.Fatal("GetRun dropped the stored profile snapshot")
	}
	if loaded.ProfileSnapshot.ID != "profile-1" || loaded.ProfileSnapshot.Name != "Mixed" {
		t.Fatalf("snapshot identity = %+v", loaded.ProfileSnapshot)
	}
	if len(loaded.ProfileSnapshot.Bindings) != 2 ||
		loaded.ProfileSnapshot.Bindings[1].ProviderID != "mac-omlx" ||
		loaded.ProfileSnapshot.Bindings[1].ProviderConfigFingerprint != "fp-b" {
		t.Fatalf("snapshot bindings = %+v", loaded.ProfileSnapshot.Bindings)
	}
	if loaded.FailedStageID != "translation" || loaded.FailedProviderID != "mac-omlx" {
		t.Fatalf("failure pair = %q/%q", loaded.FailedStageID, loaded.FailedProviderID)
	}
	// The owner API serializes Run directly: the pair must survive JSON.
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"failed_stage_id":"translation"`, `"failed_provider_id":"mac-omlx"`, `"profile_snapshot":{"id":"profile-1"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("run JSON missing %s: %s", want, encoded)
		}
	}
}

// TestFinishStageAttemptBoundsDiagnostics proves every attempt diagnostic
// field obeys its storage bound with an explicit flag: structured usage,
// timing, and metadata stay valid JSON (a bounded sentinel replaces
// oversized values), while stderr and error detail are bounded excerpts.
func TestFinishStageAttemptBoundsDiagnostics(t *testing.T) {
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
		CacheDisposition: "miss",
	})
	if err != nil {
		t.Fatal(err)
	}

	oversizedJSON := `{"padding":"` + strings.Repeat("x", stageMetadataLimitBytes) + `"}`
	if err := history.FinishStageAttempt(ctx, attempt.ID, StageAttemptFinish{
		Status: "failed", ReportedModel: "model-a", UsageJSON: oversizedJSON,
		TimingJSON: oversizedJSON, MetadataJSON: oversizedJSON,
		StderrExcerpt: strings.Repeat("y", stageExcerptLimitBytes+1),
		ErrorCode:     "v1.test", ErrorDetail: strings.Repeat("z", stageExcerptLimitBytes+1),
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	loaded, err := history.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(loaded.StageAttempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(loaded.StageAttempts))
	}
	got := loaded.StageAttempts[0]
	if !got.UsageTruncated || !got.TimingTruncated || !got.MetadataTruncated ||
		!got.StderrTruncated || !got.ErrorDetailTruncated {
		t.Fatalf("truncation flags = %+v, want all true", got)
	}
	for name, value := range map[string]string{
		"usage": got.UsageJSON, "timing": got.TimingJSON, "metadata": got.MetadataJSON,
	} {
		if len(value) > stageMetadataLimitBytes {
			t.Fatalf("%s still %d bytes", name, len(value))
		}
		var sentinel struct {
			Truncated     bool `json:"truncated"`
			OriginalBytes int  `json:"original_bytes"`
		}
		if err := json.Unmarshal([]byte(value), &sentinel); err != nil {
			t.Fatalf("%s invalid JSON after bound: %v", name, err)
		}
		if !sentinel.Truncated || sentinel.OriginalBytes <= stageMetadataLimitBytes {
			t.Fatalf("%s sentinel = %+v", name, sentinel)
		}
	}
	if len(got.ProviderStderrExcerpt) != stageExcerptLimitBytes || len(got.ErrorDetail) != stageExcerptLimitBytes {
		t.Fatalf("excerpts not cut to the bound: %d/%d", len(got.ProviderStderrExcerpt), len(got.ErrorDetail))
	}

	// Values within bounds persist verbatim with clear flags.
	second, err := history.StartStageAttempt(ctx, StageAttempt{
		RunID: run.ID.String(), BlockIndex: 1, StageID: "translation",
		ProviderID: "mac-omlx", ProviderType: "openai_compatible",
		ConfigFingerprint: "fp2", ModelID: "model-b", ContractVersion: "reader.translation.v1",
		PromptVersion: "reader-translation-prompt.v1", InputHash: "in", UpstreamHash: "up",
		CacheDisposition: "miss",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.FinishStageAttempt(ctx, second.ID, StageAttemptFinish{
		Status: "succeeded", UsageJSON: `{"total_tokens":3}`, TimingJSON: `{"ms":4}`,
		MetadataJSON: `{"model":"m"}`, StderrExcerpt: "warn", ErrorDetail: "",
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	loaded, err = history.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	var clean StageAttemptSummary
	for _, item := range loaded.StageAttempts {
		if item.ID == second.ID {
			clean = item
		}
	}
	if clean.UsageJSON != `{"total_tokens":3}` || clean.MetadataJSON != `{"model":"m"}` {
		t.Fatalf("in-bound values altered: %+v", clean)
	}
	if clean.UsageTruncated || clean.TimingTruncated || clean.MetadataTruncated ||
		clean.StderrTruncated || clean.ErrorDetailTruncated {
		t.Fatalf("in-bound flags set: %+v", clean)
	}
}

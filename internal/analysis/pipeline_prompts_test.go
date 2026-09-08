package analysis

import (
	"context"
	"strings"
	"testing"

	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

// promptSnapshotForTest captures one instruction as an immutable execution
// snapshot with the exact hash a resolver capture would store.
func promptSnapshotForTest(t *testing.T, promptType prompts.PromptType, instruction string) pipeline.PromptSnapshot {
	t.Helper()
	return pipeline.PromptSnapshot{
		Type: string(promptType), ID: prompts.NewVersionID(), Version: 1,
		ContentHash: prompts.ContentHashOf(instruction), InstructionText: instruction,
		EnvelopeVersion: pipeline.PromptCapturedEnvelopeVersion,
	}
}

// snapshotWithArticlePrompts clones a profile fixture and attaches the three
// article prompt snapshots exactly like the enqueue resolver would.
func snapshotWithArticlePrompts(t *testing.T, base *pipeline.ProfileSnapshot, linguistic, translation, correction string) *pipeline.ProfileSnapshot {
	t.Helper()
	next := *base
	next.PromptSnapshots = []pipeline.PromptSnapshot{
		promptSnapshotForTest(t, prompts.TypeLinguisticAnalysis, linguistic),
		promptSnapshotForTest(t, prompts.TypeArticleTranslation, translation),
		promptSnapshotForTest(t, prompts.TypeCorrection, correction),
	}
	return &next
}

// runQueuedArticle processes one queued article job with the shared fake
// providers and asserts the article finished ready.
func runQueuedArticle(t *testing.T, ctx context.Context, runner *PipelineRunner, article *reader.Article) {
	t.Helper()
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	final, err := runner.reader.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.AnalysisStatus != reader.AnalysisReady {
		t.Fatalf("article status = %q error %q", final.AnalysisStatus, final.AnalysisErrorCode)
	}
}

// TestPipelineRunnerExecutesCapturedPromptSnapshots proves a job queued with
// captured prompt snapshots runs those exact instruction texts through both
// stages and corrective turns, and that editing the stored prompt versions
// afterwards (the queued-edit/restart hazard) never changes the captured
// bytes a claimed job executes.
func TestPipelineRunnerExecutesCapturedPromptSnapshots(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	linguisticInstruction := "Custom linguistic instruction v3.\n"
	translationInstruction := "Custom translation instruction v7.\n"
	correctionInstruction := "Custom correction instruction v2.\n"
	snapshot := snapshotWithArticlePrompts(t, pipelineProfileForTest(t),
		linguisticInstruction, translationInstruction, correctionInstruction)

	first, err := reader.NewArticle("Instructies", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, snapshot); err != nil {
		t.Fatal(err)
	}
	second, err := reader.NewArticle("Instructies", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &second, snapshot); err != nil {
		t.Fatal(err)
	}

	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})

	// The owner edits the stored prompt versions after queueing: a claimed
	// job must keep running its captured bytes, never the newer text.
	if _, err := db.Exec(ctx, `UPDATE prompt_version SET instruction_text = 'Drifted instruction'`); err != nil {
		t.Fatal(err)
	}

	runQueuedArticle(t, ctx, runner, &first)
	runQueuedArticle(t, ctx, runner, &second)

	for _, probe := range []struct {
		provider  *fakeStageProvider
		requested string
	}{
		{linguisticProvider, linguisticInstruction},
		{translationProvider, translationInstruction},
	} {
		promptsSeen := probe.provider.Prompts()
		if len(promptsSeen) == 0 {
			t.Fatalf("provider %q received no prompts", probe.provider.descriptor.ID)
		}
		if !strings.HasPrefix(promptsSeen[0], probe.requested) {
			t.Fatalf("provider %q ran text %q..., want the captured %q... instruction", probe.provider.descriptor.ID, promptsSeen[0][:40], probe.requested)
		}
		if strings.Contains(promptsSeen[0], "Drifted instruction") {
			t.Fatalf("provider %q ran a drifted instruction", probe.provider.descriptor.ID)
		}
	}
}

// TestPipelineRunnerPromptIdentitySeparatesStageCaches proves the effective
// prompt identity is operation-specific: changing only the article_translation
// instruction invalidates translation cache eligibility while the linguistic
// stage keeps hitting, and legacy payloads keep the builtin prompt constant.
func TestPipelineRunnerPromptIdentitySeparatesStageCaches(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	linguisticInstruction := prompts.DefaultInstruction(prompts.TypeLinguisticAnalysis) + "\n"
	translationInstruction := prompts.DefaultInstruction(prompts.TypeArticleTranslation) + "\n"
	changedTranslationInstruction := prompts.DefaultInstruction(prompts.TypeArticleTranslation) + "\nExperiment v2.\n"
	correctionInstruction := prompts.DefaultInstruction(prompts.TypeCorrection)

	first, err := reader.NewArticle("Caches", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first,
		snapshotWithArticlePrompts(t, pipelineProfileForTest(t), linguisticInstruction, translationInstruction, correctionInstruction)); err != nil {
		t.Fatal(err)
	}
	second, err := reader.NewArticle("Caches", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &second,
		snapshotWithArticlePrompts(t, pipelineProfileForTest(t), linguisticInstruction, changedTranslationInstruction, correctionInstruction)); err != nil {
		t.Fatal(err)
	}

	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	runQueuedArticle(t, ctx, runner, &first)
	runQueuedArticle(t, ctx, runner, &second)

	// The translation-only change ran the translation stage for both blocks
	// of the second article while its linguistic stage hit the cache for
	// both blocks (the linguistic identity is unchanged).
	if got := linguisticProvider.TurnCount(); got != 2 {
		t.Fatalf("linguistic provider turns = %d, want 2 (second article must hit the cache)", got)
	}
	if got := translationProvider.TurnCount(); got != 4 {
		t.Fatalf("translation provider turns = %d, want 4 (changed prompt is a miss for both blocks)", got)
	}
	if got := strings.Count(strings.Join(translationProvider.Prompts(), ""), "Experiment v2."); got != 2 {
		t.Fatalf("changed translation instruction ran %d times, want 2", got)
	}

	// Cache rows carry the effective identity, distinct from the legacy
	// builtin constants and derived from the captured content hashes.
	translationIdentity := pipeline.EffectivePromptVersion("translation",
		prompts.ContentHashOf(translationInstruction), prompts.ContentHashOf(correctionInstruction), pipeline.PromptCapturedEnvelopeVersion)
	linguisticIdentity := pipeline.EffectivePromptVersion("linguistic_analysis",
		prompts.ContentHashOf(linguisticInstruction), prompts.ContentHashOf(correctionInstruction), pipeline.PromptCapturedEnvelopeVersion)
	if translationIdentity == linguisticIdentity {
		t.Fatal("stage identities collide across operations")
	}
	changedTranslationIdentity := pipeline.EffectivePromptVersion("translation",
		prompts.ContentHashOf(changedTranslationInstruction), prompts.ContentHashOf(correctionInstruction), pipeline.PromptCapturedEnvelopeVersion)
	var identities int
	if err := db.QueryRow(ctx, `SELECT COUNT(DISTINCT prompt_version) FROM analysis_stage_cache WHERE stage_id = 'translation'`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 2 {
		t.Fatalf("translation cache identities = %d, want 2 (one per captured translation instruction)", identities)
	}
	var legacyRows int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_cache WHERE stage_id = 'translation' AND prompt_version = ?`,
		pipeline.TranslationPromptVersion).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	if legacyRows != 0 {
		t.Fatal("translation cache rows kept the legacy builtin constant under captured prompts")
	}
	var withExpected int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_cache WHERE stage_id = 'translation' AND prompt_version IN (?, ?)`,
		translationIdentity, changedTranslationIdentity).Scan(&withExpected); err != nil {
		t.Fatal(err)
	}
	if withExpected != 4 {
		t.Fatalf("expected effective identities stored, found %d rows matching (two blocks per article)", withExpected)
	}
	// The linguistic stage rows all carry the one unchanged linguistic
	// identity: the translation-only change never touched them.
	var linguisticIdentities int
	if err := db.QueryRow(ctx, `SELECT COUNT(DISTINCT prompt_version) FROM analysis_stage_cache WHERE stage_id = 'linguistic_analysis'`).Scan(&linguisticIdentities); err != nil {
		t.Fatal(err)
	}
	if linguisticIdentities != 1 {
		t.Fatalf("linguistic cache identities = %d, want 1", linguisticIdentities)
	}
	var matchesLinguisticIdentity int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_cache WHERE stage_id = 'linguistic_analysis' AND prompt_version = ?`,
		linguisticIdentity).Scan(&matchesLinguisticIdentity); err != nil {
		t.Fatal(err)
	}
	if matchesLinguisticIdentity != 2 {
		t.Fatalf("linguistic identity rows = %d, want 2 (one per block)", matchesLinguisticIdentity)
	}
}

// TestPipelineRunnerLegacyPayloadRunsBuiltinInstructions proves payloads
// without captured snapshots keep their recognized legacy behavior: builtin
// instructions run and cache rows store the legacy builtin prompt constants.
func TestPipelineRunnerLegacyPayloadRunsBuiltinInstructions(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	first, err := reader.NewArticle("Legacy", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, pipelineProfileForTest(t)); err != nil {
		t.Fatal(err)
	}
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	runQueuedArticle(t, ctx, runner, &first)

	linguisticPrompts := linguisticProvider.Prompts()
	if len(linguisticPrompts) == 0 || !strings.HasPrefix(linguisticPrompts[0], prompts.DefaultInstruction(prompts.TypeLinguisticAnalysis)) {
		t.Fatal("legacy payload did not run the builtin linguistic instruction")
	}
	translationPrompts := translationProvider.Prompts()
	if len(translationPrompts) == 0 || !strings.HasPrefix(translationPrompts[0], prompts.DefaultInstruction(prompts.TypeArticleTranslation)) {
		t.Fatal("legacy payload did not run the builtin translation instruction")
	}
	var promptVersion string
	if err := db.QueryRow(ctx, `SELECT prompt_version FROM analysis_stage_cache WHERE stage_id = 'linguistic_analysis'`).Scan(&promptVersion); err != nil {
		t.Fatal(err)
	}
	if promptVersion != pipeline.LinguisticPromptVersion {
		t.Fatalf("legacy cache prompt_version = %q, want the builtin constant", promptVersion)
	}
}

// TestPipelineRunnerRejectsTamperedPromptSnapshots proves fail-closed hash
// verification: captured instruction bytes that no longer match their content
// hash fail the job with a visible prompt error before article state changes.
func TestPipelineRunnerRejectsTamperedPromptSnapshots(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	base := *pipelineProfileForTest(t)
	tampered := pipeline.PromptSnapshot{
		Type: string(prompts.TypeLinguisticAnalysis), ID: prompts.NewVersionID(), Version: 1,
		ContentHash: strings.Repeat("b", 64), InstructionText: "Tampered instruction",
		EnvelopeVersion: pipeline.PromptCapturedEnvelopeVersion,
	}
	base.PromptSnapshots = append([]pipeline.PromptSnapshot(nil), tampered)
	base.PromptSnapshots = append(base.PromptSnapshots,
		promptSnapshotForTest(t, prompts.TypeArticleTranslation, "translation"),
		promptSnapshotForTest(t, prompts.TypeCorrection, "correction"))

	first, err := reader.NewArticle("Tampered", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, &base); err != nil {
		t.Fatal(err)
	}
	// Make the prompt preflight failure terminal so the article transitions.
	if _, err := db.Exec(ctx, `UPDATE job SET max_attempts = 1 WHERE owner_id = ?`, first.ID.String()); err != nil {
		t.Fatal(err)
	}
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	if err := runner.RunOnce(ctx); err == nil || !strings.Contains(err.Error(), "v1.analysis_prompt_invalid") {
		t.Fatalf("run error = %v, want the visible prompt-contract error", err)
	}
	if linguisticProvider.TurnCount() != 0 || translationProvider.TurnCount() != 0 {
		t.Fatal("provider was invoked despite a tampered prompt snapshot")
	}
	var errorCode string
	if err := db.QueryRow(ctx, `SELECT error_code FROM job WHERE owner_id = ?`, first.ID.String()).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "v1.analysis_prompt_invalid" {
		t.Fatalf("job error code = %q, want v1.analysis_prompt_invalid", errorCode)
	}
	final, err := runner.reader.GetArticle(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.AnalysisStatus != reader.AnalysisFailed || final.AnalysisErrorCode != "v1.analysis_prompt_invalid" {
		t.Fatalf("tampered article = %q/%q, want failed with the prompt error", final.AnalysisStatus, final.AnalysisErrorCode)
	}
}

// TestPipelineRunnerRejectsUnsupportedEnvelopeVersions proves versioned
// captured envelopes are enforced: a snapshot stamped with anything but the
// captured envelope version fails closed with the visible prompt error
// instead of silently rendering under different boundary semantics.
func TestPipelineRunnerRejectsUnsupportedEnvelopeVersions(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)

	snapshot := snapshotWithArticlePrompts(t, pipelineProfileForTest(t), "gen", "translation", "correction")
	for index := range snapshot.PromptSnapshots {
		snapshot.PromptSnapshots[index].EnvelopeVersion = pipeline.PromptEnvelopeVersion
	}

	first, err := reader.NewArticle("Old envelope", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE job SET max_attempts = 1 WHERE owner_id = ?`, first.ID.String()); err != nil {
		t.Fatal(err)
	}
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	if err := runner.RunOnce(ctx); err == nil || !strings.Contains(err.Error(), "v1.analysis_prompt_invalid") {
		t.Fatalf("run error = %v, want the visible prompt-contract error", err)
	}
	if linguisticProvider.TurnCount() != 0 || translationProvider.TurnCount() != 0 {
		t.Fatal("provider was invoked despite an unsupported envelope version")
	}
	final, err := runner.reader.GetArticle(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.AnalysisStatus != reader.AnalysisFailed || final.AnalysisErrorCode != "v1.analysis_prompt_invalid" {
		t.Fatalf("article = %q/%q, want failed with the prompt error", final.AnalysisStatus, final.AnalysisErrorCode)
	}
}

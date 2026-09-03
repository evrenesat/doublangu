package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"doublangu/internal/library"

	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/pipeline"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

// fakeStageProvider answers each stage turn from the exact token enum in the
// request schema with an always-valid unchanged artifact.
type fakeStageProvider struct {
	descriptor  annotator.ProviderDescriptor
	mu          sync.Mutex
	turns       int
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	blockOnCall int
	blocked     bool
	// turnDelay sleeps before every successful completion so tests can
	// observe nonzero attempt durations deterministically.
	turnDelay time.Duration
}

func newFakeStageProvider(id, providerType string, blockOnCall int) *fakeStageProvider {
	return &fakeStageProvider{
		descriptor: annotator.ProviderDescriptor{
			ID: id, Type: providerType, Enabled: true, ConfigFingerprint: "fp",
		},
		entered: make(chan struct{}), release: make(chan struct{}),
		blockOnCall: blockOnCall, blocked: blockOnCall >= 0,
	}
}

func (p *fakeStageProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }

func (p *fakeStageProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return nil, errors.New("not used")
}

func (p *fakeStageProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return &fakeStageSession{provider: p}, nil
}

func (p *fakeStageProvider) Unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *fakeStageProvider) TurnCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turns
}

type fakeStageSession struct {
	provider *fakeStageProvider
}

// artifactFromSchema builds a valid stage artifact covering every token id in
// the request schema, using unchanged classifications and empty translations.
func artifactFromSchema(schema []byte, translation bool) (string, error) {
	var object map[string]any
	if err := json.Unmarshal(schema, &object); err != nil {
		return "", err
	}
	properties := object["properties"].(map[string]any)
	tokens := properties["tokens"].(map[string]any)
	items := tokens["items"].(map[string]any)
	tokenProperties := items["properties"].(map[string]any)
	enums := tokenProperties["token_id"].(map[string]any)["enum"].([]any)
	if !translation {
		artifact := map[string]any{
			"version":    pipeline.LinguisticContractVersion,
			"tokens":     make([]map[string]any, 0, len(enums)),
			"new_senses": []any{}, "constructions": []any{},
		}
		for _, id := range enums {
			artifact["tokens"] = append(artifact["tokens"].([]map[string]any), map[string]any{
				"token_id": id, "classification": "unchanged", "kind": "word",
				"semantic_sense_id": "", "new_sense_ref": "",
				"canonical_pronunciation_text": "", "context_pronunciation_key": "",
				"confidence_milli": 1000,
			})
		}
		return string(mustMarshal(artifact)), nil
	}
	artifact := map[string]any{
		"version":    pipeline.TranslationContractVersion,
		"tokens":     make([]map[string]any, 0, len(enums)),
		"new_senses": []any{}, "constructions": []any{},
	}
	for _, id := range enums {
		artifact["tokens"] = append(artifact["tokens"].([]map[string]any), map[string]any{
			"token_id": id, "shadow_text": "",
		})
	}
	return string(mustMarshal(artifact)), nil
}

func mustMarshal(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (s *fakeStageSession) Turn(_ context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	s.provider.mu.Lock()
	index := s.provider.turns
	s.provider.turns++
	blocked := s.provider.blocked && index == s.provider.blockOnCall
	s.provider.mu.Unlock()
	if blocked {
		select {
		case <-s.provider.entered:
		default:
			close(s.provider.entered)
		}
		<-s.provider.release
	}
	translation := request.StageID == pipeline.StageTranslation
	text, err := artifactFromSchema(request.OutputSchema, translation)
	if err != nil {
		return annotator.Completion{}, err
	}
	if s.provider.turnDelay > 0 {
		time.Sleep(s.provider.turnDelay)
	}
	return annotator.Completion{
		Text: text, ReportedModel: "fake-model",
		UsageJSON: `{"total_tokens":5}`,
	}, nil
}

func (s *fakeStageSession) Close() error { return nil }

type fakePipelineRegistry struct {
	providers map[string]annotator.Provider
}

func (r *fakePipelineRegistry) Provider(id string) (annotator.Provider, bool) {
	provider, ok := r.providers[id]
	return provider, ok
}

func pipelineProfileForTest(t *testing.T) *pipeline.ProfileSnapshot {
	t.Helper()
	codexOptions, err := config.CanonicalizeProviderOptions(config.ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	omlxOptions, err := config.CanonicalizeProviderOptions(config.ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0,"max_output_tokens":16384}`))
	if err != nil {
		t.Fatal(err)
	}
	bindings := make([]pipeline.BindingSnapshot, 0, 2)
	for _, item := range []struct {
		stage    pipeline.StageID
		provider string
		options  json.RawMessage
	}{
		{pipeline.StageLinguisticAnalysis, "ling-provider", codexOptions},
		{pipeline.StageTranslation, "tr-provider", omlxOptions},
	} {
		contract, prompt, _ := pipeline.StageContracts(item.stage)
		optionsHash, err := pipeline.OptionsHashOf(item.options)
		if err != nil {
			t.Fatal(err)
		}
		providerType := config.ProviderTypeCodexAppServer
		if item.stage == pipeline.StageTranslation {
			providerType = config.ProviderTypeOpenAICompatible
		}
		bindings = append(bindings, pipeline.BindingSnapshot{
			StageID: item.stage, ProviderID: item.provider, ProviderType: providerType,
			ProviderConfigFingerprint: "fp", ModelID: "model-a", Options: item.options,
			OptionsHash: optionsHash, ContractVersion: contract, PromptVersion: prompt,
		})
	}
	snapshot := &pipeline.ProfileSnapshot{ID: "profile-run", Name: "Pipeline Test", Bindings: bindings}
	if _, err := snapshot.SnapshotHash(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestPipelineRunnerPublishesParagraphOnlyAfterBothStages proves the gated
// two-stage acceptance: block zero's linguistic stage succeeds while its
// translation stage is blocked, nothing publishes, and the paragraph becomes
// visible only after the translation provider is released.
func TestPipelineRunnerPublishesParagraphOnlyAfterBothStages(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	snapshot := pipelineProfileForTest(t)
	article, err := reader.NewArticle("Twee Fasen", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	if err := articles.CreateArticleQueuedWithProfile(ctx, &article, snapshot); err != nil {
		t.Fatal(err)
	}
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, 0)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	done := make(chan error, 1)
	go func() { done <- runner.RunOnce(ctx) }()
	select {
	case <-translationProvider.entered:
	case err := <-done:
		t.Fatalf("runner finished before the translation gate: %v", err)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisProcessing {
		t.Fatalf("article status while gated = %q", loaded.AnalysisStatus)
	}
	if len(loaded.Blocks[0].Occurrences) != 0 {
		t.Fatalf("paragraph 1 published before translation finished")
	}
	var linguisticSuccess, translationRunning int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_attempt WHERE stage_id = 'linguistic_analysis' AND status = 'succeeded'`).Scan(&linguisticSuccess); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_attempt WHERE stage_id = 'translation' AND status = 'running'`).Scan(&translationRunning); err != nil {
		t.Fatal(err)
	}
	if linguisticSuccess != 1 || translationRunning != 1 {
		t.Fatalf("stage states while gated = ling %d / tr %d", linguisticSuccess, translationRunning)
	}
	translationProvider.Unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pipeline run failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline run did not finish")
	}
	loaded, err = articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisProgress.CompletedParagraphs != 2 {
		t.Fatalf("final state = %+v / %+v", loaded.AnalysisStatus, loaded.AnalysisProgress)
	}
	for index, block := range loaded.Blocks {
		if block.AnalysisStatus != reader.BlockReady || len(block.Occurrences) == 0 {
			t.Fatalf("block %d not published: %+v", index, block)
		}
	}
	var attempts, cacheRows, turns int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_attempt WHERE run_id = (SELECT MAX(id) FROM analysis_run WHERE article_id = ?)`, article.ID.String()).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_cache`).Scan(&cacheRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_turn`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if attempts != 4 || cacheRows != 4 || turns != 4 {
		t.Fatalf("attempts/cache/turns = %d/%d/%d (one row per stage per paragraph)", attempts, cacheRows, turns)
	}
	var failedStage string
	if err := db.QueryRow(ctx, `SELECT failed_stage_id FROM analysis_run WHERE article_id = ?`, article.ID.String()).Scan(&failedStage); err != nil {
		t.Fatal(err)
	}
	if failedStage != "" {
		t.Fatalf("failed_stage_id = %q", failedStage)
	}
	if linguisticProvider.TurnCount() != 2 || translationProvider.TurnCount() != 2 {
		t.Fatalf("provider turns = %d/%d", linguisticProvider.TurnCount(), translationProvider.TurnCount())
	}
}

func queuedPipelineArticle(t *testing.T, db *store.DB) (*reader.Store, reader.Article) {
	t.Helper()
	ctx := context.Background()
	snapshot := pipelineProfileForTest(t)
	article, err := reader.NewArticle("Twee Fasen", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	if err := articles.CreateArticleQueuedWithProfile(ctx, &article, snapshot); err != nil {
		t.Fatal(err)
	}
	return articles, article
}

func leaseExpiry(t *testing.T, db *store.DB, jobID string) time.Time {
	t.Helper()
	var raw string
	if err := db.QueryRow(context.Background(), `SELECT lease_expires_at FROM job WHERE id = ?`, jobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

// TestPipelineRunnerHeartbeatKeepsLeaseLive proves the run renews its job
// lease while a provider call is still blocked, so a long stage cannot be
// reclaimed by the scheduler mid-turn.
func TestPipelineRunnerHeartbeatKeepsLeaseLive(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles, article := queuedPipelineArticle(t, db)
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, 0)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	runner.heartbeatInterval = 40 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- runner.RunOnce(context.Background()) }()
	select {
	case <-translationProvider.entered:
	case err := <-done:
		t.Fatalf("runner finished before the gate: %v", err)
	}
	before := leaseExpiry(t, db, article.AnalysisJobID)
	time.Sleep(160 * time.Millisecond)
	after := leaseExpiry(t, db, article.AnalysisJobID)
	if !after.After(before) {
		t.Fatalf("lease did not renew during the blocked provider call: %v -> %v", before, after)
	}
	translationProvider.Unblock()
	if err := <-done; err != nil {
		t.Fatalf("pipeline run failed: %v", err)
	}
	loaded, err := articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisReady {
		t.Fatalf("final status = %q", loaded.AnalysisStatus)
	}
}

// TestPipelineRunnerLeaseLossSkipsArticleFailure proves an expired lease
// during a provider call aborts the run without marking the article failed or
// publishing paragraphs: the article state belongs to the newer run.
func TestPipelineRunnerLeaseLossSkipsArticleFailure(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles, article := queuedPipelineArticle(t, db)
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, 0)
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	runner.heartbeatInterval = time.Hour // do not fight the manual expiry
	done := make(chan error, 1)
	go func() { done <- runner.RunOnce(context.Background()) }()
	select {
	case <-translationProvider.entered:
	case err := <-done:
		t.Fatalf("runner finished before the gate: %v", err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, article.AnalysisJobID); err != nil {
		t.Fatal(err)
	}
	translationProvider.Unblock()
	runErr := <-done
	if runErr == nil || !strings.Contains(runErr.Error(), "v1.analysis_lease_lost") {
		t.Fatalf("run error = %v, want a lease-lost error", runErr)
	}
	loaded, err := articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus == reader.AnalysisFailed {
		t.Fatal("superseded run marked the article failed")
	}
	if loaded.AnalysisErrorCode != "" {
		t.Fatalf("article error code = %q", loaded.AnalysisErrorCode)
	}
	for _, block := range loaded.Blocks {
		if len(block.Occurrences) != 0 {
			t.Fatalf("block %d published after lease loss", block.BlockIndex)
		}
	}
}

// cancelAwareStageProvider blocks the translation turn until the session
// context is canceled, mirroring how the real transports abort an in-flight
// request when the runner's per-run context is canceled.
type cancelAwareStageProvider struct {
	descriptor  annotator.ProviderDescriptor
	entered     chan struct{}
	enteredOnce sync.Once
	mu          sync.Mutex
	turns       int
}

func (p *cancelAwareStageProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *cancelAwareStageProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return nil, errors.New("not used")
}
func (p *cancelAwareStageProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return &cancelAwareSession{provider: p}, nil
}

type cancelAwareSession struct {
	provider *cancelAwareStageProvider
}

func (s *cancelAwareSession) Turn(ctx context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	s.provider.mu.Lock()
	s.provider.turns++
	s.provider.mu.Unlock()
	if request.StageID == pipeline.StageTranslation {
		s.provider.enteredOnce.Do(func() { close(s.provider.entered) })
		select {
		case <-ctx.Done():
			return annotator.Completion{}, ctx.Err()
		}
	}
	text, err := artifactFromSchema(request.OutputSchema, false)
	if err != nil {
		return annotator.Completion{}, err
	}
	return annotator.Completion{Text: text, ReportedModel: "fake-model"}, nil
}

func (s *cancelAwareSession) Close() error { return nil }

// TestPipelineRunnerCancelAbortsBlockedProviderCall proves owner cancellation
// observed by the heartbeat cancels the per-run context, aborting an in-flight
// provider call immediately instead of occupying the sole runner until its
// request timeout, and that the reclaimed worker writes no turns, attempt
// success/failure, cache, or article state afterwards.
func TestPipelineRunnerCancelAbortsBlockedProviderCall(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles, article := queuedPipelineArticle(t, db)
	translationProvider := &cancelAwareStageProvider{
		descriptor: annotator.ProviderDescriptor{
			ID: "tr-provider", Type: config.ProviderTypeOpenAICompatible, Enabled: true, ConfigFingerprint: "fp",
		},
		entered: make(chan struct{}),
	}
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1),
		"tr-provider":   translationProvider,
	}})
	runner.heartbeatInterval = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- runner.RunOnce(ctx) }()
	select {
	case <-translationProvider.entered:
	case err := <-done:
		t.Fatalf("runner finished before the gate: %v", err)
	}
	// Owner cancellation (fresh run supersede or manual cancel): the job moves
	// to canceled while this worker's lease credential is still present.
	if _, err := db.Exec(ctx, `UPDATE job SET state = 'canceled' WHERE id = ?`, article.AnalysisJobID); err != nil {
		t.Fatal(err)
	}
	select {
	case runErr := <-done:
		if runErr == nil || !strings.Contains(runErr.Error(), "v1.analysis_lease_lost") {
			t.Fatalf("run error = %v, want a lease-lost error", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked provider call was not aborted by cancellation")
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range loaded.Blocks {
		if len(block.Occurrences) != 0 {
			t.Fatalf("block %d published after cancellation", block.BlockIndex)
		}
	}
	// The stale worker wrote no attempt/turn state for the in-flight stage:
	// the translation attempt remains running with no turns and no failure
	// code (startup recovery marks interrupted attempts later), and the
	// linguistic attempt was already committed before the cancellation.
	var runID string
	if err := db.QueryRow(ctx, `SELECT id FROM analysis_run WHERE article_id = ? ORDER BY started_at DESC LIMIT 1`, article.ID.String()).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, `SELECT stage_id, status, error_code FROM analysis_stage_attempt WHERE run_id = ? ORDER BY block_index, stage_id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	byStage := map[string]struct{ status, code string }{}
	for rows.Next() {
		var stage, status, code string
		if err := rows.Scan(&stage, &status, &code); err != nil {
			t.Fatal(err)
		}
		byStage[stage] = struct{ status, code string }{status, code}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	translation := byStage[string(pipeline.StageTranslation)]
	if translation.status != "running" || translation.code != "" {
		t.Fatalf("translation attempt = %+v, want running with no failure code", translation)
	}
	linguistic := byStage[string(pipeline.StageLinguisticAnalysis)]
	if linguistic.status != "succeeded" {
		t.Fatalf("linguistic attempt = %+v, want succeeded", linguistic)
	}
}

// failingTurnProvider returns a provider error on every turn so the executor
// reports a provider-phase failure with its accumulated turn artifacts.
type failingTurnProvider struct {
	descriptor annotator.ProviderDescriptor
}

func (p *failingTurnProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *failingTurnProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return nil, nil
}
func (p *failingTurnProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return &failingTurnSession{}, nil
}

type failingTurnSession struct{}

func (s *failingTurnSession) Turn(context.Context, annotator.TurnRequest) (annotator.Completion, error) {
	return annotator.Completion{}, errors.New("provider offline during conformance turn")
}
func (s *failingTurnSession) Close() error { return nil }

// TestPipelineRunnerFailureRecordsTurnsAndProviderCode proves a provider
// failure is exposed to the article as v1.analysis_provider_unavailable
// (finding 3) while the failed turn still reaches analysis_stage_turn through
// the run detail (finding 2).
func TestPipelineRunnerFailureRecordsTurnsAndProviderCode(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles, article := queuedPipelineArticle(t, db)
	descriptor := annotator.ProviderDescriptor{
		ID: "ling-provider", Type: "codex_app_server", Enabled: true, ConfigFingerprint: "fp",
	}
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": &failingTurnProvider{descriptor: descriptor},
		"tr-provider":   newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1),
	}})
	if runErr := runner.RunOnce(ctx); runErr == nil {
		t.Fatal("run unexpectedly succeeded")
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisFailed || loaded.AnalysisErrorCode != "v1.analysis_provider_unavailable" {
		t.Fatalf("article state = %q / %q", loaded.AnalysisStatus, loaded.AnalysisErrorCode)
	}
	var runID string
	if err := db.QueryRow(ctx, `SELECT id FROM analysis_run WHERE article_id = ? ORDER BY started_at DESC LIMIT 1`, article.ID.String()).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	run, err := NewHistoryStore(db).GetRun(ctx, libraryULIDOf(t, runID))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.StageAttempts) != 1 || run.StageAttempts[0].Status != "failed" {
		t.Fatalf("attempts = %+v", run.StageAttempts)
	}
	attempt := run.StageAttempts[0]
	if attempt.ErrorCode == "" || len(attempt.Turns) != 1 || attempt.Turns[0].Status != "failed" || attempt.Turns[0].ProviderError == "" {
		t.Fatalf("failed attempt turns not retained: %+v", attempt)
	}
	if attempt.Turns[0].Prompt == "" || attempt.Turns[0].OutputSchema == "" || attempt.Turns[0].StartedAt == "" {
		t.Fatalf("failed turn artifacts incomplete: %+v", attempt.Turns[0])
	}
	if len(run.Turns) != 0 {
		t.Fatalf("pipeline run leaked legacy turns: %d", len(run.Turns))
	}
}

func libraryULIDOf(t *testing.T, value string) library.ULID {
	t.Helper()
	id, err := library.ParseULID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestPipelineRunnerPersistsStageProvenanceAndCacheIdentity proves every
// stage attempt records its binding identity (options, options hash, requested
// model), its completion provenance (reported model, usage, duration), and the
// exact cache disposition. Provider runs are 'miss' with no source row; a
// later run over identical content is a 'hit' carrying the source cache row,
// records no turns, and never invokes the provider.
func TestPipelineRunnerPersistsStageProvenanceAndCacheIdentity(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := reader.NewStore(db)
	snapshot := pipelineProfileForTest(t)
	first, err := reader.NewArticle("Twee Fasen", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &first, snapshot); err != nil {
		t.Fatal(err)
	}
	second, err := reader.NewArticle("Twee Fasen", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &second, snapshot); err != nil {
		t.Fatal(err)
	}
	linguisticProvider := newFakeStageProvider("ling-provider", config.ProviderTypeCodexAppServer, -1)
	translationProvider := newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1)
	linguisticProvider.turnDelay = 5 * time.Millisecond
	translationProvider.turnDelay = 5 * time.Millisecond
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		"ling-provider": linguisticProvider, "tr-provider": translationProvider,
	}})
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := linguisticProvider.TurnCount(); got != 2 {
		t.Fatalf("linguistic provider turns = %d, want 2 (cache hit invoked no provider)", got)
	}
	if got := translationProvider.TurnCount(); got != 2 {
		t.Fatalf("translation provider turns = %d, want 2 (cache hit invoked no provider)", got)
	}
	runIDs := make([]string, 0, 2)
	rows, err := db.Query(ctx, `SELECT id FROM analysis_run WHERE article_id = ? ORDER BY started_at`, first.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(runIDs) != 1 {
		t.Fatalf("first article runs = %d", len(runIDs))
	}
	secondRunID := ""
	if err := db.QueryRow(ctx, `SELECT id FROM analysis_run WHERE article_id = ?`, second.ID.String()).Scan(&secondRunID); err != nil {
		t.Fatal(err)
	}

	// Provider-run attempt provenance: binding identity, completion detail,
	// duration, and a miss disposition.
	codexHash := ""
	omlxHash := ""
	for _, binding := range snapshot.Bindings {
		if binding.StageID == pipeline.StageLinguisticAnalysis {
			codexHash = binding.OptionsHash
		} else {
			omlxHash = binding.OptionsHash
		}
	}
	for _, probe := range []struct {
		stage       string
		optionsHash string
	}{
		{"linguistic_analysis", codexHash},
		{"translation", omlxHash},
	} {
		var optionsJSON, optionsHash, requestedModel, reportedModel, usageJSON, disposition, sourceCacheID string
		var durationMS int64
		if err := db.QueryRow(ctx, `SELECT options_json, options_hash, requested_model, reported_model,
			usage_json, cache_disposition, source_cache_id, duration_ms
			FROM analysis_stage_attempt WHERE run_id = ? AND stage_id = ? AND block_index = 0`,
			runIDs[0], probe.stage).Scan(&optionsJSON, &optionsHash, &requestedModel, &reportedModel,
			&usageJSON, &disposition, &sourceCacheID, &durationMS); err != nil {
			t.Fatalf("%s attempt: %v", probe.stage, err)
		}
		if optionsHash != probe.optionsHash || requestedModel != "model-a" {
			t.Fatalf("%s binding identity = hash %q model %q", probe.stage, optionsHash, requestedModel)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(optionsJSON), &decoded); err != nil || len(decoded) == 0 {
			t.Fatalf("%s options_json = %q", probe.stage, optionsJSON)
		}
		if reportedModel != "fake-model" || !strings.Contains(usageJSON, "total_tokens") || durationMS <= 0 {
			t.Fatalf("%s completion provenance = reported %q usage %q duration %d", probe.stage, reportedModel, usageJSON, durationMS)
		}
		if disposition != "miss" || sourceCacheID != "" {
			t.Fatalf("%s disposition = %q source %q, want miss with no source", probe.stage, disposition, sourceCacheID)
		}
	}

	// Cache-hit run: hit disposition, source cache identity, no turns.
	var hitDisposition, hitSource string
	if err := db.QueryRow(ctx, `SELECT cache_disposition, source_cache_id FROM analysis_stage_attempt
		WHERE run_id = ? AND stage_id = 'linguistic_analysis' AND block_index = 0`, secondRunID).
		Scan(&hitDisposition, &hitSource); err != nil {
		t.Fatal(err)
	}
	if hitDisposition != "hit" || hitSource == "" {
		t.Fatalf("hit attempt = disposition %q source %q", hitDisposition, hitSource)
	}
	// Every cache row records the producing run: a miss stores the current
	// run ID, and a later hit retains that provenance instead of claiming
	// the reusing run.
	rows, err = db.Query(ctx, `SELECT source_run_id FROM analysis_stage_cache`)
	if err != nil {
		t.Fatal(err)
	}
	cached := 0
	for rows.Next() {
		var sourceRunID string
		if err := rows.Scan(&sourceRunID); err != nil {
			t.Fatal(err)
		}
		if sourceRunID != runIDs[0] {
			t.Fatalf("cache source_run_id = %q, want producing run %q", sourceRunID, runIDs[0])
		}
		cached++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if cached == 0 {
		t.Fatal("no stage cache rows stored")
	}
	var secondTurns int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_turn WHERE stage_attempt_id IN
		(SELECT id FROM analysis_stage_attempt WHERE run_id = ?)`, secondRunID).Scan(&secondTurns); err != nil {
		t.Fatal(err)
	}
	if secondTurns != 0 {
		t.Fatalf("cache-hit run recorded %d turns", secondTurns)
	}
	// Run summaries carry the profile plus both compact bindings.
	page, err := NewHistoryStore(db).ListRuns(ctx, first.ID.String(), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 {
		t.Fatalf("run summaries = %d", len(page.Runs))
	}
	summary := page.Runs[0]
	if summary.ProfileName == "" || len(summary.Bindings) != 2 ||
		summary.Bindings[0] != (RunBindingSummary{StageID: "linguistic_analysis", ProviderID: "ling-provider", ModelID: "model-a"}) ||
		summary.Bindings[1] != (RunBindingSummary{StageID: "translation", ProviderID: "tr-provider", ModelID: "model-a"}) {
		t.Fatalf("run summary = %+v", summary)
	}
	// Run detail exposes the stored options object, not only its hash.
	run, err := NewHistoryStore(db).GetRun(ctx, libraryULIDOf(t, runIDs[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.StageAttempts) != 4 {
		t.Fatalf("run attempts = %d", len(run.StageAttempts))
	}
	options := run.StageAttempts[0].Options
	if !strings.Contains(string(options), "reasoning_effort") || !strings.Contains(string(options), "low") {
		t.Fatalf("run detail options = %s", options)
	}
}

// TestPipelineRunnerPreflightTerminalFailureFailsArticle proves a preflight
// failure (provider configuration changed or provider missing) on the final
// job attempt transitions the owning article to the failed state with the
// stable code: retry exhaustion can never leave an article stranded in queued
// referencing a terminally failed job.
func TestPipelineRunnerPreflightTerminalFailureFailsArticle(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles, article := queuedPipelineArticle(t, db)
	if _, err := db.Exec(ctx, `UPDATE job SET max_attempts = 1 WHERE id = ?`, article.AnalysisJobID); err != nil {
		t.Fatal(err)
	}
	runner := NewPipelineRunner(db, &fakePipelineRegistry{providers: map[string]annotator.Provider{
		// The linguistic provider the queued snapshot binds is gone.
		"tr-provider": newFakeStageProvider("tr-provider", config.ProviderTypeOpenAICompatible, -1),
	}})
	if runErr := runner.RunOnce(ctx); runErr == nil || !strings.Contains(runErr.Error(), "v1.analysis_provider_unavailable") {
		t.Fatalf("run error = %v, want a provider-unavailable error", runErr)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisFailed || loaded.AnalysisErrorCode != "v1.analysis_provider_unavailable" {
		t.Fatalf("article state = %q / %q, want failed with v1.analysis_provider_unavailable", loaded.AnalysisStatus, loaded.AnalysisErrorCode)
	}
	for _, block := range loaded.Blocks {
		if block.BlockIndex == 0 && (block.AnalysisStatus != reader.BlockFailed || block.AnalysisErrorCode != "v1.analysis_provider_unavailable") {
			t.Fatalf("block 0 = %q / %q, want failed with the stable code", block.AnalysisStatus, block.AnalysisErrorCode)
		}
		if block.BlockIndex != 0 && block.AnalysisStatus != reader.BlockPending {
			t.Fatalf("block %d = %q, want pending", block.BlockIndex, block.AnalysisStatus)
		}
	}
	var jobState string
	if err := db.QueryRow(ctx, `SELECT state FROM job WHERE id = ?`, article.AnalysisJobID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "failed" {
		t.Fatalf("job state = %q, want failed", jobState)
	}
}

// TestPipelineRunnerTransportFailureHidesEndpointURL proves a distinctive
// configured base URL never reaches persisted stage history: the linguistic
// stage of a real OpenAI-compatible provider at a closed port fails, and the
// stored provider error carries the stable sanitized message instead of the
// URL.
func TestPipelineRunnerTransportFailureHidesEndpointURL(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	endpoint := "http://" + listener.Addr().String() + "/v1"
	listener.Close()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	entry := config.ProviderEntry{
		ID: "omlx-unreachable", Label: "Unreachable", EndpointLabel: "Test",
		Type: config.ProviderTypeOpenAICompatible, Enabled: true,
		RequestTimeoutSeconds: 30, BaseURL: endpoint, APIKeyEnv: "DOUBLANGU_TEST_KEY",
	}
	registry, err := annotator.NewRegistry(&config.ProviderConfigFile{
		Version: config.ProviderConfigVersion, Providers: []config.ProviderEntry{entry},
	}, "codex", time.Minute, func(string) (string, error) { return "test-secret", nil })
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	fingerprint := config.ProviderConfigFingerprint(entry)
	options, err := config.CanonicalizeProviderOptions(config.ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0,"max_output_tokens":16384}`))
	if err != nil {
		t.Fatal(err)
	}
	optionsHash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	bindings := make([]pipeline.BindingSnapshot, 0, 2)
	for _, stage := range []pipeline.StageID{pipeline.StageLinguisticAnalysis, pipeline.StageTranslation} {
		contract, prompt, _ := pipeline.StageContracts(stage)
		bindings = append(bindings, pipeline.BindingSnapshot{
			StageID: stage, ProviderID: "omlx-unreachable", ProviderType: config.ProviderTypeOpenAICompatible,
			ProviderConfigFingerprint: fingerprint, ModelID: "omlx-model", Options: options,
			OptionsHash: optionsHash, ContractVersion: contract, PromptVersion: prompt,
		})
	}
	snapshot := &pipeline.ProfileSnapshot{ID: "profile-unreachable", Name: "Unreachable", Bindings: bindings}
	if _, err := snapshot.SnapshotHash(); err != nil {
		t.Fatal(err)
	}
	article, err := reader.NewArticle("Onbereikbaar", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticleQueuedWithProfile(ctx, &article, snapshot); err != nil {
		t.Fatal(err)
	}
	// The unreachable stage fails the run; the assertion target is what the
	// failure wrote to history.
	if err := NewPipelineRunner(db, registry).RunOnce(ctx); err == nil {
		t.Fatal("run unexpectedly succeeded against a closed port")
	}
	rows, err := db.Query(ctx, `
		SELECT a.error_detail, t.provider_error
		FROM analysis_stage_attempt a LEFT JOIN analysis_stage_turn t ON t.stage_attempt_id = a.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	failed := 0
	for rows.Next() {
		var errorDetail, providerError string
		if err := rows.Scan(&errorDetail, &providerError); err != nil {
			t.Fatal(err)
		}
		if errorDetail == "" && providerError == "" {
			continue
		}
		failed++
		for _, leaked := range []string{endpoint, "127.0.0.1", "test-secret"} {
			if strings.Contains(providerError, leaked) || strings.Contains(errorDetail, leaked) {
				t.Fatalf("endpoint identity leaked into history: %q %q", providerError, errorDetail)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if failed == 0 {
		t.Fatal("no failed stage attempts recorded")
	}
}

package analysis

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"doublangu/internal/annotator"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

type countingSemanticProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingSemanticProvider) Analyze(_ context.Context, input semantics.PreparedArticle) (semantics.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
	}
	for _, token := range input.Tokens {
		response.Tokens = append(response.Tokens, semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	return response, nil
}

func (p *countingSemanticProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type chunkCountingProvider struct {
	mu        sync.Mutex
	calls     []int
	failBlock int
}

func (p *chunkCountingProvider) Analyze(context.Context, semantics.PreparedArticle) (semantics.Response, error) {
	return semantics.Response{}, &annotator.Error{Code: annotator.CodeProviderFailure}
}

func (p *chunkCountingProvider) AnalyzeChunk(_ context.Context, chunk semantics.PreparedChunk, options annotator.AnalysisOptions) (annotator.ChunkAttempt, error) {
	p.mu.Lock()
	p.calls = append(p.calls, chunk.Block.BlockIndex)
	fail := p.failBlock == chunk.Block.BlockIndex
	p.mu.Unlock()
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		Tokens:  make([]semantics.TokenResult, 0, len(chunk.Tokens)), NewSenses: []semantics.NewSense{}, Constructions: []semantics.Construction{},
	}
	for _, token := range chunk.Tokens {
		response.Tokens = append(response.Tokens, semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	raw, _ := json.Marshal(response)
	turn := annotator.TurnArtifact{
		BlockIndex: chunk.Block.BlockIndex, TurnIndex: 0, TurnKind: "initial",
		Prompt: "fake prompt", OutputSchema: "fake schema", CompletedResponse: string(raw),
		CompletionMetadataJSON: `{"model":"fake"}`, StartedAt: store.NowUTC(), CompletedAt: store.NowUTC(), Status: "completed",
	}
	attempt := annotator.ChunkAttempt{Response: response, Turns: []annotator.TurnArtifact{turn}, ReportedModel: options.Model}
	if fail {
		turn.ProviderError = "paragraph provider failed"
		turn.Status = "failed"
		attempt.Turns[0] = turn
		return attempt, &annotator.Error{Code: annotator.CodeProviderFailure, Err: context.Canceled}
	}
	return attempt, nil
}

func (p *chunkCountingProvider) Calls() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.calls...)
}

func TestRunnerRetainsFailedParagraphAndReusesOnlyCompatibleChunks(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := NewSettingsStore(db).Seed(ctx, "model-a", "low"); err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	article, err := reader.NewArticle("Chunks", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	provider := &chunkCountingProvider{failBlock: 1}
	runner := NewRunner(db, provider)
	if err := runner.RunOnce(ctx); err == nil {
		t.Fatal("failed paragraph unexpectedly completed")
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Progressive publication: paragraph 1 is durably visible even though the
	// run failed at paragraph 2, which is marked failed with its rows absent.
	if loaded.AnalysisStatus != reader.AnalysisFailed || loaded.AnalysisErrorCode != annotator.CodeProviderFailure {
		t.Fatalf("failed article = %+v", loaded)
	}
	if loaded.AnalysisProgress.CompletedParagraphs != 1 || loaded.AnalysisProgress.FailedBlockIndex != 1 || loaded.AnalysisProgress.TotalParagraphs != 2 {
		t.Fatalf("failed progress = %+v", loaded.AnalysisProgress)
	}
	if len(loaded.Occurrences) != 2 || len(loaded.Blocks[0].Occurrences) != 2 {
		t.Fatalf("published paragraph occurrences = %+v", loaded.Blocks)
	}
	if loaded.Blocks[0].AnalysisStatus != reader.BlockReady || !loaded.Blocks[0].HasAnalysis || !loaded.Blocks[0].AnalysisIsCurrent {
		t.Fatalf("published block state = %+v", loaded.Blocks[0])
	}
	if loaded.Blocks[1].AnalysisStatus != reader.BlockFailed || len(loaded.Blocks[1].Occurrences) != 0 {
		t.Fatalf("failed block state = %+v", loaded.Blocks[1])
	}
	var chunkRows int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_chunk_cache`).Scan(&chunkRows); err != nil {
		t.Fatal(err)
	}
	if chunkRows != 1 {
		t.Fatalf("saved chunk rows after paragraph failure = %d", chunkRows)
	}
	page, err := NewHistoryStore(db).ListRuns(ctx, article.ID.String(), 20, "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Status != "failed" || page.Runs[0].CompletedParagraphs != 1 || page.Runs[0].FailedBlockIndex != 1 {
		t.Fatalf("failed run summary = %+v err=%v", page, err)
	}
	detail, err := NewHistoryStore(db).GetRun(ctx, page.Runs[0].ID)
	if err != nil || len(detail.Turns) != 2 || detail.Turns[1].ProviderError == "" {
		t.Fatalf("failed run detail = %+v err=%v", detail, err)
	}

	provider.mu.Lock()
	provider.failBlock = -1
	provider.mu.Unlock()
	if _, err := articles.QueueAnalysis(ctx, article.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := provider.Calls(); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 1 {
		t.Fatalf("provider calls after compatible retry = %v", got)
	}
	loaded, err = articles.GetArticle(ctx, article.ID)
	if err != nil || loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisModel != "model-a" || loaded.AnalysisEffort != "low" {
		t.Fatalf("successful article = %+v err=%v", loaded, err)
	}

	if _, err := articles.QueueAnalysis(ctx, article.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := provider.Calls(); len(got) != 5 || got[3] != 0 || got[4] != 1 {
		t.Fatalf("provider calls after fresh retry = %v", got)
	}

	if _, err := NewSettingsStore(db).Save(ctx, "model-b", "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := articles.QueueAnalysis(ctx, article.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := provider.Calls(); len(got) != 7 || got[5] != 0 || got[6] != 1 {
		t.Fatalf("provider calls after configuration change = %v", got)
	}
	page, err = NewHistoryStore(db).ListRuns(ctx, article.ID.String(), 20, "")
	if err != nil || len(page.Runs) != 4 {
		t.Fatalf("retained run count = %+v err=%v", page, err)
	}
}

func TestRunnerFailsClosedWhenNoAnalysisModelIsSelected(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	article, err := reader.NewArticle("No model", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	provider := &chunkCountingProvider{failBlock: -1}
	if err := NewRunner(db, provider).RunOnce(ctx); err == nil {
		t.Fatal("analysis without a selected model unexpectedly completed")
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisFailed || loaded.AnalysisErrorCode != "v1.analysis_model_unavailable" {
		t.Fatalf("article without model = %+v", loaded)
	}
	if len(provider.Calls()) != 0 {
		t.Fatalf("provider calls without model = %v", provider.Calls())
	}
	runs, err := NewHistoryStore(db).ListRuns(ctx, article.ID.String(), 20, "")
	if err != nil || len(runs.Runs) != 1 || runs.Runs[0].ErrorCode != "v1.analysis_model_unavailable" {
		t.Fatalf("no-model run = %+v err=%v", runs, err)
	}
}

func TestDecodeAnalysisPayloadRejectsNullFresh(t *testing.T) {
	_, err := decodeAnalysisPayload(`{"article_id":"01J00000000000000000000000","content_hash":"hash","contract_version":"reader.analysis.v3","prompt_version":"reader-analysis-prompt.v6","model":"model","effort":"medium","fresh":null}`)
	if err == nil {
		t.Fatal("null fresh value unexpectedly accepted")
	}
}

func TestRunnerReusesExactValidatedCacheAcrossArticles(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := NewSettingsStore(db).Seed(ctx, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	first, err := reader.NewArticle("Cache", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.NewArticle("Cache", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &first); err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &second); err != nil {
		t.Fatal(err)
	}
	provider := &chunkCountingProvider{failBlock: -1}
	runner := NewRunner(db, provider)
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// The first article misses both paragraph caches; the identical second
	// article hits both exact caches and republishes them paragraph by
	// paragraph without a provider call. The whole-article cache is retired.
	if got := provider.Calls(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("provider calls = %v, want one miss per paragraph", got)
	}
	for _, item := range []reader.Article{first, second} {
		loaded, err := articles.GetArticle(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisRevision != semantics.AnalysisContractVersion {
			t.Fatalf("article lifecycle = %+v", loaded)
		}
		if loaded.AnalysisProgress.CompletedParagraphs != 2 || loaded.Blocks[0].AnalysisStatus != reader.BlockReady || loaded.Blocks[1].AnalysisStatus != reader.BlockReady {
			t.Fatalf("article progress = %+v", loaded.AnalysisProgress)
		}
	}
	var wholeCacheRows, chunkCacheRows, serverSucceeded int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_cache`).Scan(&wholeCacheRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_chunk_cache`).Scan(&chunkCacheRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE execution_target = 'server' AND state = 'succeeded'`).Scan(&serverSucceeded); err != nil {
		t.Fatal(err)
	}
	if wholeCacheRows != 0 || chunkCacheRows != 2 || serverSucceeded != 2 {
		t.Fatalf("cache/server rows = %d/%d/%d", wholeCacheRows, chunkCacheRows, serverSucceeded)
	}
}

// gatedChunkProvider blocks analysis of one paragraph until the test releases
// it, proving that earlier paragraphs are published while later ones run.
type gatedChunkProvider struct {
	mu          sync.Mutex
	calls       []int
	blockedAt   int
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newGatedChunkProvider(blockedAt int) *gatedChunkProvider {
	return &gatedChunkProvider{blockedAt: blockedAt, entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *gatedChunkProvider) Analyze(context.Context, semantics.PreparedArticle) (semantics.Response, error) {
	return semantics.Response{}, &annotator.Error{Code: annotator.CodeProviderFailure}
}

func (p *gatedChunkProvider) AnalyzeChunk(_ context.Context, chunk semantics.PreparedChunk, options annotator.AnalysisOptions) (annotator.ChunkAttempt, error) {
	p.mu.Lock()
	p.calls = append(p.calls, chunk.Block.BlockIndex)
	blocked := chunk.Block.BlockIndex == p.blockedAt
	p.mu.Unlock()
	if blocked {
		select {
		case <-p.entered:
		default:
			close(p.entered)
		}
		<-p.release
	}
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		Tokens:  make([]semantics.TokenResult, 0, len(chunk.Tokens)), NewSenses: []semantics.NewSense{}, Constructions: []semantics.Construction{},
	}
	for _, token := range chunk.Tokens {
		response.Tokens = append(response.Tokens, semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	raw, _ := json.Marshal(response)
	attempt := annotator.ChunkAttempt{
		Response: response,
		Turns: []annotator.TurnArtifact{{
			BlockIndex: chunk.Block.BlockIndex, TurnIndex: 0, TurnKind: "initial",
			Prompt: "fake prompt", OutputSchema: "fake schema", CompletedResponse: string(raw),
			CompletionMetadataJSON: `{"model":"fake"}`, StartedAt: store.NowUTC(), CompletedAt: store.NowUTC(), Status: "completed",
		}},
		ReportedModel: options.Model,
	}
	return attempt, nil
}

func (p *gatedChunkProvider) Unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *gatedChunkProvider) Calls() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.calls...)
}

// TestRunnerPublishesParagraphsProgressively proves the headline acceptance
// case: a three-paragraph run exposes paragraph 1 through the article API
// while paragraph 2 is still blocked, and paragraph 3 stays raw and pending.
func TestRunnerPublishesParagraphsProgressively(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := NewSettingsStore(db).Seed(ctx, "model-a", "low"); err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	article, err := reader.NewArticle("Progressief", "Een zin.\n\nNog een.\n\nDerde alinea!", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	provider := newGatedChunkProvider(1)
	runner := NewRunner(db, provider)
	done := make(chan error, 1)
	go func() { done <- runner.RunOnce(ctx) }()
	select {
	case <-provider.entered:
	case <-done:
		t.Fatalf("runner finished before paragraph 2 was reached: %v", <-done)
	}
	// Paragraph 1 is durably visible through the normal article API while
	// paragraph 2 is still being produced; paragraph 3 has no semantics yet.
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisProcessing {
		t.Fatalf("article status while blocked = %q", loaded.AnalysisStatus)
	}
	if loaded.AnalysisProgress.CompletedParagraphs != 1 || loaded.AnalysisProgress.CurrentBlockIndex != 1 || loaded.AnalysisProgress.TotalParagraphs != 3 {
		t.Fatalf("progress while blocked = %+v", loaded.AnalysisProgress)
	}
	if len(loaded.Blocks[0].Occurrences) == 0 || loaded.Blocks[0].AnalysisStatus != reader.BlockReady || !loaded.Blocks[0].AnalysisIsCurrent {
		t.Fatalf("paragraph 1 not visible while paragraph 2 is blocked: %+v", loaded.Blocks[0])
	}
	if loaded.Blocks[1].AnalysisStatus != reader.BlockProcessing || loaded.Blocks[2].AnalysisStatus != reader.BlockPending {
		t.Fatalf("later paragraph states while blocked = %+v / %+v", loaded.Blocks[1].AnalysisStatus, loaded.Blocks[2].AnalysisStatus)
	}
	if len(loaded.Blocks[1].Occurrences) != 0 || len(loaded.Blocks[2].Occurrences) != 0 || loaded.Blocks[2].HasAnalysis {
		t.Fatalf("unpublished paragraphs leaked semantics: %+v", loaded.Blocks)
	}
	provider.Unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("progressive run failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("progressive run did not finish")
	}
	loaded, err = articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisProgress.CompletedParagraphs != 3 || loaded.AnalysisProgress.FailedBlockIndex != -1 {
		t.Fatalf("final state = %+v / %+v", loaded.AnalysisStatus, loaded.AnalysisProgress)
	}
	for index, block := range loaded.Blocks {
		if block.AnalysisStatus != reader.BlockReady || len(block.Occurrences) == 0 {
			t.Fatalf("block %d not ready with semantics: %+v", index, block)
		}
	}
	if calls := provider.Calls(); len(calls) != 3 || calls[0] != 0 || calls[1] != 1 || calls[2] != 2 {
		t.Fatalf("provider calls = %v", calls)
	}
}

// TestRunnerFailedReanalysisPreservesOlderMaterialization proves acceptance
// case 2 for a reanalysis: when paragraph 2 of a fresh run fails, published
// paragraph 1 and the older paragraph 2 materialization remain readable with
// completed_paragraphs = 1 and failed_block_index = 1.
func TestRunnerFailedReanalysisPreservesOlderMaterialization(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := NewSettingsStore(db).Seed(ctx, "model-a", "low"); err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	article, err := reader.NewArticle("Heranalyse", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	// First run succeeds completely and publishes both paragraphs.
	if err := NewRunner(db, &chunkCountingProvider{failBlock: -1}).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	block1Before := len(before.Blocks[1].Occurrences)
	if block1Before == 0 {
		t.Fatal("first run published no paragraph 2 rows")
	}
	// The fresh reanalysis fails at paragraph 2.
	failing := &chunkCountingProvider{failBlock: 1}
	runner := NewRunner(db, failing)
	if _, err := articles.QueueAnalysis(ctx, article.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err == nil {
		t.Fatal("failed reanalysis unexpectedly completed")
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisFailed {
		t.Fatalf("article status = %q", loaded.AnalysisStatus)
	}
	if loaded.AnalysisProgress.CompletedParagraphs != 1 || loaded.AnalysisProgress.FailedBlockIndex != 1 {
		t.Fatalf("progress after failed reanalysis = %+v", loaded.AnalysisProgress)
	}
	if len(loaded.Blocks[0].Occurrences) == 0 || loaded.Blocks[0].AnalysisStatus != reader.BlockReady {
		t.Fatalf("published paragraph 1 lost: %+v", loaded.Blocks[0])
	}
	if loaded.Blocks[1].AnalysisStatus != reader.BlockFailed || len(loaded.Blocks[1].Occurrences) != block1Before {
		t.Fatalf("older paragraph 2 materialization not preserved: %+v", loaded.Blocks[1])
	}
	if !loaded.Blocks[1].HasAnalysis || loaded.Blocks[1].AnalysisIsCurrent {
		t.Fatalf("stale materialization markers wrong: %+v", loaded.Blocks[1])
	}
}

// TestRunnerSchedulerRetryReinitializesBlocks proves that an automatically
// requeued job (same job id, no QueueAnalysis call) can process blocks that
// an earlier attempt published or failed: the retry resets this job's block
// lifecycle to pending while preserving the published materialization.
func TestRunnerSchedulerRetryReinitializesBlocks(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := NewSettingsStore(db).Seed(ctx, "model-a", "low"); err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	article, err := reader.NewArticle("Herpoging", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	provider := &chunkCountingProvider{failBlock: 1}
	runner := NewRunner(db, provider)
	if err := runner.RunOnce(ctx); err == nil {
		t.Fatal("first attempt with a failing paragraph unexpectedly completed")
	}
	// The first attempt durably published paragraph one before failing at
	// paragraph two; the scheduler requeues the same job after its retry
	// backoff. The retry must reinitialize this job's block lifecycle (not
	// stumble over the failed paragraph two) while the published material
	// stays readable and is republished from the exact chunk cache.
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisProgress.CompletedParagraphs != 1 || loaded.Blocks[0].AnalysisStatus != reader.BlockReady || len(loaded.Blocks[0].Occurrences) == 0 {
		t.Fatalf("published paragraph lost before the retry: %+v", loaded.AnalysisProgress)
	}
	provider.mu.Lock()
	provider.failBlock = -1
	provider.mu.Unlock()
	// Bring the requeued job's availability forward so the test does not wait
	// out the real five-second backoff.
	if _, err := db.Exec(ctx, `UPDATE job SET available_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE owner_type = 'article' AND owner_id = ? AND state = 'queued'`, article.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("automatic retry failed: %v", err)
	}
	loaded, err = articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisProgress.CompletedParagraphs != 2 {
		t.Fatalf("article after automatic retry = %+v / %+v", loaded.AnalysisStatus, loaded.AnalysisProgress)
	}
	for index, block := range loaded.Blocks {
		if block.AnalysisStatus != reader.BlockReady || !block.AnalysisIsCurrent || len(block.Occurrences) == 0 {
			t.Fatalf("block %d after automatic retry = %+v", index, block)
		}
	}
	// First attempt called the provider for paragraphs one and two; the retry
	// republished paragraph one from its exact cache hit and reprocessed
	// paragraph two.
	if calls := provider.Calls(); len(calls) != 3 || calls[0] != 0 || calls[1] != 1 || calls[2] != 1 {
		t.Fatalf("provider calls = %v", calls)
	}
}

// scriptedChunkProvider returns prepared raw responses per block so a test
// can model cross-paragraph sense references exactly like the provider would.
type scriptedChunkProvider struct {
	responses map[int]semantics.Response
}

func (p *scriptedChunkProvider) Analyze(context.Context, semantics.PreparedArticle) (semantics.Response, error) {
	return semantics.Response{}, &annotator.Error{Code: annotator.CodeProviderFailure}
}

func (p *scriptedChunkProvider) AnalyzeChunk(_ context.Context, chunk semantics.PreparedChunk, options annotator.AnalysisOptions) (annotator.ChunkAttempt, error) {
	response, ok := p.responses[chunk.Block.BlockIndex]
	if !ok {
		return annotator.ChunkAttempt{}, &annotator.Error{Code: annotator.CodeProviderFailure, Err: context.Canceled}
	}
	raw, _ := json.Marshal(response)
	return annotator.ChunkAttempt{
		Response: response,
		Turns: []annotator.TurnArtifact{{
			BlockIndex: chunk.Block.BlockIndex, TurnIndex: 0, TurnKind: "initial",
			Prompt: "fake prompt", OutputSchema: "fake schema", CompletedResponse: string(raw),
			CompletionMetadataJSON: `{"model":"fake"}`, StartedAt: store.NowUTC(), CompletedAt: store.NowUTC(), Status: "completed",
		}},
		ReportedModel: options.Model,
	}, nil
}

// TestRunnerPersistsPriorSenseReferencesAndAuditsRawChunks proves that a
// later paragraph referencing an earlier paragraph's namespaced sense
// materializes that sense (reusing the durable row) and that the final
// consistency audit does not namespace chunk responses twice.
func TestRunnerPersistsPriorSenseReferencesAndAuditsRawChunks(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := NewSettingsStore(db).Seed(ctx, "model-a", "low"); err != nil {
		t.Fatal(err)
	}
	articles := reader.NewStore(db)
	article, err := reader.NewArticle("Draagkracht", "De bank.\n\nDe bank.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	block0 := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		NewSenses: []semantics.NewSense{{
			Ref: "bank-sofa", Kind: semantics.KindWord, CanonicalForm: "bank", NormalizedForm: "bank",
			Lemma: "bank", PartOfSpeech: "noun", SenseDiscriminator: "sofa",
			PrimaryTranslation: "sofa",
		}},
	}
	block0.Tokens = []semantics.TokenResult{
		{TokenID: "b0:t0", Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000},
		{TokenID: "b0:t1", Classification: "lexical", Kind: semantics.KindWord, NewSenseRef: "bank-sofa", ShadowText: "sofa", ConfidenceMilli: 950},
	}
	// Paragraph two references the earlier paragraph's namespaced sense
	// exactly as the provider sees it in PRIOR_VALIDATED_SENSES.
	block1 := semantics.Response{Version: semantics.AnalysisContractVersion}
	block1.Tokens = []semantics.TokenResult{
		{TokenID: "b1:t2", Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000},
		{TokenID: "b1:t3", Classification: "lexical", Kind: semantics.KindWord, NewSenseRef: "b0:bank-sofa", ShadowText: "sofa", ConfidenceMilli: 950},
	}
	provider := &scriptedChunkProvider{responses: map[int]semantics.Response{0: block0, 1: block1}}
	if err := NewRunner(db, provider).RunOnce(ctx); err != nil {
		t.Fatalf("prior-reference run failed: %v", err)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisProgress.CompletedParagraphs != 2 {
		t.Fatalf("article = %+v / %+v", loaded.AnalysisStatus, loaded.AnalysisProgress)
	}
	var firstBankID, secondBankID string
	for _, occurrence := range loaded.Occurrences {
		if occurrence.ShadowText == "sofa" && occurrence.Role == "token" {
			if occurrence.SemanticSenseID == nil {
				t.Fatalf("bank occurrence has no sense: %+v", occurrence)
			}
			if firstBankID == "" {
				firstBankID = occurrence.SemanticSenseID.String()
			} else if secondBankID == "" {
				secondBankID = occurrence.SemanticSenseID.String()
			}
		}
	}
	if firstBankID == "" || secondBankID == "" || firstBankID != secondBankID {
		t.Fatalf("prior sense did not reuse the durable row: %q / %q", firstBankID, secondBankID)
	}
	var sofaSenses int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM semantic_sense WHERE primary_translation = 'sofa' AND retired_at = ''`).Scan(&sofaSenses); err != nil {
		t.Fatal(err)
	}
	if sofaSenses != 1 {
		t.Errorf("sense rows for sofa = %d, want 1", sofaSenses)
	}
}

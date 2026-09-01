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
		Version:   semantics.AnalysisContractVersion,
		Sentences: []semantics.Sentence{{Source: semantics.SpanRef{BlockIndex: 0, SourceText: input.Blocks[0].SourceText, Occurrence: 0}}},
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
		Version:   semantics.AnalysisContractVersion,
		Sentences: []semantics.Sentence{{Source: semantics.SpanRef{BlockIndex: chunk.Block.BlockIndex, SourceText: chunk.Block.SourceText, Occurrence: 0}}},
		Tokens:    make([]semantics.TokenResult, 0, len(chunk.Tokens)), NewSenses: []semantics.NewSense{}, Constructions: []semantics.Construction{},
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
	if loaded.AnalysisStatus != reader.AnalysisFailed || loaded.AnalysisErrorCode != annotator.CodeProviderFailure || len(loaded.Occurrences) != 0 {
		t.Fatalf("failed article = %+v", loaded)
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
	_, err := decodeAnalysisPayload(`{"article_id":"01J00000000000000000000000","content_hash":"hash","contract_version":"reader.analysis.v2","prompt_version":"reader-analysis-prompt.v2","model":"model","effort":"medium","fresh":null}`)
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
	first, err := reader.NewArticle("Cache", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.NewArticle("Cache", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &first); err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &second); err != nil {
		t.Fatal(err)
	}
	provider := &countingSemanticProvider{}
	runner := NewRunner(db, provider)
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want one cache miss", provider.Calls())
	}
	for _, id := range []reader.Article{first, second} {
		loaded, err := articles.GetArticle(ctx, id.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.AnalysisStatus != reader.AnalysisReady || loaded.AnalysisRevision != semantics.AnalysisContractVersion {
			t.Fatalf("article lifecycle = %+v", loaded)
		}
	}
	var cacheRows, serverSucceeded int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_cache`).Scan(&cacheRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE execution_target = 'server' AND state = 'succeeded'`).Scan(&serverSucceeded); err != nil {
		t.Fatal(err)
	}
	if cacheRows != 1 || serverSucceeded != 2 {
		t.Fatalf("cache/server rows = %d/%d", cacheRows, serverSucceeded)
	}
}

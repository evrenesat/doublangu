package analysis

import (
	"context"
	"sync"
	"testing"

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

func TestRunnerReusesExactValidatedCacheAcrossArticles(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
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

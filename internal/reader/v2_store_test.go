package reader

import (
	"context"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

func validUnchangedResponse(input semantics.PreparedArticle) semantics.Response {
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		Tokens:  make([]semantics.TokenResult, 0, len(input.Tokens)), NewSenses: []semantics.NewSense{}, Constructions: []semantics.Construction{},
	}
	for _, token := range input.Tokens {
		response.Tokens = append(response.Tokens, semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	return response
}

func TestPersistAnalysisMaterializesLayeredRowsAndSpeechJobs(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	article, err := NewArticle("Bank", "De bank staat.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := NewStore(db)
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	prepared, err := articles.PrepareAnalysis(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		NewSenses: []semantics.NewSense{{
			Ref: "bank-sofa", Kind: semantics.KindWord, CanonicalForm: "bank", NormalizedForm: "bank", Lemma: "bank",
			SenseDiscriminator: "sofa", PrimaryTranslation: "sofa", Alternatives: []string{"couch"},
			CanonicalPronunciationText: "bank",
		}},
	}
	for _, token := range prepared.Tokens {
		result := semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000}
		if token.NormalizedForm == "bank" {
			result.Classification = "lexical"
			result.NewSenseRef = "bank-sofa"
			result.ShadowText = "sofa"
			result.CanonicalPronunciation = "bank-sound"
			result.ContextPronunciationKey = "sofa-context"
		}
		response.Tokens = append(response.Tokens, result)
	}
	validated, err := semantics.ValidateResponse(prepared, response)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysis(ctx, article.ID, prepared, validated, semantics.ProviderID, "test-model"); err != nil {
		t.Fatal(err)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != AnalysisReady || len(loaded.Sentences) != 1 || len(loaded.Occurrences) != len(prepared.Tokens) {
		t.Fatalf("loaded lifecycle/slices = %s/%d/%d", loaded.AnalysisStatus, len(loaded.Sentences), len(loaded.Occurrences))
	}
	var bankSense *SemanticSense
	for index := range loaded.Occurrences {
		for _, span := range loaded.Occurrences[index].Spans {
			if span.SourceText == "bank" {
				bankSense = loaded.Occurrences[index].Sense
			}
		}
	}
	if bankSense == nil || bankSense.PrimaryTranslation != "sofa" {
		t.Fatalf("bank sense = %+v occurrences=%+v", bankSense, loaded.Occurrences)
	}
	var speechJobs, lexicalRenders, narrationRenders int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE execution_target = 'macos'`).Scan(&speechJobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audio_render WHERE retention_class = 'lexical_permanent'`).Scan(&lexicalRenders); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audio_render WHERE retention_class = 'article_narration'`).Scan(&narrationRenders); err != nil {
		t.Fatal(err)
	}
	if speechJobs == 0 || lexicalRenders == 0 || narrationRenders != 1 {
		t.Fatalf("speech jobs/renders = %d/%d/%d", speechJobs, lexicalRenders, narrationRenders)
	}
	var spokenText, pronunciationKey string
	if err := db.QueryRow(ctx, `SELECT spoken_text, context_pronunciation_key FROM speech_unit WHERE unit_kind = 'word' AND spoken_text = 'bank'`).Scan(&spokenText, &pronunciationKey); err != nil {
		t.Fatal(err)
	}
	if spokenText != "bank" || pronunciationKey != "sofa-context" {
		t.Fatalf("pronunciation unit = %q/%q", spokenText, pronunciationKey)
	}

	if _, err := db.Exec(ctx, `UPDATE job SET state = 'succeeded', completed_at = ? WHERE owner_type = 'article' AND owner_id = ? AND job_type = ?`, store.NowUTC(), article.ID.String(), jobs.AnalysisJobType); err != nil {
		t.Fatal(err)
	}
	queued, err := articles.QueueAnalysis(ctx, article.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != jobs.StateQueued {
		t.Fatalf("requeued ready analysis state = %q", queued.State)
	}
	loaded, err = articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != AnalysisQueued || len(loaded.Occurrences) == 0 {
		t.Fatalf("requeued ready article = %+v", loaded)
	}
}

func TestAnalysisCachesRequirePreparedInputAndProviderSelection(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	article, err := NewArticle("Cache", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := NewStore(db)
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	prepared, err := articles.PrepareAnalysis(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := validUnchangedResponse(prepared)
	validated, err := semantics.ValidateResponse(prepared, response)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysis(ctx, article.ID, prepared, validated, semantics.ProviderID, "model-a", "low", "model-a", "reported-a"); err != nil {
		t.Fatal(err)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisModel != "model-a" || loaded.AnalysisEffort != "low" {
		t.Fatalf("article provenance = %q/%q", loaded.AnalysisModel, loaded.AnalysisEffort)
	}
	if _, _, hit, err := articles.CachedAnalysis(ctx, prepared, "model-a", "low"); err != nil || !hit {
		t.Fatalf("exact cache lookup hit=%v err=%v", hit, err)
	}
	if _, _, hit, err := articles.CachedAnalysis(ctx, prepared, "model-b", "low"); err != nil || hit {
		t.Fatalf("model-cross-hit=%v err=%v", hit, err)
	}
	if _, _, hit, err := articles.CachedAnalysis(ctx, prepared, "model-a", "high"); err != nil || hit {
		t.Fatalf("effort-cross-hit=%v err=%v", hit, err)
	}
	changedInput := prepared
	changedInput.Candidates = []semantics.SenseCandidate{{ID: "new-candidate", SourceLanguage: "nl", TargetLanguage: "en", Kind: semantics.KindWord, CanonicalForm: "zin", NormalizedForm: "zin", PrimaryTranslation: "sentence", SenseDiscriminator: "test"}}
	if _, _, hit, err := articles.CachedAnalysis(ctx, changedInput, "model-a", "low"); err != nil || hit {
		t.Fatalf("candidate-cross-hit=%v err=%v", hit, err)
	}
	if _, err := db.Exec(ctx, `UPDATE analysis_cache SET response_hash = 'corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, _, hit, err := articles.CachedAnalysis(ctx, prepared, "model-a", "low"); err != nil || hit {
		t.Fatalf("corrupt whole cache hit=%v err=%v", hit, err)
	}

	chunk, err := semantics.PrepareChunk(prepared, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.SaveChunk(ctx, chunk, response, semantics.ProviderID, "model-a", "low", library.NewULID()); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := articles.CachedChunk(ctx, chunk, "model-a", "low"); err != nil || !hit {
		t.Fatalf("exact chunk cache hit=%v err=%v", hit, err)
	}
	if _, err := db.Exec(ctx, `UPDATE analysis_chunk_cache SET response_hash = 'corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := articles.CachedChunk(ctx, chunk, "model-a", "low"); err != nil || hit {
		t.Fatalf("corrupt chunk cache hit=%v err=%v", hit, err)
	}
}

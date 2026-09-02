package reader

import (
	"context"
	"testing"

	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// TestCreateArticleQueuedPersistsDeterministicSourceSentences proves that a
// new article stores stable source sentence rows in the same transaction as
// its blocks, before any analysis job runs.
func TestCreateArticleQueuedPersistsDeterministicSourceSentences(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	article, err := NewArticle("Stabiel", "Eerste zin. Tweede zin.\n\nTweede alinea!", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := NewStore(db)
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Sentences) != 3 {
		t.Fatalf("stored sentences = %d, want 3", len(loaded.Sentences))
	}
	for index, sentence := range loaded.Sentences {
		if sentence.SourceText == "" || sentence.EndUTF16 <= sentence.StartUTF16 {
			t.Fatalf("sentence %d has an invalid span: %+v", index, sentence)
		}
	}
	// The stored rows must be exactly the deterministic segmentation output.
	expected, err := SegmentArticleSentences([]semantics.Block{
		{BlockIndex: 0, SourceText: loaded.Blocks[0].SourceText},
		{BlockIndex: 1, SourceText: loaded.Blocks[1].SourceText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) != 3 {
		t.Fatalf("expected anchors = %d, want 3", len(expected))
	}
	for index, sentence := range loaded.Sentences {
		anchor := expected[index]
		if sentence.StartUTF16 != anchor.Span.StartUTF16 || sentence.EndUTF16 != anchor.Span.EndUTF16 || sentence.SourceText != anchor.Span.SourceText {
			t.Errorf("sentence %d = %+v, want anchor %+v", index, sentence, anchor.Span)
		}
	}
	// Blocks carry the sentences they own in order.
	if len(loaded.Blocks[0].Sentences) != 2 || len(loaded.Blocks[1].Sentences) != 1 {
		t.Errorf("per-block sentences = %d/%d", len(loaded.Blocks[0].Sentences), len(loaded.Blocks[1].Sentences))
	}
	var revision string
	if err := db.QueryRow(ctx, `SELECT sentence_revision FROM article WHERE id = ?`, article.ID.String()).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != SentenceRevisionSourceSentencesV1 {
		t.Errorf("sentence revision = %q, want %q", revision, SentenceRevisionSourceSentencesV1)
	}
	// No analysis has touched the article yet, so the revision still records
	// the deterministic origin.
	if loaded.AnalysisStatus != AnalysisQueued {
		t.Errorf("analysis status = %q, want queued", loaded.AnalysisStatus)
	}
}

// TestPrepareAnalysisUsesStoredOrLazilyCreatedSentences proves that prepared
// inputs always carry the persisted anchors and that an article without rows
// receives deterministic rows exactly once.
func TestPrepareAnalysisUsesStoredOrLazilyCreatedSentences(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	t.Run("created article reuses stored rows", func(t *testing.T) {
		article, err := NewArticle("Ankers", "De bank staat. Hij is oud.", "nl", "en")
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
		if len(prepared.Sentences) != 2 {
			t.Fatalf("prepared anchors = %d, want 2", len(prepared.Sentences))
		}
		for index, anchor := range prepared.Sentences {
			if anchor.Index != index || anchor.Span.SourceText == "" {
				t.Errorf("anchor %d = %+v", index, anchor)
			}
		}
		if prepared.Sentences[0].Span.EndUTF16 > prepared.Sentences[1].Span.StartUTF16 {
			t.Errorf("anchors overlap or are out of order: %+v", prepared.Sentences)
		}
	})

	t.Run("legacy article gets deterministic rows lazily once", func(t *testing.T) {
		article, err := NewArticle("Lazy", "Eén zin zonder punt", "nl", "en")
		if err != nil {
			t.Fatal(err)
		}
		articles := NewStore(db)
		// Legacy creation path: article and blocks only, no sentence rows.
		if err := articles.CreateArticle(ctx, &article); err != nil {
			t.Fatal(err)
		}
		var before int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		prepared, err := articles.PrepareAnalysis(ctx, article.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(prepared.Sentences) != 1 {
			t.Fatalf("lazy anchors = %d, want 1", len(prepared.Sentences))
		}
		var after int
		var revision string
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT sentence_revision FROM article WHERE id = ?`, article.ID.String()).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if after-before != 1 {
			t.Errorf("lazy sentence rows delta = %d, want 1", after-before)
		}
		if revision != SentenceRevisionSourceSentencesV1 {
			t.Errorf("lazy sentence revision = %q, want %q", revision, SentenceRevisionSourceSentencesV1)
		}
		// A second preparation must not duplicate the rows.
		if _, err := articles.PrepareAnalysis(ctx, article.ID); err != nil {
			t.Fatal(err)
		}
		var second int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence`).Scan(&second); err != nil {
			t.Fatal(err)
		}
		if second != after {
			t.Errorf("sentence rows after second prepare = %d, want %d", second, after)
		}
	})
}

// TestPersistAnalysisMarksReplacedSentencesAsLegacy records the provenance of
// the transitional whole-article persistence path: rows it replaces are
// provider-derived and therefore legacy, never silently labelled
// deterministic.
func TestPersistAnalysisMarksReplacedSentencesAsLegacy(t *testing.T) {
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
	response := validUnchangedResponse(prepared)
	validated, err := semantics.ValidateResponse(prepared, response)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysis(ctx, article.ID, prepared, validated, semantics.ProviderID, "test-model"); err != nil {
		t.Fatal(err)
	}
	var revision string
	if err := db.QueryRow(ctx, `SELECT sentence_revision FROM article WHERE id = ?`, article.ID.String()).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != SentenceRevisionLegacyAnalysis {
		t.Errorf("sentence revision after whole-article persist = %q, want %q", revision, SentenceRevisionLegacyAnalysis)
	}
	var sentences int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence WHERE article_block_id IN (SELECT id FROM article_block WHERE article_id = ?)`, article.ID.String()).Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if sentences != 1 {
		t.Errorf("sentences after persist = %d, want 1", sentences)
	}
}

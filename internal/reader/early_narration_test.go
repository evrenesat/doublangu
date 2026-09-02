package reader

import (
	"context"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// TestEarlyNarrationPrecedesAnalysis proves acceptance case 5: narration jobs
// exist before the fake analyzer is released, while no lexical job exists for
// an unpublished block, and paragraph publication never rewrites narration.
func TestEarlyNarrationPrecedesAnalysis(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID, prepared, _, validated := chunkFixture(t, db)
	articles := NewStore(db)

	var narrationRenders, narrationJobs, lexicalRenders, bindings int
	count := func(query string, target *int) {
		t.Helper()
		if err := db.QueryRow(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	count(`SELECT COUNT(*) FROM audio_render WHERE retention_class = 'article_narration'`, &narrationRenders)
	count(`SELECT COUNT(*) FROM job WHERE execution_target = 'macos' AND job_type = 'tts.chatterbox.v3'`, &narrationJobs)
	count(`SELECT COUNT(*) FROM audio_render WHERE retention_class = 'lexical_permanent'`, &lexicalRenders)
	if narrationRenders != 2 || narrationJobs != 2 {
		t.Fatalf("early narration renders/jobs = %d/%d, want 2/2", narrationRenders, narrationJobs)
	}
	if lexicalRenders != 0 {
		t.Fatalf("lexical renders before any publish = %d, want 0", lexicalRenders)
	}
	var narrationStatus string
	if err := db.QueryRow(ctx, `SELECT narration_status FROM article WHERE id = ?`, articleID.String()).Scan(&narrationStatus); err != nil {
		t.Fatal(err)
	}
	if narrationStatus != "queued" {
		t.Errorf("narration status after creation = %q, want queued", narrationStatus)
	}

	// Publish paragraph 1 only and prove narration rows and bindings are
	// untouched while lexical audio appears for that paragraph.
	var sentenceIDs []string
	rows, err := db.Query(ctx, `SELECT id FROM article_sentence ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		sentenceIDs = append(sentenceIDs, id)
	}
	rows.Close()
	count(`SELECT COUNT(*) FROM article_sentence_audio`, &bindings)

	jobID := activeAnalysisJobID(t, db, articleID)
	if err := articles.MarkAnalysisProcessing(ctx, articleID, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 0, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 0, jobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	var narrationRendersAfter, bindingsAfter, lexicalRendersAfter int
	count(`SELECT COUNT(*) FROM audio_render WHERE retention_class = 'article_narration'`, &narrationRendersAfter)
	count(`SELECT COUNT(*) FROM article_sentence_audio`, &bindingsAfter)
	count(`SELECT COUNT(*) FROM audio_render WHERE retention_class = 'lexical_permanent'`, &lexicalRendersAfter)
	if narrationRendersAfter != narrationRenders || bindingsAfter != bindings {
		t.Fatalf("narration changed during analysis: renders %d->%d bindings %d->%d", narrationRenders, narrationRendersAfter, bindings, bindingsAfter)
	}
	if lexicalRendersAfter == 0 {
		t.Fatal("no lexical render appeared for the published paragraph")
	}
	var remainingSentenceIDs []string
	rows, err = db.Query(ctx, `SELECT id FROM article_sentence ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		remainingSentenceIDs = append(remainingSentenceIDs, id)
	}
	rows.Close()
	if len(remainingSentenceIDs) != len(sentenceIDs) {
		t.Fatalf("sentence rows changed during analysis: %d -> %d", len(sentenceIDs), len(remainingSentenceIDs))
	}
	for index := range sentenceIDs {
		if sentenceIDs[index] != remainingSentenceIDs[index] {
			t.Fatalf("stable sentence id %d changed: %s -> %s", index, sentenceIDs[index], remainingSentenceIDs[index])
		}
	}
}

// TestQueuedAnalysisPublishAllBlocksVerifiesIdsAreStable exercises the full
// per-block publish with narration queued at creation (acceptance case 6).
func TestQueuedAnalysisPublishAllBlocksVerifiesIdsAreStable(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID, prepared, _, validated := chunkFixture(t, db)
	articles := NewStore(db)
	jobID := activeAnalysisJobID(t, db, articleID)
	if err := articles.MarkAnalysisProcessing(ctx, articleID, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 0, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 0, jobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	var narrationBindings, lexicalRenders int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence_audio`).Scan(&narrationBindings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audio_render WHERE retention_class = 'lexical_permanent'`).Scan(&lexicalRenders); err != nil {
		t.Fatal(err)
	}
	if narrationBindings != 2 || lexicalRenders == 0 {
		t.Fatalf("bindings/renders = %d/%d", narrationBindings, lexicalRenders)
	}
	// A fresh reanalysis must not delete, renumber, or replace sentences or
	// their narration bindings even though it replaces paragraph semantics.
	second, err := articles.QueueAnalysis(ctx, articleID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkAnalysisProcessing(ctx, articleID, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 0, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 0, second.ID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	var narrationBindingsAfter int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence_audio`).Scan(&narrationBindingsAfter); err != nil {
		t.Fatal(err)
	}
	if narrationBindingsAfter != narrationBindings {
		t.Errorf("narration bindings changed across reanalysis: %d -> %d", narrationBindings, narrationBindingsAfter)
	}
}

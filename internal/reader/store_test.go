package reader

import (
	"context"
	"errors"
	"testing"
	"time"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

func TestStoreArticleLifecycleAndLearningPrecedence(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := NewStore(db)
	article := testArticle(t, "Ik wil tot rust komen.")
	if err := articles.CreateArticle(context.Background(), &article); err != nil {
		t.Fatal(err)
	}
	list, err := articles.ListArticles(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %#v err=%v", list, err)
	}
	if list[0].EnrichmentStatus != StatusDraft {
		t.Fatalf("initial status = %q", list[0].EnrichmentStatus)
	}

	result, err := NormalizeCandidates(&article, []Candidate{testCandidate("tot rust komen", KindExpression, 0, 0, "to calm down", true)})
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.ReplaceAnnotations(context.Background(), article.ID, result.Annotations); err != nil {
		t.Fatal(err)
	}
	got, err := articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnrichmentStatus != StatusReady || len(got.Blocks) != 1 || len(got.Blocks[0].Annotations) != 1 {
		t.Fatalf("stored article = %+v", got)
	}
	annotation := got.Blocks[0].Annotations[0]
	if !annotation.ShowShadow || annotation.ID.IsZero() {
		t.Fatalf("default subtitle/id = %+v", annotation)
	}

	state, err := articles.UpsertLearningState(context.Background(), &LearningState{
		SourceLanguage: "NL", Kind: KindExpression, LearningKey: " TOT\tRUST  KOMEN ", Status: LearningStatusLearned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.SourceLanguage != "nl" || state.LearningKey != "tot rust komen" {
		t.Fatalf("normalized state = %+v", state)
	}
	got, err = articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	annotation = got.Blocks[0].Annotations[0]
	if annotation.LearningState == nil || annotation.LearningState.Status != LearningStatusLearned || annotation.ShowShadow {
		t.Fatalf("learned annotation = %+v", annotation)
	}

	if _, err := articles.UpsertLearningState(context.Background(), &LearningState{
		SourceLanguage: "nl", Kind: KindExpression, LearningKey: "tot rust komen", Status: LearningStatusUnlearned,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Blocks[0].Annotations[0].ShowShadow != true {
		t.Fatal("explicit unlearned state did not show subtitle")
	}
}

func TestStoreFailurePreservesPreviousAnnotationSetAndRecovery(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := NewStore(db)
	article := testArticle(t, "Een woord.")
	if err := articles.CreateArticle(context.Background(), &article); err != nil {
		t.Fatal(err)
	}
	result, err := NormalizeCandidates(&article, []Candidate{testCandidate("woord", KindWord, 0, 0, "word", true)})
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.ReplaceAnnotations(context.Background(), article.ID, result.Annotations); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkProcessing(context.Background(), article.ID); err != nil {
		t.Fatal(err)
	}
	var typed *Error
	if err := articles.MarkProcessing(context.Background(), article.ID); !errors.As(err, &typed) || typed.Kind != KindInProgress {
		t.Fatalf("second processing error = %v", err)
	}
	if err := articles.MarkFailed(context.Background(), article.ID, "v1.enrichment_provider_failure"); err != nil {
		t.Fatal(err)
	}
	got, err := articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnrichmentStatus != StatusFailed || got.EnrichmentErrorCode != "v1.enrichment_provider_failure" || len(got.Blocks[0].Annotations) != 1 {
		t.Fatalf("failed article = %+v", got)
	}
	if err := articles.MarkProcessing(context.Background(), article.ID); err != nil {
		t.Fatal(err)
	}
	if err := articles.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnrichmentStatus != StatusFailed || got.EnrichmentErrorCode != "v1.enrichment_interrupted" || len(got.Blocks[0].Annotations) != 1 {
		t.Fatalf("recovered article = %+v", got)
	}
}

func TestStoreRecoveryFinalizesInterruptedAnalysisRunAndPreservesProgress(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := NewStore(db)
	article := testArticle(t, "Een analyse.")
	if err := articles.CreateArticle(ctx, &article); err != nil {
		t.Fatal(err)
	}
	jobID := library.NewULID()
	if _, err := db.Exec(ctx, `UPDATE article SET analysis_status = 'queued', analysis_job_id = ? WHERE id = ?`, jobID.String(), article.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkAnalysisProcessing(ctx, article.ID, jobID); err != nil {
		t.Fatal(err)
	}
	runID := library.NewULID()
	startedAt := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec(ctx, `
		INSERT INTO analysis_run (
			id, article_id, job_id, attempt_count, content_hash, contract_version,
			prompt_version, requested_model, requested_effort, provider_id,
			started_at, status, total_paragraphs, completed_paragraphs,
			failed_block_index, reported_model, stderr_excerpt
		) VALUES (?, ?, ?, 1, 'hash', 'contract', 'prompt', 'model', 'low', 'provider', ?, 'running', 3, 2, 1, 'reported', 'stderr')
	`, runID.String(), article.ID.String(), library.NewULID().String(), startedAt); err != nil {
		t.Fatal(err)
	}

	if err := articles.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	var status, completedAt, errorCode, errorDetail, reportedModel, stderrExcerpt string
	var durationMS, completedParagraphs, failedBlockIndex int
	if err := db.QueryRow(ctx, `
		SELECT status, completed_at, duration_ms, completed_paragraphs,
			failed_block_index, error_code, error_detail, reported_model, stderr_excerpt
		FROM analysis_run WHERE id = ?
	`, runID.String()).Scan(&status, &completedAt, &durationMS, &completedParagraphs, &failedBlockIndex, &errorCode, &errorDetail, &reportedModel, &stderrExcerpt); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || completedAt == "" || durationMS <= 0 || completedParagraphs != 2 || failedBlockIndex != 1 || errorCode != "v1.analysis_interrupted" || errorDetail == "" || reportedModel != "reported" || stderrExcerpt != "stderr" {
		t.Fatalf("recovered analysis run = status=%q completed_at=%q duration=%d completed=%d failed_block=%d code=%q detail=%q model=%q stderr=%q", status, completedAt, durationMS, completedParagraphs, failedBlockIndex, errorCode, errorDetail, reportedModel, stderrExcerpt)
	}
	got, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AnalysisStatus != AnalysisQueued || got.AnalysisErrorCode != "v1.analysis_interrupted" {
		t.Fatalf("recovered article analysis = %q/%q", got.AnalysisStatus, got.AnalysisErrorCode)
	}
}

func TestStoreReplaceRejectsMismatchedSpanWithoutDeletingGoodSet(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := NewStore(db)
	article := testArticle(t, "Een woord.")
	if err := articles.CreateArticle(context.Background(), &article); err != nil {
		t.Fatal(err)
	}
	result, err := NormalizeCandidates(&article, []Candidate{testCandidate("woord", KindWord, 0, 0, "word", true)})
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.ReplaceAnnotations(context.Background(), article.ID, result.Annotations); err != nil {
		t.Fatal(err)
	}
	bad := result.Annotations
	bad[0].StartUTF16 = 0
	if err := articles.ReplaceAnnotations(context.Background(), article.ID, bad); err == nil {
		t.Fatal("mismatched span accepted")
	}
	got, err := articles.GetArticle(context.Background(), article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks[0].Annotations) != 1 || got.EnrichmentStatus != StatusReady {
		t.Fatalf("previous set not preserved: %+v", got)
	}
}

func TestArticleSummaryNewestFirst(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := NewStore(db)
	for index, title := range []string{"first", "second"} {
		article, err := NewArticle(title, "tekst", "nl", "en")
		if err != nil {
			t.Fatal(err)
		}
		if err := articles.CreateArticle(context.Background(), &article); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	list, err := articles.ListArticles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "second" {
		t.Fatalf("newest-first list = %+v", list)
	}

	if _, err := articles.GetArticle(context.Background(), library.ULID("01ARZ3NDEKTSV4RRFFQ69G5FAV")); err == nil {
		t.Fatal("unknown article unexpectedly found")
	}
}

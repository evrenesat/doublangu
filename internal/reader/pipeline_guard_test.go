package reader

import (
	"context"
	"errors"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

// TestMarkAnalysisFailedForJobSupersedeGuard proves that only the active job
// may move an article to failed: a superseded run receives a conflict and
// cannot overwrite the state of the newer run.
func TestMarkAnalysisFailedForJobSupersedeGuard(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := NewStore(db)
	article, err := NewArticle("Guard", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &article, profileSnapshotFixture(t)); err != nil {
		t.Fatal(err)
	}
	firstJobID := article.AnalysisJobID
	if firstJobID == "" {
		t.Fatal("no job queued for the pipeline article")
	}
	// The active job may fail the article.
	if err := articles.MarkAnalysisFailedForJob(ctx, article.ID, mustULID(t, firstJobID), "v1.analysis_stage_failed"); err != nil {
		t.Fatalf("active job failure: %v", err)
	}
	loaded, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != AnalysisFailed || loaded.AnalysisErrorCode != "v1.analysis_stage_failed" {
		t.Fatalf("article state = %q / %q", loaded.AnalysisStatus, loaded.AnalysisErrorCode)
	}
	// A newer explicit fresh run takes over the article with a distinct job.
	if _, err := articles.QueueAnalysisWithProfile(ctx, article.ID, false, true, profileSnapshotFixture(t)); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	requeued, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondJobID := requeued.AnalysisJobID
	if secondJobID == "" || secondJobID == firstJobID {
		t.Fatalf("second job = %q (first %q)", secondJobID, firstJobID)
	}
	if requeued.AnalysisStatus != AnalysisQueued {
		t.Fatalf("requeued status = %q", requeued.AnalysisStatus)
	}
	// The superseded job cannot fail the newer run's article.
	err = articles.MarkAnalysisFailedForJob(ctx, article.ID, mustULID(t, firstJobID), "v1.analysis_stage_failed")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindConflict {
		t.Fatalf("superseded failure error = %v, want conflict", err)
	}
	after, err := articles.GetArticle(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AnalysisStatus != AnalysisQueued || after.AnalysisJobID != secondJobID {
		t.Fatalf("superseded run changed article state: %+v", after)
	}
}

func mustULID(t *testing.T, value string) library.ULID {
	t.Helper()
	id, err := library.ParseULID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

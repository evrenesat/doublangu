package analysis_test

import (
	"context"
	"errors"
	"testing"

	"doublangu/internal/analysis"
	"doublangu/internal/library"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

// TestListRunsFilteredByOperation proves the checkpoint-11 list contract:
// per-operation filtering, operation/subject/phase exposure on summaries,
// and rejection of unknown operations.
func TestListRunsFilteredByOperation(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	article, err := reader.NewArticle("Operations", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	history := analysis.NewHistoryStore(db)
	start := func(operation, subjectID, subjectLabel string) analysis.Run {
		run, err := history.StartRun(ctx, analysis.RunStart{
			ArticleID: article.ID, ArticleTitle: article.Title, JobID: library.NewULID(),
			AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
			PromptVersion: "prompt", RequestedModel: "model", RequestedEffort: "low",
			ProviderID: "provider", TotalParagraphs: 1,
			OperationType: operation, SubjectID: subjectID, SubjectLabel: subjectLabel,
		})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	articleRun := start("", "", "")
	exploreRun := start("explore", "entry-1", "huis")
	sentenceRun := start("sentence_translation", "sent-1", "Een zin.")

	// The unfiltered list retains legacy behavior and exposes the new fields.
	page, err := history.ListRuns(ctx, article.ID.String(), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 3 {
		t.Fatalf("unfiltered runs = %d, want 3", len(page.Runs))
	}
	byID := make(map[string]analysis.RunSummary, len(page.Runs))
	for _, summary := range page.Runs {
		byID[summary.ID.String()] = summary
	}
	if got := byID[articleRun.ID.String()]; got.OperationType != "article_analysis" || got.SubjectID != "" || got.SubjectLabel != "" || got.Phase != "running" {
		t.Fatalf("article summary = %+v", got)
	}
	if got := byID[exploreRun.ID.String()]; got.OperationType != "explore" || got.SubjectID != "entry-1" || got.SubjectLabel != "huis" {
		t.Fatalf("explore summary = %+v", got)
	}
	if got := byID[sentenceRun.ID.String()]; got.OperationType != "sentence_translation" || got.SubjectID != "sent-1" || got.SubjectLabel != "Een zin." {
		t.Fatalf("sentence summary = %+v", got)
	}

	// Each operation filter selects exactly its own runs.
	for operation, wantID := range map[string]string{
		"article_analysis":     articleRun.ID.String(),
		"explore":              exploreRun.ID.String(),
		"sentence_translation": sentenceRun.ID.String(),
	} {
		filtered, err := history.ListRunsFiltered(ctx, article.ID.String(), operation, 20, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(filtered.Runs) != 1 || filtered.Runs[0].ID.String() != wantID {
			t.Fatalf("operation %q runs = %+v", operation, filtered.Runs)
		}
	}

	// Unknown operations fail as caller errors, not database failures.
	if _, err := history.ListRunsFiltered(ctx, article.ID.String(), "comparison", 20, ""); !errors.Is(err, analysis.ErrInvalidRunQuery) {
		t.Fatalf("unknown operation error = %v", err)
	}
}

// TestGetRunExposesOperationAndErrorPhase proves run detail carries the
// operation/subject/phase labels and the per-attempt error phase.
func TestGetRunExposesOperationAndErrorPhase(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	article, err := reader.NewArticle("Detail", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	history := analysis.NewHistoryStore(db)
	run, err := history.StartRun(ctx, analysis.RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: library.NewULID(),
		AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
		PromptVersion: "prompt", RequestedModel: "model", RequestedEffort: "low",
		ProviderID: "provider", TotalParagraphs: 1,
		OperationType: "sentence_translation", SubjectID: "sent-9", SubjectLabel: "Een zin.",
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := history.StartStageAttempt(ctx, analysis.StageAttempt{
		RunID: run.ID.String(), BlockIndex: 0, StageID: "translation",
		ProviderID: "provider", ModelID: "model",
		ContractVersion: "contract", PromptVersion: "prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.FinishStageAttempt(ctx, attempt.ID, analysis.StageAttemptFinish{
		Status: "failed", ErrorCode: "v1.analysis_provider_unavailable", ErrorPhase: "provider",
		ErrorDetail: "safe detail",
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := history.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.OperationType != "sentence_translation" || detail.SubjectID != "sent-9" || detail.SubjectLabel != "Een zin." || detail.Phase != "running" {
		t.Fatalf("run detail labels = %q/%q/%q/%q", detail.OperationType, detail.SubjectID, detail.SubjectLabel, detail.Phase)
	}
	if len(detail.StageAttempts) != 1 || detail.StageAttempts[0].ErrorPhase != "provider" {
		t.Fatalf("run detail attempts = %+v", detail.StageAttempts)
	}
}

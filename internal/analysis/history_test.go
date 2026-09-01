package analysis

import (
	"context"
	"database/sql"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

func TestSettingsSeedIsWriteOnceAndHistoryRetainsTurnArtifacts(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	settings := NewSettingsStore(db)
	if err := settings.Seed(ctx, "model-one", "low"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Seed(ctx, "model-two", "high"); err != nil {
		t.Fatal(err)
	}
	got, err := settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "model-one" || got.Effort != "low" {
		t.Fatalf("seeded settings = %+v", got)
	}
	if _, err := settings.Save(ctx, "model-two", "high"); err != nil {
		t.Fatal(err)
	}
	got, err = settings.Get(ctx)
	if err != nil || got.Model != "model-two" || got.Effort != "high" {
		t.Fatalf("saved settings = %+v err=%v", got, err)
	}

	article, err := reader.NewArticle("History", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticle(ctx, &article); err != nil {
		t.Fatal(err)
	}
	jobID := library.NewULID()
	run, err := NewHistoryStore(db).StartRun(ctx, RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: jobID,
		AttemptCount: 1, ContentHash: "content", ContractVersion: "contract",
		PromptVersion: "prompt", RequestedModel: "model-two", RequestedEffort: "high",
		ProviderID: "provider", CodexCLIVersion: "codex 1", TotalParagraphs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn := Turn{
		RunID: run.ID, BlockIndex: 0, TurnIndex: 0, TurnKind: "initial",
		Prompt: "quoted <prompt>", OutputSchema: `{"type":"object"}`,
		CompletedResponse: `{"version":"response"}`, ResponseHash: "hash",
		CompletionMetadataJSON: `{"model":"model-two"}`, StartedAt: run.StartedAt,
		CompletedAt: run.StartedAt, Status: "completed",
	}
	if err := NewHistoryStore(db).AppendTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := NewHistoryStore(db).UpdateProgress(ctx, run.ID, 1, -1); err != nil {
		t.Fatal(err)
	}
	if err := NewHistoryStore(db).FinishRun(ctx, run.ID, RunFinish{
		Status: "failed", ReportedModel: "reported", DurationMS: 42,
		CompletedParags: 1, FailedBlockIndex: 1, ErrorCode: "v1.failure",
		ErrorDetail: "exact validation detail", StderrExcerpt: "bounded stderr",
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := NewHistoryStore(db).GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "failed" || detail.ReportedModel != "reported" || detail.CompletedParagraphs != 1 || detail.FailedBlockIndex != 1 || detail.ErrorDetail == "" || len(detail.Turns) != 1 || detail.Turns[0].Prompt != turn.Prompt {
		t.Fatalf("run detail = %+v", detail)
	}
	page, err := NewHistoryStore(db).ListRuns(ctx, article.ID.String(), 20, "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ErrorCode != "v1.failure" {
		t.Fatalf("run page = %+v err=%v", page, err)
	}

	if _, err := db.Exec(ctx, `DELETE FROM article WHERE id = ?`, article.ID.String()); err != nil {
		t.Fatal(err)
	}
	var runs, turns int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_run`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_turn`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || turns != 0 {
		t.Fatalf("diagnostic cascade left runs=%d turns=%d", runs, turns)
	}
	if _, err := NewHistoryStore(db).GetRun(ctx, run.ID); err != sql.ErrNoRows {
		t.Fatalf("deleted run error = %v, want sql.ErrNoRows", err)
	}
}

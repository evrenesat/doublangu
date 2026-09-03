package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"doublangu/internal/analysis"
	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

func TestAnalysisHTTPHistory(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	h := httpapi.NewAnalysisHandler(db)

	article, err := reader.NewArticle("History", "Een zin.\n\nNog een.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.NewStore(db).CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	history := analysis.NewHistoryStore(db)
	run, err := history.StartRun(ctx, analysis.RunStart{
		ArticleID: article.ID, ArticleTitle: article.Title, JobID: library.NewULID(), AttemptCount: 1,
		ContentHash: article.ContentHash, ContractVersion: semantics.AnalysisContractVersion,
		PromptVersion: semantics.PromptVersion, RequestedModel: "gpt-visible", RequestedEffort: "low",
		ProviderID: "codex.appserver", TotalParagraphs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.AppendTurn(ctx, analysis.Turn{
		RunID: run.ID, BlockIndex: 0, TurnIndex: 0, TurnKind: "initial", Prompt: "quoted source",
		OutputSchema: "{}", CompletedResponse: `{"version":"bad"}`, ValidationError: "invalid response",
		CompletionMetadataJSON: `{}`, StartedAt: store.NowUTC(), Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := history.FinishRun(ctx, run.ID, analysis.RunFinish{
		Status: "failed", ReportedModel: "gpt-visible", CompletedParags: 0, FailedBlockIndex: 0,
		ErrorCode: "v1.annotator_invalid_output", ErrorDetail: "private diagnostic",
	}); err != nil {
		t.Fatal(err)
	}

	runsResponse := httptest.NewRecorder()
	h.ServeRuns(runsResponse, authedRequest(http.MethodGet, "/api/v1/analysis/runs?article_id="+article.ID.String(), ""))
	if runsResponse.Code != http.StatusOK || runsResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runs response = %d/%q", runsResponse.Code, runsResponse.Header().Get("Cache-Control"))
	}
	runs := decodeJSON[analysis.RunsPage](t, runsResponse.Body.String())
	if len(runs.Runs) != 1 || runs.Runs[0].Status != "failed" || runs.Runs[0].FailedBlockIndex != 0 {
		t.Fatalf("run summaries = %+v", runs)
	}

	detailResponse := httptest.NewRecorder()
	detailRequest := authedRequest(http.MethodGet, "/api/v1/analysis/runs/"+run.ID.String(), "")
	detailRequest.SetPathValue("id", run.ID.String())
	h.ServeRun(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK || detailResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("run detail response = %d/%q", detailResponse.Code, detailResponse.Header().Get("Cache-Control"))
	}
	detail := decodeJSON[analysis.Run](t, detailResponse.Body.String())
	if len(detail.Turns) != 1 || detail.Turns[0].Prompt != "quoted source" || detail.ErrorDetail != "private diagnostic" {
		t.Fatalf("run detail = %+v", detail)
	}
}

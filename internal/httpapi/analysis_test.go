package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

type fakeAnalysisModelCatalog struct {
	mu     sync.Mutex
	models []annotator.Model
	err    error
	calls  int
}

func (f *fakeAnalysisModelCatalog) ListModels(context.Context) ([]annotator.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]annotator.Model(nil), f.models...), nil
}

func (f *fakeAnalysisModelCatalog) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeAnalysisModelCatalog) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestAnalysisHTTPModelsSettingsAndHistory(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	provider := &fakeAnalysisModelCatalog{models: []annotator.Model{
		{ID: "gpt-visible", DisplayName: "Visible", IsDefault: true, SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "low"}, {Value: "medium"}}},
		{ID: "gpt-hidden", DisplayName: "Hidden", Hidden: true, SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "high"}}},
	}}
	h := httpapi.NewAnalysisHandler(db, allowArticleCSRF{}, provider)

	modelsResponse := httptest.NewRecorder()
	h.ServeModels(modelsResponse, authedRequest(http.MethodGet, "/api/v1/analysis/models", ""))
	if modelsResponse.Code != http.StatusOK || modelsResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("models response = %d/%q", modelsResponse.Code, modelsResponse.Header().Get("Cache-Control"))
	}
	models := decodeJSON[httpapi.AnalysisModelsResponse](t, modelsResponse.Body.String())
	if len(models.Models) != 2 || !models.Models[1].Hidden || !models.Models[0].IsDefault || provider.callCount() != 1 {
		t.Fatalf("model catalog = %+v calls=%d", models, provider.callCount())
	}

	cachedResponse := httptest.NewRecorder()
	h.ServeModels(cachedResponse, authedRequest(http.MethodGet, "/api/v1/analysis/models", ""))
	if cachedResponse.Code != http.StatusOK || provider.callCount() != 1 {
		t.Fatalf("cached model catalog = %d calls=%d", cachedResponse.Code, provider.callCount())
	}

	provider.setError(errors.New("catalog transport failed"))
	staleResponse := httptest.NewRecorder()
	h.ServeModels(staleResponse, authedRequest(http.MethodGet, "/api/v1/analysis/models?refresh=true", ""))
	if staleResponse.Code != http.StatusOK || staleResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("stale catalog response = %d", staleResponse.Code)
	}
	stale := decodeJSON[httpapi.AnalysisModelsResponse](t, staleResponse.Body.String())
	if !stale.Stale || !strings.Contains(stale.LastError, "catalog transport failed") || len(stale.Models) != 2 {
		t.Fatalf("stale catalog = %+v", stale)
	}

	invalidSelection := httptest.NewRecorder()
	h.ServeSettings(invalidSelection, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"model":"missing","effort":"low"}`))
	if invalidSelection.Code != http.StatusBadRequest || decodeAPIError(t, invalidSelection.Body.String()).Code != httpapi.ErrCodeAnalysisInvalidSelection {
		t.Fatalf("invalid selection = %d %s", invalidSelection.Code, invalidSelection.Body.String())
	}
	validSelection := httptest.NewRecorder()
	h.ServeSettings(validSelection, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"model":"gpt-visible","effort":"low"}`))
	if validSelection.Code != http.StatusOK || validSelection.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("valid selection = %d/%q", validSelection.Code, validSelection.Header().Get("Cache-Control"))
	}
	settings := decodeJSON[analysis.Settings](t, validSelection.Body.String())
	if settings.Model != "gpt-visible" || settings.Effort != "low" {
		t.Fatalf("saved settings = %+v", settings)
	}

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

func TestAnalysisHTTPFailsClosedWithoutCatalogAndRequiresCSRF(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := httpapi.NewAnalysisHandler(db, &testCSRF{shouldError: true}, nil)

	models := httptest.NewRecorder()
	h.ServeModels(models, authedRequest(http.MethodGet, "/api/v1/analysis/models", ""))
	if models.Code != http.StatusServiceUnavailable || models.Header().Get("Cache-Control") != "no-store" || decodeAPIError(t, models.Body.String()).Code != httpapi.ErrCodeAnalysisModelUnavailable {
		t.Fatalf("unavailable catalog = %d/%s", models.Code, models.Body.String())
	}

	settings := httptest.NewRecorder()
	h.ServeSettings(settings, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{}`))
	if settings.Code != http.StatusForbidden || settings.Header().Get("Cache-Control") != "no-store" || decodeAPIError(t, settings.Body.String()).Code != httpapi.ErrCodeCSRF {
		t.Fatalf("settings csrf = %d/%s", settings.Code, settings.Body.String())
	}
}

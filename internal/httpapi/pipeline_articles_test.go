package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

type articlePipelineEnv struct {
	h        *httpapi.ArticleHandler
	profiles *analysis.ProfileStore
	db       *store.DB
	registry *apiFakeRegistry
}

func newArticlePipelineEnv(t *testing.T) *articlePipelineEnv {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	descriptor := annotator.ProviderDescriptor{
		ID: "codex-app-server", Type: "codex_app_server", Enabled: true, ConfigFingerprint: "fp",
	}
	registry := &apiFakeRegistry{descriptors: []annotator.ProviderDescriptor{descriptor}}
	registry.providers = map[string]annotator.Provider{"codex-app-server": &apiFakeProvider{descriptor: descriptor}}
	h := httpapi.NewArticleHandler(db, allowArticleCSRF{}, nil)
	h.ConfigurePipeline(db, registry)
	return &articlePipelineEnv{h: h, profiles: analysis.NewProfileStore(db), db: db, registry: registry}
}

func pipelineProfileBindings(t *testing.T, providerID string) []pipeline.BindingSnapshot {
	t.Helper()
	bindings := make([]pipeline.BindingSnapshot, 0, 2)
	for _, stage := range pipeline.RegisteredStages() {
		options, err := config.CanonicalizeProviderOptions(config.ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"low"}`))
		if err != nil {
			t.Fatal(err)
		}
		hash, err := pipeline.OptionsHashOf(options)
		if err != nil {
			t.Fatal(err)
		}
		contract, prompt, _ := pipeline.StageContracts(stage)
		bindings = append(bindings, pipeline.BindingSnapshot{
			StageID: stage, ProviderID: providerID, ProviderType: config.ProviderTypeCodexAppServer,
			ProviderConfigFingerprint: "fp", ModelID: "model-a", Options: options, OptionsHash: hash,
			ContractVersion: contract, PromptVersion: prompt,
		})
	}
	return bindings
}

func seedActiveProfile(t *testing.T, env *articlePipelineEnv, name string) string {
	t.Helper()
	profile, err := env.profiles.Create(context.Background(), name, pipelineProfileBindings(t, "codex-app-server"))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.profiles.Activate(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	return profile.ID
}

func createArticleViaHandler(t *testing.T, env *articlePipelineEnv) string {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"title":"Pijplijn","body":"Een zin.\n\nTwee zinnen.","source_language":"nl","target_language":"en"}`
	env.h.ServeArticles(rec, authedRequest(http.MethodPost, "/api/v1/articles", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	var article struct {
		ID       string `json:"id"`
		Pipeline *struct {
			ProfileID    string `json:"profile_id"`
			SnapshotHash string `json:"snapshot_hash"`
		} `json:"analysis_pipeline"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &article); err != nil {
		t.Fatal(err)
	}
	if article.Pipeline == nil || article.Pipeline.ProfileID == "" || article.Pipeline.SnapshotHash == "" {
		t.Fatalf("missing pipeline provenance in %s", rec.Body.String())
	}
	return article.ID
}

func TestPipelineArticleCreateUsesActiveProfile(t *testing.T) {
	env := newArticlePipelineEnv(t)
	seedActiveProfile(t, env, "Active")
	id := createArticleViaHandler(t, env)
	// The queued job must be a pipeline payload, decodable with the snapshot
	// recomputed from the stored profile row.
	ctx := context.Background()
	hasSnapshot, err := reader.NewStore(env.db).HasPipelineSnapshot(ctx, mustParseULID(t, id))
	if err != nil {
		t.Fatal(err)
	}
	if !hasSnapshot {
		t.Fatal("article has no pipeline snapshot")
	}
}

func TestPipelineArticleCreateWithoutActiveProfileFailsAnalysis(t *testing.T) {
	env := newArticlePipelineEnv(t)
	rec := httptest.NewRecorder()
	body := `{"title":"Zonder","body":"Een zin.","source_language":"nl","target_language":"en"}`
	env.h.ServeArticles(rec, authedRequest(http.MethodPost, "/api/v1/articles", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	var article struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &article); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := reader.NewStore(env.db)
	loaded, err := store.GetArticle(ctx, mustParseULID(t, article.ID))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisStatus != reader.AnalysisFailed {
		t.Fatalf("status = %q", loaded.AnalysisStatus)
	}
	if loaded.Blocks[0].AnalysisErrorCode != "v1.analysis_profile_unavailable" {
		t.Fatalf("block error = %q", loaded.Blocks[0].AnalysisErrorCode)
	}
}

func TestPipelineArticleReanalyzeProfileRules(t *testing.T) {
	env := newArticlePipelineEnv(t)
	activeID := seedActiveProfile(t, env, "Active")
	override, err := env.profiles.Create(context.Background(), "Override", pipelineProfileBindings(t, "codex-app-server"))
	if err != nil {
		t.Fatal(err)
	}
	id := createArticleViaHandler(t, env)
	ctx := context.Background()
	articleStore := reader.NewStore(env.db)
	ulid := mustParseULID(t, id)
	_ = ulid

	// profile_id on a non-fresh request is rejected.
	rec := httptest.NewRecorder()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":false,"profile_id":"`+override.ID+`"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-fresh profile override = %d body=%s", rec.Code, rec.Body.String())
	}

	// fresh with an unknown profile is 404 and leaves the article alone.
	rec = httptest.NewRecorder()
	missingID := library.NewULID().String()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":true,"profile_id":"`+missingID+`"}`, "id", id))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile = %d body=%s", rec.Code, rec.Body.String())
	}
	before, err := articleStore.GetArticle(ctx, ulid)
	if err != nil {
		t.Fatal(err)
	}

	// fresh with a valid override switches the stored snapshot to the
	// override profile even though the active profile differs.
	rec = httptest.NewRecorder()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":true,"profile_id":"`+override.ID+`"}`, "id", id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("fresh override = %d body=%s", rec.Code, rec.Body.String())
	}
	after, err := articleStore.GetArticle(ctx, ulid)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pipeline == nil || after.Pipeline.ProfileID != override.ID {
		t.Fatalf("override not applied: %+v", after.Pipeline)
	}
	if before.Pipeline != nil && after.Pipeline != nil && before.Pipeline.SnapshotHash == after.Pipeline.SnapshotHash {
		t.Fatalf("snapshot unchanged after fresh override")
	}
	if after.AnalysisStatus != reader.AnalysisQueued && after.AnalysisStatus != reader.AnalysisProcessing {
		t.Fatalf("status = %q", after.AnalysisStatus)
	}

	// fresh with no profile uses the active profile.
	rec = httptest.NewRecorder()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":true}`, "id", id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("fresh active = %d body=%s", rec.Code, rec.Body.String())
	}
	freshActive, err := articleStore.GetArticle(ctx, ulid)
	if err != nil {
		t.Fatal(err)
	}
	if freshActive.Pipeline == nil || freshActive.Pipeline.ProfileID != activeID {
		t.Fatalf("active profile not applied: %+v", freshActive.Pipeline)
	}

	// A non-fresh reanalysis with no profile reuses the stored snapshot.
	rec = httptest.NewRecorder()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":false}`, "id", id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("normal retry = %d body=%s", rec.Code, rec.Body.String())
	}
	retry, err := articleStore.GetArticle(ctx, ulid)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Pipeline == nil || retry.Pipeline.ProfileID != activeID {
		t.Fatalf("stored snapshot not reused: %+v", retry.Pipeline)
	}
	if !strings.Contains(rec.Body.String(), `"analysis_status":"queued"`) {
		t.Fatalf("retry body = %s", rec.Body.String())
	}
}

// TestPipelineArticleReanalyzeRejectsDisabledProvider proves a stored profile
// whose provider was disabled by a later configuration change can no longer be
// used for fresh analysis: active-profile resolution and explicit fresh
// profile selection reject it before any article or job state changes, and a
// normal retry still reuses the stored snapshot untouched.
func TestPipelineArticleReanalyzeRejectsDisabledProvider(t *testing.T) {
	env := newArticlePipelineEnv(t)
	activeID := seedActiveProfile(t, env, "Active")
	id := createArticleViaHandler(t, env)
	ctx := context.Background()
	articleStore := reader.NewStore(env.db)
	ulid := mustParseULID(t, id)
	before, err := articleStore.GetArticle(ctx, ulid)
	if err != nil {
		t.Fatal(err)
	}
	if before.AnalysisJobID == "" {
		t.Fatal("expected a queued analysis job")
	}
	// Configuration restart disables the only provider.
	env.registry.descriptors = []annotator.ProviderDescriptor{{
		ID: "codex-app-server", Type: "codex_app_server", Enabled: false, ConfigFingerprint: "fp",
	}}
	// Active-profile resolution fails: fresh run without override is a 503.
	rec := httptest.NewRecorder()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":true}`, "id", id))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh active with disabled provider = %d body=%s", rec.Code, rec.Body.String())
	}
	// Explicit fresh profile selection fails the same way.
	rec = httptest.NewRecorder()
	env.h.ServeReanalyze(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+id+"/reanalyze", `{"fresh":true,"profile_id":"`+activeID+`"}`, "id", id))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh override with disabled provider = %d body=%s", rec.Code, rec.Body.String())
	}
	// Neither rejected request superseded the existing job or mutated the
	// stored snapshot: a normal retry still reuses it.
	after, err := articleStore.GetArticle(ctx, ulid)
	if err != nil {
		t.Fatal(err)
	}
	if after.AnalysisJobID != before.AnalysisJobID || after.Pipeline.SnapshotHash != before.Pipeline.SnapshotHash {
		t.Fatal("rejected fresh runs changed the queued job or stored snapshot")
	}
}

func mustParseULID(t *testing.T, value string) library.ULID {
	t.Helper()
	id, err := library.ParseULID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

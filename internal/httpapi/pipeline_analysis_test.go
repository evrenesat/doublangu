package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/httpapi"
	"doublangu/internal/pipeline"
	"doublangu/internal/store"
)

type apiFakeRegistry struct {
	descriptors []annotator.ProviderDescriptor
	providers   map[string]annotator.Provider
}

func (r *apiFakeRegistry) Provider(id string) (annotator.Provider, bool) {
	provider, ok := r.providers[id]
	return provider, ok
}

func (r *apiFakeRegistry) Descriptors() []annotator.ProviderDescriptor {
	return r.descriptors
}

func codexDescriptor() annotator.ProviderDescriptor {
	return annotator.ProviderDescriptor{
		ID: "codex-app-server", Label: "Codex", EndpointLabel: "Local Codex",
		Type: "codex_app_server", Enabled: true, ConfigFingerprint: "fp",
	}
}

func apiBinding(stage pipeline.StageID) pipeline.BindingSnapshot {
	options, _ := config.CanonicalizeProviderOptions(config.ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"low"}`))
	hash, _ := pipeline.OptionsHashOf(options)
	contract, prompt, _ := pipeline.StageContracts(stage)
	return pipeline.BindingSnapshot{
		StageID: stage, ProviderID: "codex-app-server", ProviderType: "codex_app_server",
		ProviderConfigFingerprint: "fp", ModelID: "model-a", Options: options, OptionsHash: hash,
		ContractVersion: contract, PromptVersion: prompt,
	}
}

func apiProfileBody() string {
	linguistic, _ := json.Marshal(apiBinding(pipeline.StageLinguisticAnalysis))
	translation, _ := json.Marshal(apiBinding(pipeline.StageTranslation))
	return `{"name":"API Profile","bindings":[` + string(linguistic) + `,` + string(translation) + `]}`
}

func TestPipelineAnalysisProfilesAndSettings(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor()},
		providers:   map[string]annotator.Provider{"codex-app-server": &apiFakeProvider{descriptor: codexDescriptor()}},
	}
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry)

	list := httptest.NewRecorder()
	h.ServeProfiles(list, authedRequest(http.MethodGet, "/api/v1/analysis/profiles", ""))
	if list.Code != http.StatusOK || list.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list = %d %q", list.Code, list.Header().Get("Cache-Control"))
	}
	if !strings.Contains(list.Body.String(), `"profiles":[]`) {
		t.Fatalf("empty list body = %s", list.Body.String())
	}

	created := httptest.NewRecorder()
	h.ServeProfiles(created, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", apiProfileBody()))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", created.Code, created.Body.String())
	}
	var profile profilePayload
	decodeJSONBody(t, created.Body.String(), &profile)
	if profile.ID == "" || len(profile.Bindings) != 2 {
		t.Fatalf("created profile = %+v", profile)
	}

	settings := httptest.NewRecorder()
	h.ServeProfileSettings(settings, authedRequest(http.MethodPut, "/api/v1/analysis/pipeline-settings", `{"active_profile_id":"`+profile.ID+`"}`))
	if settings.Code != http.StatusOK {
		t.Fatalf("activate = %d body=%s", settings.Code, settings.Body.String())
	}
	settings = httptest.NewRecorder()
	h.ServeProfileSettings(settings, authedRequest(http.MethodGet, "/api/v1/analysis/pipeline-settings", ""))
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), profile.ID) {
		t.Fatalf("settings get = %d body=%s", settings.Code, settings.Body.String())
	}
	// Deleting the active profile conflicts; an empty settings body is 400.
	deleted := httptest.NewRecorder()
	h.ServeProfile(deleted, authedRequest(http.MethodDelete, "/api/v1/analysis/profiles/"+profile.ID, ""))
	if deleted.Code != http.StatusConflict {
		t.Fatalf("active delete = %d body=%s", deleted.Code, deleted.Body.String())
	}
	empty := httptest.NewRecorder()
	h.ServeProfileSettings(empty, authedRequest(http.MethodPut, "/api/v1/analysis/pipeline-settings", `{}`))
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty settings body = %d", empty.Code)
	}

	// Provider and profile responses carry exactly the schema-declared
	// fields: no endpoint label, config fingerprint, request timeout, or
	// snapshot-only binding fields reach the browser.
	providers := httptest.NewRecorder()
	h.ServeProviders(providers, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
	if providers.Code != http.StatusOK {
		t.Fatalf("providers = %d body=%s", providers.Code, providers.Body.String())
	}
	var providerBody struct {
		Providers []map[string]any `json:"providers"`
	}
	decodeJSONBody(t, providers.Body.String(), &providerBody)
	if len(providerBody.Providers) != 1 {
		t.Fatalf("provider count = %d", len(providerBody.Providers))
	}
	for _, forbidden := range []string{"endpoint_label", "config_fingerprint", "request_timeout_ms"} {
		if _, present := providerBody.Providers[0][forbidden]; present {
			t.Fatalf("provider response leaked %q", forbidden)
		}
	}
	models := providerBody.Providers[0]["models"].([]any)
	model := models[0].(map[string]any)
	if model["id"] != "model-a" || model["display_name"] != "Model A" {
		t.Fatalf("provider model entry = %v", model)
	}
	for _, forbidden := range []string{"endpoint_label", "config_fingerprint", "request_timeout_ms", "hidden", "is_default"} {
		if _, present := model[forbidden]; present {
			t.Fatalf("provider model entry leaked %q", forbidden)
		}
	}
	efforts, ok := model["supported_reasoning_efforts"].([]any)
	if !ok || len(efforts) != 3 {
		t.Fatalf("provider model efforts = %v, want the three advertised efforts", model["supported_reasoning_efforts"])
	}
	profiles := httptest.NewRecorder()
	h.ServeProfiles(profiles, authedRequest(http.MethodGet, "/api/v1/analysis/profiles", ""))
	if profiles.Code != http.StatusOK {
		t.Fatalf("profiles = %d body=%s", profiles.Code, profiles.Body.String())
	}
	var profileBody struct {
		Profiles []profilePayload `json:"profiles"`
	}
	decodeJSONBody(t, profiles.Body.String(), &profileBody)
	if len(profileBody.Profiles) != 1 {
		t.Fatalf("profile count = %d", len(profileBody.Profiles))
	}
	var rawProfiles struct {
		Profiles []struct {
			Bindings []map[string]any `json:"bindings"`
		} `json:"profiles"`
	}
	decodeJSONBody(t, profiles.Body.String(), &rawProfiles)
	for _, binding := range rawProfiles.Profiles[0].Bindings {
		if len(binding) != 4 {
			t.Fatalf("binding entry = %v, want exactly stage_id/provider_id/model_id/options", binding)
		}
		for _, key := range []string{"stage_id", "provider_id", "model_id", "options"} {
			if _, present := binding[key]; !present {
				t.Fatalf("binding entry missing %q: %v", key, binding)
			}
		}
	}

	// A persisted profile referencing a provider that a configuration restart
	// disabled cannot be activated: the request is rejected before any
	// settings state changes.
	disabledDescriptor := codexDescriptor()
	disabledDescriptor.Enabled = false
	registry.descriptors = []annotator.ProviderDescriptor{disabledDescriptor}
	registry.providers = map[string]annotator.Provider{}
	settings = httptest.NewRecorder()
	h.ServeProfileSettings(settings, authedRequest(http.MethodPut, "/api/v1/analysis/pipeline-settings", `{"active_profile_id":"`+profile.ID+`"}`))
	if settings.Code != http.StatusConflict {
		t.Fatalf("activate disabled-provider profile = %d body=%s", settings.Code, settings.Body.String())
	}
}

func TestPipelineAnalysisCSRFAndUnknownProvider(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := &apiFakeRegistry{descriptors: []annotator.ProviderDescriptor{codexDescriptor()}}
	h := httpapi.NewPipelineAnalysisHandler(db, &testCSRF{shouldError: true}, registry)
	rec := httptest.NewRecorder()
	h.ServeProfiles(rec, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", apiProfileBody()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("csrf create = %d", rec.Code)
	}
	h = httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry)
	rec = httptest.NewRecorder()
	h.ServeProviderTest(rec, authedRequest(http.MethodPost, "/api/v1/analysis/providers/missing/test", `{"stage_id":"linguistic_analysis","model_id":"m","options":{"reasoning_effort":"low"}}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider test = %d", rec.Code)
	}
}

type apiFakeSession struct{}

func (s *apiFakeSession) Turn(_ context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	return annotator.Completion{Text: `{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`}, nil
}

func (s *apiFakeSession) Close() error { return nil }

type apiFakeProvider struct {
	descriptor annotator.ProviderDescriptor
	models     []annotator.Model
	listErr    error
}

func (p *apiFakeProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *apiFakeProvider) ListModels(context.Context) ([]annotator.Model, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	if p.models != nil {
		return p.models, nil
	}
	return []annotator.Model{{
		ID: "model-a", DisplayName: "Model A",
		SupportedReasoningEfforts: []annotator.ReasoningEffort{
			{Value: "low"}, {Value: "medium"}, {Value: "high"},
		},
	}}, nil
}
func (p *apiFakeProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return &apiFakeSession{}, nil
}

type profilePayload struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Bindings []pipeline.BindingSnapshot `json:"bindings"`
	IsActive bool                       `json:"is_active"`
}

func decodeJSONBody(t *testing.T, body string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), target); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

func profileBodyWith(providerID, modelID, stage string) string {
	return profileBodyWithEffort(providerID, modelID, stage, "low")
}

func profileBodyWithEffort(providerID, modelID, stage, effort string) string {
	binding, _ := json.Marshal(pipeline.BindingSnapshot{
		StageID: pipeline.StageID(stage), ProviderID: providerID, ModelID: modelID,
		ProviderType: "codex_app_server", ProviderConfigFingerprint: "fp",
		Options: json.RawMessage(`{"reasoning_effort":"` + effort + `"}`),
	})
	return `{"name":"Bound","bindings":[` + string(binding) + `]}`
}

func twoBindingBody(name, providerID, modelID string) string {
	return twoBindingBodyEffort(name, providerID, modelID, "low")
}

func twoBindingBodyEffort(name, providerID, modelID, effort string) string {
	bindings := make([]string, 0, 2)
	for _, stage := range pipeline.RegisteredStages() {
		binding, _ := json.Marshal(map[string]any{
			"stage_id": stage, "provider_id": providerID, "model_id": modelID,
			"provider_config_fingerprint": "fp",
			"options":                     map[string]any{"reasoning_effort": effort},
		})
		bindings = append(bindings, string(binding))
	}
	return `{"name":"` + name + `","bindings":[` + strings.Join(bindings, ",") + `]}`
}

func TestPipelineAnalysisRejectsUnusableBindings(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enabled := &apiFakeProvider{descriptor: codexDescriptor()}
	disabledDescriptor := codexDescriptor()
	disabledDescriptor.ID = "codex-disabled"
	disabledDescriptor.Enabled = false
	disabled := &apiFakeProvider{descriptor: disabledDescriptor}
	catalogDown := &apiFakeProvider{descriptor: codexDescriptor(), listErr: errors.New("catalog offline")}
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor(), disabledDescriptor},
		providers: map[string]annotator.Provider{
			"codex-app-server": enabled, "codex-disabled": disabled,
		},
	}
	catalog := httpapi.NewProviderCatalogService(registry)
	catalog.SetTTL(time.Millisecond)
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry, catalog)

	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"unknown provider", profileBodyWith("codex-missing", "model-a", "linguistic_analysis"), http.StatusBadRequest},
		{"disabled provider", profileBodyWith("codex-disabled", "model-a", "linguistic_analysis"), http.StatusBadRequest},
		{"unlisted model", profileBodyWith("codex-app-server", "ghost-model", "linguistic_analysis"), http.StatusBadRequest},
		{"unsupported effort", profileBodyWithEffort("codex-app-server", "model-a", "linguistic_analysis", "extreme"), http.StatusBadRequest},
	}
	for _, test := range cases {
		rec := httptest.NewRecorder()
		h.ServeProfiles(rec, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", test.body))
		if rec.Code != test.status {
			t.Fatalf("%s: status = %d body=%s", test.name, rec.Code, rec.Body.String())
		}
	}

	// A valid profile saves, caching the provider catalog.
	rec := httptest.NewRecorder()
	h.ServeProfiles(rec, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", twoBindingBody("Valid", "codex-app-server", "model-a")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid create = %d body=%s", rec.Code, rec.Body.String())
	}
	var created profilePayload
	decodeJSONBody(t, rec.Body.String(), &created)

	// With the catalog expired and the provider down, a changed tuple cannot
	// be verified against a stale last-good catalog: 503, never a silent save.
	time.Sleep(5 * time.Millisecond)
	registry.providers["codex-app-server"] = catalogDown
	rec = httptest.NewRecorder()
	h.ServeProfile(rec, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+created.ID, twoBindingBody("Changed", "codex-app-server", "model-b")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("changed-model with stale catalog = %d body=%s", rec.Code, rec.Body.String())
	}
	// An options-only change is also a changed tuple and needs the catalog.
	rec = httptest.NewRecorder()
	h.ServeProfile(rec, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+created.ID, twoBindingBodyEffort("EffortChange", "codex-app-server", "model-a", "high")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("options-only change with stale catalog = %d body=%s", rec.Code, rec.Body.String())
	}
	// A PUT that only renames the profile (unchanged provider/model/options
	// tuple) still succeeds while the catalog is stale or down.
	rec = httptest.NewRecorder()
	h.ServeProfile(rec, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+created.ID, twoBindingBody("Renamed", "codex-app-server", "model-a")))
	if rec.Code != http.StatusOK {
		t.Fatalf("unchanged-tuple rename = %d body=%s", rec.Code, rec.Body.String())
	}

	// Restored catalog: an unsupported-effort options change fails locally
	// against the refreshed model capabilities.
	registry.providers["codex-app-server"] = enabled
	time.Sleep(5 * time.Millisecond)
	rec = httptest.NewRecorder()
	h.ServeProfile(rec, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+created.ID, twoBindingBodyEffort("EffortChange", "codex-app-server", "model-a", "extreme")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported-effort options change = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestPipelineProviderListingRefreshAndStale covers the shared catalog
// semantics of the providers endpoint: refresh=true requires one valid
// provider_id, refresh targets only that provider, and a transient failure
// retains the last-good models with stale=true and a sanitized error instead
// of dropping them.
func TestPipelineProviderListingRefreshAndStale(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enabled := &apiFakeProvider{descriptor: codexDescriptor()}
	failing := &apiFakeProvider{descriptor: codexDescriptor(), listErr: errors.New("catalog offline")}
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor()},
		providers:   map[string]annotator.Provider{"codex-app-server": enabled},
	}
	catalog := httpapi.NewProviderCatalogService(registry)
	catalog.SetTTL(time.Millisecond)
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry, catalog)

	list := func(query string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers"+query, ""))
		if rec.Code != http.StatusOK {
			return rec.Code, nil
		}
		var body struct {
			Providers []map[string]any `json:"providers"`
		}
		decodeJSONBody(t, rec.Body.String(), &body)
		return rec.Code, body.Providers[0]
	}

	status, entry := list("")
	if status != http.StatusOK || entry["health"] != "healthy" || entry["stale"] != false {
		t.Fatalf("healthy listing = %d %v", status, entry)
	}
	if _, present := entry["last_error"]; present {
		t.Fatalf("healthy listing leaked last_error: %v", entry)
	}
	if _, present := entry["models"]; !present {
		t.Fatalf("healthy listing has no models: %v", entry)
	}

	// refresh=true requires a provider_id and a resolvable provider.
	rec := httptest.NewRecorder()
	h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers?refresh=true", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh without provider_id = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers?refresh=true&provider_id=ghost", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("refresh unknown provider = %d", rec.Code)
	}

	// A failing provider with an expired cache keeps the last-good models and
	// reports stale plus the sanitized error.
	time.Sleep(5 * time.Millisecond)
	registry.providers["codex-app-server"] = failing
	status, entry = list("")
	if status != http.StatusOK || entry["health"] != "unhealthy" || entry["stale"] != true {
		t.Fatalf("stale listing = %d %v", status, entry)
	}
	models, ok := entry["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("stale listing dropped last-good models: %v", entry)
	}
	if entry["last_error"] != "catalog offline" {
		t.Fatalf("stale listing error = %v", entry["last_error"])
	}

	// refresh=true targets only the named provider and clears the stale flag
	// once the provider recovers.
	registry.providers["codex-app-server"] = enabled
	time.Sleep(5 * time.Millisecond)
	status, entry = list("?refresh=true&provider_id=codex-app-server")
	if status != http.StatusOK || entry["health"] != "healthy" || entry["stale"] != false || entry["last_error"] != nil {
		t.Fatalf("refreshed listing = %d %v", status, entry)
	}
}

// TestPipelineActivationRequiresCurrentModelAndEffort proves activation
// revalidates stored bindings against the non-stale provider catalog: a
// profile whose model or reasoning effort was removed from the catalog after
// the profile was saved cannot be activated.
func TestPipelineActivationRequiresCurrentModelAndEffort(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := &apiFakeProvider{descriptor: codexDescriptor()}
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor()},
		providers:   map[string]annotator.Provider{"codex-app-server": provider},
	}
	catalog := httpapi.NewProviderCatalogService(registry)
	catalog.SetTTL(time.Millisecond)
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry, catalog)

	rec := httptest.NewRecorder()
	h.ServeProfiles(rec, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", twoBindingBody("Active", "codex-app-server", "model-a")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	var created profilePayload
	decodeJSONBody(t, rec.Body.String(), &created)
	rec = httptest.NewRecorder()
	h.ServeProfileSettings(rec, authedRequest(http.MethodPut, "/api/v1/analysis/pipeline-settings", `{"active_profile_id":"`+created.ID+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("initial activate = %d body=%s", rec.Code, rec.Body.String())
	}

	// Catalog restart removes the model entirely: activation now conflicts.
	time.Sleep(5 * time.Millisecond)
	provider.models = []annotator.Model{{ID: "model-z", DisplayName: "Model Z", SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "low"}}}}
	rec = httptest.NewRecorder()
	h.ServeProfileSettings(rec, authedRequest(http.MethodPut, "/api/v1/analysis/pipeline-settings", `{"active_profile_id":"`+created.ID+`"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("activate removed-model profile = %d body=%s", rec.Code, rec.Body.String())
	}

	// Catalog restart keeps the model but drops its low effort: also 409.
	time.Sleep(5 * time.Millisecond)
	provider.models = []annotator.Model{{ID: "model-a", DisplayName: "Model A", SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "minimal"}}}}
	rec = httptest.NewRecorder()
	h.ServeProfileSettings(rec, authedRequest(http.MethodPut, "/api/v1/analysis/pipeline-settings", `{"active_profile_id":"`+created.ID+`"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("activate removed-effort profile = %d body=%s", rec.Code, rec.Body.String())
	}
}

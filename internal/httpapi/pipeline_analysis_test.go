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
	h.ServeProfileSettings(settings, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"active_profile_id":"`+profile.ID+`"}`))
	if settings.Code != http.StatusOK {
		t.Fatalf("activate = %d body=%s", settings.Code, settings.Body.String())
	}
	settings = httptest.NewRecorder()
	h.ServeProfileSettings(settings, authedRequest(http.MethodGet, "/api/v1/analysis/settings", ""))
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
	h.ServeProfileSettings(empty, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{}`))
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty settings body = %d", empty.Code)
	}

	// Provider and profile responses carry exactly the schema-declared
	// fields: the sanitized endpoint label is owner-visible, but config
	// fingerprint, request timeout, and snapshot-only binding fields never
	// reach the browser.
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
	for _, forbidden := range []string{"config_fingerprint", "request_timeout_ms"} {
		if _, present := providerBody.Providers[0][forbidden]; present {
			t.Fatalf("provider response leaked %q", forbidden)
		}
	}
	if providerBody.Providers[0]["endpoint_label"] != "Local Codex" {
		t.Fatalf("provider endpoint_label = %v", providerBody.Providers[0]["endpoint_label"])
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
		if len(binding) != 5 {
			t.Fatalf("binding entry = %v, want exactly stage_id/provider_id/model_id/options/valid", binding)
		}
		for _, key := range []string{"stage_id", "provider_id", "model_id", "options", "valid"} {
			if _, present := binding[key]; !present {
				t.Fatalf("binding entry missing %q: %v", key, binding)
			}
		}
		if binding["valid"] != true {
			t.Fatalf("usable binding entry = %v, want valid", binding)
		}
		for _, forbidden := range []string{"provider_type", "provider_config_fingerprint", "options_hash", "contract_version", "prompt_version"} {
			if _, present := binding[forbidden]; present {
				t.Fatalf("binding entry leaked %q: %v", forbidden, binding)
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
	h.ServeProfileSettings(settings, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"active_profile_id":"`+profile.ID+`"}`))
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
	h.ServeProfileSettings(rec, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"active_profile_id":"`+created.ID+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("initial activate = %d body=%s", rec.Code, rec.Body.String())
	}

	// Catalog restart removes the model entirely: activation now conflicts.
	time.Sleep(5 * time.Millisecond)
	provider.models = []annotator.Model{{ID: "model-z", DisplayName: "Model Z", SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "low"}}}}
	rec = httptest.NewRecorder()
	h.ServeProfileSettings(rec, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"active_profile_id":"`+created.ID+`"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("activate removed-model profile = %d body=%s", rec.Code, rec.Body.String())
	}

	// Catalog restart keeps the model but drops its low effort: also 409.
	time.Sleep(5 * time.Millisecond)
	provider.models = []annotator.Model{{ID: "model-a", DisplayName: "Model A", SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "minimal"}}}}
	rec = httptest.NewRecorder()
	h.ServeProfileSettings(rec, authedRequest(http.MethodPut, "/api/v1/analysis/settings", `{"active_profile_id":"`+created.ID+`"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("activate removed-effort profile = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestPipelineConformanceRetentionPerTuple proves a provider test retains the
// latest in-memory result per provider/stage/model/options tuple and returns
// those summaries with the providers listing: distinct tuples accumulate,
// re-testing a tuple overwrites it, and no profile or database row is needed.
func TestPipelineConformanceRetentionPerTuple(t *testing.T) {
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

	runTest := func(stage, model, effort string) {
		t.Helper()
		body := `{"stage_id":"` + stage + `","model_id":"` + model + `","options":{"reasoning_effort":"` + effort + `"}}`
		rec := httptest.NewRecorder()
		h.ServeProviderTest(rec, authedRequestWithID(http.MethodPost, "/api/v1/analysis/providers/codex-app-server/test", body, "codex-app-server"))
		if rec.Code != http.StatusOK {
			t.Fatalf("test %s/%s/%s = %d body=%s", stage, model, effort, rec.Code, rec.Body.String())
		}
	}
	conformance := func() []map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("providers = %d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Providers []struct {
				ID          string           `json:"id"`
				Conformance []map[string]any `json:"conformance"`
			} `json:"providers"`
		}
		decodeJSONBody(t, rec.Body.String(), &body)
		if len(body.Providers) != 1 {
			t.Fatalf("providers = %v", body.Providers)
		}
		return body.Providers[0].Conformance
	}

	if got := conformance(); len(got) != 0 {
		t.Fatalf("untested provider conformance = %v", got)
	}
	runTest("linguistic_analysis", "model-a", "low")
	runTest("translation", "model-a", "low")
	got := conformance()
	if len(got) != 2 {
		t.Fatalf("two tuples conformance = %v", got)
	}
	stages := map[string]bool{}
	for _, entry := range got {
		stage, _ := entry["stage_id"].(string)
		checked, _ := entry["checked_at"].(string)
		status, _ := entry["status"].(string)
		stages[stage] = true
		if entry["model_id"] != "model-a" || checked == "" || status == "" {
			t.Fatalf("tuple entry = %v", entry)
		}
		if _, ok := entry["duration_ms"]; !ok {
			t.Fatalf("tuple entry missing duration_ms = %v", entry)
		}
	}
	if !stages["linguistic_analysis"] || !stages["translation"] {
		t.Fatalf("stages = %v", stages)
	}

	// A different options tuple is a different retained result.
	runTest("linguistic_analysis", "model-a", "medium")
	if got := conformance(); len(got) != 3 {
		t.Fatalf("three tuples conformance = %v", got)
	}

	// Re-testing an identical tuple overwrites instead of accumulating.
	runTest("linguistic_analysis", "model-a", "low")
	if got := conformance(); len(got) != 3 {
		t.Fatalf("re-tested tuple conformance = %v", got)
	}
}

// TestPipelineProviderTestRejectsInvalidTuples proves a conformance test
// validates the complete model/options tuple before any provider call:
// missing/invalid/unsupported tuple fields are 400s, a stale catalog is a
// 503, and none of them is recorded as a retained conformance result.
func TestPipelineProviderTestRejectsInvalidTuples(t *testing.T) {
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

	post := func(body string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeProviderTest(rec, authedRequestWithID(http.MethodPost, "/api/v1/analysis/providers/codex-app-server/test", body, "codex-app-server"))
		return rec.Code
	}
	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"missing model", `{"stage_id":"linguistic_analysis","options":{"reasoning_effort":"low"}}`, http.StatusBadRequest},
		{"blank model", `{"stage_id":"linguistic_analysis","model_id":" ","options":{"reasoning_effort":"low"}}`, http.StatusBadRequest},
		{"array options", `{"stage_id":"linguistic_analysis","model_id":"model-a","options":["low"]}`, http.StatusBadRequest},
		{"missing options", `{"stage_id":"linguistic_analysis","model_id":"model-a"}`, http.StatusBadRequest},
		{"unlisted model", `{"stage_id":"linguistic_analysis","model_id":"ghost-model","options":{"reasoning_effort":"low"}}`, http.StatusBadRequest},
		{"unsupported effort", `{"stage_id":"linguistic_analysis","model_id":"model-a","options":{"reasoning_effort":"extreme"}}`, http.StatusBadRequest},
		{"invalid stage", `{"stage_id":"summarize","model_id":"model-a","options":{"reasoning_effort":"low"}}`, http.StatusBadRequest},
	}
	for _, test := range cases {
		if status := post(test.body); status != test.status {
			t.Fatalf("%s = %d, want %d", test.name, status, test.status)
		}
	}

	// None of the rejected tuples was retained.
	rec := httptest.NewRecorder()
	h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
	var listing struct {
		Providers []struct {
			Conformance []map[string]any `json:"conformance"`
		} `json:"providers"`
	}
	decodeJSONBody(t, rec.Body.String(), &listing)
	if len(listing.Providers) != 1 || len(listing.Providers[0].Conformance) != 0 {
		t.Fatalf("rejected tuples retained: %v", listing.Providers)
	}

	// A stale catalog is a 503, not a provider failure.
	catalog := httpapi.NewProviderCatalogService(registry)
	catalog.SetTTL(time.Millisecond)
	staleHandler := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry, catalog)
	prime := httptest.NewRecorder()
	staleHandler.ServeProviders(prime, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
	if prime.Code != http.StatusOK {
		t.Fatalf("prime providers = %d", prime.Code)
	}
	time.Sleep(5 * time.Millisecond)
	registry.providers["codex-app-server"] = &apiFakeProvider{descriptor: codexDescriptor(), listErr: errors.New("catalog offline")}
	rec = httptest.NewRecorder()
	staleHandler.ServeProviderTest(rec, authedRequestWithID(http.MethodPost, "/api/v1/analysis/providers/codex-app-server/test",
		`{"stage_id":"linguistic_analysis","model_id":"model-a","options":{"reasoning_effort":"low"}}`, "codex-app-server"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale catalog test = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestPipelineProfileReadsReportBindingValidity proves profile reads derive
// per-binding usability from the live registry and shared catalog: a usable
// profile reads back valid, a removed model or disabled provider reads back
// invalid with a sanitized reason, while editing stays available.
func TestPipelineProfileReadsReportBindingValidity(t *testing.T) {
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
	h.ServeProfiles(rec, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", twoBindingBody("Valid", "codex-app-server", "model-a")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	var created profilePayload
	decodeJSONBody(t, rec.Body.String(), &created)

	validity := func() map[string]map[string]any {
		t.Helper()
		time.Sleep(5 * time.Millisecond)
		rec := httptest.NewRecorder()
		h.ServeProfiles(rec, authedRequest(http.MethodGet, "/api/v1/analysis/profiles", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Profiles []struct {
				ID       string           `json:"id"`
				Bindings []map[string]any `json:"bindings"`
			} `json:"profiles"`
		}
		decodeJSONBody(t, rec.Body.String(), &body)
		if len(body.Profiles) != 1 {
			t.Fatalf("profiles = %v", body.Profiles)
		}
		out := make(map[string]map[string]any)
		for _, binding := range body.Profiles[0].Bindings {
			stage, _ := binding["stage_id"].(string)
			out[stage] = binding
		}
		return out
	}

	for stage, binding := range validity() {
		if binding["valid"] != true {
			t.Fatalf("%s binding = %v, want valid", stage, binding)
		}
		if _, present := binding["validity_reason"]; present {
			t.Fatalf("%s binding leaks reason: %v", stage, binding)
		}
	}

	// The catalog drops the bound model: reads report the affected binding.
	provider.models = []annotator.Model{{ID: "model-z", DisplayName: "Model Z"}}
	for stage, binding := range validity() {
		if binding["valid"] != false || binding["validity_reason"] != "model not listed" {
			t.Fatalf("%s binding = %v, want invalid model reason", stage, binding)
		}
	}

	// The provider is disabled: reads report that instead.
	disabled := codexDescriptor()
	disabled.Enabled = false
	registry.descriptors = []annotator.ProviderDescriptor{disabled}
	for stage, binding := range validity() {
		if binding["valid"] != false || binding["validity_reason"] != "disabled provider" {
			t.Fatalf("%s binding = %v, want invalid disabled reason", stage, binding)
		}
	}
}

// TestPipelineProfileWriteRejectsValidityFields proves the write surface
// accepts only the four AnalysisProfileBindingInput fields: a GET/edit/PUT
// round trip that echoes response-only validity state is a 400 under the
// strict decoder, so writers must strip valid/validity_reason.
func TestPipelineProfileWriteRejectsValidityFields(t *testing.T) {
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

	rec := httptest.NewRecorder()
	h.ServeProfiles(rec, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", twoBindingBody("Echo", "codex-app-server", "model-a")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	var created profilePayload
	decodeJSONBody(t, rec.Body.String(), &created)

	// Read the profile back and echo one binding verbatim (including the
	// response-only validity fields) into a PUT.
	get := httptest.NewRecorder()
	h.ServeProfile(get, authedRequestWithID(http.MethodGet, "/api/v1/analysis/profiles/"+created.ID, "", created.ID))
	if get.Code != http.StatusOK {
		t.Fatalf("get = %d body=%s", get.Code, get.Body.String())
	}
	var stored struct {
		Name     string           `json:"name"`
		Bindings []map[string]any `json:"bindings"`
	}
	decodeJSONBody(t, get.Body.String(), &stored)
	if len(stored.Bindings) != 2 {
		t.Fatalf("bindings = %v", stored.Bindings)
	}
	roundTrip, _ := json.Marshal(map[string]any{"name": stored.Name, "bindings": stored.Bindings})
	put := httptest.NewRecorder()
	h.ServeProfile(put, authedRequestWithID(http.MethodPut, "/api/v1/analysis/profiles/"+created.ID, string(roundTrip), created.ID))
	if put.Code != http.StatusBadRequest {
		t.Fatalf("echoed validity PUT = %d body=%s, want 400", put.Code, put.Body.String())
	}
}

// TestPipelineProvidersRedactEchoedKey proves a catalog failure that echoes
// the resolved API key never reaches browser state: the shared catalog
// stores the redacted failure and the providers listing carries no key and
// no echoed body in last_error.
func TestPipelineProvidersRedactEchoedKey(t *testing.T) {
	const secret = "super-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid token: " + secret))
	}))
	defer server.Close()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry, err := annotator.NewRegistry(&config.ProviderConfigFile{
		Version: config.ProviderConfigVersion,
		Providers: []config.ProviderEntry{{
			ID: "mac-omlx", Label: "OMLX", EndpointLabel: "Test",
			Type: config.ProviderTypeOpenAICompatible, Enabled: true,
			RequestTimeoutSeconds: 30, BaseURL: server.URL, APIKeyEnv: "DOUBLANGU_TEST_OMLX_KEY",
		}},
	}, "codex", func(string) (string, error) { return secret, nil })
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry)
	rec := httptest.NewRecorder()
	h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("providers = %d body=%s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Providers []struct {
			ID        string `json:"id"`
			Health    string `json:"health"`
			LastError string `json:"last_error"`
		} `json:"providers"`
	}
	decodeJSONBody(t, rec.Body.String(), &listing)
	if len(listing.Providers) != 1 {
		t.Fatalf("providers = %v", listing.Providers)
	}
	entry := listing.Providers[0]
	if entry.Health != "unhealthy" {
		t.Fatalf("health = %q, want unhealthy", entry.Health)
	}
	for _, leaked := range []string{secret, "invalid token"} {
		if strings.Contains(entry.LastError, leaked) {
			t.Fatalf("last_error leaked %q: %q", leaked, entry.LastError)
		}
	}
}

// TestPipelineProvidersExposeEndpointLabelAndRetrievedAt proves the provider
// card carries the sanitized endpoint label and the catalog retrieval time
// without leaking the endpoint URL, fingerprint, timeout, or secret.
func TestPipelineProvidersExposeEndpointLabelAndRetrievedAt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a","owned_by":"test"}]}`))
	}))
	defer server.Close()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry, err := annotator.NewRegistry(&config.ProviderConfigFile{
		Version: config.ProviderConfigVersion,
		Providers: []config.ProviderEntry{{
			ID: "mac-omlx", Label: "OMLX", EndpointLabel: "Test endpoint",
			Type: config.ProviderTypeOpenAICompatible, Enabled: true,
			RequestTimeoutSeconds: 30, BaseURL: server.URL, APIKeyEnv: "DOUBLANGU_TEST_OMLX_KEY",
		}},
	}, "codex", func(string) (string, error) { return "super-secret-value", nil })
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry)
	rec := httptest.NewRecorder()
	h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("providers = %d body=%s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	var listing struct {
		Providers []struct {
			ID            string `json:"id"`
			EndpointLabel string `json:"endpoint_label"`
			RetrievedAt   string `json:"retrieved_at"`
		} `json:"providers"`
	}
	decodeJSONBody(t, raw, &listing)
	if len(listing.Providers) != 1 {
		t.Fatalf("providers = %v", listing.Providers)
	}
	entry := listing.Providers[0]
	if entry.EndpointLabel != "Test endpoint" {
		t.Fatalf("endpoint_label = %q, want the sanitized label", entry.EndpointLabel)
	}
	if entry.RetrievedAt == "" {
		t.Fatal("retrieved_at missing from healthy provider card")
	}
	for _, leaked := range []string{server.URL, "super-secret-value", "DOUBLANGU_TEST_OMLX_KEY", "config_fingerprint", "request_timeout"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("providers listing leaked %q: %s", leaked, raw)
		}
	}
}

// TestPipelineProvidersExcludeResponseBodies proves a 5xx catalog body
// carrying a hostname, filesystem path, and third-party secret never reaches
// the providers listing: last_error carries only the stable classification.
func TestPipelineProvidersExcludeResponseBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("panic db.internal.example.com:/var/lib/secret password=hunter2-third-party"))
	}))
	defer server.Close()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry, err := annotator.NewRegistry(&config.ProviderConfigFile{
		Version: config.ProviderConfigVersion,
		Providers: []config.ProviderEntry{{
			ID: "mac-omlx", Label: "OMLX", EndpointLabel: "Test",
			Type: config.ProviderTypeOpenAICompatible, Enabled: true,
			RequestTimeoutSeconds: 30, BaseURL: server.URL, APIKeyEnv: "DOUBLANGU_TEST_OMLX_KEY",
		}},
	}, "codex", func(string) (string, error) { return "super-secret-value", nil })
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	h := httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry)
	rec := httptest.NewRecorder()
	h.ServeProviders(rec, authedRequest(http.MethodGet, "/api/v1/analysis/providers", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("providers = %d body=%s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Providers []struct {
			ID        string `json:"id"`
			LastError string `json:"last_error"`
		} `json:"providers"`
	}
	decodeJSONBody(t, rec.Body.String(), &listing)
	if len(listing.Providers) != 1 {
		t.Fatalf("providers = %v", listing.Providers)
	}
	entry := listing.Providers[0]
	for _, leaked := range []string{"db.internal.example.com", "/var/lib/secret", "hunter2-third-party", "panic"} {
		if strings.Contains(entry.LastError, leaked) {
			t.Fatalf("last_error leaked %q: %q", leaked, entry.LastError)
		}
	}
	if !strings.Contains(entry.LastError, "500") {
		t.Fatalf("last_error lost the stable classification: %q", entry.LastError)
	}
}

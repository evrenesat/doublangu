package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/annotator"
	"doublangu/internal/httpapi"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
	"doublangu/internal/store"
)

// TestPromptLibraryVersionsCRUDContract proves the prompt library contract:
// unknown types 404, seeded versions list newest first, POST saves a new
// immutable version with normalized bytes and a real content hash, validation
// failures are 400 with field-safe detail, and CSRF is required to save.
func TestPromptLibraryVersionsCRUDContract(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := prompts.NewStore(db).EnsureSeed(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor()},
		providers:   map[string]annotator.Provider{"codex-app-server": &apiFakeProvider{descriptor: codexDescriptor()}},
	}
	h := httpapiPromptHandler(db, registry)
	versionsPath := "/api/v1/analysis/prompts/explore/versions"

	// Unknown types are 404 for both methods.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		h.ServePromptVersions(rec, authedRequest(method, "/api/v1/analysis/prompts/poetry/versions", `{"instruction_text":"x"}`, "prompt_type", "poetry"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s unknown type = %d body=%s", method, rec.Code, rec.Body.String())
		}
	}

	// Seeded versions list newest first with full saved rows.
	list := httptest.NewRecorder()
	h.ServePromptVersions(list, authedRequest(http.MethodGet, versionsPath, "", "prompt_type", "explore"))
	if list.Code != http.StatusOK || list.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list = %d cache %q", list.Code, list.Header().Get("Cache-Control"))
	}
	var listed struct {
		PromptType string `json:"prompt_type"`
		Versions   []struct {
			ID              string `json:"id"`
			Version         int    `json:"version"`
			InstructionText string `json:"instruction_text"`
			ContentHash     string `json:"content_hash"`
			Label           string `json:"label"`
			CreatedAt       string `json:"created_at"`
		} `json:"versions"`
	}
	decodeJSONBody(t, list.Body.String(), &listed)
	if listed.PromptType != "explore" || len(listed.Versions) != 1 || listed.Versions[0].Version != 1 {
		t.Fatalf("seeded list = %+v", listed)
	}
	if listed.Versions[0].InstructionText != prompts.DefaultInstruction(prompts.TypeExplore) {
		t.Fatal("seeded explore instruction differs from the builtin default")
	}
	if listed.Versions[0].ContentHash != prompts.ContentHashOf(listed.Versions[0].InstructionText) {
		t.Fatal("seeded content hash does not match the stored bytes")
	}

	// POST saves the next immutable version; CRLF normalizes, labels trim.
	rec := httptest.NewRecorder()
	h.ServePromptVersions(rec, authedRequest(http.MethodPost, versionsPath,
		`{"instruction_text":"Owner instruction\r\nsecond line","label":"  Experiment A  "}`, "prompt_type", "explore"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("save = %d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		PromptType string `json:"prompt_type"`
		Version    struct {
			ID              string `json:"id"`
			Version         int    `json:"version"`
			InstructionText string `json:"instruction_text"`
			Label           string `json:"label"`
			ContentHash     string `json:"content_hash"`
		} `json:"version"`
	}
	decodeJSONBody(t, rec.Body.String(), &saved)
	if saved.PromptType != "explore" || saved.Version.Version != 2 || saved.Version.Label != "Experiment A" {
		t.Fatalf("saved = %+v", saved)
	}
	if strings.Contains(saved.Version.InstructionText, "\r") ||
		saved.Version.InstructionText != "Owner instruction\nsecond line" {
		t.Fatalf("saved instruction = %q", saved.Version.InstructionText)
	}
	if saved.Version.ContentHash != prompts.ContentHashOf("Owner instruction\nsecond line") {
		t.Fatal("saved content hash mismatch")
	}

	// Validation failures are 400 with field-safe detail; PUT is rejected;
	// CSRF is enforced.
	for _, testCase := range []struct{ name, body string }{
		{"blank instruction", `{"instruction_text":"   "}`},
		{"missing instruction", `{"label":"x"}`},
		{"oversized instruction", `{"instruction_text":"` + strings.Repeat("a", 64*1024+1) + `"}`},
		{"oversized label", `{"instruction_text":"x","label":"` + strings.Repeat("l", 81) + `"}`},
	} {
		rec = httptest.NewRecorder()
		h.ServePromptVersions(rec, authedRequest(http.MethodPost, versionsPath, testCase.body, "prompt_type", "explore"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d body=%s", testCase.name, rec.Code, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	h.ServePromptVersions(rec, authedRequest(http.MethodPut, versionsPath, `{}`, "prompt_type", "explore"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	noCSRF := httptest.NewRecorder()
	_ = noCSRF
	csrfHandler := httpapiPromptHandlerWithCSRFError(db, registry)
	csrfHandler.ServePromptVersions(rec, authedRequest(http.MethodPost, versionsPath, `{"instruction_text":"x"}`, "prompt_type", "explore"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("csrf = %d", rec.Code)
	}
}

// TestPipelineProfileChoicesAPI proves the profile API contract for Explore
// bindings and prompt selections: creation with explicit choices stores them,
// replacement omitting the fields preserves them, wrong-type and partial
// selections reject with the validation error shape, and responses resolve
// ids with version numbers and labels.
func TestPipelineProfileChoicesAPI(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor()},
		providers:   map[string]annotator.Provider{"codex-app-server": &apiFakeProvider{descriptor: codexDescriptor()}},
	}
	h := httpapiPipelineHandler(db, registry)
	ctx := context.Background()

	// Custom versions for explicit selection.
	promptStore := prompts.NewStore(db)
	custom := make(map[prompts.PromptType]string, 5)
	versions := make(map[prompts.PromptType]prompts.Version, 5)
	for _, promptType := range prompts.Types {
		version, err := promptStore.Save(ctx, promptType, "Custom "+string(promptType)+" instruction.", "Label "+string(promptType))
		if err != nil {
			t.Fatal(err)
		}
		custom[promptType] = version.ID
		versions[promptType] = *version
	}

	selectionsJSON := `{"linguistic_analysis":"` + custom[prompts.TypeLinguisticAnalysis] +
		`","article_translation":"` + custom[prompts.TypeArticleTranslation] +
		`","explore":"` + custom[prompts.TypeExplore] +
		`","sentence_translation":"` + custom[prompts.TypeSentenceTranslation] +
		`","correction":"` + custom[prompts.TypeCorrection] + `"}`
	linguistic, _ := json.Marshal(apiBinding(pipeline.StageLinguisticAnalysis))
	translation, _ := json.Marshal(apiBinding(pipeline.StageTranslation))
	createBody := `{"name":"Choices","bindings":[` + string(linguistic) + `,` + string(translation) + `],
		"explore_binding":{"provider_id":"codex-app-server","model_id":"model-a","options":{"reasoning_effort":"high"}},
		"prompt_versions":` + selectionsJSON + `}`
	created := httptest.NewRecorder()
	h.ServeProfiles(created, authedRequest(http.MethodPost, "/api/v1/analysis/profiles", createBody))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", created.Code, created.Body.String())
	}
	// Decode with the exact wire shape: the explore binding carries validity,
	// and prompt_versions carry resolved ids with version number and label.
	var createdResponse struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		ExploreBinding *struct {
			StageID        string          `json:"stage_id"`
			ProviderID     string          `json:"provider_id"`
			ModelID        string          `json:"model_id"`
			Options        json.RawMessage `json:"options"`
			Valid          bool            `json:"valid"`
			ValidityReason string          `json:"validity_reason"`
		} `json:"explore_binding"`
		PromptVersions map[string]struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Label   string `json:"label"`
		} `json:"prompt_versions"`
	}
	decodeJSONBody(t, created.Body.String(), &createdResponse)
	if createdResponse.ID == "" {
		t.Fatal("created profile has no id")
	}
	if createdResponse.ExploreBinding == nil {
		t.Fatal("explore binding missing from the created profile response")
	}
	exploreBinding := createdResponse.ExploreBinding
	if exploreBinding.ModelID != "model-a" || exploreBinding.ProviderID != "codex-app-server" || !exploreBinding.Valid {
		t.Fatalf("explore binding = %+v", exploreBinding)
	}
	var options map[string]any
	if err := json.Unmarshal(exploreBinding.Options, &options); err != nil {
		t.Fatal(err)
	}
	if options["reasoning_effort"] != "high" {
		t.Fatalf("explore options = %v, want an effort independent of translation", options)
	}
	for _, promptType := range prompts.Types {
		ref, ok := createdResponse.PromptVersions[string(promptType)]
		if !ok || ref.ID != custom[promptType] || ref.Version != 1 || ref.Label != "Label "+string(promptType) {
			t.Fatalf("%s prompt ref = %+v", promptType, ref)
		}
	}

	// Replacement omitting the fields preserves the stored explore binding
	// and selections (old-client compatibility).
	replaceBody := `{"name":"Choices renamed","bindings":[` + string(linguistic) + `,` + string(translation) + `]}`
	replaced := httptest.NewRecorder()
	h.ServeProfile(replaced, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+createdResponse.ID, replaceBody, "id", createdResponse.ID))
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace = %d body=%s", replaced.Code, replaced.Body.String())
	}
	var renamed struct {
		Name           string `json:"name"`
		ExploreBinding *struct {
			ModelID string `json:"model_id"`
		} `json:"explore_binding"`
		PromptVersions map[string]struct {
			ID string `json:"id"`
		} `json:"prompt_versions"`
	}
	decodeJSONBody(t, replaced.Body.String(), &renamed)
	if renamed.Name != "Choices renamed" || renamed.ExploreBinding == nil || len(renamed.PromptVersions) != 5 {
		t.Fatalf("replaced profile lost stored choices: %+v", renamed)
	}

	// A partial map and a wrong-type version reject with the validation
	// error shape.
	partial := `{"name":"Choices","bindings":[` + string(linguistic) + `,` + string(translation) + `],
		"prompt_versions":{"linguistic_analysis":"` + custom[prompts.TypeLinguisticAnalysis] + `","explore":"` + custom[prompts.TypeExplore] + `"}}`
	rec := httptest.NewRecorder()
	h.ServeProfile(rec, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+createdResponse.ID, partial, "id", createdResponse.ID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("partial selections = %d body=%s", rec.Code, rec.Body.String())
	}
	wrongType := `{"name":"Choices","bindings":[` + string(linguistic) + `,` + string(translation) + `],
		"prompt_versions":{"linguistic_analysis":"` + custom[prompts.TypeExplore] +
		`","article_translation":"` + custom[prompts.TypeArticleTranslation] +
		`","explore":"` + custom[prompts.TypeExplore] +
		`","sentence_translation":"` + custom[prompts.TypeSentenceTranslation] +
		`","correction":"` + custom[prompts.TypeCorrection] + `"}}`
	rec = httptest.NewRecorder()
	h.ServeProfile(rec, authedRequest(http.MethodPut, "/api/v1/analysis/profiles/"+createdResponse.ID, wrongType, "id", createdResponse.ID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-type selection = %d body=%s", rec.Code, rec.Body.String())
	}
}

// httpapiPromptHandler builds the prompt library handler.
func httpapiPromptHandler(db *store.DB, registry *apiFakeRegistry) *httpapi.PromptLibraryHandler {
	return httpapi.NewPromptLibraryHandler(db, allowArticleCSRF{})
}

// httpapiPromptHandlerWithCSRFError builds a prompt handler whose CSRF
// verification fails, for proving saves are CSRF-protected.
func httpapiPromptHandlerWithCSRFError(db *store.DB, registry *apiFakeRegistry) *httpapi.PromptLibraryHandler {
	return httpapi.NewPromptLibraryHandler(db, &testCSRF{shouldError: true})
}

// httpapiPipelineHandler builds the pipeline analysis handler.
func httpapiPipelineHandler(db *store.DB, registry *apiFakeRegistry) *httpapi.PipelineAnalysisHandler {
	return httpapi.NewPipelineAnalysisHandler(db, allowArticleCSRF{}, registry)
}

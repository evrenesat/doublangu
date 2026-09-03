package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// providerRegistry is the owner-API registry seam; annotator.Registry and
// test doubles satisfy it.
type providerRegistry interface {
	Provider(id string) (annotator.Provider, bool)
	Descriptors() []annotator.ProviderDescriptor
}

type pipelineAnalysisHandler struct {
	profiles *analysis.ProfileStore
	registry providerRegistry
	csrf     CSRFVerifier
	catalog  *ProviderCatalogService
}

func NewPipelineAnalysisHandler(db *store.DB, csrf CSRFVerifier, registry providerRegistry, catalog ...*ProviderCatalogService) *PipelineAnalysisHandler {
	service := NewProviderCatalogService(registry)
	if len(catalog) > 0 && catalog[0] != nil {
		service = catalog[0]
	}
	return &PipelineAnalysisHandler{
		profiles: analysis.NewProfileStore(db), registry: registry, csrf: csrf, catalog: service,
	}
}

type PipelineAnalysisHandler = pipelineAnalysisHandler

// providerListEntry is the explicit owner-visible provider card. It exposes
// only fields declared by the AnalysisProvider OpenAPI schema: the sanitized
// descriptor (id/label/type/enabled) plus live catalog/health state. Provider
// connection identity (endpoint_label, config_fingerprint, request timeout)
// is never serialized to the browser.
type providerListEntry struct {
	ID        string               `json:"id"`
	Label     string               `json:"label"`
	Type      string               `json:"type"`
	Enabled   bool                 `json:"enabled"`
	Models    []providerModelEntry `json:"models,omitempty"`
	Stale     bool                 `json:"stale"`
	LastError string               `json:"last_error,omitempty"`
	Health    string               `json:"health"`
}

// providerModelEntry matches the AnalysisProviderModel schema exactly:
// catalog fields such as hidden/is_default are not part of the owner provider
// response, while the supported reasoning efforts are exposed so the editor
// can offer only the efforts a model actually supports.
type providerModelEntry struct {
	ID                        string                      `json:"id"`
	DisplayName               string                      `json:"display_name,omitempty"`
	SupportedReasoningEfforts []annotator.ReasoningEffort `json:"supported_reasoning_efforts,omitempty"`
}

type providersResponse struct {
	Providers []providerListEntry `json:"providers"`
}

type profileRequest struct {
	Name     string                     `json:"name"`
	Bindings []pipeline.BindingSnapshot `json:"bindings"`
}

// profileBindingResponse is the explicit owner-visible binding row declared by
// the AnalysisProfileBinding schema. Snapshot-only fields (provider type,
// config fingerprint, options hash, stage contract/prompt versions) stay on
// the server and in run history; the browser never sees them here.
type profileBindingResponse struct {
	StageID    string          `json:"stage_id"`
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	Options    json.RawMessage `json:"options"`
}

type profileResponse struct {
	ID       string                   `json:"id"`
	Name     string                   `json:"name"`
	Bindings []profileBindingResponse `json:"bindings"`
	IsActive bool                     `json:"is_active"`
}

type pipelineSettingsResponse struct {
	ActiveProfileID string `json:"active_profile_id,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type providerTestRequest struct {
	StageID string          `json:"stage_id"`
	ModelID string          `json:"model_id"`
	Options json.RawMessage `json:"options"`
}

type providerTestResponse struct {
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	ErrorCode  string `json:"error_code,omitempty"`
}

// providerTypesByID builds the provider type map from registry descriptors.
func providerTypesByID(registry providerRegistry) map[string]string {
	types := make(map[string]string)
	for _, descriptor := range registry.Descriptors() {
		types[descriptor.ID] = descriptor.Type
	}
	return types
}

// bindingValidationError separates client-correctable validation problems
// from provider/catalog unavailability so handlers can choose the status.
type bindingValidationError struct {
	Unavailable bool
	Message     string
}

func (e *bindingValidationError) Error() string { return e.Message }

// canonicalizeUsableBindings validates profile bindings against live enabled
// provider instances before saving: disabled or unknown providers are
// rejected, and any binding whose complete provider/model/options tuple
// changed from the stored profile must exist in a successful model catalog
// listing, with the Codex reasoning effort supported by that model. Unchanged
// tuples skip the catalog check so an existing profile can be renamed or
// re-saved while a catalog is briefly unavailable. Options are canonicalized
// before the tuple comparison so an options-only change is never mistaken for
// an unchanged binding.
func (h *PipelineAnalysisHandler) canonicalizeUsableBindings(ctx context.Context, existing []pipeline.BindingSnapshot, raw []pipeline.BindingSnapshot) ([]pipeline.BindingSnapshot, error) {
	existingByStage := make(map[pipeline.StageID]pipeline.BindingSnapshot, len(existing))
	for _, binding := range existing {
		existingByStage[binding.StageID] = binding
	}
	for index := range raw {
		binding := &raw[index]
		provider, ok := h.registry.Provider(binding.ProviderID)
		if !ok {
			return nil, &bindingValidationError{Message: "binding references an unknown provider"}
		}
		descriptor := provider.Descriptor()
		if !descriptor.Enabled {
			return nil, &bindingValidationError{Message: "binding references a disabled provider"}
		}
		if descriptor.Type != binding.ProviderType && binding.ProviderType != "" {
			return nil, &bindingValidationError{Message: "binding provider type does not match the configured provider"}
		}
		canonical, err := config.CanonicalizeProviderOptions(descriptor.Type, binding.Options)
		if err != nil {
			return nil, &bindingValidationError{Message: fmt.Sprintf("binding %s options are invalid for provider type %s", binding.StageID, descriptor.Type)}
		}
		optionsHash, err := pipeline.OptionsHashOf(canonical)
		if err != nil {
			return nil, &bindingValidationError{Message: fmt.Sprintf("binding %s options are invalid", binding.StageID)}
		}
		binding.Options = canonical
		binding.OptionsHash = optionsHash
		prior, hadPrior := existingByStage[binding.StageID]
		tupleChanged := !hadPrior || prior.ProviderID != binding.ProviderID ||
			prior.ModelID != binding.ModelID || prior.OptionsHash != optionsHash
		if tupleChanged {
			snapshot, err := h.catalog.Snapshot(ctx, binding.ProviderID, false)
			if err != nil {
				return nil, &bindingValidationError{Unavailable: true, Message: "provider model catalog is unavailable; cannot verify the binding"}
			}
			if snapshot.Stale {
				return nil, &bindingValidationError{Unavailable: true, Message: "provider model catalog is stale; refresh it before changing this binding"}
			}
			models := snapshot.Models
			found := false
			for _, model := range models {
				if model.ID == binding.ModelID {
					found = true
					break
				}
			}
			if !found {
				return nil, &bindingValidationError{Message: "model is not listed in the provider catalog"}
			}
			if descriptor.Type == config.ProviderTypeCodexAppServer {
				var options codexCatalogOptions
				if err := json.Unmarshal(canonical, &options); err != nil || options.ReasoningEffort == "" {
					return nil, &bindingValidationError{Message: fmt.Sprintf("binding %s options are invalid for provider type %s", binding.StageID, descriptor.Type)}
				}
				if !annotator.SupportsSelection(models, binding.ModelID, options.ReasoningEffort) {
					return nil, &bindingValidationError{Message: fmt.Sprintf("reasoning effort %q is not supported by model %q", options.ReasoningEffort, binding.ModelID)}
				}
			}
		}
	}
	return analysis.CanonicalizeBindings(providerTypesByID(h.registry), raw)
}

// codexCatalogOptions is the canonical codex_app_server options object used
// to verify the advertised model/effort pair against the model catalog.
type codexCatalogOptions struct {
	ReasoningEffort string `json:"reasoning_effort"`
}

// usableProfileBindings validates one stored profile's bindings against the
// live registry and the shared provider catalog, then enriches them for
// queueing. Providers must exist and be enabled with a matching type, options
// must canonicalize, the bound model must still be listed in a non-stale
// catalog (with the Codex reasoning effort still advertised by that model),
// and stage contract/prompt versions plus the provider config fingerprint are
// filled in. Article resolution, fresh-run profile selection, and settings
// activation all use this single check, so a persisted profile referencing a
// provider, model, or effort that a configuration change removed can never be
// activated or queued.
func usableProfileBindings(ctx context.Context, registry providerRegistry, catalog *ProviderCatalogService, bindings []pipeline.BindingSnapshot) ([]pipeline.BindingSnapshot, error) {
	if catalog == nil {
		catalog = NewProviderCatalogService(registry)
	}
	types := make(map[string]annotator.ProviderDescriptor)
	if registry != nil {
		for _, descriptor := range registry.Descriptors() {
			types[descriptor.ID] = descriptor
		}
	}
	enriched := make([]pipeline.BindingSnapshot, 0, len(bindings))
	for _, binding := range bindings {
		descriptor, ok := types[binding.ProviderID]
		if !ok {
			return nil, errors.New("binding references an unknown provider")
		}
		if !descriptor.Enabled {
			return nil, errors.New("binding references a disabled provider")
		}
		binding.ProviderType = descriptor.Type
		binding.ProviderConfigFingerprint = descriptor.ConfigFingerprint
		canonical, err := config.CanonicalizeProviderOptions(descriptor.Type, binding.Options)
		if err != nil {
			return nil, err
		}
		hash, err := pipeline.OptionsHashOf(canonical)
		if err != nil {
			return nil, err
		}
		binding.Options = canonical
		binding.OptionsHash = hash
		snapshot, err := catalog.Snapshot(ctx, binding.ProviderID, false)
		if err != nil {
			return nil, fmt.Errorf("binding %s provider catalog is unavailable", binding.StageID)
		}
		if snapshot.Stale {
			return nil, fmt.Errorf("binding %s provider catalog is stale; refresh the provider first", binding.StageID)
		}
		found := false
		for _, model := range snapshot.Models {
			if model.ID == binding.ModelID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("binding %s model %q is no longer listed in the provider catalog", binding.StageID, binding.ModelID)
		}
		if descriptor.Type == config.ProviderTypeCodexAppServer {
			var options codexCatalogOptions
			if err := json.Unmarshal(canonical, &options); err != nil || options.ReasoningEffort == "" {
				return nil, fmt.Errorf("binding %s options are invalid for provider type %s", binding.StageID, descriptor.Type)
			}
			if !annotator.SupportsSelection(snapshot.Models, binding.ModelID, options.ReasoningEffort) {
				return nil, fmt.Errorf("binding %s reasoning effort %q is no longer supported by model %q", binding.StageID, options.ReasoningEffort, binding.ModelID)
			}
		}
		contract, prompt, ok := pipeline.StageContracts(binding.StageID)
		if !ok {
			return nil, errors.New("binding references an unregistered stage")
		}
		binding.ContractVersion = contract
		binding.PromptVersion = prompt
		enriched = append(enriched, binding)
	}
	return pipeline.SortBindings(enriched)
}

func providerEntry(descriptor annotator.ProviderDescriptor, models []annotator.Model, stale bool, lastError, health string) providerListEntry {
	entry := providerListEntry{
		ID: descriptor.ID, Label: descriptor.Label, Type: descriptor.Type,
		Enabled: descriptor.Enabled, Stale: stale, LastError: lastError, Health: health,
	}
	for _, model := range models {
		entry.Models = append(entry.Models, providerModelEntry{
			ID: model.ID, DisplayName: model.DisplayName,
			SupportedReasoningEfforts: append([]annotator.ReasoningEffort(nil), model.SupportedReasoningEfforts...),
		})
	}
	return entry
}

func profileBindingsResponse(bindings []pipeline.BindingSnapshot) []profileBindingResponse {
	response := make([]profileBindingResponse, 0, len(bindings))
	for _, binding := range bindings {
		response = append(response, profileBindingResponse{
			StageID: string(binding.StageID), ProviderID: binding.ProviderID,
			ModelID: binding.ModelID, Options: binding.Options,
		})
	}
	return response
}

// ServeProviders renders the predefined provider cards from the shared
// per-provider catalog. The optional refresh=true&provider_id= pair forces a
// refresh of exactly that provider; every other provider renders from its
// last-good cache. Models are never dropped by a transient refresh failure:
// the stale flag and a sanitized error accompany the retained last-good list.
func (h *PipelineAnalysisHandler) ServeProviders(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	refresh, err := parseRefresh(r.URL.Query().Get("refresh"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "refresh must be true or false", ErrCodeValidation)
		return
	}
	providerID := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	if refresh && providerID == "" {
		WriteError(w, http.StatusBadRequest, "refresh=true requires one provider_id", ErrCodeValidation)
		return
	}
	if refresh {
		if _, ok := h.registry.Provider(providerID); !ok {
			WriteError(w, http.StatusNotFound, "provider not found", ErrCodeNotFound)
			return
		}
	}
	entries := make([]providerListEntry, 0)
	for _, descriptor := range h.registry.Descriptors() {
		if !descriptor.Enabled {
			entries = append(entries, providerEntry(descriptor, nil, false, "", "disabled"))
			continue
		}
		if _, ok := h.registry.Provider(descriptor.ID); !ok {
			entries = append(entries, providerEntry(descriptor, nil, false, "", "unknown"))
			continue
		}
		snapshot, err := h.catalog.Snapshot(r.Context(), descriptor.ID, refresh && descriptor.ID == providerID)
		if err != nil {
			entries = append(entries, providerEntry(descriptor, nil, false, sanitizedProviderError(err), "unhealthy"))
			continue
		}
		health := "healthy"
		if snapshot.LastError != "" {
			health = "unhealthy"
		}
		entries = append(entries, providerEntry(descriptor, snapshot.Models, snapshot.Stale, snapshot.LastError, health))
	}
	WriteOK(w, providersResponse{Providers: entries})
}

func (h *PipelineAnalysisHandler) ServeProfiles(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	switch r.Method {
	case http.MethodGet:
		profiles, err := h.profiles.List(r.Context())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "profiles unavailable", ErrCodeInternal)
			return
		}
		response := make([]profileResponse, 0, len(profiles))
		for _, profile := range profiles {
			response = append(response, profileResponse{
				ID: profile.ID, Name: profile.Name, Bindings: profileBindingsResponse(profile.Bindings), IsActive: profile.IsActive,
			})
		}
		WriteOK(w, map[string]any{"profiles": response})
	case http.MethodPost:
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return
		}
		var input profileRequest
		if err := decodeJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid profile", ErrCodeValidation)
			return
		}
		canonical, err := h.canonicalizeUsableBindings(r.Context(), nil, input.Bindings)
		if err != nil {
			writeBindingValidationError(w, err)
			return
		}
		profile, err := h.profiles.Create(r.Context(), input.Name, canonical)
		if err != nil {
			writeProfileError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, profileResponse{
			ID: profile.ID, Name: profile.Name, Bindings: profileBindingsResponse(profile.Bindings),
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *PipelineAnalysisHandler) ServeProfile(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	profileID := r.PathValue("id")
	if profileID == "" {
		WriteError(w, http.StatusBadRequest, "profile id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(profileID); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid profile id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := h.profiles.Get(r.Context(), profileID)
		if err != nil {
			writeProfileError(w, err)
			return
		}
		WriteOK(w, profileResponse{ID: profile.ID, Name: profile.Name, Bindings: profileBindingsResponse(profile.Bindings), IsActive: profile.IsActive})
	case http.MethodPut:
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return
		}
		var input profileRequest
		if err := decodeJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid profile", ErrCodeValidation)
			return
		}
		stored, err := h.profiles.Get(r.Context(), profileID)
		if err != nil {
			writeProfileError(w, err)
			return
		}
		canonical, err := h.canonicalizeUsableBindings(r.Context(), stored.Bindings, input.Bindings)
		if err != nil {
			writeBindingValidationError(w, err)
			return
		}
		profile, err := h.profiles.Replace(r.Context(), profileID, input.Name, canonical)
		if err != nil {
			writeProfileError(w, err)
			return
		}
		WriteOK(w, profileResponse{ID: profile.ID, Name: profile.Name, Bindings: profileBindingsResponse(profile.Bindings), IsActive: profile.IsActive})
	case http.MethodDelete:
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return
		}
		if err := h.profiles.Delete(r.Context(), profileID); err != nil {
			writeProfileError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"deleted": profileID})
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *PipelineAnalysisHandler) ServeProfileSettings(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	switch r.Method {
	case http.MethodGet:
		activeID, err := h.profiles.ActiveProfile(r.Context())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "analysis settings unavailable", ErrCodeInternal)
			return
		}
		WriteOK(w, pipelineSettingsResponse{ActiveProfileID: activeID})
	case http.MethodPut:
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return
		}
		var input pipelineSettingsResponse
		if err := decodeJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid analysis settings", ErrCodeValidation)
			return
		}
		if input.ActiveProfileID == "" {
			WriteError(w, http.StatusBadRequest, "active_profile_id is required", ErrCodeValidation)
			return
		}
		profile, err := h.profiles.Get(r.Context(), input.ActiveProfileID)
		if err != nil {
			writeProfileError(w, err)
			return
		}
		// A persisted profile can outlive the provider configuration that
		// created it (restart with a provider disabled/removed or a model or
		// effort removed from the catalog). Reject activation before any
		// settings state changes when the profile is no longer usable,
		// mirroring article resolution and fresh-run selection.
		if _, err := usableProfileBindings(r.Context(), h.registry, h.catalog, profile.Bindings); err != nil {
			WriteError(w, http.StatusConflict, "profile is not currently usable: "+sanitizedProviderError(err), ErrCodeConflict)
			return
		}
		if err := h.profiles.Activate(r.Context(), input.ActiveProfileID); err != nil {
			writeProfileError(w, err)
			return
		}
		WriteOK(w, pipelineSettingsResponse{ActiveProfileID: input.ActiveProfileID, UpdatedAt: store.NowUTC()})
	default:
		w.Header().Set("Allow", "GET, PUT")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

// ServeProviderTest runs the fixed safe fixture through the selected stage and
// returns only status, duration, and a stable error code.
func (h *PipelineAnalysisHandler) ServeProviderTest(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
		WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
		return
	}
	providerID := r.PathValue("id")
	provider, ok := h.registry.Provider(providerID)
	if !ok {
		WriteError(w, http.StatusNotFound, "provider not found", ErrCodeNotFound)
		return
	}
	var input providerTestRequest
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid provider test", ErrCodeValidation)
		return
	}
	if !pipeline.StageID(input.StageID).Valid() {
		WriteError(w, http.StatusBadRequest, "invalid stage", ErrCodeValidation)
		return
	}
	started := time.Now()
	status, errorCode := "healthy", ""
	if err := runProviderFixture(r.Context(), provider, input); err != nil {
		status, errorCode = "unhealthy", annotator.StageErrorCode(err)
	}
	WriteOK(w, providerTestResponse{Status: status, DurationMS: time.Since(started).Milliseconds(), ErrorCode: errorCode})
}

func writeBindingValidationError(w http.ResponseWriter, err error) {
	var typed *bindingValidationError
	if errors.As(err, &typed) {
		if typed.Unavailable {
			WriteError(w, http.StatusServiceUnavailable, typed.Message, ErrCodeAnalysisUnavailable)
			return
		}
		WriteError(w, http.StatusBadRequest, typed.Message, ErrCodeValidation)
		return
	}
	WriteError(w, http.StatusBadRequest, sanitizedProviderError(err), ErrCodeValidation)
}

func writeProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, analysis.ErrProfileNotFound):
		WriteError(w, http.StatusNotFound, "profile not found", ErrCodeNotFound)
	default:
		var conflict *analysis.ProfileConflictError
		if errors.As(err, &conflict) {
			WriteError(w, http.StatusConflict, conflict.Reason, ErrCodeConflict)
			return
		}
		WriteError(w, http.StatusBadRequest, sanitizedProviderError(err), ErrCodeValidation)
	}
}

func sanitizedProviderError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 240 {
		message = message[:240]
	}
	for _, secret := range []string{"Bearer ", "api_key", "Authorization"} {
		if strings.Contains(message, secret) {
			return "provider request failed"
		}
	}
	return message
}

// runProviderFixture runs one stage against the fixed safe fixture source
// "De kat zit op de mat." with server-prepared tokens/sentences. For the
// translation stage the checked-in server-built linguistic artifact (all
// unchanged tokens) is used.
func runProviderFixture(ctx context.Context, provider annotator.Provider, input providerTestRequest) error {
	canonicalOptions, err := canonicalizeForProvider(provider.Descriptor().Type, input.Options)
	if err != nil {
		return err
	}
	binding, err := pipelineBindingForTest(provider.Descriptor(), pipeline.StageID(input.StageID), input.ModelID, canonicalOptions)
	if err != nil {
		return err
	}
	resolved, err := annotator.ResolveBinding(binding)
	if err != nil {
		return err
	}
	chunk, err := fixtureChunk(ctx)
	if err != nil {
		return err
	}
	if pipeline.StageID(input.StageID) == pipeline.StageLinguisticAnalysis {
		_, _, err := annotator.ExecuteLinguisticStage(ctx, provider, resolved, chunk)
		return err
	}
	linguistic, err := fixtureLinguistic(chunk)
	if err != nil {
		return err
	}
	_, _, err = annotator.ExecuteTranslationStage(ctx, provider, resolved, chunk, linguistic)
	return err
}

func canonicalizeForProvider(providerType string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("options are required")
	}
	return config.CanonicalizeProviderOptions(providerType, raw)
}

func pipelineBindingForTest(descriptor annotator.ProviderDescriptor, stage pipeline.StageID, modelID string, options json.RawMessage) (pipeline.BindingSnapshot, error) {
	contract, prompt, ok := pipeline.StageContracts(stage)
	if !ok {
		return pipeline.BindingSnapshot{}, errors.New("invalid stage")
	}
	optionsHash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		return pipeline.BindingSnapshot{}, err
	}
	return pipeline.BindingSnapshot{
		StageID: stage, ProviderID: descriptor.ID, ProviderType: descriptor.Type,
		ProviderConfigFingerprint: descriptor.ConfigFingerprint, ModelID: modelID,
		Options: options, OptionsHash: optionsHash,
		ContractVersion: contract, PromptVersion: prompt,
	}, nil
}

// fixtureChunk prepares the fixed conformance source with server sentences.
func fixtureChunk(ctx context.Context) (semantics.PreparedChunk, error) {
	_ = ctx
	input, err := semantics.Prepare("Conformance", "nl", "en", []semantics.Block{{BlockIndex: 0, SourceText: "De kat zit op de mat."}}, nil)
	if err != nil {
		return semantics.PreparedChunk{}, err
	}
	span, err := semantics.ResolveSpan(input.Blocks[0], input.Blocks[0].SourceText, 0)
	if err != nil {
		return semantics.PreparedChunk{}, err
	}
	input.Sentences = []semantics.ResolvedSentence{{Index: 0, Span: span}}
	return semantics.PrepareChunk(input, 0, nil)
}

// fixtureLinguistic builds the checked-in unchanged-token linguistic artifact
// for the fixed conformance source.
func fixtureLinguistic(chunk semantics.PreparedChunk) (*semantics.ValidatedLinguistic, error) {
	artifact := semantics.LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		artifact.Tokens = append(artifact.Tokens, semantics.LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	return semantics.ValidateLinguistic(chunk, artifact)
}

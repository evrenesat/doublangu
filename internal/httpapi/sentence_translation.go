// Sentence translation HTTP surface: authenticated read/poll and the
// explicit POST that may enqueue a sentence job. Saved data is always read
// before any provider availability check; a ready translation never triggers
// a provider or catalog call, and article/sentence membership is verified
// server-side before any run or queue work.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"doublangu/internal/analysis"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/sentencetranslation"
	"doublangu/internal/store"
)

// SentenceHandler serves the sentence translation routes.
type SentenceHandler struct {
	db       *store.DB
	csrf     CSRFVerifier
	service  *sentencetranslation.Service
	registry providerRegistry
	catalog  *ProviderCatalogService
	profiles *analysis.ProfileStore
}

// NewSentenceHandler builds the handler. The binding resolver applies the
// shared usableProfileBindings checks to the active profile's translation
// binding only; the linguistic binding is never resolved for sentence
// generation.
func NewSentenceHandler(db *store.DB, csrf CSRFVerifier, registry providerRegistry, catalog *ProviderCatalogService) *SentenceHandler {
	handler := &SentenceHandler{
		db: db, csrf: csrf, registry: registry, catalog: catalog,
		profiles: analysis.NewProfileStore(db),
	}
	handler.service = sentencetranslation.NewService(db, handler.resolveSentenceGeneration)
	return handler
}

// resolveSentenceGeneration resolves the active profile's Translation binding
// plus its pinned sentence_translation generation and correction prompt
// snapshots outside any write transaction. Only the Translation binding is
// resolved here: an unavailable unrelated linguistic provider must not block
// sentence work.
func (h *SentenceHandler) resolveSentenceGeneration(ctx context.Context) (sentencetranslation.ResolvedSentenceGeneration, error) {
	activeID, err := h.profiles.ActiveProfile(ctx)
	if err != nil || activeID == "" {
		return sentencetranslation.ResolvedSentenceGeneration{}, errors.New("no active profile")
	}
	profile, err := h.profiles.Get(ctx, activeID)
	if err != nil {
		return sentencetranslation.ResolvedSentenceGeneration{}, err
	}
	var stored *pipeline.BindingSnapshot
	for index := range profile.Bindings {
		if profile.Bindings[index].StageID == pipeline.StageTranslation {
			stored = &profile.Bindings[index]
			break
		}
	}
	if stored == nil {
		return sentencetranslation.ResolvedSentenceGeneration{}, errors.New("active profile has no translation binding")
	}
	bindings, err := usableProfileBindings(ctx, h.registry, h.catalog, []pipeline.BindingSnapshot{*stored})
	if err != nil {
		return sentencetranslation.ResolvedSentenceGeneration{}, err
	}
	if len(bindings) == 0 {
		return sentencetranslation.ResolvedSentenceGeneration{}, errors.New("active profile has no usable translation binding")
	}
	promptSnapshots, err := h.profiles.SentencePromptSnapshots(ctx, activeID)
	if err != nil {
		return sentencetranslation.ResolvedSentenceGeneration{}, err
	}
	return sentencetranslation.ResolvedSentenceGeneration{
		Binding:         bindings[0],
		PromptSnapshots: promptSnapshots,
		ProfileID:       profile.ID,
		ProfileName:     profile.Name,
	}, nil
}

// ServeTranslation handles GET and POST
// /api/v1/articles/{id}/sentences/{sentence_id}/translation.
func (h *SentenceHandler) ServeTranslation(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	articleID, err := library.ParseULID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusNotFound, "article not found", ErrCodeNotFound)
		return
	}
	sentenceID, err := library.ParseULID(r.PathValue("sentence_id"))
	if err != nil {
		WriteError(w, http.StatusNotFound, "sentence not found", ErrCodeNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		envelope, err := h.service.Lookup(r.Context(), articleID, sentenceID)
		if err != nil {
			h.writeSentenceError(w, err)
			return
		}
		WriteOK(w, envelope)
	case http.MethodPost:
		h.serveStart(w, r, articleID, sentenceID)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

type sentenceStartInput struct {
	Mode string `json:"mode"`
}

func (h *SentenceHandler) serveStart(w http.ResponseWriter, r *http.Request, articleID, sentenceID library.ULID) {
	if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
		WriteError(w, http.StatusForbidden, "csrf", ErrCodeCSRF)
		return
	}
	var input sentenceStartInput
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	var regenerate bool
	switch input.Mode {
	case "ensure":
	case "regenerate":
		regenerate = true
	default:
		WriteError(w, http.StatusBadRequest, "mode must be ensure or regenerate", ErrCodeValidation)
		return
	}
	envelope, started, err := h.service.Ensure(r.Context(), articleID, sentenceID, regenerate)
	if err != nil {
		h.writeSentenceError(w, err)
		return
	}
	status := http.StatusOK
	if started || envelope.Status == sentencetranslation.StatusQueued || envelope.Status == sentencetranslation.StatusRunning {
		status = http.StatusAccepted
	}
	WriteJSON(w, status, envelope)
}

func (h *SentenceHandler) writeSentenceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sentencetranslation.ErrNotFound):
		WriteError(w, http.StatusNotFound, "sentence not found", ErrCodeNotFound)
	case errors.Is(err, sentencetranslation.ErrProviderUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "the configured translation provider is not usable", sentencetranslation.CodeProviderUnavailable)
	default:
		WriteError(w, http.StatusInternalServerError, "sentence translation request failed", ErrCodeInternal)
	}
}

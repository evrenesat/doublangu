// Reader dictionary explore HTTP surface: authenticated read/poll and the
// explicit POST that may enqueue a dictionary job. Saved data is always read
// before any provider availability check; a ready entry never triggers a
// provider or catalog call.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"doublangu/internal/analysis"
	"doublangu/internal/dictionary"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/store"
)

// DictionaryHandler serves the explore routes.
type DictionaryHandler struct {
	db       *store.DB
	csrf     CSRFVerifier
	service  *dictionary.Service
	registry providerRegistry
	catalog  *ProviderCatalogService
	profiles *analysis.ProfileStore
}

// NewDictionaryHandler builds the handler. The binding resolver applies the
// shared usableProfileBindings checks to the active profile's translation
// binding only; the linguistic binding is never resolved for dictionary
// generation.
func NewDictionaryHandler(db *store.DB, csrf CSRFVerifier, registry providerRegistry, catalog *ProviderCatalogService) *DictionaryHandler {
	handler := &DictionaryHandler{
		db: db, csrf: csrf, registry: registry, catalog: catalog,
		profiles: analysis.NewProfileStore(db),
	}
	handler.service = dictionary.NewService(db, handler.resolveExploreGeneration)
	return handler
}

// resolveExploreGeneration resolves the active profile's independent Explore
// binding plus its pinned explore generation and correction prompt snapshots
// outside any write transaction. Only the Explore binding is resolved here:
// the translation binding of the same profile is never consulted for
// on-demand explore generation.
func (h *DictionaryHandler) resolveExploreGeneration(ctx context.Context) (dictionary.ResolvedExploreGeneration, error) {
	activeID, err := h.profiles.ActiveProfile(ctx)
	if err != nil || activeID == "" {
		return dictionary.ResolvedExploreGeneration{}, errors.New("no active profile")
	}
	profile, err := h.profiles.Get(ctx, activeID)
	if err != nil {
		return dictionary.ResolvedExploreGeneration{}, err
	}
	stored, err := h.profiles.ExploreBinding(ctx, activeID)
	if err != nil {
		return dictionary.ResolvedExploreGeneration{}, err
	}
	if stored == nil {
		return dictionary.ResolvedExploreGeneration{}, errors.New("active profile has no explore binding")
	}
	// The explore binding rides the translation transport identity through
	// the same usability checks as stage bindings.
	stored.StageID = pipeline.StageTranslation
	bindings, err := usableProfileBindings(ctx, h.registry, h.catalog, []pipeline.BindingSnapshot{*stored})
	if err != nil {
		return dictionary.ResolvedExploreGeneration{}, err
	}
	if len(bindings) == 0 {
		return dictionary.ResolvedExploreGeneration{}, errors.New("active profile has no usable explore binding")
	}
	promptSnapshots, err := h.profiles.ExplorePromptSnapshots(ctx, activeID)
	if err != nil {
		return dictionary.ResolvedExploreGeneration{}, err
	}
	return dictionary.ResolvedExploreGeneration{
		Binding:         bindings[0],
		PromptSnapshots: promptSnapshots,
		ProfileID:       profile.ID,
		ProfileName:     profile.Name,
	}, nil
}

// ServeExplore handles GET and POST /api/v1/articles/{id}/explore.
func (h *DictionaryHandler) ServeExplore(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	articleID, err := library.ParseULID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusNotFound, "article not found", ErrCodeNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.serveLookup(w, r, articleID)
	case http.MethodPost:
		h.serveStart(w, r, articleID)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *DictionaryHandler) referenceFromQuery(r *http.Request) (dictionary.Reference, bool) {
	occurrence := strings.TrimSpace(r.URL.Query().Get("occurrence_id"))
	annotation := strings.TrimSpace(r.URL.Query().Get("annotation_id"))
	if (occurrence == "") == (annotation == "") {
		return dictionary.Reference{}, false
	}
	ref := dictionary.Reference{}
	if occurrence != "" {
		id, err := library.ParseULID(occurrence)
		if err != nil {
			return dictionary.Reference{}, false
		}
		ref.OccurrenceID = id
		return ref, true
	}
	id, err := library.ParseULID(annotation)
	if err != nil {
		return dictionary.Reference{}, false
	}
	ref.AnnotationID = id
	return ref, true
}

func (h *DictionaryHandler) serveLookup(w http.ResponseWriter, r *http.Request, articleID library.ULID) {
	ref, ok := h.referenceFromQuery(r)
	if !ok {
		WriteError(w, http.StatusBadRequest, "exactly one of occurrence_id or annotation_id is required", ErrCodeValidation)
		return
	}
	envelope, _, err := h.service.Lookup(r.Context(), articleID, ref)
	if err != nil {
		h.writeDictionaryError(w, err)
		return
	}
	WriteOK(w, envelope)
}

type dictionaryStartInput struct {
	OccurrenceID string `json:"occurrence_id"`
	AnnotationID string `json:"annotation_id"`
	Retry        *bool  `json:"retry"`
	Regenerate   *bool  `json:"regenerate"`
}

func (h *DictionaryHandler) serveStart(w http.ResponseWriter, r *http.Request, articleID library.ULID) {
	if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
		WriteError(w, http.StatusForbidden, "csrf", ErrCodeCSRF)
		return
	}
	var input dictionaryStartInput
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if (input.OccurrenceID == "") == (input.AnnotationID == "") {
		WriteError(w, http.StatusBadRequest, "exactly one of occurrence_id or annotation_id is required", ErrCodeValidation)
		return
	}
	if input.Retry == nil {
		WriteError(w, http.StatusBadRequest, "retry is required", ErrCodeValidation)
		return
	}
	ref := dictionary.Reference{}
	if input.OccurrenceID != "" {
		id, err := library.ParseULID(input.OccurrenceID)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid occurrence_id", ErrCodeValidation)
			return
		}
		ref.OccurrenceID = id
	} else {
		id, err := library.ParseULID(input.AnnotationID)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid annotation_id", ErrCodeValidation)
			return
		}
		ref.AnnotationID = id
	}
	regenerate := input.Regenerate != nil && *input.Regenerate
	envelope, _, started, err := h.service.Start(r.Context(), articleID, ref, *input.Retry, regenerate)
	if err != nil {
		h.writeDictionaryError(w, err)
		return
	}
	status := http.StatusOK
	if started || envelope.Status == dictionary.StatusQueued || envelope.Status == dictionary.StatusGenerating {
		status = http.StatusAccepted
	}
	WriteJSON(w, status, envelope)
}

// ServeEntry handles GET /api/v1/dictionary/entries/{id}, the article-free
// polling surface. Dictionary records survive deleting the source article.
func (h *DictionaryHandler) ServeEntry(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	entryID, err := library.ParseULID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusNotFound, "dictionary entry not found", ErrCodeNotFound)
		return
	}
	envelope, err := h.service.GetEntryByID(r.Context(), entryID)
	if errors.Is(err, dictionary.ErrNotFound) {
		WriteError(w, http.StatusNotFound, "dictionary entry not found", ErrCodeNotFound)
		return
	}
	if err != nil {
		h.writeDictionaryError(w, err)
		return
	}
	WriteOK(w, envelope)
}

func (h *DictionaryHandler) writeDictionaryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dictionary.ErrNotFound):
		WriteError(w, http.StatusNotFound, "dictionary subject not found", ErrCodeNotFound)
	case errors.Is(err, dictionary.ErrAmbiguousRequest):
		WriteError(w, http.StatusBadRequest, "retry and regenerate are mutually exclusive", ErrCodeValidation)
	case errors.Is(err, dictionary.ErrProviderUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "the configured translation provider is not usable", "v1.dictionary_provider_unavailable")
	default:
		WriteError(w, http.StatusInternalServerError, "dictionary request failed", ErrCodeInternal)
	}
}

package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"

	"doublangu/internal/annotator"
	"doublangu/internal/library"
	"doublangu/internal/media"
	"doublangu/internal/reader"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

// ArticleHandler exposes the authenticated reader API and coordinates one
// enrichment operation per article within a running server process.
type ArticleHandler struct {
	db        *store.DB
	store     *reader.Store
	speech    *speech.Store
	media     *media.Store
	csrf      CSRFVerifier
	annotator annotator.Annotator
	active    map[string]struct{}
	activeMu  sync.Mutex
}

// NewArticleHandler returns an article handler with an injected annotator.
// A nil annotator becomes the explicit disabled provider.
func NewArticleHandler(db *store.DB, csrf CSRFVerifier, provider annotator.Annotator, mediaStores ...*media.Store) *ArticleHandler {
	if provider == nil {
		provider = annotator.Disabled{}
	}
	var mediaStore *media.Store
	if len(mediaStores) > 0 {
		mediaStore = mediaStores[0]
	}
	articleStore := reader.NewStore(db)
	if mediaStore != nil {
		articleStore = reader.NewStoreWithMedia(db, mediaStore)
	}
	return &ArticleHandler{
		db:        db,
		store:     articleStore,
		speech:    speech.NewStore(db),
		media:     mediaStore,
		csrf:      csrf,
		annotator: provider,
		active:    make(map[string]struct{}),
	}
}

// ServeNarration returns the ordered sentence manifest for an article.
func (h *ArticleHandler) ServeNarration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	id, ok := articleID(w, r)
	if !ok {
		return
	}
	narration, err := h.speech.GetNarration(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "article not found", ErrCodeNotFound)
		} else {
			WriteError(w, http.StatusInternalServerError, "narration unavailable", ErrCodeInternal)
		}
		return
	}
	for index := range narration.Clips {
		if narration.Clips[index].Audio != nil {
			narration.Clips[index].Audio.URL = "/api/v1/audio/" + narration.Clips[index].Audio.RenderID.String()
		}
	}
	WriteOK(w, narration)
}

// ServeGenerateNarration queues missing or purged narration using the original
// sentence request identities. It never waits for the Mac worker.
func (h *ArticleHandler) ServeGenerateNarration(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	id, ok := articleID(w, r)
	if !ok {
		return
	}
	article, err := h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	if article.AnalysisStatus != reader.AnalysisReady {
		WriteError(w, http.StatusConflict, "article analysis is not ready; wait for English shadows before generating narration", ErrCodeAnalysisNotReady)
		return
	}
	if err := h.speech.QueueArticleAudio(r.Context(), id, true); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "article not found", ErrCodeNotFound)
		} else {
			WriteError(w, http.StatusInternalServerError, "narration could not be queued", ErrCodeInternal)
		}
		return
	}
	article, err = h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, article)
}

// ServeClearNarration removes article-only long-form bindings and leaves
// lexical pronunciation renders untouched.
func (h *ArticleHandler) ServeClearNarration(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	id, ok := articleID(w, r)
	if !ok {
		return
	}
	result, err := h.speech.ClearNarration(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "article not found", ErrCodeNotFound)
		} else {
			WriteError(w, http.StatusInternalServerError, "narration could not be cleared", ErrCodeInternal)
		}
		return
	}
	if h.media != nil {
		for _, digest := range result.CleanupDigests {
			if _, cleanupErr := h.media.CleanupOrphan(r.Context(), h.storeDB(), digest); cleanupErr != nil {
				WriteError(w, http.StatusInternalServerError, "narration cleanup is incomplete", ErrCodeInternal)
				return
			}
		}
	}
	WriteOK(w, result)
}

func (h *ArticleHandler) storeDB() *store.DB {
	return h.db
}

// ServeArticles dispatches the article collection GET and POST routes.
func (h *ArticleHandler) ServeArticles(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listArticles(w, r)
	case http.MethodPost:
		h.createArticle(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

// ServeArticle handles GET /api/v1/articles/{id}.
func (h *ArticleHandler) ServeArticle(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	id, ok := articleID(w, r)
	if !ok {
		return
	}
	article, err := h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteOK(w, article)
}

// ServeEnrich handles synchronous POST /api/v1/articles/{id}/enrich.
func (h *ArticleHandler) ServeEnrich(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	id, ok := articleID(w, r)
	if !ok {
		return
	}
	key := id.String()
	if !h.claimEnrichment(key) {
		WriteError(w, http.StatusConflict, "enrichment is already in progress", ErrCodeEnrichmentInProgress)
		return
	}
	defer h.releaseEnrichment(key)

	article, err := h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	input, err := article.AnnotatorInput()
	if err != nil {
		writeReaderError(w, err)
		return
	}
	if err := h.store.MarkProcessing(r.Context(), id); err != nil {
		var typed *reader.Error
		if errors.As(err, &typed) && typed.Kind == reader.KindInProgress {
			WriteError(w, http.StatusConflict, "enrichment is already in progress", ErrCodeEnrichmentInProgress)
			return
		}
		writeReaderError(w, err)
		return
	}

	candidates, err := h.annotator.Annotate(r.Context(), input)
	if err != nil {
		code := annotator.CodeOf(err)
		_ = h.store.MarkFailed(r.Context(), id, code)
		log.Printf("article enrichment failed article_id=%s code=%s error=%v", id, code, err)
		status, message := enrichmentErrorResponse(code)
		WriteError(w, status, message, code)
		return
	}
	normalized, err := reader.NormalizeCandidates(article, candidates)
	if err != nil {
		code := annotator.CodeInvalidOutput
		_ = h.store.MarkFailed(r.Context(), id, code)
		WriteError(w, http.StatusBadGateway, "Codex returned invalid article annotations", code)
		return
	}
	if err := h.store.ReplaceAnnotations(r.Context(), id, normalized.Annotations); err != nil {
		code := annotator.CodeInvalidOutput
		_ = h.store.MarkFailed(r.Context(), id, code)
		writeReaderError(w, err)
		return
	}
	ready, err := h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteOK(w, ready)
}

// ServeEnrichQueued is the documented compatibility alias. New callers use
// /reanalyze; this route only queues durable work and never holds the browser
// request open for Codex.
func (h *ArticleHandler) ServeEnrichQueued(w http.ResponseWriter, r *http.Request) {
	h.serveQueuedAnalysis(w, r, false)
}

// ServeReanalyze queues an explicit owner-requested analysis revision while
// leaving the last accepted v2 materialization readable until replacement.
func (h *ArticleHandler) ServeReanalyze(w http.ResponseWriter, r *http.Request) {
	h.serveQueuedAnalysis(w, r, true)
}

func (h *ArticleHandler) serveQueuedAnalysis(w http.ResponseWriter, r *http.Request, force bool) {
	if !h.requireMutation(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	id, ok := articleID(w, r)
	if !ok {
		return
	}
	fresh := false
	if force {
		var input struct {
			Fresh json.RawMessage `json:"fresh"`
		}
		if err := decodeOptionalJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid reanalysis request", ErrCodeValidation)
			return
		}
		switch strings.TrimSpace(string(input.Fresh)) {
		case "":
		case "true":
			fresh = true
		case "false":
		default:
			WriteError(w, http.StatusBadRequest, "invalid reanalysis request", ErrCodeValidation)
			return
		}
	}
	if _, err := h.store.QueueAnalysis(r.Context(), id, force, fresh); err != nil {
		writeReaderError(w, err)
		return
	}
	article, err := h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, article)
}

// ServeLearningState handles idempotent PUT /api/v1/learning-state.
func (h *ArticleHandler) ServeLearningState(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	var input struct {
		SemanticSenseID     string                `json:"semantic_sense_id"`
		ArticleOccurrenceID string                `json:"article_occurrence_id"`
		SourceLanguage      string                `json:"source_language"`
		Kind                reader.AnnotationKind `json:"kind"`
		LearningKey         string                `json:"learning_key"`
		Status              reader.LearningStatus `json:"status"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if input.SemanticSenseID != "" {
		senseID, err := library.ParseULID(input.SemanticSenseID)
		if err != nil || senseID.IsZero() {
			WriteError(w, http.StatusBadRequest, "invalid semantic sense id", ErrCodeValidation)
			return
		}
		var occurrenceID library.ULID
		if input.ArticleOccurrenceID != "" {
			occurrenceID, err = library.ParseULID(input.ArticleOccurrenceID)
			if err != nil || occurrenceID.IsZero() {
				WriteError(w, http.StatusBadRequest, "invalid article occurrence id", ErrCodeValidation)
				return
			}
		}
		state, err := h.store.UpsertSemanticLearningState(r.Context(), senseID, input.Status, occurrenceID)
		if err != nil {
			writeReaderError(w, err)
			return
		}
		WriteOK(w, state)
		return
	}
	state := reader.LearningState{
		SourceLanguage: input.SourceLanguage,
		Kind:           input.Kind,
		LearningKey:    input.LearningKey,
		Status:         input.Status,
	}
	stored, err := h.store.UpsertLearningState(r.Context(), &state)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteOK(w, stored)
}

func (h *ArticleHandler) requireMutation(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete:
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return false
		}
	}
	return true
}

func (h *ArticleHandler) listArticles(w http.ResponseWriter, r *http.Request) {
	articles, err := h.store.ListArticles(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "list articles failed", ErrCodeInternal)
		return
	}
	WriteOK(w, articles)
}

func (h *ArticleHandler) createArticle(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title          string `json:"title"`
		Body           string `json:"body"`
		SourceLanguage string `json:"source_language"`
		TargetLanguage string `json:"target_language"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	article, err := reader.NewArticle(input.Title, input.Body, input.SourceLanguage, input.TargetLanguage)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	if err := h.store.CreateArticleQueued(r.Context(), &article); err != nil {
		writeReaderError(w, err)
		return
	}
	created, err := h.store.GetArticle(r.Context(), article.ID)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, created)
}

func (h *ArticleHandler) claimEnrichment(id string) bool {
	h.activeMu.Lock()
	defer h.activeMu.Unlock()
	if _, exists := h.active[id]; exists {
		return false
	}
	h.active[id] = struct{}{}
	return true
}

func (h *ArticleHandler) releaseEnrichment(id string) {
	h.activeMu.Lock()
	delete(h.active, id)
	h.activeMu.Unlock()
}

func articleID(w http.ResponseWriter, r *http.Request) (library.ULID, bool) {
	value := strings.TrimSpace(r.PathValue("id"))
	id, err := library.ParseULID(value)
	if err != nil || id.IsZero() {
		WriteError(w, http.StatusBadRequest, "invalid article id", ErrCodeValidation)
		return "", false
	}
	return id, true
}

func writeReaderError(w http.ResponseWriter, err error) {
	var typed *reader.Error
	if errors.As(err, &typed) {
		switch typed.Kind {
		case reader.KindNotFound:
			WriteError(w, http.StatusNotFound, typed.Error(), ErrCodeNotFound)
		case reader.KindValidation:
			WriteError(w, http.StatusBadRequest, typed.Error(), ErrCodeValidation)
		case reader.KindInProgress:
			WriteError(w, http.StatusConflict, "enrichment is already in progress", ErrCodeEnrichmentInProgress)
		case reader.KindConflict:
			WriteError(w, http.StatusConflict, typed.Error(), ErrCodeConflict)
		default:
			WriteError(w, http.StatusInternalServerError, "internal error", ErrCodeInternal)
		}
		return
	}
	WriteError(w, http.StatusInternalServerError, "internal error", ErrCodeInternal)
}

func enrichmentErrorResponse(code string) (int, string) {
	switch code {
	case annotator.CodeUnavailable:
		return http.StatusServiceUnavailable, "article annotator unavailable"
	case annotator.CodeNotAuthenticated:
		return http.StatusServiceUnavailable, "Codex is not authenticated"
	case annotator.CodeTimeout:
		return http.StatusGatewayTimeout, "article enrichment timed out; please retry"
	case annotator.CodeInvalidInput:
		return http.StatusBadRequest, "article cannot be enriched"
	case annotator.CodeInvalidOutput:
		return http.StatusBadGateway, "Codex returned invalid article annotations"
	case annotator.CodeProtocol, annotator.CodeProviderFailure:
		return http.StatusBadGateway, "article enrichment failed; please retry"
	default:
		return http.StatusBadGateway, "article enrichment failed; please retry"
	}
}

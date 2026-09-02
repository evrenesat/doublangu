package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

const (
	ErrCodeAnalysisModelUnavailable   = "v1.analysis_model_unavailable"
	ErrCodeAnalysisInvalidSelection   = "v1.analysis_invalid_selection"
	ErrCodeAnalysisHistoryUnavailable = "v1.analysis_history_unavailable"
)

type AnalysisModelsResponse struct {
	Models      []annotator.Model `json:"models"`
	RetrievedAt string            `json:"retrieved_at"`
	Stale       bool              `json:"stale"`
	LastError   string            `json:"last_error,omitempty"`
}

type AnalysisSettingsInput struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type AnalysisHandler struct {
	settings *analysis.SettingsStore
	history  *analysis.HistoryStore
	csrf     CSRFVerifier
	provider annotator.ModelCatalogProvider

	catalogMu  sync.Mutex
	models     []annotator.Model
	lastGoodAt time.Time
	lastError  string
}

func NewAnalysisHandler(db *store.DB, csrf CSRFVerifier, provider annotator.ModelCatalogProvider) *AnalysisHandler {
	return &AnalysisHandler{
		settings: analysis.NewSettingsStore(db), history: analysis.NewHistoryStore(db),
		csrf: csrf, provider: provider,
	}
}

func (h *AnalysisHandler) ServeModels(w http.ResponseWriter, r *http.Request) {
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
	snapshot, catalogErr := h.loadCatalog(r.Context(), refresh)
	if len(snapshot.Models) == 0 && catalogErr != nil {
		WriteError(w, http.StatusServiceUnavailable, "analysis model catalog unavailable", ErrCodeAnalysisModelUnavailable)
		return
	}
	WriteOK(w, AnalysisModelsResponse{Models: snapshot.Models, RetrievedAt: snapshot.RetrievedAt, Stale: snapshot.Stale, LastError: snapshot.LastError})
}

func (h *AnalysisHandler) ServeSettings(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method == http.MethodPut && !h.requireMutation(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := h.settings.Get(r.Context())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "analysis settings unavailable", ErrCodeInternal)
			return
		}
		WriteOK(w, settings)
	case http.MethodPut:
		var input AnalysisSettingsInput
		if err := decodeJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid analysis settings", ErrCodeValidation)
			return
		}
		current, err := h.settings.Get(r.Context())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "analysis settings unavailable", ErrCodeInternal)
			return
		}
		model := strings.TrimSpace(input.Model)
		effort := strings.TrimSpace(input.Effort)
		snapshot, catalogErr := h.loadCatalog(r.Context(), false)
		if len(snapshot.Models) == 0 {
			if catalogErr != nil {
				WriteError(w, http.StatusServiceUnavailable, "analysis model catalog unavailable", ErrCodeAnalysisModelUnavailable)
				return
			}
			WriteError(w, http.StatusServiceUnavailable, "analysis model catalog is empty", ErrCodeAnalysisModelUnavailable)
			return
		}
		if snapshot.Stale && (model != current.Model || effort != current.Effort) {
			WriteError(w, http.StatusBadRequest, "refresh the analysis model catalog before changing the selection", ErrCodeAnalysisInvalidSelection)
			return
		}
		if !annotator.SupportsSelection(snapshot.Models, model, effort) {
			WriteError(w, http.StatusBadRequest, "model and reasoning effort are not supported", ErrCodeAnalysisInvalidSelection)
			return
		}
		settings, err := h.settings.Save(r.Context(), model, effort)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "analysis settings unavailable", ErrCodeInternal)
			return
		}
		WriteOK(w, settings)
	default:
		w.Header().Set("Allow", "GET, PUT")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *AnalysisHandler) ServeRuns(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	articleID := strings.TrimSpace(r.URL.Query().Get("article_id"))
	if articleID != "" {
		id, err := library.ParseULID(articleID)
		if err != nil || id.IsZero() {
			WriteError(w, http.StatusBadRequest, "invalid article id", ErrCodeValidation)
			return
		}
		articleID = id.String()
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid analysis run limit", ErrCodeValidation)
		return
	}
	page, err := h.history.ListRuns(r.Context(), articleID, limit, strings.TrimSpace(r.URL.Query().Get("cursor")))
	if err != nil {
		if errors.Is(err, analysis.ErrInvalidRunQuery) {
			WriteError(w, http.StatusBadRequest, "invalid analysis run query", ErrCodeValidation)
		} else {
			WriteError(w, http.StatusInternalServerError, "analysis history unavailable", ErrCodeAnalysisHistoryUnavailable)
		}
		return
	}
	WriteOK(w, page)
}

func (h *AnalysisHandler) ServeRun(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	runID, err := library.ParseULID(strings.TrimSpace(r.PathValue("id")))
	if err != nil || runID.IsZero() {
		WriteError(w, http.StatusBadRequest, "invalid analysis run id", ErrCodeValidation)
		return
	}
	run, err := h.history.GetRun(r.Context(), runID)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "analysis run not found", ErrCodeNotFound)
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "analysis run unavailable", ErrCodeAnalysisHistoryUnavailable)
		return
	}
	WriteOK(w, run)
}

type catalogSnapshot struct {
	Models      []annotator.Model
	RetrievedAt string
	Stale       bool
	LastError   string
}

func (h *AnalysisHandler) loadCatalog(ctx context.Context, refresh bool) (catalogSnapshot, error) {
	h.catalogMu.Lock()
	defer h.catalogMu.Unlock()
	if !refresh && len(h.models) > 0 && time.Since(h.lastGoodAt) < 5*time.Minute {
		return h.catalogSnapshotLocked(), nil
	}
	if h.provider == nil {
		h.lastError = "analysis model provider is unavailable"
		if len(h.models) > 0 {
			return h.catalogSnapshotLocked(), errors.New(h.lastError)
		}
		return catalogSnapshot{}, errors.New(h.lastError)
	}
	models, err := h.provider.ListModels(ctx)
	if err != nil {
		h.lastError = err.Error()
		if len(h.models) > 0 {
			return h.catalogSnapshotLocked(), err
		}
		return catalogSnapshot{}, err
	}
	if len(models) == 0 {
		h.lastError = "model catalog is empty"
		if len(h.models) > 0 {
			return h.catalogSnapshotLocked(), errors.New(h.lastError)
		}
		return catalogSnapshot{}, errors.New(h.lastError)
	}
	h.models = append([]annotator.Model(nil), models...)
	h.lastGoodAt = time.Now()
	h.lastError = ""
	return h.catalogSnapshotLocked(), nil
}

func (h *AnalysisHandler) catalogSnapshotLocked() catalogSnapshot {
	snapshot := catalogSnapshot{
		Models:    append([]annotator.Model(nil), h.models...),
		LastError: h.lastError,
	}
	if !h.lastGoodAt.IsZero() {
		snapshot.RetrievedAt = h.lastGoodAt.UTC().Format(time.RFC3339Nano)
	}
	snapshot.Stale = h.lastError != ""
	return snapshot
}

func (h *AnalysisHandler) requireMutation(w http.ResponseWriter, r *http.Request) bool {
	if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
		WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
		return false
	}
	return true
}

func parseRefresh(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New("invalid refresh")
	}
}

func parseLimit(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 20, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 50 {
		return 0, errors.New("invalid limit")
	}
	return limit, nil
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"doublangu/internal/analysis"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

const (
	ErrCodeAnalysisHistoryUnavailable = "v1.analysis_history_unavailable"
)

type AnalysisHandler struct {
	history *analysis.HistoryStore
}

func NewAnalysisHandler(db *store.DB) *AnalysisHandler {
	return &AnalysisHandler{
		history: analysis.NewHistoryStore(db),
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

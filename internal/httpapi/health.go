package httpapi

import (
	"net/http"

	"doublangu/internal/store"
)

// HealthHandler serves liveness and readiness endpoints.
type HealthHandler struct {
	DB *store.DB
}

// NewHealthHandler returns a HealthHandler using the provided database for
// readiness checks.
func NewHealthHandler(db *store.DB) *HealthHandler {
	return &HealthHandler{DB: db}
}

// ServeLive handles GET /live — always reports "ok" if the server can respond.
func (h *HealthHandler) ServeLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	WriteOK(w, LivenessResponse{Status: "ok"})
}

// ServeReady handles GET /ready — reports "ok" when the database is reachable.
func (h *HealthHandler) ServeReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}

	checks := map[string]string{}

	// Database readiness check.
	dbStatus := "ok"
	if h.DB == nil {
		dbStatus = "unavailable"
	} else if err := h.DB.Conn().Ping(); err != nil {
		dbStatus = "error: " + err.Error()
	}
	checks["database"] = dbStatus

	status := "ok"
	if dbStatus != "ok" {
		status = "degraded"
	}

	if status != "ok" {
		WriteError(w, http.StatusServiceUnavailable, "service not ready", ErrCodeUnavailable)
		return
	}

	resp := ReadinessResponse{
		Status:   status,
		Checks:   checks,
		Database: dbStatus,
	}

	WriteOK(w, resp)
}

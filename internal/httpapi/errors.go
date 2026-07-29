// Package httpapi provides shared HTTP API infrastructure for the Doublangu server.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// APIError is the structured, versioned error envelope for all API responses.
type APIError struct {
	Error      string `json:"error"`
	Code       string `json:"code"`
	StatusCode int    `json:"-"`
}

// Common error codes.
const (
	ErrCodeValidation     = "v1.validation_error"
	ErrCodeAuth           = "v1.authentication_error"
	ErrCodeCSRF           = "v1.csrf_error"
	ErrCodeRateLimit      = "v1.rate_limit_error"
	ErrCodeNotFound       = "v1.not_found"
	ErrCodeMethodNotAllow = "v1.method_not_allowed"
	ErrCodeUnavailable    = "v1.service_unavailable"
	ErrCodeInternal       = "v1.internal_error"
)

// WriteError writes a structured JSON error response.
func WriteError(w http.ResponseWriter, statusCode int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(APIError{
		Error:      message,
		Code:       code,
		StatusCode: statusCode,
	})
}

// WriteOK writes a 200 OK JSON response with the given payload.
func WriteOK(w http.ResponseWriter, payload interface{}) {
	WriteJSON(w, http.StatusOK, payload)
}

// WriteJSON writes a JSON response with the supplied status. Non-2xx API
// callers must use WriteError so clients always receive the error envelope.
func WriteJSON(w http.ResponseWriter, statusCode int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

// HealthStatus is returned by the liveness and readiness endpoints.
type HealthStatus struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// LivenessResponse is returned by GET /live.
type LivenessResponse struct {
	Status string `json:"status"`
}

// ReadinessResponse is returned by GET /ready.
type ReadinessResponse struct {
	Status   string            `json:"status"`
	Checks   map[string]string `json:"checks,omitempty"`
	Database string            `json:"database,omitempty"`
}

// Version is the API version string reported in health endpoints.
const Version = "v1"

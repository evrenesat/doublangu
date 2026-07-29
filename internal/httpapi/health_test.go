package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"doublangu/internal/store"
)

func TestServeLive(t *testing.T) {
	handler := NewHealthHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	rec := httptest.NewRecorder()
	handler.ServeLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp LivenessResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
}

func TestServeLiveMethodNotAllowed(t *testing.T) {
	handler := NewHealthHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/live", nil)
	rec := httptest.NewRecorder()
	handler.ServeLive(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
	var apiErr APIError
	if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if apiErr.Code != ErrCodeMethodNotAllow {
		t.Errorf("expected code %q, got %q", ErrCodeMethodNotAllow, apiErr.Code)
	}
}

func TestServeReadyOK(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	defer db.Close()

	handler := NewHealthHandler(db)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	handler.ServeReady(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp ReadinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	if resp.Database != "ok" {
		t.Errorf("expected database ok, got %q", resp.Database)
	}
	if dbStatus, ok := resp.Checks["database"]; !ok || dbStatus != "ok" {
		t.Errorf("expected checks.database=ok, got %v", resp.Checks)
	}
}

func TestServeReadyDatabaseUnavailable(t *testing.T) {
	handler := NewHealthHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	handler.ServeReady(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != ErrCodeUnavailable || resp.Error == "" {
		t.Errorf("expected structured unavailable error, got %+v", resp)
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, "thing not found", ErrCodeNotFound)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
	var apiErr APIError
	if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if apiErr.Error != "thing not found" {
		t.Errorf("error = %q", apiErr.Error)
	}
	if apiErr.Code != ErrCodeNotFound {
		t.Errorf("code = %q", apiErr.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestWriteOK(t *testing.T) {
	rec := httptest.NewRecorder()
	payload := map[string]string{"key": "value"}
	WriteOK(rec, payload)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var decoded map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["key"] != "value" {
		t.Errorf("payload = %v", decoded)
	}
}

func TestAPIErrorOmitStatusCodeInJSON(t *testing.T) {
	// The json:"-" tag should prevent StatusCode from being marshaled.
	apiErr := APIError{Error: "test", Code: "test_code", StatusCode: 500}
	data, err := json.Marshal(apiErr)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, exists := m["StatusCode"]; exists {
		t.Error("StatusCode should not appear in JSON (json:\"-\")")
	}
	if _, exists := m["status_code"]; exists {
		t.Error("status_code should not appear in JSON")
	}
}

package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/httpapi"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

type readerSettingsResponse struct {
	PronounceOnHover bool   `json:"pronounce_on_hover"`
	UpdatedAt        string `json:"updated_at"`
}

func TestReaderSettingsDefaultsStrictUpdatesAndCSRF(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Run("defaults enabled and cache-free", func(t *testing.T) {
		h := httpapi.NewReaderSettingsHandler(db, allowArticleCSRF{})
		rec := httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodGet, "/api/v1/reader/settings", ""))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("settings response = %d cache %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		settings := decodeJSON[readerSettingsResponse](t, rec.Body.String())
		if !settings.PronounceOnHover {
			t.Fatalf("default settings = %+v", settings)
		}
	})

	t.Run("PUT persists and GET reflects the value", func(t *testing.T) {
		h := httpapi.NewReaderSettingsHandler(db, allowArticleCSRF{})
		rec := httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodPut, "/api/v1/reader/settings", `{"pronounce_on_hover":false}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT status = %d body=%s", rec.Code, rec.Body.String())
		}
		settings := decodeJSON[readerSettingsResponse](t, rec.Body.String())
		if settings.PronounceOnHover || settings.UpdatedAt == "" {
			t.Fatalf("updated settings = %+v", settings)
		}
		rec = httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodGet, "/api/v1/reader/settings", ""))
		settings = decodeJSON[readerSettingsResponse](t, rec.Body.String())
		if settings.PronounceOnHover {
			t.Fatalf("GET after PUT = %+v", settings)
		}
		// A fresh handler on the same database still sees the persisted value.
		again := httpapi.NewReaderSettingsHandler(db, allowArticleCSRF{})
		rec = httptest.NewRecorder()
		again.ServeSettings(rec, authedRequest(http.MethodGet, "/api/v1/reader/settings", ""))
		settings = decodeJSON[readerSettingsResponse](t, rec.Body.String())
		if settings.PronounceOnHover || settings.UpdatedAt == "" {
			t.Fatalf("persisted settings = %+v", settings)
		}
	})

	t.Run("strict JSON and CSRF", func(t *testing.T) {
		h := httpapi.NewReaderSettingsHandler(db, &testCSRF{shouldError: true})
		rec := httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodPut, "/api/v1/reader/settings", `{"pronounce_on_hover":false}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("CSRF failure status = %d", rec.Code)
		}
		h = httpapi.NewReaderSettingsHandler(db, allowArticleCSRF{})
		rec = httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodPut, "/api/v1/reader/settings", `{"pronounce_on_hover":"yes"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed PUT status = %d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), httpapi.ErrCodeValidation) {
			t.Fatalf("malformed PUT body = %s", rec.Body.String())
		}
	})

	t.Run("empty payload is rejected and the preference is untouched", func(t *testing.T) {
		h := httpapi.NewReaderSettingsHandler(db, allowArticleCSRF{})
		rec := httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodPut, "/api/v1/reader/settings", `{"pronounce_on_hover":true}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("baseline PUT status = %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodPut, "/api/v1/reader/settings", `{}`))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), httpapi.ErrCodeValidation) {
			t.Fatalf("empty PUT status = %d body=%s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		h.ServeSettings(rec, authedRequest(http.MethodGet, "/api/v1/reader/settings", ""))
		settings := decodeJSON[readerSettingsResponse](t, rec.Body.String())
		if !settings.PronounceOnHover {
			t.Fatalf("preference changed by an empty payload: %+v", settings)
		}
	})
}

func TestReaderSettingsStoreRoundTrip(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := reader.NewStore(db)
	settings, err := articles.GetReaderSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.PronounceOnHover {
		t.Fatalf("migration seed = %+v", settings)
	}
	updated, err := articles.SetReaderSettings(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PronounceOnHover || updated.UpdatedAt == "" {
		t.Fatalf("updated = %+v", updated)
	}
	loaded, err := articles.GetReaderSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PronounceOnHover || loaded.UpdatedAt != updated.UpdatedAt {
		t.Fatalf("round trip = %+v", loaded)
	}
}

package httpapi

import (
	"net/http"

	"doublangu/internal/reader"
	"doublangu/internal/store"
)

type ReaderSettingsHandler struct {
	store *reader.Store
	csrf  CSRFVerifier
}

func NewReaderSettingsHandler(db *store.DB, csrf CSRFVerifier) *ReaderSettingsHandler {
	return &ReaderSettingsHandler{store: reader.NewStore(db), csrf: csrf}
}

type readerSettingsInput struct {
	// Pointer so a missing field is distinguishable from an explicit false:
	// an empty payload must never silently rewrite the owner preference.
	PronounceOnHover *bool `json:"pronounce_on_hover"`
}

// ServeSettings exposes the owner-wide pronounce-on-hover preference. GET is
// cache-free and PUT requires CSRF; responses never store intermediate data.
func (h *ReaderSettingsHandler) ServeSettings(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method == http.MethodPut {
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := h.store.GetReaderSettings(r.Context())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "reader settings unavailable", ErrCodeInternal)
			return
		}
		WriteOK(w, settings)
	case http.MethodPut:
		var input readerSettingsInput
		if err := decodeJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid reader settings", ErrCodeValidation)
			return
		}
		if input.PronounceOnHover == nil {
			WriteError(w, http.StatusBadRequest, "pronounce_on_hover is required", ErrCodeValidation)
			return
		}
		settings, err := h.store.SetReaderSettings(r.Context(), *input.PronounceOnHover)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "reader settings unavailable", ErrCodeInternal)
			return
		}
		WriteOK(w, settings)
	default:
		w.Header().Set("Allow", "GET, PUT")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

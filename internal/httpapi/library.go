// Package httpapi provides shared HTTP API infrastructure for the Doublangu server.
package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

// CSRFVerifier is the interface that auth.CSRF satisfies for verifying
// double-submit cookie tokens on state-changing requests.
type CSRFVerifier interface {
	VerifyRequest(r *http.Request) error
}

// ErrCodeConflict is returned when a uniqueness or foreign-key constraint is violated.
const ErrCodeConflict = "v1.conflict"

// LibraryHandler exposes authenticated CRUD routes for library metadata records.
// Every mutation requires a valid CSRF double-submit cookie/header pair.
type LibraryHandler struct {
	store *library.Store
	db    *store.DB
	csrf  CSRFVerifier
}

// NewLibraryHandler returns a LibraryHandler backed by the supplied database
// and CSRF protector.
func NewLibraryHandler(db *store.DB, csrf CSRFVerifier) *LibraryHandler {
	return &LibraryHandler{store: &library.Store{}, db: db, csrf: csrf}
}

// requireMutation verifies CSRF before a mutating route reads path or body
// input. Authentication is deliberately supplied by the assembled route wrapper.
func (h *LibraryHandler) requireMutation(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete:
		if err := h.csrf.VerifyRequest(r); err != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return false
		}
	}
	return true
}

func getRecord[T any](h *LibraryHandler, w http.ResponseWriter, r *http.Request, id library.ULID, get func(context.Context, *sql.Tx, library.ULID) (*T, error)) {
	var record *T
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var getErr error
		record, getErr = get(r.Context(), tx, id)
		return getErr
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteOK(w, record)
}

func createAndRead[T any](h *LibraryHandler, w http.ResponseWriter, r *http.Request, create func(*sql.Tx) error, id library.ULID, get func(context.Context, *sql.Tx, library.ULID) (*T, error)) {
	if err := h.db.WithTransaction(r.Context(), create); err != nil {
		h.writeStoreError(w, err)
		return
	}
	var record *T
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var getErr error
		record, getErr = get(r.Context(), tx, id)
		return getErr
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, record)
}

func deleteRecord(h *LibraryHandler, w http.ResponseWriter, r *http.Request, id library.ULID, delete func(context.Context, *sql.Tx, library.ULID) error) {
	if err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error { return delete(r.Context(), tx, id) }); err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteOK(w, map[string]bool{"ok": true})
}

func decodeJSONObject(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func decodeOptionalJSONObject(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("request body must contain exactly one JSON object")
	}
	objectDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(target); err != nil {
		return err
	}
	if err := objectDecoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

// --- Libraries ---

// ServeLibraries dispatches list (GET) and create (POST) for libraries.
func (h *LibraryHandler) ServeLibraries(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listLibraries(w, r)
	case http.MethodPost:
		h.createLibrary(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) listLibraries(w http.ResponseWriter, r *http.Request) {
	var records []library.Library
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var listErr error
		records, listErr = h.store.ListLibraries(r.Context(), tx)
		return listErr
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "list libraries failed", ErrCodeInternal)
		return
	}
	WriteOK(w, records)
}

func (h *LibraryHandler) createLibrary(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name           string `json:"name"`
		SourceLanguage string `json:"source_language"`
		TargetLanguage string `json:"target_language"`
		Description    string `json:"description"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		WriteError(w, http.StatusBadRequest, "name is required", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.SourceLanguage) == "" {
		WriteError(w, http.StatusBadRequest, "source_language is required", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.TargetLanguage) == "" {
		WriteError(w, http.StatusBadRequest, "target_language is required", ErrCodeValidation)
		return
	}

	record, err := library.NewLibrary(input.Name, input.SourceLanguage, input.TargetLanguage, input.Description)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
		return
	}

	createAndRead(h, w, r, func(tx *sql.Tx) error { return h.store.CreateLibrary(r.Context(), tx, &record) }, record.ID, h.store.GetLibrary)
}

// ServeLibrary dispatches get (GET), update (PUT), and delete (DELETE) for a single library.
func (h *LibraryHandler) ServeLibrary(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	id := library.ULID(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(id.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid library id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getRecord(h, w, r, id, h.store.GetLibrary)
	case http.MethodPut:
		h.updateLibrary(w, r, id)
	case http.MethodDelete:
		deleteRecord(h, w, r, id, h.store.DeleteLibrary)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) updateLibrary(w http.ResponseWriter, r *http.Request, id library.ULID) {
	var input struct {
		Name           *string `json:"name"`
		SourceLanguage *string `json:"source_language"`
		TargetLanguage *string `json:"target_language"`
		Description    *string `json:"description"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}

	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		existing, err := h.store.GetLibrary(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if input.Name != nil {
			if strings.TrimSpace(*input.Name) == "" {
				return &library.Error{Op: "update library", Kind: library.KindValidation, Err: errValidation("name must not be empty")}
			}
			existing.Name = *input.Name
		}
		if input.SourceLanguage != nil {
			existing.SourceLanguage = *input.SourceLanguage
		}
		if input.TargetLanguage != nil {
			existing.TargetLanguage = *input.TargetLanguage
		}
		if input.Description != nil {
			existing.Description = *input.Description
		}
		return h.store.UpdateLibrary(r.Context(), tx, existing)
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	getRecord(h, w, r, id, h.store.GetLibrary)
}

// --- Works ---

// ServeWorksByLibrary dispatches list (GET) and create (POST) for works under a library.
func (h *LibraryHandler) ServeWorksByLibrary(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	libID := library.ULID(r.PathValue("id"))
	if libID == "" {
		WriteError(w, http.StatusBadRequest, "library id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(libID.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid library id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listWorks(w, r, libID)
	case http.MethodPost:
		h.createWork(w, r, libID)
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) listWorks(w http.ResponseWriter, r *http.Request, libID library.ULID) {
	var records []library.Work
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var listErr error
		records, listErr = h.store.ListWorksByLibrary(r.Context(), tx, libID)
		return listErr
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteOK(w, records)
}

func (h *LibraryHandler) createWork(w http.ResponseWriter, r *http.Request, libID library.ULID) {
	var input struct {
		Title     string `json:"title"`
		Author    string `json:"author"`
		Kind      string `json:"kind"`
		SourceURL string `json:"source_url"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Title) == "" {
		WriteError(w, http.StatusBadRequest, "title is required", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Kind) == "" {
		WriteError(w, http.StatusBadRequest, "kind is required", ErrCodeValidation)
		return
	}

	record, err := library.NewWork(libID, input.Title, input.Author, input.Kind, input.SourceURL)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
		return
	}

	createAndRead(h, w, r, func(tx *sql.Tx) error { return h.store.CreateWork(r.Context(), tx, &record) }, record.ID, h.store.GetWork)
}

// ServeWork dispatches get (GET), update (PUT), and delete (DELETE) for a single work.
func (h *LibraryHandler) ServeWork(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	id := library.ULID(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(id.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid work id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getRecord(h, w, r, id, h.store.GetWork)
	case http.MethodPut:
		h.updateWork(w, r, id)
	case http.MethodDelete:
		deleteRecord(h, w, r, id, h.store.DeleteWork)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) updateWork(w http.ResponseWriter, r *http.Request, id library.ULID) {
	var input struct {
		LibraryID *string `json:"library_id"`
		Title     *string `json:"title"`
		Author    *string `json:"author"`
		Kind      *string `json:"kind"`
		SourceURL *string `json:"source_url"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}

	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		existing, err := h.store.GetWork(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if input.LibraryID != nil {
			parsed, err := library.ParseULID(*input.LibraryID)
			if err != nil {
				return &library.Error{Op: "update work", Kind: library.KindValidation, Err: errValidation("invalid library_id")}
			}
			existing.LibraryID = parsed
		}
		if input.Title != nil {
			if strings.TrimSpace(*input.Title) == "" {
				return &library.Error{Op: "update work", Kind: library.KindValidation, Err: errValidation("title must not be empty")}
			}
			existing.Title = *input.Title
		}
		if input.Author != nil {
			existing.Author = *input.Author
		}
		if input.Kind != nil {
			if strings.TrimSpace(*input.Kind) == "" {
				return &library.Error{Op: "update work", Kind: library.KindValidation, Err: errValidation("kind must not be empty")}
			}
			existing.Kind = *input.Kind
		}
		if input.SourceURL != nil {
			existing.SourceURL = *input.SourceURL
		}
		return h.store.UpdateWork(r.Context(), tx, existing)
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	getRecord(h, w, r, id, h.store.GetWork)
}

// --- Editions ---

// ServeEditionsByWork dispatches list (GET) and create (POST) for editions under a work.
func (h *LibraryHandler) ServeEditionsByWork(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	workID := library.ULID(r.PathValue("id"))
	if workID == "" {
		WriteError(w, http.StatusBadRequest, "work id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(workID.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid work id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listEditions(w, r, workID)
	case http.MethodPost:
		h.createEdition(w, r, workID)
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) listEditions(w http.ResponseWriter, r *http.Request, workID library.ULID) {
	var records []library.Edition
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var listErr error
		records, listErr = h.store.ListEditionsByWork(r.Context(), tx, workID)
		return listErr
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteOK(w, records)
}

func (h *LibraryHandler) createEdition(w http.ResponseWriter, r *http.Request, workID library.ULID) {
	var input struct {
		Name     string `json:"name"`
		Language string `json:"language"`
		Format   string `json:"format"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		WriteError(w, http.StatusBadRequest, "name is required", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Language) == "" {
		WriteError(w, http.StatusBadRequest, "language is required", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Format) == "" {
		WriteError(w, http.StatusBadRequest, "format is required", ErrCodeValidation)
		return
	}

	record, err := library.NewEdition(workID, input.Name, input.Language, input.Format)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
		return
	}

	createAndRead(h, w, r, func(tx *sql.Tx) error { return h.store.CreateEdition(r.Context(), tx, &record) }, record.ID, h.store.GetEdition)
}

// ServeEdition dispatches get (GET), update (PUT), and delete (DELETE) for a single edition.
func (h *LibraryHandler) ServeEdition(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	id := library.ULID(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(id.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid edition id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getRecord(h, w, r, id, h.store.GetEdition)
	case http.MethodPut:
		h.updateEdition(w, r, id)
	case http.MethodDelete:
		deleteRecord(h, w, r, id, h.store.DeleteEdition)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) updateEdition(w http.ResponseWriter, r *http.Request, id library.ULID) {
	var input struct {
		WorkID   *string `json:"work_id"`
		Name     *string `json:"name"`
		Language *string `json:"language"`
		Format   *string `json:"format"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}

	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		existing, err := h.store.GetEdition(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if input.WorkID != nil {
			parsed, err := library.ParseULID(*input.WorkID)
			if err != nil {
				return &library.Error{Op: "update edition", Kind: library.KindValidation, Err: errValidation("invalid work_id")}
			}
			existing.WorkID = parsed
		}
		if input.Name != nil {
			if strings.TrimSpace(*input.Name) == "" {
				return &library.Error{Op: "update edition", Kind: library.KindValidation, Err: errValidation("name must not be empty")}
			}
			existing.Name = *input.Name
		}
		if input.Language != nil {
			existing.Language = *input.Language
		}
		if input.Format != nil {
			if strings.TrimSpace(*input.Format) == "" {
				return &library.Error{Op: "update edition", Kind: library.KindValidation, Err: errValidation("format must not be empty")}
			}
			existing.Format = *input.Format
		}
		return h.store.UpdateEdition(r.Context(), tx, existing)
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	getRecord(h, w, r, id, h.store.GetEdition)
}

// --- Chapters ---

// ServeChaptersByEdition dispatches list (GET) and create (POST) for chapters under an edition.
func (h *LibraryHandler) ServeChaptersByEdition(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	editionID := library.ULID(r.PathValue("id"))
	if editionID == "" {
		WriteError(w, http.StatusBadRequest, "edition id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(editionID.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid edition id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listChapters(w, r, editionID)
	case http.MethodPost:
		h.createChapter(w, r, editionID)
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) listChapters(w http.ResponseWriter, r *http.Request, editionID library.ULID) {
	var records []library.Chapter
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var listErr error
		records, listErr = h.store.ListChaptersByEdition(r.Context(), tx, editionID)
		return listErr
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteOK(w, records)
}

func (h *LibraryHandler) createChapter(w http.ResponseWriter, r *http.Request, editionID library.ULID) {
	var input struct {
		Title      string `json:"title"`
		ChapterNum int    `json:"chapter_number"`
		StartMs    int64  `json:"start_ms"`
		EndMs      int64  `json:"end_ms"`
		DurationMs int64  `json:"duration_ms"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.Title) == "" {
		WriteError(w, http.StatusBadRequest, "title is required", ErrCodeValidation)
		return
	}

	record, err := library.NewChapter(editionID, input.Title, input.ChapterNum, input.StartMs, input.EndMs, input.DurationMs)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
		return
	}

	createAndRead(h, w, r, func(tx *sql.Tx) error { return h.store.CreateChapter(r.Context(), tx, &record) }, record.ID, h.store.GetChapter)
}

// ServeChapter dispatches get (GET), update (PUT), and delete (DELETE) for a single chapter.
func (h *LibraryHandler) ServeChapter(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	id := library.ULID(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(id.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid chapter id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getRecord(h, w, r, id, h.store.GetChapter)
	case http.MethodPut:
		h.updateChapter(w, r, id)
	case http.MethodDelete:
		deleteRecord(h, w, r, id, h.store.DeleteChapter)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) updateChapter(w http.ResponseWriter, r *http.Request, id library.ULID) {
	var input struct {
		EditionID  *string `json:"edition_id"`
		Title      *string `json:"title"`
		ChapterNum *int    `json:"chapter_number"`
		StartMs    *int64  `json:"start_ms"`
		EndMs      *int64  `json:"end_ms"`
		DurationMs *int64  `json:"duration_ms"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}

	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		existing, err := h.store.GetChapter(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if input.EditionID != nil {
			parsed, err := library.ParseULID(*input.EditionID)
			if err != nil {
				return &library.Error{Op: "update chapter", Kind: library.KindValidation, Err: errValidation("invalid edition_id")}
			}
			existing.EditionID = parsed
		}
		if input.Title != nil {
			if strings.TrimSpace(*input.Title) == "" {
				return &library.Error{Op: "update chapter", Kind: library.KindValidation, Err: errValidation("title must not be empty")}
			}
			existing.Title = *input.Title
		}
		if input.ChapterNum != nil {
			existing.ChapterNum = *input.ChapterNum
		}
		if input.StartMs != nil {
			existing.StartMs = *input.StartMs
		}
		if input.EndMs != nil {
			existing.EndMs = *input.EndMs
		}
		if input.DurationMs != nil {
			existing.DurationMs = *input.DurationMs
		}
		return h.store.UpdateChapter(r.Context(), tx, existing)
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	getRecord(h, w, r, id, h.store.GetChapter)
}

// --- Source Assets ---

// ServeAssetsByChapter dispatches list (GET) and create (POST) for source assets under a chapter.
func (h *LibraryHandler) ServeAssetsByChapter(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	chapterID := library.ULID(r.PathValue("id"))
	if chapterID == "" {
		WriteError(w, http.StatusBadRequest, "chapter id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(chapterID.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid chapter id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listSourceAssets(w, r, chapterID)
	case http.MethodPost:
		h.createSourceAsset(w, r, chapterID)
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) listSourceAssets(w http.ResponseWriter, r *http.Request, chapterID library.ULID) {
	var records []library.SourceAsset
	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		var listErr error
		records, listErr = h.store.ListSourceAssetsByChapter(r.Context(), tx, chapterID)
		return listErr
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	WriteOK(w, records)
}

func (h *LibraryHandler) createSourceAsset(w http.ResponseWriter, r *http.Request, chapterID library.ULID) {
	var input struct {
		URL        string `json:"url"`
		MIMEType   string `json:"mime_type"`
		SizeBytes  int64  `json:"size_bytes"`
		SHA256Hash string `json:"sha256_hash"`
		StartMs    int64  `json:"start_ms"`
		EndMs      int64  `json:"end_ms"`
		DurationMs int64  `json:"duration_ms"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.URL) == "" {
		WriteError(w, http.StatusBadRequest, "url is required", ErrCodeValidation)
		return
	}
	if strings.TrimSpace(input.MIMEType) == "" {
		WriteError(w, http.StatusBadRequest, "mime_type is required", ErrCodeValidation)
		return
	}
	if !validDigest(input.SHA256Hash) {
		WriteError(w, http.StatusBadRequest, "sha256_hash must be 64 lowercase hexadecimal characters", ErrCodeValidation)
		return
	}

	record, err := library.NewSourceAsset(chapterID, input.URL, input.MIMEType, input.SizeBytes, input.SHA256Hash, input.StartMs, input.EndMs, input.DurationMs)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
		return
	}

	createAndRead(h, w, r, func(tx *sql.Tx) error { return h.store.CreateSourceAsset(r.Context(), tx, &record) }, record.ID, h.store.GetSourceAsset)
}

// ServeSourceAsset dispatches get (GET), update (PUT), and delete (DELETE) for a single source asset.
func (h *LibraryHandler) ServeSourceAsset(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutation(w, r) {
		return
	}
	id := library.ULID(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "id is required", ErrCodeValidation)
		return
	}
	if _, err := library.ParseULID(id.String()); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid source asset id", ErrCodeValidation)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getRecord(h, w, r, id, h.store.GetSourceAsset)
	case http.MethodPut:
		h.updateSourceAsset(w, r, id)
	case http.MethodDelete:
		deleteRecord(h, w, r, id, h.store.DeleteSourceAsset)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func (h *LibraryHandler) updateSourceAsset(w http.ResponseWriter, r *http.Request, id library.ULID) {
	var input struct {
		ChapterID  *string `json:"chapter_id"`
		URL        *string `json:"url"`
		MIMEType   *string `json:"mime_type"`
		SizeBytes  *int64  `json:"size_bytes"`
		SHA256Hash *string `json:"sha256_hash"`
		StartMs    *int64  `json:"start_ms"`
		EndMs      *int64  `json:"end_ms"`
		DurationMs *int64  `json:"duration_ms"`
	}
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body", ErrCodeValidation)
		return
	}
	if input.SHA256Hash != nil && !validDigest(*input.SHA256Hash) {
		WriteError(w, http.StatusBadRequest, "sha256_hash must be 64 lowercase hexadecimal characters", ErrCodeValidation)
		return
	}

	err := h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		existing, err := h.store.GetSourceAsset(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if input.ChapterID != nil {
			parsed, err := library.ParseULID(*input.ChapterID)
			if err != nil {
				return &library.Error{Op: "update source asset", Kind: library.KindValidation, Err: errValidation("invalid chapter_id")}
			}
			existing.ChapterID = parsed
		}
		if input.URL != nil {
			if strings.TrimSpace(*input.URL) == "" {
				return &library.Error{Op: "update source asset", Kind: library.KindValidation, Err: errValidation("url must not be empty")}
			}
			existing.URL = *input.URL
		}
		if input.MIMEType != nil {
			if strings.TrimSpace(*input.MIMEType) == "" {
				return &library.Error{Op: "update source asset", Kind: library.KindValidation, Err: errValidation("mime_type must not be empty")}
			}
			existing.MIMEType = *input.MIMEType
		}
		if input.SizeBytes != nil {
			existing.SizeBytes = *input.SizeBytes
		}
		if input.SHA256Hash != nil {
			existing.SHA256Hash = *input.SHA256Hash
		}
		if input.StartMs != nil {
			existing.StartMs = *input.StartMs
		}
		if input.EndMs != nil {
			existing.EndMs = *input.EndMs
		}
		if input.DurationMs != nil {
			existing.DurationMs = *input.DurationMs
		}
		return h.store.UpdateSourceAsset(r.Context(), tx, existing)
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	getRecord(h, w, r, id, h.store.GetSourceAsset)
}

// writeStoreError maps library.Store errors to HTTP status codes and structured
// APIError responses.
func (h *LibraryHandler) writeStoreError(w http.ResponseWriter, err error) {
	var libErr *library.Error
	if errors.As(err, &libErr) {
		switch libErr.Kind {
		case library.KindNotFound:
			WriteError(w, http.StatusNotFound, libErr.Error(), ErrCodeNotFound)
		case library.KindValidation:
			WriteError(w, http.StatusBadRequest, libErr.Error(), ErrCodeValidation)
		case library.KindConflict:
			WriteError(w, http.StatusConflict, libErr.Error(), ErrCodeConflict)
		default:
			WriteError(w, http.StatusInternalServerError, "internal error", ErrCodeInternal)
		}
		return
	}
	WriteError(w, http.StatusInternalServerError, "internal error", ErrCodeInternal)
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func errValidation(msg string) error { return &validationError{msg: msg} }

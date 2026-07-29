package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

// testCSRF is a CSRFVerifier spy that records whether verification was called.
type testCSRF struct {
	called      bool
	shouldError bool
}

func (c *testCSRF) VerifyRequest(r *http.Request) error {
	c.called = true
	if c.shouldError {
		return errors.New("csrf invalid")
	}
	return nil
}

// newLibraryHandler creates a LibraryHandler backed by an in-memory database.
func newLibraryHandler(t *testing.T) (*httpapi.LibraryHandler, *store.DB) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return httpapi.NewLibraryHandler(db, &testCSRF{}), db
}

// authedRequest creates an httptest request with a method, target, and optional body.
// It automatically extracts path values for Go 1.22+ mux handlers:
//   - The last URL path segment becomes r.PathValue("id") when it is not a
//     known collection name.
//   - For nested routes (/prefix/{id}/collection), the second-to-last segment
//     becomes r.PathValue("id").
//
// Optional pathValues are additional (key, value) pairs.
func authedRequest(method, target, body string, pathValues ...string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	segments := strings.Split(strings.TrimRight(target, "/"), "/")
	collections := map[string]bool{
		"libraries": true, "works": true, "editions": true, "chapters": true,
		"assets": true, "media": true,
	}
	// Try the last segment as the id first.
	if len(segments) >= 1 {
		last := segments[len(segments)-1]
		if last != "" && !collections[last] {
			req.SetPathValue("id", last)
		} else if len(segments) >= 2 && !collections[segments[len(segments)-2]] {
			// For /prefix/{id}/collection, use the second-to-last segment.
			req.SetPathValue("id", segments[len(segments)-2])
		}
	}
	for i := 0; i+1 < len(pathValues); i += 2 {
		req.SetPathValue(pathValues[i], pathValues[i+1])
	}
	return req
}

// authedRequestWithID creates a request and sets r.PathValue("id") to the given value.
func authedRequestWithID(method, target, body, id string) *http.Request {
	return authedRequest(method, target, body, "id", id)
}

func decodeJSON[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, body)
	}
	return v
}

func decodeAPIError(t *testing.T, body string) httpapi.APIError {
	t.Helper()
	var e httpapi.APIError
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("decode APIError: %v\nbody: %s", err, body)
	}
	return e
}

func TestLibraryListReturnsEmptyArray(t *testing.T) {
	h, _ := newLibraryHandler(t)

	list := httptest.NewRecorder()
	h.ServeLibraries(list, authedRequest(http.MethodGet, "/api/v1/libraries", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list libraries status = %d", list.Code)
	}
	body := strings.TrimSpace(list.Body.String())
	if body != "[]" {
		t.Fatalf("empty list body = %s", body)
	}
}

func TestLibraryRejectsNonStrictJSON(t *testing.T) {
	h, _ := newLibraryHandler(t)
	for name, body := range map[string]string{
		"unknown property": `{"name":"Library","source_language":"nl","target_language":"en","unknown":true}`,
		"trailing JSON":    `{"name":"Library","source_language":"nl","target_language":"en"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.ServeLibraries(response, authedRequest(http.MethodPost, "/api/v1/libraries", body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLibraryUpdatePartialSafety(t *testing.T) {
	h, _ := newLibraryHandler(t)

	// Create.
	create := httptest.NewRecorder()
	h.ServeLibraries(create, authedRequest(http.MethodPost, "/api/v1/libraries",
		`{"name":"Original","source_language":"nl","target_language":"en"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d", create.Code)
	}
	lib := decodeJSON[library.Library](t, create.Body.String())

	// Update only the name — other fields must be preserved.
	update := httptest.NewRecorder()
	h.ServeLibrary(update, authedRequest(http.MethodPut, "/api/v1/libraries/"+lib.ID.String(),
		`{"name":"Updated Name"}`))
	if update.Code != http.StatusOK {
		t.Fatalf("partial update status = %d: %s", update.Code, update.Body.String())
	}
	updated := decodeJSON[library.Library](t, update.Body.String())
	if updated.Name != "Updated Name" {
		t.Errorf("name not updated: %q", updated.Name)
	}
	if updated.SourceLanguage != "nl" {
		t.Errorf("source_language changed: %q", updated.SourceLanguage)
	}
	if updated.TargetLanguage != "en" {
		t.Errorf("target_language changed: %q", updated.TargetLanguage)
	}
	if updated.Description != "" {
		t.Errorf("description changed: %q", updated.Description)
	}
}

func TestLibraryCSRFEnforcement(t *testing.T) {
	csrf := &testCSRF{shouldError: true}
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := httpapi.NewLibraryHandler(db, csrf)

	for _, test := range []struct {
		name, method, target string
		serve                func(http.ResponseWriter, *http.Request)
	}{
		{"libraries", http.MethodPost, "/api/v1/libraries", h.ServeLibraries},
		{"library-put", http.MethodPut, "/api/v1/libraries/not-a-ulid", h.ServeLibrary},
		{"library-delete", http.MethodDelete, "/api/v1/libraries/not-a-ulid", h.ServeLibrary},
		{"works", http.MethodPost, "/api/v1/libraries/not-a-ulid/works", h.ServeWorksByLibrary},
		{"work-put", http.MethodPut, "/api/v1/works/not-a-ulid", h.ServeWork},
		{"work-delete", http.MethodDelete, "/api/v1/works/not-a-ulid", h.ServeWork},
		{"editions", http.MethodPost, "/api/v1/works/not-a-ulid/editions", h.ServeEditionsByWork},
		{"edition-put", http.MethodPut, "/api/v1/editions/not-a-ulid", h.ServeEdition},
		{"edition-delete", http.MethodDelete, "/api/v1/editions/not-a-ulid", h.ServeEdition},
		{"chapters", http.MethodPost, "/api/v1/editions/not-a-ulid/chapters", h.ServeChaptersByEdition},
		{"chapter-put", http.MethodPut, "/api/v1/chapters/not-a-ulid", h.ServeChapter},
		{"chapter-delete", http.MethodDelete, "/api/v1/chapters/not-a-ulid", h.ServeChapter},
		{"assets", http.MethodPost, "/api/v1/chapters/not-a-ulid/assets", h.ServeAssetsByChapter},
		{"asset-put", http.MethodPut, "/api/v1/assets/not-a-ulid", h.ServeSourceAsset},
		{"asset-delete", http.MethodDelete, "/api/v1/assets/not-a-ulid", h.ServeSourceAsset},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			test.serve(rec, authedRequest(test.method, test.target, "{"))
			if rec.Code != http.StatusForbidden || decodeAPIError(t, rec.Body.String()).Code != httpapi.ErrCodeCSRF {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec = httptest.NewRecorder()
			test.serve(rec, authedRequest(http.MethodGet, test.target, ""))
			if rec.Code == http.StatusForbidden {
				t.Fatal("GET incorrectly required CSRF")
			}
		})
	}
}

// --- Work CRUD ---

func TestWorkCreateAndList(t *testing.T) {
	h, _ := newLibraryHandler(t)

	// Create a library first.
	createLib := httptest.NewRecorder()
	h.ServeLibraries(createLib, authedRequest(http.MethodPost, "/api/v1/libraries",
		`{"name":"Lib","source_language":"nl","target_language":"en"}`))
	if createLib.Code != http.StatusCreated {
		t.Fatalf("create library = %d", createLib.Code)
	}
	lib := decodeJSON[library.Library](t, createLib.Body.String())

	// Create a work under the library.
	createWork := httptest.NewRecorder()
	h.ServeWorksByLibrary(createWork, authedRequest(http.MethodPost,
		"/api/v1/libraries/"+lib.ID.String()+"/works",
		`{"title":"Het Achterhuis","author":"Anne Frank","kind":"audiobook"}`))
	if createWork.Code != http.StatusCreated {
		t.Fatalf("create work status = %d: %s", createWork.Code, createWork.Body.String())
	}
	work := decodeJSON[library.Work](t, createWork.Body.String())
	if work.ID == "" || work.CreatedAt == "" || work.UpdatedAt == "" {
		t.Fatal("work create response is missing persisted identity or timestamps")
	}
	if work.Title != "Het Achterhuis" {
		t.Errorf("title = %q", work.Title)
	}
	if work.LibraryID.String() != lib.ID.String() {
		t.Errorf("library_id = %q", work.LibraryID)
	}

	// List works under the library.
	list := httptest.NewRecorder()
	h.ServeWorksByLibrary(list, authedRequest(http.MethodGet,
		"/api/v1/libraries/"+lib.ID.String()+"/works", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list works status = %d", list.Code)
	}
	works := decodeJSON[[]library.Work](t, list.Body.String())
	if len(works) != 1 {
		t.Fatalf("works count = %d", len(works))
	}

	// List works for unknown library returns empty array.
	listUnknown := httptest.NewRecorder()
	h.ServeWorksByLibrary(listUnknown, authedRequest(http.MethodGet,
		"/api/v1/libraries/01J00000000000000000000000/works", ""))
	if listUnknown.Code != http.StatusOK {
		t.Fatalf("list works for unknown library status = %d", listUnknown.Code)
	}
	if strings.TrimSpace(listUnknown.Body.String()) != "[]" {
		t.Fatalf("unknown library list = %s", listUnknown.Body.String())
	}
}

func TestWorkUpdateAndDelete(t *testing.T) {
	h, _ := newLibraryHandler(t)

	// Setup: library + work.
	createLib := httptest.NewRecorder()
	h.ServeLibraries(createLib, authedRequest(http.MethodPost, "/api/v1/libraries",
		`{"name":"Lib","source_language":"nl","target_language":"en"}`))
	lib := decodeJSON[library.Library](t, createLib.Body.String())

	createWork := httptest.NewRecorder()
	h.ServeWorksByLibrary(createWork, authedRequest(http.MethodPost,
		"/api/v1/libraries/"+lib.ID.String()+"/works",
		`{"title":"Original","author":"Author","kind":"ebook"}`))
	work := decodeJSON[library.Work](t, createWork.Body.String())

	// Update title.
	update := httptest.NewRecorder()
	h.ServeWork(update, authedRequest(http.MethodPut,
		"/api/v1/works/"+work.ID.String(),
		`{"title":"Updated Title"}`))
	if update.Code != http.StatusOK {
		t.Fatalf("update work status = %d: %s", update.Code, update.Body.String())
	}
	updated := decodeJSON[library.Work](t, update.Body.String())
	if updated.Title != "Updated Title" {
		t.Errorf("title = %q", updated.Title)
	}
	if updated.Author != "Author" {
		t.Errorf("author changed: %q", updated.Author)
	}

	// Delete.
	del := httptest.NewRecorder()
	h.ServeWork(del, authedRequest(http.MethodDelete, "/api/v1/works/"+work.ID.String(), ""))
	if del.Code != http.StatusOK {
		t.Fatalf("delete work status = %d", del.Code)
	}

	// Get after delete.
	get := httptest.NewRecorder()
	h.ServeWork(get, authedRequest(http.MethodGet, "/api/v1/works/"+work.ID.String(), ""))
	if get.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d", get.Code)
	}
}

// --- Edition CRUD ---

func TestEditionCreateAndList(t *testing.T) {
	h, _ := newLibraryHandler(t)

	// Setup: library → work.
	createLib := httptest.NewRecorder()
	h.ServeLibraries(createLib, authedRequest(http.MethodPost, "/api/v1/libraries",
		`{"name":"Lib","source_language":"nl","target_language":"en"}`))
	lib := decodeJSON[library.Library](t, createLib.Body.String())

	createWork := httptest.NewRecorder()
	h.ServeWorksByLibrary(createWork, authedRequest(http.MethodPost,
		"/api/v1/libraries/"+lib.ID.String()+"/works",
		`{"title":"Work","author":"A","kind":"audiobook"}`))
	work := decodeJSON[library.Work](t, createWork.Body.String())

	// Create edition.
	createEd := httptest.NewRecorder()
	h.ServeEditionsByWork(createEd, authedRequest(http.MethodPost,
		"/api/v1/works/"+work.ID.String()+"/editions",
		`{"name":"First Edition","language":"nl","format":"mp3"}`))
	if createEd.Code != http.StatusCreated {
		t.Fatalf("create edition = %d: %s", createEd.Code, createEd.Body.String())
	}
	edition := decodeJSON[library.Edition](t, createEd.Body.String())
	if edition.ID == "" || edition.CreatedAt == "" || edition.UpdatedAt == "" {
		t.Fatal("edition create response is missing persisted identity or timestamps")
	}
	if edition.Name != "First Edition" {
		t.Errorf("name = %q", edition.Name)
	}
	if edition.Format != "mp3" {
		t.Errorf("format = %q", edition.Format)
	}

	// List editions.
	list := httptest.NewRecorder()
	h.ServeEditionsByWork(list, authedRequest(http.MethodGet,
		"/api/v1/works/"+work.ID.String()+"/editions", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list editions = %d", list.Code)
	}
	editions := decodeJSON[[]library.Edition](t, list.Body.String())
	if len(editions) != 1 {
		t.Fatalf("editions count = %d", len(editions))
	}
}

func TestEditionNotFound(t *testing.T) {
	h, _ := newLibraryHandler(t)

	get := httptest.NewRecorder()
	h.ServeEdition(get, authedRequest(http.MethodGet, "/api/v1/editions/01J00000000000000000000000", ""))
	if get.Code != http.StatusNotFound {
		t.Fatalf("get unknown edition = %d: %s", get.Code, get.Body.String())
	}
	errBody := decodeAPIError(t, get.Body.String())
	if errBody.Code != httpapi.ErrCodeNotFound {
		t.Errorf("error code = %q", errBody.Code)
	}
}

// --- Chapter CRUD ---

func TestChapterCreateAndList(t *testing.T) {
	h, _ := newLibraryHandler(t)

	// Setup: library → work → edition.
	createLib := httptest.NewRecorder()
	h.ServeLibraries(createLib, authedRequest(http.MethodPost, "/api/v1/libraries",
		`{"name":"Lib","source_language":"nl","target_language":"en"}`))
	lib := decodeJSON[library.Library](t, createLib.Body.String())

	createWork := httptest.NewRecorder()
	h.ServeWorksByLibrary(createWork, authedRequest(http.MethodPost,
		"/api/v1/libraries/"+lib.ID.String()+"/works",
		`{"title":"Work","author":"A","kind":"audiobook"}`))
	work := decodeJSON[library.Work](t, createWork.Body.String())

	createEd := httptest.NewRecorder()
	h.ServeEditionsByWork(createEd, authedRequest(http.MethodPost,
		"/api/v1/works/"+work.ID.String()+"/editions",
		`{"name":"Edition","language":"nl","format":"mp3"}`))
	edition := decodeJSON[library.Edition](t, createEd.Body.String())

	// Create chapter.
	createCh := httptest.NewRecorder()
	h.ServeChaptersByEdition(createCh, authedRequest(http.MethodPost,
		"/api/v1/editions/"+edition.ID.String()+"/chapters",
		`{"title":"Chapter 1","chapter_number":1,"start_ms":0,"end_ms":60000,"duration_ms":60000}`))
	if createCh.Code != http.StatusCreated {
		t.Fatalf("create chapter = %d: %s", createCh.Code, createCh.Body.String())
	}
	chapter := decodeJSON[library.Chapter](t, createCh.Body.String())
	if chapter.ID == "" || chapter.CreatedAt == "" || chapter.UpdatedAt == "" {
		t.Fatal("chapter create response is missing persisted identity or timestamps")
	}
	if chapter.Title != "Chapter 1" {
		t.Errorf("title = %q", chapter.Title)
	}
	if chapter.ChapterNum != 1 {
		t.Errorf("chapter_num = %d", chapter.ChapterNum)
	}

	// List chapters.
	list := httptest.NewRecorder()
	h.ServeChaptersByEdition(list, authedRequest(http.MethodGet,
		"/api/v1/editions/"+edition.ID.String()+"/chapters", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list chapters = %d", list.Code)
	}
	chapters := decodeJSON[[]library.Chapter](t, list.Body.String())
	if len(chapters) != 1 {
		t.Fatalf("chapters count = %d", len(chapters))
	}
	if chapters[0].ID.String() != chapter.ID.String() {
		t.Errorf("chapter id mismatch")
	}
}

// --- Source Asset CRUD ---

func TestSourceAssetCreateAndList(t *testing.T) {
	h, _ := newLibraryHandler(t)
	chapter := seedChapter(t, h)

	for name, digest := range map[string]string{
		"malformed": "not-a-digest",
		"uppercase": strings.Repeat("A", 64),
	} {
		t.Run(name+" digest", func(t *testing.T) {
			response := httptest.NewRecorder()
			body := `{"url":"file:///audio/ch1.mp3","mime_type":"audio/mpeg","size_bytes":1,"sha256_hash":"` + digest + `"}`
			h.ServeAssetsByChapter(response, authedRequest(http.MethodPost,
				"/api/v1/chapters/"+chapter.ID.String()+"/assets", body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}

	// Create source asset.
	createSA := httptest.NewRecorder()
	h.ServeAssetsByChapter(createSA, authedRequest(http.MethodPost,
		"/api/v1/chapters/"+chapter.ID.String()+"/assets",
		`{"url":"file:///audio/ch1.mp3","mime_type":"audio/mpeg","size_bytes":1234567,"sha256_hash":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","start_ms":0,"end_ms":60000,"duration_ms":60000}`))
	if createSA.Code != http.StatusCreated {
		t.Fatalf("create source asset = %d: %s", createSA.Code, createSA.Body.String())
	}
	asset := decodeJSON[library.SourceAsset](t, createSA.Body.String())
	if asset.ID == "" || asset.CreatedAt == "" || asset.UpdatedAt == "" {
		t.Fatal("asset create response is missing persisted identity or timestamps")
	}
	if asset.MIMEType != "audio/mpeg" {
		t.Errorf("mime_type = %q", asset.MIMEType)
	}
	rejectUpdate := httptest.NewRecorder()
	h.ServeSourceAsset(rejectUpdate, authedRequest(http.MethodPut,
		"/api/v1/assets/"+asset.ID.String(), `{"sha256_hash":"`+strings.Repeat("A", 64)+`"}`))
	if rejectUpdate.Code != http.StatusBadRequest {
		t.Fatalf("uppercase digest update = %d, want 400: %s", rejectUpdate.Code, rejectUpdate.Body.String())
	}

	// List source assets.
	list := httptest.NewRecorder()
	h.ServeAssetsByChapter(list, authedRequest(http.MethodGet,
		"/api/v1/chapters/"+chapter.ID.String()+"/assets", ""))
	if list.Code != http.StatusOK {
		t.Fatalf("list assets = %d", list.Code)
	}
	assets := decodeJSON[[]library.SourceAsset](t, list.Body.String())
	if len(assets) != 1 {
		t.Fatalf("assets count = %d", len(assets))
	}
}

// TestSourceAssetOpaqueMediaType proves the production create/read boundary
// accepts a nonblank opaque media value that does not match a strict MIME
// pattern — the same value is stored unchanged and returned by the handler.
func TestSourceAssetOpaqueMediaType(t *testing.T) {
	for _, mimeType := range []string{"opaque-media", "application/octet-stream+x-custom"} {
		t.Run(mimeType, func(t *testing.T) {
			h, _ := newLibraryHandler(t)
			chapter := seedChapter(t, h)

			// Create a source asset with an opaque, non-MIME media value.
			createSA := httptest.NewRecorder()
			h.ServeAssetsByChapter(createSA, authedRequest(http.MethodPost,
				"/api/v1/chapters/"+chapter.ID.String()+"/assets",
				`{"url":"file:///test","mime_type":"`+mimeType+`","size_bytes":1,"sha256_hash":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`))
			if createSA.Code != http.StatusCreated {
				t.Fatalf("create source asset with opaque mime_type status = %d: %s", createSA.Code, createSA.Body.String())
			}
			asset := decodeJSON[library.SourceAsset](t, createSA.Body.String())
			if asset.MIMEType != mimeType {
				t.Errorf("mime_type = %q, want %q", asset.MIMEType, mimeType)
			}

			// Read back and confirm the opaque value round-trips.
			getSA := httptest.NewRecorder()
			h.ServeSourceAsset(getSA, authedRequest(http.MethodGet,
				"/api/v1/assets/"+asset.ID.String(), ""))
			if getSA.Code != http.StatusOK {
				t.Fatalf("get source asset status = %d: %s", getSA.Code, getSA.Body.String())
			}
			got := decodeJSON[library.SourceAsset](t, getSA.Body.String())
			if got.MIMEType != mimeType {
				t.Errorf("read-back mime_type = %q, want %q", got.MIMEType, mimeType)
			}
		})
	}
}

var _ httpapi.CSRFVerifier = (*testCSRF)(nil)

// seedChapter creates library→work→edition→chapter and returns the chapter.
func seedChapter(t *testing.T, h *httpapi.LibraryHandler) library.Chapter {
	t.Helper()
	createLib := httptest.NewRecorder()
	h.ServeLibraries(createLib, authedRequest(http.MethodPost, "/api/v1/libraries",
		`{"name":"Lib","source_language":"nl","target_language":"en"}`))
	lib := decodeJSON[library.Library](t, createLib.Body.String())

	createWork := httptest.NewRecorder()
	h.ServeWorksByLibrary(createWork, authedRequest(http.MethodPost,
		"/api/v1/libraries/"+lib.ID.String()+"/works",
		`{"title":"Work","author":"A","kind":"audiobook"}`))
	work := decodeJSON[library.Work](t, createWork.Body.String())

	createEd := httptest.NewRecorder()
	h.ServeEditionsByWork(createEd, authedRequest(http.MethodPost,
		"/api/v1/works/"+work.ID.String()+"/editions",
		`{"name":"Edition","language":"nl","format":"mp3"}`))
	edition := decodeJSON[library.Edition](t, createEd.Body.String())

	createCh := httptest.NewRecorder()
	h.ServeChaptersByEdition(createCh, authedRequest(http.MethodPost,
		"/api/v1/editions/"+edition.ID.String()+"/chapters",
		`{"title":"Chapter 1","chapter_number":1,"start_ms":0,"end_ms":60000,"duration_ms":60000}`))
	return decodeJSON[library.Chapter](t, createCh.Body.String())
}

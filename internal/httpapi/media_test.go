package httpapi_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

// mediaRequest creates a test request and auto-extracts the last path segment
// as r.PathValue("id") so Go 1.22+ mux handlers see the path parameter.
func mediaRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	segments := strings.Split(strings.TrimRight(target, "/"), "/")
	collections := map[string]bool{"media": true}
	if len(segments) >= 1 {
		last := segments[len(segments)-1]
		if last != "" && !collections[last] {
			req.SetPathValue("id", last)
		} else if len(segments) >= 2 && !collections[segments[len(segments)-2]] {
			req.SetPathValue("id", segments[len(segments)-2])
		}
	}
	return req
}

// newMediaHandler creates a MediaHandler backed by an in-memory database
// and a real temp directory for blob storage.
func newMediaHandler(t *testing.T) (*httpapi.MediaHandler, *store.DB, string) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mediaRoot := filepath.Join(t.TempDir(), "media")
	for _, dir := range []string{"temp", "blobs"} {
		if err := os.MkdirAll(filepath.Join(mediaRoot, dir), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	h := httpapi.NewMediaHandler(db, &library.Store{}, mediaRoot, "")
	return h, db, mediaRoot
}

// seedMediaSourceAsset creates library -> work -> edition -> chapter -> source_asset
// and writes blob bytes. Returns (sourceAssetID, digest, mimeType, blobData).
func seedMediaSourceAsset(t *testing.T, db *store.DB, mediaRoot string, blobData []byte, mimeType string) (string, string, string, []byte) {
	t.Helper()

	digestBytes := sha256.Sum256(blobData)
	digest := hex.EncodeToString(digestBytes[:])

	// Write the blob file.
	blobPath := filepath.Join(mediaRoot, "blobs", digest)
	if err := os.WriteFile(blobPath, blobData, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	libStore := &library.Store{}

	err := db.WithTransaction(t.Context(), func(tx *sql.Tx) error {
		lib, err := library.NewLibrary("Test Library", "nl", "en", "")
		if err != nil {
			return err
		}
		if err := libStore.CreateLibrary(t.Context(), tx, &lib); err != nil {
			return err
		}
		work, err := library.NewWork(lib.ID, "Test Work", "Author", "audiobook", "")
		if err != nil {
			return err
		}
		if err := libStore.CreateWork(t.Context(), tx, &work); err != nil {
			return err
		}
		edition, err := library.NewEdition(work.ID, "Edition", "nl", "mp3")
		if err != nil {
			return err
		}
		if err := libStore.CreateEdition(t.Context(), tx, &edition); err != nil {
			return err
		}
		chapter, err := library.NewChapter(edition.ID, "Chapter 1", 1, 0, 60000, 60000)
		if err != nil {
			return err
		}
		if err := libStore.CreateChapter(t.Context(), tx, &chapter); err != nil {
			return err
		}
		sa, err := library.NewSourceAsset(chapter.ID, "file:///test.mp3", mimeType, int64(len(blobData)), digest, 0, int64(len(blobData)), int64(len(blobData)))
		if err != nil {
			return err
		}
		return libStore.CreateSourceAsset(t.Context(), tx, &sa)
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	var sourceAssetID string
	db.Conn().QueryRowContext(t.Context(),
		"SELECT id FROM source_asset WHERE sha256_hash = ?", digest,
	).Scan(&sourceAssetID)

	return sourceAssetID, digest, mimeType, blobData
}

func TestMediaGetReturnsBlobBytes(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	blobData := []byte("hello media world")
	id, digest, mimeType, _ := seedMediaSourceAsset(t, db, mediaRoot, blobData, "text/plain")

	rec := httptest.NewRecorder()
	h.ServeMedia(rec, mediaRequest(http.MethodGet, "/api/v1/media/"+id, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET media status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(blobData) {
		t.Errorf("body = %q, want %q", rec.Body.String(), string(blobData))
	}
	if ct := rec.Header().Get("Content-Type"); ct != mimeType {
		t.Errorf("Content-Type = %q, want %q", ct, mimeType)
	}
	if etag := rec.Header().Get("ETag"); etag != `"`+digest+`"` {
		t.Errorf("ETag = %q, want %q", etag, `"`+digest+`"`)
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(blobData)) {
		t.Errorf("Content-Length = %q, want %d", cl, len(blobData))
	}
}

func TestMediaRangeRequestPartialContent(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	blobData := []byte("0123456789ABCDEF") // 16 bytes
	id, _, _, _ := seedMediaSourceAsset(t, db, mediaRoot, blobData, "application/octet-stream")

	// Range: bytes=0-4 (first 5 bytes).
	req := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	h.ServeMedia(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "01234" {
		t.Errorf("range body = %q, want %q", rec.Body.String(), "01234")
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-4/16" {
		t.Errorf("Content-Range = %q", cr)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "5" {
		t.Errorf("Content-Length = %q", cl)
	}

	// Range: bytes=10- (from byte 10 to end).
	req2 := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req2.Header.Set("Range", "bytes=10-")
	rec2 := httptest.NewRecorder()
	h.ServeMedia(rec2, req2)
	if rec2.Code != http.StatusPartialContent {
		t.Fatalf("open-ended range status = %d", rec2.Code)
	}
	if rec2.Body.String() != "ABCDEF" {
		t.Errorf("open range body = %q", rec2.Body.String())
	}

	// Range: bytes=-5 (last 5 bytes).
	req3 := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req3.Header.Set("Range", "bytes=-5")
	rec3 := httptest.NewRecorder()
	h.ServeMedia(rec3, req3)
	if rec3.Code != http.StatusPartialContent {
		t.Fatalf("suffix range status = %d", rec3.Code)
	}
	if rec3.Body.String() != "BCDEF" {
		t.Errorf("suffix range body = %q", rec3.Body.String())
	}
}

func TestMediaRangeUnsatisfiable(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	blobData := []byte("hello")
	id, _, _, _ := seedMediaSourceAsset(t, db, mediaRoot, blobData, "text/plain")

	// Range: bytes=10-20 (beyond file).
	req := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req.Header.Set("Range", "bytes=10-20")
	rec := httptest.NewRecorder()
	h.ServeMedia(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range status = %d: %s", rec.Code, rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); !strings.Contains(cr, "*/5") {
		t.Errorf("Content-Range = %q", cr)
	}
}

func TestMediaEmptyRangeIsUnsatisfiable(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	id, _, _, _ := seedMediaSourceAsset(t, db, mediaRoot, []byte{}, "application/octet-stream")
	req := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	h.ServeMedia(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Range") != "bytes */0" {
		t.Fatalf("status=%d content-range=%q", rec.Code, rec.Header().Get("Content-Range"))
	}
}

func TestMediaIfNoneMatch(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	blobData := []byte("conditional test data")
	id, digest, _, _ := seedMediaSourceAsset(t, db, mediaRoot, blobData, "text/plain")

	// Strong ETag match.
	req := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req.Header.Set("If-None-Match", `"`+digest+`"`)
	rec := httptest.NewRecorder()
	h.ServeMedia(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match match status = %d", rec.Code)
	}

	// Weak ETag match.
	req2 := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req2.Header.Set("If-None-Match", `W/"`+digest+`"`)
	rec2 := httptest.NewRecorder()
	h.ServeMedia(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("weak If-None-Match status = %d", rec2.Code)
	}

	// Wildcard match.
	req3 := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req3.Header.Set("If-None-Match", "*")
	rec3 := httptest.NewRecorder()
	h.ServeMedia(rec3, req3)
	if rec3.Code != http.StatusNotModified {
		t.Fatalf("wildcard If-None-Match status = %d", rec3.Code)
	}

	// Non-matching ETag.
	req4 := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
	req4.Header.Set("If-None-Match", `"different-etag"`)
	rec4 := httptest.NewRecorder()
	h.ServeMedia(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("non-matching If-None-Match status = %d", rec4.Code)
	}
}

func TestMediaAccelSharesConditionalAndRangeRules(t *testing.T) {
	_, db, mediaRoot := newMediaHandler(t)
	id, digest, _, _ := seedMediaSourceAsset(t, db, mediaRoot, []byte("range-data"), "text/plain")
	h := httpapi.NewMediaHandler(db, &library.Store{}, mediaRoot, "/_media-internal/")
	for _, test := range []struct {
		name, condition, rangeValue string
		status                      int
	}{
		{"weak-etag", `W/"` + digest + `"`, "", http.StatusNotModified},
		{"range", "", "bytes=0-2", http.StatusPartialContent},
		{"multi-range", "", "bytes=0-1,3-4", http.StatusRequestedRangeNotSatisfiable},
		{"unsatisfiable", "", "bytes=99-", http.StatusRequestedRangeNotSatisfiable},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := mediaRequest(http.MethodGet, "/api/v1/media/"+id, "")
			req.Header.Set("If-None-Match", test.condition)
			req.Header.Set("Range", test.rangeValue)
			rec := httptest.NewRecorder()
			h.ServeMedia(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if test.status == http.StatusNotModified && rec.Header().Get("X-Accel-Redirect") != "" {
				t.Fatal("304 redirected")
			}
			if test.status == http.StatusPartialContent && rec.Header().Get("X-Accel-Redirect") != "/_media-internal/"+digest {
				t.Fatal("missing opaque redirect")
			}
			if test.status == http.StatusRequestedRangeNotSatisfiable && rec.Header().Get("Content-Range") != "bytes */10" {
				t.Fatal("missing 416 range")
			}
		})
	}
}

func TestMediaRejectsStoredMalformedDigest(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	id, _, _, _ := seedMediaSourceAsset(t, db, mediaRoot, []byte("safe"), "text/plain")
	sentinel := filepath.Join(filepath.Dir(mediaRoot), "outside")
	if err := os.WriteFile(sentinel, []byte("outside bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{"../outside", `\\outside`, "/outside", "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789", "short", strings.Repeat("g", 64), "%2e%2e"} {
		t.Run(digest, func(t *testing.T) {
			if _, err := db.Exec(t.Context(), "UPDATE source_asset SET sha256_hash = ? WHERE id = ?", digest, id); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.ServeMedia(rec, mediaRequest(http.MethodGet, "/api/v1/media/"+id, ""))
			if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "outside bytes") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMediaStoreFailureIsInternal(t *testing.T) {
	h, db, _ := newMediaHandler(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeMedia(rec, mediaRequest(http.MethodGet, "/api/v1/media/01J00000000000000000000000", ""))
	if rec.Code != http.StatusInternalServerError || decodeAPIError(t, rec.Body.String()).Code != httpapi.ErrCodeInternal {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMediaTraversalRejection(t *testing.T) {
	h, _, _ := newMediaHandler(t)

	for _, id := range []string{
		"../../../etc/passwd",
		"01J..\\..\\evil",
		"01J000/../000000",
	} {
		t.Run("traversal/"+id, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeMedia(rec, mediaRequest(http.MethodGet, "/api/v1/media/"+id, ""))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("traversal %q status = %d, want 400", id, rec.Code)
			}
			var apiErr httpapi.APIError
			if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil || apiErr.Code != httpapi.ErrCodeValidation {
				t.Errorf("traversal error = %+v", apiErr)
			}
		})
	}
}

func TestMediaHEADWithRange(t *testing.T) {
	h, db, mediaRoot := newMediaHandler(t)
	blobData := []byte("0123456789")
	id, _, _, _ := seedMediaSourceAsset(t, db, mediaRoot, blobData, "text/plain")

	// HEAD with Range uses the same range decision and returns no body.
	req := mediaRequest(http.MethodHead, "/api/v1/media/"+id, "")
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	h.ServeMedia(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("HEAD with range status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %d bytes", rec.Body.Len())
	}
}

// TestMediaOpaqueContentType proves that media delivery returns the exact
// stored source-asset mime_type as the Content-Type header, even when the
// value does not conform to a strict MIME type/ subtype pattern.
func TestMediaOpaqueContentType(t *testing.T) {
	for _, mimeType := range []string{"opaque-media", "application/octet-stream+x-custom"} {
		t.Run(mimeType, func(t *testing.T) {
			h, db, mediaRoot := newMediaHandler(t)
			blobData := []byte("opaque content type test data")
			id, _, _, _ := seedMediaSourceAsset(t, db, mediaRoot, blobData, mimeType)

			// GET — Content-Type must match the stored opaque value.
			rec := httptest.NewRecorder()
			h.ServeMedia(rec, mediaRequest(http.MethodGet, "/api/v1/media/"+id, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET media status = %d: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != mimeType {
				t.Errorf("Content-Type = %q, want %q", ct, mimeType)
			}

			// HEAD — Content-Type must also match without a body.
			rec2 := httptest.NewRecorder()
			h.ServeMedia(rec2, mediaRequest(http.MethodHead, "/api/v1/media/"+id, ""))
			if rec2.Code != http.StatusOK {
				t.Fatalf("HEAD media status = %d", rec2.Code)
			}
			if ct := rec2.Header().Get("Content-Type"); ct != mimeType {
				t.Errorf("HEAD Content-Type = %q, want %q", ct, mimeType)
			}
		})
	}
}

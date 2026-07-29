// Package httpapi provides shared HTTP API infrastructure for the Doublangu server.
package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

// MediaHandler serves authorized media bytes with conditional and range support.
// When accelPrefix is non-empty it returns X-Accel-Redirect instead of
// streaming bytes directly.
type MediaHandler struct {
	mediaRoot   string // filesystem root for blobs (mediaRoot/blobs/<digest>)
	db          *store.DB
	libStore    *library.Store
	accelPrefix string // X-Accel-Redirect internal URI prefix (or empty for direct)
}

// NewMediaHandler returns a MediaHandler that resolves source assets through
// the library store and serves blobs from mediaRoot. When accelPrefix is
// empty the handler streams bytes directly; otherwise it delegates to
// X-Accel-Redirect with an opaque internal path that never exposes the
// real filesystem location.
func NewMediaHandler(db *store.DB, libStore *library.Store, mediaRoot, accelPrefix string) *MediaHandler {
	return &MediaHandler{
		mediaRoot:   mediaRoot,
		db:          db,
		libStore:    libStore,
		accelPrefix: accelPrefix,
	}
}

// ServeMedia handles GET and HEAD for /api/v1/media/{id}.
// It resolves the source asset, verifies the backing blob exists, and serves
// byte-range requests with strong ETag support.
func (h *MediaHandler) ServeMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}

	id, err := parseAndValidateMediaID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
		return
	}

	// Resolve the source asset to its backing media digest.
	var sourceAsset *library.SourceAsset
	err = h.db.WithTransaction(r.Context(), func(tx *sql.Tx) error {
		sa, getErr := h.libStore.GetSourceAsset(r.Context(), tx, id)
		sourceAsset = sa
		return getErr
	})
	if err != nil {
		var libErr *library.Error
		if errors.As(err, &libErr) && libErr.Kind == library.KindNotFound {
			WriteError(w, http.StatusNotFound, "source asset not found", ErrCodeNotFound)
		} else {
			WriteError(w, http.StatusInternalServerError, "source asset lookup failed", ErrCodeInternal)
		}
		return
	}

	digest := sourceAsset.SHA256Hash
	blobPath, err := h.blobPath(digest)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "source asset has invalid media blob", ErrCodeInternal)
		return
	}

	// Verified backing-file open before success.
	f, err := os.Open(blobPath)
	if err != nil {
		if os.IsNotExist(err) {
			WriteError(w, http.StatusNotFound, "media blob not found", ErrCodeNotFound)
			return
		}
		WriteError(w, http.StatusInternalServerError, "media blob inaccessible", ErrCodeInternal)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "media blob stat failed", ErrCodeInternal)
		return
	}
	fileSize := info.Size()
	etag := `"` + digest + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", sourceAsset.MIMEType)

	if mediaETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	var start, end int64
	partial := r.Header.Get("Range") != ""
	if partial {
		var ok bool
		start, end, ok = parseSingleRange(r.Header.Get("Range"), fileSize)
		if !ok {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(fileSize, 10))
			WriteError(w, http.StatusRequestedRangeNotSatisfiable, "requested range not satisfiable", ErrCodeValidation)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
	}

	// X-Accel-Redirect is selected only after conditional and range semantics.
	if h.accelPrefix != "" {
		w.Header().Set("X-Accel-Redirect", h.accelPrefix+digest)
		if partial {
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		return
	}

	if partial {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			WriteError(w, http.StatusInternalServerError, "seek failed", ErrCodeInternal)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		if r.Method == http.MethodGet {
			_, _ = io.CopyN(w, f, end-start+1)
		}
		return
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (h *MediaHandler) blobPath(digest string) (string, error) {
	if !validDigest(digest) {
		return "", errors.New("invalid digest")
	}
	root := filepath.Join(h.mediaRoot, "blobs")
	path := filepath.Join(root, digest)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel != digest || filepath.Dir(rel) != "." {
		return "", errors.New("blob path escapes media root")
	}
	return path, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// parseAndValidateMediaID validates the raw ID as a ULID and rejects path traversal.
func parseAndValidateMediaID(raw string) (library.ULID, error) {
	if raw == "" {
		return "", fmt.Errorf("source asset id is required")
	}
	if strings.Contains(raw, "/") || strings.Contains(raw, "\\") || strings.Contains(raw, "..") {
		return "", fmt.Errorf("invalid source asset id")
	}
	id := library.ULID(raw)
	parsed, err := library.ParseULID(raw)
	if err != nil || parsed.IsZero() {
		return "", fmt.Errorf("invalid source asset id")
	}
	return id, nil
}

// parseSingleRange parses a single bytes=N-M range header.
// Returns (start, end, ok). If end is omitted, end = fileSize-1.
// Suffix ranges (bytes=-N) are supported; multiple ranges are rejected.
func parseSingleRange(header string, fileSize int64) (int64, int64, bool) {
	const prefix = "bytes="
	rest, ok := strings.CutPrefix(header, prefix)
	if !ok {
		return 0, 0, false
	}
	if strings.Contains(rest, ",") {
		return 0, 0, false
	}
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	var start, end int64
	if startStr == "" {
		suffix, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false
		}
		start = fileSize - suffix
		if start < 0 {
			start = 0
		}
		end = fileSize - 1
	} else {
		var err error
		start, err = strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, false
		}
		if endStr == "" {
			end = fileSize - 1
		} else {
			end, err = strconv.ParseInt(endStr, 10, 64)
			if err != nil || end < 0 || end < start {
				return 0, 0, false
			}
		}
	}
	if start >= fileSize {
		return 0, 0, false
	}
	if end >= fileSize {
		end = fileSize - 1
	}
	return start, end, true
}

// mediaETagMatches returns true when If-None-Match matches the strong ETag.
// It handles strong ETags, weak ETags (W/"..."), wildcard ("*"), and
// comma-separated lists.
func mediaETagMatches(headerValue, strongETag string) bool {
	trimmed := strings.TrimSpace(headerValue)
	if trimmed == "*" {
		return true
	}
	clean := strings.Trim(strongETag, `"`)
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == strongETag {
			return true
		}
		if etag, ok := strings.CutPrefix(part, "W/"); ok {
			if strings.Trim(etag, `"`) == clean {
				return true
			}
		}
	}
	return false
}

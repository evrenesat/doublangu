// Package pluginassets serves authorized same-origin UI plugin assets.
package pluginassets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultPrefix is the only HTTP namespace from which the browser may import
// trusted plugin modules.
const DefaultPrefix = "/api/v1/plugins/assets/"

var contentHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Handler serves regular files below a canonical root. Authorization is
// deliberately mandatory: a nil policy is a construction error, never allow.
type Handler struct {
	root      string
	prefix    string
	authorize func(*http.Request) bool
}

// New constructs a fail-closed plugin-asset handler. Versioned URLs have the
// form <prefix>v1/<sha256>/<file>; only those URLs receive immutable caching,
// and the digest is checked against the bytes before serving.
func New(root, prefix string, authorize func(*http.Request) bool) (*Handler, error) {
	if authorize == nil {
		return nil, fmt.Errorf("pluginassets: authorization policy is required")
	}
	if !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") {
		return nil, fmt.Errorf("pluginassets: prefix %q must start and end with /", prefix)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("pluginassets: resolve root %q: %w", root, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("pluginassets: resolve root %q: %w", abs, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("pluginassets: stat root %q: %w", canonical, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("pluginassets: root %q is not a directory", canonical)
	}
	return &Handler{root: canonical, prefix: prefix, authorize: authorize}, nil
}

// ServeHTTP serves GET and HEAD requests only. It deliberately performs no
// redirect or directory handling, so its outcomes are stable for callers.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	relative, err := h.relativePath(r.URL)
	if err != nil {
		status := http.StatusForbidden
		if err == errWrongPrefix {
			status = http.StatusNotFound
		}
		http.Error(w, http.StatusText(status), status)
		return
	}

	candidate := filepath.Join(h.root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !within(h.root, resolved) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if digest, versioned := versionedDigest(relative); versioned {
		if !matchesDigest(resolved, digest) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeFile(w, r, resolved)
}

var errWrongPrefix = fmt.Errorf("wrong asset prefix")

// relativePath decodes exactly once, strips the mount prefix, then validates
// slash-separated URL segments before converting them to a filesystem path.
func (h *Handler) relativePath(u *url.URL) (string, error) {
	escaped := u.EscapedPath()
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return "", fmt.Errorf("invalid path escape: %w", err)
	}
	if !strings.HasPrefix(decoded, h.prefix) {
		return "", errWrongPrefix
	}
	relative := strings.TrimPrefix(decoded, h.prefix)
	if relative == "" || strings.Contains(relative, "\\") || strings.ContainsRune(relative, '\x00') {
		return "", fmt.Errorf("invalid asset path")
	}
	for _, segment := range strings.Split(relative, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("asset path traversal")
		}
	}
	cleaned := path.Clean(relative)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("asset path traversal")
	}
	return cleaned, nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func versionedDigest(relative string) (string, bool) {
	parts := strings.Split(relative, "/")
	if len(parts) < 3 || parts[0] != "v1" || !contentHash.MatchString(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func matchesDigest(filename, expected string) bool {
	file, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected
}

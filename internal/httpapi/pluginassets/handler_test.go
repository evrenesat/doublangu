package pluginassets_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"doublangu/internal/httpapi/pluginassets"
)

func fixture(t *testing.T, body string) (string, string) {
	t.Helper()
	root := t.TempDir()
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	filename := filepath.Join(root, "v1", digest, "module.js")
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, digest
}

func newHandler(t *testing.T, root string) *pluginassets.Handler {
	t.Helper()
	h, err := pluginassets.New(root, pluginassets.DefaultPrefix, func(*http.Request) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func request(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

func TestNewRequiresAuthorization(t *testing.T) {
	if _, err := pluginassets.New(t.TempDir(), pluginassets.DefaultPrefix, nil); err == nil {
		t.Fatal("New accepted a nil authorization policy")
	}
}

func TestHandlerServesAuthorizedVersionedAssetImmutably(t *testing.T) {
	root, digest := fixture(t, "export default 1;\n")
	h := newHandler(t, root)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request(http.MethodGet, pluginassets.DefaultPrefix+"v1/"+digest+"/module.js"))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "export default 1;\n" {
		t.Fatalf("GET = %d %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}

	head := httptest.NewRecorder()
	h.ServeHTTP(head, request(http.MethodHead, pluginassets.DefaultPrefix+"v1/"+digest+"/module.js"))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d body=%q", head.Code, head.Body.String())
	}
}

func TestHandlerRejectsUnauthorizedMethodPrefixTraversalAndSymlinkEscape(t *testing.T) {
	root, digest := fixture(t, "export default 1;\n")
	denied, err := pluginassets.New(root, pluginassets.DefaultPrefix, func(*http.Request) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	denied.ServeHTTP(unauthorized, request(http.MethodGet, pluginassets.DefaultPrefix+"v1/"+digest+"/module.js"))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized = %d", unauthorized.Code)
	}

	h := newHandler(t, root)
	cases := []struct {
		name, method, target string
		want                 int
	}{
		{"method", http.MethodPost, pluginassets.DefaultPrefix + "v1/" + digest + "/module.js", http.StatusMethodNotAllowed},
		{"prefix", http.MethodGet, "/wrong/v1/" + digest + "/module.js", http.StatusNotFound},
		{"traversal", http.MethodGet, pluginassets.DefaultPrefix + "v1/" + digest + "/../module.js", http.StatusForbidden},
		{"missing", http.MethodGet, pluginassets.DefaultPrefix + "v1/" + digest + "/missing.js", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			h.ServeHTTP(recorder, request(tc.method, tc.target))
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}

	outside := filepath.Join(t.TempDir(), "secret.js")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "v1", digest, "escape.js")); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request(http.MethodGet, pluginassets.DefaultPrefix+"v1/"+digest+"/escape.js"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("symlink = %d", recorder.Code)
	}
}

func TestHandlerDoesNotGiveImmutableCachingToUnversionedOrMismatchedBytes(t *testing.T) {
	root, digest := fixture(t, "export default 1;\n")
	if err := os.MkdirAll(filepath.Join(root, "unversioned"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unversioned", "module.js"), []byte("export default 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, root)

	unversioned := httptest.NewRecorder()
	h.ServeHTTP(unversioned, request(http.MethodGet, pluginassets.DefaultPrefix+"unversioned/module.js"))
	if unversioned.Code != http.StatusOK || unversioned.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unversioned = %d cache=%q", unversioned.Code, unversioned.Header().Get("Cache-Control"))
	}

	if err := os.WriteFile(filepath.Join(root, "v1", digest, "module.js"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	mismatched := httptest.NewRecorder()
	h.ServeHTTP(mismatched, request(http.MethodGet, pluginassets.DefaultPrefix+"v1/"+digest+"/module.js"))
	if mismatched.Code != http.StatusNotFound {
		t.Fatalf("mismatched = %d", mismatched.Code)
	}
}

func TestHandlerDecodesExactlyOnce(t *testing.T) {
	root, digest := fixture(t, "export default 1;\n")
	h := newHandler(t, root)
	req := request(http.MethodGet, pluginassets.DefaultPrefix+"v1/"+digest+"/module.js")
	req.URL = &url.URL{Path: pluginassets.DefaultPrefix + "v1/" + digest + "/../module.js", RawPath: pluginassets.DefaultPrefix + "v1/" + digest + "/%2e%2e/module.js"}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("encoded traversal = %d", recorder.Code)
	}
}

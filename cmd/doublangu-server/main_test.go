package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/auth"
	"doublangu/internal/config"
	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	manifest "doublangu/internal/plugins"
	"doublangu/internal/reader"
	"doublangu/internal/store"
	v1 "doublangu/pkg/pluginapi/v1"
)

// testAuth creates a lightweight AuthHandler backed by an in-memory database
// with a pre-created owner.
func testAuth(t *testing.T) (*auth.AuthHandler, *store.DB) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ownerMgr := auth.NewOwnerManager(db)
	if err := ownerMgr.CreateOwner(t.Context(), "test-password-123", false); err != nil {
		t.Fatalf("CreateOwner: %v", err)
	}

	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}

	return &auth.AuthHandler{
		Sessions:      auth.NewSessionStore(db),
		OwnerManager:  ownerMgr,
		CSRF:          auth.NewCSRF(secret),
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		SessionMaxAge: time.Hour,
		Secure:        false,
	}, db
}

func testHealth(t *testing.T, db *store.DB) *httpapi.HealthHandler {
	t.Helper()
	return httpapi.NewHealthHandler(db)
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		Listen:    ":0",
		PublicURL: "http" + "://localhost:8080",
		Secret:    bytes.Repeat([]byte("x"), 32),
		Database:  config.DatabaseConfig{Path: ":memory:"},
		Session: config.SessionConfig{
			MaxAge:   time.Hour,
			Secure:   false,
			HTTPOnly: true,
			SameSite: "lax",
		},
		Paths: config.PathsConfig{
			Media: filepath.Join(dir, "media"),
			Data:  filepath.Join(dir, "data"),
		},
	}
}

func serverRequest(method, target, body, session, csrf string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
	}
	if csrf != "" {
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookie, Value: csrf})
		req.Header.Set(auth.CSRFHeader, csrf)
	}
	return req
}

func seedServerMedia(t *testing.T, db *store.DB, mediaRoot string) (string, string, []byte) {
	t.Helper()
	data := []byte("assembled media")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(mediaRoot, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaRoot, "blobs", digest), data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &library.Store{}
	var asset library.SourceAsset
	err := db.WithTransaction(t.Context(), func(tx *sql.Tx) error {
		lib, err := library.NewLibrary("Library", "nl", "en", "")
		if err != nil {
			return err
		}
		if err = s.CreateLibrary(t.Context(), tx, &lib); err != nil {
			return err
		}
		work, err := library.NewWork(lib.ID, "Work", "Author", "audiobook", "")
		if err != nil {
			return err
		}
		if err = s.CreateWork(t.Context(), tx, &work); err != nil {
			return err
		}
		edition, err := library.NewEdition(work.ID, "Edition", "nl", "mp3")
		if err != nil {
			return err
		}
		if err = s.CreateEdition(t.Context(), tx, &edition); err != nil {
			return err
		}
		chapter, err := library.NewChapter(edition.ID, "Chapter", 1, 0, int64(len(data)), int64(len(data)))
		if err != nil {
			return err
		}
		if err = s.CreateChapter(t.Context(), tx, &chapter); err != nil {
			return err
		}
		asset, err = library.NewSourceAsset(chapter.ID, "file:///media.mp3", "audio/mpeg", int64(len(data)), digest, 0, int64(len(data)), int64(len(data)))
		if err != nil {
			return err
		}
		return s.CreateSourceAsset(t.Context(), tx, &asset)
	})
	if err != nil {
		t.Fatal(err)
	}
	return asset.ID.String(), digest, data
}

func TestAssembledLibraryAndMediaAuthCSRF(t *testing.T) {
	ah, db := testAuth(t)
	handler := newHandler(manifest.NewRegistry(), &manifest.ParsedSchema{}, ah, testHealth(t, db), testConfig(t), db)
	for _, target := range []string{"/api/v1/libraries", "/api/v1/media/01J00000000000000000000000"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, serverRequest(http.MethodGet, target, "", "", ""))
		if rec.Code != http.StatusUnauthorized || decodeServerError(t, rec).Code != httpapi.ErrCodeAuth {
			t.Fatalf("%s status=%d", target, rec.Code)
		}
	}
	session, err := ah.Sessions.Create(t.Context(), time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := ah.CSRF.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		serverRequest(http.MethodPost, "/api/v1/libraries/not-a-ulid/works", "{", session, ""),
		serverRequest(http.MethodPost, "/api/v1/libraries/not-a-ulid/works", "{", session, "bad"),
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request)
		if rec.Code != http.StatusForbidden || decodeServerError(t, rec).Code != httpapi.ErrCodeCSRF {
			t.Fatalf("csrf ordering status=%d", rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, serverRequest(http.MethodPost, "/api/v1/libraries", `{"name":"Dutch","source_language":"nl","target_language":"en"}`, session, csrf))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created library.Library
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil || created.ID == "" || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, serverRequest(http.MethodGet, "/api/v1/libraries/"+created.ID.String(), "", session, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("stored CRUD status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, serverRequest(http.MethodGet, "/api/v1/media/not-a-ulid", "", session, ""))
	if rec.Code != http.StatusBadRequest || decodeServerError(t, rec).Code != httpapi.ErrCodeValidation {
		t.Fatalf("media handler status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, serverRequest(http.MethodGet, "/api/v1/media/%2E%2E", "", session, ""))
	if rec.Code != http.StatusBadRequest || decodeServerError(t, rec).Code != httpapi.ErrCodeValidation {
		t.Fatalf("escaped media traversal status=%d", rec.Code)
	}
}

func TestAssembledArticleRoutesRequireOwnerAndCSRF(t *testing.T) {
	ah, db := testAuth(t)
	handler := newHandler(manifest.NewRegistry(), &manifest.ParsedSchema{}, ah, testHealth(t, db), testConfig(t), db, annotator.Disabled{})

	for _, target := range []string{"/api/v1/articles", "/api/v1/analysis/models", "/api/v1/reader/settings"} {
		unauthorized := httptest.NewRecorder()
		handler.ServeHTTP(unauthorized, serverRequest(http.MethodGet, target, "", "", ""))
		if unauthorized.Code != http.StatusUnauthorized || decodeServerError(t, unauthorized).Code != httpapi.ErrCodeAuth {
			t.Fatalf("unauthorized %s = %d", target, unauthorized.Code)
		}
		if (strings.HasPrefix(target, "/api/v1/analysis") || strings.HasPrefix(target, "/api/v1/reader")) && unauthorized.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unauthorized %s cache control = %q", target, unauthorized.Header().Get("Cache-Control"))
		}
	}

	session, err := ah.Sessions.Create(t.Context(), time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	withoutCSRF := httptest.NewRecorder()
	handler.ServeHTTP(withoutCSRF, serverRequest(http.MethodPost, "/api/v1/articles", `{}`, session, ""))
	if withoutCSRF.Code != http.StatusForbidden || decodeServerError(t, withoutCSRF).Code != httpapi.ErrCodeCSRF {
		t.Fatalf("article CSRF response = %d", withoutCSRF.Code)
	}
	withoutCSRF = httptest.NewRecorder()
	handler.ServeHTTP(withoutCSRF, serverRequest(http.MethodPut, "/api/v1/analysis/settings", `{}`, session, ""))
	if withoutCSRF.Code != http.StatusForbidden || decodeServerError(t, withoutCSRF).Code != httpapi.ErrCodeCSRF {
		t.Fatalf("analysis settings CSRF response = %d", withoutCSRF.Code)
	}
	withoutCSRF = httptest.NewRecorder()
	handler.ServeHTTP(withoutCSRF, serverRequest(http.MethodPut, "/api/v1/reader/settings", `{}`, session, ""))
	if withoutCSRF.Code != http.StatusForbidden || decodeServerError(t, withoutCSRF).Code != httpapi.ErrCodeCSRF {
		t.Fatalf("reader settings CSRF response = %d", withoutCSRF.Code)
	}

	csrf, err := ah.CSRF.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, serverRequest(http.MethodPost, "/api/v1/articles", `{"title":"Test","body":"Een zin.","source_language":"nl","target_language":"en"}`, session, csrf))
	if created.Code != http.StatusCreated {
		t.Fatalf("article create = %d %s", created.Code, created.Body.String())
	}
	var article reader.Article
	if err := json.NewDecoder(created.Body).Decode(&article); err != nil || article.ID.IsZero() {
		t.Fatalf("created article = %+v err=%v", article, err)
	}
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, serverRequest(http.MethodGet, "/api/v1/articles/"+article.ID.String(), "", session, ""))
	if got.Code != http.StatusOK {
		t.Fatalf("article get = %d %s", got.Code, got.Body.String())
	}
}

func TestAssembledMediaRedirectConfig(t *testing.T) {
	ah, db := testAuth(t)
	mediaRoot := t.TempDir()
	id, digest, data := seedServerMedia(t, db, mediaRoot)
	session, err := ah.Sessions.Create(t.Context(), time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	directConfig := testConfig(t)
	directConfig.Paths.Media = mediaRoot
	direct := newHandler(manifest.NewRegistry(), &manifest.ParsedSchema{}, ah, testHealth(t, db), directConfig, db)
	rec := httptest.NewRecorder()
	direct.ServeHTTP(rec, serverRequest(http.MethodGet, "/api/v1/media/"+id, "", session, ""))
	if rec.Code != http.StatusOK || rec.Body.String() != string(data) || rec.Header().Get("X-Accel-Redirect") != "" {
		t.Fatalf("direct status=%d body=%q redirect=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-Accel-Redirect"))
	}
	t.Setenv("DOUBLANGU_SECRET", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 32)))
	t.Setenv("DOUBLANGU_MEDIA_PATH", mediaRoot)
	t.Setenv("DOUBLANGU_MEDIA_REDIRECT", "/_media-internal/")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	accelerated := newHandler(manifest.NewRegistry(), &manifest.ParsedSchema{}, ah, testHealth(t, db), cfg, db)
	rec = httptest.NewRecorder()
	accelerated.ServeHTTP(rec, serverRequest(http.MethodGet, "/api/v1/media/"+id, "", session, ""))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("X-Accel-Redirect") != "/_media-internal/"+digest {
		t.Fatalf("accelerated status=%d body=%q redirect=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-Accel-Redirect"))
	}
	t.Setenv("DOUBLANGU_MEDIA_REDIRECT", "/invalid")
	if _, err := config.Load(); err == nil {
		t.Fatal("invalid redirect configuration loaded")
	}
}

func decodeServerError(t *testing.T, rec *httptest.ResponseRecorder) httpapi.APIError {
	t.Helper()
	var body httpapi.APIError
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestServerHandlerAssemblyDoesNotUseGlobalMux(t *testing.T) {
	registry := manifest.NewRegistry()
	ah, db := testAuth(t)
	hh := testHealth(t, db)
	cfg := testConfig(t)

	first := newHandler(registry, &manifest.ParsedSchema{}, ah, hh, cfg, db)
	second := newHandler(registry, &manifest.ParsedSchema{}, ah, hh, cfg, db)

	unauthorizedHealth := httptest.NewRecorder()
	first.ServeHTTP(unauthorizedHealth, httptest.NewRequest(http.MethodGet, "/health", nil))
	if unauthorizedHealth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health status = %d, want 401", unauthorizedHealth.Code)
	}
	session, err := ah.Sessions.Create(t.Context(), time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewRecorder()
	first.ServeHTTP(health, serverRequest(http.MethodGet, "/health", "", session, ""))
	if health.Code != http.StatusOK || health.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("health response = status %d content-type %q", health.Code, health.Header().Get("Content-Type"))
	}
	var report manifest.DiagnosticsReport
	if err := json.Unmarshal(health.Body.Bytes(), &report); err != nil {
		t.Fatalf("health JSON: %v", err)
	}
	if !report.CoreReady || !report.LoaderReady || report.PluginCount != 0 {
		t.Errorf("health report = %+v", report)
	}

	unknown := httptest.NewRecorder()
	second.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/not-registered", nil))
	if unknown.Code != http.StatusNotFound {
		t.Errorf("separate mux status = %d, want 404", unknown.Code)
	}
}

func TestUIContributionsEndpointReturnsVersionedSnakeCasePayload(t *testing.T) {
	registry := manifest.NewRegistry()
	transaction := registry.Begin("plugin.sample")
	if err := transaction.AddUI(v1.UIRegistration{
		ID: "sample", Label: "Sample", Type: v1.UITypePanel, Priority: 10,
		SourceURL: "/api/v1/plugins/assets/v1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/module.js",
	}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	ah, db := testAuth(t)
	hh := testHealth(t, db)
	cfg := testConfig(t)
	handler := newHandler(registry, &manifest.ParsedSchema{}, ah, hh, cfg, db)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/ui/contributions", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated UI contributions = %d", unauthorized.Code)
	}
	var apiError httpapi.APIError
	if err := json.NewDecoder(unauthorized.Body).Decode(&apiError); err != nil || apiError.Code != httpapi.ErrCodeAuth {
		t.Fatalf("unauthenticated error = %+v err=%v", apiError, err)
	}
	session, err := ah.Sessions.Create(t.Context(), time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ui/contributions", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"version":"v1"`) || !strings.Contains(body, `"source_url"`) || !strings.Contains(body, `"plugin_id":"plugin.sample"`) || strings.Contains(body, `"sourceUrl"`) {
		t.Fatalf("payload = %s", body)
	}
}

func TestAssembledPluginAssetsRequireOwnerSession(t *testing.T) {
	root := t.TempDir()
	body := []byte("export default 1;\n")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	filename := filepath.Join(root, "v1", digest, "module.js")
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOUBLANGU_PLUGIN_ASSETS", root)
	ah, db := testAuth(t)
	cfg := testConfig(t)
	handler := newHandler(manifest.NewRegistry(), &manifest.ParsedSchema{}, ah, testHealth(t, db), cfg, db)
	path := "/api/v1/plugins/assets/v1/" + digest + "/module.js"

	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, path, nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized plugin asset = %d", denied.Code)
	}
	session, err := ah.Sessions.Create(t.Context(), time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	allowedRequest := httptest.NewRequest(http.MethodGet, path, nil)
	allowedRequest.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, allowedRequest)
	if allowed.Code != http.StatusOK || allowed.Body.String() != string(body) {
		t.Fatalf("authorized plugin asset = %d %q", allowed.Code, allowed.Body.String())
	}
}

func TestProductionCSRFBootstrapLoginFlow(t *testing.T) {
	registry := manifest.NewRegistry()
	ah, db := testAuth(t)
	cfg := testConfig(t)
	server := httptest.NewServer(newHandler(registry, &manifest.ParsedSchema{}, ah, testHealth(t, db), cfg, db))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	bootstrap, err := client.Get(server.URL + "/api/v1/auth/csrf")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.Body.Close()
	if bootstrap.StatusCode != http.StatusOK {
		t.Fatalf("csrf bootstrap = %d", bootstrap.StatusCode)
	}
	var csrfToken string
	for _, item := range jar.Cookies(bootstrap.Request.URL) {
		if item.Name == auth.CSRFCookie {
			csrfToken = item.Value
		}
	}
	if csrfToken == "" {
		t.Fatal("production bootstrap did not populate CSRF cookie jar")
	}

	postLogin := func(password string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(auth.CSRFHeader, csrfToken)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	wrong := postLogin("wrong-password")
	if wrong.StatusCode != http.StatusUnauthorized {
		wrong.Body.Close()
		t.Fatalf("wrong login = %d", wrong.StatusCode)
	}
	var wrongError httpapi.APIError
	if err := json.NewDecoder(wrong.Body).Decode(&wrongError); err != nil || wrongError.Code != httpapi.ErrCodeAuth {
		wrong.Body.Close()
		t.Fatalf("wrong login error = %+v err=%v", wrongError, err)
	}
	wrong.Body.Close()

	success := postLogin("test-password-123")
	if success.StatusCode != http.StatusOK {
		success.Body.Close()
		t.Fatalf("correct login = %d", success.StatusCode)
	}
	success.Body.Close()
	status, err := client.Get(server.URL + "/api/v1/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	defer status.Body.Close()
	var session map[string]bool
	if err := json.NewDecoder(status.Body).Decode(&session); err != nil || !session["authenticated"] {
		t.Fatalf("session status = %+v err=%v", session, err)
	}
}

func TestOwnerCLIUsesPasswordInputWithoutLeakingIt(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("s"), 32))
	databasePath := filepath.Join(t.TempDir(), "nested", "owner.db")
	t.Setenv("DOUBLANGU_SECRET", secret)
	t.Setenv("DOUBLANGU_DB_PATH", databasePath)

	runOwner := func(args []string, input string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(args, strings.NewReader(input), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	password := "owner-secret-password"
	if code, stdout, stderr := runOwner([]string{"--create-owner"}, password+"\n"); code != 0 || !strings.Contains(stdout, "created") || strings.Contains(stdout+stderr, password) {
		t.Fatalf("create code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if code, _, stderr := runOwner([]string{"--create-owner"}, password+"\n"); code != 1 || !strings.Contains(stderr, "already exists") || strings.Contains(stderr, password) {
		t.Fatalf("duplicate create code=%d stderr=%q", code, stderr)
	}
	if code, stdout, stderr := runOwner([]string{"--reset-owner"}, "replacement-secret\n"); code != 0 || !strings.Contains(stdout, "reset") || strings.Contains(stdout+stderr, "replacement-secret") {
		t.Fatalf("reset code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if code, _, stderr := runOwner([]string{"--create-owner", "--reset-owner"}, "ignored\n"); code != 2 || strings.Contains(stderr, "ignored") {
		t.Fatalf("conflict code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runOwner([]string{"--create-owner=argv-secret"}, "ignored\n"); code != 2 || strings.Contains(stderr, "argv-secret") {
		t.Fatalf("argv redaction code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runOwner([]string{"--create-owner"}, ""); code != 1 || !strings.Contains(stderr, "EOF") || strings.Contains(stderr, password) {
		t.Fatalf("EOF code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runOwner([]string{"--create-owner"}, "short\n"); code != 1 || strings.Contains(stderr, "short") {
		t.Fatalf("short-password redaction code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Dir(databasePath)); err != nil {
		t.Fatalf("database parent was not created: %v", err)
	}
	startupSecret := "startup-secret-must-not-leak"
	t.Setenv("DOUBLANGU_SECRET", startupSecret)
	var startupOut, startupErr bytes.Buffer
	if code := run(nil, strings.NewReader(""), &startupOut, &startupErr); code != 1 || strings.Contains(startupOut.String()+startupErr.String(), startupSecret) {
		t.Fatalf("startup redaction code=%d stdout=%q stderr=%q", code, startupOut.String(), startupErr.String())
	}
}

func TestLiveEndpoint(t *testing.T) {
	registry := manifest.NewRegistry()
	ah, db := testAuth(t)
	hh := testHealth(t, db)
	cfg := testConfig(t)

	recorder := httptest.NewRecorder()
	newHandler(registry, &manifest.ParsedSchema{}, ah, hh, cfg, db).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/live", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("live status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var resp httpapi.LivenessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode live: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("live status = %q", resp.Status)
	}
}

func TestReadyEndpoint(t *testing.T) {
	registry := manifest.NewRegistry()
	ah, db := testAuth(t)
	hh := testHealth(t, db)
	cfg := testConfig(t)

	recorder := httptest.NewRecorder()
	newHandler(registry, &manifest.ParsedSchema{}, ah, hh, cfg, db).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var resp httpapi.ReadinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode ready: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("ready status = %q", resp.Status)
	}
}

func TestAuthEndpointsRegistered(t *testing.T) {
	registry := manifest.NewRegistry()
	ah, db := testAuth(t)
	hh := testHealth(t, db)
	cfg := testConfig(t)
	handler := newHandler(registry, &manifest.ParsedSchema{}, ah, hh, cfg, db)

	// POST /api/v1/auth/login should exist (even if it returns 403 for missing CSRF).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`)))
	if rec.Code == http.StatusNotFound {
		t.Fatal("login endpoint not found")
	}

	// POST /api/v1/auth/logout should exist.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatal("logout endpoint not found")
	}

	// GET /api/v1/auth/session should exist.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("session endpoint status = %d", rec.Code)
	}
}

func TestZeroPluginServerSmoke(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "doublangu-server")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/doublangu-server")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}

	// Generate a valid SECRET for the smoke test.
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	secretEnv := base64.StdEncoding.EncodeToString(secret)

	command := exec.Command(binary)
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(),
		"DOUBLANGU_LISTEN=127.0.0.1:0",
		"DOUBLANGU_SECRET="+secretEnv,
		"DOUBLANGU_DB_PATH="+filepath.Join(t.TempDir(), "server", "doublangu.db"),
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("server stdout pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	var startup []string
	var address string
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for address == "" {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("server stopped before listening: %s", stderr.String())
			}
			startup = append(startup, line)
			if strings.HasPrefix(line, "listening on ") {
				address = strings.TrimPrefix(line, "listening on ")
			}
		case <-timeout.C:
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
			t.Fatalf("server did not announce an ephemeral listener: %s", stderr.String())
		}
	}

	// Plugin diagnostics require an owner session.
	endpoint := "http:" + "//" + address + "/health"
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		t.Fatalf("request health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health status = %d, want 401", response.StatusCode)
	}

	// Check /live endpoint.
	liveResp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http:" + "//" + address + "/live")
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		t.Fatalf("request live: %v", err)
	}
	liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK {
		t.Errorf("live status = %d", liveResp.StatusCode)
	}

	// Check /ready endpoint.
	readyResp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http:" + "//" + address + "/ready")
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		t.Fatalf("request ready: %v", err)
	}
	readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Errorf("ready status = %d", readyResp.StatusCode)
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt server: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Errorf("server exit = %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("server did not terminate after interrupt")
	}
	if !strings.Contains(strings.Join(startup, "\n"), "feature plugins: 0") {
		t.Errorf("startup banner = %q", startup)
	}
	if !strings.Contains(strings.Join(startup, "\n"), "database: ok") {
		t.Errorf("startup banner missing database line: %q", startup)
	}
	if stderr.Len() != 0 {
		t.Errorf("server stderr = %q", stderr.String())
	}
}

func findRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("current directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod not found")
		}
		directory = parent
	}
}

type seedFakeProvider struct {
	descriptor annotator.ProviderDescriptor
	models     []annotator.Model
	listErr    error
}

func (p *seedFakeProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *seedFakeProvider) ListModels(context.Context) ([]annotator.Model, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	return p.models, nil
}
func (p *seedFakeProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return nil, errors.New("no sessions in seed tests")
}

type seedFakeCatalog struct {
	providers map[string]annotator.Provider
}

func (c *seedFakeCatalog) ListModels(ctx context.Context, id string) ([]annotator.Model, error) {
	provider, ok := c.providers[id]
	if !ok {
		return nil, errors.New("unknown provider")
	}
	return provider.ListModels(ctx)
}

func seedBootstrapFile(modelID, effort string) *config.ProviderConfigFile {
	options, _ := json.Marshal(map[string]string{"reasoning_effort": effort})
	return &config.ProviderConfigFile{
		Version: config.ProviderConfigVersion,
		Providers: []config.ProviderEntry{{
			ID: "codex-app-server", Label: "Codex", EndpointLabel: "Local",
			Type: config.ProviderTypeCodexAppServer, Enabled: true,
		}},
		BootstrapProfile: &config.BootstrapProfileConfig{
			Name: "Bootstrap",
			Bindings: map[pipeline.StageID]config.BootstrapBindingConfig{
				pipeline.StageLinguisticAnalysis: {ProviderID: "codex-app-server", ModelID: modelID, Options: options},
				pipeline.StageTranslation:        {ProviderID: "codex-app-server", ModelID: modelID, Options: options},
			},
		},
	}
}

func seedCatalog() *seedFakeCatalog {
	return &seedFakeCatalog{providers: map[string]annotator.Provider{
		"codex-app-server": &seedFakeProvider{
			descriptor: annotator.ProviderDescriptor{ID: "codex-app-server", Type: config.ProviderTypeCodexAppServer, Enabled: true},
			models: []annotator.Model{{
				ID: "model-a", DisplayName: "Model A",
				SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "low"}, {Value: "medium"}},
			}},
		},
	}}
}

func TestSeedBootstrapValidatesModelsBeforeActivation(t *testing.T) {
	newDB := func(t *testing.T) *store.DB {
		db, err := store.OpenTest()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}

	t.Run("valid bootstrap seeds and activates", func(t *testing.T) {
		db := newDB(t)
		seedBootstrapProfile(db, seedBootstrapFile("model-a", "low"), seedCatalog())
		profiles := analysis.NewProfileStore(db)
		active, err := profiles.ActiveProfile(context.Background())
		if err != nil || active == "" {
			t.Fatalf("active = %q err=%v", active, err)
		}
		stored, err := profiles.Get(context.Background(), active)
		if err != nil || stored.Name != "Bootstrap" || len(stored.Bindings) != 2 {
			t.Fatalf("stored = %+v err=%v", stored, err)
		}
	})

	for _, test := range []struct {
		name   string
		file   *config.ProviderConfigFile
		mutate func(*seedFakeCatalog)
	}{
		{"unlisted model", seedBootstrapFile("ghost-model", "low"), nil},
		{"unsupported effort", seedBootstrapFile("model-a", "extreme"), nil},
		{"catalog unavailable", seedBootstrapFile("model-a", "low"), func(c *seedFakeCatalog) {
			c.providers["codex-app-server"] = &seedFakeProvider{
				descriptor: annotator.ProviderDescriptor{ID: "codex-app-server", Type: config.ProviderTypeCodexAppServer, Enabled: true},
				listErr:    errors.New("catalog offline"),
			}
		}},
	} {
		t.Run(test.name+" leaves profiles empty", func(t *testing.T) {
			db := newDB(t)
			catalog := seedCatalog()
			if test.mutate != nil {
				test.mutate(catalog)
			}
			seedBootstrapProfile(db, test.file, catalog)
			count, err := analysis.NewProfileStore(db).Count(context.Background())
			if err != nil || count != 0 {
				t.Fatalf("profiles = %d err=%v, want none", count, err)
			}
		})
	}
}

func TestCompatibilityProviderFile(t *testing.T) {
	newDB := func(t *testing.T) *store.DB {
		db, err := store.OpenTest()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	newConfig := func(annotator, model, effort string) *config.Config {
		return &config.Config{Annotator: annotator, CodexModel: model, CodexEffort: effort}
	}

	file, err := compatibilityProviderFile(newDB(t), newConfig("codex", "gpt-x", "medium"))
	if err != nil {
		t.Fatalf("file = %v", err)
	}
	if len(file.Providers) != 1 || file.Providers[0].ID != "codex-app-server" || !file.Providers[0].Enabled {
		t.Fatalf("providers = %+v", file.Providers)
	}
	if file.BootstrapProfile == nil || file.BootstrapProfile.Name != "Imported Codex" {
		t.Fatalf("bootstrap = %+v", file.BootstrapProfile)
	}
	for _, stage := range pipeline.RegisteredStages() {
		binding, ok := file.BootstrapProfile.Bindings[stage]
		if !ok || binding.ProviderID != "codex-app-server" || binding.ModelID != "gpt-x" {
			t.Fatalf("stage %s binding = %+v", stage, binding)
		}
	}

	disabled, err := compatibilityProviderFile(newDB(t), newConfig("disabled", "gpt-x", "medium"))
	if err != nil {
		t.Fatalf("disabled file = %v", err)
	}
	if disabled.Providers[0].Enabled || disabled.BootstrapProfile != nil {
		t.Fatalf("disabled file = %+v", disabled)
	}

	blank, err := compatibilityProviderFile(newDB(t), newConfig("codex", "", "medium"))
	if err != nil {
		t.Fatalf("blank file = %v", err)
	}
	if blank.BootstrapProfile != nil {
		t.Fatalf("blank model bootstrap = %+v", blank.BootstrapProfile)
	}
}

func TestCompatibilityProviderFileImportsPersistedSettings(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// An existing installation saved its selection through the Settings API
	// while the model environment variable is unset (the upgrade case).
	if _, err := analysis.NewSettingsStore(db).Save(context.Background(), "persisted-model", "high"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	file, err := compatibilityProviderFile(db, &config.Config{Annotator: "codex", CodexEffort: "medium"})
	if err != nil {
		t.Fatalf("file = %v", err)
	}
	if file.BootstrapProfile == nil || file.BootstrapProfile.Name != "Imported Codex" {
		t.Fatalf("bootstrap = %+v", file.BootstrapProfile)
	}
	for _, stage := range pipeline.RegisteredStages() {
		binding, ok := file.BootstrapProfile.Bindings[stage]
		if !ok || binding.ProviderID != "codex-app-server" || binding.ModelID != "persisted-model" {
			t.Fatalf("stage %s binding = %+v", stage, binding)
		}
		var options struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.Unmarshal(binding.Options, &options); err != nil || options.ReasoningEffort != "high" {
			t.Fatalf("stage %s options = %s err=%v", stage, binding.Options, err)
		}
	}
}

func TestLegacyProviderSelection(t *testing.T) {
	t.Setenv("DOUBLANGU_ANNOTATOR", "")
	t.Setenv("DOUBLANGU_CODEX_MODEL", "")
	t.Setenv("DOUBLANGU_CODEX_EFFORT", "")
	if name, set := legacyProviderSelection(); set {
		t.Fatalf("selection = %q, want unset", name)
	}
	t.Setenv("DOUBLANGU_CODEX_MODEL", "gpt-x")
	if name, set := legacyProviderSelection(); !set || name != "DOUBLANGU_CODEX_MODEL" {
		t.Fatalf("selection = %q %v", name, set)
	}
}

func TestLoadProviderRegistrySynthesizesCompatibilityProvider(t *testing.T) {
	t.Setenv("DOUBLANGU_PROVIDER_CONFIG", "")
	t.Setenv("DOUBLANGU_ANNOTATOR", "")
	t.Setenv("DOUBLANGU_CODEX_MODEL", "")
	t.Setenv("DOUBLANGU_CODEX_EFFORT", "")
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	registry, err := loadProviderRegistry(db, &config.Config{Annotator: "codex", CodexEffort: "medium"}, nil)
	if err != nil {
		t.Fatalf("load = %v", err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 1 || descriptors[0].ID != "codex-app-server" || !descriptors[0].Enabled {
		t.Fatalf("descriptors = %+v", descriptors)
	}
	if _, ok := registry.Provider("codex-app-server"); !ok {
		t.Fatal("synthetic provider not resolvable")
	}
	count, err := analysis.NewProfileStore(db).Count(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("profiles = %d err=%v, want none for a blank legacy model", count, err)
	}
}

func TestLoadProviderRegistryRejectsCombinedSources(t *testing.T) {
	t.Setenv("DOUBLANGU_PROVIDER_CONFIG", "/tmp/doublangu-providers.json")
	t.Setenv("DOUBLANGU_CODEX_MODEL", "gpt-x")
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := loadProviderRegistry(db, &config.Config{Annotator: "codex", CodexModel: "gpt-x", CodexEffort: "medium"}, nil); err == nil {
		t.Fatal("combined file and legacy selection accepted, want startup error")
	}
}

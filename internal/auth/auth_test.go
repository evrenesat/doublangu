package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"doublangu/internal/httpapi"
	"doublangu/internal/store"
)

const testPassword = "test-password-123"

func newTestAuthHandler(db *store.DB) *AuthHandler {
	return &AuthHandler{
		Sessions:      NewSessionStore(db),
		OwnerManager:  NewOwnerManager(db),
		CSRF:          NewCSRF([]byte("test-secret-32-bytes-long!!!!!!")),
		RateLimiter:   NewRateLimiter(100, time.Minute),
		SessionMaxAge: time.Hour,
	}
}

func cookie(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, item := range response.Cookies() {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("response did not set %s", name)
	return nil
}

// bootstrap obtains the production CSRF cookie path; login tests never mint a
// token directly through CSRF.GenerateToken.
func bootstrap(t *testing.T, handler *AuthHandler) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeCSRF(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("csrf bootstrap = %d: %s", recorder.Code, recorder.Body.String())
	}
	return cookie(t, recorder.Result(), CSRFCookie)
}

func loginRequest(password string, csrf *http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(CSRFHeader, csrf.Value)
	request.AddCookie(csrf)
	request.RemoteAddr = "198.51.100.8:443"
	return request
}

func login(t *testing.T, handler *AuthHandler, password string) (*http.Cookie, *http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	csrf := bootstrap(t, handler)
	recorder := httptest.NewRecorder()
	handler.ServeLogin(recorder, loginRequest(password, csrf))
	return cookie(t, recorder.Result(), SessionCookie), cookie(t, recorder.Result(), CSRFCookie), recorder
}

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, "correct-horse-battery-staple"); err != nil {
		t.Fatalf("correct password: %v", err)
	}
	if err := VerifyPassword(hash, "wrong-password"); err == nil {
		t.Fatal("wrong password was accepted")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password was accepted")
	}
}

func TestSessionCreateRotateValidateAndDelete(t *testing.T) {
	if err := store.WithTestDB(func(db *store.DB) error {
		ctx := context.Background()
		sessions := NewSessionStore(db)
		original, err := sessions.Create(ctx, time.Hour, "test-agent")
		if err != nil {
			return err
		}
		replacement, err := sessions.Rotate(ctx, original, time.Hour, "test-agent")
		if err != nil {
			return err
		}
		if replacement == original {
			t.Fatal("session token did not rotate")
		}
		if valid, err := sessions.Validate(ctx, original); err != nil || valid {
			t.Fatalf("old token valid=%t err=%v", valid, err)
		}
		if valid, err := sessions.Validate(ctx, replacement); err != nil || !valid {
			t.Fatalf("replacement valid=%t err=%v", valid, err)
		}
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM session").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("session count = %d, want exactly one", count)
		}
		return sessions.Delete(ctx, replacement)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCSRFValidationAndCookieAttributes(t *testing.T) {
	csrf := NewCSRF([]byte("test-secret-32-bytes-long!!!!!!"))
	token, err := csrf.GenerateToken()
	if err != nil || !csrf.ValidateToken(token) || csrf.ValidateToken(token+"x") {
		t.Fatalf("csrf validation token=%q err=%v", token, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.AddCookie(&http.Cookie{Name: CSRFCookie, Value: token})
	request.Header.Set(CSRFHeader, token)
	if err := csrf.VerifyRequest(request); err != nil {
		t.Fatalf("valid csrf request: %v", err)
	}
	recorder := httptest.NewRecorder()
	csrf.SetCookie(recorder, token, true)
	issued := cookie(t, recorder.Result(), CSRFCookie)
	if issued.HttpOnly || !issued.Secure || issued.SameSite != http.SameSiteStrictMode {
		t.Fatalf("csrf cookie = %+v", issued)
	}
}

func TestAuthMethodFailuresUseStructuredErrors(t *testing.T) {
	if err := store.WithTestDB(func(db *store.DB) error {
		handler := newTestAuthHandler(db)
		cases := []struct {
			name   string
			serve  func(http.ResponseWriter, *http.Request)
			method string
			path   string
		}{
			{name: "csrf", serve: handler.ServeCSRF, method: http.MethodPost, path: "/api/v1/auth/csrf"},
			{name: "login", serve: handler.ServeLogin, method: http.MethodGet, path: "/api/v1/auth/login"},
			{name: "logout", serve: handler.ServeLogout, method: http.MethodGet, path: "/api/v1/auth/logout"},
			{name: "session", serve: handler.ServeSessionCheck, method: http.MethodPost, path: "/api/v1/auth/session"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				testCase.serve(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
				if recorder.Code != http.StatusMethodNotAllowed {
					t.Fatalf("status = %d", recorder.Code)
				}
				var apiError httpapi.APIError
				if err := json.NewDecoder(recorder.Body).Decode(&apiError); err != nil || apiError.Code != httpapi.ErrCodeMethodNotAllow || apiError.Error == "" {
					t.Fatalf("response = %+v err=%v", apiError, err)
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimiterIgnoresUntrustedForwardedHeaders(t *testing.T) {
	limiter := NewRateLimiter(2, time.Hour)
	for _, forwarded := range []string{"203.0.113.1", "203.0.113.2"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		request.RemoteAddr = "198.51.100.8:4567"
		request.Header.Set("X-Forwarded-For", forwarded)
		request.Header.Set("X-Real-IP", forwarded)
		if !limiter.AllowRequest(request) {
			t.Fatalf("request with rotated header %q was unexpectedly denied", forwarded)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	request.RemoteAddr = "198.51.100.8:4567"
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	if limiter.AllowRequest(request) {
		t.Fatal("forwarded header rotation evaded the remote-peer rate limit")
	}
}

func TestOwnerResetRevokesSessionsAtomically(t *testing.T) {
	if err := store.WithTestDB(func(db *store.DB) error {
		ctx := context.Background()
		owner := NewOwnerManager(db)
		if err := owner.CreateOwner(ctx, testPassword, false); err != nil {
			return err
		}
		sessions := NewSessionStore(db)
		token, err := sessions.Create(ctx, time.Hour, "test")
		if err != nil {
			return err
		}
		if err := owner.CreateOwner(ctx, "new-password-456", true); err != nil {
			return err
		}
		if err := owner.VerifyOwnerPassword(ctx, testPassword); err == nil {
			t.Fatal("old password remained valid")
		}
		if err := owner.VerifyOwnerPassword(ctx, "new-password-456"); err != nil {
			return err
		}
		if valid, err := sessions.Validate(ctx, token); err != nil || valid {
			t.Fatalf("reset session valid=%t err=%v", valid, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerResetRollbackPreservesHashAndSessions(t *testing.T) {
	if err := store.WithTestDB(func(db *store.DB) error {
		ctx := context.Background()
		owner := NewOwnerManager(db)
		if err := owner.CreateOwner(ctx, testPassword, false); err != nil {
			return err
		}
		sessions := NewSessionStore(db)
		token, err := sessions.Create(ctx, time.Hour, "test")
		if err != nil {
			return err
		}
		owner.afterOwnerUpdate = func(*sql.Tx) error { return errors.New("injected reset failure") }
		if err := owner.CreateOwner(ctx, "new-password-456", true); err == nil {
			t.Fatal("reset unexpectedly succeeded")
		}
		if err := owner.VerifyOwnerPassword(ctx, testPassword); err != nil {
			t.Fatalf("old password changed after rollback: %v", err)
		}
		if err := owner.VerifyOwnerPassword(ctx, "new-password-456"); err == nil {
			t.Fatal("new password survived rollback")
		}
		if valid, err := sessions.Validate(ctx, token); err != nil || !valid {
			t.Fatalf("session valid=%t err=%v after rollback", valid, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginLogoutCSRFAndStructuredErrors(t *testing.T) {
	if err := store.WithTestDB(func(db *store.DB) error {
		ctx := context.Background()
		handler := newTestAuthHandler(db)
		if err := handler.OwnerManager.CreateOwner(ctx, testPassword, false); err != nil {
			return err
		}

		bootstrapCookie := bootstrap(t, handler)
		wrong := httptest.NewRecorder()
		handler.ServeLogin(wrong, loginRequest("wrong-password", bootstrapCookie))
		if wrong.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password status = %d", wrong.Code)
		}
		var loginError httpapi.APIError
		if err := json.NewDecoder(wrong.Body).Decode(&loginError); err != nil || loginError.Code != httpapi.ErrCodeAuth || loginError.Error == "" {
			t.Fatalf("wrong password error = %+v err=%v", loginError, err)
		}

		session, csrf, success := login(t, handler, testPassword)
		if success.Code != http.StatusOK || !session.HttpOnly || csrf.HttpOnly {
			t.Fatalf("login status=%d session=%+v csrf=%+v", success.Code, session, csrf)
		}
		for _, mutation := range []struct {
			name   string
			cookie *http.Cookie
			header string
		}{
			{name: "missing"},
			{name: "mismatched", cookie: csrf, header: bootstrapCookie.Value},
			{name: "tampered", cookie: &http.Cookie{Name: CSRFCookie, Value: csrf.Value + "x"}, header: csrf.Value + "x"},
		} {
			t.Run(mutation.name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
				request.AddCookie(session)
				if mutation.cookie != nil {
					request.AddCookie(mutation.cookie)
				}
				request.Header.Set(CSRFHeader, mutation.header)
				recorder := httptest.NewRecorder()
				handler.ServeLogout(recorder, request)
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("logout %s = %d", mutation.name, recorder.Code)
				}
				if valid, err := handler.Sessions.Validate(ctx, session.Value); err != nil || !valid {
					t.Fatalf("session changed after %s: valid=%t err=%v", mutation.name, valid, err)
				}
			})
		}

		logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		logout.AddCookie(session)
		logout.AddCookie(csrf)
		logout.Header.Set(CSRFHeader, csrf.Value)
		logoutRecorder := httptest.NewRecorder()
		handler.ServeLogout(logoutRecorder, logout)
		if logoutRecorder.Code != http.StatusOK {
			t.Fatalf("valid logout = %d", logoutRecorder.Code)
		}
		if valid, err := handler.Sessions.Validate(ctx, session.Value); err != nil || valid {
			t.Fatalf("session survived valid logout: valid=%t err=%v", valid, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginRotatesPresentedSessionAndPrivateAuth(t *testing.T) {
	if err := store.WithTestDB(func(db *store.DB) error {
		ctx := context.Background()
		handler := newTestAuthHandler(db)
		if err := handler.OwnerManager.CreateOwner(ctx, testPassword, false); err != nil {
			return err
		}
		old, _, _ := login(t, handler, testPassword)
		csrf := bootstrap(t, handler)
		request := loginRequest(testPassword, csrf)
		request.AddCookie(old)
		recorder := httptest.NewRecorder()
		handler.ServeLogin(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("rotating login = %d", recorder.Code)
		}
		fresh := cookie(t, recorder.Result(), SessionCookie)
		if fresh.Value == old.Value {
			t.Fatal("login reused existing session token")
		}
		if valid, err := handler.Sessions.Validate(ctx, old.Value); err != nil || valid {
			t.Fatalf("old token valid=%t err=%v", valid, err)
		}
		if valid, err := handler.Sessions.Validate(ctx, fresh.Value); err != nil || !valid {
			t.Fatalf("fresh token valid=%t err=%v", valid, err)
		}

		protected := handler.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpapi.WriteOK(w, map[string]bool{"ok": true}) }))
		unauthorized := httptest.NewRecorder()
		protected.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/private", nil))
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("private status = %d", unauthorized.Code)
		}
		var apiError httpapi.APIError
		if err := json.NewDecoder(unauthorized.Body).Decode(&apiError); err != nil || apiError.Code != httpapi.ErrCodeAuth {
			t.Fatalf("private API error = %+v err=%v", apiError, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

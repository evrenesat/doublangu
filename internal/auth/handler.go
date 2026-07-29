package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"doublangu/internal/httpapi"
	"doublangu/internal/store"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "doublangu_session"

// OwnerManager manages the single-owner record in the database.
type OwnerManager struct {
	db               *store.DB
	afterOwnerUpdate func(*sql.Tx) error // test seam for transactional rollback proof
}

// NewOwnerManager returns an OwnerManager backed by the database.
func NewOwnerManager(db *store.DB) *OwnerManager {
	return &OwnerManager{db: db}
}

// OwnerExists reports whether an owner record has been created.
func (m *OwnerManager) OwnerExists(ctx context.Context) (bool, error) {
	var count int
	err := m.db.QueryRow(ctx, "SELECT COUNT(*) FROM owner").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check owner exists: %w", err)
	}
	return count > 0, nil
}

// CreateOwner creates the single owner. With force it atomically replaces the
// password and revokes all sessions, so no session survives a password reset.
func (m *OwnerManager) CreateOwner(ctx context.Context, password string, force bool) error {
	if password == "" {
		return errors.New("password must not be empty")
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	exists, err := m.OwnerExists(ctx)
	if err != nil {
		return err
	}
	if exists && !force {
		return errors.New("owner already exists; use --reset-owner to overwrite")
	}

	now := store.NowUTC()
	if !exists {
		_, err = m.db.Exec(ctx,
			"INSERT INTO owner (id, password_hash, created_at, updated_at) VALUES (1, ?, ?, ?)",
			hash, now, now,
		)
		if err != nil {
			return fmt.Errorf("create owner: %w", err)
		}
		return nil
	}

	return m.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"UPDATE owner SET password_hash = ?, updated_at = ? WHERE id = 1", hash, now,
		); err != nil {
			return fmt.Errorf("reset owner: %w", err)
		}
		if m.afterOwnerUpdate != nil {
			if err := m.afterOwnerUpdate(tx); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM session"); err != nil {
			return fmt.Errorf("revoke owner sessions: %w", err)
		}
		return nil
	})
}

// VerifyOwnerPassword checks the stored password hash for the single owner.
func (m *OwnerManager) VerifyOwnerPassword(ctx context.Context, password string) error {
	var hash string
	err := m.db.QueryRow(ctx, "SELECT password_hash FROM owner WHERE id = 1").Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("no owner configured; create an owner first")
	}
	if err != nil {
		return fmt.Errorf("verify owner: %w", err)
	}
	return VerifyPassword(hash, password)
}

// AuthHandler provides HTTP handlers for login, logout, and session validation.
type AuthHandler struct {
	Sessions      *SessionStore
	OwnerManager  *OwnerManager
	CSRF          *CSRF
	RateLimiter   *RateLimiter
	SessionMaxAge time.Duration
	Secure        bool
}

// LoginRequest is the JSON body for POST /api/v1/auth/login.
type LoginRequest struct {
	Password string `json:"password"`
}

// LoginResponse is returned on successful authentication.
type LoginResponse struct {
	OK bool `json:"ok"`
}

// CSRFResponse confirms that the double-submit cookie was issued. The token is
// deliberately read from the same-origin non-HttpOnly cookie by the browser.
type CSRFResponse struct {
	OK bool `json:"ok"`
}

// ServeCSRF issues a CSRF cookie before authentication.
func (h *AuthHandler) ServeCSRF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", httpapi.ErrCodeMethodNotAllow)
		return
	}
	token, err := h.CSRF.GenerateToken()
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "csrf bootstrap failed", httpapi.ErrCodeInternal)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.CSRF.SetCookie(w, token, h.Secure)
	httpapi.WriteOK(w, CSRFResponse{OK: true})
}

// ServeLogin handles POST /api/v1/auth/login.
func (h *AuthHandler) ServeLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", httpapi.ErrCodeMethodNotAllow)
		return
	}
	if err := h.CSRF.VerifyRequest(r); err != nil {
		httpapi.WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", httpapi.ErrCodeCSRF)
		return
	}
	if !h.RateLimiter.AllowRequest(r) {
		httpapi.WriteError(w, http.StatusTooManyRequests, "rate limited", httpapi.ErrCodeRateLimit)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Password == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body", httpapi.ErrCodeValidation)
		return
	}
	if err := h.OwnerManager.VerifyOwnerPassword(r.Context(), req.Password); err != nil {
		httpapi.WriteError(w, http.StatusUnauthorized, "invalid password", httpapi.ErrCodeAuth)
		return
	}
	csrfToken, err := h.CSRF.GenerateToken()
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "csrf refresh failed", httpapi.ErrCodeInternal)
		return
	}

	presented := ""
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		presented = cookie.Value
	}
	token, err := h.Sessions.Rotate(r.Context(), presented, h.SessionMaxAge, r.UserAgent())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "session creation failed", httpapi.ErrCodeInternal)
		return
	}
	h.setSessionCookie(w, token)
	h.CSRF.SetCookie(w, csrfToken, h.Secure)
	httpapi.WriteOK(w, LoginResponse{OK: true})
}

// ServeLogout handles POST /api/v1/auth/logout. Invalid CSRF requests do not
// alter either persisted session state or client cookies.
func (h *AuthHandler) ServeLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", httpapi.ErrCodeMethodNotAllow)
		return
	}
	if err := h.CSRF.VerifyRequest(r); err != nil {
		httpapi.WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", httpapi.ErrCodeCSRF)
		return
	}
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		if err := h.Sessions.Delete(r.Context(), cookie.Value); err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "session deletion failed", httpapi.ErrCodeInternal)
			return
		}
	}
	h.clearSessionCookie(w)
	httpapi.WriteOK(w, LoginResponse{OK: true})
}

// ServeSessionCheck handles GET /api/v1/auth/session — returns whether the
// current session is valid.
func (h *AuthHandler) ServeSessionCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", httpapi.ErrCodeMethodNotAllow)
		return
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		httpapi.WriteOK(w, map[string]bool{"authenticated": false})
		return
	}
	valid, err := h.Sessions.Validate(r.Context(), cookie.Value)
	if err != nil || !valid {
		httpapi.WriteOK(w, map[string]bool{"authenticated": false})
		return
	}
	httpapi.WriteOK(w, map[string]bool{"authenticated": true})
}

// RequireAuth is middleware that returns a structured 401 for unauthenticated requests.
func (h *AuthHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			httpapi.WriteError(w, http.StatusUnauthorized, "authentication required", httpapi.ErrCodeAuth)
			return
		}
		valid, err := h.Sessions.Validate(r.Context(), cookie.Value)
		if err != nil || !valid {
			httpapi.WriteError(w, http.StatusUnauthorized, "authentication required", httpapi.ErrCodeAuth)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AuthorizeFunc returns an authorization function compatible with the
// pluginassets handler signature. Asset responses retain their CP7 non-JSON
// content contract, so this deliberately returns only a boolean.
func (h *AuthHandler) AuthorizeFunc() func(*http.Request) bool {
	return func(r *http.Request) bool {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			return false
		}
		valid, err := h.Sessions.Validate(r.Context(), cookie.Value)
		return err == nil && valid
	}
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", MaxAge: int(h.SessionMaxAge.Seconds()),
		HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteLaxMode,
	})
}

// SessionCookieName reports the cookie name used for sessions.
func SessionCookieName() string { return SessionCookie }

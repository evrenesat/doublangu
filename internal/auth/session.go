package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"doublangu/internal/store"
)

// SessionTokenBytes is the number of random bytes in a session token.
const SessionTokenBytes = 32

// SessionStore manages session persistence in SQLite.
type SessionStore struct {
	db *store.DB
}

// NewSessionStore returns a SessionStore backed by the provided database.
func NewSessionStore(db *store.DB) *SessionStore {
	return &SessionStore{db: db}
}

// Create generates a new random session token, stores it with the given expiry
// and user agent, and returns the base64url-encoded token.
func (s *SessionStore) Create(ctx context.Context, maxAge time.Duration, userAgent string) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	if err := s.create(ctx, token, maxAge, userAgent); err != nil {
		return "", err
	}
	return token, nil
}

func (s *SessionStore) create(ctx context.Context, token string, maxAge time.Duration, userAgent string) error {
	expiresAt := time.Now().UTC().Add(maxAge).Format("2006-01-02T15:04:05.000Z")
	_, err := s.db.Exec(ctx,
		"INSERT INTO session (token, expires_at, user_agent) VALUES (?, ?, ?)",
		token, expiresAt, userAgent,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// Rotate atomically replaces a currently valid presented token with a fresh
// token. Missing or expired tokens receive a normal new session instead.
func (s *SessionStore) Rotate(ctx context.Context, presented string, maxAge time.Duration, userAgent string) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().UTC().Add(maxAge).Format("2006-01-02T15:04:05.000Z")
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if presented != "" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM session WHERE token = ? AND expires_at <= ?", presented, now); err != nil {
				return fmt.Errorf("remove expired presented session: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM session WHERE token = ?", presented); err != nil {
				return fmt.Errorf("rotate presented session: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO session (token, expires_at, user_agent) VALUES (?, ?, ?)", token, expiresAt, userAgent,
		); err != nil {
			return fmt.Errorf("create replacement session: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("rotate session: %w", err)
	}
	return token, nil
}

// Validate checks whether the token exists and has not expired. It deletes
// the session on expiry.
func (s *SessionStore) Validate(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var expiresAt string
	err := s.db.QueryRow(ctx,
		"SELECT expires_at FROM session WHERE token = ?", token,
	).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("validate session: %w", err)
	}
	expiry, err := time.Parse("2006-01-02T15:04:05.000Z", expiresAt)
	if err != nil {
		return false, fmt.Errorf("parse session expiry: %w", err)
	}
	if time.Now().UTC().After(expiry) {
		_, _ = s.db.Exec(ctx, "DELETE FROM session WHERE token = ?", token)
		return false, nil
	}
	return true, nil
}

// Delete removes a session token from the database.
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM session WHERE token = ?", token)
	return err
}

// DeleteAll revokes every persisted session. It is used only while resetting
// the single owner password in the same transaction as that password change.
func (s *SessionStore) DeleteAll(ctx context.Context) error {
	_, err := s.db.Exec(ctx, "DELETE FROM session")
	return err
}

// CleanupExpired removes all expired sessions from the database.
func (s *SessionStore) CleanupExpired(ctx context.Context) (int64, error) {
	result, err := s.db.Exec(ctx,
		"DELETE FROM session WHERE expires_at < ?",
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

func generateToken() (string, error) {
	bytes := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

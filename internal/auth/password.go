package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the bcrypt cost parameter. 12 is a reasonable default for
// single-user self-hosted authentication.
const BcryptCost = 12

// HashPassword returns a bcrypt hash of the plaintext password.
// Empty passwords are rejected.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword compares a plaintext password against a bcrypt hash.
// It uses constant-time comparison and returns nil on match.
func VerifyPassword(hash, password string) error {
	if password == "" {
		return errors.New("password must not be empty")
	}
	if hash == "" {
		return errors.New("hash must not be empty")
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		// Return a generic error to avoid leaking hash details.
		return errors.New("invalid password")
	}
	return nil
}

// SecureCompare performs a constant-time comparison of two strings.
func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

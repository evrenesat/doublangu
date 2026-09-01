// Package library defines the core record contracts for Doublangu's
// language-learning library system. Validated record identities use canonical
// uppercase 26-character ULIDs, language fields use BCP-47 tags, and
// chapter/source timing uses integer milliseconds.
package library

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// ULID is a record identity in its encoded form. Keeping the encoded form lets
// validation reject malformed values received through public DTO literals or
// JSON before they reach persistence.
type ULID string

const zeroULID = "00000000000000000000000000"

// newULID generates a fresh ULID using cryptographically random entropy and
// the current time. It deliberately provides no cross-call ordering promise.
// This is the only production ULID creation boundary.
func newULID() ULID {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	return ULID(id.String())
}

// NewULID generates a canonical record identity for packages that own a
// separate domain model but share the library identity contract.
func NewULID() ULID { return newULID() }

// ParseULID parses a 26-character uppercase ULID string into canonical form.
// It rejects inputs that are not exactly 26 uppercase Crockford base32
// characters (the alphabet excludes I, L, O, U).
func ParseULID(s string) (ULID, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed != s {
		return "", fmt.Errorf("ulid: whitespace-padded input")
	}
	if len(s) != ulid.EncodedSize {
		return "", fmt.Errorf("ulid: expected %d characters, got %d", ulid.EncodedSize, len(s))
	}
	// ParseStrict validates characters against the Crockford base32 alphabet
	// but is case-insensitive. Reject lowercase input explicitly.
	if s != strings.ToUpper(s) {
		return "", fmt.Errorf("ulid: must be uppercase")
	}
	id, err := ulid.ParseStrict(s)
	if err != nil {
		return "", fmt.Errorf("ulid: parse: %w", err)
	}
	return ULID(id.String()), nil
}

// String returns the encoded identity. The empty value is represented as the
// canonical zero ULID for diagnostics.
func (u ULID) String() string {
	if u == "" {
		return zeroULID
	}
	return string(u)
}

// IsZero reports whether u is the zero ULID.
func (u ULID) IsZero() bool {
	return u == "" || u == zeroULID
}

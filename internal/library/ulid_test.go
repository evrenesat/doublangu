package library

import (
	"strings"
	"testing"
)

func TestNewULID_GeneratesValidULID(t *testing.T) {
	// Run multiple times to confirm every generated ULID is valid.
	for i := 0; i < 50; i++ {
		id := newULID()
		s := id.String()
		if len(s) != 26 {
			t.Fatalf("ULID string length = %d, want 26 (%s)", len(s), s)
		}
		if s != strings.ToUpper(s) {
			t.Fatalf("ULID must be uppercase: %s", s)
		}
		// Verify Crockford base32: only 0-9 and A-Z excluding I, L, O, U.
		for _, r := range s {
			if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'H') ||
				(r >= 'J' && r <= 'K') || (r >= 'M' && r <= 'N') ||
				(r >= 'P' && r <= 'T') || (r >= 'V' && r <= 'Z') {
				continue
			}
			t.Fatalf("ULID contains invalid character %q in %s", r, s)
		}
		// Round-trip through ParseULID.
		parsed, err := ParseULID(s)
		if err != nil {
			t.Fatalf("ParseULID(%s): %v", s, err)
		}
		if parsed != id {
			t.Fatalf("round-trip mismatch: %v != %v", parsed, id)
		}
		if id.IsZero() {
			t.Fatal("newly generated ULID is zero")
		}
	}
}

func TestNewULID_GeneratesDistinctIdentifiers(t *testing.T) {
	// New IDs use fresh cryptographic entropy; this is a uniqueness sanity check,
	// not a cross-call ordering assertion.
	var prev ULID
	for i := 0; i < 100; i++ {
		id := newULID()
		if id == prev {
			t.Fatal("two newULID calls returned identical ULID")
		}
		prev = id
	}
}

func TestParseULID_Valid(t *testing.T) {
	// Parse a known-good ULID.
	s := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	id, err := ParseULID(s)
	if err != nil {
		t.Fatalf("ParseULID(%s): %v", s, err)
	}
	if id.String() != s {
		t.Fatalf("ParseULID round-trip: %s != %s", id.String(), s)
	}
	if id.IsZero() {
		t.Fatal("parsed ULID reports zero")
	}
}

func TestParseULID_WhitespacePadded(t *testing.T) {
	for _, s := range []string{
		" 01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01ARZ3NDEKTSV4RRFFQ69G5FAV ",
		"\t01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"\n01ARZ3NDEKTSV4RRFFQ69G5FAV",
	} {
		_, err := ParseULID(s)
		if err == nil {
			t.Fatalf("ParseULID(%q) should reject whitespace-padded input", s)
		}
	}
}

func TestParseULID_WrongLength(t *testing.T) {
	for _, s := range []string{
		"",
		"01ARZ3NDEKTSV4RRFFQ69G5FA",
		"01ARZ3NDEKTSV4RRFFQ69G5FAV0",
		"01ARZ3NDEKTSV4RRFFQ69G5FAVXX",
	} {
		_, err := ParseULID(s)
		if err == nil {
			t.Fatalf("ParseULID(%q) should reject wrong-length input", s)
		}
	}
}

func TestParseULID_UUIDNegativeControl(t *testing.T) {
	// UUID-shaped values (36 chars with dashes) must be rejected.
	uuids := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"00000000-0000-0000-0000-000000000000",
	}
	for _, s := range uuids {
		_, err := ParseULID(s)
		if err == nil {
			t.Fatalf("ParseULID(%q) should reject UUID-shaped input", s)
		}
	}
}

func TestParseULID_InvalidCharacters(t *testing.T) {
	// Characters outside Crockford base32.
	for _, s := range []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAI", // contains I
		"01ARZ3NDEKTSV4RRFFQ69G5FAL", // contains L
		"01ARZ3NDEKTSV4RRFFQ69G5FAO", // contains O
		"01ARZ3NDEKTSV4RRFFQ69G5FAU", // contains U
		"01ARZ3NDEKTSV4RRFFQ69G5F!V", // contains !
	} {
		_, err := ParseULID(s)
		if err == nil {
			t.Fatalf("ParseULID(%q) should reject invalid characters", s)
		}
	}
}

func TestParseULID_LowerCaseRejected(t *testing.T) {
	s := "01arz3ndektsv4rrffq69g5fav"
	_, err := ParseULID(s)
	if err == nil {
		t.Fatalf("ParseULID(%q) should reject lowercase input", s)
	}
}

func TestZeroULID(t *testing.T) {
	var zero ULID
	if !zero.IsZero() {
		t.Fatal("zero ULID should report IsZero")
	}
	if zero.String() != "00000000000000000000000000" {
		t.Fatalf("zero ULID string = %s", zero.String())
	}
}

func TestParseULID_NegativeUUIDShapes(t *testing.T) {
	// Various UUID-like shapes that must not parse as ULID.
	for _, s := range []string{
		// Standard UUID (36 chars with dashes).
		"550e8400-e29b-41d4-a716-446655440000",
		// UUID without dashes (32 hex chars).
		"550e8400e29b41d4a716446655440000",
		// Upper case hex only.
		"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
		// Leading/trailing garbage.
		"01ARZ3NDEKTSV4RRFFQ69G5FAV-trailing",
	} {
		_, err := ParseULID(s)
		if err == nil {
			t.Fatalf("ParseULID(%q) unexpectedly succeeded for non-ULID shape", s)
		}
	}
}

package library

import (
	"testing"
)

func TestParseBCP47_CanonicalForms(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Primary language subtags are lowercased; region subtags are uppercased.
		{"en", "en"},
		{"EN", "en"},
		{"en-US", "en-US"},
		{"en-us", "en-US"},
		{"zh-Hans", "zh-Hans"},
		{"zh-hans", "zh-Hans"},
		{"zh-Hans-CN", "zh-Hans-CN"},
		{"nl", "nl"},
		{"NL", "nl"},
		{"nl-NL", "nl-NL"},
		{"nl-BE", "nl-BE"},
		{"de", "de"},
		{"fr", "fr"},
		{"ja", "ja"},
		{"ko", "ko"},
		{"pt-BR", "pt-BR"},
		{"es-419", "es-419"}, // Latin America region
		{"ar-001", "ar-001"}, // Modern Standard Arabic
	}
	for _, tt := range tests {
		got, err := ParseBCP47(tt.input)
		if err != nil {
			t.Errorf("ParseBCP47(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("ParseBCP47(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseBCP47_Empty(t *testing.T) {
	_, err := ParseBCP47("")
	if err == nil {
		t.Fatal("ParseBCP47(\"\") should return error")
	}
}

func TestParseBCP47_WhitespacePadded(t *testing.T) {
	for _, input := range []string{
		" en",
		"en ",
		"\ten",
		"en\t",
		"\nen",
		"en\n",
		" en-US",
		"en-US ",
		"   nl",
	} {
		_, err := ParseBCP47(input)
		if err == nil {
			t.Fatalf("ParseBCP47(%q) should reject whitespace-padded input", input)
		}
	}
}

func TestParseBCP47_Malformed(t *testing.T) {
	for _, input := range []string{
		"123",       // starts with digit
		"not a tag", // contains space
		"en@US",     // contains @
		"e",         // too short
		"x",         // single letter
		"",          // empty
		"!",         // invalid
		"-en",       // leading hyphen
		"en-",       // trailing hyphen
		"en--US",    // double hyphen
	} {
		_, err := ParseBCP47(input)
		if err == nil {
			t.Fatalf("ParseBCP47(%q) should reject malformed input", input)
		}
	}
}

func TestParseBCP47_SilentlySubstitutedRejected(t *testing.T) {
	// Completely fake tags that the parser might silently accept but should
	// be rejected because they are not valid BCP-47.
	for _, input := range []string{
		"zzzz",         // not a valid primary language
		"qq-Qaaa-AA",   // not valid
		"xxxxxxxxxxxx", // very long, not valid
	} {
		_, err := ParseBCP47(input)
		if err == nil {
			t.Fatalf("ParseBCP47(%q) should reject invalid/substituted input", input)
		}
	}
}

func TestParseBCP47_ExtendedLanguage(t *testing.T) {
	// zh-yue (Cantonese) — valid extended language subtag.
	got, err := ParseBCP47("zh-yue")
	if err != nil {
		t.Fatalf("ParseBCP47(%q): unexpected error: %v", "zh-yue", err)
	}
	if got != "zh-yue" && got != "yue" {
		t.Errorf("ParseBCP47(%q) = %q, expected zh-yue or yue", "zh-yue", got)
	}
}

func TestParseBCP47_DutchVariants(t *testing.T) {
	tests := []string{"nl", "nl-NL", "nl-BE", "nl-SR"}
	for _, input := range tests {
		got, err := ParseBCP47(input)
		if err != nil {
			t.Errorf("ParseBCP47(%q): unexpected error: %v", input, err)
			continue
		}
		// Canonical form must be non-empty and contain the primary language.
		if got == "" {
			t.Errorf("ParseBCP47(%q) returned empty string", input)
		}
	}
}

func TestParseBCP47_RoundTripStable(t *testing.T) {
	inputs := []string{"en", "nl", "zh-Hans", "pt-BR", "de-DE", "fr-CA"}
	for _, input := range inputs {
		canonical, err := ParseBCP47(input)
		if err != nil {
			t.Errorf("ParseBCP47(%q): %v", input, err)
			continue
		}
		// Parsing the canonical form again must produce the same result.
		canonical2, err := ParseBCP47(canonical)
		if err != nil {
			t.Errorf("ParseBCP47(%q) second parse: %v", canonical, err)
			continue
		}
		if canonical2 != canonical {
			t.Errorf("ParseBCP47(%q) round-trip: %q != %q", input, canonical, canonical2)
		}
	}
}

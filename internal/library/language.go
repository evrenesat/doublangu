package library

import (
	"fmt"
	"strings"

	"golang.org/x/text/language"
)

// ParseBCP47 validates and canonicalizes a BCP-47 language tag.
// It rejects empty, whitespace-padded, malformed, and silently substituted
// tags. The returned string is the canonical form suitable for storage.
func ParseBCP47(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", fmt.Errorf("bcp47: empty tag")
	}
	if trimmed != input {
		return "", fmt.Errorf("bcp47: whitespace-padded tag %q", input)
	}

	// Syntactic pre-check: a BCP-47 tag must start with an ASCII letter
	// and contain only ASCII letters, digits, and hyphens.
	if len(trimmed) < 2 {
		return "", fmt.Errorf("bcp47: tag too short: %q", trimmed)
	}
	for i, r := range trimmed {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
				return "", fmt.Errorf("bcp47: must start with a letter: %q", trimmed)
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return "", fmt.Errorf("bcp47: invalid character %q in %q", string(r), trimmed)
			}
		}
	}

	tag, err := language.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("bcp47: parse %q: %w", trimmed, err)
	}
	if tag.IsRoot() {
		return "", fmt.Errorf("bcp47: invalid tag %q", trimmed)
	}

	canonical := tag.String()

	// Verify the canonical form is itself valid and stable.
	roundtrip, _ := language.Parse(canonical)
	if roundtrip.String() != canonical {
		return "", fmt.Errorf("bcp47: canonical form %q of %q is not stable", canonical, trimmed)
	}

	return canonical, nil
}

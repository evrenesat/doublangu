package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"doublangu/internal/library"
)

// PromptType names one fixed instruction type. The set is closed: owners
// choose versions of these types, never new types.
type PromptType string

const (
	TypeLinguisticAnalysis  PromptType = "linguistic_analysis"
	TypeArticleTranslation  PromptType = "article_translation"
	TypeExplore             PromptType = "explore"
	TypeSentenceTranslation PromptType = "sentence_translation"
	TypeCorrection          PromptType = "correction"
)

// Types is the fixed prompt-type order used for seeding and validation.
var Types = []PromptType{
	TypeLinguisticAnalysis,
	TypeArticleTranslation,
	TypeExplore,
	TypeSentenceTranslation,
	TypeCorrection,
}

// Valid reports whether promptType is one of the five registered types.
func (t PromptType) Valid() bool {
	for _, candidate := range Types {
		if t == candidate {
			return true
		}
	}
	return false
}

// Label and instruction bounds mirror the owner-facing contract: trimmed
// labels of at most 80 Unicode scalars and nonblank valid UTF-8 instructions
// of at most 64 KiB.
const (
	MaxLabelScalars     = 80
	MaxInstructionBytes = 64 * 1024
)

// Version is one immutable stored prompt version. Rows are never updated or
// deleted; a new save always allocates the next version of the type.
type Version struct {
	ID              string     `json:"id"`
	PromptType      PromptType `json:"prompt_type"`
	Version         int        `json:"version"`
	Label           string     `json:"label"`
	InstructionText string     `json:"instruction_text"`
	ContentHash     string     `json:"content_hash"`
	CreatedAt       string     `json:"created_at"`
}

// NormalizeInstruction applies the only sanctioned input normalization:
// CRLF and bare CR become LF. Every other byte is preserved verbatim.
func NormalizeInstruction(text string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\r", "\n")
}

// ContentHashOf returns the SHA-256 over the exact UTF-8 bytes of the
// already-normalized instruction text.
func ContentHashOf(instruction string) string {
	sum := sha256.Sum256([]byte(instruction))
	return hex.EncodeToString(sum[:])
}

// ValidateInstruction checks the stored-shape rules for one instruction text
// (already CRLF-normalized by the caller): valid UTF-8, nonblank, and within
// the 64 KiB byte bound.
func ValidateInstruction(instruction string) error {
	if !utf8.ValidString(instruction) {
		return errors.New("instruction text must be valid UTF-8")
	}
	if strings.TrimSpace(instruction) == "" {
		return errors.New("instruction text must not be blank")
	}
	if len(instruction) > MaxInstructionBytes {
		return fmt.Errorf("instruction text must be at most %d bytes", MaxInstructionBytes)
	}
	return nil
}

// ValidateLabel checks the optional label: trimmed, at most 80 Unicode
// scalar values.
func ValidateLabel(label string) (string, error) {
	trimmed := strings.TrimSpace(label)
	if !utf8.ValidString(trimmed) {
		return "", errors.New("label must be valid UTF-8")
	}
	if utf8.RuneCountInString(trimmed) > MaxLabelScalars {
		return "", fmt.Errorf("label must be at most %d characters", MaxLabelScalars)
	}
	return trimmed, nil
}

// NewVersionID returns a fresh ULID for a stored prompt version.
func NewVersionID() string { return library.NewULID().String() }

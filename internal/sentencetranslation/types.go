// Package sentencetranslation owns the durable sentence translation record:
// one saved translation per source sentence in the article's target language.
// It may depend on library and store only; generation workers (checkpoint 9)
// and HTTP handlers (checkpoint 10) live in later checkpoints and import this
// package. Article semantic state is never mutated here.
package sentencetranslation

import (
	"doublangu/internal/library"
)

// Entry is one persisted sentence translation row. TranslationText is nil
// until the first successful generation; result metadata uses empty defaults
// when absent; job and run pointers stay nullable.
type Entry struct {
	SentenceID     library.ULID
	SourceHash     string
	TargetLanguage string
	// TranslationText is the last successful translation, nil when none.
	TranslationText *string
	ResultHash      string
	ProvenanceJSON  string
	LastJobID       *string
	LastRunID       *string
	UpdatedAt       string
}

// Ready reports whether a successful translation is saved.
func (e *Entry) Ready() bool { return e != nil && e.TranslationText != nil }

// SaveParams carries one translation write. SentenceID and SourceHash must
// still match the stored article_sentence anchor; TargetLanguage must match
// the owning article's target language, which the article owns.
type SaveParams struct {
	SentenceID     library.ULID
	SourceHash     string
	TargetLanguage string
	// TranslationText is the accepted translation to persist.
	TranslationText string
	ResultHash      string
	ProvenanceJSON  string
	LastJobID       *string
	LastRunID       *string
}

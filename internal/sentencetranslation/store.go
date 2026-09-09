package sentencetranslation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

// ErrNotFound marks a missing translation row.
var ErrNotFound = errors.New("sentencetranslation: not found")

// ErrStaleAnchor marks a write whose sentence anchor no longer matches the
// stored article_sentence row: the sentence was deleted or recreated with a
// different source hash. Stale results are never remapped to another sentence.
var ErrStaleAnchor = errors.New("sentencetranslation: sentence anchor changed")

// Store reads and writes sentence translation rows and verifies every write
// against the current source anchor.
type Store struct {
	db *store.DB
}

func NewStore(db *store.DB) *Store { return &Store{db: db} }

// Get returns the saved translation for one sentence, or ErrNotFound when the
// sentence has no saved row yet. Existing articles start missing.
func (s *Store) Get(ctx context.Context, sentenceID library.ULID) (*Entry, error) {
	entry := &Entry{SentenceID: sentenceID}
	var translation sql.NullString
	var lastJobID, lastRunID sql.NullString
	err := s.db.QueryRow(ctx, `
		SELECT source_hash, target_language, translation_text, result_hash,
			provenance_json, last_job_id, last_run_id, updated_at
		FROM sentence_translation WHERE sentence_id = ?
	`, sentenceID.String()).Scan(
		&entry.SourceHash, &entry.TargetLanguage, &translation,
		&entry.ResultHash, &entry.ProvenanceJSON, &lastJobID, &lastRunID,
		&entry.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sentencetranslation: get: %w", err)
	}
	if translation.Valid {
		value := translation.String
		entry.TranslationText = &value
	}
	if lastJobID.Valid {
		value := lastJobID.String
		entry.LastJobID = &value
	}
	if lastRunID.Valid {
		value := lastRunID.String
		entry.LastRunID = &value
	}
	return entry, nil
}

// Save persists one accepted translation after verifying the sentence still
// exists with the captured source hash and the article's target language.
// A deleted sentence, a recreated anchor with a different source hash, or a
// target language the article does not own all fail without storing anything.
func (s *Store) Save(ctx context.Context, params SaveParams) (*Entry, error) {
	if params.SentenceID.IsZero() {
		return nil, fmt.Errorf("sentencetranslation: save: %w", errors.New("sentence id is required"))
	}
	if strings.TrimSpace(params.SourceHash) == "" {
		return nil, errors.New("sentencetranslation: save: source hash is required")
	}
	if strings.TrimSpace(params.TranslationText) == "" {
		return nil, errors.New("sentencetranslation: save: translation text is required")
	}
	var storedHash, articleTarget string
	err := s.db.QueryRow(ctx, `
		SELECT s.source_hash, a.target_language
		FROM article_sentence s
		JOIN article_block b ON b.id = s.article_block_id
		JOIN article a ON a.id = b.article_id
		WHERE s.id = ?
	`, params.SentenceID.String()).Scan(&storedHash, &articleTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStaleAnchor
	}
	if err != nil {
		return nil, fmt.Errorf("sentencetranslation: verify anchor: %w", err)
	}
	if storedHash != params.SourceHash {
		return nil, ErrStaleAnchor
	}
	if params.TargetLanguage != articleTarget {
		return nil, fmt.Errorf("sentencetranslation: save: target language %q is not owned by the article (%q)", params.TargetLanguage, articleTarget)
	}
	var lastJobID, lastRunID sql.NullString
	if params.LastJobID != nil {
		lastJobID = sql.NullString{String: *params.LastJobID, Valid: true}
	}
	if params.LastRunID != nil {
		lastRunID = sql.NullString{String: *params.LastRunID, Valid: true}
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO sentence_translation
			(sentence_id, source_hash, target_language, translation_text,
				result_hash, provenance_json, last_job_id, last_run_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(sentence_id) DO UPDATE SET
			source_hash = excluded.source_hash,
			target_language = excluded.target_language,
			translation_text = excluded.translation_text,
			result_hash = excluded.result_hash,
			provenance_json = excluded.provenance_json,
			last_job_id = excluded.last_job_id,
			last_run_id = excluded.last_run_id,
			updated_at = excluded.updated_at
	`, params.SentenceID.String(), params.SourceHash, params.TargetLanguage,
		params.TranslationText, params.ResultHash, params.ProvenanceJSON,
		lastJobID, lastRunID); err != nil {
		return nil, fmt.Errorf("sentencetranslation: save: %w", err)
	}
	return s.Get(ctx, params.SentenceID)
}

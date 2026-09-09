// Package dictionary owns the durable reader dictionary: subject resolution
// from article references, the reusable dictionary_entry record, and the
// server worker for reader.dictionary.v1 jobs. It may depend on semantics,
// annotator, jobs, library, and store; no other domain package may depend on
// this orchestration package. Article semantic state is never mutated here.
package dictionary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// Subject is the server-resolved explore subject for one article reference.
type Subject struct {
	SourceLanguage string
	TargetLanguage string
	LookupKind     string // semantics.DictionaryLookupWord or ...Expression
	LookupForm     string
	NormalizedForm string
}

// Entry is one persisted dictionary record. Ready is derived from an accepted
// document; otherwise status derives from the joined last job.
type Entry struct {
	ID               library.ULID
	SourceLanguage   string
	TargetLanguage   string
	LookupKind       string
	LookupForm       string
	NormalizedForm   string
	DocumentJSON     *string
	DocumentHash     *string
	ContractVersion  *string
	PromptVersion    *string
	ProvenanceJSON   *string
	LastJobID        *string
	LastJobState     string
	LastJobErrorCode string
	CreatedAt        string
	UpdatedAt        string
}

// Status values for the reader API.
const (
	StatusMissing    = "missing"
	StatusQueued     = "queued"
	StatusGenerating = "generating"
	StatusReady      = "ready"
	StatusFailed     = "failed"
)

// Status derives the visible dictionary status from the entry row and its
// joined last job. A nil entry is missing.
func (e *Entry) Status() string {
	if e == nil {
		return StatusMissing
	}
	if e.DocumentJSON != nil {
		return StatusReady
	}
	switch e.LastJobState {
	case jobs.StateQueued:
		return StatusQueued
	case jobs.StateLeased, jobs.StateRunning:
		return StatusGenerating
	case jobs.StateFailed, jobs.StateCanceled:
		return StatusFailed
	default:
		return StatusMissing
	}
}

// Ready reports whether an accepted document is saved.
func (e *Entry) Ready() bool { return e != nil && e.DocumentJSON != nil }

var ErrNotFound = errors.New("dictionary: not found")

// Store reads and writes dictionary entries and resolves explore subjects.
type Store struct {
	db        *store.DB
	semantics *semantics.Store
}

func NewStore(db *store.DB) *Store {
	return &Store{db: db, semantics: semantics.NewStore(db)}
}

// ResolveArticleOccurrence verifies the occurrence belongs to the article and
// derives its dictionary subject from stored source and semantic data.
func (s *Store) ResolveArticleOccurrence(ctx context.Context, articleID library.ULID, occurrenceID library.ULID) (*Subject, error) {
	var sourceLanguage, targetLanguage, kind, role string
	var senseID sql.NullString
	err := s.db.QueryRow(ctx, `
		SELECT a.source_language, a.target_language, o.kind, o.role, o.semantic_sense_id
		FROM article_occurrence o
		JOIN article_block b ON b.id = o.article_block_id
		JOIN article a ON a.id = b.article_id
		WHERE o.id = ? AND a.id = ?
	`, occurrenceID.String(), articleID.String()).Scan(&sourceLanguage, &targetLanguage, &kind, &role, &senseID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dictionary: resolve occurrence: %w", err)
	}
	subject := Subject{SourceLanguage: sourceLanguage, TargetLanguage: targetLanguage}
	if role == "token" {
		subject.LookupKind = semantics.DictionaryLookupWord
	} else {
		subject.LookupKind = semantics.DictionaryLookupExpression
	}
	// Stored semantic identity wins; exact source text is the fallback.
	identity := ""
	if senseID.Valid && senseID.String != "" {
		sense, senseErr := s.semantics.GetSense(ctx, library.ULID(senseID.String))
		if senseErr == nil {
			if subject.LookupKind == semantics.DictionaryLookupExpression {
				identity = sense.CanonicalForm
			} else if strings.TrimSpace(sense.Lemma) != "" {
				identity = sense.Lemma
			} else {
				identity = sense.CanonicalForm
			}
		} else if !errors.Is(senseErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("dictionary: load sense: %w", senseErr)
		}
	}
	if strings.TrimSpace(identity) == "" {
		identity, err = s.occurrenceSourceText(ctx, occurrenceID)
		if err != nil {
			return nil, err
		}
	}
	return s.finishSubject(ctx, subject, identity)
}

// ResolveArticleAnnotation resolves a legacy annotation-only reference. The
// annotation's arbitrary learning key is never treated as a lemma.
func (s *Store) ResolveArticleAnnotation(ctx context.Context, articleID library.ULID, annotationID library.ULID) (*Subject, error) {
	var sourceLanguage, targetLanguage, kind, sourceText string
	err := s.db.QueryRow(ctx, `
		SELECT a.source_language, a.target_language, n.kind, n.source_text
		FROM article_annotation n
		JOIN article_block b ON b.id = n.article_block_id
		JOIN article a ON a.id = b.article_id
		WHERE n.id = ? AND a.id = ?
	`, annotationID.String(), articleID.String()).Scan(&sourceLanguage, &targetLanguage, &kind, &sourceText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dictionary: resolve annotation: %w", err)
	}
	subject := Subject{SourceLanguage: sourceLanguage, TargetLanguage: targetLanguage}
	if kind == "word" {
		subject.LookupKind = semantics.DictionaryLookupWord
	} else {
		subject.LookupKind = semantics.DictionaryLookupExpression
	}
	return s.finishSubject(ctx, subject, sourceText)
}

// occurrenceSourceText joins the occurrence's ordered source spans.
func (s *Store) occurrenceSourceText(ctx context.Context, occurrenceID library.ULID) (string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT source_text FROM article_occurrence_span
		WHERE article_occurrence_id = ? ORDER BY span_index
	`, occurrenceID.String())
	if err != nil {
		return "", fmt.Errorf("dictionary: load spans: %w", err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return "", fmt.Errorf("dictionary: scan span: %w", err)
		}
		parts = append(parts, text)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("dictionary: spans: %w", err)
	}
	return strings.Join(parts, " "), nil
}

// finishSubject validates the language pair, normalizes the lookup form with
// the shared semantics normalization, and rejects nonlexical subjects.
func (s *Store) finishSubject(ctx context.Context, subject Subject, identity string) (*Subject, error) {
	_ = ctx
	source, err := library.ParseBCP47(subject.SourceLanguage)
	if err != nil {
		return nil, fmt.Errorf("dictionary: source language: %w", err)
	}
	target, err := library.ParseBCP47(subject.TargetLanguage)
	if err != nil {
		return nil, fmt.Errorf("dictionary: target language: %w", err)
	}
	if languageBase(source) != "nl" || languageBase(target) != "en" {
		return nil, ErrNotFound
	}
	subject.SourceLanguage, subject.TargetLanguage = source, target
	trimmed := strings.TrimSpace(identity)
	if trimmed == "" {
		return nil, ErrNotFound
	}
	normalized, err := semantics.NormalizeForm(trimmed)
	if err != nil {
		return nil, fmt.Errorf("dictionary: lookup form: %w", err)
	}
	if len([]rune(normalized)) > semantics.MaxDictionaryLookupScalars {
		return nil, ErrNotFound
	}
	// Numbers and punctuation-only spans are not lexical words.
	if !strings.ContainsFunc(normalized, unicode.IsLetter) {
		return nil, ErrNotFound
	}
	subject.LookupForm = trimmed
	subject.NormalizedForm = normalized
	return &subject, nil
}

func languageBase(tag string) string {
	if index := strings.Index(tag, "-"); index >= 0 {
		return strings.ToLower(tag[:index])
	}
	return strings.ToLower(tag)
}

const entryColumns = `id, source_language, target_language, lookup_kind, lookup_form,
	normalized_lookup_form, document_json, document_hash, contract_version,
	prompt_version, provenance_json, last_job_id, created_at, updated_at`

func scanEntry(row interface{ Scan(...any) error }) (*Entry, error) {
	var entry Entry
	var id string
	err := row.Scan(&id, &entry.SourceLanguage, &entry.TargetLanguage, &entry.LookupKind,
		&entry.LookupForm, &entry.NormalizedForm, &entry.DocumentJSON, &entry.DocumentHash,
		&entry.ContractVersion, &entry.PromptVersion, &entry.ProvenanceJSON, &entry.LastJobID,
		&entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dictionary: scan entry: %w", err)
	}
	entry.ID = library.ULID(id)
	return &entry, nil
}

// GetEntry loads one dictionary entry with its joined last-job status.
func (s *Store) GetEntry(ctx context.Context, id library.ULID) (*Entry, error) {
	entry, err := scanEntry(s.db.QueryRow(ctx, `SELECT `+entryColumns+` FROM dictionary_entry WHERE id = ?`, id.String()))
	if err != nil {
		return nil, err
	}
	s.attachLastJob(ctx, entry)
	return entry, nil
}

// GetEntryByKey loads the shared entry for a normalized dictionary key.
func (s *Store) GetEntryByKey(ctx context.Context, sourceLanguage, targetLanguage, lookupKind, normalizedForm string) (*Entry, error) {
	entry, err := scanEntry(s.db.QueryRow(ctx, `SELECT `+entryColumns+` FROM dictionary_entry
		WHERE source_language = ? AND target_language = ? AND lookup_kind = ? AND normalized_lookup_form = ?`,
		sourceLanguage, targetLanguage, lookupKind, normalizedForm))
	if err != nil {
		return nil, err
	}
	s.attachLastJob(ctx, entry)
	return entry, nil
}

func (s *Store) attachLastJob(ctx context.Context, entry *Entry) {
	if entry == nil || entry.LastJobID == nil || *entry.LastJobID == "" {
		return
	}
	var state, errorCode string
	err := s.db.QueryRow(ctx, `SELECT state, error_code FROM job WHERE id = ?`, *entry.LastJobID).Scan(&state, &errorCode)
	if err != nil {
		// The job row is gone (SET NULL keeps the reference only while the
		// job exists; a deleted job leaves this null). Treat as missing.
		entry.LastJobID = nil
		return
	}
	entry.LastJobState, entry.LastJobErrorCode = state, errorCode
}

// EnsureEntryTx returns the shared entry for the key, inserting a placeholder
// when absent, inside the caller's transaction. The database unique key is the
// authority for concurrent clicks.
func EnsureEntryTx(ctx context.Context, tx *sql.Tx, subject *Subject) (*Entry, error) {
	now := store.NowUTC()
	id := library.NewULID().String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_language, target_language, lookup_kind, normalized_lookup_form) DO NOTHING
	`, id, subject.SourceLanguage, subject.TargetLanguage, subject.LookupKind, subject.LookupForm, subject.NormalizedForm, now, now); err != nil {
		return nil, fmt.Errorf("dictionary: insert entry: %w", err)
	}
	return scanEntry(tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM dictionary_entry
		WHERE source_language = ? AND target_language = ? AND lookup_kind = ? AND normalized_lookup_form = ?`,
		subject.SourceLanguage, subject.TargetLanguage, subject.LookupKind, subject.NormalizedForm))
}

// EntryTx loads an entry inside a transaction.
func EntryTx(ctx context.Context, tx *sql.Tx, id library.ULID) (*Entry, error) {
	return scanEntry(tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM dictionary_entry WHERE id = ?`, id.String()))
}

// SetLastJobTx points the entry at a new generation job inside the caller's
// transaction. It fails when another writer moved the entry meanwhile.
func SetLastJobTx(ctx context.Context, tx *sql.Tx, entryID library.ULID, previousJobID, jobID string) error {
	query := `UPDATE dictionary_entry SET last_job_id = ?, updated_at = ?
		WHERE id = ? AND last_job_id IS NULL`
	args := []any{jobID, store.NowUTC(), entryID.String()}
	if previousJobID != "" {
		query = `UPDATE dictionary_entry SET last_job_id = ?, updated_at = ?
			WHERE id = ? AND last_job_id = ?`
		args = append(args, previousJobID)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("dictionary: set last job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("dictionary: entry moved concurrently")
	}
	return nil
}

// PublishTx stores an accepted document together with the job acknowledgement
// inside the caller's transaction. The caller checks the job lease; this
// function additionally requires the entry to still point at the publishing
// job so a stale or canceled worker cannot publish.
func PublishTx(ctx context.Context, tx *sql.Tx, entryID library.ULID, jobID string, documentJSON, documentHash, contractVersion, promptVersion, provenanceJSON string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE dictionary_entry
		SET document_json = ?, document_hash = ?, contract_version = ?, prompt_version = ?,
		    provenance_json = ?, updated_at = ?
		WHERE id = ? AND last_job_id = ?
	`, documentJSON, documentHash, contractVersion, promptVersion, provenanceJSON, store.NowUTC(), entryID.String(), jobID)
	if err != nil {
		return fmt.Errorf("dictionary: publish: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("dictionary: entry no longer points at the publishing job")
	}
	return nil
}

// SetLastRunTx points the entry's preflight and generation evidence at one
// analysis run. Callers invoke it right after creating the run so concurrent
// requests observe the in-flight attempt even before last_job_id exists.
func SetLastRunTx(ctx context.Context, tx *sql.Tx, entryID library.ULID, runID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE dictionary_entry SET last_run_id = ? WHERE id = ?`, runID, entryID.String())
	return err
}

// SetLastRun is the standalone form of SetLastRunTx.
func (s *Store) SetLastRun(ctx context.Context, entryID library.ULID, runID string) error {
	_, err := s.db.Exec(ctx, `UPDATE dictionary_entry SET last_run_id = ? WHERE id = ?`, runID, entryID.String())
	return err
}

// LastRunID returns the entry's retained run pointer, or empty.
func (s *Store) LastRunID(ctx context.Context, entryID library.ULID) (string, error) {
	var runID sql.NullString
	if err := s.db.QueryRow(ctx, `SELECT last_run_id FROM dictionary_entry WHERE id = ?`, entryID.String()).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if !runID.Valid {
		return "", nil
	}
	return runID.String, nil
}

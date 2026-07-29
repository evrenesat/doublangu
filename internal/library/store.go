// Package library defines the core record contracts for Doublangu's
// language-learning library system. This file implements the transactional
// metadata store — all operations receive a *sql.Tx and never acquire their
// own database connection.
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"modernc.org/sqlite"
)

// Store provides transactional CRUD operations for library metadata records.
// It holds no connection of its own; every method receives a *sql.Tx supplied
// by the caller. This guarantees operations never escape their transaction or
// reacquire the database connection.
type Store struct{}

// Kind classifies a Store error.
type Kind int

const (
	// KindNotFound indicates the requested record does not exist.
	KindNotFound Kind = iota + 1
	// KindValidation indicates a record failed its representation-level
	// validation before persistence.
	KindValidation
	// KindConflict indicates a foreign-key or uniqueness constraint violation.
	KindConflict
)

// Error is a typed store error that classifies failures so callers can
// distinguish not-found, validation, and conflict conditions.
type Error struct {
	Op   string // e.g. "create library", "get work"
	Kind Kind
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("store %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("store %s: %s", e.Op, e.Kind)
}

func (e *Error) Unwrap() error { return e.Err }

// String returns a human-readable name for the error kind.
func (k Kind) String() string {
	switch k {
	case KindNotFound:
		return "not found"
	case KindValidation:
		return "validation"
	case KindConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

func notFoundError(op string, id ULID) error {
	return &Error{Op: op, Kind: KindNotFound, Err: fmt.Errorf("%s not found", id)}
}

func validationError(op string, err error) error {
	return &Error{Op: op, Kind: KindValidation, Err: err}
}

func conflictError(op string, err error) error {
	return &Error{Op: op, Kind: KindConflict, Err: err}
}

// writeError preserves the underlying write failure and marks only SQLite
// constraint errors as conflicts. Cancellation and transaction failures remain
// ordinary wrapped errors so callers can use errors.Is/errors.As on them.
func writeError(op string, err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 { // SQLITE_CONSTRAINT
		return conflictError(op, err)
	}
	return fmt.Errorf("store %s: %w", op, err)
}

func requireAffected(op string, id ULID, result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store %s: rows affected: %w", op, err)
	}
	if n == 0 {
		return notFoundError(op, id)
	}
	return nil
}

// --- Library ---

// CreateLibrary validates the record and inserts it into the library table.
// created_at and updated_at are set by SQLite defaults.
func (s *Store) CreateLibrary(ctx context.Context, tx *sql.Tx, record *Library) error {
	const op = "create library"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO library (id, name, source_language, target_language, description) VALUES (?, ?, ?, ?, ?)`,
		record.ID.String(), record.Name, record.SourceLanguage, record.TargetLanguage, record.Description,
	)
	if err != nil {
		return writeError(op, err)
	}
	return nil
}

// GetLibrary returns the library identified by id, or a KindNotFound error.
func (s *Store) GetLibrary(ctx context.Context, tx *sql.Tx, id ULID) (*Library, error) {
	const op = "get library"
	row := tx.QueryRowContext(ctx,
		`SELECT id, name, source_language, target_language, description, created_at, updated_at FROM library WHERE id = ?`,
		id.String(),
	)
	var record Library
	var rawID string
	if err := row.Scan(&rawID, &record.Name, &record.SourceLanguage, &record.TargetLanguage, &record.Description, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFoundError(op, id)
		}
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	record.ID = ULID(rawID)
	return &record, nil
}

// ListLibraries returns all libraries ordered by created_at, id. Returns an
// empty slice (never nil) when no rows exist.
func (s *Store) ListLibraries(ctx context.Context, tx *sql.Tx) ([]Library, error) {
	const op = "list libraries"
	rows, err := tx.QueryContext(ctx, `SELECT id, name, source_language, target_language, description, created_at, updated_at FROM library ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	defer rows.Close()

	var out []Library
	for rows.Next() {
		var record Library
		var rawID string
		if err := rows.Scan(&rawID, &record.Name, &record.SourceLanguage, &record.TargetLanguage, &record.Description, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store %s: %w", op, err)
		}
		record.ID = ULID(rawID)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	if out == nil {
		out = []Library{}
	}
	return out, nil
}

// UpdateLibrary validates the record and writes changed fields plus
// updated_at = now.
func (s *Store) UpdateLibrary(ctx context.Context, tx *sql.Tx, record *Library) error {
	const op = "update library"
	parsed, err := ParseULID(record.ID.String())
	if err != nil || parsed.IsZero() {
		return validationError(op, fmt.Errorf("invalid id"))
	}
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE library SET name = ?, source_language = ?, target_language = ?, description = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		record.Name, record.SourceLanguage, record.TargetLanguage, record.Description, record.ID.String(),
	)
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, record.ID, result)
}

// DeleteLibrary removes a library by id. Returns KindNotFound if the id does
// not exist.
func (s *Store) DeleteLibrary(ctx context.Context, tx *sql.Tx, id ULID) error {
	const op = "delete library"
	result, err := tx.ExecContext(ctx, `DELETE FROM library WHERE id = ?`, id.String())
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, id, result)
}

// --- Work ---

// CreateWork validates the record and inserts it.
func (s *Store) CreateWork(ctx context.Context, tx *sql.Tx, record *Work) error {
	const op = "create work"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO work (id, library_id, title, author, kind, source_url) VALUES (?, ?, ?, ?, ?, ?)`,
		record.ID.String(), record.LibraryID.String(), record.Title, record.Author, record.Kind, record.SourceURL,
	)
	if err != nil {
		return writeError(op, err)
	}
	return nil
}

// GetWork returns the work identified by id, or a KindNotFound error.
func (s *Store) GetWork(ctx context.Context, tx *sql.Tx, id ULID) (*Work, error) {
	const op = "get work"
	row := tx.QueryRowContext(ctx,
		`SELECT id, library_id, title, author, kind, source_url, created_at, updated_at FROM work WHERE id = ?`,
		id.String(),
	)
	var record Work
	var rawID, rawLibraryID string
	if err := row.Scan(&rawID, &rawLibraryID, &record.Title, &record.Author, &record.Kind, &record.SourceURL, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFoundError(op, id)
		}
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	record.ID = ULID(rawID)
	record.LibraryID = ULID(rawLibraryID)
	return &record, nil
}

// ListWorksByLibrary returns all works for a library ordered by created_at, id.
func (s *Store) ListWorksByLibrary(ctx context.Context, tx *sql.Tx, libraryID ULID) ([]Work, error) {
	const op = "list works"
	rows, err := tx.QueryContext(ctx,
		`SELECT id, library_id, title, author, kind, source_url, created_at, updated_at FROM work WHERE library_id = ? ORDER BY created_at, id`,
		libraryID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	defer rows.Close()

	var out []Work
	for rows.Next() {
		var record Work
		var rawID, rawLibraryID string
		if err := rows.Scan(&rawID, &rawLibraryID, &record.Title, &record.Author, &record.Kind, &record.SourceURL, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store %s: %w", op, err)
		}
		record.ID = ULID(rawID)
		record.LibraryID = ULID(rawLibraryID)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	if out == nil {
		out = []Work{}
	}
	return out, nil
}

// UpdateWork validates and updates a work record.
func (s *Store) UpdateWork(ctx context.Context, tx *sql.Tx, record *Work) error {
	const op = "update work"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE work SET library_id = ?, title = ?, author = ?, kind = ?, source_url = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		record.LibraryID.String(), record.Title, record.Author, record.Kind, record.SourceURL, record.ID.String(),
	)
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, record.ID, result)
}

// DeleteWork removes a work by id.
func (s *Store) DeleteWork(ctx context.Context, tx *sql.Tx, id ULID) error {
	const op = "delete work"
	result, err := tx.ExecContext(ctx, `DELETE FROM work WHERE id = ?`, id.String())
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, id, result)
}

// --- Edition ---

// CreateEdition validates and inserts an edition.
func (s *Store) CreateEdition(ctx context.Context, tx *sql.Tx, record *Edition) error {
	const op = "create edition"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO edition (id, work_id, name, language, format) VALUES (?, ?, ?, ?, ?)`,
		record.ID.String(), record.WorkID.String(), record.Name, record.Language, record.Format,
	)
	if err != nil {
		return writeError(op, err)
	}
	return nil
}

// GetEdition returns the edition identified by id.
func (s *Store) GetEdition(ctx context.Context, tx *sql.Tx, id ULID) (*Edition, error) {
	const op = "get edition"
	row := tx.QueryRowContext(ctx,
		`SELECT id, work_id, name, language, format, created_at, updated_at FROM edition WHERE id = ?`,
		id.String(),
	)
	var record Edition
	var rawID, rawWorkID string
	if err := row.Scan(&rawID, &rawWorkID, &record.Name, &record.Language, &record.Format, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFoundError(op, id)
		}
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	record.ID = ULID(rawID)
	record.WorkID = ULID(rawWorkID)
	return &record, nil
}

// ListEditionsByWork returns all editions for a work ordered by created_at, id.
func (s *Store) ListEditionsByWork(ctx context.Context, tx *sql.Tx, workID ULID) ([]Edition, error) {
	const op = "list editions"
	rows, err := tx.QueryContext(ctx,
		`SELECT id, work_id, name, language, format, created_at, updated_at FROM edition WHERE work_id = ? ORDER BY created_at, id`,
		workID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	defer rows.Close()

	var out []Edition
	for rows.Next() {
		var record Edition
		var rawID, rawWorkID string
		if err := rows.Scan(&rawID, &rawWorkID, &record.Name, &record.Language, &record.Format, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store %s: %w", op, err)
		}
		record.ID = ULID(rawID)
		record.WorkID = ULID(rawWorkID)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	if out == nil {
		out = []Edition{}
	}
	return out, nil
}

// UpdateEdition validates and updates an edition.
func (s *Store) UpdateEdition(ctx context.Context, tx *sql.Tx, record *Edition) error {
	const op = "update edition"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE edition SET work_id = ?, name = ?, language = ?, format = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		record.WorkID.String(), record.Name, record.Language, record.Format, record.ID.String(),
	)
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, record.ID, result)
}

// DeleteEdition removes an edition by id.
func (s *Store) DeleteEdition(ctx context.Context, tx *sql.Tx, id ULID) error {
	const op = "delete edition"
	result, err := tx.ExecContext(ctx, `DELETE FROM edition WHERE id = ?`, id.String())
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, id, result)
}

// --- Chapter ---

// CreateChapter validates and inserts a chapter.
func (s *Store) CreateChapter(ctx context.Context, tx *sql.Tx, record *Chapter) error {
	const op = "create chapter"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.ID.String(), record.EditionID.String(), record.Title, record.ChapterNum, record.StartMs, record.EndMs, record.DurationMs,
	)
	if err != nil {
		return writeError(op, err)
	}
	return nil
}

// GetChapter returns the chapter identified by id.
func (s *Store) GetChapter(ctx context.Context, tx *sql.Tx, id ULID) (*Chapter, error) {
	const op = "get chapter"
	row := tx.QueryRowContext(ctx,
		`SELECT id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms, created_at, updated_at FROM chapter WHERE id = ?`,
		id.String(),
	)
	var record Chapter
	var rawID, rawEditionID string
	if err := row.Scan(&rawID, &rawEditionID, &record.Title, &record.ChapterNum, &record.StartMs, &record.EndMs, &record.DurationMs, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFoundError(op, id)
		}
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	record.ID = ULID(rawID)
	record.EditionID = ULID(rawEditionID)
	return &record, nil
}

// ListChaptersByEdition returns all chapters for an edition ordered by created_at, id.
func (s *Store) ListChaptersByEdition(ctx context.Context, tx *sql.Tx, editionID ULID) ([]Chapter, error) {
	const op = "list chapters"
	rows, err := tx.QueryContext(ctx,
		`SELECT id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms, created_at, updated_at FROM chapter WHERE edition_id = ? ORDER BY created_at, id`,
		editionID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	defer rows.Close()

	var out []Chapter
	for rows.Next() {
		var record Chapter
		var rawID, rawEditionID string
		if err := rows.Scan(&rawID, &rawEditionID, &record.Title, &record.ChapterNum, &record.StartMs, &record.EndMs, &record.DurationMs, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store %s: %w", op, err)
		}
		record.ID = ULID(rawID)
		record.EditionID = ULID(rawEditionID)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	if out == nil {
		out = []Chapter{}
	}
	return out, nil
}

// UpdateChapter validates and updates a chapter.
func (s *Store) UpdateChapter(ctx context.Context, tx *sql.Tx, record *Chapter) error {
	const op = "update chapter"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE chapter SET edition_id = ?, title = ?, chapter_num = ?, start_ms = ?, end_ms = ?, duration_ms = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		record.EditionID.String(), record.Title, record.ChapterNum, record.StartMs, record.EndMs, record.DurationMs, record.ID.String(),
	)
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, record.ID, result)
}

// DeleteChapter removes a chapter by id.
func (s *Store) DeleteChapter(ctx context.Context, tx *sql.Tx, id ULID) error {
	const op = "delete chapter"
	result, err := tx.ExecContext(ctx, `DELETE FROM chapter WHERE id = ?`, id.String())
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, id, result)
}

// --- SourceAsset ---

// CreateSourceAsset validates and inserts a source asset.
func (s *Store) CreateSourceAsset(ctx context.Context, tx *sql.Tx, record *SourceAsset) error {
	const op = "create source asset"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO source_asset (id, chapter_id, url, mime_type, size_bytes, sha256_hash, start_ms, end_ms, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID.String(), record.ChapterID.String(), record.URL, record.MIMEType, record.SizeBytes, record.SHA256Hash, record.StartMs, record.EndMs, record.DurationMs,
	)
	if err != nil {
		return writeError(op, err)
	}
	return nil
}

// GetSourceAsset returns the source asset identified by id.
func (s *Store) GetSourceAsset(ctx context.Context, tx *sql.Tx, id ULID) (*SourceAsset, error) {
	const op = "get source asset"
	row := tx.QueryRowContext(ctx,
		`SELECT id, chapter_id, url, mime_type, size_bytes, sha256_hash, start_ms, end_ms, duration_ms, created_at, updated_at FROM source_asset WHERE id = ?`,
		id.String(),
	)
	var record SourceAsset
	var rawID, rawChapterID string
	if err := row.Scan(&rawID, &rawChapterID, &record.URL, &record.MIMEType, &record.SizeBytes, &record.SHA256Hash, &record.StartMs, &record.EndMs, &record.DurationMs, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFoundError(op, id)
		}
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	record.ID = ULID(rawID)
	record.ChapterID = ULID(rawChapterID)
	return &record, nil
}

// ListSourceAssetsByChapter returns all source assets for a chapter ordered by created_at, id.
func (s *Store) ListSourceAssetsByChapter(ctx context.Context, tx *sql.Tx, chapterID ULID) ([]SourceAsset, error) {
	const op = "list source assets"
	rows, err := tx.QueryContext(ctx,
		`SELECT id, chapter_id, url, mime_type, size_bytes, sha256_hash, start_ms, end_ms, duration_ms, created_at, updated_at FROM source_asset WHERE chapter_id = ? ORDER BY created_at, id`,
		chapterID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	defer rows.Close()

	var out []SourceAsset
	for rows.Next() {
		var record SourceAsset
		var rawID, rawChapterID string
		if err := rows.Scan(&rawID, &rawChapterID, &record.URL, &record.MIMEType, &record.SizeBytes, &record.SHA256Hash, &record.StartMs, &record.EndMs, &record.DurationMs, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store %s: %w", op, err)
		}
		record.ID = ULID(rawID)
		record.ChapterID = ULID(rawChapterID)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store %s: %w", op, err)
	}
	if out == nil {
		out = []SourceAsset{}
	}
	return out, nil
}

// UpdateSourceAsset validates and updates a source asset.
func (s *Store) UpdateSourceAsset(ctx context.Context, tx *sql.Tx, record *SourceAsset) error {
	const op = "update source asset"
	if err := record.Validate(); err != nil {
		return validationError(op, err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE source_asset SET chapter_id = ?, url = ?, mime_type = ?, size_bytes = ?, sha256_hash = ?, start_ms = ?, end_ms = ?, duration_ms = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		record.ChapterID.String(), record.URL, record.MIMEType, record.SizeBytes, record.SHA256Hash, record.StartMs, record.EndMs, record.DurationMs, record.ID.String(),
	)
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, record.ID, result)
}

// DeleteSourceAsset removes a source asset by id.
func (s *Store) DeleteSourceAsset(ctx context.Context, tx *sql.Tx, id ULID) error {
	const op = "delete source asset"
	result, err := tx.ExecContext(ctx, `DELETE FROM source_asset WHERE id = ?`, id.String())
	if err != nil {
		return writeError(op, err)
	}
	return requireAffected(op, id, result)
}

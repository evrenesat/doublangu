package reader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"doublangu/internal/library"
	"doublangu/internal/media"
	"doublangu/internal/store"
	"modernc.org/sqlite"
)

// Store owns article transactions while reusing the repository-wide SQLite
// connection and migration lifecycle.
type Store struct {
	db    *store.DB
	media *media.Store
}

// NewStore returns an article store backed by db.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// NewStoreWithMedia attaches the server-authoritative media cleanup boundary.
// The plain constructor remains useful for tests and callers that only need
// article metadata.
func NewStoreWithMedia(db *store.DB, mediaStore *media.Store) *Store {
	return &Store{db: db, media: mediaStore}
}

// CreateArticle inserts an article and all of its blocks atomically.
func (s *Store) CreateArticle(ctx context.Context, article *Article) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	if article == nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: errors.New("article is nil")}
	}
	if err := article.Validate(); err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO article (id, title, source_language, target_language, enrichment_status, enrichment_error_code)
			VALUES (?, ?, ?, ?, ?, ?)
		`, article.ID.String(), article.Title, article.SourceLanguage, article.TargetLanguage, article.EnrichmentStatus, article.EnrichmentErrorCode); err != nil {
			return writeError("create article", err)
		}
		for index := range article.Blocks {
			block := &article.Blocks[index]
			if block.ID.IsZero() {
				block.ID = library.NewULID()
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO article_block (id, article_id, block_index, kind, source_text)
				VALUES (?, ?, ?, ?, ?)
			`, block.ID.String(), article.ID.String(), block.BlockIndex, block.Kind, block.SourceText); err != nil {
				return writeError("create article block", err)
			}
		}
		return nil
	})
}

// ListArticles returns compact article summaries newest first.
func (s *Store) ListArticles(ctx context.Context) ([]ArticleSummary, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("reader: nil database")
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, title, source_language, target_language, enrichment_status,
		       enrichment_error_code, created_at, updated_at, content_hash,
		       analysis_status, analysis_error_code, analysis_model, analysis_effort,
		       narration_status, narration_error_code
		FROM article ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("reader list articles: %w", err)
	}
	defer rows.Close()
	articles := make([]ArticleSummary, 0)
	for rows.Next() {
		var article ArticleSummary
		var id, status string
		var analysisStatus, narrationStatus string
		if err := rows.Scan(&id, &article.Title, &article.SourceLanguage, &article.TargetLanguage, &status, &article.EnrichmentErrorCode, &article.CreatedAt, &article.UpdatedAt, &article.ContentHash, &analysisStatus, &article.AnalysisErrorCode, &article.AnalysisModel, &article.AnalysisEffort, &narrationStatus, &article.NarrationErrorCode); err != nil {
			return nil, fmt.Errorf("reader list articles: %w", err)
		}
		article.ID = library.ULID(id)
		article.EnrichmentStatus = EnrichmentStatus(status)
		article.AnalysisStatus = AnalysisStatus(analysisStatus)
		article.NarrationStatus = NarrationStatus(narrationStatus)
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reader list articles: %w", err)
	}
	return articles, nil
}

// GetArticle returns an article with ordered blocks, annotations, and joined
// learner state. The caller must supply a validated canonical ULID.
func (s *Store) GetArticle(ctx context.Context, id library.ULID) (*Article, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("reader: nil database")
	}
	var article *Article
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		article, err = s.getArticleTx(ctx, tx, id)
		return err
	})
	return article, err
}

func (s *Store) getArticleTx(ctx context.Context, tx *sql.Tx, id library.ULID) (*Article, error) {
	const op = "get article"
	var article Article
	var rawID, status string
	err := tx.QueryRowContext(ctx, `
		SELECT id, title, source_language, target_language, enrichment_status,
		       enrichment_error_code, created_at, updated_at
		FROM article WHERE id = ?
	`, id.String()).Scan(&rawID, &article.Title, &article.SourceLanguage, &article.TargetLanguage, &status, &article.EnrichmentErrorCode, &article.CreatedAt, &article.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &Error{Op: op, Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
	}
	if err != nil {
		return nil, fmt.Errorf("reader %s: %w", op, err)
	}
	article.ID = library.ULID(rawID)
	article.EnrichmentStatus = EnrichmentStatus(status)
	article.Blocks = make([]ArticleBlock, 0)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, article_id, block_index, kind, source_text
		FROM article_block WHERE article_id = ? ORDER BY block_index
	`, id.String())
	if err != nil {
		return nil, fmt.Errorf("reader list article blocks: %w", err)
	}
	blockByID := make(map[string]int)
	for rows.Next() {
		var block ArticleBlock
		var rawBlockID, rawArticleID string
		if err := rows.Scan(&rawBlockID, &rawArticleID, &block.BlockIndex, &block.Kind, &block.SourceText); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reader scan article block: %w", err)
		}
		block.ID = library.ULID(rawBlockID)
		block.ArticleID = library.ULID(rawArticleID)
		block.Annotations = make([]Annotation, 0)
		block.Sentences = make([]ArticleSentence, 0)
		block.Occurrences = make([]ArticleOccurrence, 0)
		blockByID[rawBlockID] = len(article.Blocks)
		article.Blocks = append(article.Blocks, block)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("reader list article blocks: %w", err)
	}
	rows.Close()

	annotationRows, err := tx.QueryContext(ctx, `
		SELECT a.id, a.article_block_id, a.start_utf16, a.end_utf16, a.source_text,
		       a.kind, a.learning_key, a.primary_translation, a.alternatives_json,
		       a.literal_translation, a.meaning_note, a.usage_note, a.parts_note,
		       a.suggest_shadow, ls.status, ls.updated_at
		FROM article_annotation AS a
		JOIN article_block AS b ON b.id = a.article_block_id
		LEFT JOIN learning_state AS ls
		  ON ls.source_language = ? AND ls.kind = a.kind AND ls.learning_key = a.learning_key
		WHERE b.article_id = ?
		ORDER BY b.block_index, a.start_utf16, a.end_utf16
	`, article.SourceLanguage, id.String())
	if err != nil {
		return nil, fmt.Errorf("reader list article annotations: %w", err)
	}
	defer annotationRows.Close()
	for annotationRows.Next() {
		var annotation Annotation
		var rawAnnotationID, rawBlockID, rawKind, alternativesJSON string
		var suggestShadow int
		var learnerStatus, learnerUpdated sql.NullString
		if err := annotationRows.Scan(
			&rawAnnotationID, &rawBlockID, &annotation.StartUTF16, &annotation.EndUTF16,
			&annotation.SourceText, &rawKind, &annotation.LearningKey, &annotation.PrimaryTranslation,
			&alternativesJSON, &annotation.LiteralTranslation, &annotation.MeaningNote,
			&annotation.UsageNote, &annotation.PartsNote, &suggestShadow, &learnerStatus, &learnerUpdated,
		); err != nil {
			return nil, fmt.Errorf("reader scan annotation: %w", err)
		}
		annotation.ID = library.ULID(rawAnnotationID)
		annotation.ArticleBlockID = library.ULID(rawBlockID)
		annotation.Kind = AnnotationKind(rawKind)
		annotation.SuggestShadow = suggestShadow != 0
		if err := json.Unmarshal([]byte(alternativesJSON), &annotation.Alternatives); err != nil {
			return nil, fmt.Errorf("reader decode annotation alternatives: %w", err)
		}
		if annotation.Alternatives == nil {
			annotation.Alternatives = []string{}
		}
		if learnerStatus.Valid {
			annotation.LearningState = &LearningState{
				SourceLanguage: article.SourceLanguage,
				Kind:           annotation.Kind,
				LearningKey:    annotation.LearningKey,
				Status:         LearningStatus(learnerStatus.String),
				UpdatedAt:      learnerUpdated.String,
			}
		}
		annotation.ShowShadow = annotation.SuggestShadow
		if annotation.LearningState != nil {
			annotation.ShowShadow = annotation.LearningState.Status != LearningStatusLearned
		}
		if blockIndex, ok := blockByID[rawBlockID]; ok {
			article.Blocks[blockIndex].Annotations = append(article.Blocks[blockIndex].Annotations, annotation)
		}
	}
	if err := annotationRows.Err(); err != nil {
		return nil, fmt.Errorf("reader list article annotations: %w", err)
	}
	if err := annotationRows.Close(); err != nil {
		return nil, fmt.Errorf("reader close article annotations: %w", err)
	}
	if err := s.loadV2Tx(ctx, tx, id, &article, blockByID); err != nil {
		return nil, err
	}
	return &article, nil
}

// MarkProcessing claims an article for enrichment. Existing processing state
// is reported as KindInProgress and previous annotations remain untouched.
func (s *Store) MarkProcessing(ctx context.Context, id library.ULID) error {
	return s.withDB(ctx, func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx, "SELECT enrichment_status FROM article WHERE id = ?", id.String()).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return &Error{Op: "mark article processing", Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
		}
		if err != nil {
			return fmt.Errorf("reader mark article processing: %w", err)
		}
		if status == string(StatusProcessing) {
			return &Error{Op: "mark article processing", Kind: KindInProgress, Err: errors.New("enrichment is already in progress")}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE article SET enrichment_status = 'processing', enrichment_error_code = '',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
		`, id.String()); err != nil {
			return writeError("mark article processing", err)
		}
		return nil
	})
}

// MarkFailed records a sanitized stable code without deleting a previous good
// annotation set.
func (s *Store) MarkFailed(ctx context.Context, id library.ULID, code string) error {
	if !validErrorCode(code) {
		return &Error{Op: "mark article failed", Kind: KindValidation, Err: errors.New("invalid enrichment error code")}
	}
	return s.withDB(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE article SET enrichment_status = 'failed', enrichment_error_code = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
		`, code, id.String())
		if err != nil {
			return writeError("mark article failed", err)
		}
		return requireAffected("mark article failed", id, result)
	})
}

// ReplaceAnnotations atomically replaces the annotation set and marks the
// article ready. Any validation or write error rolls back the old good set.
func (s *Store) ReplaceAnnotations(ctx context.Context, id library.ULID, annotations []Annotation) error {
	return s.withDB(ctx, func(tx *sql.Tx) error {
		if err := articleExists(ctx, tx, id); err != nil {
			return err
		}
		blocks, err := articleBlocks(ctx, tx, id)
		if err != nil {
			return err
		}
		for index := range annotations {
			if err := validateStoredAnnotation(&annotations[index], blocks); err != nil {
				return &Error{Op: "replace article annotations", Kind: KindValidation, Err: fmt.Errorf("annotation %d: %w", index, err)}
			}
		}
		for left := 0; left < len(annotations); left++ {
			for right := left + 1; right < len(annotations); right++ {
				if annotations[left].ArticleBlockID == annotations[right].ArticleBlockID && spansOverlap(annotations[left], annotations[right]) {
					return &Error{Op: "replace article annotations", Kind: KindValidation, Err: fmt.Errorf("annotations %d and %d overlap", left, right)}
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM article_annotation
			WHERE article_block_id IN (SELECT id FROM article_block WHERE article_id = ?)
		`, id.String()); err != nil {
			return fmt.Errorf("reader replace article annotations: %w", err)
		}
		for _, annotation := range annotations {
			alternativesJSON, err := json.Marshal(annotation.Alternatives)
			if err != nil {
				return fmt.Errorf("reader encode annotation alternatives: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO article_annotation (
					id, article_block_id, start_utf16, end_utf16, source_text, kind,
					learning_key, primary_translation, alternatives_json, literal_translation,
					meaning_note, usage_note, parts_note, suggest_shadow
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, annotation.ID.String(), annotation.ArticleBlockID.String(), annotation.StartUTF16, annotation.EndUTF16,
				annotation.SourceText, annotation.Kind, annotation.LearningKey, annotation.PrimaryTranslation,
				string(alternativesJSON), annotation.LiteralTranslation, annotation.MeaningNote,
				annotation.UsageNote, annotation.PartsNote, boolInt(annotation.SuggestShadow)); err != nil {
				return writeError("replace article annotations", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE article SET enrichment_status = 'ready', enrichment_error_code = '',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?
		`, id.String()); err != nil {
			return writeError("mark article ready", err)
		}
		return nil
	})
}

// UpsertLearningState idempotently stores a normalized explicit learner state.
func (s *Store) UpsertLearningState(ctx context.Context, state *LearningState) (*LearningState, error) {
	if state == nil {
		return nil, &Error{Op: "upsert learning state", Kind: KindValidation, Err: errors.New("learning state is nil")}
	}
	if err := normalizeLearningState(state); err != nil {
		return nil, &Error{Op: "upsert learning state", Kind: KindValidation, Err: err}
	}
	if state.UpdatedAt == "" {
		state.UpdatedAt = store.NowUTC()
	}
	err := s.withDB(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO learning_state (source_language, kind, learning_key, status, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(source_language, kind, learning_key) DO UPDATE SET
				status = excluded.status, updated_at = excluded.updated_at
		`, state.SourceLanguage, state.Kind, state.LearningKey, state.Status, state.UpdatedAt)
		if err != nil {
			return writeError("upsert learning state", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	copy := *state
	return &copy, nil
}

// RecoverInterrupted converts processing rows left by an exited server into a
// retryable failure before request handling begins. It also closes any
// analysis_run still marked running: the terminal failed status carries the
// stable interruption code, while paragraph progress and retained turn data
// remain unchanged for diagnostics and retry.
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	return s.withDB(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE article SET enrichment_status = 'failed', enrichment_error_code = 'v1.enrichment_interrupted',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE enrichment_status = 'processing'
		`)
		if err != nil {
			return fmt.Errorf("reader recover interrupted enrichment: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE article SET analysis_status = 'queued', analysis_error_code = 'v1.analysis_interrupted',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE analysis_status = 'processing'
		`)
		if err != nil {
			return fmt.Errorf("reader recover interrupted analysis: %w", err)
		}
		recoveredAt := store.NowUTC()
		_, err = tx.ExecContext(ctx, `
			UPDATE analysis_run SET status = 'failed', completed_at = ?,
				duration_ms = CASE
					WHEN julianday(started_at) IS NULL THEN duration_ms
					WHEN julianday(?) > julianday(started_at)
						THEN CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)
					ELSE 0
				END,
				error_code = 'v1.analysis_interrupted',
				error_detail = 'analysis run interrupted during server restart'
			WHERE status = 'running'
		`, recoveredAt, recoveredAt, recoveredAt)
		if err != nil {
			return fmt.Errorf("reader recover interrupted analysis runs: %w", err)
		}
		return nil
	})
}

func (s *Store) withDB(ctx context.Context, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	return s.db.WithTransaction(ctx, fn)
}

func articleExists(ctx context.Context, tx *sql.Tx, id library.ULID) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM article WHERE id = ?", id.String()).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return &Error{Op: "replace article annotations", Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
	} else if err != nil {
		return fmt.Errorf("reader find article: %w", err)
	}
	return nil
}

type blockRecord struct {
	id   library.ULID
	text string
}

func articleBlocks(ctx context.Context, tx *sql.Tx, id library.ULID) (map[string]blockRecord, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, source_text FROM article_block WHERE article_id = ?", id.String())
	if err != nil {
		return nil, fmt.Errorf("reader find article blocks: %w", err)
	}
	defer rows.Close()
	blocks := make(map[string]blockRecord)
	for rows.Next() {
		var rawID, text string
		if err := rows.Scan(&rawID, &text); err != nil {
			return nil, fmt.Errorf("reader scan article block: %w", err)
		}
		blocks[rawID] = blockRecord{id: library.ULID(rawID), text: text}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reader list article blocks: %w", err)
	}
	return blocks, nil
}

func validateStoredAnnotation(annotation *Annotation, blocks map[string]blockRecord) error {
	if annotation == nil {
		return errors.New("annotation is nil")
	}
	block, ok := blocks[annotation.ArticleBlockID.String()]
	if !ok {
		return errors.New("article block does not belong to article")
	}
	if annotation.ID.IsZero() {
		annotation.ID = library.NewULID()
	}
	if !annotation.Kind.Valid() {
		return fmt.Errorf("invalid kind %q", annotation.Kind)
	}
	if annotation.StartUTF16 < 0 || annotation.EndUTF16 <= annotation.StartUTF16 {
		return errors.New("invalid UTF-16 span")
	}
	text, err := TextForUTF16Span(block.text, annotation.StartUTF16, annotation.EndUTF16)
	if err != nil {
		return err
	}
	if text != annotation.SourceText {
		return errors.New("source_text does not match UTF-16 span")
	}
	key, err := NormalizeLearningKey(annotation.LearningKey)
	if err != nil {
		return err
	}
	annotation.LearningKey = key
	if strings.TrimSpace(annotation.PrimaryTranslation) == "" {
		return errors.New("primary_translation must not be empty")
	}
	if len(annotation.Alternatives) > 3 {
		return errors.New("alternatives must contain at most three strings")
	}
	seen := make(map[string]struct{}, len(annotation.Alternatives))
	for index, alternative := range annotation.Alternatives {
		alternative = strings.TrimSpace(alternative)
		if alternative == "" {
			return fmt.Errorf("alternatives[%d] must not be empty", index)
		}
		if _, exists := seen[alternative]; exists {
			return fmt.Errorf("alternatives[%d] is duplicated", index)
		}
		seen[alternative] = struct{}{}
		annotation.Alternatives[index] = alternative
	}
	if annotation.Alternatives == nil {
		annotation.Alternatives = []string{}
	}
	return nil
}

func normalizeLearningState(state *LearningState) error {
	var err error
	state.SourceLanguage, err = library.ParseBCP47(state.SourceLanguage)
	if err != nil {
		return err
	}
	if !state.Kind.Valid() {
		return fmt.Errorf("invalid kind %q", state.Kind)
	}
	state.LearningKey, err = NormalizeLearningKey(state.LearningKey)
	if err != nil {
		return err
	}
	if state.Status != LearningStatusLearned && state.Status != LearningStatusUnlearned {
		return fmt.Errorf("invalid learning status %q", state.Status)
	}
	return nil
}

func validErrorCode(code string) bool {
	if code == "" || len(code) > 100 || !strings.HasPrefix(code, "v1.") {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireAffected(op string, id library.ULID, result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reader %s rows affected: %w", op, err)
	}
	if count == 0 {
		return &Error{Op: op, Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
	}
	return nil
}

func writeError(op string, err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 {
		return &Error{Op: op, Kind: KindConflict, Err: err}
	}
	return fmt.Errorf("reader %s: %w", op, err)
}

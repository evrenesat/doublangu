package reader

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

// AnalysisJobPayload is deliberately small: the worker reloads canonical
// article source from SQLite and never trusts a browser-provided copy.
type AnalysisJobPayload struct {
	ArticleID     string `json:"article_id"`
	ContentHash   string `json:"content_hash"`
	Contract      string `json:"contract_version"`
	PromptVersion string `json:"prompt_version"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	Fresh         bool   `json:"fresh"`
}

func (s *Store) analysisSelection(ctx context.Context) (AnalysisSelection, error) {
	if s == nil || s.db == nil {
		return AnalysisSelection{}, errors.New("reader: nil database")
	}
	var selection AnalysisSelection
	err := s.db.QueryRow(ctx, `SELECT model, effort FROM analysis_settings WHERE id = 1`).Scan(&selection.Model, &selection.Effort)
	if errors.Is(err, sql.ErrNoRows) {
		return AnalysisSelection{Effort: "medium"}, nil
	}
	if err != nil {
		return AnalysisSelection{}, err
	}
	if selection.Effort == "" {
		selection.Effort = "medium"
	}
	return selection, nil
}

func analysisSelectionTx(ctx context.Context, tx *sql.Tx) (AnalysisSelection, error) {
	var selection AnalysisSelection
	err := tx.QueryRowContext(ctx, `SELECT model, effort FROM analysis_settings WHERE id = 1`).Scan(&selection.Model, &selection.Effort)
	if errors.Is(err, sql.ErrNoRows) {
		return AnalysisSelection{Effort: "medium"}, nil
	}
	if err != nil {
		return AnalysisSelection{}, err
	}
	if selection.Effort == "" {
		selection.Effort = "medium"
	}
	return selection, nil
}

// PrepareAnalysis creates deterministic source anchors and the scoped local
// lexicon candidate list for an article. The persisted sentence rows are the
// authoritative narration anchors: they were either preserved from an earlier
// contract ('legacy.analysis') or created deterministically with the article.
// An article that has no sentence rows yet receives deterministic rows lazily
// so every prepared analysis input carries stable source sentences.
func (s *Store) PrepareAnalysis(ctx context.Context, id library.ULID) (semantics.PreparedArticle, error) {
	if s == nil || s.db == nil {
		return semantics.PreparedArticle{}, errors.New("reader: nil database")
	}
	var prepared semantics.PreparedArticle
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var title, sourceLanguage, targetLanguage string
		if err := tx.QueryRowContext(ctx, `SELECT title, source_language, target_language FROM article WHERE id = ?`, id.String()).Scan(&title, &sourceLanguage, &targetLanguage); errors.Is(err, sql.ErrNoRows) {
			return &Error{Op: "prepare analysis", Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
		} else if err != nil {
			return err
		}
		blocks, err := sourceBlocksTx(ctx, tx, id)
		if err != nil {
			return err
		}
		prepared, err = semantics.Prepare(title, sourceLanguage, targetLanguage, blocks, nil)
		if err != nil {
			return err
		}
		anchors, err := storedSentenceAnchorsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(anchors) == 0 {
			// Lazy deterministic creation: only articles that have no
			// sentence rows at all are re-segmented; preserved rows are
			// never silently re-segmented.
			anchors, err = SegmentArticleSentences(blocks)
			if err != nil {
				return &Error{Op: "prepare analysis", Kind: KindValidation, Err: err}
			}
			blockIDs, err := blockIDsByIndexTx(ctx, tx, id)
			if err != nil {
				return err
			}
			for _, block := range blocks {
				blockID, ok := blockIDs[block.BlockIndex]
				if !ok {
					return &Error{Op: "prepare analysis", Kind: KindValidation, Err: fmt.Errorf("block %d not found", block.BlockIndex)}
				}
				if err := insertBlockSentencesTx(ctx, tx, blockID, block.SourceText); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE article SET sentence_revision = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, SentenceRevisionSourceSentencesV1, id.String()); err != nil {
				return err
			}
		}
		prepared.Sentences = anchors
		return nil
	})
	if err != nil {
		return semantics.PreparedArticle{}, err
	}
	candidates, err := semantics.NewStore(s.db).LookupCandidates(ctx, prepared)
	if err != nil {
		return semantics.PreparedArticle{}, err
	}
	prepared.Candidates = candidates
	return prepared, nil
}

// storedSentenceAnchorsTx returns the persisted source sentence anchors of an
// article in block order and then sentence order.
func storedSentenceAnchorsTx(ctx context.Context, tx *sql.Tx, id library.ULID) ([]semantics.ResolvedSentence, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT b.block_index, s.sentence_index, s.start_utf16, s.end_utf16, s.source_text
		FROM article_sentence AS s
		JOIN article_block AS b ON b.id = s.article_block_id
		WHERE b.article_id = ?
		ORDER BY b.block_index, s.sentence_index
	`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	anchors := make([]semantics.ResolvedSentence, 0)
	for rows.Next() {
		var anchor semantics.ResolvedSentence
		if err := rows.Scan(&anchor.Span.BlockIndex, &anchor.Index, &anchor.Span.StartUTF16, &anchor.Span.EndUTF16, &anchor.Span.SourceText); err != nil {
			return nil, err
		}
		anchors = append(anchors, anchor)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return anchors, nil
}

func blockIDsByIndexTx(ctx context.Context, tx *sql.Tx, id library.ULID) (map[int]library.ULID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT block_index, id FROM article_block WHERE article_id = ?`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int]library.ULID)
	for rows.Next() {
		var index int
		var rawID string
		if err := rows.Scan(&index, &rawID); err != nil {
			return nil, err
		}
		result[index] = library.ULID(rawID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// CachedAnalysis returns a validated-contract response only for the exact
// article hash/language/contract tuple. A malformed cache row is ignored so a
// provider can repair it; it is never served as reader data.
func (s *Store) CachedAnalysis(ctx context.Context, input semantics.PreparedArticle, selection ...string) (response semantics.Response, providerModel string, hit bool, err error) {
	if s == nil || s.db == nil {
		return semantics.Response{}, "", false, errors.New("reader: nil database")
	}
	model, effort := "", ""
	if len(selection) > 0 {
		model = selection[0]
	}
	if len(selection) > 1 {
		effort = selection[1]
	}
	if effort == "" {
		effort = "medium"
	}
	preparedHash := semantics.PreparedInputHash(input)
	var raw, responseHash string
	err = s.db.QueryRow(ctx, `SELECT validated_response_json, provider_model, response_hash FROM analysis_cache WHERE prepared_input_hash = ? AND contract_version = ? AND prompt_version = ? AND provider_model = ? AND provider_effort = ?`, preparedHash, semantics.AnalysisContractVersion, semantics.PromptVersion, model, effort).Scan(&raw, &providerModel, &responseHash)
	if errors.Is(err, sql.ErrNoRows) {
		return semantics.Response{}, "", false, nil
	}
	if err != nil {
		return semantics.Response{}, "", false, err
	}
	if sha256Hex([]byte(raw)) != responseHash {
		return semantics.Response{}, "", false, nil
	}
	response, err = semantics.DecodeResponse([]byte(raw))
	if err != nil {
		return semantics.Response{}, "", false, nil
	}
	if _, err := semantics.ValidateResponse(input, response); err != nil {
		return semantics.Response{}, "", false, nil
	}
	return response, providerModel, true, nil
}

// CachedChunk loads only an exact validated paragraph response. Cache rows are
// re-hashed and revalidated on every read; corrupt or stale rows are misses.
func (s *Store) CachedChunk(ctx context.Context, chunk semantics.PreparedChunk, providerModel, providerEffort string) (semantics.Response, bool, error) {
	if s == nil || s.db == nil {
		return semantics.Response{}, false, errors.New("reader: nil database")
	}
	var raw, responseHash string
	err := s.db.QueryRow(ctx, `
		SELECT validated_response_json, response_hash
		FROM analysis_chunk_cache
		WHERE chunk_input_hash = ? AND contract_version = ? AND prompt_version = ?
		  AND provider_model = ? AND provider_effort = ?
	`, chunk.InputHash, semantics.AnalysisContractVersion, semantics.PromptVersion, providerModel, providerEffort).Scan(&raw, &responseHash)
	if errors.Is(err, sql.ErrNoRows) {
		return semantics.Response{}, false, nil
	}
	if err != nil {
		return semantics.Response{}, false, err
	}
	if sha256Hex([]byte(raw)) != responseHash {
		return semantics.Response{}, false, nil
	}
	response, err := semantics.DecodeResponse([]byte(raw))
	if err != nil {
		return semantics.Response{}, false, nil
	}
	if _, err := semantics.ValidateChunkResponse(chunk, response); err != nil {
		return semantics.Response{}, false, nil
	}
	return response, true, nil
}

// SaveChunk stores a response only after local chunk validation succeeds.
func (s *Store) SaveChunk(ctx context.Context, chunk semantics.PreparedChunk, response semantics.Response, providerID, providerModel, providerEffort string, sourceRunID library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	if _, err := semantics.ValidateChunkResponse(chunk, response); err != nil {
		return fmt.Errorf("save analysis chunk: %w", err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	responseHash := sha256Hex(raw)
	_, err = s.db.Exec(ctx, `
		INSERT INTO analysis_chunk_cache (
			id, source_language, target_language, content_hash, block_index,
			block_hash, carry_hash, chunk_input_hash, contract_version,
			prompt_version, provider_id, provider_model, provider_effort,
			validated_response_json, response_hash, source_run_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chunk_input_hash, contract_version, prompt_version, provider_model, provider_effort)
		DO UPDATE SET provider_id = excluded.provider_id, validated_response_json = excluded.validated_response_json,
			response_hash = excluded.response_hash, source_run_id = excluded.source_run_id,
			created_at = excluded.created_at
	`, library.NewULID().String(), chunk.SourceLanguage, chunk.TargetLanguage, chunk.ContentHash,
		chunk.Block.BlockIndex, semantics.BlockHash(chunk.Block), semantics.CarryHash(chunk.PriorValidatedSenses),
		chunk.InputHash, semantics.AnalysisContractVersion, semantics.PromptVersion, providerID,
		providerModel, providerEffort, string(raw), responseHash, sourceRunID.String(), store.NowUTC())
	return err
}

// CreateArticleQueued stores source text and its initial analysis job in one
// transaction. It is the v2 article-creation path; CreateArticle remains the
// legacy persistence helper used by compatibility tests/callers.
func (s *Store) CreateArticleQueued(ctx context.Context, article *Article) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	if article == nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: errors.New("article is nil")}
	}
	if err := article.Validate(); err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	blocks := make([]semantics.Block, len(article.Blocks))
	for index, block := range article.Blocks {
		blocks[index] = semantics.Block{BlockIndex: block.BlockIndex, SourceText: block.SourceText}
	}
	prepared, err := semantics.Prepare(article.Title, article.SourceLanguage, article.TargetLanguage, blocks, nil)
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	// Source sentences are deterministic and stable: they are created with
	// the article, before any job is queued, so narration never depends on
	// model output.
	anchors, err := SegmentArticleSentences(blocks)
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	prepared.Sentences = anchors
	article.ContentHash = prepared.ContentHash
	article.AnalysisStatus = AnalysisQueued
	article.AnalysisRevision = ""
	article.AnalysisErrorCode = ""
	article.AnalysisModel = ""
	article.AnalysisEffort = ""
	article.NarrationStatus = NarrationNotRequested
	article.NarrationErrorCode = ""
	article.SentenceRevision = SentenceRevisionSourceSentencesV1
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		selection, err := analysisSelectionTx(ctx, tx)
		if err != nil {
			return err
		}
		article.AnalysisModel = selection.Model
		article.AnalysisEffort = selection.Effort
		if err := insertArticleTx(ctx, tx, article); err != nil {
			return err
		}
		payload, _ := json.Marshal(AnalysisJobPayload{
			ArticleID: article.ID.String(), ContentHash: prepared.ContentHash,
			Contract: semantics.AnalysisContractVersion, PromptVersion: semantics.PromptVersion,
			Model: selection.Model, Effort: selection.Effort, Fresh: false,
		})
		job, err := jobs.EnqueueTx(ctx, tx, jobs.Spec{
			JobType: jobs.AnalysisJobType, ExecutionTarget: jobs.TargetServer,
			OwnerType: "article", OwnerID: article.ID.String(),
			IdempotencyKey: analysisIdempotencyKey(article.ID, prepared.ContentHash, selection.Model, selection.Effort, false, false),
			InputHash:      prepared.ContentHash, PayloadJSON: string(payload), Priority: 100,
		})
		if err != nil {
			return err
		}
		article.AnalysisJobID = job.ID.String()
		if err := activateJobTx(ctx, tx, article.ID, job.ID); err != nil {
			return err
		}
		// Source sentences exist now, so narration can be queued before any
		// analysis: it never depends on subtitles or provider output.
		return speech.QueueArticleNarrationTx(ctx, tx, article.ID, false)
	})
}

func insertArticleTx(ctx context.Context, tx *sql.Tx, article *Article) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO article (id, title, source_language, target_language, enrichment_status,
			enrichment_error_code, content_hash, analysis_status, analysis_revision,
			analysis_error_code, analysis_model, analysis_effort, narration_status,
			narration_error_code, sentence_revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, article.ID.String(), article.Title, article.SourceLanguage, article.TargetLanguage,
		article.EnrichmentStatus, article.EnrichmentErrorCode, article.ContentHash,
		article.AnalysisStatus, article.AnalysisRevision, article.AnalysisErrorCode,
		article.AnalysisModel, article.AnalysisEffort, article.NarrationStatus,
		article.NarrationErrorCode, article.SentenceRevision); err != nil {
		return writeError("create article", err)
	}
	for index := range article.Blocks {
		block := &article.Blocks[index]
		if block.ID.IsZero() {
			block.ID = library.NewULID()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, ?, ?, ?)`, block.ID.String(), article.ID.String(), block.BlockIndex, block.Kind, block.SourceText); err != nil {
			return writeError("create article block", err)
		}
	}
	for _, block := range article.Blocks {
		if err := insertBlockSentencesTx(ctx, tx, block.ID, block.SourceText); err != nil {
			return err
		}
	}
	return nil
}

// insertBlockSentencesTx persists the deterministic sentence rows for one
// source block. Rows are source-owned: they are created once and semantic
// analysis never deletes, renumbers, or replaces them.
// profileSnapshotColumns returns the article snapshot columns to persist for
// a resolved pipeline profile. Nil snapshots keep every column empty.
func profileSnapshotColumns(snapshot *pipeline.ProfileSnapshot) (id, name, snapshotJSON, snapshotHash string, err error) {
	if snapshot == nil {
		return "", "", "", "", nil
	}
	hash, err := snapshot.SnapshotHash()
	if err != nil {
		return "", "", "", "", err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", "", "", "", err
	}
	return snapshot.ID, snapshot.Name, string(encoded), hash, nil
}

// persistArticleProfileSnapshotTx stores the resolved profile snapshot on the
// article row inside the caller's transaction. Settings changes never rewrite
// an already stored snapshot.
func persistArticleProfileSnapshotTx(ctx context.Context, tx *sql.Tx, id library.ULID, snapshot *pipeline.ProfileSnapshot) error {
	profileID, profileName, snapshotJSON, snapshotHash, err := profileSnapshotColumns(snapshot)
	if err != nil {
		return &Error{Op: "persist article profile", Kind: KindValidation, Err: err}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE article SET analysis_profile_id = ?, analysis_profile_name = ?, analysis_pipeline_snapshot_json = ?, analysis_pipeline_snapshot_hash = ? WHERE id = ?`,
		profileID, profileName, snapshotJSON, snapshotHash, id.String()); err != nil {
		return err
	}
	return nil
}

// CreateArticleQueuedWithProfile stores source text, deterministic sentences,
// narration, and the article's initial pipeline analysis job in one
// transaction while persisting the resolved profile snapshot. It is the
// pipeline article-creation path; CreateArticleQueued remains the legacy
// compatibility helper.
// CreateArticlePipelineUnavailable stores a readable source article when no
// usable analysis profile is active: no job is queued and the first block is
// marked failed with the stable profile-unavailable code.
func (s *Store) CreateArticlePipelineUnavailable(ctx context.Context, article *Article) error {
	if article == nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: errors.New("article is nil")}
	}
	if err := article.Validate(); err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	blocks := make([]semantics.Block, len(article.Blocks))
	for index, block := range article.Blocks {
		blocks[index] = semantics.Block{BlockIndex: block.BlockIndex, SourceText: block.SourceText}
	}
	prepared, err := semantics.Prepare(article.Title, article.SourceLanguage, article.TargetLanguage, blocks, nil)
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	anchors, err := SegmentArticleSentences(blocks)
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	prepared.Sentences = anchors
	article.ContentHash = prepared.ContentHash
	article.AnalysisStatus = AnalysisFailed
	article.AnalysisRevision = ""
	article.AnalysisErrorCode = ""
	article.AnalysisModel = ""
	article.AnalysisEffort = ""
	article.NarrationStatus = NarrationNotRequested
	article.NarrationErrorCode = ""
	article.SentenceRevision = SentenceRevisionSourceSentencesV1
	article.AnalysisJobID = ""
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := insertArticleTx(ctx, tx, article); err != nil {
			return err
		}
		if err := failFirstBlockTx(ctx, tx, article.ID, "v1.analysis_profile_unavailable"); err != nil {
			return err
		}
		return speech.QueueArticleNarrationTx(ctx, tx, article.ID, false)
	})
}

func (s *Store) CreateArticleQueuedWithProfile(ctx context.Context, article *Article, snapshot *pipeline.ProfileSnapshot) error {
	if article == nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: errors.New("article is nil")}
	}
	if err := article.Validate(); err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	if snapshot == nil {
		return s.CreateArticleQueued(ctx, article)
	}
	if _, _, _, snapshotHash, err := profileSnapshotColumns(snapshot); err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	} else {
		_ = snapshotHash
	}
	blocks := make([]semantics.Block, len(article.Blocks))
	for index, block := range article.Blocks {
		blocks[index] = semantics.Block{BlockIndex: block.BlockIndex, SourceText: block.SourceText}
	}
	prepared, err := semantics.Prepare(article.Title, article.SourceLanguage, article.TargetLanguage, blocks, nil)
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	anchors, err := SegmentArticleSentences(blocks)
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	prepared.Sentences = anchors
	snapshotHash, err := snapshot.SnapshotHash()
	if err != nil {
		return &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	article.ContentHash = prepared.ContentHash
	article.AnalysisStatus = AnalysisQueued
	article.AnalysisRevision = ""
	article.AnalysisErrorCode = ""
	article.AnalysisModel = ""
	article.AnalysisEffort = ""
	article.NarrationStatus = NarrationNotRequested
	article.NarrationErrorCode = ""
	article.SentenceRevision = SentenceRevisionSourceSentencesV1
	article.AnalysisJobID = ""
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := insertArticleTx(ctx, tx, article); err != nil {
			return err
		}
		if err := persistArticleProfileSnapshotTx(ctx, tx, article.ID, snapshot); err != nil {
			return err
		}
		payload, err := pipeline.EncodeJobPayload(pipeline.JobPayload{
			ArticleID: article.ID.String(), ContentHash: prepared.ContentHash,
			AnalysisContractVersion: pipeline.AnalysisContractVersion,
			PipelineVersion:         pipeline.PipelineVersion, Fresh: false,
			Profile: *snapshot, ProfileSnapshotHash: snapshotHash,
		})
		if err != nil {
			return err
		}
		job, err := jobs.EnqueueTx(ctx, tx, jobs.Spec{
			JobType: jobs.AnalysisJobType, ExecutionTarget: jobs.TargetServer,
			OwnerType: "article", OwnerID: article.ID.String(),
			IdempotencyKey: pipelineIdempotencyKey(article.ID, snapshotHash, prepared.ContentHash, false, false),
			InputHash:      prepared.ContentHash, PayloadJSON: string(payload), Priority: 100,
		})
		if err != nil {
			return err
		}
		article.AnalysisJobID = job.ID.String()
		if err := activateJobTx(ctx, tx, article.ID, job.ID); err != nil {
			return err
		}
		return speech.QueueArticleNarrationTx(ctx, tx, article.ID, false)
	})
}

// failFirstBlockTx marks the first source block failed with the given code
// so the article renders readable (the remaining blocks stay pending).
func failFirstBlockTx(ctx context.Context, tx *sql.Tx, articleID library.ULID, code string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE article_block SET analysis_status = 'failed', analysis_error_code = ? WHERE id = (SELECT id FROM article_block WHERE article_id = ? ORDER BY block_index LIMIT 1)`, code, articleID.String()); err != nil {
		return writeError("fail first article block", err)
	}
	return nil
}

func insertBlockSentencesTx(ctx context.Context, tx *sql.Tx, blockID library.ULID, sourceText string) error {
	sentences, err := SegmentSentences(sourceText)
	if err != nil {
		return &Error{Op: "create article sentences", Kind: KindValidation, Err: err}
	}
	for sentenceIndex, sentence := range sentences {
		if _, err := tx.ExecContext(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES (?, ?, ?, ?, ?, ?, ?)`, library.NewULID().String(), blockID.String(), sentenceIndex, sentence.StartUTF16, sentence.EndUTF16, sentence.SourceText, sha256Hex([]byte(sentence.SourceText))); err != nil {
			return writeError("create article sentence", err)
		}
	}
	return nil
}

// SegmentArticleSentences returns the deterministic ordered sentence anchors
// for every source block of an article.
func SegmentArticleSentences(blocks []semantics.Block) ([]semantics.ResolvedSentence, error) {
	anchors := make([]semantics.ResolvedSentence, 0)
	for _, block := range blocks {
		sentences, err := SegmentSentences(block.SourceText)
		if err != nil {
			return nil, err
		}
		for index, sentence := range sentences {
			anchors = append(anchors, semantics.ResolvedSentence{
				Index: index,
				Span: semantics.ResolvedSpan{
					BlockIndex: block.BlockIndex,
					StartUTF16: sentence.StartUTF16,
					EndUTF16:   sentence.EndUTF16,
					SourceText: sentence.SourceText,
				},
			})
		}
	}
	return anchors, nil
}

func sourceBlocksTx(ctx context.Context, tx *sql.Tx, id library.ULID) ([]semantics.Block, error) {
	rows, err := tx.QueryContext(ctx, `SELECT block_index, source_text FROM article_block WHERE article_id = ? ORDER BY block_index`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocks := make([]semantics.Block, 0)
	for rows.Next() {
		var block semantics.Block
		if err := rows.Scan(&block.BlockIndex, &block.SourceText); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, rows.Err()
}

// QueueAnalysis marks an article for background analysis and returns the
// durable job. A normal queue is idempotent; force creates a new owner-requested
// work item while leaving the last accepted analysis visible until replacement.
func (s *Store) QueueAnalysis(ctx context.Context, id library.ULID, force bool, fresh ...bool) (*jobs.Job, error) {
	prepared, err := s.PrepareAnalysis(ctx, id)
	if err != nil {
		return nil, err
	}
	freshRequested := len(fresh) > 0 && fresh[0]
	var job *jobs.Job
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		selection, err := analysisSelectionTx(ctx, tx)
		if err != nil {
			return err
		}
		status, err := articleAnalysisStatusTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !force && (status == string(AnalysisQueued) || status == string(AnalysisProcessing)) {
			// Returning the active idempotent work item is preferable to creating
			// a second queue entry after a browser retry.
			job, err = jobs.GetActiveOwnerJobTx(ctx, tx, "article", id.String(), jobs.AnalysisJobType)
			if err != nil {
				return err
			}
			// Legacy queued articles (queued before job ids were tracked) carry
			// an empty article job id. Adopt the active job and reset blocks
			// only then; a job that already owns the article is left untouched
			// so an in-flight run is never disturbed.
			var activeJobID string
			if err := tx.QueryRowContext(ctx, `SELECT analysis_job_id FROM article WHERE id = ?`, id.String()).Scan(&activeJobID); err != nil {
				return err
			}
			if activeJobID != job.ID.String() {
				return activateJobTx(ctx, tx, id, job.ID)
			}
			return nil
		}
		key := analysisIdempotencyKey(id, prepared.ContentHash, selection.Model, selection.Effort, freshRequested, force)
		payload, _ := json.Marshal(AnalysisJobPayload{
			ArticleID: id.String(), ContentHash: prepared.ContentHash,
			Contract: semantics.AnalysisContractVersion, PromptVersion: semantics.PromptVersion,
			Model: selection.Model, Effort: selection.Effort, Fresh: freshRequested,
		})
		if force {
			// An explicit reanalysis supersedes any older queued attempt. The
			// accepted materialization remains readable until the new job commits.
			if _, err := jobs.CancelOwnerJobsTx(ctx, tx, "article", id.String(), jobs.AnalysisJobType, "v1.analysis_superseded"); err != nil {
				return err
			}
		}
		if !force && (status == string(AnalysisFailed) || status == string(AnalysisReady)) {
			if existing, getErr := jobs.GetByIdempotencyKeyTx(ctx, tx, key); getErr == nil {
				if existing.State == jobs.StateFailed || existing.State == jobs.StateCanceled || existing.State == jobs.StateSucceeded {
					if _, err := tx.ExecContext(ctx, `UPDATE job SET state = 'queued', attempt_count = 0, available_at = ?, lease_owner = '', lease_token_hash = '', lease_expires_at = '', progress_percent = 0, error_code = '', completed_at = '', payload_json = ?, input_hash = ?, updated_at = ? WHERE id = ?`, store.NowUTC(), string(payload), prepared.ContentHash, store.NowUTC(), existing.ID.String()); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `UPDATE article SET content_hash = ?, analysis_model = ?, analysis_effort = ?, analysis_status = 'queued', analysis_error_code = '', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, prepared.ContentHash, selection.Model, selection.Effort, id.String()); err != nil {
						return err
					}
					job, err = jobs.GetByIdempotencyKeyTx(ctx, tx, key)
					if err != nil {
						return err
					}
					return activateJobTx(ctx, tx, id, job.ID)
				}
			} else if !errors.Is(getErr, sql.ErrNoRows) {
				return getErr
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE article SET content_hash = ?, analysis_model = ?, analysis_effort = ?, analysis_status = 'queued', analysis_error_code = '', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, prepared.ContentHash, selection.Model, selection.Effort, id.String()); err != nil {
			return err
		}
		job, err = jobs.EnqueueTx(ctx, tx, jobs.Spec{
			JobType: jobs.AnalysisJobType, ExecutionTarget: jobs.TargetServer,
			OwnerType: "article", OwnerID: id.String(), IdempotencyKey: key,
			InputHash: prepared.ContentHash, PayloadJSON: string(payload), Priority: 100,
		})
		if err != nil {
			return err
		}
		return activateJobTx(ctx, tx, id, job.ID)
	})
	return job, err
}

// analysisIdempotencyKey builds the durable job identity for one queue
// request. The prefix is historical job-type naming; the payload contract and
// prompt versions inside the key keep v3 work distinct from older attempts.
// pipelineIdempotencyKey builds the durable job identity for pipeline jobs:
// pipeline version, snapshot hash, content hash, and fresh/force mode. The
// snapshot hash makes settings changes after queueing unable to alias jobs.
func pipelineIdempotencyKey(id library.ULID, snapshotHash, contentHash string, fresh, force bool) string {
	mode := "normal"
	if fresh {
		mode = "fresh"
	}
	if force {
		return fmt.Sprintf("reader.pipeline:%s:%s:%s:%s:%s:request:%s", id.String(), pipeline.PipelineVersion, snapshotHash, contentHash, mode, library.NewULID().String())
	}
	return fmt.Sprintf("reader.pipeline:%s:%s:%s:%s:%s", id.String(), pipeline.PipelineVersion, snapshotHash, contentHash, mode)
}

func analysisIdempotencyKey(id library.ULID, contentHash, model, effort string, fresh, force bool) string {
	mode := "normal"
	if fresh {
		mode = "fresh"
	}
	if force {
		return fmt.Sprintf("reader.analysis.v2:%s:%s:%s:%s:%s:%s:%s:request:%s", id.String(), contentHash, semantics.AnalysisContractVersion, semantics.PromptVersion, model, effort, mode, library.NewULID().String())
	}
	return fmt.Sprintf("reader.analysis.v2:%s:%s:%s:%s:%s:%s:%s", id.String(), contentHash, semantics.AnalysisContractVersion, semantics.PromptVersion, model, effort, mode)
}

// QueueAnalysisWithProfile queues a pipeline analysis job for an article.
// A normal queue reuses the article's stored profile snapshot (a legacy
// article with a blank snapshot adopts the supplied fallback and persists it);
// a fresh run requires the caller-supplied snapshot. The snapshot is stored on
// the article in the same transaction as the job, and later settings changes
// never mutate it.
func (s *Store) QueueAnalysisWithProfile(ctx context.Context, id library.ULID, force bool, fresh bool, snapshot *pipeline.ProfileSnapshot) (*jobs.Job, error) {
	prepared, err := s.PrepareAnalysis(ctx, id)
	if err != nil {
		return nil, err
	}
	if fresh && snapshot == nil {
		return nil, &Error{Op: "queue analysis", Kind: KindValidation, Err: errors.New("a fresh pipeline run requires a resolved profile")}
	}
	var job *jobs.Job
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var profileID, profileName, snapshotJSON, snapshotHash string
		if err := tx.QueryRowContext(ctx, `SELECT analysis_profile_id, analysis_profile_name, analysis_pipeline_snapshot_json, analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, id.String()).Scan(&profileID, &profileName, &snapshotJSON, &snapshotHash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &Error{Op: "queue analysis", Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
			}
			return err
		}
		active := snapshot
		activeHash := ""
		if snapshotJSON != "" {
			// Normal retries reuse the stored snapshot exactly; a fresh run's
			// caller-supplied profile overrides it.
			if fresh && snapshot != nil {
				active = snapshot
				hashValue, hashErr := active.SnapshotHash()
				if hashErr != nil {
					return &Error{Op: "queue analysis", Kind: KindValidation, Err: hashErr}
				}
				activeHash = hashValue
			} else {
				stored, decodeErr := decodeStoredProfile(snapshotJSON)
				if decodeErr != nil {
					return &Error{Op: "queue analysis", Kind: KindConflict, Err: fmt.Errorf("stored profile snapshot is corrupt: %w", decodeErr)}
				}
				active = stored
				activeHash = snapshotHash
			}
		} else if active != nil {
			hashValue, err := active.SnapshotHash()
			if err != nil {
				return &Error{Op: "queue analysis", Kind: KindValidation, Err: err}
			}
			activeHash = hashValue
		} else {
			return &Error{Op: "queue analysis", Kind: KindConflict, Err: errors.New("article has no profile snapshot and no fallback profile was supplied")}
		}
		_ = profileID
		_ = profileName
		status, err := articleAnalysisStatusTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !force && (status == string(AnalysisQueued) || status == string(AnalysisProcessing)) {
			existing, err := jobs.GetActiveOwnerJobTx(ctx, tx, "article", id.String(), jobs.AnalysisJobType)
			if err != nil {
				return err
			}
			job = existing
			return nil
		}
		payload, err := pipeline.EncodeJobPayload(pipeline.JobPayload{
			ArticleID: id.String(), ContentHash: prepared.ContentHash,
			AnalysisContractVersion: pipeline.AnalysisContractVersion,
			PipelineVersion:         pipeline.PipelineVersion, Fresh: fresh,
			Profile: *active, ProfileSnapshotHash: activeHash,
		})
		if err != nil {
			return err
		}
		key := pipelineIdempotencyKey(id, activeHash, prepared.ContentHash, fresh, force)
		if force {
			if _, err := jobs.CancelOwnerJobsTx(ctx, tx, "article", id.String(), jobs.AnalysisJobType, "v1.analysis_superseded"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE article SET content_hash = ?, analysis_status = 'queued', analysis_error_code = '', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, prepared.ContentHash, id.String()); err != nil {
			return err
		}
		job, err = jobs.EnqueueTx(ctx, tx, jobs.Spec{
			JobType: jobs.AnalysisJobType, ExecutionTarget: jobs.TargetServer,
			OwnerType: "article", OwnerID: id.String(), IdempotencyKey: key,
			InputHash: prepared.ContentHash, PayloadJSON: string(payload), Priority: 100,
		})
		if err != nil {
			return err
		}
		if (snapshotJSON == "" || fresh) && active != nil {
			if err := persistArticleProfileSnapshotTx(ctx, tx, id, active); err != nil {
				return err
			}
		}
		return activateJobTx(ctx, tx, id, job.ID)
	})
	return job, err
}

// decodeStoredProfile rebuilds the immutable snapshot from stored JSON.
func decodeStoredProfile(raw string) (*pipeline.ProfileSnapshot, error) {
	var profile pipeline.ProfileSnapshot
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return nil, err
	}
	if _, err := profile.SnapshotHash(); err != nil {
		return nil, err
	}
	return &profile, nil
}

func articleAnalysisStatusTx(ctx context.Context, tx *sql.Tx, id library.ULID) (string, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT analysis_status FROM article WHERE id = ?`, id.String()).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return "", &Error{Op: "queue analysis", Kind: KindNotFound, Err: fmt.Errorf("%s not found", id.String())}
	} else if err != nil {
		return "", err
	}
	return status, nil
}

// MarkAnalysisProcessing transitions the article to processing only while it
// still belongs to the given durable job. A stale job that was superseded by
// an owner-requested reanalysis (article.analysis_job_id now names a newer
// job) is rejected, so a late runner can never mark the newer job's article
// processing and wedge it for every subsequent attempt.
func (s *Store) MarkAnalysisProcessing(ctx context.Context, id library.ULID, jobID library.ULID) error {
	result, err := s.db.Exec(ctx, `UPDATE article SET analysis_status = 'processing', analysis_error_code = '', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND analysis_job_id = ? AND analysis_status IN ('queued', 'needs_analysis', 'failed')`, id.String(), jobID.String())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		var status, activeJob string
		if err := s.db.QueryRow(ctx, `SELECT analysis_status, analysis_job_id FROM article WHERE id = ?`, id.String()).Scan(&status, &activeJob); errors.Is(err, sql.ErrNoRows) {
			return &Error{Op: "mark analysis processing", Kind: KindNotFound, Err: sql.ErrNoRows}
		} else if err != nil {
			return err
		}
		if status == string(AnalysisProcessing) && activeJob == jobID.String() {
			return &Error{Op: "mark analysis processing", Kind: KindInProgress, Err: errors.New("analysis is already processing")}
		}
		if activeJob != "" && activeJob != jobID.String() {
			return &Error{Op: "mark analysis processing", Kind: KindConflict, Err: errors.New("analysis job was superseded")}
		}
		return &Error{Op: "mark analysis processing", Kind: KindConflict, Err: fmt.Errorf("analysis status is %q", status)}
	}
	return nil
}

// ResetBlocksForJob reinitializes the per-block lifecycle of one job before
// a scheduler retry: earlier published paragraphs are reset to pending under
// the same job so the retry can republish them through the normal paragraph
// transaction, while published_* provenance and the accepted semantic rows
// stay intact and readable. Blocks owned by other jobs are never touched.
func (s *Store) ResetBlocksForJob(ctx context.Context, id library.ULID, jobID library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	result, err := s.db.Exec(ctx, `
		UPDATE article_block SET analysis_status = 'pending', analysis_error_code = ''
		WHERE article_id = ? AND analysis_job_id = ? AND analysis_status IN ('pending', 'processing', 'ready', 'failed')
	`, id.String(), jobID.String())
	if err != nil {
		return err
	}
	_, err = result.RowsAffected()
	return err
}

// MarkBlockProcessing claims one paragraph for the active analysis job after
// the runner verified its lease. A block owned by a different (superseded)
// job can never be marked or published.
func (s *Store) MarkBlockProcessing(ctx context.Context, id library.ULID, blockIndex int, jobID library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var exists int
		var jobIDValue string
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(analysis_job_id), '') FROM article WHERE id = ?`, id.String()).Scan(&exists, &jobIDValue); err != nil {
			return err
		}
		if exists == 0 {
			return &Error{Op: "mark block processing", Kind: KindNotFound, Err: sql.ErrNoRows}
		}
		if jobIDValue != jobID.String() {
			return &Error{Op: "mark block processing", Kind: KindConflict, Err: errors.New("analysis job was superseded")}
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE article_block SET analysis_status = 'processing', analysis_error_code = ''
			WHERE article_id = ? AND block_index = ? AND analysis_job_id = ? AND analysis_status = 'pending'
		`, id.String(), blockIndex, jobID.String())
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			var blockExists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM article_block WHERE article_id = ? AND block_index = ?`, id.String(), blockIndex).Scan(&blockExists); err != nil {
				return err
			}
			if blockExists == 0 {
				return &Error{Op: "mark block processing", Kind: KindNotFound, Err: fmt.Errorf("article block %d not found", blockIndex)}
			}
			return &Error{Op: "mark block processing", Kind: KindConflict, Err: fmt.Errorf("article block %d is not pending for the active job", blockIndex)}
		}
		return nil
	})
}

// FailBlockForJob marks one paragraph failed under its owning job without
// touching published materializations or other blocks.
func (s *Store) FailBlockForJob(ctx context.Context, id library.ULID, blockIndex int, jobID library.ULID, code string) error {
	if !validErrorCode(code) {
		return &Error{Op: "fail analysis block", Kind: KindValidation, Err: errors.New("invalid analysis error code")}
	}
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	result, err := s.db.Exec(ctx, `
		UPDATE article_block SET analysis_status = 'failed', analysis_error_code = ?
		WHERE article_id = ? AND block_index = ? AND analysis_job_id = ?
	`, code, id.String(), blockIndex, jobID.String())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return &Error{Op: "fail analysis block", Kind: KindNotFound, Err: fmt.Errorf("article block %d is not owned by the active job", blockIndex)}
	}
	return nil
}

func (s *Store) MarkAnalysisFailed(ctx context.Context, id library.ULID, code string) error {
	if !validErrorCode(code) {
		return &Error{Op: "mark analysis failed", Kind: KindValidation, Err: errors.New("invalid analysis error code")}
	}
	result, err := s.db.Exec(ctx, `UPDATE article SET analysis_status = 'failed', analysis_error_code = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, code, id.String())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return &Error{Op: "mark analysis failed", Kind: KindNotFound, Err: sql.ErrNoRows}
	}
	return nil
}

// MarkAnalysisFailedForJob marks the article failed only when the given job
// is still the active analysis job, so a superseded run can never overwrite
// the state of a newer run. The legacy MarkAnalysisFailed above remains for
// synchronous single-flight paths that own the article outright.
func (s *Store) MarkAnalysisFailedForJob(ctx context.Context, id library.ULID, jobID library.ULID, code string) error {
	if !validErrorCode(code) {
		return &Error{Op: "mark analysis failed", Kind: KindValidation, Err: errors.New("invalid analysis error code")}
	}
	result, err := s.db.Exec(ctx, `UPDATE article SET analysis_status = 'failed', analysis_error_code = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND analysis_job_id = ?`, code, id.String(), jobID.String())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		var exists int
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM article WHERE id = ?`, id.String()).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &Error{Op: "mark analysis failed", Kind: KindNotFound, Err: sql.ErrNoRows}
		}
		return &Error{Op: "mark analysis failed", Kind: KindConflict, Err: errors.New("analysis job was superseded")}
	}
	return nil
}

// MarkAnalysisReady marks the article ready after the final whole-response
// consistency audit succeeded. Only the active job may perform the transition.
func (s *Store) MarkAnalysisReady(ctx context.Context, id library.ULID, jobID library.ULID, model, effort string) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	result, err := s.db.Exec(ctx, `UPDATE article SET analysis_status = 'ready', analysis_revision = ?, analysis_error_code = '', analysis_model = ?, analysis_effort = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND analysis_job_id = ?`, semantics.AnalysisContractVersion, model, effort, id.String(), jobID.String())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		var exists int
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM article WHERE id = ?`, id.String()).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &Error{Op: "mark analysis ready", Kind: KindNotFound, Err: sql.ErrNoRows}
		}
		return &Error{Op: "mark analysis ready", Kind: KindConflict, Err: errors.New("analysis job was superseded")}
	}
	return nil
}

// PersistAnalysis atomically replaces only v2 materialized rows. Existing
// accepted rows remain visible if validation or any insert fails because the
// entire operation is one transaction.
func (s *Store) PersistAnalysis(ctx context.Context, id library.ULID, prepared semantics.PreparedArticle, validated semantics.ValidatedResponse, providerID, providerModel string, provenance ...string) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	providerEffort := "medium"
	requestedModel := providerModel
	if len(provenance) > 0 && provenance[0] != "" {
		providerEffort = provenance[0]
	}
	if len(provenance) > 1 && provenance[1] != "" {
		requestedModel = provenance[1]
	}
	if len(provenance) > 2 && provenance[2] != "" {
		providerModel = provenance[2]
	}
	if requestedModel == "" {
		requestedModel = providerModel
	}
	responseJSON, err := json.Marshal(validated.Response)
	if err != nil {
		return err
	}
	responseHash := sha256Hex(responseJSON)
	var cleanupDigests []string
	persistErr := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var sourceLanguage, targetLanguage, contentHash string
		if err := tx.QueryRowContext(ctx, `SELECT source_language, target_language, content_hash FROM article WHERE id = ?`, id.String()).Scan(&sourceLanguage, &targetLanguage, &contentHash); errors.Is(err, sql.ErrNoRows) {
			return &Error{Op: "persist analysis", Kind: KindNotFound, Err: sql.ErrNoRows}
		} else if err != nil {
			return err
		}
		if contentHash != prepared.ContentHash {
			return &Error{Op: "persist analysis", Kind: KindConflict, Err: errors.New("article content changed while analysis was running")}
		}
		blockByIndex, err := articleBlocksByIndex(ctx, tx, id)
		if err != nil {
			return err
		}
		oldNarrationRenderIDs, err := articleNarrationRenderIDsTx(ctx, tx, id.String())
		if err != nil {
			return err
		}

		senseByID := make(map[string]*semantics.Sense)
		for _, candidate := range prepared.Candidates {
			senseByID[candidate.ID] = nil
		}
		for _, result := range validated.Tokens {
			if result.Result.SemanticSenseID != "" {
				if _, ok := senseByID[result.Result.SemanticSenseID]; !ok {
					return &Error{Op: "persist analysis", Kind: KindValidation, Err: fmt.Errorf("sense %q was not supplied", result.Result.SemanticSenseID)}
				}
			}
		}
		newByRef := make(map[string]*semantics.Sense)
		for _, proposal := range validated.Response.NewSenses {
			sense, err := semantics.EnsureSenseTx(ctx, tx, sourceLanguage, targetLanguage, proposal, providerID, requestedModel)
			if err != nil {
				return &Error{Op: "persist analysis", Kind: KindValidation, Err: err}
			}
			newByRef[proposal.Ref] = sense
		}
		resolveSense := func(idValue, ref string, kind semantics.Kind) (*semantics.Sense, error) {
			if idValue != "" {
				_, ok := findCandidate(prepared.Candidates, idValue)
				if !ok {
					return nil, errors.New("existing sense was not supplied")
				}
				return semantics.EnsureExistingSenseTx(ctx, tx, idValue, sourceLanguage, targetLanguage, kind)
			}
			if ref != "" {
				if sense := newByRef[ref]; sense != nil {
					return sense, nil
				}
				return nil, fmt.Errorf("new sense ref %q was not materialized", ref)
			}
			return nil, nil
		}
		// Remove article-owned rows only after all provider references have
		// passed validation. Cascades remove spans; semantic senses remain shared.
		if _, err := tx.ExecContext(ctx, `DELETE FROM article_occurrence WHERE article_block_id IN (SELECT id FROM article_block WHERE article_id = ?)`, id.String()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM article_sentence WHERE article_block_id IN (SELECT id FROM article_block WHERE article_id = ?)`, id.String()); err != nil {
			return err
		}

		sentenceIDs := make([]string, len(validated.Sentences))
		sentenceByBlock := make(map[int][]articleSentenceSpan)
		for index, sentence := range validated.Sentences {
			block := blockByIndex[sentence.Span.BlockIndex]
			if block.id.IsZero() {
				return fmt.Errorf("sentence references unknown block %d", sentence.Span.BlockIndex)
			}
			idValue := library.NewULID().String()
			sentenceIDs[index] = idValue
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES (?, ?, ?, ?, ?, ?, ?)`, idValue, block.id.String(), sentenceIndex(sentence.Span, sentenceByBlock[sentence.Span.BlockIndex]), sentence.Span.StartUTF16, sentence.Span.EndUTF16, sentence.Span.SourceText, sha256Hex([]byte(sentence.Span.SourceText))); err != nil {
				return err
			}
			sentenceByBlock[sentence.Span.BlockIndex] = append(sentenceByBlock[sentence.Span.BlockIndex], articleSentenceSpan{start: sentence.Span.StartUTF16, end: sentence.Span.EndUTF16, id: idValue})
		}
		findSentence := func(span semantics.ResolvedSpan) string {
			for _, candidate := range sentenceByBlock[span.BlockIndex] {
				if span.StartUTF16 >= candidate.start && span.EndUTF16 <= candidate.end {
					return candidate.id
				}
			}
			return ""
		}
		contiguousTokens := make(map[string]struct{})
		for _, construction := range validated.Constructions {
			if construction.Construction.Role == "contiguous_construction" {
				for _, tokenID := range construction.Construction.TokenIDs {
					contiguousTokens[tokenID] = struct{}{}
				}
			}
		}
		tokenByID := make(map[string]semantics.ResolvedToken, len(validated.Tokens))
		for _, token := range validated.Tokens {
			tokenByID[token.Token.ID] = token
		}
		for _, token := range validated.Tokens {
			result := token.Result
			sense, err := resolveSense(result.SemanticSenseID, result.NewSenseRef, result.Kind)
			if err != nil {
				return &Error{Op: "persist analysis", Kind: KindValidation, Err: err}
			}
			shadowPolicy := ShadowToken
			if result.ShadowText == "" || result.Classification == "proper_name" || result.Classification == "number" || result.Classification == "acronym" || result.Classification == "unchanged" {
				shadowPolicy = ShadowNone
			}
			if _, ok := contiguousTokens[token.Token.ID]; ok {
				shadowPolicy = ShadowNone
			}
			occurrenceID := library.NewULID().String()
			var sentenceID any
			if value := findSentence(semantics.ResolvedSpan{BlockIndex: token.Token.BlockIndex, StartUTF16: token.Token.StartUTF16, EndUTF16: token.Token.EndUTF16, SourceText: token.Token.SourceText}); value != "" {
				sentenceID = value
			}
			var senseID any
			if sense != nil {
				senseID = sense.ID.String()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, semantic_sense_id, kind, role, shadow_policy, shadow_text, canonical_pronunciation_text, context_pronunciation_key, confidence_milli) VALUES (?, ?, ?, ?, ?, 'token', ?, ?, ?, ?, ?)`, occurrenceID, blockByIndex[token.Token.BlockIndex].id.String(), sentenceID, senseID, result.Kind, shadowPolicy, result.ShadowText, result.CanonicalPronunciation, result.ContextPronunciationKey, result.ConfidenceMilli); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, 0, ?, ?, ?)`, library.NewULID().String(), occurrenceID, token.Token.StartUTF16, token.Token.EndUTF16, token.Token.SourceText); err != nil {
				return err
			}
		}
		for _, construction := range validated.Constructions {
			value := construction.Construction
			sense, err := resolveSense(value.SemanticSenseID, value.NewSenseRef, value.Kind)
			if err != nil {
				return &Error{Op: "persist analysis", Kind: KindValidation, Err: err}
			}
			if sense == nil {
				return errors.New("construction has no semantic sense")
			}
			occurrenceID := library.NewULID().String()
			var sentenceID any
			if len(construction.Spans) > 0 {
				if value := findSentence(construction.Spans[0]); value != "" {
					sentenceID = value
				}
			}
			policy := ShadowGroup
			if value.Role == "discontinuous_construction" {
				policy = ShadowMarker
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, semantic_sense_id, kind, role, shadow_policy, shadow_text, canonical_pronunciation_text, context_pronunciation_key, confidence_milli) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, occurrenceID, blockByIndex[construction.Spans[0].BlockIndex].id.String(), sentenceID, sense.ID.String(), value.Kind, value.Role, policy, value.ShadowText, value.CanonicalPronunciationText, value.ContextPronunciationKey, value.ConfidenceMilli); err != nil {
				return err
			}
			for spanIndex, span := range construction.Spans {
				block := blockByIndex[span.BlockIndex]
				if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, ?, ?, ?, ?)`, library.NewULID().String(), occurrenceID, spanIndex, span.StartUTF16, span.EndUTF16, span.SourceText); err != nil {
					return err
				}
				_ = block
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO analysis_cache (
				id, content_hash, source_language, target_language, contract_version,
				provider_id, provider_model, provider_effort, prompt_version,
				prepared_input_hash, validated_response_json, response_hash
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(prepared_input_hash, contract_version, prompt_version, provider_model, provider_effort)
		WHERE prepared_input_hash <> ''
		DO UPDATE SET provider_id = excluded.provider_id, validated_response_json = excluded.validated_response_json,
				response_hash = excluded.response_hash, created_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		`, library.NewULID().String(), prepared.ContentHash, sourceLanguage, targetLanguage,
			semantics.AnalysisContractVersion, providerID, requestedModel, providerEffort,
			semantics.PromptVersion, semantics.PreparedInputHash(prepared), string(responseJSON), responseHash); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE article SET analysis_status = 'ready', analysis_revision = ?, analysis_error_code = '', analysis_model = ?, analysis_effort = ?, sentence_revision = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, semantics.AnalysisContractVersion, requestedModel, providerEffort, SentenceRevisionLegacyAnalysis, id.String()); err != nil {
			return err
		}
		if err := speech.QueueArticleAudioTx(ctx, tx, id, true); err != nil {
			return err
		}
		cleanupDigests, err = retireUnboundNarrationRendersTx(ctx, tx, oldNarrationRenderIDs)
		return err
	})
	if persistErr != nil {
		return persistErr
	}
	if s.media != nil {
		for _, digest := range cleanupDigests {
			// Analysis is already durably accepted. A failed best-effort file
			// cleanup remains recoverable by the normal media startup sweep and
			// must not turn a successful provider result into a failed analysis.
			_, _ = s.media.CleanupOrphan(ctx, s.db, digest)
		}
	}
	return nil
}

func articleNarrationRenderIDsTx(ctx context.Context, tx *sql.Tx, articleID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT a.audio_render_id
		FROM article_sentence_audio a JOIN audio_render r ON r.id = a.audio_render_id
		WHERE a.article_id = ? AND r.retention_class = 'article_narration'
		ORDER BY a.audio_render_id
	`, articleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func retireUnboundNarrationRendersTx(ctx context.Context, tx *sql.Tx, renderIDs []string) ([]string, error) {
	var cleanupDigests []string
	for _, renderID := range renderIDs {
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM article_sentence_audio WHERE audio_render_id = ?`, renderID).Scan(&references); err != nil {
			return nil, err
		}
		if references != 0 {
			continue
		}
		if _, err := jobs.CancelOwnerJobsTx(ctx, tx, "audio_render", renderID, jobs.ChatterboxJobType, "v1.article_reanalyzed"); err != nil {
			return nil, err
		}
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = ?), '')`, renderID).Scan(&digest); err != nil {
			return nil, err
		}
		if digest != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM audio_blob_reference WHERE audio_render_id = ?`, renderID); err != nil {
				return nil, err
			}
			cleanupDigests = append(cleanupDigests, digest)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE audio_render SET state = 'purged', error_code = '', updated_at = ? WHERE id = ? AND retention_class = 'article_narration'`, store.NowUTC(), renderID); err != nil {
			return nil, err
		}
	}
	return cleanupDigests, nil
}

type articleSentenceSpan struct {
	start, end int
	id         string
}

func sentenceIndex(_ semantics.ResolvedSpan, previous []articleSentenceSpan) int {
	return len(previous)
}

func findCandidate(candidates []semantics.SenseCandidate, id string) (semantics.SenseCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return semantics.SenseCandidate{}, false
}

func articleBlocksByIndex(ctx context.Context, tx *sql.Tx, id library.ULID) (map[int]blockRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT block_index, id, source_text FROM article_block WHERE article_id = ? ORDER BY block_index`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int]blockRecord)
	for rows.Next() {
		var index int
		var rawID, text string
		if err := rows.Scan(&index, &rawID, &text); err != nil {
			return nil, err
		}
		result[index] = blockRecord{id: library.ULID(rawID), text: text}
	}
	return result, rows.Err()
}

func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// loadV2Tx fills additive semantic-reader fields while leaving legacy
// annotations untouched. It is called by the existing GetArticle transaction.
func (s *Store) loadV2Tx(ctx context.Context, tx *sql.Tx, id library.ULID, article *Article, blockByID map[string]int) error {
	var profileID, profileName, snapshotHash string
	if err := tx.QueryRowContext(ctx, `SELECT content_hash, analysis_status, analysis_revision, analysis_error_code, analysis_model, analysis_effort, analysis_job_id, narration_status, narration_error_code, analysis_profile_id, analysis_profile_name, analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, id.String()).Scan(&article.ContentHash, &article.AnalysisStatus, &article.AnalysisRevision, &article.AnalysisErrorCode, &article.AnalysisModel, &article.AnalysisEffort, &article.AnalysisJobID, &article.NarrationStatus, &article.NarrationErrorCode, &profileID, &profileName, &snapshotHash); err != nil {
		return fmt.Errorf("reader load article lifecycle: %w", err)
	}
	if profileID != "" {
		article.Pipeline = &ArticlePipelineProvenance{
			ProfileID: profileID, ProfileName: profileName, SnapshotHash: snapshotHash,
		}
	}
	article.Sentences = make([]ArticleSentence, 0)
	article.Occurrences = make([]ArticleOccurrence, 0)
	for index := range article.Blocks {
		article.Blocks[index].Sentences = make([]ArticleSentence, 0)
		article.Blocks[index].Occurrences = make([]ArticleOccurrence, 0)
	}
	// Per-block current/progress state depends on the article's active job,
	// which is only known after the lifecycle query above.
	article.AnalysisProgress = AnalysisProgress{TotalParagraphs: len(article.Blocks), CurrentBlockIndex: -1, FailedBlockIndex: -1}
	for index := range article.Blocks {
		block := &article.Blocks[index]
		block.AnalysisIsCurrent = block.publishedJobID == article.AnalysisJobID
		switch {
		case block.AnalysisStatus == BlockReady && block.analysisJobID == article.AnalysisJobID:
			article.AnalysisProgress.CompletedParagraphs++
		case block.AnalysisStatus == BlockFailed && block.analysisJobID == article.AnalysisJobID && article.AnalysisProgress.FailedBlockIndex < 0:
			article.AnalysisProgress.FailedBlockIndex = index
		}
		if block.analysisJobID != article.AnalysisJobID {
			continue
		}
		if block.AnalysisStatus == BlockProcessing && article.AnalysisProgress.CurrentBlockIndex < 0 {
			article.AnalysisProgress.CurrentBlockIndex = index
		}
		if block.AnalysisStatus == BlockPending && article.AnalysisProgress.CurrentBlockIndex < 0 && (article.AnalysisStatus == AnalysisProcessing || article.AnalysisStatus == AnalysisQueued) {
			article.AnalysisProgress.CurrentBlockIndex = index
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.article_block_id, s.sentence_index, s.start_utf16, s.end_utf16, s.source_text, s.source_hash FROM article_sentence s JOIN article_block b ON b.id = s.article_block_id WHERE b.article_id = ? ORDER BY b.block_index, s.sentence_index`, id.String())
	if err != nil {
		return err
	}
	for rows.Next() {
		var sentence ArticleSentence
		if err := rows.Scan(&sentence.ID, &sentence.ArticleBlockID, &sentence.SentenceIndex, &sentence.StartUTF16, &sentence.EndUTF16, &sentence.SourceText, &sentence.SourceHash); err != nil {
			rows.Close()
			return err
		}
		article.Sentences = append(article.Sentences, sentence)
		if index, ok := blockByID[sentence.ArticleBlockID.String()]; ok {
			article.Blocks[index].Sentences = append(article.Blocks[index].Sentences, sentence)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// Decode occurrences with explicit destinations so legacy NULL semantic
	// references remain safe while v2 rows expose their full sense details. Keep
	// the SQL rows closed before loading related senses and learning state: the
	// SQLite driver may use the transaction's only connection for those queries.
	type occurrenceRow struct {
		occurrence ArticleOccurrence
		blockID    string
	}
	occurrenceRows := make([]occurrenceRow, 0)
	rows, err = tx.QueryContext(ctx, `SELECT o.id, o.article_block_id, o.article_sentence_id, o.semantic_sense_id, o.kind, o.role, o.shadow_policy, o.shadow_text, o.canonical_pronunciation_text, o.context_pronunciation_key, o.confidence_milli FROM article_occurrence o JOIN article_block b ON b.id = o.article_block_id WHERE b.article_id = ? ORDER BY b.block_index, (SELECT MIN(start_utf16) FROM article_occurrence_span sp WHERE sp.article_occurrence_id = o.id), o.id`, id.String())
	if err != nil {
		return err
	}
	for rows.Next() {
		var occurrence ArticleOccurrence
		var blockID string
		var sentenceID, senseID sql.NullString
		if err := rows.Scan(&occurrence.ID, &blockID, &sentenceID, &senseID, &occurrence.Kind, &occurrence.Role, &occurrence.ShadowPolicy, &occurrence.ShadowText, &occurrence.CanonicalPronunciationText, &occurrence.ContextPronunciationKey, &occurrence.ConfidenceMilli); err != nil {
			rows.Close()
			return err
		}
		occurrence.ArticleBlockID = library.ULID(blockID)
		if sentenceID.Valid {
			value := library.ULID(sentenceID.String)
			occurrence.ArticleSentenceID = &value
		}
		if senseID.Valid {
			value := library.ULID(senseID.String)
			occurrence.SemanticSenseID = &value
		}
		occurrence.Spans = make([]ArticleOccurrenceSpan, 0)
		occurrence.SubtitleSuppressionReason = SubtitleNone
		occurrence.MemberOccurrenceIDs = []string{}
		occurrenceRows = append(occurrenceRows, occurrenceRow{occurrence: occurrence, blockID: blockID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// Exact contiguous-construction members never display their own subtitle;
	// membership rows are v3-only and are never inferred from legacy spans.
	contiguousMembers := make(map[string]struct{})
	memberRows, err := tx.QueryContext(ctx, `
		SELECT m.token_occurrence_id
		FROM article_construction_member m
		JOIN article_occurrence c ON c.id = m.construction_occurrence_id
		JOIN article_block b ON b.id = c.article_block_id
		WHERE b.article_id = ? AND c.role = 'contiguous_construction'
	`, id.String())
	if err != nil {
		return err
	}
	for memberRows.Next() {
		var tokenOccurrenceID string
		if err := memberRows.Scan(&tokenOccurrenceID); err != nil {
			memberRows.Close()
			return err
		}
		contiguousMembers[tokenOccurrenceID] = struct{}{}
	}
	if err := memberRows.Err(); err != nil {
		memberRows.Close()
		return err
	}
	memberRows.Close()
	// Exact ordered membership for construction occurrences: tokens list the
	// construction that owns them above; constructions list their members.
	memberOfConstruction := make(map[string][]string)
	memberRows, err = tx.QueryContext(ctx, `
		SELECT m.construction_occurrence_id, m.token_occurrence_id
		FROM article_construction_member m
		JOIN article_occurrence c ON c.id = m.construction_occurrence_id
		JOIN article_block b ON b.id = c.article_block_id
		WHERE b.article_id = ?
		ORDER BY m.construction_occurrence_id, m.member_index
	`, id.String())
	if err != nil {
		return err
	}
	for memberRows.Next() {
		var constructionID, tokenID string
		if err := memberRows.Scan(&constructionID, &tokenID); err != nil {
			memberRows.Close()
			return err
		}
		memberOfConstruction[constructionID] = append(memberOfConstruction[constructionID], tokenID)
	}
	if err := memberRows.Err(); err != nil {
		memberRows.Close()
		return err
	}
	memberRows.Close()
	occurrencesByID := make(map[string]int, len(occurrenceRows))
	for _, row := range occurrenceRows {
		occurrence := row.occurrence
		if occurrence.SemanticSenseID != nil {
			sense, err := loadSemanticSenseTx(ctx, tx, occurrence.SemanticSenseID.String())
			if err != nil {
				return err
			}
			occurrence.Sense = sense
			var status string
			var updated string
			err = tx.QueryRowContext(ctx, `SELECT status, updated_at FROM semantic_learning_state WHERE semantic_sense_id = ?`, occurrence.SemanticSenseID.String()).Scan(&status, &updated)
			if err == nil {
				occurrence.LearningState = &SemanticLearningState{SemanticSenseID: *occurrence.SemanticSenseID, Status: LearningStatus(status), UpdatedAt: updated}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		article.Occurrences = append(article.Occurrences, occurrence)
		occurrencesByID[occurrence.ID.String()] = len(article.Occurrences) - 1
		if index, ok := blockByID[row.blockID]; ok {
			article.Blocks[index].Occurrences = append(article.Blocks[index].Occurrences, occurrence)
		}
	}
	spanRows, err := tx.QueryContext(ctx, `SELECT sp.id, sp.article_occurrence_id, sp.span_index, sp.start_utf16, sp.end_utf16, sp.source_text FROM article_occurrence_span sp JOIN article_occurrence o ON o.id = sp.article_occurrence_id JOIN article_block b ON b.id = o.article_block_id WHERE b.article_id = ? ORDER BY sp.article_occurrence_id, sp.span_index`, id.String())
	if err != nil {
		return err
	}
	for spanRows.Next() {
		var span ArticleOccurrenceSpan
		if err := spanRows.Scan(&span.ID, &span.ArticleOccurrenceID, &span.SpanIndex, &span.StartUTF16, &span.EndUTF16, &span.SourceText); err != nil {
			spanRows.Close()
			return err
		}
		if occurrenceIndex, ok := occurrencesByID[span.ArticleOccurrenceID.String()]; ok {
			article.Occurrences[occurrenceIndex].Spans = append(article.Occurrences[occurrenceIndex].Spans, span)
			for blockIndex := range article.Blocks {
				for occurrenceIndex := range article.Blocks[blockIndex].Occurrences {
					if article.Blocks[blockIndex].Occurrences[occurrenceIndex].ID == span.ArticleOccurrenceID {
						article.Blocks[blockIndex].Occurrences[occurrenceIndex].Spans = append(article.Blocks[blockIndex].Occurrences[occurrenceIndex].Spans, span)
					}
				}
			}
		}
	}
	if err := spanRows.Err(); err != nil {
		spanRows.Close()
		return err
	}
	spanRows.Close()
	// Effective subtitles and suppression reasons depend on spans, senses,
	// learning state, and exact membership, so they are computed once all
	// related rows are attached. Both copies (article-level and block-level)
	// receive the same derived values.
	for index := range article.Occurrences {
		finishOccurrenceDisplay(&article.Occurrences[index], contiguousMembers)
		article.Occurrences[index].MemberOccurrenceIDs = memberOfConstruction[article.Occurrences[index].ID.String()]
	}
	for blockIndex := range article.Blocks {
		for occurrenceIndex := range article.Blocks[blockIndex].Occurrences {
			finishOccurrenceDisplay(&article.Blocks[blockIndex].Occurrences[occurrenceIndex], contiguousMembers)
			article.Blocks[blockIndex].Occurrences[occurrenceIndex].MemberOccurrenceIDs = memberOfConstruction[article.Blocks[blockIndex].Occurrences[occurrenceIndex].ID.String()]
		}
	}
	attachAudio := func(renderID, state, errorCode string, duration, size int64) *AudioRef {
		return &AudioRef{RenderID: library.ULID(renderID), URL: "/api/v1/audio/" + renderID, Ready: state == speech.RenderReady, DurationMS: duration, SizeBytes: size, ErrorCode: errorCode}
	}
	sentenceAudio := make(map[string]*AudioRef)
	audioRows, err := tx.QueryContext(ctx, `
		SELECT a.article_sentence_id, r.id, r.state, r.duration_ms, r.size_bytes, r.error_code
		FROM article_sentence_audio a JOIN audio_render r ON r.id = a.audio_render_id
		WHERE a.article_id = ? ORDER BY a.sequence_index
	`, id.String())
	if err != nil {
		return err
	}
	for audioRows.Next() {
		var sentenceID, renderID, state, errorCode string
		var duration, size int64
		if err := audioRows.Scan(&sentenceID, &renderID, &state, &duration, &size, &errorCode); err != nil {
			audioRows.Close()
			return err
		}
		sentenceAudio[sentenceID] = attachAudio(renderID, state, errorCode, duration, size)
	}
	if err := audioRows.Err(); err != nil {
		audioRows.Close()
		return err
	}
	audioRows.Close()
	for index := range article.Sentences {
		article.Sentences[index].Audio = sentenceAudio[article.Sentences[index].ID.String()]
	}
	for blockIndex := range article.Blocks {
		for sentenceIndex := range article.Blocks[blockIndex].Sentences {
			sentence := &article.Blocks[blockIndex].Sentences[sentenceIndex]
			sentence.Audio = sentenceAudio[sentence.ID.String()]
		}
	}
	occurrenceAudio := make(map[string]*AudioRef)
	audioRows, err = tx.QueryContext(ctx, `
		SELECT a.article_occurrence_id, r.id, r.state, r.duration_ms, r.size_bytes, r.error_code
		FROM article_occurrence_audio a JOIN audio_render r ON r.id = a.audio_render_id
		JOIN article_occurrence o ON o.id = a.article_occurrence_id
		JOIN article_block b ON b.id = o.article_block_id
		WHERE b.article_id = ? AND a.purpose = 'pronunciation' AND a.preferred = 1
		ORDER BY a.article_occurrence_id, r.id
	`, id.String())
	if err != nil {
		return err
	}
	for audioRows.Next() {
		var occurrenceID, renderID, state, errorCode string
		var duration, size int64
		if err := audioRows.Scan(&occurrenceID, &renderID, &state, &duration, &size, &errorCode); err != nil {
			audioRows.Close()
			return err
		}
		if _, exists := occurrenceAudio[occurrenceID]; !exists {
			occurrenceAudio[occurrenceID] = attachAudio(renderID, state, errorCode, duration, size)
		}
	}
	if err := audioRows.Err(); err != nil {
		audioRows.Close()
		return err
	}
	audioRows.Close()
	for index := range article.Occurrences {
		article.Occurrences[index].Pronunciation = occurrenceAudio[article.Occurrences[index].ID.String()]
	}
	for blockIndex := range article.Blocks {
		for occurrenceIndex := range article.Blocks[blockIndex].Occurrences {
			occurrence := &article.Blocks[blockIndex].Occurrences[occurrenceIndex]
			occurrence.Pronunciation = occurrenceAudio[occurrence.ID.String()]
		}
	}
	var sentenceCount, readyCount int
	var duration, size int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN r.state = 'ready' THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN r.state = 'ready' THEN r.duration_ms ELSE 0 END), 0), COALESCE(SUM(CASE WHEN r.state = 'ready' THEN r.size_bytes ELSE 0 END), 0) FROM article_sentence s LEFT JOIN article_sentence_audio a ON a.article_sentence_id = s.id LEFT JOIN audio_render r ON r.id = a.audio_render_id JOIN article_block b ON b.id = s.article_block_id WHERE b.article_id = ?`, id.String()).Scan(&sentenceCount, &readyCount, &duration, &size); err != nil {
		return err
	}
	var reclaimable int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(refs.size_bytes), 0)
		FROM (
			SELECT DISTINCT a.audio_render_id, r.size_bytes
			FROM article_sentence_audio a JOIN audio_render r ON r.id = a.audio_render_id
			WHERE a.article_id = ? AND r.retention_class = 'article_narration' AND r.state = 'ready'
			  AND NOT EXISTS (
				  SELECT 1 FROM article_sentence_audio other
				  WHERE other.audio_render_id = a.audio_render_id AND other.article_id <> ?
			  )
		) refs
	`, id.String(), id.String()).Scan(&reclaimable); err != nil {
		return err
	}
	article.Narration = NarrationSummary{Status: article.NarrationStatus, ErrorCode: article.NarrationErrorCode, SentenceCount: sentenceCount, ReadyCount: readyCount, DurationMS: duration, SizeBytes: size, ReclaimableBytes: reclaimable}
	return nil
}

// finishOccurrenceDisplay derives the effective subtitle, the explicit
// suppression reason, and show_shadow for one occurrence. The effective
// subtitle is shadow_text with a fallback to the referenced sense's primary
// translation. A token without a sense whose subtitle normalizes exactly to
// its own source text carries no effective subtitle at all: a source copy is
// never a translation.
func finishOccurrenceDisplay(occurrence *ArticleOccurrence, contiguousMembers map[string]struct{}) {
	effective := occurrence.ShadowText
	if effective == "" && occurrence.Sense != nil {
		effective = occurrence.Sense.PrimaryTranslation
	}
	if occurrence.Role == OccurrenceToken && occurrence.Sense == nil && effective != "" && len(occurrence.Spans) > 0 {
		subtitleNormalized, subtitleErr := semantics.NormalizeForm(effective)
		sourceNormalized, sourceErr := semantics.NormalizeForm(occurrence.Spans[0].SourceText)
		if subtitleErr == nil && sourceErr == nil && subtitleNormalized == sourceNormalized {
			effective = ""
		}
	}
	occurrence.ShadowText = effective
	reason := SubtitleNone
	switch {
	case occurrence.Role == OccurrenceToken && effective == "":
		reason = SubtitleSpecialToken
	}
	if _, member := contiguousMembers[occurrence.ID.String()]; member {
		reason = SubtitleContiguousGroupMember
	}
	occurrence.SubtitleSuppressionReason = reason
	unlearned := occurrence.LearningState == nil || occurrence.LearningState.Status != LearningStatusLearned
	occurrence.ShowShadow = unlearned && reason == SubtitleNone && effective != ""
}

func loadSemanticSenseTx(ctx context.Context, tx *sql.Tx, id string) (*SemanticSense, error) {
	var sense SemanticSense
	var kind, alternatives string
	err := tx.QueryRowContext(ctx, `SELECT s.id, s.semantic_item_id, i.kind, i.canonical_form, s.sense_discriminator, s.primary_translation, s.alternatives_json, s.literal_translation, s.meaning_note, s.usage_note, s.parts_note, s.canonical_pronunciation_text FROM semantic_sense s JOIN semantic_item i ON i.id = s.semantic_item_id WHERE s.id = ?`, id).Scan(&sense.ID, &sense.SemanticItemID, &kind, &sense.CanonicalForm, &sense.SenseDiscriminator, &sense.PrimaryTranslation, &alternatives, &sense.LiteralTranslation, &sense.MeaningNote, &sense.UsageNote, &sense.PartsNote, &sense.CanonicalPronunciationText)
	if err != nil {
		return nil, err
	}
	sense.Kind = AnnotationKind(kind)
	if err := json.Unmarshal([]byte(alternatives), &sense.Alternatives); err != nil {
		return nil, err
	}
	if sense.Alternatives == nil {
		sense.Alternatives = []string{}
	}
	return &sense, nil
}

// UpsertSemanticLearningState changes only one server-owned sense. An optional
// occurrence ID is checked against that sense to prevent cross-article writes.
func (s *Store) UpsertSemanticLearningState(ctx context.Context, senseID library.ULID, status LearningStatus, occurrenceID library.ULID) (*SemanticLearningState, error) {
	if status != LearningStatusLearned && status != LearningStatusUnlearned {
		return nil, &Error{Op: "upsert semantic learning state", Kind: KindValidation, Err: errors.New("invalid learning status")}
	}
	var state SemanticLearningState
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM semantic_sense WHERE id = ?`, senseID.String()).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &Error{Op: "upsert semantic learning state", Kind: KindNotFound, Err: sql.ErrNoRows}
		}
		if !occurrenceID.IsZero() {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM article_occurrence WHERE id = ? AND semantic_sense_id = ?`, occurrenceID.String(), senseID.String()).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return &Error{Op: "upsert semantic learning state", Kind: KindConflict, Err: errors.New("occurrence does not reference sense")}
			}
		}
		now := store.NowUTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_learning_state (semantic_sense_id, status, updated_at) VALUES (?, ?, ?) ON CONFLICT(semantic_sense_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`, senseID.String(), status, now); err != nil {
			return err
		}
		state = SemanticLearningState{SemanticSenseID: senseID, Status: status, UpdatedAt: now}
		return nil
	})
	return &state, err
}

// HasPipelineSnapshot reports whether the article carries an immutable
// pipeline profile snapshot (created or last queued through the pipeline).
func (s *Store) HasPipelineSnapshot(ctx context.Context, id library.ULID) (bool, error) {
	var hash string
	if err := s.db.QueryRow(ctx, `SELECT analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, id.String()).Scan(&hash); errors.Is(err, sql.ErrNoRows) {
		return false, &Error{Op: "article pipeline snapshot", Kind: KindNotFound, Err: sql.ErrNoRows}
	} else if err != nil {
		return false, err
	}
	return hash != "", nil
}

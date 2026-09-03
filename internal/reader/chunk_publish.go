package reader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

// activateJobTx records a durable job as the article's active analysis job and
// resets every block's current lifecycle to that job and pending. Published
// materializations and their published_* provenance are preserved so the last
// accepted semantics stay visible until each block is replaced. Superseded
// jobs are canceled by the caller exactly as before; a force/fresh request
// therefore cannot publish a late paragraph through an old job id.
func activateJobTx(ctx context.Context, tx *sql.Tx, id library.ULID, jobID library.ULID) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE article SET analysis_job_id = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
	`, jobID.String(), id.String()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE article_block
		SET analysis_job_id = ?, analysis_status = 'pending', analysis_error_code = ''
		WHERE article_id = ?
	`, jobID.String(), id.String()); err != nil {
		return err
	}
	return nil
}

// PersistAnalysisChunk durably publishes one validated paragraph under the
// active analysis job. In one transaction it:
//
//  1. verifies the article content hash, the active analysis job id, and the
//     block's own job id so a superseded runner cannot publish late rows;
//  2. deletes and replaces only this block's semantic occurrences, spans,
//     construction members, and lexical audio bindings;
//  3. persists exact construction membership and derives occurrence spans
//     from maximal adjacent runs of member token ids (never from a broad
//     provider span);
//  4. queues this block's lexical pronunciation;
//  5. marks the block ready with its published provenance.
//
// Sentences and narration bindings are source-owned and are never touched.
// ChunkPipelineProvenance carries the immutable pipeline identity for one
// published paragraph: the profile snapshot and the linguistic/translation
// provider pairs that produced the merged artifact.
type ChunkPipelineProvenance struct {
	ProfileID             string
	ProfileName           string
	SnapshotHash          string
	LinguisticProviderID  string
	LinguisticModel       string
	TranslationProviderID string
	TranslationModel      string
}

func (s *Store) PersistAnalysisChunk(ctx context.Context, id library.ULID, blockIndex int, jobID, runID library.ULID, prepared semantics.PreparedArticle, validated semantics.ValidatedResponse, providerID, requestedModel, providerEffort string, prior ...[]semantics.NewSense) error {
	return s.persistAnalysisChunk(ctx, id, blockIndex, jobID, runID, prepared, validated, providerID, requestedModel, providerEffort, nil, prior...)
}

// PersistAnalysisChunkWithProvenance publishes one validated paragraph with
// the full pipeline provenance: block profile columns and sense translation
// provenance are written for newly created rows only.
func (s *Store) PersistAnalysisChunkWithProvenance(ctx context.Context, id library.ULID, blockIndex int, jobID, runID library.ULID, prepared semantics.PreparedArticle, validated semantics.ValidatedResponse, providerID, requestedModel, providerEffort string, provenance *ChunkPipelineProvenance, prior ...[]semantics.NewSense) error {
	return s.persistAnalysisChunk(ctx, id, blockIndex, jobID, runID, prepared, validated, providerID, requestedModel, providerEffort, provenance, prior...)
}

func (s *Store) persistAnalysisChunk(ctx context.Context, id library.ULID, blockIndex int, jobID, runID library.ULID, prepared semantics.PreparedArticle, validated semantics.ValidatedResponse, providerID, requestedModel, providerEffort string, provenance *ChunkPipelineProvenance, prior ...[]semantics.NewSense) error {
	if s == nil || s.db == nil {
		return errors.New("reader: nil database")
	}
	var priorSenses []semantics.NewSense
	if len(prior) > 0 {
		priorSenses = prior[0]
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		return persistAnalysisChunkTx(ctx, tx, id, blockIndex, jobID, runID, prepared, validated, providerID, requestedModel, providerEffort, priorSenses, provenance)
	})
}

func persistAnalysisChunkTx(ctx context.Context, tx *sql.Tx, id library.ULID, blockIndex int, jobID, runID library.ULID, prepared semantics.PreparedArticle, validated semantics.ValidatedResponse, providerID, requestedModel, providerEffort string, priorSenses []semantics.NewSense, provenance *ChunkPipelineProvenance) error {
	const op = "publish analysis chunk"
	var sourceLanguage, targetLanguage, contentHash, analysisJobID string
	if err := tx.QueryRowContext(ctx, `SELECT source_language, target_language, content_hash, analysis_job_id FROM article WHERE id = ?`, id.String()).Scan(&sourceLanguage, &targetLanguage, &contentHash, &analysisJobID); errors.Is(err, sql.ErrNoRows) {
		return &Error{Op: op, Kind: KindNotFound, Err: sql.ErrNoRows}
	} else if err != nil {
		return err
	}
	if contentHash != prepared.ContentHash {
		return &Error{Op: op, Kind: KindConflict, Err: errors.New("article content changed while analysis was running")}
	}
	if analysisJobID != jobID.String() {
		return &Error{Op: op, Kind: KindConflict, Err: errors.New("analysis job was superseded")}
	}
	var blockID string
	var blockJobID, blockStatus string
	if err := tx.QueryRowContext(ctx, `SELECT id, analysis_job_id, analysis_status FROM article_block WHERE article_id = ? AND block_index = ?`, id.String(), blockIndex).Scan(&blockID, &blockJobID, &blockStatus); errors.Is(err, sql.ErrNoRows) {
		return &Error{Op: op, Kind: KindNotFound, Err: fmt.Errorf("article block %d not found", blockIndex)}
	} else if err != nil {
		return err
	}
	if blockJobID != jobID.String() || (blockStatus != string(BlockPending) && blockStatus != string(BlockProcessing)) {
		return &Error{Op: op, Kind: KindConflict, Err: fmt.Errorf("article block %d is not owned by the active job", blockIndex)}
	}

	// Only this block's rows may be touched. Cascades remove spans,
	// construction members, and lexical bindings; semantic senses are shared
	// and remain untouched.
	if _, err := tx.ExecContext(ctx, `DELETE FROM article_occurrence WHERE article_block_id = ?`, blockID); err != nil {
		return err
	}

	blockByIndex, err := articleBlocksByIndex(ctx, tx, id)
	if err != nil {
		return err
	}
	sentenceByBlock := sentencesByBlockTx(ctx, tx, id)
	block := blockByIndex[blockIndex]

	// Resolve every referenced sense for this chunk. New-sense refs were
	// already namespaced by the caller (bN:local form). Prior validated senses
	// may be referenced by later paragraphs (for example paragraph two
	// referencing paragraph one's b0:bank); they are materialized through the
	// idempotent EnsureSenseTx, which returns the same active sense row that
	// the earlier paragraph published, so their durable identity is reused.
	newByRef := make(map[string]*semantics.Sense)
	translationProvenance := semantics.TranslationProvenance{}
	if provenance != nil {
		translationProvenance = semantics.TranslationProvenance{ProviderID: provenance.TranslationProviderID, ProviderModel: provenance.TranslationModel}
	}
	for _, proposal := range priorSenses {
		sense, err := semantics.EnsureSenseTx(ctx, tx, sourceLanguage, targetLanguage, proposal, providerID, requestedModel, translationProvenance)
		if err != nil {
			return &Error{Op: op, Kind: KindValidation, Err: err}
		}
		newByRef[proposal.Ref] = sense
	}
	for _, proposal := range validated.Response.NewSenses {
		if _, already := newByRef[proposal.Ref]; already {
			return &Error{Op: op, Kind: KindValidation, Err: fmt.Errorf("new sense ref %q collides with a prior validated sense", proposal.Ref)}
		}
		sense, err := semantics.EnsureSenseTx(ctx, tx, sourceLanguage, targetLanguage, proposal, providerID, requestedModel, translationProvenance)
		if err != nil {
			return &Error{Op: op, Kind: KindValidation, Err: err}
		}
		newByRef[proposal.Ref] = sense
	}
	candidateIDs := make(map[string]struct{}, len(prepared.Candidates))
	for _, candidate := range prepared.Candidates {
		candidateIDs[candidate.ID] = struct{}{}
	}
	resolveSense := func(idValue, ref string, kind semantics.Kind) (*semantics.Sense, error) {
		switch {
		case idValue != "":
			if _, ok := candidateIDs[idValue]; !ok {
				return nil, fmt.Errorf("existing sense %q was not supplied", idValue)
			}
			return semantics.EnsureExistingSenseTx(ctx, tx, idValue, sourceLanguage, targetLanguage, kind)
		case ref != "":
			sense, ok := newByRef[ref]
			if !ok || sense == nil {
				return nil, fmt.Errorf("new sense ref %q was not materialized", ref)
			}
			return sense, nil
		}
		return nil, nil
	}

	type blockToken struct {
		token     semantics.Token
		result    semantics.TokenResult
		sentence  string
		sense     *semantics.Sense
		occID     string
		occPolicy ShadowPolicy
	}
	tokens := make([]blockToken, 0, len(validated.Tokens))
	for _, resolved := range validated.Tokens {
		token := resolved.Token
		if token.BlockIndex != blockIndex {
			continue
		}
		entry := blockToken{token: token, result: resolved.Result, occID: library.NewULID().String(), occPolicy: ShadowNone}
		sense, err := resolveSense(resolved.Result.SemanticSenseID, resolved.Result.NewSenseRef, resolved.Result.Kind)
		if err != nil {
			return &Error{Op: op, Kind: KindValidation, Err: err}
		}
		entry.sense = sense
		entry.sentence = sentenceForSpanTx(sentenceByBlock, semantics.ResolvedSpan{BlockIndex: token.BlockIndex, StartUTF16: token.StartUTF16, EndUTF16: token.EndUTF16, SourceText: token.SourceText})
		effective := resolved.Result.ShadowText
		if effective == "" && sense != nil {
			effective = sense.PrimaryTranslation
		}
		if effective != "" {
			entry.occPolicy = ShadowToken
		}
		tokens = append(tokens, entry)
	}
	occurrenceIDByTokenID := make(map[string]string, len(tokens))
	for _, entry := range tokens {
		if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, semantic_sense_id, kind, role, shadow_policy, shadow_text, canonical_pronunciation_text, context_pronunciation_key, confidence_milli) VALUES (?, ?, ?, ?, ?, 'token', ?, ?, ?, ?, ?)`, entry.occID, blockID, nullableString(entry.sentence), nullableULID(entry.sense), entry.result.Kind, entry.occPolicy, entry.result.ShadowText, entry.result.CanonicalPronunciation, entry.result.ContextPronunciationKey, entry.result.ConfidenceMilli); err != nil {
			return writeError(op, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, 0, ?, ?, ?)`, library.NewULID().String(), entry.occID, entry.token.StartUTF16, entry.token.EndUTF16, entry.token.SourceText); err != nil {
			return err
		}
		occurrenceIDByTokenID[entry.token.ID] = entry.occID
	}

	tokenByID := make(map[string]semantics.Token, len(tokens))
	for _, entry := range tokens {
		tokenByID[entry.token.ID] = entry.token
	}
	for _, resolved := range validated.Constructions {
		construction := resolved.Construction
		spans := resolved.Spans
		if len(spans) == 0 || spans[0].BlockIndex != blockIndex {
			continue
		}
		sense, err := resolveSense(construction.SemanticSenseID, construction.NewSenseRef, construction.Kind)
		if err != nil {
			return &Error{Op: op, Kind: KindValidation, Err: err}
		}
		if sense == nil {
			return &Error{Op: op, Kind: KindValidation, Err: errors.New("construction has no semantic sense")}
		}
		// Membership is the exact ordered token list; spans are derived from
		// maximal adjacent runs of member token ids. The provider's broad
		// spans never define membership.
		members := make([]semantics.Token, 0, len(construction.TokenIDs))
		for _, tokenID := range construction.TokenIDs {
			token, ok := tokenByID[tokenID]
			if !ok {
				return &Error{Op: op, Kind: KindValidation, Err: fmt.Errorf("construction references unknown block token %q", tokenID)}
			}
			members = append(members, token)
		}
		occID := library.NewULID().String()
		policy := ShadowGroup
		if construction.Role == "discontinuous_construction" {
			policy = ShadowMarker
		}
		sentenceID := ""
		if len(members) > 0 {
			sentenceID = sentenceForSpanTx(sentenceByBlock, semantics.ResolvedSpan{BlockIndex: members[0].BlockIndex, StartUTF16: members[0].StartUTF16, EndUTF16: members[0].EndUTF16, SourceText: members[0].SourceText})
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, semantic_sense_id, kind, role, shadow_policy, shadow_text, canonical_pronunciation_text, context_pronunciation_key, confidence_milli) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, occID, blockID, nullableString(sentenceID), sense.ID.String(), construction.Kind, construction.Role, policy, construction.ShadowText, construction.CanonicalPronunciationText, construction.ContextPronunciationKey, construction.ConfidenceMilli); err != nil {
			return writeError(op, err)
		}
		runs := memberRuns(members)
		for runIndex, run := range runs {
			start, end, err := utf16Bounds(block.text, run[0].StartUTF16, run[len(run)-1].EndUTF16)
			if err != nil {
				return &Error{Op: op, Kind: KindValidation, Err: err}
			}
			text := block.text[start:end]
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, ?, ?, ?, ?)`, library.NewULID().String(), occID, runIndex, run[0].StartUTF16, run[len(run)-1].EndUTF16, text); err != nil {
				return err
			}
		}
		for memberIndex, member := range members {
			tokenOccurrenceID, ok := occurrenceIDByTokenID[member.ID]
			if !ok {
				return &Error{Op: op, Kind: KindValidation, Err: fmt.Errorf("construction member token %q was not materialized", member.ID)}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_construction_member (construction_occurrence_id, token_occurrence_id, member_index) VALUES (?, ?, ?)`, occID, tokenOccurrenceID, memberIndex); err != nil {
				return writeError(op, err)
			}
		}
	}

	if err := speech.QueueBlockPronunciationsTx(ctx, tx, id, library.ULID(blockID)); err != nil {
		return err
	}
	var profileColumns string
	var profileArgs []any
	if provenance != nil {
		profileColumns = ", published_analysis_profile_id = ?, published_analysis_profile_name = ?, published_analysis_snapshot_hash = ?"
		profileArgs = []any{provenance.ProfileID, provenance.ProfileName, provenance.SnapshotHash}
	}
	args := []any{jobID.String(), jobID.String(), runID.String(), semantics.AnalysisContractVersion, requestedModel, providerEffort, store.NowUTC()}
	args = append(args, profileArgs...)
	args = append(args, blockID, jobID.String())
	if _, err := tx.ExecContext(ctx, `
		UPDATE article_block SET
			analysis_status = 'ready',
			analysis_error_code = '',
			analysis_job_id = ?,
			published_analysis_job_id = ?,
			published_analysis_run_id = ?,
			published_analysis_revision = ?,
			published_analysis_model = ?,
			published_analysis_effort = ?,
			published_at = ?`+profileColumns+`
		WHERE id = ? AND analysis_job_id = ?
	`, args...); err != nil {
		return err
	}
	return nil
}

// memberRuns groups ordered member tokens into maximal adjacent runs. Members
// are already validated to be unique and in source order; adjacency is defined
// by consecutive TokenIndex values within the same block.
func memberRuns(members []semantics.Token) [][]semantics.Token {
	runs := make([][]semantics.Token, 0, 1)
	for _, member := range members {
		if len(runs) == 0 {
			runs = append(runs, []semantics.Token{member})
			continue
		}
		last := runs[len(runs)-1]
		previous := last[len(last)-1]
		if member.BlockIndex == previous.BlockIndex && member.TokenIndex == previous.TokenIndex+1 {
			runs[len(runs)-1] = append(runs[len(runs)-1], member)
			continue
		}
		runs = append(runs, []semantics.Token{member})
	}
	return runs
}

// utf16Bounds converts a UTF-16 span back into byte bounds of the block text.
func utf16Bounds(text string, startUTF16, endUTF16 int) (int, int, error) {
	start, err := ByteOffsetFromUTF16(text, startUTF16)
	if err != nil {
		return 0, 0, err
	}
	end, err := ByteOffsetFromUTF16(text, endUTF16)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func sentencesByBlockTx(ctx context.Context, tx *sql.Tx, id library.ULID) map[int][]articleSentenceSpan {
	rows, err := tx.QueryContext(ctx, `SELECT b.block_index, s.id, s.start_utf16, s.end_utf16 FROM article_sentence s JOIN article_block b ON b.id = s.article_block_id WHERE b.article_id = ? ORDER BY b.block_index, s.sentence_index`, id.String())
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make(map[int][]articleSentenceSpan)
	for rows.Next() {
		var blockIndex int
		var span articleSentenceSpan
		if err := rows.Scan(&blockIndex, &span.id, &span.start, &span.end); err != nil {
			return nil
		}
		result[blockIndex] = append(result[blockIndex], span)
	}
	return result
}

func sentenceForSpanTx(sentences map[int][]articleSentenceSpan, span semantics.ResolvedSpan) string {
	for _, candidate := range sentences[span.BlockIndex] {
		if span.StartUTF16 >= candidate.start && span.EndUTF16 <= candidate.end {
			return candidate.id
		}
	}
	return ""
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableULID(sense *semantics.Sense) any {
	if sense == nil {
		return nil
	}
	return sense.ID.String()
}

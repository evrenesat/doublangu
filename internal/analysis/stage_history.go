package analysis

import (
	"context"
	"errors"
	"fmt"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

// Diagnostic bounds (§8.4): full prompt/schema/response retained up to hard
// limits; only metadata and excerpts are truncated with explicit flags.
const (
	stagePromptLimitBytes   = 1 << 20
	stageResponseLimitBytes = 1 << 20
	stageMetadataLimitBytes = 64 << 10
	stageExcerptLimitBytes  = 16 << 10
)

// StageAttempt is one per-paragraph stage execution record.
type StageAttempt struct {
	ID                string `json:"id"`
	RunID             string `json:"run_id"`
	BlockIndex        int    `json:"block_index"`
	StageID           string `json:"stage_id"`
	ProviderID        string `json:"provider_id"`
	ProviderType      string `json:"provider_type"`
	ConfigFingerprint string `json:"config_fingerprint"`
	ModelID           string `json:"model_id"`
	OptionsJSON       string `json:"options_json,omitempty"`
	OptionsHash       string `json:"options_hash,omitempty"`
	RequestedModel    string `json:"requested_model,omitempty"`
	ContractVersion   string `json:"contract_version"`
	PromptVersion     string `json:"prompt_version"`
	InputHash         string `json:"input_hash"`
	UpstreamHash      string `json:"upstream_artifact_hash"`
	CacheDisposition  string `json:"cache_disposition"`
	SourceCacheID     string `json:"source_cache_id"`
	Status            string `json:"status"`
	StartedAt         string `json:"started_at"`
}

// StageAttemptFinish carries the terminal attempt outcome.
type StageAttemptFinish struct {
	Status        string
	ReportedModel string
	RequestID     string
	FinishReason  string
	UsageJSON     string
	TimingJSON    string
	MetadataJSON  string
	StderrExcerpt string
	ErrorCode     string
	ErrorDetail   string
	CompletedAt   string
	DurationMS    int64
}

// StageTurn is one recorded stage turn bound to a stage attempt.
type StageTurn struct {
	AttemptID             string
	TurnIndex             int
	TurnKind              string
	Prompt                string
	OutputSchema          string
	CompletedResponse     string
	ResponseHash          string
	ValidationError       string
	ProviderError         string
	CompletionMetadata    string
	ProviderStderrExcerpt string
	StartedAt             string
	CompletedAt           string
	DurationMS            int64
	Status                string
}

func boundField(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	return value[:limit], true
}

// StartStageAttempt records one stage attempt as running.
func (s *HistoryStore) StartStageAttempt(ctx context.Context, attempt StageAttempt) (StageAttempt, error) {
	if s == nil || s.db == nil {
		return StageAttempt{}, errors.New("analysis history: nil database")
	}
	if attempt.ID == "" {
		attempt.ID = library.NewULID().String()
	}
	if attempt.RunID == "" || attempt.BlockIndex < 0 || attempt.StageID == "" ||
		attempt.ProviderID == "" || attempt.ModelID == "" || attempt.ContractVersion == "" ||
		attempt.PromptVersion == "" {
		return StageAttempt{}, errors.New("analysis history: invalid stage attempt")
	}
	if attempt.Status == "" {
		attempt.Status = "running"
	}
	if attempt.CacheDisposition == "" {
		attempt.CacheDisposition = "miss"
	}
	if attempt.StartedAt == "" {
		attempt.StartedAt = store.NowUTC()
	}
	if attempt.OptionsJSON == "" {
		attempt.OptionsJSON = "{}"
	}
	if attempt.RequestedModel == "" {
		attempt.RequestedModel = attempt.ModelID
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_stage_attempt (
			id, run_id, block_index, stage_id, status, provider_id, provider_type,
			provider_config_fingerprint, model_id, options_json, options_hash,
			requested_model, contract_version, prompt_version,
			input_hash, upstream_artifact_hash, cache_disposition, source_cache_id,
			started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, attempt.ID, attempt.RunID, attempt.BlockIndex, attempt.StageID, attempt.Status,
		attempt.ProviderID, attempt.ProviderType, attempt.ConfigFingerprint, attempt.ModelID,
		attempt.OptionsJSON, attempt.OptionsHash, attempt.RequestedModel,
		attempt.ContractVersion, attempt.PromptVersion, attempt.InputHash, attempt.UpstreamHash,
		attempt.CacheDisposition, attempt.SourceCacheID, attempt.StartedAt)
	if err != nil {
		return StageAttempt{}, fmt.Errorf("start stage attempt: %w", err)
	}
	return attempt, nil
}

// FinishStageAttempt marks one attempt terminal.
func (s *HistoryStore) FinishStageAttempt(ctx context.Context, attemptID string, finish StageAttemptFinish) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	if attemptID == "" || (finish.Status != "succeeded" && finish.Status != "failed") {
		return errors.New("analysis history: invalid stage attempt finish")
	}
	if finish.CompletedAt == "" {
		finish.CompletedAt = store.NowUTC()
	}
	metadata, metadataTruncated := boundField(finish.MetadataJSON, stageMetadataLimitBytes)
	_ = metadataTruncated
	stderr, _ := boundField(finish.StderrExcerpt, stageExcerptLimitBytes)
	detail, _ := boundField(finish.ErrorDetail, stageExcerptLimitBytes)
	_, err := s.db.Exec(ctx, `
		UPDATE analysis_stage_attempt SET
			status = ?, reported_model = ?, request_id = ?, finish_reason = ?,
			usage_json = ?, timing_json = ?, metadata_json = ?,
			provider_stderr_excerpt = ?, error_code = ?, error_detail = ?,
			completed_at = ?, duration_ms = ?
		WHERE id = ?
	`, finish.Status, finish.ReportedModel, finish.RequestID, finish.FinishReason,
		finish.UsageJSON, finish.TimingJSON, metadata, stderr, finish.ErrorCode,
		detail, finish.CompletedAt, finish.DurationMS, attemptID)
	return err
}

// AppendStageTurn records one stage turn. Oversized prompts or responses are
// rejected rather than silently truncated (provider protocol errors already
// enforce the response bound before this point).
func (s *HistoryStore) AppendStageTurn(ctx context.Context, turn StageTurn) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	if turn.AttemptID == "" || turn.TurnIndex < 0 ||
		(turn.TurnKind != "initial" && turn.TurnKind != "corrective") {
		return errors.New("analysis history: invalid stage turn")
	}
	if turn.Prompt == "" || turn.OutputSchema == "" {
		return errors.New("analysis history: stage turn prompt and schema are required")
	}
	if len(turn.Prompt) > stagePromptLimitBytes || len(turn.CompletedResponse) > stageResponseLimitBytes {
		return errors.New("analysis history: stage turn content exceeds the retention bound")
	}
	if turn.Status == "" {
		turn.Status = "completed"
	}
	if turn.StartedAt == "" {
		turn.StartedAt = store.NowUTC()
	}
	if turn.CompletionMetadata == "" {
		turn.CompletionMetadata = "{}"
	}
	metadata, metadataTruncated := boundField(turn.CompletionMetadata, stageMetadataLimitBytes)
	stderr, stderrTruncated := boundField(turn.ProviderStderrExcerpt, stageExcerptLimitBytes)
	validation, validationTruncated := boundField(turn.ValidationError, stageExcerptLimitBytes)
	providerError, providerErrorTruncated := boundField(turn.ProviderError, stageExcerptLimitBytes)
	flag := func(truncated bool) int {
		if truncated {
			return 1
		}
		return 0
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_stage_turn (
			id, stage_attempt_id, turn_index, turn_kind, prompt, output_schema,
			completed_response, response_hash, validation_error, provider_error,
			completion_metadata_json, provider_stderr_excerpt, started_at,
			completed_at, duration_ms, status,
			validation_truncated, provider_error_truncated, metadata_truncated, stderr_truncated
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, library.NewULID().String(), turn.AttemptID, turn.TurnIndex, turn.TurnKind,
		turn.Prompt, turn.OutputSchema, turn.CompletedResponse, turn.ResponseHash,
		validation, providerError, metadata, stderr, turn.StartedAt, turn.CompletedAt,
		turn.DurationMS, turn.Status, flag(validationTruncated), flag(providerErrorTruncated),
		flag(metadataTruncated), flag(stderrTruncated))
	if err != nil {
		return fmt.Errorf("append stage turn: %w", err)
	}
	return nil
}

// RecoverInterruptedStageAttempts marks every running stage attempt from an
// exited process failed with the interruption code, preserving artifacts.
func (s *HistoryStore) RecoverInterruptedStageAttempts(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	recoveredAt := store.NowUTC()
	_, err := s.db.Exec(ctx, `
		UPDATE analysis_stage_attempt SET
			status = 'failed',
			error_code = 'v1.analysis_interrupted',
			error_detail = 'stage attempt interrupted during server restart',
			completed_at = ?,
			duration_ms = CASE
				WHEN julianday(started_at) IS NULL THEN duration_ms
				WHEN julianday(?) > julianday(started_at)
					THEN CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)
				ELSE 0
			END
		WHERE status = 'running'
	`, recoveredAt, recoveredAt, recoveredAt)
	return err
}

// ListStageAttempts returns ordered attempts for one run.
func (s *HistoryStore) ListStageAttempts(ctx context.Context, runID string) ([]StageAttempt, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("analysis history: nil database")
	}
	rows, err := s.db.Query(ctx, `SELECT id, run_id, block_index, stage_id, provider_id, provider_type,
		provider_config_fingerprint, model_id, options_json, options_hash, requested_model,
		contract_version, prompt_version,
		input_hash, upstream_artifact_hash, cache_disposition, source_cache_id, status, started_at
		FROM analysis_stage_attempt WHERE run_id = ? ORDER BY block_index, stage_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := make([]StageAttempt, 0)
	for rows.Next() {
		var attempt StageAttempt
		if err := rows.Scan(&attempt.ID, &attempt.RunID, &attempt.BlockIndex, &attempt.StageID,
			&attempt.ProviderID, &attempt.ProviderType, &attempt.ConfigFingerprint, &attempt.ModelID,
			&attempt.OptionsJSON, &attempt.OptionsHash, &attempt.RequestedModel,
			&attempt.ContractVersion, &attempt.PromptVersion, &attempt.InputHash,
			&attempt.UpstreamHash, &attempt.CacheDisposition, &attempt.SourceCacheID,
			&attempt.Status, &attempt.StartedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attempts, nil
}

// SetRunPipelineProvenance records the pipeline identity on an analysis run.
func (s *HistoryStore) SetRunPipelineProvenance(ctx context.Context, runID string, payloadJSON string, pipelineVersion, profileID, profileName, snapshotHash string) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	_, err := s.db.Exec(ctx, `UPDATE analysis_run SET pipeline_version = ?, profile_id = ?, profile_name = ?, profile_snapshot_json = ?, profile_snapshot_hash = ? WHERE id = ?`,
		pipelineVersion, profileID, profileName, payloadJSON, snapshotHash, runID)
	return err
}

// SetRunPipelineFailure records which stage/provider failed a run.
func (s *HistoryStore) SetRunPipelineFailure(ctx context.Context, runID, stageID, providerID string) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	_, err := s.db.Exec(ctx, `UPDATE analysis_run SET failed_stage_id = ?, failed_provider_id = ? WHERE id = ?`, stageID, providerID, runID)
	return err
}

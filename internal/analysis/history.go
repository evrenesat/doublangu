package analysis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/store"
)

// ErrInvalidRunQuery distinguishes caller-supplied pagination errors from
// database failures so the owner API can return the correct stable status.
var ErrInvalidRunQuery = errors.New("invalid analysis run query")

// Run is the retained owner-visible provenance for one durable job attempt.
// ErrorDetail is intentionally available only from owner-authenticated
// diagnostics handlers; public article responses expose only ErrorCode.
type Run struct {
	ID                  library.ULID `json:"id"`
	ArticleID           library.ULID `json:"article_id"`
	ArticleTitle        string       `json:"article_title"`
	JobID               library.ULID `json:"job_id"`
	AttemptCount        int          `json:"attempt_count"`
	ContentHash         string       `json:"content_hash"`
	ContractVersion     string       `json:"contract_version"`
	PromptVersion       string       `json:"prompt_version"`
	RequestedModel      string       `json:"requested_model"`
	RequestedEffort     string       `json:"requested_effort"`
	ProviderID          string       `json:"provider_id"`
	CodexCLIVersion     string       `json:"codex_cli_version"`
	ReportedModel       string       `json:"reported_model"`
	StartedAt           string       `json:"started_at"`
	CompletedAt         string       `json:"completed_at"`
	DurationMS          int64        `json:"duration_ms"`
	Status              string       `json:"status"`
	TotalParagraphs     int          `json:"total_paragraphs"`
	CompletedParagraphs int          `json:"completed_paragraphs"`
	FailedBlockIndex    int          `json:"failed_block_index"`
	ErrorCode           string       `json:"error_code"`
	ErrorDetail         string       `json:"error_detail,omitempty"`
	StderrExcerpt       string       `json:"stderr_excerpt,omitempty"`
	Turns               []Turn       `json:"turns"`
	ProfileID           string       `json:"profile_id,omitempty"`
	ProfileName         string       `json:"profile_name,omitempty"`
	ProfileSnapshotHash string       `json:"profile_snapshot_hash,omitempty"`
	// ProfileSnapshot is the exact immutable profile used by the run,
	// decoded from the stored canonical JSON. FailedStageID and
	// FailedProviderID name the authoritative failure binding, including
	// failures that left no complete attempt sequence.
	ProfileSnapshot  *pipeline.ProfileSnapshot `json:"profile_snapshot,omitempty"`
	FailedStageID    string                    `json:"failed_stage_id,omitempty"`
	FailedProviderID string                    `json:"failed_provider_id,omitempty"`
	StageAttempts    []StageAttemptSummary     `json:"stage_attempts,omitempty"`
}

// RunSummary deliberately uses the same safe fields as Run without turn
// artifacts. Full prompts and responses are returned only by GetRun.
type RunSummary struct {
	ID                  library.ULID        `json:"id"`
	ArticleID           library.ULID        `json:"article_id"`
	ArticleTitle        string              `json:"article_title"`
	AttemptCount        int                 `json:"attempt_count"`
	RequestedModel      string              `json:"requested_model"`
	RequestedEffort     string              `json:"requested_effort"`
	Status              string              `json:"status"`
	TotalParagraphs     int                 `json:"total_paragraphs"`
	CompletedParagraphs int                 `json:"completed_paragraphs"`
	FailedBlockIndex    int                 `json:"failed_block_index"`
	DurationMS          int64               `json:"duration_ms"`
	StartedAt           string              `json:"started_at"`
	CompletedAt         string              `json:"completed_at"`
	ErrorCode           string              `json:"error_code"`
	ProfileID           string              `json:"profile_id,omitempty"`
	ProfileName         string              `json:"profile_name,omitempty"`
	ProfileSnapshotHash string              `json:"profile_snapshot_hash,omitempty"`
	Bindings            []RunBindingSummary `json:"bindings,omitempty"`
}

// RunBindingSummary is the compact stage provenance for one pipeline run:
// stage, provider, and model only. Options stay in run detail and history.
type RunBindingSummary struct {
	StageID    string `json:"stage_id"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
}

// runBindingsFromSnapshot derives compact binding summaries from a stored
// immutable profile snapshot. Legacy rows without a snapshot (or with a
// snapshot that no longer decodes) yield nil so callers fall back to the
// legacy requested model/effort fields.
func runBindingsFromSnapshot(raw string) []RunBindingSummary {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var snapshot pipeline.ProfileSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || len(snapshot.Bindings) == 0 {
		return nil
	}
	bindings := make([]RunBindingSummary, 0, len(snapshot.Bindings))
	for _, binding := range snapshot.Bindings {
		if binding.StageID == "" || binding.ProviderID == "" || binding.ModelID == "" {
			return nil
		}
		bindings = append(bindings, RunBindingSummary{
			StageID: string(binding.StageID), ProviderID: binding.ProviderID, ModelID: binding.ModelID,
		})
	}
	return bindings
}

type RunStart struct {
	ArticleID       library.ULID
	ArticleTitle    string
	JobID           library.ULID
	AttemptCount    int
	ContentHash     string
	ContractVersion string
	PromptVersion   string
	RequestedModel  string
	RequestedEffort string
	ProviderID      string
	CodexCLIVersion string
	TotalParagraphs int
}

type RunFinish struct {
	Status           string
	ReportedModel    string
	CompletedAt      string
	DurationMS       int64
	CompletedParags  int
	FailedBlockIndex int
	ErrorCode        string
	ErrorDetail      string
	StderrExcerpt    string
}

type Turn struct {
	ID                     library.ULID `json:"id"`
	RunID                  library.ULID `json:"run_id"`
	BlockIndex             int          `json:"block_index"`
	TurnIndex              int          `json:"turn_index"`
	TurnKind               string       `json:"turn_kind"`
	Prompt                 string       `json:"prompt"`
	OutputSchema           string       `json:"output_schema"`
	CompletedResponse      string       `json:"completed_response,omitempty"`
	ResponseHash           string       `json:"response_hash,omitempty"`
	ValidationError        string       `json:"validation_error,omitempty"`
	ProviderError          string       `json:"provider_error,omitempty"`
	CompletionMetadataJSON string       `json:"completion_metadata_json"`
	ProviderStderrExcerpt  string       `json:"provider_stderr_excerpt,omitempty"`
	StartedAt              string       `json:"started_at"`
	CompletedAt            string       `json:"completed_at"`
	DurationMS             int64        `json:"duration_ms"`
	Status                 string       `json:"status"`
}

// StageAttemptSummary is the owner-visible stage attempt row for pipeline
// runs, including the stored provider/model/options/hash/usage metadata and
// the nested stage turns recorded for the attempt.
type StageAttemptSummary struct {
	ID                        string             `json:"id"`
	StageID                   string             `json:"stage_id"`
	BlockIndex                int                `json:"block_index"`
	Status                    string             `json:"status"`
	ProviderID                string             `json:"provider_id"`
	ProviderType              string             `json:"provider_type"`
	ProviderConfigFingerprint string             `json:"provider_config_fingerprint"`
	ModelID                   string             `json:"model_id"`
	ContractVersion           string             `json:"contract_version"`
	PromptVersion             string             `json:"prompt_version"`
	InputHash                 string             `json:"input_hash"`
	UpstreamArtifactHash      string             `json:"upstream_artifact_hash"`
	OptionsHash               string             `json:"options_hash"`
	Options                   json.RawMessage    `json:"options,omitempty"`
	CacheDisposition          string             `json:"cache_disposition"`
	SourceCacheID             string             `json:"source_cache_id"`
	RequestedModel            string             `json:"requested_model"`
	ReportedModel             string             `json:"reported_model"`
	RequestID                 string             `json:"request_id"`
	FinishReason              string             `json:"finish_reason"`
	UsageJSON                 string             `json:"usage_json"`
	TimingJSON                string             `json:"timing_json"`
	MetadataJSON              string             `json:"metadata_json"`
	ProviderStderrExcerpt     string             `json:"provider_stderr_excerpt,omitempty"`
	ErrorCode                 string             `json:"error_code"`
	ErrorDetail               string             `json:"error_detail,omitempty"`
	UsageTruncated            bool               `json:"usage_truncated"`
	TimingTruncated           bool               `json:"timing_truncated"`
	MetadataTruncated         bool               `json:"metadata_truncated"`
	StderrTruncated           bool               `json:"stderr_truncated"`
	ErrorDetailTruncated      bool               `json:"error_detail_truncated"`
	StartedAt                 string             `json:"started_at"`
	CompletedAt               string             `json:"completed_at,omitempty"`
	DurationMS                int64              `json:"duration_ms"`
	Turns                     []StageTurnSummary `json:"turns"`
}

// StageTurnSummary is one retained stage turn inside an attempt.
type StageTurnSummary struct {
	ID                     string `json:"id"`
	TurnIndex              int    `json:"turn_index"`
	TurnKind               string `json:"turn_kind"`
	Prompt                 string `json:"prompt"`
	OutputSchema           string `json:"output_schema"`
	CompletedResponse      string `json:"completed_response,omitempty"`
	ResponseHash           string `json:"response_hash,omitempty"`
	ValidationError        string `json:"validation_error,omitempty"`
	ProviderError          string `json:"provider_error,omitempty"`
	CompletionMetadataJSON string `json:"completion_metadata_json"`
	ProviderStderrExcerpt  string `json:"provider_stderr_excerpt,omitempty"`
	ValidationTruncated    bool   `json:"validation_truncated"`
	ProviderErrorTruncated bool   `json:"provider_error_truncated"`
	MetadataTruncated      bool   `json:"metadata_truncated"`
	StderrTruncated        bool   `json:"stderr_truncated"`
	StartedAt              string `json:"started_at"`
	CompletedAt            string `json:"completed_at,omitempty"`
	DurationMS             int64  `json:"duration_ms"`
	Status                 string `json:"status"`
}

type RunsPage struct {
	Runs       []RunSummary `json:"runs"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type HistoryStore struct {
	db *store.DB
}

func NewHistoryStore(db *store.DB) *HistoryStore { return &HistoryStore{db: db} }

func (s *HistoryStore) StartRun(ctx context.Context, start RunStart) (Run, error) {
	if s == nil || s.db == nil {
		return Run{}, errors.New("analysis history: nil database")
	}
	if start.ArticleID.IsZero() || start.AttemptCount <= 0 || start.TotalParagraphs < 0 {
		return Run{}, errors.New("analysis history: invalid run start")
	}
	run := Run{
		ID: library.NewULID(), ArticleID: start.ArticleID, ArticleTitle: start.ArticleTitle,
		JobID: start.JobID, AttemptCount: start.AttemptCount, ContentHash: start.ContentHash,
		ContractVersion: start.ContractVersion, PromptVersion: start.PromptVersion,
		RequestedModel: start.RequestedModel, RequestedEffort: start.RequestedEffort,
		ProviderID: start.ProviderID, CodexCLIVersion: start.CodexCLIVersion,
		StartedAt: store.NowUTC(), Status: "running", TotalParagraphs: start.TotalParagraphs,
		FailedBlockIndex: -1, Turns: []Turn{},
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_run (
			id, article_id, job_id, attempt_count, content_hash, contract_version,
			prompt_version, requested_model, requested_effort, provider_id,
			codex_cli_version, started_at, status, total_paragraphs,
			completed_paragraphs, failed_block_index
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, -1)
	`, run.ID.String(), run.ArticleID.String(), run.JobID.String(), run.AttemptCount,
		run.ContentHash, run.ContractVersion, run.PromptVersion, run.RequestedModel,
		run.RequestedEffort, run.ProviderID, run.CodexCLIVersion, run.StartedAt,
		run.Status, run.TotalParagraphs)
	if err != nil {
		return Run{}, fmt.Errorf("start analysis run: %w", err)
	}
	return run, nil
}

func (s *HistoryStore) UpdateProgress(ctx context.Context, runID library.ULID, completed, failedBlockIndex int) error {
	if completed < 0 || failedBlockIndex < -1 {
		return errors.New("analysis history: invalid progress")
	}
	_, err := s.db.Exec(ctx, `UPDATE analysis_run SET completed_paragraphs = ?, failed_block_index = ? WHERE id = ?`, completed, failedBlockIndex, runID.String())
	return err
}

func (s *HistoryStore) UpdateProvenance(ctx context.Context, runID library.ULID, cliVersion, reportedModel string) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	_, err := s.db.Exec(ctx, `UPDATE analysis_run SET codex_cli_version = CASE WHEN ? <> '' THEN ? ELSE codex_cli_version END, reported_model = CASE WHEN ? <> '' THEN ? ELSE reported_model END WHERE id = ?`, cliVersion, cliVersion, reportedModel, reportedModel, runID.String())
	return err
}

func (s *HistoryStore) AppendTurn(ctx context.Context, turn Turn) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	if turn.RunID.IsZero() || turn.BlockIndex < 0 || turn.TurnIndex < 0 || strings.TrimSpace(turn.TurnKind) == "" || turn.Prompt == "" || turn.OutputSchema == "" {
		return errors.New("analysis history: invalid turn")
	}
	if turn.ID.IsZero() {
		turn.ID = library.NewULID()
	}
	if turn.StartedAt == "" {
		turn.StartedAt = store.NowUTC()
	}
	if turn.CompletionMetadataJSON == "" {
		turn.CompletionMetadataJSON = "{}"
	}
	if turn.Status == "" {
		turn.Status = "completed"
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_turn (
			id, run_id, block_index, turn_index, turn_kind, prompt, output_schema,
			completed_response, response_hash, validation_error, provider_error,
			completion_metadata_json, provider_stderr_excerpt, started_at,
			completed_at, duration_ms, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, turn.ID.String(), turn.RunID.String(), turn.BlockIndex, turn.TurnIndex, turn.TurnKind,
		turn.Prompt, turn.OutputSchema, turn.CompletedResponse, turn.ResponseHash,
		turn.ValidationError, turn.ProviderError, turn.CompletionMetadataJSON,
		turn.ProviderStderrExcerpt, turn.StartedAt, turn.CompletedAt, turn.DurationMS,
		turn.Status)
	return err
}

func (s *HistoryStore) FinishRun(ctx context.Context, runID library.ULID, finish RunFinish) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	if finish.Status != "succeeded" && finish.Status != "failed" {
		return errors.New("analysis history: invalid final status")
	}
	if finish.CompletedAt == "" {
		finish.CompletedAt = store.NowUTC()
	}
	if finish.FailedBlockIndex < -1 {
		return errors.New("analysis history: invalid failed block")
	}
	_, err := s.db.Exec(ctx, `
		UPDATE analysis_run SET status = ?, reported_model = ?, completed_at = ?,
			duration_ms = ?, completed_paragraphs = ?, failed_block_index = ?,
			error_code = ?, error_detail = ?, stderr_excerpt = ? WHERE id = ?
	`, finish.Status, finish.ReportedModel, finish.CompletedAt, finish.DurationMS,
		finish.CompletedParags, finish.FailedBlockIndex, finish.ErrorCode,
		finish.ErrorDetail, finish.StderrExcerpt, runID.String())
	return err
}

func (s *HistoryStore) ListRuns(ctx context.Context, articleID string, limit int, cursor string) (RunsPage, error) {
	if s == nil || s.db == nil {
		return RunsPage{}, errors.New("analysis history: nil database")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		return RunsPage{}, fmt.Errorf("%w: limit must be at most 50", ErrInvalidRunQuery)
	}
	where := []string{"1 = 1"}
	args := make([]any, 0, 7)
	if strings.TrimSpace(articleID) != "" {
		where = append(where, "r.article_id = ?")
		args = append(args, articleID)
	}
	if cursor != "" {
		startedAt, id, err := decodeRunCursor(cursor)
		if err != nil {
			return RunsPage{}, fmt.Errorf("%w: %v", ErrInvalidRunQuery, err)
		}
		where = append(where, "(r.started_at < ? OR (r.started_at = ? AND r.id < ?))")
		args = append(args, startedAt, startedAt, id)
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.article_id, a.title, r.attempt_count, r.requested_model,
		       r.requested_effort, r.status, r.total_paragraphs,
		       r.completed_paragraphs, r.failed_block_index, r.duration_ms,
		       r.started_at, r.completed_at, r.error_code,
		       r.profile_id, r.profile_name, r.profile_snapshot_hash,
		       r.profile_snapshot_json
		FROM analysis_run r JOIN article a ON a.id = r.article_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY r.started_at DESC, r.id DESC LIMIT ?
	`, args...)
	if err != nil {
		return RunsPage{}, fmt.Errorf("list analysis runs: %w", err)
	}
	defer rows.Close()
	page := RunsPage{Runs: make([]RunSummary, 0, limit)}
	for rows.Next() {
		var summary RunSummary
		var rawID, rawArticleID, snapshotJSON string
		if err := rows.Scan(&rawID, &rawArticleID, &summary.ArticleTitle, &summary.AttemptCount, &summary.RequestedModel, &summary.RequestedEffort, &summary.Status, &summary.TotalParagraphs, &summary.CompletedParagraphs, &summary.FailedBlockIndex, &summary.DurationMS, &summary.StartedAt, &summary.CompletedAt, &summary.ErrorCode, &summary.ProfileID, &summary.ProfileName, &summary.ProfileSnapshotHash, &snapshotJSON); err != nil {
			return RunsPage{}, err
		}
		summary.ID = library.ULID(rawID)
		summary.ArticleID = library.ULID(rawArticleID)
		summary.Bindings = runBindingsFromSnapshot(snapshotJSON)
		page.Runs = append(page.Runs, summary)
	}
	if err := rows.Err(); err != nil {
		return RunsPage{}, err
	}
	if len(page.Runs) > limit {
		last := page.Runs[limit-1]
		page.Runs = page.Runs[:limit]
		page.NextCursor = encodeRunCursor(last.StartedAt, last.ID.String())
	}
	return page, nil
}

func (s *HistoryStore) GetRun(ctx context.Context, id library.ULID) (Run, error) {
	if s == nil || s.db == nil {
		return Run{}, errors.New("analysis history: nil database")
	}
	var run Run
	var rawID, rawArticleID, rawJobID string
	var snapshotJSON, failedStageID, failedProviderID string
	err := s.db.QueryRow(ctx, `
		SELECT r.id, r.article_id, a.title, r.job_id, r.attempt_count,
		       r.content_hash, r.contract_version, r.prompt_version,
		       r.requested_model, r.requested_effort, r.provider_id,
		       r.codex_cli_version, r.reported_model, r.started_at,
		       r.completed_at, r.duration_ms, r.status, r.total_paragraphs,
		       r.completed_paragraphs, r.failed_block_index, r.error_code,
		       r.error_detail, r.stderr_excerpt,
		       r.profile_id, r.profile_name, r.profile_snapshot_hash,
		       r.profile_snapshot_json, r.failed_stage_id, r.failed_provider_id
		FROM analysis_run r JOIN article a ON a.id = r.article_id WHERE r.id = ?
	`, id.String()).Scan(&rawID, &rawArticleID, &run.ArticleTitle, &rawJobID, &run.AttemptCount, &run.ContentHash, &run.ContractVersion, &run.PromptVersion, &run.RequestedModel, &run.RequestedEffort, &run.ProviderID, &run.CodexCLIVersion, &run.ReportedModel, &run.StartedAt, &run.CompletedAt, &run.DurationMS, &run.Status, &run.TotalParagraphs, &run.CompletedParagraphs, &run.FailedBlockIndex, &run.ErrorCode, &run.ErrorDetail, &run.StderrExcerpt, &run.ProfileID, &run.ProfileName, &run.ProfileSnapshotHash, &snapshotJSON, &failedStageID, &failedProviderID)
	if err != nil {
		return Run{}, err
	}
	if snapshotJSON != "" {
		var snapshot pipeline.ProfileSnapshot
		if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
			return Run{}, fmt.Errorf("analysis history: invalid stored profile snapshot: %w", err)
		}
		run.ProfileSnapshot = &snapshot
	}
	run.FailedStageID = failedStageID
	run.FailedProviderID = failedProviderID
	run.ID = library.ULID(rawID)
	run.ArticleID = library.ULID(rawArticleID)
	run.JobID = library.ULID(rawJobID)
	attemptRows, err := s.db.Query(ctx, `
		SELECT id, stage_id, block_index, status, provider_id, provider_type,
		       provider_config_fingerprint, model_id, options_json,
		       contract_version,
		       prompt_version, input_hash, upstream_artifact_hash, options_hash,
		       cache_disposition, source_cache_id, requested_model,
		       reported_model, request_id, finish_reason, usage_json,
		       timing_json, metadata_json, provider_stderr_excerpt,
		       error_code, error_detail, usage_truncated, timing_truncated,
		       metadata_truncated, stderr_truncated, error_detail_truncated,
		       started_at, completed_at, duration_ms
		FROM analysis_stage_attempt WHERE run_id = ?
		ORDER BY block_index, stage_id
	`, id.String())
	if err != nil {
		return Run{}, err
	}
	defer attemptRows.Close()
	run.StageAttempts = make([]StageAttemptSummary, 0)
	for attemptRows.Next() {
		var attempt StageAttemptSummary
		var optionsText string
		var usageTruncated, timingTruncated, metadataTruncated, stderrTruncated, detailTruncated int
		if err := attemptRows.Scan(&attempt.ID, &attempt.StageID, &attempt.BlockIndex, &attempt.Status, &attempt.ProviderID, &attempt.ProviderType, &attempt.ProviderConfigFingerprint, &attempt.ModelID, &optionsText, &attempt.ContractVersion, &attempt.PromptVersion, &attempt.InputHash, &attempt.UpstreamArtifactHash, &attempt.OptionsHash, &attempt.CacheDisposition, &attempt.SourceCacheID, &attempt.RequestedModel, &attempt.ReportedModel, &attempt.RequestID, &attempt.FinishReason, &attempt.UsageJSON, &attempt.TimingJSON, &attempt.MetadataJSON, &attempt.ProviderStderrExcerpt, &attempt.ErrorCode, &attempt.ErrorDetail, &usageTruncated, &timingTruncated, &metadataTruncated, &stderrTruncated, &detailTruncated, &attempt.StartedAt, &attempt.CompletedAt, &attempt.DurationMS); err != nil {
			return Run{}, err
		}
		attempt.UsageTruncated = usageTruncated == 1
		attempt.TimingTruncated = timingTruncated == 1
		attempt.MetadataTruncated = metadataTruncated == 1
		attempt.StderrTruncated = stderrTruncated == 1
		attempt.ErrorDetailTruncated = detailTruncated == 1
		if optionsText != "" {
			attempt.Options = json.RawMessage(optionsText)
		}
		attempt.Turns = make([]StageTurnSummary, 0)
		run.StageAttempts = append(run.StageAttempts, attempt)
	}
	if err := attemptRows.Err(); err != nil {
		return Run{}, err
	}
	if len(run.StageAttempts) > 0 {
		turnRows, err := s.db.Query(ctx, `
			SELECT t.stage_attempt_id, t.id, t.turn_index, t.turn_kind,
			       t.prompt, t.output_schema, t.completed_response,
			       t.response_hash, t.validation_error, t.provider_error,
			       t.completion_metadata_json, t.provider_stderr_excerpt,
			       t.validation_truncated, t.provider_error_truncated,
			       t.metadata_truncated, t.stderr_truncated,
			       t.started_at, t.completed_at, t.duration_ms, t.status
			FROM analysis_stage_turn t
			JOIN analysis_stage_attempt a ON a.id = t.stage_attempt_id
			WHERE a.run_id = ? ORDER BY a.block_index, t.turn_index
		`, id.String())
		if err != nil {
			return Run{}, err
		}
		defer turnRows.Close()
		byAttempt := make(map[string]int, len(run.StageAttempts))
		for index, attempt := range run.StageAttempts {
			byAttempt[attempt.ID] = index
		}
		for turnRows.Next() {
			var rawAttemptID, rawTurnID string
			var turn StageTurnSummary
			var validationTruncated, providerErrorTruncated, metadataTruncated, stderrTruncated int
			if err := turnRows.Scan(&rawAttemptID, &rawTurnID, &turn.TurnIndex, &turn.TurnKind, &turn.Prompt, &turn.OutputSchema, &turn.CompletedResponse, &turn.ResponseHash, &turn.ValidationError, &turn.ProviderError, &turn.CompletionMetadataJSON, &turn.ProviderStderrExcerpt, &validationTruncated, &providerErrorTruncated, &metadataTruncated, &stderrTruncated, &turn.StartedAt, &turn.CompletedAt, &turn.DurationMS, &turn.Status); err != nil {
				return Run{}, err
			}
			turn.ID = rawTurnID
			turn.ValidationTruncated = validationTruncated == 1
			turn.ProviderErrorTruncated = providerErrorTruncated == 1
			turn.MetadataTruncated = metadataTruncated == 1
			turn.StderrTruncated = stderrTruncated == 1
			if index, ok := byAttempt[rawAttemptID]; ok {
				run.StageAttempts[index].Turns = append(run.StageAttempts[index].Turns, turn)
			}
		}
		if err := turnRows.Err(); err != nil {
			return Run{}, err
		}
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, run_id, block_index, turn_index, turn_kind, prompt,
		       output_schema, completed_response, response_hash, validation_error,
		       provider_error, completion_metadata_json, provider_stderr_excerpt,
		       started_at, completed_at, duration_ms, status
		FROM analysis_turn WHERE run_id = ? ORDER BY block_index, turn_index
	`, id.String())
	if err != nil {
		return Run{}, err
	}
	defer rows.Close()
	run.Turns = make([]Turn, 0)
	for rows.Next() {
		var turn Turn
		var rawTurnID, rawRunID string
		if err := rows.Scan(&rawTurnID, &rawRunID, &turn.BlockIndex, &turn.TurnIndex, &turn.TurnKind, &turn.Prompt, &turn.OutputSchema, &turn.CompletedResponse, &turn.ResponseHash, &turn.ValidationError, &turn.ProviderError, &turn.CompletionMetadataJSON, &turn.ProviderStderrExcerpt, &turn.StartedAt, &turn.CompletedAt, &turn.DurationMS, &turn.Status); err != nil {
			return Run{}, err
		}
		turn.ID = library.ULID(rawTurnID)
		turn.RunID = library.ULID(rawRunID)
		run.Turns = append(run.Turns, turn)
	}
	if err := rows.Err(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func encodeRunCursor(startedAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(startedAt + "\x00" + id))
}

func decodeRunCursor(cursor string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", errors.New("invalid run cursor")
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid run cursor")
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", parts[0]); err != nil {
		// SQLite timestamps are generated by store.NowUTC, but accepting a
		// fractional precision variant keeps cursors portable across old rows.
		if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
			return "", "", errors.New("invalid run cursor timestamp")
		}
	}
	if len(parts[1]) < 1 || strings.ContainsAny(parts[1], "\r\n") {
		return "", "", errors.New("invalid run cursor id")
	}
	return parts[0], parts[1], nil
}

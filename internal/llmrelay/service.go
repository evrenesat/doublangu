package llmrelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

// PresenceWindow is how recently a relay lane must have been seen for the
// relay to count as available. TTS traffic never refreshes relay presence.
const PresenceWindow = 2 * time.Minute

// PollInterval is the durable wait-loop period for one child job/result.
const PollInterval = 250 * time.Millisecond

// Service owns relay enqueue, the durable result wait, worker availability,
// and atomic result persistence. All durable state lives in SQLite.
type Service struct {
	db   *store.DB
	jobs *jobs.Store
}

// NewService creates the relay service over one database. Its job store
// carries no terminal-recovery callback: relay jobs own no domain state
// beyond their result row.
func NewService(db *store.DB) *Service {
	return &Service{db: db, jobs: jobs.NewStore(db)}
}

// Available reports whether a relay worker is currently present: a
// non-revoked worker with an enrolled relay capability seen on the relay
// lane within the presence window.
func (s *Service) Available(ctx context.Context) bool {
	if s == nil || s.db == nil {
		return false
	}
	cutoff := time.Now().UTC().Add(-PresenceWindow).Format("2006-01-02T15:04:05.000Z")
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM speech_worker WHERE revoked_at = '' AND llm_relay_capabilities_json <> '[]' AND relay_last_seen_at > ?`, cutoff).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// BuildChatCompletion marshals one `chat_completion` request, rejects
// oversized payloads, and returns the exact bytes with their input hash.
// Every logical relay operation gets a fresh request id, so the hash — and
// therefore the job — is fresh as well.
func BuildChatCompletion(requestID library.ULID, model string, messages []Message, schema json.RawMessage, temperatureMilli, maxOutputTokens int) (payload []byte, inputHash string, err error) {
	if requestID.IsZero() {
		return nil, "", errors.New("llmrelay request id is required")
	}
	schema = append([]byte(nil), bytes.TrimSpace(schema)...)
	request := ChatCompletionRequest{
		ProtocolVersion: ProtocolVersion, Operation: OperationChatCompletion,
		RequestID: requestID.String(), Model: model, Messages: messages,
		Options: Options{TemperatureMilli: temperatureMilli, MaxOutputTokens: maxOutputTokens},
		Limits:  Limits{MaxCompletionBytes: MaxCompletionBytes},
	}
	request.ResponseFormat.Type = "json_schema"
	request.ResponseFormat.JSONSchema.Name = ResponseSchemaName
	request.ResponseFormat.JSONSchema.Strict = true
	request.ResponseFormat.JSONSchema.Schema = schema
	if err := request.Validate(); err != nil {
		return nil, "", err
	}
	payload, err = json.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	if len(payload) > PayloadLimitBytes {
		return nil, "", fmt.Errorf("llmrelay chat_completion payload exceeds the %d-byte limit", PayloadLimitBytes)
	}
	return payload, HashRequest(payload), nil
}

// BuildListModels marshals one `list_models` request with its input hash.
func BuildListModels(requestID library.ULID) (payload []byte, inputHash string, err error) {
	if requestID.IsZero() {
		return nil, "", errors.New("llmrelay request id is required")
	}
	request := ListModelsRequest{
		ProtocolVersion: ProtocolVersion, Operation: OperationListModels,
		RequestID: requestID.String(), Limits: Limits{MaxCompletionBytes: MaxCompletionBytes},
	}
	payload, err = json.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	if len(payload) > PayloadLimitBytes {
		return nil, "", fmt.Errorf("llmrelay list_models payload exceeds the %d-byte limit", PayloadLimitBytes)
	}
	return payload, HashRequest(payload), nil
}

// Enqueue inserts (or idempotently returns) the relay child job for exact
// payload bytes. Generic jobs idempotency is unchanged.
func (s *Service) Enqueue(ctx context.Context, payload []byte, inputHash, requestID string) (*jobs.Job, error) {
	if s == nil || s.db == nil || s.jobs == nil {
		return nil, errors.New("llmrelay: nil database")
	}
	if len(payload) == 0 || len(payload) > PayloadLimitBytes {
		return nil, fmt.Errorf("llmrelay payload must be 1..%d bytes", PayloadLimitBytes)
	}
	if requestID == "" || inputHash == "" {
		return nil, errors.New("llmrelay request id and input hash are required")
	}
	return s.jobs.Enqueue(ctx, jobs.Spec{
		JobType: jobs.LLMRelayJobType, ExecutionTarget: jobs.TargetMacOS,
		OwnerType: OwnerType, OwnerID: requestID,
		IdempotencyKey: IdempotencyKey(inputHash), InputHash: inputHash,
		PayloadJSON: string(payload), MaxAttempts: 3,
	})
}

// StoredResult is one persisted terminal relay outcome.
type StoredResult struct {
	JobID      library.ULID
	Operation  string
	ResultJSON string
}

// TerminalError carries the stable code of a terminally failed or canceled
// relay job.
type TerminalError struct {
	JobID library.ULID
	Code  string
}

func (e *TerminalError) Error() string {
	if e == nil {
		return "llmrelay terminal failure"
	}
	return fmt.Sprintf("llmrelay job %s failed: %s", e.JobID.String(), e.Code)
}

// Wait blocks on one child job and its result row, polling every 250 ms.
// It exits on terminal state or context cancellation and cancels the child
// on parent cancellation. A queued job whose relay presence disappeared is
// canceled and reported unavailable; an expired leased/running job is
// recovered through the narrow per-job helper, never the global sweep.
func (s *Service) Wait(ctx context.Context, jobID library.ULID) (*StoredResult, error) {
	if s == nil || s.db == nil || s.jobs == nil || jobID.IsZero() {
		return nil, errors.New("llmrelay: nil database")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			// Detach the cancellation: the child must be canceled even
			// though the parent context is already done.
			_ = s.jobs.Cancel(context.WithoutCancel(ctx), jobID, CodeParentCanceled)
			return nil, fmt.Errorf("llmrelay wait canceled: %w", ctx.Err())
		case <-timer.C:
		}
		job, err := s.jobs.Get(ctx, jobID)
		if err != nil {
			return nil, fmt.Errorf("llmrelay wait: %w", err)
		}
		switch job.State {
		case jobs.StateSucceeded:
			result, err := s.GetResult(ctx, jobID)
			if err != nil {
				return nil, err
			}
			return result, nil
		case jobs.StateFailed, jobs.StateCanceled:
			return nil, &TerminalError{JobID: jobID, Code: job.ErrorCode}
		case jobs.StateQueued:
			if !s.Available(ctx) {
				_ = s.jobs.Cancel(context.WithoutCancel(ctx), jobID, CodeParentCanceled)
				return nil, &Error{Code: CodeUnavailable, Err: errors.New("no relay-capable worker is present")}
			}
		case jobs.StateLeased, jobs.StateRunning:
			if _, err := s.jobs.RecoverExpiredJob(ctx, jobID); err != nil {
				return nil, fmt.Errorf("llmrelay wait: %w", err)
			}
		default:
			return nil, fmt.Errorf("llmrelay job %s is in unexpected state %q", jobID.String(), job.State)
		}
		timer.Reset(PollInterval)
	}
}

// GetResult loads the persisted result for a job.
func (s *Service) GetResult(ctx context.Context, jobID library.ULID) (*StoredResult, error) {
	if s == nil || s.db == nil || jobID.IsZero() {
		return nil, errors.New("llmrelay: nil database")
	}
	var result StoredResult
	var rawID string
	if err := s.db.QueryRow(ctx, `SELECT job_id, operation, result_json FROM llm_relay_result WHERE job_id = ?`, jobID.String()).Scan(&rawID, &result.Operation, &result.ResultJSON); err != nil {
		return nil, fmt.Errorf("llmrelay result for job %s: %w", jobID.String(), err)
	}
	parsed, err := library.ParseULID(rawID)
	if err != nil {
		return nil, err
	}
	result.JobID = parsed
	return &result, nil
}

// CompleteTx validates a worker result and commits it atomically with the
// job success transition. A duplicate completion carrying the same canonical
// bytes is accepted idempotently; a different second result returns a
// *NondeterministicError and the first accepted result stays authoritative.
func (s *Service) CompleteTx(ctx context.Context, tx *sql.Tx, job jobs.Job, attempt int, token string, result []byte) error {
	if s == nil || tx == nil {
		return errors.New("llmrelay: nil completion transaction")
	}
	if job.JobType != jobs.LLMRelayJobType {
		return fmt.Errorf("llmrelay cannot complete job type %q", job.JobType)
	}
	if job.OwnerType != OwnerType || job.OwnerID == "" {
		return errors.New("llmrelay job has an invalid relay owner")
	}
	if int64(len(result)) > ResultLimitBytes {
		return fmt.Errorf("llmrelay result exceeds the %d-byte hard limit", ResultLimitBytes)
	}
	payload, err := DecodeRelayPayload([]byte(job.PayloadJSON))
	if err != nil {
		return err
	}
	maxBytes := payload.Limits.MaxCompletionBytes
	if maxBytes <= 0 || maxBytes > ResultLimitBytes {
		return fmt.Errorf("llmrelay job bound exceeds the %d-byte hard limit", ResultLimitBytes)
	}
	var canonical []byte
	var operation string
	switch payload.Operation {
	case OperationChatCompletion:
		decoded, err := DecodeChatResult(result, payload.RequestID, maxBytes)
		if err != nil {
			return err
		}
		operation = OperationChatCompletion
		canonical, err = CanonicalChatResult(decoded)
		if err != nil {
			return err
		}
	case OperationListModels:
		decoded, err := DecodeListModelsResult(result, payload.RequestID, maxBytes)
		if err != nil {
			return err
		}
		operation = OperationListModels
		canonical, err = CanonicalListModelsResult(decoded)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("llmrelay unsupported operation %q", payload.Operation)
	}
	sum := sha256.Sum256(canonical)
	resultHash := hex.EncodeToString(sum[:])
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT result_hash FROM llm_relay_result WHERE job_id = ?`, job.ID.String()).Scan(&existingHash)
	switch {
	case err == nil:
		if existingHash != resultHash {
			return &NondeterministicError{JobID: job.ID.String()}
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO llm_relay_result (job_id, input_hash, operation, result_json, result_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`, job.ID.String(), job.InputHash, operation, string(canonical), resultHash, store.NowUTC()); err != nil {
			return fmt.Errorf("llmrelay persist result: %w", err)
		}
	default:
		return fmt.Errorf("llmrelay load result: %w", err)
	}
	return jobs.CompleteTx(ctx, tx, job.ID, attempt, token)
}

// NondeterministicError reports a second completion whose bytes differ from
// the accepted result.
type NondeterministicError struct {
	JobID string
}

func (e *NondeterministicError) Error() string {
	return fmt.Sprintf("llmrelay job %s already has a different accepted result", e.JobID)
}

// RelayPayload is the decoded relay job payload: operation, request id, and
// byte bound.
type RelayPayload struct {
	Operation string
	RequestID string
	Limits    Limits
}

// DecodeRelayPayload strictly decodes a relay job payload, dispatching on
// operation.
func DecodeRelayPayload(data []byte) (*RelayPayload, error) {
	// The operation probe is intentionally lenient: it only dispatches.
	// Each operation validator strictly decodes the full payload next.
	var probe struct {
		ProtocolVersion string `json:"protocol_version"`
		Operation       string `json:"operation"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("llmrelay decode payload: %w", err)
	}
	switch probe.Operation {
	case OperationChatCompletion:
		request, err := DecodeChatCompletionRequest(data)
		if err != nil {
			return nil, err
		}
		return &RelayPayload{Operation: request.Operation, RequestID: request.RequestID, Limits: request.Limits}, nil
	case OperationListModels:
		request, err := DecodeListModelsRequest(data)
		if err != nil {
			return nil, err
		}
		return &RelayPayload{Operation: request.Operation, RequestID: request.RequestID, Limits: request.Limits}, nil
	default:
		return nil, fmt.Errorf("llmrelay unsupported operation %q", probe.Operation)
	}
}

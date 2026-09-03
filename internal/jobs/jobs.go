// Package jobs provides the SQLite-backed durable scheduler shared by server
// analysis and outbound speech workers. The database conditional updates are
// the authority; no process-local lock is used to claim work.
package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

const (
	AnalysisJobType   = "reader.analysis.v2"
	AVSpeechJobType   = "tts.avspeech.v1"
	ChatterboxJobType = "tts.chatterbox.v3"
	LLMRelayJobType   = "llm.relay.v1"

	TargetServer = "server"
	TargetMacOS  = "macos"

	LeaseExpiredErrorCode = "v1.job_lease_expired"

	StateQueued    = "queued"
	StateLeased    = "leased"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCanceled  = "canceled"

	LeaseDuration  = 90 * time.Second
	HeartbeatEvery = 30 * time.Second
)

var retryBackoff = [...]time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

var (
	ErrNoWork       = errors.New("jobs: no leaseable work")
	ErrLeaseLost    = errors.New("jobs: lease is no longer valid")
	ErrLeaseExpired = errors.New("jobs: lease expired")
	ErrCycle        = errors.New("jobs: dependency cycle")
	ErrInvalidJob   = errors.New("jobs: invalid job")
)

type Error struct {
	Op   string
	Kind string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("jobs %s: %s", e.Op, e.Kind)
	}
	return fmt.Sprintf("jobs %s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

type Job struct {
	ID              library.ULID `json:"id"`
	JobType         string       `json:"job_type"`
	ExecutionTarget string       `json:"execution_target"`
	OwnerType       string       `json:"owner_type"`
	OwnerID         string       `json:"owner_id"`
	IdempotencyKey  string       `json:"idempotency_key"`
	InputHash       string       `json:"input_hash"`
	PayloadJSON     string       `json:"payload_json"`
	State           string       `json:"state"`
	Priority        int          `json:"priority"`
	AttemptCount    int          `json:"attempt_count"`
	MaxAttempts     int          `json:"max_attempts"`
	AvailableAt     string       `json:"available_at"`
	LeaseOwner      string       `json:"lease_owner"`
	LeaseExpiresAt  string       `json:"lease_expires_at"`
	ProgressPercent int          `json:"progress_percent"`
	ErrorCode       string       `json:"error_code"`
	CreatedAt       string       `json:"created_at"`
	UpdatedAt       string       `json:"updated_at"`
	StartedAt       string       `json:"started_at"`
	CompletedAt     string       `json:"completed_at"`
	leaseTokenHash  string
}

type Spec struct {
	ID              library.ULID
	JobType         string
	ExecutionTarget string
	OwnerType       string
	OwnerID         string
	IdempotencyKey  string
	InputHash       string
	PayloadJSON     string
	Priority        int
	MaxAttempts     int
	AvailableAt     string
}

type Lease struct {
	Job
	LeaseToken string `json:"lease_token"`
}

type HeartbeatResult struct {
	CancelRequested bool
	Job             Job
}

// TerminalJobRecovery synchronizes domain-owned state after the scheduler
// terminally fails an expired job. It runs inside the same transaction as the
// job update, so a recovered job cannot commit without its dependent state.
type TerminalJobRecovery func(context.Context, *sql.Tx, Job) error

type Store struct {
	db               *store.DB
	terminalRecovery TerminalJobRecovery
}

func NewStore(db *store.DB, terminalRecovery ...TerminalJobRecovery) *Store {
	var recovery TerminalJobRecovery
	if len(terminalRecovery) > 0 {
		recovery = terminalRecovery[0]
	}
	return &Store{db: db, terminalRecovery: recovery}
}

func (s *Store) Enqueue(ctx context.Context, spec Spec) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("jobs: nil database")
	}
	if err := validateSpec(&spec); err != nil {
		return nil, &Error{Op: "enqueue", Kind: "validation", Err: err}
	}
	var job *Job
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		job, err = enqueueTx(ctx, tx, spec)
		return err
	})
	return job, err
}

// EnqueueTx inserts or returns the existing job for an idempotency key. It is
// used by article/audio transactions so metadata and work become visible
// together.
func EnqueueTx(ctx context.Context, tx *sql.Tx, spec Spec) (*Job, error) {
	if err := validateSpec(&spec); err != nil {
		return nil, &Error{Op: "enqueue", Kind: "validation", Err: err}
	}
	return enqueueTx(ctx, tx, spec)
}

func enqueueTx(ctx context.Context, tx *sql.Tx, spec Spec) (*Job, error) {
	if spec.ID.IsZero() {
		spec.ID = library.NewULID()
	}
	if spec.AvailableAt == "" {
		spec.AvailableAt = store.NowUTC()
	}
	if spec.MaxAttempts == 0 {
		spec.MaxAttempts = 3
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO job (id, job_type, execution_target, owner_type, owner_id,
			idempotency_key, input_hash, payload_json, state, priority, max_attempts,
			available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING
	`, spec.ID.String(), spec.JobType, spec.ExecutionTarget, spec.OwnerType, spec.OwnerID,
		spec.IdempotencyKey, spec.InputHash, spec.PayloadJSON, spec.Priority, spec.MaxAttempts,
		spec.AvailableAt, store.NowUTC(), store.NowUTC())
	if err != nil {
		return nil, fmt.Errorf("jobs enqueue: %w", err)
	}
	return getTx(ctx, tx, spec.IdempotencyKey)
}

func (s *Store) Get(ctx context.Context, id library.ULID) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("jobs: nil database")
	}
	return s.get(ctx, "id", id.String())
}

func (s *Store) GetByIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("jobs: nil database")
	}
	return s.get(ctx, "idempotency_key", key)
}

// GetByIdempotencyKeyTx reads a job inside an existing transaction. It is used
// by article/audio composition when an idempotent retry must return the
// already-created work without inserting a placeholder row.
func GetByIdempotencyKeyTx(ctx context.Context, tx *sql.Tx, key string) (*Job, error) {
	if tx == nil || strings.TrimSpace(key) == "" {
		return nil, &Error{Op: "get", Kind: "validation", Err: ErrInvalidJob}
	}
	return getTx(ctx, tx, key)
}

// GetActiveOwnerJobTx returns the current queued/leased/running job for an
// owner. It lets idempotent callers return an already-snapshotted job even if
// the singleton settings changed while that job was active.
func GetActiveOwnerJobTx(ctx context.Context, tx *sql.Tx, ownerType, ownerID, jobType string) (*Job, error) {
	if tx == nil || strings.TrimSpace(ownerType) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(jobType) == "" {
		return nil, &Error{Op: "get active owner job", Kind: "validation", Err: ErrInvalidJob}
	}
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE owner_type = ? AND owner_id = ? AND job_type = ? AND state IN ('queued', 'leased', 'running') ORDER BY created_at DESC, id DESC LIMIT 1`, ownerType, ownerID, jobType))
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Store) get(ctx context.Context, column, value string) (*Job, error) {
	if column != "id" && column != "idempotency_key" {
		return nil, ErrInvalidJob
	}
	row := s.db.QueryRow(ctx, jobSelect+" WHERE "+column+" = ?", value)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &Error{Op: "get", Kind: "not_found", Err: sql.ErrNoRows}
	}
	if err != nil {
		return nil, fmt.Errorf("jobs get: %w", err)
	}
	return job, nil
}

func getTx(ctx context.Context, tx *sql.Tx, key string) (*Job, error) {
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+" WHERE idempotency_key = ?", key))
	if err != nil {
		return nil, fmt.Errorf("jobs get enqueued: %w", err)
	}
	return job, nil
}

// AddDependency rejects self edges and any edge that would make the directed
// dependency graph cyclic. Both jobs must already exist.
func (s *Store) AddDependency(ctx context.Context, jobID, dependencyID library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("jobs: nil database")
	}
	if jobID.IsZero() || dependencyID.IsZero() || jobID == dependencyID {
		return &Error{Op: "add dependency", Kind: "validation", Err: ErrCycle}
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM job WHERE id IN (?, ?)", jobID.String(), dependencyID.String()).Scan(&count); err != nil {
			return fmt.Errorf("jobs dependency lookup: %w", err)
		}
		if count != 2 {
			return &Error{Op: "add dependency", Kind: "not_found", Err: sql.ErrNoRows}
		}
		var reaches int
		if err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE ancestors(id) AS (
				SELECT dependency_job_id FROM job_dependency WHERE job_id = ?
				UNION
				SELECT d.dependency_job_id FROM job_dependency d JOIN ancestors a ON d.job_id = a.id
			)
			SELECT EXISTS(SELECT 1 FROM ancestors WHERE id = ?)
		`, dependencyID.String(), jobID.String()).Scan(&reaches); err != nil {
			return fmt.Errorf("jobs dependency cycle check: %w", err)
		}
		if reaches != 0 {
			return &Error{Op: "add dependency", Kind: "conflict", Err: ErrCycle}
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO job_dependency (job_id, dependency_job_id) VALUES (?, ?)`, jobID.String(), dependencyID.String())
		return err
	})
}

// Claim performs one conditional SQLite update. A worker process may safely
// race another worker or a second server instance without an in-memory lock.
func (s *Store) Claim(ctx context.Context, executionTarget, leaseOwner string) (*Lease, error) {
	return s.ClaimMatching(ctx, executionTarget, leaseOwner, nil)
}

// ClaimMatching is Claim with a deterministic payload predicate. The
// conditional update remains the authority; the predicate only prevents a
// worker from taking a queued job its advertised capabilities cannot execute.
func (s *Store) ClaimMatching(ctx context.Context, executionTarget, leaseOwner string, matches func(Job) bool) (*Lease, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("jobs: nil database")
	}
	if executionTarget != TargetServer && executionTarget != TargetMacOS || strings.TrimSpace(leaseOwner) == "" {
		return nil, &Error{Op: "claim", Kind: "validation", Err: ErrInvalidJob}
	}
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("jobs claim token: %w", err)
	}
	now := store.NowUTC()
	expires := time.Now().UTC().Add(LeaseDuration).Format("2006-01-02T15:04:05.000Z")
	hash := hashToken(token)
	var lease *Lease
	noWork := false
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := reconcileDependencyFailuresTx(ctx, tx, now); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, jobSelect+`
			WHERE execution_target = ? AND state = 'queued'
			  AND available_at <= ? AND attempt_count < max_attempts
			  AND NOT EXISTS (
				SELECT 1 FROM job_dependency jd JOIN job d ON d.id = jd.dependency_job_id
				WHERE jd.job_id = job.id AND d.state <> 'succeeded'
			  )
			ORDER BY priority DESC, created_at ASC, id ASC`, executionTarget, now)
		if err != nil {
			return fmt.Errorf("jobs choose claim: %w", err)
		}
		var candidate *Job
		for rows.Next() {
			job, scanErr := scanJob(rows)
			if scanErr != nil {
				rows.Close()
				return fmt.Errorf("jobs scan claim candidate: %w", scanErr)
			}
			if matches == nil || matches(*job) {
				candidate = job
				break
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if candidate == nil {
			noWork = true
			return nil
		}
		id := candidate.ID.String()
		result, err := tx.ExecContext(ctx, `
			UPDATE job SET state = 'leased', attempt_count = attempt_count + 1,
				lease_owner = ?, lease_token_hash = ?, lease_expires_at = ?,
				progress_percent = 0, started_at = CASE WHEN started_at = '' THEN ? ELSE started_at END,
				updated_at = ?
			WHERE id = ? AND execution_target = ? AND state = 'queued'
			  AND available_at <= ? AND attempt_count < max_attempts
			  AND NOT EXISTS (
				SELECT 1 FROM job_dependency jd JOIN job d ON d.id = jd.dependency_job_id
				WHERE jd.job_id = job.id AND d.state <> 'succeeded'
			  )
		`, leaseOwner, hash, expires, now, now, id, executionTarget, now)
		if err != nil {
			return fmt.Errorf("jobs claim update: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			noWork = true
			return nil
		}
		job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+" WHERE id = ?", id))
		if err != nil {
			return err
		}
		lease = &Lease{Job: *job, LeaseToken: token}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if noWork {
		return nil, ErrNoWork
	}
	return lease, nil
}

// VerifyLease checks a worker's attempt without changing it. It is used by
// multipart completion so bytes cannot be accepted for a stale or another
// worker's lease.
func (s *Store) VerifyLease(ctx context.Context, id library.ULID, attempt int, token, owner string) (*Job, error) {
	if s == nil || s.db == nil || id.IsZero() || attempt <= 0 || token == "" || strings.TrimSpace(owner) == "" {
		return nil, ErrLeaseLost
	}
	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.AttemptCount != attempt || !constantTokenMatch(token, job.leaseTokenHash) {
		return nil, ErrLeaseLost
	}
	if job.State != StateLeased && job.State != StateRunning {
		if job.State == StateSucceeded || (job.LeaseOwner == owner && (job.State == StateCanceled || job.State == StateFailed || job.State == StateQueued)) {
			return job, nil
		}
		return nil, ErrLeaseLost
	}
	if job.LeaseOwner != owner {
		return nil, ErrLeaseLost
	}
	expires, parseErr := time.Parse("2006-01-02T15:04:05.000Z", job.LeaseExpiresAt)
	if parseErr != nil || !time.Now().UTC().Before(expires) {
		return nil, ErrLeaseExpired
	}
	return job, nil
}

func (s *Store) Heartbeat(ctx context.Context, id library.ULID, attempt int, token string, progress int) (*HeartbeatResult, error) {
	if progress < 0 || progress > 100 || token == "" {
		return nil, &Error{Op: "heartbeat", Kind: "validation", Err: ErrInvalidJob}
	}
	now := store.NowUTC()
	expires := time.Now().UTC().Add(LeaseDuration).Format("2006-01-02T15:04:05.000Z")
	var result *HeartbeatResult
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+" WHERE id = ?", id.String()))
		if err != nil {
			return &Error{Op: "heartbeat", Kind: "not_found", Err: err}
		}
		if job.AttemptCount != attempt || !constantTokenMatch(token, job.leaseTokenHash) {
			return ErrLeaseLost
		}
		if job.State == StateSucceeded {
			result = &HeartbeatResult{Job: *job}
			return nil
		}
		if job.State == StateCanceled || job.State == StateFailed {
			result = &HeartbeatResult{CancelRequested: true, Job: *job}
			return nil
		}
		if job.State != StateLeased && job.State != StateRunning {
			return ErrLeaseLost
		}
		if !jobLeaseLive(job) {
			return ErrLeaseExpired
		}
		_, err = tx.ExecContext(ctx, `UPDATE job SET state = 'running', lease_expires_at = ?, progress_percent = ?, updated_at = ? WHERE id = ? AND attempt_count = ? AND lease_token_hash = ? AND state IN ('leased', 'running')`, expires, progress, now, id.String(), attempt, hashToken(token))
		if err != nil {
			return err
		}
		updated, err := scanJob(tx.QueryRowContext(ctx, jobSelect+" WHERE id = ?", id.String()))
		if err != nil {
			return err
		}
		result = &HeartbeatResult{Job: *updated}
		return nil
	})
	return result, err
}

// Complete is idempotent for a matching completed attempt and lease token.
func (s *Store) Complete(ctx context.Context, id library.ULID, attempt int, token string) error {
	return s.finish(ctx, id, attempt, token, true, "")
}

// CompleteTx is the transaction-boundary variant used when a media blob,
// render reference, and job acknowledgement must commit together.
func CompleteTx(ctx context.Context, tx *sql.Tx, id library.ULID, attempt int, token string) error {
	return finishTx(ctx, tx, id, attempt, token, true, "")
}

// Fail records a stable code and, when retry is true, schedules the next
// automatic attempt with the 5s/30s/2m backoff. The fourth attempt is never
// automatically created.
func (s *Store) Fail(ctx context.Context, id library.ULID, attempt int, token, code string, retry bool) error {
	if !validErrorCode(code) {
		return &Error{Op: "fail", Kind: "validation", Err: ErrInvalidJob}
	}
	return s.finish(ctx, id, attempt, token, false, code+"\x00"+fmt.Sprint(retry))
}

func (s *Store) finish(ctx context.Context, id library.ULID, attempt int, token string, success bool, failure string) error {
	if s == nil || s.db == nil || token == "" {
		return ErrLeaseLost
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		return finishTx(ctx, tx, id, attempt, token, success, failure)
	})
}

func finishTx(ctx context.Context, tx *sql.Tx, id library.ULID, attempt int, token string, success bool, failure string) error {
	if tx == nil || token == "" {
		return ErrLeaseLost
	}
	now := store.NowUTC()
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+" WHERE id = ?", id.String()))
	if err != nil {
		return &Error{Op: "finish", Kind: "not_found", Err: err}
	}
	if job.AttemptCount != attempt || !constantTokenMatch(token, job.leaseTokenHash) {
		return ErrLeaseLost
	}
	if success && job.State == StateSucceeded {
		return nil
	}
	if !success && (job.State == StateFailed || job.State == StateCanceled || job.State == StateQueued) {
		return nil
	}
	if success {
		if job.State != StateLeased && job.State != StateRunning {
			return ErrLeaseLost
		}
		if !jobLeaseLive(job) {
			return ErrLeaseExpired
		}
		_, err = tx.ExecContext(ctx, `UPDATE job SET state = 'succeeded', progress_percent = 100, error_code = '', lease_expires_at = '', completed_at = ?, updated_at = ? WHERE id = ? AND attempt_count = ? AND lease_token_hash = ? AND state IN ('leased', 'running')`, now, now, id.String(), attempt, hashToken(token))
		return err
	}
	parts := strings.SplitN(failure, "\x00", 2)
	retry := len(parts) == 2 && parts[1] == "true"
	state := StateFailed
	available := now
	if retry && attempt < job.MaxAttempts {
		state = StateQueued
		available = time.Now().UTC().Add(backoffForAttempt(attempt)).Format("2006-01-02T15:04:05.000Z")
	}
	// Keep the owner and hashed token after a worker-reported failure. The state
	// and cleared expiry prevent any further live mutation, while retaining the
	// attempt credential lets an exact duplicate failure acknowledge the same
	// outcome idempotently before a later retry or owner requeue overwrites it.
	_, err = tx.ExecContext(ctx, `UPDATE job SET state = ?, available_at = ?, error_code = ?, lease_expires_at = '', updated_at = ?, completed_at = CASE WHEN ? = 'failed' THEN ? ELSE completed_at END WHERE id = ? AND attempt_count = ? AND lease_token_hash = ? AND state IN ('leased', 'running')`, state, available, parts[0], now, state, now, id.String(), attempt, hashToken(token))
	return err
}

func (s *Store) Cancel(ctx context.Context, id library.ULID, code string) error {
	if s == nil || s.db == nil {
		return errors.New("jobs: nil database")
	}
	if !validErrorCode(code) {
		return &Error{Op: "cancel", Kind: "validation", Err: ErrInvalidJob}
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error { return CancelTx(ctx, tx, id, code) })
}

func (s *Store) CancelOwnerJobs(ctx context.Context, ownerType, ownerID, jobType, code string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("jobs: nil database")
	}
	if !validErrorCode(code) {
		return 0, &Error{Op: "cancel owner jobs", Kind: "validation", Err: ErrInvalidJob}
	}
	var count int64
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		count, err = CancelOwnerJobsTx(ctx, tx, ownerType, ownerID, jobType, code)
		return err
	})
	return count, err
}

// CancelTx cancels one job while retaining an active attempt's owner, token
// hash, and original expiry. The matching worker can therefore observe the
// cancellation on its next heartbeat, but completion remains forbidden.
func CancelTx(ctx context.Context, tx *sql.Tx, id library.ULID, code string) error {
	if tx == nil || id.IsZero() || !validErrorCode(code) {
		return &Error{Op: "cancel", Kind: "validation", Err: ErrInvalidJob}
	}
	now := store.NowUTC()
	if result, err := tx.ExecContext(ctx, `UPDATE job SET state = 'canceled', error_code = ?, lease_owner = '', lease_token_hash = '', lease_expires_at = '', updated_at = ?, completed_at = ? WHERE id = ? AND state = 'queued'`, code, now, now, id.String()); err != nil {
		return err
	} else if count, _ := result.RowsAffected(); count == 1 {
		return nil
	}
	if result, err := tx.ExecContext(ctx, `UPDATE job SET state = 'canceled', error_code = ?, updated_at = ?, completed_at = ? WHERE id = ? AND state IN ('leased', 'running')`, code, now, now, id.String()); err != nil {
		return err
	} else if count, _ := result.RowsAffected(); count == 1 {
		return nil
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM job WHERE id = ?`, id.String()).Scan(&state); err != nil {
		return err
	}
	if state == StateCanceled {
		return nil
	}
	return ErrLeaseLost
}

// CancelOwnerJobsTx is the transaction-boundary form used by article and
// narration teardown. It returns the number of rows newly canceled.
func CancelOwnerJobsTx(ctx context.Context, tx *sql.Tx, ownerType, ownerID, jobType, code string) (int64, error) {
	if tx == nil || strings.TrimSpace(ownerType) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(jobType) == "" || !validErrorCode(code) {
		return 0, &Error{Op: "cancel owner jobs", Kind: "validation", Err: ErrInvalidJob}
	}
	now := store.NowUTC()
	queued, err := tx.ExecContext(ctx, `UPDATE job SET state = 'canceled', error_code = ?, lease_owner = '', lease_token_hash = '', lease_expires_at = '', updated_at = ?, completed_at = ? WHERE owner_type = ? AND owner_id = ? AND job_type = ? AND state = 'queued'`, code, now, now, ownerType, ownerID, jobType)
	if err != nil {
		return 0, err
	}
	active, err := tx.ExecContext(ctx, `UPDATE job SET state = 'canceled', error_code = ?, updated_at = ?, completed_at = ? WHERE owner_type = ? AND owner_id = ? AND job_type = ? AND state IN ('leased', 'running')`, code, now, now, ownerType, ownerID, jobType)
	if err != nil {
		return 0, err
	}
	queuedCount, _ := queued.RowsAffected()
	activeCount, _ := active.RowsAffected()
	return queuedCount + activeCount, nil
}

// RecoverExpired returns expired work to the queue or marks the third failed
// attempt terminal. It is safe to run at startup and periodically.
func (s *Store) RecoverExpired(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("jobs: nil database")
	}
	var affected int64
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		now := store.NowUTC()
		rows, err := tx.QueryContext(ctx, `SELECT id, job_type, owner_type, owner_id, attempt_count, max_attempts FROM job WHERE state IN ('leased', 'running') AND lease_expires_at <> '' AND lease_expires_at <= ?`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		type expiredJob struct {
			id                          string
			jobType, ownerType, ownerID string
			attempt, max                int
		}
		var expired []expiredJob
		for rows.Next() {
			var item expiredJob
			if err := rows.Scan(&item.id, &item.jobType, &item.ownerType, &item.ownerID, &item.attempt, &item.max); err != nil {
				return err
			}
			expired = append(expired, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range expired {
			state := StateFailed
			available := now
			if item.attempt < item.max {
				state = StateQueued
				available = time.Now().UTC().Add(backoffForAttempt(item.attempt)).Format("2006-01-02T15:04:05.000Z")
			}
			result, err := tx.ExecContext(ctx, `UPDATE job SET state = ?, available_at = ?, error_code = ?, lease_owner = '', lease_token_hash = '', lease_expires_at = '', updated_at = ?, completed_at = CASE WHEN ? = 'failed' THEN ? ELSE completed_at END WHERE id = ? AND state IN ('leased', 'running') AND lease_expires_at <= ?`, state, available, LeaseExpiredErrorCode, now, state, now, item.id, now)
			if err != nil {
				return err
			}
			count, _ := result.RowsAffected()
			affected += count
			if count == 1 && state == StateFailed && s.terminalRecovery != nil {
				if err := s.terminalRecovery(ctx, tx, Job{
					ID: library.ULID(item.id), JobType: item.jobType, OwnerType: item.ownerType, OwnerID: item.ownerID,
					State: state, AttemptCount: item.attempt, MaxAttempts: item.max, ErrorCode: LeaseExpiredErrorCode,
				}); err != nil {
					return err
				}
			}
		}
		return reconcileDependencyFailuresTx(ctx, tx, now)
	})
	return affected, err
}

// RecoverExpiredJob applies the same expired-lease retry/backoff transition
// as RecoverExpired, but only to the named job. The relay wait loop uses it
// while blocked on one child job so unrelated speech work is never recovered
// without its domain callback. It reports whether the job was expired and
// transitioned; a job that is queued, terminal, live, or missing reports
// false without an error.
func (s *Store) RecoverExpiredJob(ctx context.Context, id library.ULID) (bool, error) {
	if s == nil || s.db == nil || id.IsZero() {
		return false, &Error{Op: "recover expired job", Kind: "validation", Err: ErrInvalidJob}
	}
	recovered := false
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		now := store.NowUTC()
		var jobType, ownerType, ownerID string
		var attempt, max int
		var state, leaseExpiresAt string
		err := tx.QueryRowContext(ctx, `SELECT job_type, owner_type, owner_id, attempt_count, max_attempts, state, lease_expires_at FROM job WHERE id = ?`, id.String()).Scan(&jobType, &ownerType, &ownerID, &attempt, &max, &state, &leaseExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state != StateLeased && state != StateRunning {
			return nil
		}
		if leaseExpiresAt == "" || leaseExpiresAt > now {
			return nil
		}
		next := StateFailed
		available := now
		if attempt < max {
			next = StateQueued
			available = time.Now().UTC().Add(backoffForAttempt(attempt)).Format("2006-01-02T15:04:05.000Z")
		}
		result, err := tx.ExecContext(ctx, `UPDATE job SET state = ?, available_at = ?, error_code = ?, lease_owner = '', lease_token_hash = '', lease_expires_at = '', updated_at = ?, completed_at = CASE WHEN ? = 'failed' THEN ? ELSE completed_at END WHERE id = ? AND state IN ('leased', 'running') AND lease_expires_at <= ?`, next, available, LeaseExpiredErrorCode, now, next, now, id.String(), now)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return nil
		}
		recovered = true
		if next == StateFailed && s.terminalRecovery != nil {
			if err := s.terminalRecovery(ctx, tx, Job{
				ID: id, JobType: jobType, OwnerType: ownerType, OwnerID: ownerID,
				State: next, AttemptCount: attempt, MaxAttempts: max, ErrorCode: LeaseExpiredErrorCode,
			}); err != nil {
				return err
			}
		}
		return reconcileDependencyFailuresTx(ctx, tx, now)
	})
	return recovered, err
}

func reconcileDependencyFailuresTx(ctx context.Context, tx *sql.Tx, now string) error {
	for {
		result, err := tx.ExecContext(ctx, `
			UPDATE job SET state = 'failed', error_code = 'v1.job_dependency_failed',
				completed_at = ?, updated_at = ?
			WHERE state = 'queued' AND EXISTS (
				SELECT 1 FROM job_dependency jd JOIN job d ON d.id = jd.dependency_job_id
				WHERE jd.job_id = job.id AND d.state IN ('failed', 'canceled')
			)
		`, now, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return err
		}
	}
}

func validateSpec(spec *Spec) error {
	if spec.JobType != AnalysisJobType && spec.JobType != AVSpeechJobType && spec.JobType != ChatterboxJobType && spec.JobType != LLMRelayJobType {
		return fmt.Errorf("unsupported job type %q", spec.JobType)
	}
	if spec.ExecutionTarget != TargetServer && spec.ExecutionTarget != TargetMacOS {
		return fmt.Errorf("unsupported execution target %q", spec.ExecutionTarget)
	}
	if strings.TrimSpace(spec.OwnerType) == "" || strings.TrimSpace(spec.OwnerID) == "" || strings.TrimSpace(spec.IdempotencyKey) == "" || strings.TrimSpace(spec.InputHash) == "" || strings.TrimSpace(spec.PayloadJSON) == "" {
		return errors.New("owner, idempotency key, input hash, and payload are required")
	}
	var value any
	if err := json.Unmarshal([]byte(spec.PayloadJSON), &value); err != nil {
		return fmt.Errorf("payload_json is not valid JSON: %w", err)
	}
	if spec.MaxAttempts < 0 || spec.MaxAttempts > 3 {
		return errors.New("max_attempts must be between 1 and 3")
	}
	if spec.MaxAttempts == 0 {
		spec.MaxAttempts = 3
	}
	return nil
}

func backoffForAttempt(attempt int) time.Duration {
	if attempt <= 0 {
		return retryBackoff[0]
	}
	if attempt > len(retryBackoff) {
		return retryBackoff[len(retryBackoff)-1]
	}
	return retryBackoff[attempt-1]
}

func jobLeaseLive(job *Job) bool {
	if job == nil || job.LeaseExpiresAt == "" {
		return false
	}
	expires, err := time.Parse("2006-01-02T15:04:05.000Z", job.LeaseExpiresAt)
	return err == nil && time.Now().UTC().Before(expires)
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LeaseTokenHash exposes the persisted comparison representation to the speech
// transaction boundary without exposing a worker credential in logs or JSON.
func LeaseTokenHash(token string) string { return hashToken(token) }

// LeaseTokenMatches performs the same constant-time comparison used by job
// completion and heartbeat handlers.
func LeaseTokenMatches(token, expectedHash string) bool {
	return constantTokenMatch(token, expectedHash)
}

func constantTokenMatch(token, expectedHash string) bool {
	if token == "" || expectedHash == "" {
		return false
	}
	got := hashToken(token)
	if len(got) != len(expectedHash) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expectedHash)) == 1
}

const jobSelect = `SELECT id, job_type, execution_target, owner_type, owner_id,
	idempotency_key, input_hash, payload_json, state, priority, attempt_count,
	max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at,
	progress_percent, error_code, created_at, updated_at, started_at, completed_at FROM job`

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (*Job, error) {
	var job Job
	var id string
	if err := row.Scan(&id, &job.JobType, &job.ExecutionTarget, &job.OwnerType, &job.OwnerID,
		&job.IdempotencyKey, &job.InputHash, &job.PayloadJSON, &job.State, &job.Priority,
		&job.AttemptCount, &job.MaxAttempts, &job.AvailableAt, &job.LeaseOwner, &job.leaseTokenHash,
		&job.LeaseExpiresAt, &job.ProgressPercent, &job.ErrorCode, &job.CreatedAt, &job.UpdatedAt,
		&job.StartedAt, &job.CompletedAt); err != nil {
		return nil, err
	}
	job.ID = library.ULID(id)
	return &job, nil
}

func validErrorCode(code string) bool {
	if len(code) == 0 || len(code) > 120 || !strings.HasPrefix(code, "v1.") {
		return false
	}
	for _, r := range code {
		if r != '.' && r != '_' && r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

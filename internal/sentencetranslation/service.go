// Generation service: the explicit sentence translation ensure/regenerate
// path. Reading saved data never touches a provider and never writes; only
// Ensure can create a run or enqueue a sentence job, guarded by the exact
// sentence anchor. It may depend on analysis (history), jobs, pipeline,
// library, and store; HTTP handlers (checkpoint 10) import this package.
package sentencetranslation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/store"
)

// Operation identity for the sentence job, its run history, and its owner
// pointers. The transport stage stays translation; the sentence operation
// identity lives in the job payload and the run row, never in a new
// analysis stage.
const (
	JobType       = "reader.sentence_translation.v1"
	OwnerType     = "sentence_translation"
	OperationType = "sentence_translation"
)

// Reader-visible translation statuses.
const (
	StatusMissing = "missing"
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// Generation phases for in-flight or failed work behind the envelope.
const (
	GenerationIdle    = "idle"
	GenerationQueued  = "queued"
	GenerationRunning = "running"
	GenerationFailed  = "failed"
)

// Stable sentence job error codes. Validation corrections happen inside the
// single attempt; MaxAttempts is 1, so every failure is terminal and needs
// an explicit regenerate.
const (
	CodeProviderUnavailable = "v1.sentence_provider_unavailable"
	CodeProviderChanged     = "v1.sentence_provider_changed"
	CodeInvalidOutput       = "v1.sentence_invalid_output"
	CodeContractChanged     = "v1.sentence_contract_changed"
	CodeGenerationFailed    = "v1.sentence_generation_failed"
	CodeStorageFailed       = "v1.sentence_storage_failed"
)

// Typed service failures the HTTP layer maps to stable sentence codes.
//
// ErrNotFound (owned by the store file) also marks an article/sentence
// membership mismatch: it is returned before any run, provider, or queue
// work.
var (
	// ErrProviderUnavailable marks a failed Translation binding resolution.
	// The failed run is retained and last_run_id keeps pointing at it, so
	// repeated hovers converge on the same failure instead of retrying.
	ErrProviderUnavailable = errors.New("sentencetranslation: translation provider is unavailable")
)

// ResolvedSentenceGeneration is the exact generation configuration for one
// sentence operation: the active profile's Translation binding plus that
// profile's pinned sentence_translation generation and correction prompt
// snapshots. Only the Translation binding is ever resolved here; an
// unavailable unrelated linguistic provider must not block sentence work.
type ResolvedSentenceGeneration struct {
	Binding         pipeline.BindingSnapshot
	PromptSnapshots []pipeline.PromptSnapshot
	ProfileID       string
	ProfileName     string
}

// BindingResolver resolves the active profile's Translation generation
// through the shared usability checks. It runs outside any write
// transaction. The concrete resolver lives with the HTTP wiring
// (checkpoint 10); the service only pins whatever it returns into the
// immutable payload after validating its Translation-only identity.
type BindingResolver func(ctx context.Context) (ResolvedSentenceGeneration, error)

// JobPayload is the immutable sentence job snapshot: the exact sentence
// anchor, the article-owned target language, the block source text as
// context, the sentence operation contract, the resolved Translation
// binding, the originating article and claiming run for history, and the
// profile's pinned sentence_translation/correction prompt snapshots. The
// exact context is part of the input hash. It contains no secret and no
// endpoint.
type JobPayload struct {
	ContractVersion string                             `json:"contract_version"`
	PromptVersion   string                             `json:"prompt_version"`
	ArticleID       string                             `json:"article_id"`
	SentenceID      string                             `json:"sentence_id"`
	Input           annotator.SentenceTranslationInput `json:"input"`
	InputHash       string                             `json:"input_hash"`
	Binding         pipeline.BindingSnapshot           `json:"binding"`
	// PromptSnapshots are the pinned sentence_translation generation and
	// correction versions captured before enqueue.
	PromptSnapshots []pipeline.PromptSnapshot `json:"prompt_snapshots"`
	ProfileID       string                    `json:"profile_id"`
	ProfileName     string                    `json:"profile_name"`
	// RunID is the service-claimed run created before provider preflight.
	RunID string `json:"run_id"`

	// validatedJSON holds the canonical encoded form for enqueueing; it is
	// never decoded from provider or browser input.
	validatedJSON string `json:"-"`
}

// DecodeJobPayload strictly decodes the sentence job payload.
func DecodeJobPayload(data []byte) (JobPayload, error) {
	var payload JobPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return JobPayload{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return JobPayload{}, errors.New("sentence job payload contains trailing JSON")
		}
		return JobPayload{}, fmt.Errorf("sentence job payload trailing JSON: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return JobPayload{}, err
	}
	return payload, nil
}

// Validate checks the payload identity before any provider call.
func (p JobPayload) Validate() error {
	if p.ContractVersion != annotator.SentenceTranslationContractVersion || p.PromptVersion != annotator.SentenceTranslationContractVersion {
		return errors.New("sentence job payload versions do not match this binary")
	}
	if _, err := library.ParseULID(p.ArticleID); err != nil {
		return errors.New("sentence job payload article id is invalid")
	}
	if _, err := library.ParseULID(p.SentenceID); err != nil {
		return errors.New("sentence job payload sentence id is invalid")
	}
	if _, err := library.ParseULID(p.RunID); err != nil {
		return errors.New("sentence job payload run id is invalid")
	}
	if err := p.Input.Validate(); err != nil {
		return fmt.Errorf("sentence job payload input: %w", err)
	}
	if p.Input.SentenceID != p.SentenceID {
		return errors.New("sentence job payload input does not match its sentence")
	}
	if p.InputHash != InputHash(p.Input) {
		return errors.New("sentence job payload input hash does not match its input")
	}
	if err := p.Binding.Validate(); err != nil {
		return fmt.Errorf("sentence job payload binding: %w", err)
	}
	// Translation-only resolution: the sentence operation rides the
	// Translation transport identity and nothing else.
	if p.Binding.StageID != pipeline.StageTranslation {
		return fmt.Errorf("sentence job payload binding must use the translation transport stage, got %q", p.Binding.StageID)
	}
	if len(p.PromptSnapshots) != 2 {
		return errors.New("sentence job payload must capture its sentence_translation and correction prompt snapshots")
	}
	seen := make(map[string]bool, 2)
	for index, snapshot := range p.PromptSnapshots {
		if err := snapshot.Validate(); err != nil {
			return fmt.Errorf("sentence job payload prompt_snapshots[%d]: %w", index, err)
		}
		if snapshot.Type != "sentence_translation" && snapshot.Type != "correction" {
			return fmt.Errorf("sentence job payload prompt_snapshots[%d] carries unsupported type %q", index, snapshot.Type)
		}
		if seen[snapshot.Type] {
			return fmt.Errorf("sentence job payload carries %q prompt snapshot twice", snapshot.Type)
		}
		seen[snapshot.Type] = true
		if snapshot.EnvelopeVersion != pipeline.PromptCapturedEnvelopeVersion {
			return fmt.Errorf("sentence job payload prompt_snapshots[%d] has unsupported envelope version %q", index, snapshot.EnvelopeVersion)
		}
	}
	if !seen["sentence_translation"] || !seen["correction"] {
		return errors.New("sentence job payload is missing its sentence_translation or correction prompt snapshot")
	}
	if strings.TrimSpace(p.ProfileID) == "" {
		return errors.New("sentence job payload profile id is required")
	}
	return nil
}

// InputHash digests the canonical prompt input, including the exact block
// context.
func InputHash(input annotator.SentenceTranslationInput) string {
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func jobPayload(articleID, sentenceID string, input annotator.SentenceTranslationInput, generation ResolvedSentenceGeneration, runID string) (JobPayload, error) {
	payload := JobPayload{
		ContractVersion: annotator.SentenceTranslationContractVersion,
		PromptVersion:   annotator.SentenceTranslationContractVersion,
		ArticleID:       articleID,
		SentenceID:      sentenceID,
		Input:           input,
		InputHash:       InputHash(input),
		Binding:         generation.Binding,
		PromptSnapshots: generation.PromptSnapshots,
		ProfileID:       generation.ProfileID,
		ProfileName:     generation.ProfileName,
		RunID:           runID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return payload, err
	}
	payload.validatedJSON = string(encoded)
	return payload, payload.Validate()
}

// Envelope is the shared read/poll response body.
type Envelope struct {
	Status           string  `json:"status"`
	SentenceID       string  `json:"sentence_id,omitempty"`
	Translation      *string `json:"translation,omitempty"`
	JobID            string  `json:"job_id,omitempty"`
	RunID            string  `json:"run_id,omitempty"`
	GenerationStatus string  `json:"generation_status,omitempty"`
	ErrorCode        string  `json:"error_code,omitempty"`
	ErrorSummary     string  `json:"error_summary,omitempty"`
}

// anchor is the server-resolved sentence subject: the exact source anchor
// plus the article-owned languages and the block text used as context.
type anchor struct {
	ArticleID      library.ULID
	ArticleTitle   string
	SentenceID     library.ULID
	SourceHash     string
	SourceText     string
	ParagraphText  string
	SourceLanguage string
	TargetLanguage string
}

func (a anchor) input() annotator.SentenceTranslationInput {
	return annotator.SentenceTranslationInput{
		Version:        annotator.SentenceTranslationContractVersion,
		SentenceID:     a.SentenceID.String(),
		SourceHash:     a.SourceHash,
		SourceLanguage: a.SourceLanguage,
		TargetLanguage: a.TargetLanguage,
		SourceText:     a.SourceText,
		ParagraphText:  a.ParagraphText,
	}
}

// Service performs read and explicit-start flows for sentence translations.
type Service struct {
	db       *store.DB
	store    *Store
	jobs     *jobs.Store
	history  *analysis.HistoryStore
	resolver BindingResolver
}

func NewService(db *store.DB, resolver BindingResolver) *Service {
	return &Service{db: db, store: NewStore(db), jobs: jobs.NewStore(db), history: analysis.NewHistoryStore(db), resolver: resolver}
}

// Lookup resolves the anchor and returns the saved translation state. It
// performs no writes, creates no run, and never touches a provider.
func (s *Service) Lookup(ctx context.Context, articleID, sentenceID library.ULID) (Envelope, error) {
	resolved, err := s.resolveAnchor(ctx, articleID, sentenceID)
	if err != nil {
		return Envelope{}, err
	}
	entry, err := s.store.Get(ctx, sentenceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Envelope{}, err
	}
	return s.envelope(ctx, resolved, entry), nil
}

// Ensure implements the explicit generation flow: it returns the saved
// translation without provider resolution, joins an active generation, or
// starts one fresh job under the current Translation binding. A failed
// initial generation is returned as-is; only regenerate=true starts
// replacement work, preserving the old text until a validated replacement
// publishes. Concurrent callers converge on one active job through the
// last_run_id claim and the last_job_id compare-and-set.
func (s *Service) Ensure(ctx context.Context, articleID, sentenceID library.ULID, regenerate bool) (Envelope, bool, error) {
	resolved, err := s.resolveAnchor(ctx, articleID, sentenceID)
	if err != nil {
		return Envelope{}, false, err
	}
	input := resolved.input()
	if err := input.Validate(); err != nil {
		return Envelope{}, false, fmt.Errorf("sentencetranslation: request input: %w", err)
	}

	// Phase 1: ensure the subject row (clearing translations bound to a
	// changed anchor) and observe the current pointers inside one
	// transaction. The database row is the authority for concurrent hovers.
	var first claim
	if err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := ensureSubjectTx(ctx, tx, resolved); err != nil {
			return err
		}
		var err error
		first, err = inspectTx(ctx, tx, sentenceID)
		return err
	}); err != nil {
		return Envelope{}, false, err
	}
	s.refreshClaim(ctx, &first)
	if envelope, done := settledState(resolved, &first, regenerate); done {
		return envelope, false, nil
	}

	// The run is claimed before provider preflight so disabled or missing
	// providers and other preflight failures stay visible in history, and a
	// nonterminal last_run_id converges concurrent hovers even before any
	// job exists.
	run, err := s.history.StartRun(ctx, analysis.RunStart{
		ArticleID: resolved.ArticleID, ArticleTitle: resolved.ArticleTitle,
		AttemptCount: 1, ContentHash: InputHash(input),
		ContractVersion: annotator.SentenceTranslationContractVersion,
		OperationType:   OperationType, SubjectID: sentenceID.String(), SubjectLabel: resolved.SourceText,
	})
	if err != nil {
		return Envelope{}, false, fmt.Errorf("sentencetranslation: claim run: %w", err)
	}

	// Phase 2: point last_run_id at the fresh run with a compare-and-set on
	// the previously observed pointer. A lost race rolls the claim back by
	// finishing the orphan run and converging on the winner.
	conflict, err := s.claimRun(ctx, resolved, sentenceID, first.runID, run.ID.String(), regenerate)
	if err != nil {
		_ = s.history.FinishRun(ctx, run.ID, analysis.RunFinish{
			Status: "failed", ErrorCode: CodeStorageFailed, ErrorDetail: "sentence translation claim could not be recorded",
		})
		return Envelope{}, false, err
	}
	if conflict != nil {
		_ = s.history.FinishRun(ctx, run.ID, analysis.RunFinish{
			Status: "failed", ErrorCode: CodeGenerationFailed, ErrorDetail: "sentence translation claim superseded by a newer request",
		})
		return *conflict, false, nil
	}

	// Resolve the usable Translation binding and its pinned prompt
	// snapshots outside any write transaction.
	if s.resolver == nil {
		s.failClaim(ctx, sentenceID, run.ID, CodeProviderUnavailable, "no translation generation resolver is configured")
		return Envelope{}, false, ErrProviderUnavailable
	}
	generation, err := s.resolver(ctx)
	if err != nil {
		s.failClaim(ctx, sentenceID, run.ID, CodeProviderUnavailable, "translation provider is not usable")
		return Envelope{}, false, ErrProviderUnavailable
	}

	payload, err := jobPayload(articleID.String(), sentenceID.String(), input, generation, run.ID.String())
	if err != nil {
		s.failClaim(ctx, sentenceID, run.ID, CodeContractChanged, "sentence translation request could not be snapshotted")
		return Envelope{}, false, fmt.Errorf("sentencetranslation: request snapshot: %w", err)
	}

	// Phase 3: recheck the claim, enqueue the frozen payload, and move the
	// job pointer only while the claim still holds. A newer explicit
	// regenerate replaces last_run_id; it never mutates this earlier run
	// beyond marking it superseded.
	var envelope Envelope
	started := false
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		current, err := inspectTx(ctx, tx, sentenceID)
		if err != nil {
			return err
		}
		if current.runID != run.ID.String() {
			settled, done := settledState(resolved, &current, regenerate)
			return s.finishSupersededTx(ctx, tx, run.ID, &envelope, settled, done)
		}
		if current.runStatus != "running" {
			return errors.New("sentencetranslation: claim lost before enqueue")
		}
		// The claim still holds: our own nonterminal run must not read
		// as someone else's active generation here. Converge only on
		// states another writer could have settled meanwhile.
		if current.readyIn(resolved) && !regenerate {
			finish := analysis.RunFinish{Status: "failed", ErrorCode: CodeGenerationFailed, ErrorDetail: "sentence translation claim superseded by settled state"}
			if err := s.history.FinishRunTx(ctx, tx, run.ID, finish); err != nil {
				return err
			}
			envelope = renderEnvelope(resolved, &current)
			return nil
		}
		if jobActive(current.jobState) {
			finish := analysis.RunFinish{Status: "failed", ErrorCode: CodeGenerationFailed, ErrorDetail: "sentence translation claim superseded by concurrent enqueue"}
			if err := s.history.FinishRunTx(ctx, tx, run.ID, finish); err != nil {
				return err
			}
			envelope = renderEnvelope(resolved, &current)
			return nil
		}
		jobID := library.NewULID()
		spec := jobs.Spec{
			JobType: JobType, ExecutionTarget: jobs.TargetServer,
			OwnerType: OwnerType, OwnerID: sentenceID.String(),
			IdempotencyKey: fmt.Sprintf("%s:%s:%s", JobType, sentenceID.String(), jobID.String()),
			InputHash:      payload.InputHash, PayloadJSON: payload.validatedJSON,
			MaxAttempts: 1,
		}
		job, err := jobs.EnqueueTx(ctx, tx, spec)
		if err != nil {
			return err
		}
		if err := setLastJobTx(ctx, tx, sentenceID, current.jobID, job.ID.String()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE analysis_run SET job_id = ? WHERE id = ?`, job.ID.String(), run.ID.String()); err != nil {
			return fmt.Errorf("sentencetranslation: attach job to run: %w", err)
		}
		current.jobID = job.ID.String()
		current.jobState = job.State
		current.runID = run.ID.String()
		current.runStatus = "running"
		envelope = renderEnvelope(resolved, &current)
		started = true
		return nil
	})
	if err != nil {
		return Envelope{}, false, err
	}
	return envelope, started, nil
}

// failClaim marks the claimed run failed while keeping the last_run_id
// pointer, so repeated hovers observe the retained failure instead of
// retrying preflight work after reload.
func (s *Service) failClaim(ctx context.Context, sentenceID, runID library.ULID, code, detail string) {
	_ = s.history.FinishRun(ctx, runID, analysis.RunFinish{Status: "failed", ErrorCode: code, ErrorDetail: detail})
	_ = s.history.SetRunPipelineFailure(ctx, runID.String(), string(pipeline.StageTranslation), "")
}

// claim is the observed subject state: the entry row plus the joined last
// job and last run pointers. Empty job/run fields mean no pointer.
type claim struct {
	entry     *Entry
	jobID     string
	jobState  string
	jobCode   string
	runID     string
	runStatus string
	runCode   string
	runDetail string
}

// inspectTx loads the subject state inside the caller's transaction. A
// dangling job or run pointer (its row is gone) reads as no pointer.
func inspectTx(ctx context.Context, tx *sql.Tx, sentenceID library.ULID) (claim, error) {
	var c claim
	var translation sql.NullString
	var lastJobID, lastRunID sql.NullString
	entry := &Entry{SentenceID: sentenceID}
	err := tx.QueryRowContext(ctx, `
		SELECT source_hash, target_language, translation_text, result_hash,
			provenance_json, last_job_id, last_run_id, updated_at
		FROM sentence_translation WHERE sentence_id = ?
	`, sentenceID.String()).Scan(
		&entry.SourceHash, &entry.TargetLanguage, &translation,
		&entry.ResultHash, &entry.ProvenanceJSON, &lastJobID, &lastRunID,
		&entry.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return claim{}, nil
	}
	if err != nil {
		return claim{}, fmt.Errorf("sentencetranslation: inspect: %w", err)
	}
	if translation.Valid {
		value := translation.String
		entry.TranslationText = &value
	}
	c.entry = entry
	if lastJobID.Valid && lastJobID.String != "" {
		c.jobID = lastJobID.String
		if err := tx.QueryRowContext(ctx, `SELECT state, error_code FROM job WHERE id = ?`, c.jobID).Scan(&c.jobState, &c.jobCode); err != nil {
			c.jobID, c.jobState, c.jobCode = "", "", ""
		}
	}
	if lastRunID.Valid && lastRunID.String != "" {
		c.runID = lastRunID.String
		if err := tx.QueryRowContext(ctx, `SELECT status, error_code, error_detail FROM analysis_run WHERE id = ?`, c.runID).Scan(&c.runStatus, &c.runCode, &c.runDetail); err != nil {
			c.runID, c.runStatus, c.runCode, c.runDetail = "", "", "", ""
		}
	}
	return c, nil
}

func jobActive(state string) bool {
	return state == jobs.StateQueued || state == jobs.StateLeased || state == jobs.StateRunning
}

func jobTerminallyFailed(state string) bool {
	return state == jobs.StateFailed || state == jobs.StateCanceled
}

// readyIn reports whether a validated translation is saved for the anchor.
func (c *claim) readyIn(resolved anchor) bool {
	return c.entry != nil && c.entry.TranslationText != nil &&
		c.entry.SourceHash == resolved.SourceHash && c.entry.TargetLanguage == resolved.TargetLanguage
}

// settledState renders the converged envelope when no new work is needed.
// done=false means the caller must create fresh work.
func settledState(resolved anchor, c *claim, regenerate bool) (Envelope, bool) {
	if c.readyIn(resolved) && !regenerate {
		return renderEnvelope(resolved, c), true
	}
	if jobActive(c.jobState) || c.runStatus == "running" {
		return renderEnvelope(resolved, c), true
	}
	if (jobTerminallyFailed(c.jobState) || c.runStatus == "failed") && !regenerate {
		return renderEnvelope(resolved, c), true
	}
	return Envelope{}, false
}

// refreshClaim re-reads job/run pointers observed outside a transaction so a
// deleted row reads as no pointer, matching inspectTx semantics.
func (s *Service) refreshClaim(ctx context.Context, c *claim) {
	if c.jobID != "" {
		job, err := s.jobs.Get(ctx, library.ULID(c.jobID))
		if err != nil {
			c.jobID, c.jobState, c.jobCode = "", "", ""
		} else {
			c.jobState, c.jobCode = job.State, job.ErrorCode
		}
	}
	if c.runID != "" {
		run, err := s.history.GetRun(ctx, library.ULID(c.runID))
		if err != nil {
			c.runID, c.runStatus, c.runCode, c.runDetail = "", "", "", ""
		} else {
			c.runStatus, c.runCode, c.runDetail = run.Status, run.ErrorCode, run.ErrorDetail
		}
	}
}

// claimRun points last_run_id at a fresh run with a compare-and-set on the
// previously observed pointer. On a lost race it returns the winner's
// converged envelope for the caller to return.
func (s *Service) claimRun(ctx context.Context, resolved anchor, sentenceID library.ULID, expectedRunID, runID string, regenerate bool) (*Envelope, error) {
	converge := func(current *claim) *Envelope {
		envelope, done := settledState(resolved, current, regenerate)
		if !done {
			envelope = renderEnvelope(resolved, current)
		}
		return &envelope
	}
	var winner *Envelope
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		current, err := inspectTx(ctx, tx, sentenceID)
		if err != nil {
			return err
		}
		if current.runID != expectedRunID {
			winner = converge(&current)
			return errClaimConflict
		}
		query := `UPDATE sentence_translation SET last_run_id = ?, updated_at = ? WHERE sentence_id = ? AND last_run_id IS NULL`
		args := []any{runID, store.NowUTC(), sentenceID.String()}
		if expectedRunID != "" {
			query = `UPDATE sentence_translation SET last_run_id = ?, updated_at = ? WHERE sentence_id = ? AND last_run_id = ?`
			args = append(args, expectedRunID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("sentencetranslation: claim run: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			winner = converge(&current)
			return errClaimConflict
		}
		return nil
	})
	if errors.Is(err, errClaimConflict) {
		return winner, nil
	}
	return nil, err
}

var errClaimConflict = errors.New("sentencetranslation: subject claimed concurrently")

func (s *Service) finishSupersededTx(ctx context.Context, tx *sql.Tx, runID library.ULID, out *Envelope, envelope Envelope, done bool) error {
	if !done {
		envelope = Envelope{Status: StatusQueued, RunID: runID.String(), GenerationStatus: GenerationQueued}
	}
	if err := s.history.FinishRunTx(ctx, tx, runID, analysis.RunFinish{
		Status: "failed", ErrorCode: CodeGenerationFailed, ErrorDetail: "sentence translation claim superseded by a newer request",
	}); err != nil {
		return err
	}
	*out = envelope
	return nil
}

// envelope renders the read state for Lookup: saved, active, failed, or
// missing, without starting anything.
func (s *Service) envelope(ctx context.Context, resolved anchor, entry *Entry) Envelope {
	c := claim{entry: entry}
	if entry != nil {
		c.jobID = deref(entry.LastJobID)
		c.runID = deref(entry.LastRunID)
		s.refreshClaim(ctx, &c)
	}
	return renderEnvelope(resolved, &c)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// renderEnvelope derives the visible status. A saved translation stays
// readable (status ready) while a regeneration runs or after one fails; an
// old job pointer never overrides a newer run's status.
func renderEnvelope(resolved anchor, c *claim) Envelope {
	envelope := Envelope{SentenceID: resolved.SentenceID.String(), GenerationStatus: GenerationIdle}
	if c.readyIn(resolved) {
		envelope.Status = StatusReady
		envelope.Translation = c.entry.TranslationText
	} else if jobActive(c.jobState) {
		envelope.Status = StatusQueued
		if c.jobState == jobs.StateLeased || c.jobState == jobs.StateRunning {
			envelope.Status = StatusRunning
		}
	} else if c.runStatus == "running" {
		// Claimed before any job exists (preflight window): queued.
		envelope.Status = StatusQueued
	} else if c.runStatus == "failed" || jobTerminallyFailed(c.jobState) {
		envelope.Status = StatusFailed
	} else {
		envelope.Status = StatusMissing
	}
	if c.jobID != "" {
		envelope.JobID = c.jobID
	}
	if c.runID != "" {
		envelope.RunID = c.runID
	}
	switch envelope.Status {
	case StatusQueued:
		envelope.GenerationStatus = GenerationQueued
	case StatusRunning:
		envelope.GenerationStatus = GenerationRunning
	case StatusFailed:
		envelope.GenerationStatus = GenerationFailed
		if c.runStatus == "failed" {
			envelope.ErrorCode = sentenceErrorCode(c.runCode)
			envelope.ErrorSummary = boundedSummary(c.runDetail, c.runCode)
		} else {
			envelope.ErrorCode = sentenceErrorCode(c.jobCode)
			envelope.ErrorSummary = boundedSummary("", c.jobCode)
		}
	case StatusReady:
		if jobActive(c.jobState) || c.runStatus == "running" {
			envelope.GenerationStatus = GenerationRunning
			if c.jobState == jobs.StateQueued || (c.jobState == "" && c.runStatus == "running") {
				envelope.GenerationStatus = GenerationQueued
			}
		} else if c.runStatus == "failed" || jobTerminallyFailed(c.jobState) {
			envelope.GenerationStatus = GenerationFailed
			if c.runStatus == "failed" {
				envelope.ErrorCode = sentenceErrorCode(c.runCode)
				envelope.ErrorSummary = boundedSummary(c.runDetail, c.runCode)
			} else {
				envelope.ErrorCode = sentenceErrorCode(c.jobCode)
				envelope.ErrorSummary = boundedSummary("", c.jobCode)
			}
		}
	}
	return envelope
}

// sentenceErrorCode translates a stored job or run error code into the
// stable sentence codes; unknown or empty codes map to generation_failed.
func sentenceErrorCode(code string) string {
	switch code {
	case CodeProviderUnavailable, CodeProviderChanged, CodeInvalidOutput,
		CodeContractChanged, CodeGenerationFailed, CodeStorageFailed,
		jobs.LeaseExpiredErrorCode, "v1.analysis_interrupted":
		if code == jobs.LeaseExpiredErrorCode {
			return CodeGenerationFailed
		}
		return code
	case "":
		return CodeGenerationFailed
	default:
		return CodeGenerationFailed
	}
}

func boundedSummary(detail, code string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return code
	}
	const limit = 280
	if len(detail) > limit {
		return detail[:limit] + "…"
	}
	return detail
}

func (s *Service) resolveAnchor(ctx context.Context, articleID, sentenceID library.ULID) (anchor, error) {
	var resolved anchor
	resolved.ArticleID = articleID
	resolved.SentenceID = sentenceID
	err := s.db.QueryRow(ctx, `
		SELECT s.source_hash, s.source_text, b.source_text,
			a.source_language, a.target_language, a.title
		FROM article_sentence s
		JOIN article_block b ON b.id = s.article_block_id
		JOIN article a ON a.id = b.article_id
		WHERE s.id = ? AND a.id = ?
	`, sentenceID.String(), articleID.String()).Scan(
		&resolved.SourceHash, &resolved.SourceText, &resolved.ParagraphText,
		&resolved.SourceLanguage, &resolved.TargetLanguage, &resolved.ArticleTitle,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return anchor{}, ErrNotFound
	}
	if err != nil {
		return anchor{}, fmt.Errorf("sentencetranslation: resolve sentence: %w", err)
	}
	return resolved, nil
}

// ensureSubjectTx inserts the subject row for a first generation, or
// re-points an existing row at the live anchor. A translation bound to a
// changed anchor or a foreign target language is cleared: results are never
// served or published under recreated anchors. Saved text for the live
// anchor is preserved, and job/run pointers are never touched here.
func ensureSubjectTx(ctx context.Context, tx *sql.Tx, resolved anchor) error {
	var sourceHash, targetLanguage string
	var translation sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT source_hash, target_language, translation_text FROM sentence_translation WHERE sentence_id = ?`,
		resolved.SentenceID.String()).Scan(&sourceHash, &targetLanguage, &translation)
	if errors.Is(err, sql.ErrNoRows) {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sentence_translation
				(sentence_id, source_hash, target_language, translation_text,
					result_hash, provenance_json, last_job_id, last_run_id, updated_at)
			VALUES (?, ?, ?, NULL, '', '', NULL, NULL, ?)
		`, resolved.SentenceID.String(), resolved.SourceHash, resolved.TargetLanguage, store.NowUTC())
		return err
	}
	if err != nil {
		return fmt.Errorf("sentencetranslation: ensure subject: %w", err)
	}
	if sourceHash == resolved.SourceHash && targetLanguage == resolved.TargetLanguage {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE sentence_translation
		SET source_hash = ?, target_language = ?, translation_text = NULL,
			result_hash = '', provenance_json = '', updated_at = ?
		WHERE sentence_id = ?
	`, resolved.SourceHash, resolved.TargetLanguage, store.NowUTC(), resolved.SentenceID.String())
	return err
}

// orphanRunMaxAge bounds the service claim window: a nonterminal run with
// no enqueued job older than this was abandoned between claiming and
// enqueueing (for example by a process exit) and is finalized as
// interrupted. Younger runs may still belong to a live claim.
const orphanRunMaxAge = 5 * time.Minute

// ReconcileOrphanRuns finalizes nonterminal sentence runs that never
// received an enqueued job, reusing the existing run recovery vocabulary
// instead of another state table. Runs already attached to a job are owned
// by the generic terminal-job reconciliation. Ready saved output is
// unaffected; only the abandoned claim is closed.
func ReconcileOrphanRuns(ctx context.Context, db *store.DB) (int64, error) {
	cutoff := time.Now().UTC().Add(-orphanRunMaxAge).Format("2006-01-02T15:04:05.000Z")
	now := store.NowUTC()
	result, err := db.Exec(ctx, `
		UPDATE analysis_run SET
			status = 'failed',
			error_code = 'v1.analysis_interrupted',
			error_detail = 'sentence translation run abandoned before its job was enqueued',
			phase = 'finished',
			completed_at = ?
		WHERE operation_type = 'sentence_translation'
		  AND status = 'running'
		  AND job_id = ?
		  AND started_at < ?
	`, now, library.ULID("").String(), cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// setLastJobTx points the subject at a new generation job inside the
// caller's transaction. It fails when another writer moved the subject
// meanwhile, so concurrent regenerations converge instead of overwriting.
func setLastJobTx(ctx context.Context, tx *sql.Tx, sentenceID library.ULID, previousJobID, jobID string) error {
	query := `UPDATE sentence_translation SET last_job_id = ?, updated_at = ?
		WHERE sentence_id = ? AND last_job_id IS NULL`
	args := []any{jobID, store.NowUTC(), sentenceID.String()}
	if previousJobID != "" {
		query = `UPDATE sentence_translation SET last_job_id = ?, updated_at = ?
			WHERE sentence_id = ? AND last_job_id = ?`
		args = append(args, previousJobID)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("sentencetranslation: set last job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("sentencetranslation: subject moved concurrently")
	}
	return nil
}

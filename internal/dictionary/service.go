// Generation service: the explicit Explore get-or-create path. Reading saved
// data never touches a provider; only this service's Start call can enqueue a
// dictionary job, inside one transaction guarded by the dictionary key.
package dictionary

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

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// Operation identity for the dictionary job and document provenance.
const (
	JobType   = "reader.dictionary.v1"
	OwnerType = "dictionary_entry"
)

// Typed service failures the HTTP layer maps to stable dictionary codes.
var (
	ErrProviderUnavailable = errors.New("dictionary: explore provider is unavailable")
	ErrNonLexical          = ErrNotFound
	// ErrAmbiguousRequest rejects retry=true combined with regenerate=true:
	// retry replays a failed first generation while regenerate always starts
	// a fresh one, and asking for both is meaningless.
	ErrAmbiguousRequest = errors.New("dictionary: retry and regenerate are mutually exclusive")
)

// ResolvedExploreGeneration is the exact generation configuration for one
// explore operation: the active profile's independent Explore binding plus
// that profile's pinned explore generation and correction prompt snapshots.
type ResolvedExploreGeneration struct {
	Binding         pipeline.BindingSnapshot
	PromptSnapshots []pipeline.PromptSnapshot
	ProfileID       string
	ProfileName     string
}

// BindingResolver resolves the active profile's Explore generation through
// the shared usability checks. It runs outside any write transaction.
type BindingResolver func(ctx context.Context) (ResolvedExploreGeneration, error)

// JobPayload is the immutable dictionary job snapshot: derived subject,
// dictionary contract/prompt versions, exact prompt input, input hash, the
// resolved Explore binding, the originating article for run history, and the
// profile's pinned explore/correction prompt snapshots. It contains no
// article prose, no secret, and no endpoint.
type JobPayload struct {
	ContractVersion string                    `json:"contract_version"`
	PromptVersion   string                    `json:"prompt_version"`
	EntryID         string                    `json:"entry_id"`
	ArticleID       string                    `json:"article_id"`
	Input           semantics.DictionaryInput `json:"input"`
	InputHash       string                    `json:"input_hash"`
	Binding         pipeline.BindingSnapshot  `json:"binding"`
	// PromptSnapshots are the pinned explore generation and correction
	// versions captured before enqueue.
	PromptSnapshots []pipeline.PromptSnapshot `json:"prompt_snapshots"`
	Regenerate      bool                      `json:"regenerate,omitempty"`
	ProfileID       string                    `json:"profile_id"`
	ProfileName     string                    `json:"profile_name"`

	// validatedJSON holds the canonical encoded form for enqueueing; it is
	// never decoded from provider or browser input.
	validatedJSON string `json:"-"`
}

// DecodeJobPayload strictly decodes the dictionary job payload.
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
			return JobPayload{}, errors.New("dictionary job payload contains trailing JSON")
		}
		return JobPayload{}, fmt.Errorf("dictionary job payload trailing JSON: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return JobPayload{}, err
	}
	return payload, nil
}

// Validate checks the payload identity before any provider call.
func (p JobPayload) Validate() error {
	if p.ContractVersion != semantics.DictionaryContractVersion || p.PromptVersion != semantics.DictionaryPromptVersion {
		return errors.New("dictionary job payload versions do not match this binary")
	}
	if _, err := library.ParseULID(p.EntryID); err != nil {
		return errors.New("dictionary job payload entry id is invalid")
	}
	if strings.TrimSpace(p.ArticleID) == "" {
		return errors.New("dictionary job payload article id is required")
	}
	if err := p.Input.Validate(); err != nil {
		return fmt.Errorf("dictionary job payload input: %w", err)
	}
	if p.InputHash != InputHash(p.Input) {
		return errors.New("dictionary job payload input hash does not match its input")
	}
	if err := p.Binding.Validate(); err != nil {
		return fmt.Errorf("dictionary job payload binding: %w", err)
	}
	if len(p.PromptSnapshots) != 2 {
		return errors.New("dictionary job payload must capture its explore and correction prompt snapshots")
	}
	seen := make(map[string]bool, 2)
	for index, snapshot := range p.PromptSnapshots {
		if err := snapshot.Validate(); err != nil {
			return fmt.Errorf("dictionary job payload prompt_snapshots[%d]: %w", index, err)
		}
		if snapshot.Type != "explore" && snapshot.Type != "correction" {
			return fmt.Errorf("dictionary job payload prompt_snapshots[%d] carries unsupported type %q", index, snapshot.Type)
		}
		if seen[snapshot.Type] {
			return fmt.Errorf("dictionary job payload carries %q prompt snapshot twice", snapshot.Type)
		}
		seen[snapshot.Type] = true
		if snapshot.EnvelopeVersion != pipeline.PromptCapturedEnvelopeVersion {
			return fmt.Errorf("dictionary job payload prompt_snapshots[%d] has unsupported envelope version %q", index, snapshot.EnvelopeVersion)
		}
	}
	if !seen["explore"] || !seen["correction"] {
		return errors.New("dictionary job payload is missing its explore or correction prompt snapshot")
	}
	return nil
}

// InputHash digests the canonical prompt input.
func InputHash(input semantics.DictionaryInput) string {
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func jobPayload(entryID, articleID string, input semantics.DictionaryInput, generation ResolvedExploreGeneration, regenerate bool) (JobPayload, error) {
	payload := JobPayload{
		ContractVersion: semantics.DictionaryContractVersion,
		PromptVersion:   semantics.DictionaryPromptVersion,
		EntryID:         entryID,
		ArticleID:       articleID,
		Input:           input,
		InputHash:       InputHash(input),
		Binding:         generation.Binding,
		PromptSnapshots: generation.PromptSnapshots,
		Regenerate:      regenerate,
		ProfileID:       generation.ProfileID,
		ProfileName:     generation.ProfileName,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return payload, err
	}
	payload.validatedJSON = string(encoded)
	return payload, payload.Validate()
}

// Subject is re-declared in service context for the envelope.
// StatusEnvelope is the shared read/poll response body. GenerationStatus
// mirrors the sentence-translation operation values (idle/queued/running/
// failed) so readers can render progress beside a retained document;
// GenerationErrorCode retains the last generation failure even when an older
// document stays readable under status ready.
type StatusEnvelope struct {
	Status              string                        `json:"status"`
	EntryID             string                        `json:"entry_id,omitempty"`
	JobID               string                        `json:"job_id,omitempty"`
	RunID               string                        `json:"run_id,omitempty"`
	GenerationStatus    string                        `json:"generation_status,omitempty"`
	GenerationErrorCode string                        `json:"generation_error_code,omitempty"`
	Subject             *SubjectView                  `json:"subject,omitempty"`
	Document            *semantics.DictionaryDocument `json:"document,omitempty"`
	ErrorCode           string                        `json:"error_code,omitempty"`
}

// Generation phases for in-flight or failed work behind the envelope,
// aligned with the sentence-translation operation values.
const (
	GenerationIdle    = "idle"
	GenerationQueued  = "queued"
	GenerationRunning = "running"
	GenerationFailed  = "failed"
)

// SubjectView is the server-resolved subject rendered to the reader.
type SubjectView struct {
	LookupForm     string `json:"lookup_form"`
	LookupKind     string `json:"lookup_kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
}

func subjectView(subject *Subject) *SubjectView {
	if subject == nil {
		return nil
	}
	return &SubjectView{
		LookupForm: subject.LookupForm, LookupKind: subject.LookupKind,
		SourceLanguage: subject.SourceLanguage, TargetLanguage: subject.TargetLanguage,
	}
}

// envelopeFor renders the persisted entry status. A saved document stays
// readable (status ready) while a regeneration runs or after one fails; the
// generation fields expose the latest attempt beside it.
func envelopeFor(entry *Entry, subject *Subject) StatusEnvelope {
	envelope := StatusEnvelope{
		Subject:          subjectView(subject),
		GenerationStatus: GenerationIdle,
	}
	if entry == nil {
		envelope.Status = StatusMissing
		return envelope
	}
	envelope.EntryID = entry.ID.String()
	envelope.Status = entry.Status()
	if entry.LastJobID != nil {
		envelope.JobID = *entry.LastJobID
	}
	if entry.LastRunID != nil && *entry.LastRunID != "" {
		envelope.RunID = *entry.LastRunID
	}
	failed := entry.LastJobState == jobs.StateFailed || entry.LastJobState == jobs.StateCanceled
	if failed {
		envelope.ErrorCode = dictionaryErrorCode(entry.LastJobErrorCode)
		envelope.GenerationErrorCode = dictionaryErrorCode(entry.LastJobErrorCode)
	}
	switch envelope.Status {
	case StatusQueued:
		envelope.GenerationStatus = GenerationQueued
	case StatusGenerating:
		envelope.GenerationStatus = GenerationRunning
	case StatusFailed:
		envelope.GenerationStatus = GenerationFailed
	case StatusReady:
		envelope.ErrorCode = ""
		switch {
		case entry.LastJobState == jobs.StateQueued:
			envelope.GenerationStatus = GenerationQueued
		case entry.LastJobState == jobs.StateLeased || entry.LastJobState == jobs.StateRunning:
			envelope.GenerationStatus = GenerationRunning
		case failed:
			envelope.GenerationStatus = GenerationFailed
		}
		var document semantics.DictionaryDocument
		if err := json.Unmarshal([]byte(*entry.DocumentJSON), &document); err == nil {
			envelope.Document = &document
		}
	}
	return envelope
}

// dictionaryErrorCode translates a stored job error code into the stable
// dictionary codes; unknown or empty codes map to generation_failed.
func dictionaryErrorCode(code string) string {
	switch code {
	case "v1.dictionary_provider_unavailable", "v1.dictionary_provider_changed",
		"v1.dictionary_invalid_output", "v1.dictionary_contract_changed",
		"v1.dictionary_generation_failed", "v1.dictionary_storage_failed",
		jobs.LeaseExpiredErrorCode:
		if code == jobs.LeaseExpiredErrorCode {
			return "v1.dictionary_generation_failed"
		}
		return code
	case "":
		return "v1.dictionary_generation_failed"
	default:
		return "v1.dictionary_generation_failed"
	}
}

// Reference is exactly one article-scoped explore reference.
type Reference struct {
	OccurrenceID library.ULID
	AnnotationID library.ULID
}

// Service performs read and explicit-start flows for dictionary entries.
type Service struct {
	store    *Store
	jobs     *jobs.Store
	resolver BindingResolver
}

func NewService(db *store.DB, resolver BindingResolver) *Service {
	return &Service{store: NewStore(db), jobs: jobs.NewStore(db), resolver: resolver}
}

// Lookup resolves the subject for an article reference and returns the shared
// entry status. It performs no writes and no provider checks.
func (s *Service) Lookup(ctx context.Context, articleID library.ULID, ref Reference) (StatusEnvelope, *Subject, error) {
	subject, err := s.resolveSubject(ctx, articleID, ref)
	if err != nil {
		return StatusEnvelope{}, nil, err
	}
	entry, err := s.store.GetEntryByKey(ctx, subject.SourceLanguage, subject.TargetLanguage, subject.LookupKind, subject.NormalizedForm)
	if errors.Is(err, ErrNotFound) {
		return envelopeFor(nil, subject), subject, nil
	}
	if err != nil {
		return StatusEnvelope{}, nil, err
	}
	return envelopeFor(entry, subject), subject, nil
}

// Start implements the explicit POST flow: resolve, read saved data first,
// resolve the Explore generation outside the write transaction, then
// atomically insert-or-find, recheck, enqueue, and point last_job_id. A ready
// entry is never overwritten by ensure or retry; regenerate=true always
// starts a fresh request (keeping the saved document readable until a
// validated replacement publishes). Concurrent requests converge on one
// active job through the database unique key and last_job_id compare-and-set.
// retry and regenerate are mutually exclusive.
func (s *Service) Start(ctx context.Context, articleID library.ULID, ref Reference, retry, regenerate bool) (StatusEnvelope, *Subject, bool, error) {
	if retry && regenerate {
		return StatusEnvelope{}, nil, false, ErrAmbiguousRequest
	}
	subject, err := s.resolveSubject(ctx, articleID, ref)
	if err != nil {
		return StatusEnvelope{}, nil, false, err
	}
	existing, err := s.store.GetEntryByKey(ctx, subject.SourceLanguage, subject.TargetLanguage, subject.LookupKind, subject.NormalizedForm)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return StatusEnvelope{}, nil, false, err
	}
	if existing.Ready() && !regenerate {
		return envelopeFor(existing, subject), subject, false, nil
	}
	if existing != nil && (existing.LastJobState == jobs.StateQueued || existing.LastJobState == jobs.StateLeased || existing.LastJobState == jobs.StateRunning) {
		return envelopeFor(existing, subject), subject, false, nil
	}
	// An existing failed entry requires an explicit retry or regenerate.
	if existing != nil && existing.LastJobState != "" && !retry && !regenerate {
		return envelopeFor(existing, subject), subject, false, nil
	}

	// Resolve the usable Explore binding and its pinned prompt snapshots
	// outside the write transaction.
	if s.resolver == nil {
		return StatusEnvelope{}, nil, false, ErrProviderUnavailable
	}
	generation, err := s.resolver(ctx)
	if err != nil {
		return StatusEnvelope{}, nil, false, ErrProviderUnavailable
	}

	input := semantics.DictionaryInput{
		Version: semantics.DictionaryContractVersion, LookupForm: subject.LookupForm,
		LookupKind: subject.LookupKind, SourceLanguage: subject.SourceLanguage,
		TargetLanguage: subject.TargetLanguage, KnownTranslations: []string{},
	}
	if err := input.Validate(); err != nil {
		return StatusEnvelope{}, nil, false, fmt.Errorf("dictionary: request input: %w", err)
	}

	var previousJobID string
	if existing != nil && existing.LastJobID != nil {
		previousJobID = *existing.LastJobID
	}
	started := false
	var envelope StatusEnvelope
	err = s.store.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		entry, err := EnsureEntryTx(ctx, tx, subject)
		if err != nil {
			return err
		}
		// Recheck inside the transaction: another click may have created the
		// entry and its active job after the initial read.
		fresh, err := EntryTx(ctx, tx, entry.ID)
		if err != nil {
			return err
		}
		attachLastJobTx(ctx, tx, fresh)
		if fresh.Ready() && !regenerate {
			envelope = envelopeFor(fresh, subject)
			return nil
		}
		if fresh.LastJobState == jobs.StateQueued || fresh.LastJobState == jobs.StateLeased || fresh.LastJobState == jobs.StateRunning {
			envelope = envelopeFor(fresh, subject)
			return nil
		}
		if fresh.LastJobState != "" && fresh.LastJobID != nil && !retry && !regenerate {
			previousJobID = *fresh.LastJobID
			envelope = envelopeFor(fresh, subject)
			return nil
		}
		if fresh.LastJobID != nil {
			previousJobID = *fresh.LastJobID
		}
		payload, err := jobPayload(entry.ID.String(), articleID.String(), input, generation, regenerate)
		if err != nil {
			return err
		}
		jobID := library.NewULID()
		spec := jobs.Spec{
			JobType: JobType, ExecutionTarget: jobs.TargetServer,
			OwnerType: OwnerType, OwnerID: entry.ID.String(),
			IdempotencyKey: fmt.Sprintf("%s:%s:%s", JobType, entry.ID.String(), jobID.String()),
			InputHash:      payload.InputHash, PayloadJSON: payload.validatedJSON,
			MaxAttempts: 1,
		}
		job, err := jobs.EnqueueTx(ctx, tx, spec)
		if err != nil {
			return err
		}
		if err := SetLastJobTx(ctx, tx, entry.ID, previousJobID, job.ID.String()); err != nil {
			return err
		}
		started = true
		final, err := EntryTx(ctx, tx, entry.ID)
		if err != nil {
			return err
		}
		final.LastJobState = job.State
		final.LastJobID = nil
		jobIDString := job.ID.String()
		final.LastJobID = &jobIDString
		envelope = envelopeFor(final, subject)
		return nil
	})
	if err != nil {
		return StatusEnvelope{}, nil, false, err
	}
	return envelope, subject, started, nil
}

func attachLastJobTx(ctx context.Context, tx *sql.Tx, entry *Entry) {
	if entry == nil || entry.LastJobID == nil || *entry.LastJobID == "" {
		return
	}
	var state, errorCode string
	err := tx.QueryRowContext(ctx, `SELECT state, error_code FROM job WHERE id = ?`, *entry.LastJobID).Scan(&state, &errorCode)
	if err != nil {
		entry.LastJobID = nil
		return
	}
	entry.LastJobState, entry.LastJobErrorCode = state, errorCode
}

func (s *Service) resolveSubject(ctx context.Context, articleID library.ULID, ref Reference) (*Subject, error) {
	if (ref.OccurrenceID.IsZero() && ref.AnnotationID.IsZero()) || (!ref.OccurrenceID.IsZero() && !ref.AnnotationID.IsZero()) {
		return nil, ErrNotFound
	}
	if !ref.OccurrenceID.IsZero() {
		return s.store.ResolveArticleOccurrence(ctx, articleID, ref.OccurrenceID)
	}
	return s.store.ResolveArticleAnnotation(ctx, articleID, ref.AnnotationID)
}

// GetEntryByID is the shared polling/read surface that does not need the
// original article.
func (s *Service) GetEntryByID(ctx context.Context, id library.ULID) (StatusEnvelope, error) {
	entry, err := s.store.GetEntry(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return StatusEnvelope{}, ErrNotFound
	}
	if err != nil {
		return StatusEnvelope{}, err
	}
	return envelopeFor(entry, nil), nil
}

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
	ErrProviderUnavailable = errors.New("dictionary: translation provider is unavailable")
	ErrNonLexical          = ErrNotFound
)

// BindingResolver resolves the active profile's translation binding through
// the shared usability checks. It runs outside any write transaction.
type BindingResolver func(ctx context.Context) (pipeline.BindingSnapshot, error)

// JobPayload is the immutable dictionary job snapshot: derived subject,
// dictionary contract/prompt versions, exact prompt input, input hash, and
// the resolved translation binding. It contains no article prose, no secret,
// and no endpoint.
type JobPayload struct {
	ContractVersion string                    `json:"contract_version"`
	PromptVersion   string                    `json:"prompt_version"`
	EntryID         string                    `json:"entry_id"`
	Input           semantics.DictionaryInput `json:"input"`
	InputHash       string                    `json:"input_hash"`
	Binding         pipeline.BindingSnapshot  `json:"binding"`

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
	if err := p.Input.Validate(); err != nil {
		return fmt.Errorf("dictionary job payload input: %w", err)
	}
	if p.InputHash != InputHash(p.Input) {
		return errors.New("dictionary job payload input hash does not match its input")
	}
	if err := p.Binding.Validate(); err != nil {
		return fmt.Errorf("dictionary job payload binding: %w", err)
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

func jobPayload(entryID string, input semantics.DictionaryInput, binding pipeline.BindingSnapshot) (JobPayload, error) {
	payload := JobPayload{
		ContractVersion: semantics.DictionaryContractVersion,
		PromptVersion:   semantics.DictionaryPromptVersion,
		EntryID:         entryID,
		Input:           input,
		InputHash:       InputHash(input),
		Binding:         binding,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return payload, err
	}
	payload.validatedJSON = string(encoded)
	return payload, payload.Validate()
}

// Subject is re-declared in service context for the envelope.
// StatusEnvelope is the shared read/poll response body.
type StatusEnvelope struct {
	Status    string                        `json:"status"`
	EntryID   string                        `json:"entry_id,omitempty"`
	JobID     string                        `json:"job_id,omitempty"`
	Subject   *SubjectView                  `json:"subject,omitempty"`
	Document  *semantics.DictionaryDocument `json:"document,omitempty"`
	ErrorCode string                        `json:"error_code,omitempty"`
}

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

// envelopeFor renders the persisted entry status.
func envelopeFor(entry *Entry, subject *Subject) StatusEnvelope {
	envelope := StatusEnvelope{
		Subject: subjectView(subject),
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
	if entry.LastJobState == jobs.StateFailed || entry.LastJobState == jobs.StateCanceled {
		envelope.ErrorCode = dictionaryErrorCode(entry.LastJobErrorCode)
	}
	if entry.Ready() {
		envelope.Status = StatusReady
		envelope.ErrorCode = ""
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
// resolve the binding outside the write transaction, then atomically
// insert-or-find, recheck, enqueue, and point last_job_id. A ready entry is
// never overwritten, even with retry. Concurrent clicks converge on one
// active job through the database unique key and last_job_id compare-and-set.
func (s *Service) Start(ctx context.Context, articleID library.ULID, ref Reference, retry bool) (StatusEnvelope, *Subject, bool, error) {
	subject, err := s.resolveSubject(ctx, articleID, ref)
	if err != nil {
		return StatusEnvelope{}, nil, false, err
	}
	existing, err := s.store.GetEntryByKey(ctx, subject.SourceLanguage, subject.TargetLanguage, subject.LookupKind, subject.NormalizedForm)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return StatusEnvelope{}, nil, false, err
	}
	if existing.Ready() {
		return envelopeFor(existing, subject), subject, false, nil
	}
	if existing != nil && (existing.LastJobState == jobs.StateQueued || existing.LastJobState == jobs.StateLeased || existing.LastJobState == jobs.StateRunning) {
		return envelopeFor(existing, subject), subject, false, nil
	}
	// An existing failed entry requires an explicit retry.
	if existing != nil && existing.LastJobState != "" && !retry {
		return envelopeFor(existing, subject), subject, false, nil
	}

	// Resolve the usable translation binding outside the write transaction.
	if s.resolver == nil {
		return StatusEnvelope{}, nil, false, ErrProviderUnavailable
	}
	binding, err := s.resolver(ctx)
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
		if fresh.Ready() {
			envelope = envelopeFor(fresh, subject)
			return nil
		}
		if fresh.LastJobState == jobs.StateQueued || fresh.LastJobState == jobs.StateLeased || fresh.LastJobState == jobs.StateRunning {
			envelope = envelopeFor(fresh, subject)
			return nil
		}
		if fresh.LastJobState != "" && fresh.LastJobID != nil && !retry {
			previousJobID = *fresh.LastJobID
			envelope = envelopeFor(fresh, subject)
			return nil
		}
		if fresh.LastJobID != nil {
			previousJobID = *fresh.LastJobID
		}
		payload, err := jobPayload(entry.ID.String(), input, binding)
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

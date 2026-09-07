// Server worker for reader.dictionary.v1 jobs. It claims only dictionary
// server jobs through the shared scheduler, re-verifies the snapshotted
// translation binding against the live registry, and publishes an accepted
// document together with the job acknowledgement inside one transaction.
package dictionary

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// Stable dictionary job error codes. Validation corrections happen inside the
// single attempt; MaxAttempts is 1, so every failure is terminal and needs an
// explicit user Retry.
const (
	CodeProviderUnavailable = "v1.dictionary_provider_unavailable"
	CodeProviderChanged     = "v1.dictionary_provider_changed"
	CodeInvalidOutput       = "v1.dictionary_invalid_output"
	CodeContractChanged     = "v1.dictionary_contract_changed"
	CodeGenerationFailed    = "v1.dictionary_generation_failed"
	CodeStorageFailed       = "v1.dictionary_storage_failed"
)

// providerRegistry is the narrow registry seam; annotator.Registry satisfies it.
type providerRegistry interface {
	Provider(id string) (annotator.Provider, bool)
}

// Runner executes dictionary jobs.
type Runner struct {
	db       *store.DB
	jobs     *jobs.Store
	store    *Store
	registry providerRegistry
	owner    string
	// heartbeatInterval renews the lease while a provider call runs;
	// production uses twenty seconds, tests shorten it.
	heartbeatInterval time.Duration
}

// NewRunner builds the runner over an open database.
func NewRunner(db *store.DB, registry providerRegistry) *Runner {
	return &Runner{
		db: db, jobs: jobs.NewStore(db), store: NewStore(db), registry: registry,
		owner: "server-dictionary", heartbeatInterval: 20 * time.Second,
	}
}

// SetHeartbeatInterval overrides the lease heartbeat cadence; tests use it
// to keep runner loops synchronous.
func (r *Runner) SetHeartbeatInterval(interval time.Duration) { r.heartbeatInterval = interval }

// Run polls until the context is canceled.
func (r *Runner) Run(ctx context.Context) {
	for {
		if err := r.RunOnce(ctx); err != nil && !errors.Is(err, jobs.ErrNoWork) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("dictionary: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// RunOnce leases and processes at most one dictionary server job. Article
// runners must never claim this type; this runner must never claim theirs.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil || r.jobs == nil || r.store == nil {
		return errors.New("dictionary: nil runner")
	}
	if _, err := r.jobs.RecoverExpired(ctx); err != nil {
		return err
	}
	lease, err := r.jobs.ClaimMatching(ctx, jobs.TargetServer, r.owner, func(job jobs.Job) bool {
		return job.JobType == JobType && job.OwnerType == OwnerType
	})
	if err != nil {
		return err
	}
	return r.process(ctx, lease)
}

func (r *Runner) process(ctx context.Context, lease *jobs.Lease) error {
	payload, err := DecodeJobPayload([]byte(lease.PayloadJSON))
	if err != nil {
		// A payload written by a different operation version fails closed as
		// a contract change; any other decode failure is a generation failure.
		code := CodeGenerationFailed
		var payloadProbe JobPayload
		if decodeErr := json.Unmarshal([]byte(lease.PayloadJSON), &payloadProbe); decodeErr == nil &&
			(payloadProbe.ContractVersion != semantics.DictionaryContractVersion || payloadProbe.PromptVersion != semantics.DictionaryPromptVersion) {
			code = CodeContractChanged
		}
		return r.failJob(ctx, lease, code)
	}

	// Verify the snapshotted provider against the registry before any call:
	// a removed, disabled, re-typed, or reconfigured provider fails
	// explicitly instead of silently substituting another model.
	provider, ok := r.registry.Provider(payload.Binding.ProviderID)
	if !ok {
		return r.failJob(ctx, lease, CodeProviderUnavailable)
	}
	descriptor := provider.Descriptor()
	if !descriptor.Enabled {
		return r.failJob(ctx, lease, CodeProviderUnavailable)
	}
	if descriptor.Type != payload.Binding.ProviderType {
		return r.failJob(ctx, lease, CodeProviderChanged)
	}
	if payload.Binding.ProviderConfigFingerprint != "" && descriptor.ConfigFingerprint != payload.Binding.ProviderConfigFingerprint {
		return r.failJob(ctx, lease, CodeProviderChanged)
	}
	binding, err := annotator.ResolveBinding(payload.Binding)
	if err != nil {
		return r.failJob(ctx, lease, CodeProviderChanged)
	}

	// Per-run cancelable context: the heartbeat cancels it the moment the
	// job is canceled or the lease is lost so an in-flight provider call
	// aborts. The lease error is retained and publication never happens.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var leaseErrMu sync.Mutex
	var heartbeatLeaseErr error
	retainLeaseError := func(err error) {
		leaseErrMu.Lock()
		if heartbeatLeaseErr == nil {
			heartbeatLeaseErr = err
		}
		leaseErrMu.Unlock()
		cancelRun()
	}
	retainedLeaseError := func() error {
		leaseErrMu.Lock()
		defer leaseErrMu.Unlock()
		return heartbeatLeaseErr
	}
	stopHeartbeat := r.startHeartbeat(runCtx, lease, retainLeaseError)
	defer stopHeartbeat()

	started := time.Now()
	result, err := annotator.GenerateDictionary(runCtx, provider, binding, payload.Input)
	if err != nil {
		// A run whose lease was lost or whose job was canceled must never be
		// failed by this stale worker; the scheduler owns that transition.
		if cause := retainedLeaseError(); cause != nil {
			return fmt.Errorf("dictionary: run aborted: %w", cause)
		}
		return r.failDictionaryError(ctx, lease, err)
	}
	if cause := retainedLeaseError(); cause != nil {
		return fmt.Errorf("dictionary: run aborted: %w", cause)
	}

	documentJSON, err := json.Marshal(result.Document)
	if err != nil {
		return r.failJob(ctx, lease, CodeGenerationFailed)
	}
	documentHash, err := result.Document.Hash()
	if err != nil {
		return r.failJob(ctx, lease, CodeGenerationFailed)
	}
	provenance, err := json.Marshal(provenanceFor(payload, binding, result, started))
	if err != nil {
		return r.failJob(ctx, lease, CodeGenerationFailed)
	}
	entryID, err := library.ParseULID(payload.EntryID)
	if err != nil {
		return r.failJob(ctx, lease, CodeGenerationFailed)
	}

	// One transaction stores the accepted document and acknowledges the job.
	// PublishTx requires the entry to still point at this job; CompleteTx
	// requires the live lease. A stale or canceled worker publishes nothing.
	publishErr := r.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := PublishTx(ctx, tx, entryID, lease.ID.String(), string(documentJSON), documentHash,
			semantics.DictionaryContractVersion, semantics.DictionaryPromptVersion, string(provenance)); err != nil {
			return err
		}
		return jobs.CompleteTx(ctx, tx, lease.ID, lease.AttemptCount, lease.LeaseToken)
	})
	if publishErr != nil {
		return fmt.Errorf("dictionary: publish: %w", publishErr)
	}
	return nil
}

func (r *Runner) failDictionaryError(ctx context.Context, lease *jobs.Lease, err error) error {
	var stageErr *annotator.StageError
	code := CodeGenerationFailed
	if errors.As(err, &stageErr) {
		switch stageErr.Code {
		case annotator.CodeInvalidOutput:
			code = CodeInvalidOutput
		case annotator.CodeUnavailable:
			code = CodeProviderUnavailable
		}
	}
	return r.failJob(ctx, lease, code)
}

func (r *Runner) failJob(ctx context.Context, lease *jobs.Lease, code string) error {
	if err := r.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, code, false); err != nil {
		return fmt.Errorf("dictionary job %s failed: %w", code, err)
	}
	return nil
}

func (r *Runner) startHeartbeat(runCtx context.Context, lease *jobs.Lease, onLeaseLost func(error)) func() {
	stop := make(chan struct{})
	var once sync.Once
	stopFn := func() { once.Do(func() { close(stop) }) }
	if r.heartbeatInterval <= 0 {
		return stopFn
	}
	go func() {
		ticker := time.NewTicker(r.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				heartbeat, err := r.jobs.Heartbeat(runCtx, lease.ID, lease.AttemptCount, lease.LeaseToken, 0)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						onLeaseLost(err)
					}
					return
				}
				if heartbeat.CancelRequested {
					onLeaseLost(jobs.ErrLeaseLost)
					return
				}
			}
		}
	}()
	return stopFn
}

// dictionaryProvenance is the sanitized completion record. It never contains
// credentials, endpoints, prompts, or raw responses.
type dictionaryProvenance struct {
	ProviderID        string `json:"provider_id"`
	ProviderType      string `json:"provider_type"`
	RequestedModel    string `json:"requested_model"`
	ReportedModel     string `json:"reported_model,omitempty"`
	OptionsHash       string `json:"options_hash"`
	ConfigFingerprint string `json:"config_fingerprint"`
	ContractVersion   string `json:"contract_version"`
	PromptVersion     string `json:"prompt_version"`
	RequestID         string `json:"request_id,omitempty"`
	FinishReason      string `json:"finish_reason,omitempty"`
	UsageJSON         string `json:"usage_json,omitempty"`
	TimingJSON        string `json:"timing_json,omitempty"`
	Corrections       int    `json:"corrections"`
	DurationMS        int64  `json:"duration_ms"`
}

func provenanceFor(payload JobPayload, binding annotator.ResolvedBinding, result *annotator.DictionaryGenerateResult, started time.Time) dictionaryProvenance {
	return dictionaryProvenance{
		ProviderID: binding.ProviderID, ProviderType: binding.ProviderType,
		RequestedModel: payload.Binding.ModelID, ReportedModel: result.Attempt.ReportedModel,
		OptionsHash: binding.OptionsHash, ConfigFingerprint: binding.ConfigFingerprint,
		ContractVersion: semantics.DictionaryContractVersion, PromptVersion: semantics.DictionaryPromptVersion,
		RequestID: result.Attempt.RequestID, FinishReason: result.Attempt.FinishReason,
		UsageJSON: result.Attempt.UsageJSON, TimingJSON: result.Attempt.TimingJSON,
		Corrections: len(result.Attempt.Turns) - 1, DurationMS: time.Since(started).Milliseconds(),
	}
}

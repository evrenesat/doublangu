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
	"strings"
	"sync"
	"time"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
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
	history  *analysis.HistoryStore
	registry providerRegistry
	owner    string
	// heartbeatInterval renews the lease while a provider call runs;
	// production uses twenty seconds, tests shorten it.
	heartbeatInterval time.Duration
}

// NewRunner builds the runner over an open database.
func NewRunner(db *store.DB, registry providerRegistry) *Runner {
	return &Runner{
		db: db, jobs: jobs.NewStore(db), store: NewStore(db), history: analysis.NewHistoryStore(db),
		registry: registry, owner: "server-dictionary", heartbeatInterval: 20 * time.Second,
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
	// Scheduler reconciliation: finalize runs abandoned by expired or
	// canceled jobs, preserving every partial record.
	if _, err := r.history.ReconcileTerminalJobRuns(ctx); err != nil {
		log.Printf("dictionary: terminal-job run reconciliation failed: %v", err)
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

	history := analysis.NewHistoryStore(r.db)

	// The run is created before provider resolution so disabled or missing
	// providers, bad prompt selections, and other preflight failures are
	// visible in history, not just as a job error code.
	if strings.TrimSpace(payload.ArticleID) == "" {
		return r.failJob(ctx, lease, CodeContractChanged)
	}
	articleID := library.ULID(payload.ArticleID)
	entryID, err := library.ParseULID(payload.EntryID)
	if err != nil {
		return r.failJob(ctx, lease, CodeGenerationFailed)
	}
	run, err := history.StartRun(ctx, analysis.RunStart{
		ArticleID: articleID, ArticleTitle: payload.Input.LookupForm, JobID: lease.ID,
		AttemptCount: lease.AttemptCount, ContentHash: payload.InputHash,
		ContractVersion: payload.ContractVersion, PromptVersion: payload.PromptVersion,
		RequestedModel: payload.Binding.ModelID, RequestedEffort: requestedEffort(payload.Binding),
		ProviderID: payload.Binding.ProviderID, TotalParagraphs: 1,
		OperationType: "explore", SubjectID: payload.EntryID, SubjectLabel: payload.Input.LookupForm,
	})
	if err != nil {
		log.Printf("dictionary: run start failed for entry %s: %v", payload.EntryID, err)
		return r.failJob(ctx, lease, CodeStorageFailed)
	}
	if err := r.store.SetLastRun(ctx, entryID, run.ID.String()); err != nil {
		log.Printf("dictionary: last_run_id update failed for entry %s: %v", payload.EntryID, err)
		return r.failJob(ctx, lease, CodeStorageFailed)
	}

	// Every terminal path finalizes the run failed with the stable code,
	// preserving the attempt and turn records collected so far.
	finalizeFailed := func(code, errorPhase, detail string) error {
		_ = history.FinishRun(ctx, run.ID, analysis.RunFinish{
			Status: "failed", ErrorCode: code, ErrorDetail: detail, DurationMS: 0,
		})
		_ = history.SetRunPipelineFailure(ctx, run.ID.String(), string(pipeline.StageTranslation), payload.Binding.ProviderID)
		return r.failJob(ctx, lease, code)
	}

	// Verify the snapshotted provider against the registry before any call:
	// a removed, disabled, re-typed, or reconfigured provider fails
	// explicitly instead of silently substituting another model.
	provider, ok := r.registry.Provider(payload.Binding.ProviderID)
	if !ok {
		return finalizeFailed(CodeProviderUnavailable, "preflight", "explore provider is not configured")
	}
	descriptor := provider.Descriptor()
	if !descriptor.Enabled {
		return finalizeFailed(CodeProviderUnavailable, "preflight", "explore provider is disabled")
	}
	if descriptor.Type != payload.Binding.ProviderType {
		return finalizeFailed(CodeProviderChanged, "preflight", "explore provider type changed")
	}
	if payload.Binding.ProviderConfigFingerprint != "" && descriptor.ConfigFingerprint != payload.Binding.ProviderConfigFingerprint {
		return finalizeFailed(CodeProviderChanged, "preflight", "explore provider configuration changed")
	}
	binding, err := annotator.ResolveBinding(payload.Binding)
	if err != nil {
		return finalizeFailed(CodeProviderChanged, "preflight", "explore binding could not be resolved")
	}

	// Captured prompt snapshots become the executed instructions; their bytes
	// must verify against the captured hashes.
	stagePrompts, promptErr := exploreStagePrompts(payload)
	if promptErr != nil {
		return finalizeFailed(CodeContractChanged, "preflight", promptErr.Error())
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

	// One logical attempt occupies block index 0 with the transport stage
	// identity; the explore operation identity lives in the run row.
	attempt, err := history.StartStageAttempt(ctx, analysis.StageAttempt{
		RunID: run.ID.String(), BlockIndex: 0, StageID: string(pipeline.StageTranslation),
		ProviderID: payload.Binding.ProviderID, ProviderType: payload.Binding.ProviderType,
		ConfigFingerprint: payload.Binding.ProviderConfigFingerprint, ModelID: payload.Binding.ModelID,
		RequestedModel: payload.Binding.ModelID, ContractVersion: payload.ContractVersion,
		PromptVersion: promptVersionFor(payload), InputHash: payload.InputHash,
	})
	if err != nil {
		log.Printf("dictionary: attempt start failed for entry %s: %v", payload.EntryID, err)
		return finalizeFailed(CodeStorageFailed, "storage", "stage attempt could not be recorded")
	}

	started := time.Now()
	result, generateErr := annotator.GenerateDictionary(runCtx, provider, binding, payload.Input, stagePrompts,
		annotator.WithTurnRecorder(r.stageTurnRecorder(attempt.ID)))
	if generateErr != nil {
		// A run whose lease was lost or whose job was canceled must never be
		// failed by this stale worker; the scheduler owns that transition.
		if cause := retainedLeaseError(); cause != nil {
			return fmt.Errorf("dictionary: run aborted: %w", cause)
		}
		// Result-plus-error: retain every turn collected before the failure.
		finish := stageFailureFinish(result, generateErr)
		if finish.ErrorCode == "" {
			finish.ErrorCode = CodeGenerationFailed
		}
		if err := history.FinishStageAttempt(ctx, attempt.ID, finish); err != nil {
			log.Printf("dictionary: attempt finish failed for entry %s: %v", payload.EntryID, err)
		}
		code := r.dictionaryErrorCodeFor(generateErr)
		_ = history.FinishRun(ctx, run.ID, analysis.RunFinish{
			Status: "failed", ErrorCode: code, ErrorDetail: safeGenerationDetail(generateErr),
		})
		_ = history.SetRunPipelineFailure(ctx, run.ID.String(), string(pipeline.StageTranslation), payload.Binding.ProviderID)
		return r.failJob(ctx, lease, code)
	}
	if cause := retainedLeaseError(); cause != nil {
		return fmt.Errorf("dictionary: run aborted: %w", cause)
	}

	documentJSON, err := json.Marshal(result.Document)
	if err != nil {
		return finalizeFailed(CodeGenerationFailed, "storage", "validated document could not be encoded")
	}
	documentHash, err := result.Document.Hash()
	if err != nil {
		return finalizeFailed(CodeGenerationFailed, "storage", "validated document could not be hashed")
	}
	provenance, err := json.Marshal(provenanceFor(payload, binding, result, started))
	if err != nil {
		return finalizeFailed(CodeGenerationFailed, "storage", "provenance could not be encoded")
	}

	durationMS := time.Since(started).Milliseconds()
	succeededFinish := stageFinishFromResult(result, durationMS)

	// One transaction replaces the saved document, acknowledges the job with
	// the live lease compare-and-set, and completes the history records:
	// history storage failure rolls the whole publication back instead of
	// silently claiming complete diagnostics.
	publishErr := r.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := PublishTx(ctx, tx, entryID, lease.ID.String(), string(documentJSON), documentHash,
			semantics.DictionaryContractVersion, semantics.DictionaryPromptVersion, string(provenance)); err != nil {
			return err
		}
		if err := jobs.CompleteTx(ctx, tx, lease.ID, lease.AttemptCount, lease.LeaseToken); err != nil {
			return err
		}
		if err := history.FinishStageAttemptTx(ctx, tx, attempt.ID, succeededFinish); err != nil {
			return fmt.Errorf("stage attempt completion: %w", err)
		}
		return history.FinishRunTx(ctx, tx, run.ID, analysis.RunFinish{
			Status: "succeeded", ReportedModel: result.Attempt.ReportedModel, DurationMS: durationMS,
		})
	})
	if publishErr != nil {
		log.Printf("dictionary: publish failed for entry %s: %v", payload.EntryID, publishErr)
		return fmt.Errorf("dictionary: publish: %w", publishErr)
	}
	return nil
}

// exploreStagePrompts verifies the payload's captured prompt snapshots and
// returns the executed instruction pair for the explore operation.
func exploreStagePrompts(payload JobPayload) (annotator.StagePrompts, error) {
	byType := make(map[string]pipeline.PromptSnapshot, 2)
	for index, snapshot := range payload.PromptSnapshots {
		if snapshot.Type != "explore" && snapshot.Type != "correction" {
			return annotator.StagePrompts{}, fmt.Errorf("unsupported explore prompt type %q at %d", snapshot.Type, index)
		}
		if snapshot.EnvelopeVersion != pipeline.PromptCapturedEnvelopeVersion {
			return annotator.StagePrompts{}, fmt.Errorf("unsupported envelope version %q for %s", snapshot.EnvelopeVersion, snapshot.Type)
		}
		if prompts.ContentHashOf(snapshot.InstructionText) != snapshot.ContentHash {
			return annotator.StagePrompts{}, fmt.Errorf("%s prompt bytes do not match the captured content hash", snapshot.Type)
		}
		byType[snapshot.Type] = snapshot
	}
	generation, hasGeneration := byType["explore"]
	correction, hasCorrection := byType["correction"]
	if !hasGeneration || !hasCorrection {
		return annotator.StagePrompts{}, errors.New("dictionary payload is missing its explore or correction prompt snapshot")
	}
	return annotator.CapturedStagePrompts(generation.InstructionText, correction.InstructionText), nil
}

// promptVersionFor derives the effective explore prompt identity recorded on
// the attempt: operation, generation content hash, correction content hash,
// and the captured envelope version.
func promptVersionFor(payload JobPayload) string {
	var generation, correction string
	for _, snapshot := range payload.PromptSnapshots {
		if snapshot.Type == "explore" {
			generation = snapshot.ContentHash
		}
		if snapshot.Type == "correction" {
			correction = snapshot.ContentHash
		}
	}
	return pipeline.EffectivePromptVersion("explore", generation, correction, pipeline.PromptCapturedEnvelopeVersion)
}

// requestedEffort extracts the codex reasoning effort for run provenance.
func requestedEffort(binding pipeline.BindingSnapshot) string {
	var options struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(binding.Options, &options); err != nil {
		return ""
	}
	return options.ReasoningEffort
}

// stageFailureFinish builds the failed attempt record from the retained
// result plus error: every completed turn is already inside the result.
func stageFailureFinish(result *annotator.DictionaryGenerateResult, err error) analysis.StageAttemptFinish {
	finish := analysis.StageAttemptFinish{Status: "failed"}
	if result != nil {
		finish.ReportedModel = result.Attempt.ReportedModel
		finish.RequestID = result.Attempt.RequestID
		finish.UsageJSON = result.Attempt.UsageJSON
		finish.TimingJSON = result.Attempt.TimingJSON
	}
	var stageErr *annotator.StageError
	if errors.As(err, &stageErr) {
		finish.ErrorPhase = stageErr.Phase
		finish.ErrorCode = stageErr.Code
		finish.ErrorDetail = stageErr.Error()
	}
	return finish
}

func stageFinishFromResult(result *annotator.DictionaryGenerateResult, durationMS int64) analysis.StageAttemptFinish {
	return analysis.StageAttemptFinish{
		Status: "succeeded", ReportedModel: result.Attempt.ReportedModel, RequestID: result.Attempt.RequestID,
		FinishReason: result.Attempt.FinishReason, UsageJSON: result.Attempt.UsageJSON,
		TimingJSON: result.Attempt.TimingJSON, MetadataJSON: result.Attempt.MetadataJSON,
		DurationMS: durationMS,
	}
}

// safeGenerationDetail is the bounded failure detail retained on the run:
// stable stage code and phase only, never provider payloads.
func safeGenerationDetail(err error) string {
	var stageErr *annotator.StageError
	if errors.As(err, &stageErr) {
		return stageErr.Error()
	}
	return err.Error()
}

func (r *Runner) dictionaryErrorCodeFor(err error) string {
	var stageErr *annotator.StageError
	if errors.As(err, &stageErr) {
		switch stageErr.Code {
		case annotator.CodeInvalidOutput:
			return CodeInvalidOutput
		case annotator.CodeUnavailable:
			return CodeProviderUnavailable
		}
	}
	return CodeGenerationFailed
}

// stageTurnRecorder returns the prompt turn recorder for the explore
// attempt: every completed or failed turn is persisted while its artifacts
// are still available; recording failures abort with a storage error.
func (r *Runner) stageTurnRecorder(attemptID string) annotator.TurnRecorder {
	return func(ctx context.Context, record annotator.StageTurnRecord) error {
		return r.history.AppendStageTurn(ctx, analysis.StageTurn{
			AttemptID: attemptID, TurnIndex: record.TurnIndex, TurnKind: record.TurnKind,
			Prompt: record.Prompt, OutputSchema: record.OutputSchema,
			CompletedResponse: record.CompletedResponse, ResponseHash: record.ResponseHash,
			ValidationError: record.ValidationError, ProviderError: record.ProviderError,
			CompletionMetadata: record.CompletionMetadata,
			StartedAt:          record.StartedAt, CompletedAt: record.CompletedAt,
			DurationMS: record.DurationMS, Status: record.Status,
		})
	}
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

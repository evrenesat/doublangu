// Server worker for reader.sentence_translation.v1 jobs. It claims only
// sentence server jobs through the shared scheduler, reuses the run claimed
// by the service before provider preflight, re-verifies the snapshotted
// Translation binding against the live registry, and publishes an accepted
// translation together with the job acknowledgement inside one transaction.
package sentencetranslation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	"doublangu/internal/store"
)

// providerRegistry is the narrow registry seam; annotator.Registry satisfies it.
type providerRegistry interface {
	Provider(id string) (annotator.Provider, bool)
}

// Runner executes sentence translation jobs.
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
		registry: registry, owner: "server-sentence-translation", heartbeatInterval: 20 * time.Second,
	}
}

// SetHeartbeatInterval overrides the lease heartbeat cadence; tests use it
// to keep runner loops synchronous.
func (r *Runner) SetHeartbeatInterval(interval time.Duration) { r.heartbeatInterval = interval }

// Run polls until the context is canceled.
func (r *Runner) Run(ctx context.Context) {
	for {
		if err := r.RunOnce(ctx); err != nil && !errors.Is(err, jobs.ErrNoWork) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("sentencetranslation: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// RunOnce leases and processes at most one sentence server job. Dictionary
// and article runners must never claim this type; this runner must never
// claim theirs.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil || r.jobs == nil || r.store == nil {
		return errors.New("sentencetranslation: nil runner")
	}
	if _, err := r.jobs.RecoverExpired(ctx); err != nil {
		return err
	}
	// Scheduler reconciliation: finalize runs abandoned by expired or
	// canceled jobs, preserving every partial record, then close claim
	// orphans that never received a job.
	if _, err := r.history.ReconcileTerminalJobRuns(ctx); err != nil {
		log.Printf("sentencetranslation: terminal-job run reconciliation failed: %v", err)
	}
	if _, err := ReconcileOrphanRuns(ctx, r.db); err != nil {
		log.Printf("sentencetranslation: orphan run reconciliation failed: %v", err)
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
			(payloadProbe.ContractVersion != annotator.SentenceTranslationContractVersion || payloadProbe.PromptVersion != annotator.SentenceTranslationContractVersion) {
			code = CodeContractChanged
		}
		return r.failJob(ctx, lease, code)
	}

	history := analysis.NewHistoryStore(r.db)

	articleID, err := library.ParseULID(payload.ArticleID)
	if err != nil {
		return r.failJob(ctx, lease, CodeContractChanged)
	}
	sentenceID, err := library.ParseULID(payload.SentenceID)
	if err != nil {
		return r.failJob(ctx, lease, CodeGenerationFailed)
	}

	// The service claimed the run before provider preflight and associated
	// its ID with the payload; the worker reuses it instead of opening a
	// second run. A terminal run means a newer request already superseded
	// this job, so the stale worker fails closed without publishing.
	runID, err := library.ParseULID(payload.RunID)
	if err != nil {
		return r.failJob(ctx, lease, CodeContractChanged)
	}
	run, err := history.GetRun(ctx, runID)
	if err != nil {
		log.Printf("sentencetranslation: claimed run %s missing for sentence %s, opening a replacement: %v", payload.RunID, payload.SentenceID, err)
		run, err = history.StartRun(ctx, analysis.RunStart{
			ArticleID: articleID, ArticleTitle: payload.Input.SourceText, JobID: lease.ID,
			AttemptCount: max(lease.AttemptCount, 1), ContentHash: payload.InputHash,
			ContractVersion: payload.ContractVersion, PromptVersion: promptVersionFor(payload),
			RequestedModel: payload.Binding.ModelID, RequestedEffort: requestedEffort(payload.Binding),
			ProviderID: payload.Binding.ProviderID, TotalParagraphs: 1,
			OperationType: OperationType, SubjectID: payload.SentenceID, SubjectLabel: payload.Input.SourceText,
		})
		if err != nil {
			log.Printf("sentencetranslation: replacement run start failed for sentence %s: %v", payload.SentenceID, err)
			return r.failJob(ctx, lease, CodeStorageFailed)
		}
		if err := r.store.SetLastRun(ctx, sentenceID, run.ID.String()); err != nil {
			log.Printf("sentencetranslation: last_run_id update failed for sentence %s: %v", payload.SentenceID, err)
			return r.failJob(ctx, lease, CodeStorageFailed)
		}
	} else if run.Status != "running" {
		return r.failJob(ctx, lease, CodeGenerationFailed)
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

	// Verify the snapshotted Translation binding against the registry before
	// any call: a removed, disabled, re-typed, or reconfigured provider
	// fails explicitly instead of silently substituting another model. Only
	// the Translation binding is consulted here.
	provider, ok := r.registry.Provider(payload.Binding.ProviderID)
	if !ok {
		return finalizeFailed(CodeProviderUnavailable, "preflight", "translation provider is not configured")
	}
	descriptor := provider.Descriptor()
	if !descriptor.Enabled {
		return finalizeFailed(CodeProviderUnavailable, "preflight", "translation provider is disabled")
	}
	if descriptor.Type != payload.Binding.ProviderType {
		return finalizeFailed(CodeProviderChanged, "preflight", "translation provider type changed")
	}
	if payload.Binding.ProviderConfigFingerprint != "" && descriptor.ConfigFingerprint != payload.Binding.ProviderConfigFingerprint {
		return finalizeFailed(CodeProviderChanged, "preflight", "translation provider configuration changed")
	}
	binding, err := annotator.ResolveBinding(payload.Binding)
	if err != nil {
		return finalizeFailed(CodeProviderChanged, "preflight", "translation binding could not be resolved")
	}

	// Captured prompt snapshots become the executed instructions; their bytes
	// must verify against the captured hashes.
	stagePrompts, promptErr := sentenceStagePrompts(payload)
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
	// identity; the sentence operation identity lives in the run row.
	attempt, err := history.StartStageAttempt(ctx, analysis.StageAttempt{
		RunID: run.ID.String(), BlockIndex: 0, StageID: string(pipeline.StageTranslation),
		ProviderID: payload.Binding.ProviderID, ProviderType: payload.Binding.ProviderType,
		ConfigFingerprint: payload.Binding.ProviderConfigFingerprint, ModelID: payload.Binding.ModelID,
		RequestedModel: payload.Binding.ModelID, ContractVersion: payload.ContractVersion,
		PromptVersion: promptVersionFor(payload), InputHash: payload.InputHash,
	})
	if err != nil {
		log.Printf("sentencetranslation: attempt start failed for sentence %s: %v", payload.SentenceID, err)
		return finalizeFailed(CodeStorageFailed, "storage", "stage attempt could not be recorded")
	}

	started := time.Now()
	result, generateErr := annotator.GenerateSentenceTranslation(runCtx, provider, binding, payload.Input, stagePrompts,
		annotator.WithTurnRecorder(r.stageTurnRecorder(attempt.ID)))
	if generateErr != nil {
		// A run whose lease was lost or whose job was canceled must never be
		// failed by this stale worker; the scheduler owns that transition.
		if cause := retainedLeaseError(); cause != nil {
			return fmt.Errorf("sentencetranslation: run aborted: %w", cause)
		}
		// Result-plus-error: retain every turn collected before the failure.
		finish := stageFailureFinish(result, generateErr)
		if finish.ErrorCode == "" {
			finish.ErrorCode = CodeGenerationFailed
		}
		if err := history.FinishStageAttempt(ctx, attempt.ID, finish); err != nil {
			log.Printf("sentencetranslation: attempt finish failed for sentence %s: %v", payload.SentenceID, err)
		}
		code := r.sentenceErrorCodeFor(generateErr)
		_ = history.FinishRun(ctx, run.ID, analysis.RunFinish{
			Status: "failed", ErrorCode: code, ErrorDetail: safeGenerationDetail(generateErr),
		})
		_ = history.SetRunPipelineFailure(ctx, run.ID.String(), string(pipeline.StageTranslation), payload.Binding.ProviderID)
		return r.failJob(ctx, lease, code)
	}
	if cause := retainedLeaseError(); cause != nil {
		return fmt.Errorf("sentencetranslation: run aborted: %w", cause)
	}

	translation := strings.TrimSpace(result.Document.TranslationEN)
	resultHash := sentenceResultHash(translation)
	provenance, err := json.Marshal(provenanceFor(payload, binding, result, started))
	if err != nil {
		return finalizeFailed(CodeGenerationFailed, "storage", "provenance could not be encoded")
	}

	durationMS := time.Since(started).Milliseconds()
	succeededFinish := stageFinishFromResult(result, durationMS)

	// One transaction verifies the live anchor, replaces the saved
	// translation, acknowledges the job with the live lease
	// compare-and-set, and completes the history records: history storage
	// failure rolls the whole publication back instead of silently claiming
	// complete diagnostics. A deleted or recreated anchor, or a subject that
	// no longer points at this job, aborts without storing anything: stale
	// jobs never overwrite a new anchor or the last successful text.
	publishErr := r.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := verifyPublishAnchorTx(ctx, tx, articleID, sentenceID, payload); err != nil {
			return err
		}
		if err := PublishTx(ctx, tx, sentenceID, lease.ID.String(), payload.Input.SourceHash, translation, resultHash, string(provenance)); err != nil {
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
		log.Printf("sentencetranslation: publish failed for sentence %s: %v", payload.SentenceID, publishErr)
		return fmt.Errorf("sentencetranslation: publish: %w", publishErr)
	}
	return nil
}

// verifyPublishAnchorTx re-verifies the live sentence anchor inside the
// publication transaction: the sentence must still belong to the article
// with the captured source hash and the article-owned target language. A
// changed or deleted anchor aborts publication; the stale result is never
// remapped to another sentence.
func verifyPublishAnchorTx(ctx context.Context, tx *sql.Tx, articleID, sentenceID library.ULID, payload JobPayload) error {
	var storedHash, articleTarget string
	err := tx.QueryRowContext(ctx, `
		SELECT s.source_hash, a.target_language
		FROM article_sentence s
		JOIN article_block b ON b.id = s.article_block_id
		JOIN article a ON a.id = b.article_id
		WHERE s.id = ? AND a.id = ?
	`, sentenceID.String(), articleID.String()).Scan(&storedHash, &articleTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("sentencetranslation: sentence anchor is gone; stale result not published")
	}
	if err != nil {
		return fmt.Errorf("sentencetranslation: verify anchor: %w", err)
	}
	if storedHash != payload.Input.SourceHash || articleTarget != payload.Input.TargetLanguage {
		return errors.New("sentencetranslation: sentence anchor changed; stale result not published")
	}
	return nil
}

// PublishTx stores an accepted translation together with the job
// acknowledgement inside the caller's transaction. The caller checks the
// job lease; this function additionally requires the subject to still
// point at the publishing job so a stale or canceled worker cannot publish
// over a newer generation or the last successful text.
func PublishTx(ctx context.Context, tx *sql.Tx, sentenceID library.ULID, jobID, sourceHash, translation, resultHash, provenanceJSON string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE sentence_translation
		SET source_hash = ?, translation_text = ?, result_hash = ?,
		    provenance_json = ?, updated_at = ?
		WHERE sentence_id = ? AND last_job_id = ?
	`, sourceHash, translation, resultHash, provenanceJSON, store.NowUTC(), sentenceID.String(), jobID)
	if err != nil {
		return fmt.Errorf("sentencetranslation: publish: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("sentencetranslation: subject no longer points at the publishing job")
	}
	return nil
}

// SetLastRun points the subject's preflight and generation evidence at one
// analysis run. The runner uses it only for the legacy replacement-run
// path; the service claims last_run_id before enqueue on the normal path.
func (s *Store) SetLastRun(ctx context.Context, sentenceID library.ULID, runID string) error {
	_, err := s.db.Exec(ctx, `UPDATE sentence_translation SET last_run_id = ? WHERE sentence_id = ?`, runID, sentenceID.String())
	return err
}

// sentenceStagePrompts verifies the payload's captured prompt snapshots and
// returns the executed instruction pair for the sentence operation.
func sentenceStagePrompts(payload JobPayload) (annotator.StagePrompts, error) {
	byType := make(map[string]pipeline.PromptSnapshot, 2)
	for index, snapshot := range payload.PromptSnapshots {
		if snapshot.Type != "sentence_translation" && snapshot.Type != "correction" {
			return annotator.StagePrompts{}, fmt.Errorf("unsupported sentence prompt type %q at %d", snapshot.Type, index)
		}
		if snapshot.EnvelopeVersion != pipeline.PromptCapturedEnvelopeVersion {
			return annotator.StagePrompts{}, fmt.Errorf("unsupported envelope version %q for %s", snapshot.EnvelopeVersion, snapshot.Type)
		}
		if prompts.ContentHashOf(snapshot.InstructionText) != snapshot.ContentHash {
			return annotator.StagePrompts{}, fmt.Errorf("%s prompt bytes do not match the captured content hash", snapshot.Type)
		}
		byType[snapshot.Type] = snapshot
	}
	generation, hasGeneration := byType["sentence_translation"]
	correction, hasCorrection := byType["correction"]
	if !hasGeneration || !hasCorrection {
		return annotator.StagePrompts{}, errors.New("sentence payload is missing its sentence_translation or correction prompt snapshot")
	}
	return annotator.CapturedStagePrompts(generation.InstructionText, correction.InstructionText), nil
}

// promptVersionFor derives the effective sentence prompt identity recorded
// on the attempt: operation, generation content hash, correction content
// hash, and the captured envelope version.
func promptVersionFor(payload JobPayload) string {
	var generation, correction string
	for _, snapshot := range payload.PromptSnapshots {
		if snapshot.Type == "sentence_translation" {
			generation = snapshot.ContentHash
		}
		if snapshot.Type == "correction" {
			correction = snapshot.ContentHash
		}
	}
	return pipeline.EffectivePromptVersion("sentence_translation", generation, correction, pipeline.PromptCapturedEnvelopeVersion)
}

// sentenceResultHash digests one accepted translation for change detection.
func sentenceResultHash(translation string) string {
	sum := sha256.Sum256([]byte(translation))
	return hex.EncodeToString(sum[:])
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
func stageFailureFinish(result *annotator.SentenceTranslationGenerateResult, err error) analysis.StageAttemptFinish {
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

func stageFinishFromResult(result *annotator.SentenceTranslationGenerateResult, durationMS int64) analysis.StageAttemptFinish {
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

func (r *Runner) sentenceErrorCodeFor(err error) string {
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

// stageTurnRecorder returns the prompt turn recorder for the sentence
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
		return fmt.Errorf("sentence job %s failed: %w", code, err)
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

// sentenceProvenance is the sanitized completion record. It never contains
// credentials, endpoints, prompts, or raw responses.
type sentenceProvenance struct {
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

func provenanceFor(payload JobPayload, binding annotator.ResolvedBinding, result *annotator.SentenceTranslationGenerateResult, started time.Time) sentenceProvenance {
	return sentenceProvenance{
		ProviderID: binding.ProviderID, ProviderType: binding.ProviderType,
		RequestedModel: payload.Binding.ModelID, ReportedModel: result.Attempt.ReportedModel,
		OptionsHash: binding.OptionsHash, ConfigFingerprint: binding.ConfigFingerprint,
		ContractVersion: annotator.SentenceTranslationContractVersion, PromptVersion: annotator.SentenceTranslationContractVersion,
		RequestID: result.Attempt.RequestID, FinishReason: result.Attempt.FinishReason,
		UsageJSON: result.Attempt.UsageJSON, TimingJSON: result.Attempt.TimingJSON,
		Corrections: len(result.Attempt.Turns) - 1, DurationMS: time.Since(started).Milliseconds(),
	}
}

package analysis

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

// providerRegistry is the narrow registry seam used by the pipeline runner;
// the production annotator.Registry satisfies it.
type providerRegistry interface {
	Provider(id string) (annotator.Provider, bool)
}

// PipelineRunner executes pipeline-format analysis jobs: every paragraph runs
// linguistic analysis then translation through provider sessions, records
// stage attempts and turns, reads/writes the exact stage cache, and publishes
// each merged paragraph through the unchanged progressive transaction.
type PipelineRunner struct {
	jobs     *jobs.Store
	reader   *reader.Store
	history  *HistoryStore
	registry providerRegistry
	owner    string
	// heartbeatInterval is how often an in-flight run renews its job lease
	// while provider calls are still executing. Production uses twenty
	// seconds; tests shorten it.
	heartbeatInterval time.Duration
}

func NewPipelineRunner(db *store.DB, registry providerRegistry) *PipelineRunner {
	return &PipelineRunner{
		jobs:   jobs.NewStore(db, speech.NewStore(db).ReconcileTerminalJobTx),
		reader: reader.NewStore(db), history: NewHistoryStore(db),
		registry: registry, owner: "server-pipeline",
		heartbeatInterval: 20 * time.Second,
	}
}

func (r *PipelineRunner) Run(ctx context.Context) {
	for {
		if err := r.RunOnce(ctx); err != nil && !errors.Is(err, jobs.ErrNoWork) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("analysis pipeline: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *PipelineRunner) RunOnce(ctx context.Context) error {
	if r == nil || r.jobs == nil || r.reader == nil || r.registry == nil {
		return errors.New("analysis: nil pipeline runner")
	}
	if _, err := r.jobs.RecoverExpired(ctx); err != nil {
		return err
	}
	lease, err := r.jobs.Claim(ctx, jobs.TargetServer, r.owner)
	if err != nil {
		return err
	}
	return r.process(ctx, lease)
}

func (r *PipelineRunner) process(ctx context.Context, lease *jobs.Lease) error {
	payload, err := pipeline.DecodeJobPayload([]byte(lease.PayloadJSON))
	if err != nil {
		return r.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "v1.analysis_invalid_job", false)
	}
	id, err := library.ParseULID(payload.ArticleID)
	if err != nil || id.IsZero() || payload.ContentHash != lease.InputHash {
		return r.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "v1.analysis_invalid_job", false)
	}
	// Per-run cancelable context: the heartbeat goroutine cancels it the
	// moment the job is canceled or the lease is lost so an in-flight
	// provider call aborts immediately instead of occupying the sole pipeline
	// worker until its request timeout. The retained lease error lets the
	// main flow distinguish an aborted run from a genuine provider failure.
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
	// checkOwnership verifies this worker still owns the job before any
	// success or failure artifact write: first the retained heartbeat error
	// (cancellation or lease loss observed while a provider call was
	// blocked), then the durable lease itself.
	checkOwnership := func() error {
		if err := retainedLeaseError(); err != nil {
			return err
		}
		return r.verifyLease(ctx, lease)
	}

	prepared, err := r.reader.PrepareAnalysis(ctx, id)
	if err != nil {
		return r.preflightFail(ctx, lease, id, "v1.analysis_prepare_failed")
	}
	if prepared.ContentHash != lease.InputHash || prepared.ContentHash != payload.ContentHash {
		return r.preflightFail(ctx, lease, id, "v1.analysis_source_changed")
	}
	// Verify every configured provider binding before touching article state:
	// a changed connection fingerprint fails closed.
	providerByStage := make(map[pipeline.StageID]annotator.Provider, len(payload.Profile.Bindings))
	for _, binding := range payload.Profile.Bindings {
		provider, ok := r.registry.Provider(binding.ProviderID)
		if !ok {
			return r.preflightFail(ctx, lease, id, "v1.analysis_provider_unavailable")
		}
		descriptor := provider.Descriptor()
		if !descriptor.Enabled || descriptor.Type != binding.ProviderType {
			return r.preflightFail(ctx, lease, id, "v1.analysis_provider_changed")
		}
		if descriptor.ConfigFingerprint != binding.ProviderConfigFingerprint {
			return r.preflightFail(ctx, lease, id, "v1.analysis_provider_changed")
		}
		providerByStage[binding.StageID] = provider
	}
	if providerByStage[pipeline.StageLinguisticAnalysis] == nil || providerByStage[pipeline.StageTranslation] == nil {
		return r.preflightFail(ctx, lease, id, "v1.analysis_invalid_job")
	}

	if err := r.reader.MarkAnalysisProcessing(ctx, id, lease.ID); err != nil {
		return r.failJob(ctx, lease, "v1.analysis_processing_conflict")
	}
	if err := r.reader.ResetBlocksForJob(ctx, id, lease.ID); err != nil {
		return r.failJob(ctx, lease, "v1.analysis_processing_conflict")
	}
	run, err := r.history.StartRun(ctx, RunStart{
		ArticleID: id, ArticleTitle: prepared.Title, JobID: lease.ID,
		AttemptCount: lease.AttemptCount, ContentHash: prepared.ContentHash,
		ContractVersion: semantics.AnalysisContractVersion,
		PromptVersion:   pipeline.PipelineVersion,
		RequestedModel:  profileBinding(payload, pipeline.StageLinguisticAnalysis).ModelID,
		RequestedEffort: codexEffort(payload, pipeline.StageLinguisticAnalysis),
		ProviderID:      profileBinding(payload, pipeline.StageLinguisticAnalysis).ProviderID,
		TotalParagraphs: len(prepared.Blocks),
	})
	if err != nil {
		return r.failJob(ctx, lease, "v1.analysis_history_failed")
	}
	profileJSON, _ := json.Marshal(payload.Profile)
	if err := r.history.SetRunPipelineProvenance(ctx, run.ID.String(), string(profileJSON),
		pipeline.PipelineVersion, payload.Profile.ID, payload.Profile.Name, payload.ProfileSnapshotHash); err != nil {
		return r.failJob(ctx, lease, "v1.analysis_history_failed")
	}
	runStarted := time.Now()
	completedParagraphs := 0
	var progress atomic.Int64
	progress.Store(0)
	failedStage := ""
	failedProvider := ""
	finishRun := func(status, code, detail string, failedBlock int) error {
		return r.history.FinishRun(ctx, run.ID, RunFinish{
			Status: status, DurationMS: time.Since(runStarted).Milliseconds(),
			CompletedParags: completedParagraphs, FailedBlockIndex: failedBlock,
			ErrorCode: code, ErrorDetail: detail,
		})
	}
	leaseLostPath := func(cause error, block int) error {
		// The run and job rows belong to this worker's attempt and are safe to
		// terminate; article/block/cache/turn state is never touched here.
		_ = finishRun("failed", "v1.analysis_lease_lost", cause.Error(), block)
		return r.failJob(ctx, lease, "v1.analysis_lease_lost")
	}
	failRun := func(code string, cause error, block int, stageID pipeline.StageID, providerID string) error {
		_ = r.history.UpdateProgress(ctx, run.ID, completedParagraphs, block)
		if block >= 0 {
			_ = r.reader.FailBlockForJob(ctx, id, block, lease.ID, code)
		}
		if stageID != "" {
			failedStage = string(stageID)
			failedProvider = providerID
			_ = r.history.SetRunPipelineFailure(ctx, run.ID.String(), failedStage, failedProvider)
		}
		_ = finishRun("failed", code, cause.Error(), block)
		if err := r.reader.MarkAnalysisFailedForJob(ctx, id, lease.ID, code); err != nil {
			var typed *reader.Error
			if !errors.As(err, &typed) || typed.Kind != reader.KindConflict {
				log.Printf("analysis pipeline: mark article %s failed under job %s: %v", id, lease.ID, err)
			}
		}
		return r.failJob(ctx, lease, code)
	}

	stopHeartbeat := r.startHeartbeat(runCtx, lease, len(prepared.Blocks), &progress, retainLeaseError)
	defer stopHeartbeat()

	var chunks []semantics.ChunkResult
	prior := make([]semantics.NewSense, 0)
	for blockIndex := range prepared.Blocks {
		if err := checkOwnership(); err != nil {
			return leaseLostPath(err, blockIndex)
		}
		if err := r.reader.MarkBlockProcessing(ctx, id, blockIndex, lease.ID); err != nil {
			return failRun("v1.analysis_processing_conflict", err, blockIndex, "", "")
		}
		chunk, err := semantics.PrepareChunk(prepared, blockIndex, prior)
		if err != nil {
			return failRun("v1.analysis_prepare_failed", err, blockIndex, pipeline.StageLinguisticAnalysis, "")
		}
		inputHash, err := ChunkInputHash(chunk)
		if err != nil {
			return failRun("v1.analysis_prepare_failed", err, blockIndex, "", "")
		}
		translationInputHash, err := TranslationChunkInputHash(chunk)
		if err != nil {
			return failRun("v1.analysis_prepare_failed", err, blockIndex, "", "")
		}
		linguisticBinding := profileBinding(payload, pipeline.StageLinguisticAnalysis)
		translationBinding := profileBinding(payload, pipeline.StageTranslation)

		// --- Linguistic stage ---
		linguisticSpec := StageCacheSpec{
			StageID: pipeline.StageLinguisticAnalysis, InputHash: inputHash,
			ContractVersion: linguisticBinding.ContractVersion, PromptVersion: linguisticBinding.PromptVersion,
			ProviderID: linguisticBinding.ProviderID, ProviderType: linguisticBinding.ProviderType,
			ConfigFingerprint: linguisticBinding.ProviderConfigFingerprint,
			ModelID:           linguisticBinding.ModelID, OptionsHash: linguisticBinding.OptionsHash,
		}
		attempt, _, err := r.startAttempt(ctx, run.ID.String(), blockIndex, linguisticBinding, linguisticSpec)
		if err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}
		linguistic, artifactHash, outcome, stageErr := r.runLinguistic(runCtx, lease, chunk, linguisticBinding,
			providerByStage[pipeline.StageLinguisticAnalysis], linguisticSpec, payload.Fresh, run.ID.String())
		if stageErr != nil {
			// The heartbeat may have canceled the run while the provider call
			// was blocked; never write turns or failure state for a run whose
			// lease was reclaimed or whose job was canceled.
			if err := checkOwnership(); err != nil {
				return leaseLostPath(err, blockIndex)
			}
			if err := r.recordTurns(ctx, attempt.ID, outcome.result.Turns); err != nil {
				return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
			}
			return r.finishStageAndFail(ctx, attempt, outcome, stageErr, failRun, blockIndex,
				pipeline.StageLinguisticAnalysis, linguisticBinding.ProviderID)
		}
		if err := checkOwnership(); err != nil {
			return leaseLostPath(err, blockIndex)
		}
		if err := r.recordTurns(ctx, attempt.ID, outcome.result.Turns); err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}
		if err := r.finishStage(ctx, attempt, outcome, stageFinishFromResult("succeeded", outcome.result)); err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}

		// --- Translation stage ---
		translationSpec := StageCacheSpec{
			StageID: pipeline.StageTranslation, InputHash: translationInputHash, UpstreamHash: artifactHash,
			ContractVersion: translationBinding.ContractVersion, PromptVersion: translationBinding.PromptVersion,
			ProviderID: translationBinding.ProviderID, ProviderType: translationBinding.ProviderType,
			ConfigFingerprint: translationBinding.ProviderConfigFingerprint,
			ModelID:           translationBinding.ModelID, OptionsHash: translationBinding.OptionsHash,
		}
		attempt, _, err = r.startAttempt(ctx, run.ID.String(), blockIndex, translationBinding, translationSpec)
		if err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}
		output, outcome, stageErr := r.runTranslation(runCtx, lease, chunk, linguistic, translationBinding,
			providerByStage[pipeline.StageTranslation], translationSpec, payload.Fresh, run.ID.String())
		if stageErr != nil {
			if err := checkOwnership(); err != nil {
				return leaseLostPath(err, blockIndex)
			}
			if err := r.recordTurns(ctx, attempt.ID, outcome.result.Turns); err != nil {
				return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
			}
			return r.finishStageAndFail(ctx, attempt, outcome, stageErr, failRun, blockIndex,
				pipeline.StageTranslation, translationBinding.ProviderID)
		}
		if err := checkOwnership(); err != nil {
			return leaseLostPath(err, blockIndex)
		}
		if err := r.recordTurns(ctx, attempt.ID, outcome.result.Turns); err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}
		if err := r.finishStage(ctx, attempt, outcome, stageFinishFromResult("succeeded", outcome.result)); err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}

		// --- Namespace and publish ---
		namespaced, err := semantics.NamespaceChunkResponse(blockIndex, output.Merged, prior)
		if err != nil {
			return failRun("v1.analysis_stage_failed", err, blockIndex, pipeline.StageTranslation, translationBinding.ProviderID)
		}
		namespacedValidated, err := semantics.ValidateChunkResponse(chunk, namespaced)
		if err != nil {
			return failRun("v1.analysis_stage_failed", err, blockIndex, pipeline.StageTranslation, translationBinding.ProviderID)
		}
		effort := codexEffort(payload, pipeline.StageLinguisticAnalysis)
		if err := checkOwnership(); err != nil {
			return leaseLostPath(err, blockIndex)
		}
		if err := r.reader.PersistAnalysisChunkWithProvenance(ctx, id, blockIndex, lease.ID, run.ID, prepared,
			namespacedValidated, linguisticBinding.ProviderID, linguisticBinding.ModelID, effort,
			&reader.ChunkPipelineProvenance{
				ProfileID: payload.Profile.ID, ProfileName: payload.Profile.Name,
				SnapshotHash:         payload.ProfileSnapshotHash,
				LinguisticProviderID: linguisticBinding.ProviderID, LinguisticModel: linguisticBinding.ModelID,
				TranslationProviderID: translationBinding.ProviderID, TranslationModel: translationBinding.ModelID,
			}, chunk.PriorValidatedSenses); err != nil {
			return failRun("v1.analysis_persist_failed", err, blockIndex, pipeline.StageTranslation, translationBinding.ProviderID)
		}
		chunks = append(chunks, semantics.ChunkResult{Chunk: chunk, Response: output.Merged})
		prior = append(prior, namespaced.NewSenses...)
		completedParagraphs = blockIndex + 1
		progress.Store(int64(completedParagraphs))
		if err := r.history.UpdateProgress(ctx, run.ID, completedParagraphs, -1); err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, "", "")
		}
		percent := completedParagraphs * 100 / len(prepared.Blocks)
		if _, err := r.jobs.Heartbeat(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, percent); err != nil {
			_ = finishRun("failed", "v1.analysis_lease_lost", err.Error(), blockIndex)
			return r.failJob(ctx, lease, "v1.analysis_lease_lost")
		}
	}
	if _, err := semantics.MergeChunks(prepared, chunks); err != nil {
		return failRun("v1.analysis_stage_failed", err, completedParagraphs, pipeline.StageTranslation, "")
	}
	if err := checkOwnership(); err != nil {
		return leaseLostPath(err, -1)
	}
	linguistic := profileBinding(payload, pipeline.StageLinguisticAnalysis)
	if err := r.reader.MarkAnalysisReady(ctx, id, lease.ID, linguistic.ModelID, codexEffort(payload, pipeline.StageLinguisticAnalysis)); err != nil {
		return failRun("v1.analysis_persist_failed", err, -1, "", "")
	}
	if err := finishRun("succeeded", "", "", -1); err != nil {
		return r.failJob(ctx, lease, "v1.analysis_history_failed")
	}
	_ = failedStage
	_ = failedProvider
	return r.jobs.Complete(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken)
}

func profileBinding(payload pipeline.JobPayload, stage pipeline.StageID) pipeline.BindingSnapshot {
	for _, binding := range payload.Profile.Bindings {
		if binding.StageID == stage {
			return binding
		}
	}
	return pipeline.BindingSnapshot{}
}

func codexEffort(payload pipeline.JobPayload, stage pipeline.StageID) string {
	binding := profileBinding(payload, stage)
	if binding.ProviderType != annotator.ProviderTypeCodexAppServer {
		return ""
	}
	var options codexEffortOptions
	_ = json.Unmarshal(binding.Options, &options)
	return options.ReasoningEffort
}

type codexEffortOptions struct {
	ReasoningEffort string `json:"reasoning_effort"`
}

// startHeartbeat renews the job lease on a fixed interval for as long as the
// run is executing, so long provider calls (which can run far beyond the
// lease duration) keep the job live and never risk a second worker claiming
// the same job mid-turn. When a renewal observes owner cancellation or lease
// loss, onLeaseLost is invoked with the error: it cancels the per-run context
// (aborting any in-flight provider call) and retains the error so the main
// flow never writes state it no longer owns. The returned stop function is
// idempotent.
func (r *PipelineRunner) startHeartbeat(runCtx context.Context, lease *jobs.Lease, totalBlocks int, completed *atomic.Int64, onLeaseLost func(error)) func() {
	stop := make(chan struct{})
	var once sync.Once
	stopFn := func() {
		once.Do(func() { close(stop) })
	}
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
				percent := 0
				if totalBlocks > 0 && completed != nil {
					percent = int(completed.Load()) * 100 / totalBlocks
				}
				heartbeat, err := r.jobs.Heartbeat(runCtx, lease.ID, lease.AttemptCount, lease.LeaseToken, percent)
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

func (r *PipelineRunner) verifyLease(ctx context.Context, lease *jobs.Lease) error {
	job, err := r.jobs.VerifyLease(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, r.owner)
	if err != nil {
		return err
	}
	if job.State != jobs.StateLeased && job.State != jobs.StateRunning {
		return jobs.ErrLeaseLost
	}
	return nil
}

func (r *PipelineRunner) failJob(ctx context.Context, lease *jobs.Lease, code string) error {
	retry := lease.AttemptCount < lease.MaxAttempts
	if err := r.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, code, retry); err != nil {
		return fmt.Errorf("analysis pipeline job failed: %w", err)
	}
	if !retry {
		return fmt.Errorf("analysis pipeline job failed: %s", code)
	}
	return errors.New(code)
}

// preflightFail reports a job failure detected before the article entered
// processing (provider configuration changes, missing providers, source or
// prepare mismatches). While automatic retries remain, the article stays
// queued for the next attempt. When the final attempt fails, the owning
// article is moved to the failed state with the same stable code and its
// first unresolved paragraph is marked failed, so a terminally failed job can
// never leave the article stranded in queued (startup recovery only repairs
// processing articles).
func (r *PipelineRunner) preflightFail(ctx context.Context, lease *jobs.Lease, id library.ULID, code string) error {
	retry := lease.AttemptCount < lease.MaxAttempts
	jobErr := r.failJob(ctx, lease, code)
	if retry {
		return jobErr
	}
	// Transition the article only when this worker's failure was durably
	// acknowledged on a job it still owned (state failed). If the job was
	// already terminal for another reason (canceled or superseded by a newer
	// run) the article-level guard on analysis_job_id makes the transition a
	// safe no-op.
	var state string
	if err := r.history.db.QueryRow(ctx, `SELECT state FROM job WHERE id = ?`, lease.ID.String()).Scan(&state); err != nil || state != jobs.StateFailed {
		return jobErr
	}
	if err := r.reader.MarkAnalysisFailedForJob(ctx, id, lease.ID, code); err != nil {
		log.Printf("analysis pipeline: preflight failure %s could not mark article %s failed: %v", code, id, err)
	} else {
		var firstPending sql.NullInt64
		if err := r.history.db.QueryRow(ctx, `SELECT MIN(block_index) FROM article_block WHERE article_id = ? AND analysis_status = 'pending'`, id.String()).Scan(&firstPending); err == nil && firstPending.Valid {
			if err := r.reader.FailBlockForJob(ctx, id, int(firstPending.Int64), lease.ID, code); err != nil {
				log.Printf("analysis pipeline: preflight failure %s could not mark the first unresolved block of article %s failed: %v", code, id, err)
			}
		}
	}
	return jobErr
}

// recordTurns persists executor turn records for one stage attempt.
func (r *PipelineRunner) recordTurns(ctx context.Context, attemptID string, turns []annotator.StageTurnRecord) error {
	for _, record := range turns {
		turn := StageTurn{
			AttemptID: attemptID, TurnIndex: record.TurnIndex, TurnKind: record.TurnKind,
			Prompt: record.Prompt, OutputSchema: record.OutputSchema,
			CompletedResponse: record.CompletedResponse, ResponseHash: record.ResponseHash,
			ValidationError: record.ValidationError, ProviderError: record.ProviderError,
			CompletionMetadata: record.CompletionMetadata,
			StartedAt:          record.StartedAt, CompletedAt: record.CompletedAt,
			DurationMS: record.DurationMS, Status: record.Status,
		}
		if err := r.history.AppendStageTurn(ctx, turn); err != nil {
			return err
		}
	}
	return nil
}

// stageOutcome carries everything the process loop must persist after one
// stage execution or cache hit: the cache disposition, the source cache row
// for hits, and the accumulated executor result (turns plus the provider
// completion provenance) for provider runs.
type stageOutcome struct {
	disposition   string
	sourceCacheID string
	result        annotator.StageAttemptResult
}

// runLinguistic executes or cache-loads one linguistic stage. On a cache hit
// it returns the validated artifact with the cache row identity; otherwise it
// opens the configured session, executes every turn, and stores the locally
// validated artifact. Lease ownership is re-verified around provider calls and
// before the cache write; a canceled runCtx aborts the provider session.
func (r *PipelineRunner) runLinguistic(ctx context.Context, lease *jobs.Lease, chunk semantics.PreparedChunk,
	binding pipeline.BindingSnapshot, provider annotator.Provider, spec StageCacheSpec, fresh bool, runID string) (*semantics.ValidatedLinguistic, string, stageOutcome, error) {
	outcome := stageOutcome{disposition: "miss"}
	if !fresh {
		if hit, err := r.history.ReadStageCache(ctx, spec); err == nil && hit != nil {
			artifact, err := semantics.DecodeLinguisticArtifact([]byte(hit.ArtifactJSON))
			if err == nil {
				validated, err := semantics.ValidateLinguistic(chunk, artifact)
				if err == nil {
					hash, _ := ArtifactHashOf(validated)
					if hash == hit.ArtifactHash {
						outcome.disposition = "hit"
						outcome.sourceCacheID = hit.CacheID
						return validated, hash, outcome, nil
					}
				}
			}
		}
	}
	if fresh {
		outcome.disposition = "bypassed"
	}
	if err := r.verifyLease(ctx, lease); err != nil {
		return nil, "", outcome, err
	}
	resolved, err := annotator.ResolveBinding(binding)
	if err != nil {
		return nil, "", outcome, err
	}
	validated, result, err := annotator.ExecuteLinguisticStage(ctx, provider, resolved, chunk)
	outcome.result = result
	if err != nil {
		// Turn records accumulated before the failure (provider errors and
		// rejected corrective outputs) are returned with the outcome; the
		// caller persists them only after re-verifying lease ownership.
		return nil, "", outcome, err
	}
	if err := r.verifyLease(ctx, lease); err != nil {
		return nil, "", outcome, err
	}
	rawArtifact := rawLinguisticArtifact(validated)
	hash, err := ArtifactHashOf(rawArtifact)
	if err != nil {
		return nil, "", outcome, err
	}
	artifactJSON, err := json.Marshal(rawArtifact)
	if err != nil {
		return nil, "", outcome, err
	}
	if !fresh {
		if err := r.history.SaveStageCache(ctx, spec, string(artifactJSON), hash, runID); err != nil {
			return nil, "", outcome, err
		}
	}
	return validated, hash, outcome, nil
}

// rawLinguisticArtifact rebuilds the provider-shaped artifact (no
// server-assigned construction wrapper) from the validated form so cached
// JSON round-trips through the strict stage decoder.
func rawLinguisticArtifact(validated *semantics.ValidatedLinguistic) semantics.LinguisticArtifact {
	artifact := semantics.LinguisticArtifact{
		Version: validated.Version, Tokens: validated.Tokens, NewSenses: validated.NewSenses,
	}
	for _, construction := range validated.Constructions {
		artifact.Constructions = append(artifact.Constructions, construction.Construction)
	}
	return artifact
}

// runTranslation executes or cache-loads one translation stage under the same
// ownership rules as runLinguistic, including the upstream linguistic artifact
// hash in the cache identity.
func (r *PipelineRunner) runTranslation(ctx context.Context, lease *jobs.Lease, chunk semantics.PreparedChunk,
	linguistic *semantics.ValidatedLinguistic, binding pipeline.BindingSnapshot, provider annotator.Provider,
	spec StageCacheSpec, fresh bool, runID string) (*annotator.TranslationStageOutput, stageOutcome, error) {
	outcome := stageOutcome{disposition: "miss"}
	if !fresh {
		if hit, err := r.history.ReadStageCache(ctx, spec); err == nil && hit != nil {
			artifact, err := semantics.DecodeTranslationArtifact([]byte(hit.ArtifactJSON))
			if err == nil {
				if semantics.ValidateTranslation(chunk, linguistic, artifact) == nil {
					hash, _ := ArtifactHashOf(artifact)
					if hash == hit.ArtifactHash {
						merged, err := semantics.MergeLinguisticTranslation(linguistic, artifact)
						if err == nil {
							outcome.disposition = "hit"
							outcome.sourceCacheID = hit.CacheID
							return &annotator.TranslationStageOutput{Artifact: artifact, Merged: merged}, outcome, nil
						}
					}
				}
			}
		}
	}
	if fresh {
		outcome.disposition = "bypassed"
	}
	if err := r.verifyLease(ctx, lease); err != nil {
		return nil, outcome, err
	}
	resolved, err := annotator.ResolveBinding(binding)
	if err != nil {
		return nil, outcome, err
	}
	output, result, err := annotator.ExecuteTranslationStage(ctx, provider, resolved, chunk, linguistic)
	outcome.result = result
	if err != nil {
		// Turn records accumulated before the failure are returned with the
		// outcome; the caller persists them only after re-verifying lease
		// ownership.
		return nil, outcome, err
	}
	if err := r.verifyLease(ctx, lease); err != nil {
		return nil, outcome, err
	}
	hash, err := ArtifactHashOf(output.Artifact)
	if err != nil {
		return nil, outcome, err
	}
	artifactJSON, err := json.Marshal(output.Artifact)
	if err != nil {
		return nil, outcome, err
	}
	if !fresh {
		if err := r.history.SaveStageCache(ctx, spec, string(artifactJSON), hash, runID); err != nil {
			return nil, outcome, err
		}
	}
	return output, outcome, nil
}

func (r *PipelineRunner) startAttempt(ctx context.Context, runID string, blockIndex int,
	binding pipeline.BindingSnapshot, spec StageCacheSpec) (StageAttempt, string, error) {
	disposition := "miss"
	optionsJSON, err := json.Marshal(binding.Options)
	if err != nil {
		return StageAttempt{}, "", err
	}
	attempt, err := r.history.StartStageAttempt(ctx, StageAttempt{
		RunID: runID, BlockIndex: blockIndex, StageID: string(spec.StageID),
		ProviderID: binding.ProviderID, ProviderType: binding.ProviderType,
		ConfigFingerprint: binding.ProviderConfigFingerprint, ModelID: binding.ModelID,
		OptionsJSON: string(optionsJSON), OptionsHash: binding.OptionsHash,
		RequestedModel:  binding.ModelID,
		ContractVersion: spec.ContractVersion, PromptVersion: spec.PromptVersion,
		InputHash: spec.InputHash, UpstreamHash: spec.UpstreamHash,
		CacheDisposition: disposition,
	})
	if err != nil {
		return StageAttempt{}, "", err
	}
	return attempt, disposition, nil
}

// finishStage records the terminal outcome of one stage attempt: the cache
// disposition and source cache row, the completion provenance when the stage
// ran against the provider, and the attempt duration. finish carries the
// status and any failure detail; a zero DurationMS is computed from the
// attempt start time.
func (r *PipelineRunner) finishStage(ctx context.Context, attempt StageAttempt, outcome stageOutcome, finish StageAttemptFinish) error {
	if attempt.ID == "" {
		return nil
	}
	if finish.CompletedAt == "" {
		finish.CompletedAt = store.NowUTC()
	}
	if finish.DurationMS <= 0 {
		finish.DurationMS = attemptDurationMS(attempt.StartedAt, finish.CompletedAt)
	}
	if _, err := r.history.db.Exec(ctx, `UPDATE analysis_stage_attempt SET cache_disposition = ?, source_cache_id = ? WHERE id = ?`,
		outcome.disposition, outcome.sourceCacheID, attempt.ID); err != nil {
		return err
	}
	return r.history.FinishStageAttempt(ctx, attempt.ID, finish)
}

// stageFinishFromResult builds the terminal attempt record from the executor
// result: reported model, request id, finish reason, and bounded usage/timing/
// metadata diagnostics.
func stageFinishFromResult(status string, result annotator.StageAttemptResult) StageAttemptFinish {
	return StageAttemptFinish{
		Status: status, ReportedModel: result.ReportedModel, RequestID: result.RequestID,
		FinishReason: result.FinishReason, UsageJSON: result.UsageJSON, TimingJSON: result.TimingJSON,
		MetadataJSON: result.MetadataJSON, StderrExcerpt: result.StderrExcerpt,
	}
}

// attemptDurationMS measures the wall time between the attempt start and its
// completion in milliseconds; zero when either timestamp is unparsable.
func attemptDurationMS(startedAt, completedAt string) int64 {
	layouts := []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano}
	var started, completed time.Time
	for _, layout := range layouts {
		if started.IsZero() {
			if parsed, err := time.Parse(layout, startedAt); err == nil {
				started = parsed
			}
		}
		if completed.IsZero() {
			if parsed, err := time.Parse(layout, completedAt); err == nil {
				completed = parsed
			}
		}
	}
	if started.IsZero() || completed.IsZero() || !completed.After(started) {
		return 0
	}
	return completed.Sub(started).Milliseconds()
}

// finishStageAndFail marks one stage attempt failed with the stage error and
// then fails the run/article through failRun. Callers must have verified lease
// ownership first; this writes only rows owned by this worker's run.
func (r *PipelineRunner) finishStageAndFail(ctx context.Context, attempt StageAttempt, outcome stageOutcome,
	stageErr error, failRun func(string, error, int, pipeline.StageID, string) error,
	blockIndex int, stageID pipeline.StageID, providerID string) error {
	if attempt.ID != "" {
		finish := stageFinishFromResult("failed", outcome.result)
		finish.ErrorCode = stageErrorCode(stageErr)
		finish.ErrorDetail = stageErr.Error()
		if err := r.finishStage(ctx, attempt, outcome, finish); err != nil {
			return failRun("v1.analysis_history_failed", err, blockIndex, stageID, providerID)
		}
	}
	return failRun(stageErrorArticleCode(stageErr), stageErr, blockIndex, stageID, providerID)
}

func stageErrorCode(err error) string {
	return annotator.StageErrorCode(err)
}

func stageErrorArticleCode(err error) string {
	// StageError.Phase is unqualified ("provider", "stage_validation",
	// "final_validation"); StageErrorPhase stringifies stage + phase (for
	// example "translation provider") and must not be compared here.
	var stageErr *annotator.StageError
	if errors.As(err, &stageErr) && stageErr.Phase == "provider" {
		return "v1.analysis_provider_unavailable"
	}
	return "v1.analysis_stage_failed"
}

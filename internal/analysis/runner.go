// Package analysis runs durable semantic-reader jobs on the server side.
package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/media"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

type Runner struct {
	jobs     *jobs.Store
	reader   *reader.Store
	provider annotator.SemanticAnnotator
	history  *HistoryStore
	owner    string
}

func NewRunner(db *store.DB, provider annotator.SemanticAnnotator) *Runner {
	return NewRunnerWithMedia(db, provider, nil)
}

// NewRunnerWithMedia enables post-commit cleanup of superseded article audio
// while retaining the durable analysis runner's database-only test seam.
func NewRunnerWithMedia(db *store.DB, provider annotator.SemanticAnnotator, mediaStore *media.Store) *Runner {
	speechStore := speech.NewStore(db)
	return &Runner{
		jobs:   jobs.NewStore(db, speechStore.ReconcileTerminalJobTx),
		reader: reader.NewStoreWithMedia(db, mediaStore), provider: provider,
		history: NewHistoryStore(db), owner: "server-analysis",
	}
}

// Run polls SQLite until ctx is canceled. A short idle wait keeps local tests
// responsive while avoiding an in-memory queue or a busy loop.
func (r *Runner) Run(ctx context.Context) {
	for {
		if err := r.RunOnce(ctx); err != nil && !errors.Is(err, jobs.ErrNoWork) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logAnalysisFailure(r.owner, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// RunOnce leases and processes at most one server analysis job.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil || r.jobs == nil || r.reader == nil {
		return errors.New("analysis: nil runner")
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

func (r *Runner) process(ctx context.Context, lease *jobs.Lease) error {
	payload, err := decodeAnalysisPayload(lease.PayloadJSON)
	if err != nil {
		return r.fail(ctx, lease, "v1.analysis_invalid_job", err)
	}
	id, err := library.ParseULID(payload.ArticleID)
	if err != nil || id.IsZero() || payload.Contract != semantics.AnalysisContractVersion || payload.ContentHash != lease.InputHash {
		return r.fail(ctx, lease, "v1.analysis_invalid_job", errors.New("analysis job payload does not match its lease"))
	}
	prepared, err := r.reader.PrepareAnalysis(ctx, id)
	if err != nil {
		return r.fail(ctx, lease, "v1.analysis_prepare_failed", err)
	}
	if prepared.ContentHash != lease.InputHash {
		return r.fail(ctx, lease, "v1.analysis_source_changed", errors.New("article content hash changed"))
	}
	if err := r.reader.MarkAnalysisProcessing(ctx, id); err != nil {
		article, getErr := r.reader.GetArticle(ctx, id)
		if getErr == nil && article.AnalysisStatus == reader.AnalysisReady && article.ContentHash == prepared.ContentHash {
			return r.jobs.Complete(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken)
		}
		return r.fail(ctx, lease, "v1.analysis_processing_conflict", err)
	}

	providerID := semantics.ProviderID
	runStarted := time.Now()
	run, err := r.history.StartRun(ctx, RunStart{
		ArticleID: id, ArticleTitle: prepared.Title, JobID: lease.ID,
		AttemptCount: lease.AttemptCount, ContentHash: prepared.ContentHash,
		ContractVersion: semantics.AnalysisContractVersion, PromptVersion: semantics.PromptVersion,
		RequestedModel: payload.Model, RequestedEffort: payload.Effort,
		ProviderID: providerID, CodexCLIVersion: providerCLIVersion(ctx, r.provider),
		TotalParagraphs: len(prepared.Blocks),
	})
	if err != nil {
		return r.failAnalysis(ctx, lease, id, "v1.analysis_history_failed", err)
	}
	completedParagraphs := 0
	reportedModel := ""
	finishRun := func(status, code, detail, stderr string, failedBlock int) error {
		return r.history.FinishRun(ctx, run.ID, RunFinish{
			Status: status, ReportedModel: reportedModel, DurationMS: time.Since(runStarted).Milliseconds(),
			CompletedParags: completedParagraphs, FailedBlockIndex: failedBlock,
			ErrorCode: code, ErrorDetail: detail, StderrExcerpt: diagnosticExcerpt(stderr),
		})
	}
	failRun := func(code string, cause error, failedBlock int, stderr string) error {
		_ = r.history.UpdateProgress(ctx, run.ID, completedParagraphs, failedBlock)
		historyErr := finishRun("failed", code, cause.Error(), stderr, failedBlock)
		jobErr := r.failAnalysis(ctx, lease, id, code, cause)
		var result error
		if historyErr != nil {
			if jobErr != nil {
				result = fmt.Errorf("%v; record analysis failure: %w", jobErr, historyErr)
			} else {
				result = fmt.Errorf("record analysis failure: %w", historyErr)
			}
		} else {
			result = jobErr
		}
		return wrapRunFailure(run.ID, id, lease.ID, payload.Model, payload.Effort, code, failedBlock, result)
	}
	wrapFailure := func(code string, cause error, failedBlock int) error {
		return wrapRunFailure(run.ID, id, lease.ID, payload.Model, payload.Effort, code, failedBlock, cause)
	}

	if payload.Model == "" {
		return failRun("v1.analysis_model_unavailable", errors.New("no analysis model is selected"), -1, "")
	}
	if r.provider == nil {
		return failRun(annotator.CodeUnavailable, errors.New("semantic annotator is unavailable"), -1, "")
	}

	var response semantics.Response
	providerModel := payload.Model
	if !payload.Fresh {
		cached, cachedModel, hit, cacheErr := r.reader.CachedAnalysis(ctx, prepared, payload.Model, payload.Effort)
		if cacheErr != nil {
			return failRun("v1.analysis_cache_failure", cacheErr, -1, "")
		}
		if hit {
			response, providerModel = cached, cachedModel
			if providerModel == "" {
				providerModel = payload.Model
			}
			completedParagraphs = len(prepared.Blocks)
			if err := r.history.UpdateProgress(ctx, run.ID, completedParagraphs, -1); err != nil {
				return failRun("v1.analysis_history_failed", err, -1, "")
			}
		}
	}

	if response.Version == "" {
		if chunker, ok := r.provider.(annotator.ChunkSemanticAnnotator); ok {
			chunks := make([]semantics.ChunkResult, 0, len(prepared.Blocks))
			prior := make([]semantics.NewSense, 0)
			for blockIndex := range prepared.Blocks {
				chunk, chunkErr := semantics.PrepareChunk(prepared, blockIndex, prior)
				if chunkErr != nil {
					return failRun("v1.analysis_prepare_failed", chunkErr, blockIndex, "")
				}
				var chunkResponse semantics.Response
				cacheHit := false
				if !payload.Fresh {
					chunkResponse, cacheHit, chunkErr = r.reader.CachedChunk(ctx, chunk, payload.Model, payload.Effort)
					if chunkErr != nil {
						return failRun("v1.analysis_cache_failure", chunkErr, blockIndex, "")
					}
				}
				if !cacheHit {
					attempt, providerErr := r.analyzeChunkWithHeartbeat(ctx, lease, chunker, chunk, annotator.AnalysisOptions{Model: payload.Model, Effort: payload.Effort})
					if attempt.ReportedModel != "" {
						reportedModel = attempt.ReportedModel
					}
					if attempt.CLIVersion != "" {
						_ = r.history.UpdateProvenance(ctx, run.ID, attempt.CLIVersion, reportedModel)
					}
					if err := r.appendTurnArtifacts(ctx, run.ID, attempt.Turns); err != nil {
						return failRun("v1.analysis_history_failed", err, blockIndex, attempt.StderrExcerpt)
					}
					if providerErr != nil {
						if errors.Is(providerErr, jobs.ErrLeaseLost) || errors.Is(providerErr, jobs.ErrLeaseExpired) {
							_ = finishRun("failed", "v1.analysis_lease_lost", providerErr.Error(), attempt.StderrExcerpt, blockIndex)
							return wrapFailure("v1.analysis_lease_lost", providerErr, blockIndex)
						}
						return failRun(annotator.CodeOf(providerErr), providerErr, blockIndex, attempt.StderrExcerpt)
					}
					if _, validationErr := semantics.ValidateChunkResponse(chunk, attempt.Response); validationErr != nil {
						return failRun(annotator.CodeInvalidOutput, validationErr, blockIndex, attempt.StderrExcerpt)
					}
					chunkResponse = attempt.Response
					if err := r.reader.SaveChunk(ctx, chunk, chunkResponse, providerID, payload.Model, payload.Effort, run.ID); err != nil {
						return failRun("v1.analysis_cache_failure", err, blockIndex, attempt.StderrExcerpt)
					}
				}
				chunks = append(chunks, semantics.ChunkResult{Chunk: chunk, Response: chunkResponse})
				namespaced, namespaceErr := semantics.NamespaceChunkResponse(blockIndex, chunkResponse, prior)
				if namespaceErr != nil {
					return failRun(annotator.CodeInvalidOutput, namespaceErr, blockIndex, "")
				}
				prior = append(prior, namespaced.NewSenses...)
				completedParagraphs = blockIndex + 1
				if err := r.history.UpdateProgress(ctx, run.ID, completedParagraphs, -1); err != nil {
					return failRun("v1.analysis_history_failed", err, blockIndex, "")
				}
			}
			response, err = semantics.MergeChunks(prepared, chunks)
			if err != nil {
				return failRun(annotator.CodeInvalidOutput, err, completedParagraphs, "")
			}
		} else {
			response, err = r.analyzeWithHeartbeat(ctx, lease, prepared)
			if err != nil {
				if errors.Is(err, jobs.ErrLeaseLost) || errors.Is(err, jobs.ErrLeaseExpired) {
					_ = finishRun("failed", "v1.analysis_lease_lost", err.Error(), "", -1)
					return wrapFailure("v1.analysis_lease_lost", err, -1)
				}
				return failRun(annotator.CodeOf(err), err, -1, "")
			}
			completedParagraphs = len(prepared.Blocks)
			if err := r.history.UpdateProgress(ctx, run.ID, completedParagraphs, -1); err != nil {
				return failRun("v1.analysis_history_failed", err, -1, "")
			}
		}
	}

	if err := r.verifyAnalysisLease(ctx, lease); err != nil {
		_ = finishRun("failed", "v1.analysis_lease_lost", err.Error(), "", -1)
		return wrapFailure("v1.analysis_lease_lost", err, -1)
	}
	validated, err := semantics.ValidateResponse(prepared, response)
	if err != nil {
		return failRun(annotator.CodeInvalidOutput, err, completedParagraphs, "")
	}
	if err := r.reader.PersistAnalysis(ctx, id, prepared, validated, providerID, providerModel, payload.Effort, payload.Model, reportedModel); err != nil {
		return failRun("v1.analysis_persist_failed", err, completedParagraphs, "")
	}
	if err := finishRun("succeeded", "", "", "", -1); err != nil {
		return wrapFailure("v1.analysis_history_failed", r.fail(ctx, lease, "v1.analysis_history_failed", err), -1)
	}
	return r.jobs.Complete(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken)
}

type runFailure struct {
	runID       library.ULID
	articleID   library.ULID
	jobID       library.ULID
	model       string
	effort      string
	failedBlock int
	code        string
	err         error
}

func (e *runFailure) Error() string {
	if e == nil || e.err == nil {
		return e.code
	}
	return e.err.Error()
}

func (e *runFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func wrapRunFailure(runID, articleID, jobID library.ULID, model, effort, code string, failedBlock int, err error) error {
	if err == nil {
		err = errors.New(code)
	}
	return &runFailure{runID: runID, articleID: articleID, jobID: jobID, model: model, effort: effort, failedBlock: failedBlock, code: code, err: err}
}

func logAnalysisFailure(owner string, err error) {
	var failure *runFailure
	if errors.As(err, &failure) {
		log.Printf("analysis run failed owner=%q run_id=%q article_id=%q job_id=%q model=%q effort=%q failed_block=%d code=%q", owner, failure.runID.String(), failure.articleID.String(), failure.jobID.String(), failure.model, failure.effort, failure.failedBlock, failure.code)
		return
	}
	log.Printf("analysis run failed owner=%q run_id=%q article_id=%q job_id=%q model=%q effort=%q failed_block=%d code=%q", owner, "", "", "", "", "", -1, "v1.analysis_runner_failure")
}

func decodeAnalysisPayload(raw string) (reader.AnalysisJobPayload, error) {
	var payload reader.AnalysisJobPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return reader.AnalysisJobPayload{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return reader.AnalysisJobPayload{}, errors.New("analysis job payload has trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return reader.AnalysisJobPayload{}, err
	}
	for _, field := range []string{"article_id", "content_hash", "contract_version", "prompt_version", "model", "effort", "fresh"} {
		if _, ok := fields[field]; !ok {
			return reader.AnalysisJobPayload{}, fmt.Errorf("analysis job payload is missing %s", field)
		}
	}
	for _, field := range []string{"article_id", "content_hash", "contract_version", "prompt_version", "model", "effort"} {
		value := strings.TrimSpace(string(fields[field]))
		if value == "" || value[0] != '"' {
			return reader.AnalysisJobPayload{}, fmt.Errorf("analysis job payload field %s must be a string", field)
		}
	}
	if fresh := strings.TrimSpace(string(fields["fresh"])); fresh != "true" && fresh != "false" {
		return reader.AnalysisJobPayload{}, errors.New("analysis job payload field fresh must be a boolean")
	}
	if payload.PromptVersion != semantics.PromptVersion {
		return reader.AnalysisJobPayload{}, errors.New("analysis job prompt version is unsupported")
	}
	if strings.TrimSpace(payload.Effort) == "" {
		return reader.AnalysisJobPayload{}, errors.New("analysis job effort is required")
	}
	return payload, nil
}

type providerResult struct {
	response semantics.Response
	err      error
}

type chunkProviderResult struct {
	attempt annotator.ChunkAttempt
	err     error
}

// analyzeWithHeartbeat keeps the durable lease alive while the isolated
// provider is allowed to use its ten-minute analysis deadline. The buffered
// result channel lets the provider goroutine finish cleanly after a heartbeat
// or caller cancellation wins the select.
func (r *Runner) analyzeWithHeartbeat(ctx context.Context, lease *jobs.Lease, input semantics.PreparedArticle) (semantics.Response, error) {
	providerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	resultCh := make(chan providerResult, 1)
	go func() {
		response, err := r.provider.Analyze(providerContext, input)
		resultCh <- providerResult{response: response, err: err}
	}()
	ticker := time.NewTicker(jobs.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			return result.response, result.err
		case <-ticker.C:
			heartbeat, err := r.jobs.Heartbeat(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, 0)
			if err != nil {
				return semantics.Response{}, err
			}
			if heartbeat.CancelRequested {
				return semantics.Response{}, jobs.ErrLeaseLost
			}
		case <-ctx.Done():
			return semantics.Response{}, ctx.Err()
		}
	}
}

func (r *Runner) analyzeChunkWithHeartbeat(ctx context.Context, lease *jobs.Lease, provider annotator.ChunkSemanticAnnotator, chunk semantics.PreparedChunk, options annotator.AnalysisOptions) (annotator.ChunkAttempt, error) {
	providerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	resultCh := make(chan chunkProviderResult, 1)
	go func() {
		attempt, err := provider.AnalyzeChunk(providerContext, chunk, options)
		resultCh <- chunkProviderResult{attempt: attempt, err: err}
	}()
	ticker := time.NewTicker(jobs.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			return result.attempt, result.err
		case <-ticker.C:
			heartbeat, err := r.jobs.Heartbeat(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, 0)
			if err != nil {
				return annotator.ChunkAttempt{}, err
			}
			if heartbeat.CancelRequested {
				return annotator.ChunkAttempt{}, jobs.ErrLeaseLost
			}
		case <-ctx.Done():
			return annotator.ChunkAttempt{}, ctx.Err()
		}
	}
}

func (r *Runner) appendTurnArtifacts(ctx context.Context, runID library.ULID, artifacts []annotator.TurnArtifact) error {
	for _, artifact := range artifacts {
		if err := r.history.AppendTurn(ctx, Turn{
			RunID: runID, BlockIndex: artifact.BlockIndex, TurnIndex: artifact.TurnIndex,
			TurnKind: artifact.TurnKind, Prompt: artifact.Prompt, OutputSchema: artifact.OutputSchema,
			CompletedResponse: artifact.CompletedResponse, ResponseHash: artifact.ResponseHash,
			ValidationError: artifact.ValidationError, ProviderError: artifact.ProviderError,
			CompletionMetadataJSON: artifact.CompletionMetadataJSON,
			ProviderStderrExcerpt:  artifact.ProviderStderrExcerpt, StartedAt: artifact.StartedAt,
			CompletedAt: artifact.CompletedAt, DurationMS: artifact.Duration.Milliseconds(), Status: artifact.Status,
		}); err != nil {
			return err
		}
	}
	return nil
}

func providerCLIVersion(ctx context.Context, provider annotator.SemanticAnnotator) string {
	if versioner, ok := provider.(interface{ CLIVersion(context.Context) string }); ok {
		return versioner.CLIVersion(ctx)
	}
	return ""
}

func diagnosticExcerpt(value string) string {
	const max = 16 << 10
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func (r *Runner) failAnalysis(ctx context.Context, lease *jobs.Lease, id library.ULID, code string, cause error) error {
	if err := r.verifyAnalysisLease(ctx, lease); err != nil {
		return err
	}
	_ = r.reader.MarkAnalysisFailed(ctx, id, code)
	return r.fail(ctx, lease, code, cause)
}

// verifyAnalysisLease is stricter than the generic job acknowledgement check:
// a canceled or already-terminal analysis must never publish a provider result
// after an owner-requested reanalysis has superseded it.
func (r *Runner) verifyAnalysisLease(ctx context.Context, lease *jobs.Lease) error {
	job, err := r.jobs.VerifyLease(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, r.owner)
	if err != nil {
		return err
	}
	if job.State != jobs.StateLeased && job.State != jobs.StateRunning {
		return jobs.ErrLeaseLost
	}
	return nil
}

func (r *Runner) fail(ctx context.Context, lease *jobs.Lease, code string, cause error) error {
	retry := lease.AttemptCount < lease.MaxAttempts
	if err := r.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, code, retry); err != nil {
		return fmt.Errorf("analysis job failed (%v): %w", cause, err)
	}
	if !retry {
		return fmt.Errorf("analysis job failed: %w", cause)
	}
	return cause
}

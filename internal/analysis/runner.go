// Package analysis runs durable semantic-reader jobs on the server side.
package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/media"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

type Runner struct {
	jobs     *jobs.Store
	reader   *reader.Store
	provider annotator.SemanticAnnotator
	owner    string
}

func NewRunner(db *store.DB, provider annotator.SemanticAnnotator) *Runner {
	return NewRunnerWithMedia(db, provider, nil)
}

// NewRunnerWithMedia enables post-commit cleanup of superseded article audio
// while retaining the durable analysis runner's database-only test seam.
func NewRunnerWithMedia(db *store.DB, provider annotator.SemanticAnnotator, mediaStore *media.Store) *Runner {
	return &Runner{jobs: jobs.NewStore(db), reader: reader.NewStoreWithMedia(db, mediaStore), provider: provider, owner: "server-analysis"}
}

// Run polls SQLite until ctx is canceled. A short idle wait keeps local tests
// responsive while avoiding an in-memory queue or a busy loop.
func (r *Runner) Run(ctx context.Context) {
	for {
		if err := r.RunOnce(ctx); err != nil && !errors.Is(err, jobs.ErrNoWork) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// The durable job/article state contains the actionable failure; the
			// runner intentionally stays alive for later owner retries.
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
	var payload reader.AnalysisJobPayload
	decoder := json.NewDecoder(strings.NewReader(lease.PayloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return r.fail(ctx, lease, "v1.analysis_invalid_job", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return r.fail(ctx, lease, "v1.analysis_invalid_job", errors.New("analysis job payload has trailing JSON"))
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

	var response semantics.Response
	providerID, providerModel := semantics.ProviderID, ""
	if cached, cachedModel, ok, cacheErr := r.reader.CachedAnalysis(ctx, prepared); cacheErr != nil {
		return r.failAnalysis(ctx, lease, id, "v1.analysis_cache_failure", cacheErr)
	} else if ok {
		response, providerModel = cached, cachedModel
	} else {
		if r.provider == nil {
			return r.failAnalysis(ctx, lease, id, annotator.CodeUnavailable, errors.New("semantic annotator is unavailable"))
		}
		response, err = r.analyzeWithHeartbeat(ctx, lease, prepared)
		if err != nil {
			if errors.Is(err, jobs.ErrLeaseLost) || errors.Is(err, jobs.ErrLeaseExpired) {
				return err
			}
			return r.failAnalysis(ctx, lease, id, annotator.CodeOf(err), err)
		}
		if modeler, ok := r.provider.(interface{ Model() string }); ok {
			providerModel = modeler.Model()
		}
	}
	// A provider can finish between heartbeat ticks. Recheck the lease before
	// accepting its response so an expired worker cannot publish a stale run.
	if _, err := r.jobs.VerifyLease(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, r.owner); err != nil {
		return err
	}
	validated, err := semantics.ValidateResponse(prepared, response)
	if err != nil {
		return r.failAnalysis(ctx, lease, id, annotator.CodeInvalidOutput, err)
	}
	if err := r.reader.PersistAnalysis(ctx, id, prepared, validated, providerID, providerModel); err != nil {
		return r.failAnalysis(ctx, lease, id, "v1.analysis_persist_failed", err)
	}
	return r.jobs.Complete(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken)
}

type providerResult struct {
	response semantics.Response
	err      error
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

func (r *Runner) failAnalysis(ctx context.Context, lease *jobs.Lease, id library.ULID, code string, cause error) error {
	if _, err := r.jobs.VerifyLease(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, r.owner); err != nil {
		return err
	}
	_ = r.reader.MarkAnalysisFailed(ctx, id, code)
	return r.fail(ctx, lease, code, cause)
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

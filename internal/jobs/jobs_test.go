package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

func testJobSpec(key string) Spec {
	return Spec{
		JobType: AnalysisJobType, ExecutionTarget: TargetServer,
		OwnerType: "article", OwnerID: "article-1", IdempotencyKey: key,
		InputHash: key, PayloadJSON: `{"article_id":"01J00000000000000000000000"}`, Priority: 10,
	}
}

func TestEnqueueIsIdempotentAndTransactionReadDoesNotInsert(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)

	first, err := s.Enqueue(ctx, testJobSpec("job-idempotent"))
	if err != nil {
		t.Fatal(err)
	}
	secondSpec := testJobSpec("job-idempotent")
	secondSpec.ID = library.NewULID()
	secondSpec.PayloadJSON = `{"different":true}`
	second, err := s.Enqueue(ctx, secondSpec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.PayloadJSON != first.PayloadJSON {
		t.Fatalf("idempotent enqueue changed row: first=%+v second=%+v", first, second)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE idempotency_key = 'job-idempotent'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent row count = %d", count)
	}

	if err := db.WithTransaction(ctx, func(tx *sql.Tx) error {
		job, err := GetByIdempotencyKeyTx(ctx, tx, "job-idempotent")
		if err != nil {
			return err
		}
		if job.ID != first.ID {
			t.Fatalf("transaction job id = %s, want %s", job.ID, first.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDependenciesGateClaimsAndRejectCycles(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)
	dependency, err := s.Enqueue(ctx, testJobSpec("dependency"))
	if err != nil {
		t.Fatal(err)
	}
	dependentSpec := testJobSpec("dependent")
	dependentSpec.ID = library.NewULID()
	dependent, err := s.Enqueue(ctx, dependentSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, dependent.ID, dependency.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, dependency.ID, dependent.ID); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle error = %v", err)
	}
	// The first claim is the dependency. Complete it, then the dependent can
	// be claimed by the same durable scheduler.
	lease, err := s.Claim(ctx, TargetServer, "server-2")
	if err != nil {
		t.Fatal(err)
	}
	if lease.ID != dependency.ID {
		t.Fatalf("claimed dependency = %s, want %s", lease.ID, dependency.ID)
	}
	if err := s.Complete(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken); err != nil {
		t.Fatal(err)
	}
	dependentLease, err := s.Claim(ctx, TargetServer, "server-3")
	if err != nil {
		t.Fatal(err)
	}
	if dependentLease.ID != dependent.ID {
		t.Fatalf("claimed dependent = %s, want %s", dependentLease.ID, dependent.ID)
	}
}

func TestFailedDependencyIsReconciledWithoutClaim(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)
	first, err := s.Enqueue(ctx, testJobSpec("failed-dependency"))
	if err != nil {
		t.Fatal(err)
	}
	secondSpec := testJobSpec("blocked-by-failure")
	secondSpec.ID = library.NewULID()
	second, err := s.Enqueue(ctx, secondSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, second.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, TargetServer, "server")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "v1.provider_failure", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, TargetServer, "server"); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim after dependency failure = %v", err)
	}
	got, err := s.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateFailed || got.ErrorCode != "v1.job_dependency_failed" {
		t.Fatalf("dependent state = %+v", got)
	}
}

func TestLeaseHeartbeatExpiryRetryAndRecovery(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)

	job, err := s.Enqueue(ctx, testJobSpec("retry-job"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, TargetServer, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := s.Heartbeat(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, 42)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Job.State != StateRunning || heartbeat.Job.ProgressPercent != 42 {
		t.Fatalf("heartbeat = %+v", heartbeat.Job)
	}
	if err := s.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "v1.transient", true); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != StateQueued || updated.AttemptCount != 1 || updated.leaseTokenHash == "" {
		t.Fatalf("retry state = %+v", updated)
	}
	if err := s.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "v1.transient", true); err != nil {
		t.Fatalf("duplicate failure acknowledgement = %v", err)
	}
	if _, err := s.Claim(ctx, TargetServer, "worker-2"); !errors.Is(err, ErrNoWork) {
		t.Fatalf("early retry claim = %v", err)
	}

	recoverySpec := testJobSpec("expired-job")
	recoverySpec.ID = library.NewULID()
	recovery, err := s.Enqueue(ctx, recoverySpec)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.Claim(ctx, TargetServer, "worker-expired")
	if err != nil {
		t.Fatal(err)
	}
	if expired.ID != recovery.ID {
		t.Fatal("wrong job claimed for expiry test")
	}
	if _, err := db.Exec(ctx, `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, expired.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, expired.ID, expired.AttemptCount, expired.LeaseToken, 10); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired heartbeat = %v", err)
	}
	if _, err := s.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Get(ctx, expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != StateQueued || recovered.ErrorCode != "v1.job_lease_expired" {
		t.Fatalf("recovered job = %+v", recovered)
	}
}

func TestCancelOwnerJobsClearsQueuedLease(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)
	job, err := s.Enqueue(ctx, testJobSpec("cancel-job"))
	if err != nil {
		t.Fatal(err)
	}
	if count, err := s.CancelOwnerJobs(ctx, "article", job.OwnerID, AnalysisJobType, "v1.owner_canceled"); err != nil || count != 1 {
		t.Fatalf("queued cancellation = %d/%v", count, err)
	}
	got, err := s.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateCanceled || got.LeaseOwner != "" || got.LeaseExpiresAt != "" || got.leaseTokenHash != "" {
		t.Fatalf("queued canceled job = %+v", got)
	}
}

func TestCancelOwnerJobsKeepsActiveLeaseForHeartbeat(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewStore(db)
	job, err := s.Enqueue(ctx, testJobSpec("cancel-active-job"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, TargetServer, "server")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, 12); err != nil {
		t.Fatal(err)
	}
	beforeCancel, err := s.Get(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := s.CancelOwnerJobs(ctx, "article", job.OwnerID, AnalysisJobType, "v1.owner_canceled"); err != nil || count != 1 {
		t.Fatalf("active cancellation = %d/%v", count, err)
	}
	got, err := s.Get(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateCanceled || got.LeaseOwner != "server" || got.LeaseExpiresAt != beforeCancel.LeaseExpiresAt || got.leaseTokenHash == "" || got.ProgressPercent != 12 {
		t.Fatalf("active canceled job = %+v", got)
	}
	if heartbeat, heartbeatErr := s.Heartbeat(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, 20); heartbeatErr != nil || !heartbeat.CancelRequested || heartbeat.Job.State != StateCanceled || heartbeat.Job.ProgressPercent != 12 || heartbeat.Job.LeaseExpiresAt != beforeCancel.LeaseExpiresAt {
		t.Fatalf("canceled heartbeat = %+v/%v", heartbeat, heartbeatErr)
	}
	if _, err := s.VerifyLease(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "server"); err != nil {
		t.Fatalf("matching canceled lease = %v", err)
	}
	if _, err := s.VerifyLease(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "other-worker"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("wrong canceled lease owner = %v", err)
	}
	if err := s.Complete(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("canceled completion = %v", err)
	}
	if err := s.Cancel(ctx, lease.ID, "v1.owner_canceled"); err != nil {
		t.Fatal(err)
	}
}

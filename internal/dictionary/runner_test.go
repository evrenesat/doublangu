package dictionary

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"strings"
)

// TestRunnerPublicationAndFencing drives one dictionary job through the real
// runner, then proves cancel fencing and duplicate completion are harmless.
func TestRunnerPublicationAndFencing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")

	provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
	runner := NewRunner(db, &fakeRegistry{provider: provider})
	runner.heartbeatInterval = 0
	service := resolvingService(t, db, "fp-1")
	articleID := library.ULID("01J00000000000000000000ART1")
	ref := Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}

	envelope, _, started, err := service.Start(ctx, articleID, ref, false, false)
	if err != nil || !started {
		t.Fatalf("start: err=%v started=%t", err, started)
	}
	entryID := library.ULID(envelope.EntryID)

	t.Run("cancel before publish prevents publication", func(t *testing.T) {
		lease, err := jobs.NewStore(db).ClaimMatching(ctx, jobs.TargetServer, "fenced-worker", func(job jobs.Job) bool {
			return job.JobType == JobType
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := jobs.NewStore(db).Cancel(ctx, lease.ID, "v1.job_canceled"); err != nil {
			t.Fatal(err)
		}
		input, err := payloadInput(lease.PayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		document := validRunnerResponse(input)
		err = db.WithTransaction(ctx, func(tx *sql.Tx) error {
			if err := PublishTx(ctx, tx, entryID, lease.ID.String(), document, "hash", "contract", "prompt", "prov"); err != nil {
				return err
			}
			return jobs.CompleteTx(ctx, tx, lease.ID, lease.AttemptCount, lease.LeaseToken)
		})
		if err == nil {
			t.Fatal("a canceled job must refuse completion")
		}
		var documentJSON any
		if err := db.QueryRow(ctx, `SELECT document_json FROM dictionary_entry WHERE id = ?`, entryID.String()).Scan(&documentJSON); err != nil {
			t.Fatal(err)
		}
		if documentJSON != nil {
			t.Fatal("no document may be published for a canceled job")
		}
	})

	t.Run("duplicate publish+complete is harmless", func(t *testing.T) {
		fixtureArticle(t, db, "01J00000000000000000000ART2", "Fixture 2")
		fixtureBlock(t, db, "01J00000000000000000000BLK2", "01J00000000000000000000ART2", "Het plan.")
		fixtureWordOccurrence(t, db, "01J00000000000000000000WRD2", "01J00000000000000000000BLK2", "plan", "")
		envelope, _, started, err := service.Start(ctx, library.ULID("01J00000000000000000000ART2"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD2")}, false, false)
		if err != nil || !started {
			t.Fatalf("start: err=%v started=%t", err, started)
		}
		lease, err := jobs.NewStore(db).ClaimMatching(ctx, jobs.TargetServer, "dup-worker", func(job jobs.Job) bool {
			return job.JobType == JobType && job.ID.String() == envelope.JobID
		})
		if err != nil {
			t.Fatal(err)
		}
		input, err := payloadInput(lease.PayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		document := validRunnerResponse(input)
		publish := func() error {
			return db.WithTransaction(ctx, func(tx *sql.Tx) error {
				if err := PublishTx(ctx, tx, library.ULID(envelope.EntryID), lease.ID.String(), document, "hash", "contract", "prompt", "prov"); err != nil {
					return err
				}
				return jobs.CompleteTx(ctx, tx, lease.ID, lease.AttemptCount, lease.LeaseToken)
			})
		}
		if err := publish(); err != nil {
			t.Fatalf("first publish: %v", err)
		}
		if err := publish(); err != nil {
			t.Fatalf("duplicate publish must be harmless: %v", err)
		}
	})

	t.Run("runner publishes through the full path", func(t *testing.T) {
		fixtureArticle(t, db, "01J00000000000000000000ART3", "Fixture 3")
		fixtureBlock(t, db, "01J00000000000000000000BLK3", "01J00000000000000000000ART3", "Het hok.")
		fixtureWordOccurrence(t, db, "01J00000000000000000000WRD3", "01J00000000000000000000BLK3", "hok", "")
		if _, _, started, err := service.Start(ctx, library.ULID("01J00000000000000000000ART3"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD3")}, false, false); err != nil || !started {
			t.Fatalf("start: err=%v started=%t", err, started)
		}
		if err := runner.RunOnce(ctx); err != nil {
			t.Fatalf("runner: %v", err)
		}
		result, _, err := service.Lookup(ctx, library.ULID("01J00000000000000000000ART3"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD3")})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusReady || result.Document == nil || len(result.Document.Senses) != 1 {
			t.Fatalf("result = %+v", result)
		}
		var provenance string
		if err := db.QueryRow(ctx, `SELECT provenance_json FROM dictionary_entry WHERE normalized_lookup_form = 'hok'`).Scan(&provenance); err != nil {
			t.Fatal(err)
		}
		if provenance == "" || strings.Contains(provenance, "endpoint") || strings.Contains(provenance, "secret") {
			t.Fatalf("provenance = %q", provenance)
		}
	})
}

func payloadInput(payloadJSON string) (semantics.DictionaryInput, error) {
	payload, err := DecodeJobPayload([]byte(payloadJSON))
	if err != nil {
		return semantics.DictionaryInput{}, err
	}
	return payload.Input, nil
}

// TestRoutingRunnersDoNotSteal proves neither runner claims the other's jobs.
func TestRoutingRunnersDoNotSteal(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	// A queued analysis job only: the dictionary runner must find no work.
	if _, err := jobs.NewStore(db).Enqueue(ctx, jobs.Spec{
		JobType: jobs.AnalysisJobType, ExecutionTarget: jobs.TargetServer,
		OwnerType: "article", OwnerID: "01J00000000000000000000ART9",
		IdempotencyKey: "analysis-only-1", InputHash: "hash", PayloadJSON: `{"article_id":"01J00000000000000000000ART9"}`,
	}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
	dictionaryRunner := NewRunner(db, &fakeRegistry{provider: provider})
	dictionaryRunner.heartbeatInterval = 0
	if err := dictionaryRunner.RunOnce(ctx); !errors.Is(err, jobs.ErrNoWork) {
		t.Fatalf("dictionary runner took an analysis job: %v", err)
	}
	// The legacy analysis runner must not take dictionary work either: remove
	// the analysis job so the only queued work is the dictionary job.
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")
	dictionaryService := resolvingService(t, db, "fp-1")
	if _, _, _, err := dictionaryService.Start(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}, false, false); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `DELETE FROM job WHERE job_type = ? AND idempotency_key = 'analysis-only-1'`, jobs.AnalysisJobType)
	analysisRunner := analysis.NewRunner(db, annotator.Disabled{})
	if err := analysisRunner.RunOnce(ctx); !errors.Is(err, jobs.ErrNoWork) {
		t.Fatalf("analysis runner took a dictionary job: %v", err)
	}
	// The dictionary runner claims exactly its own queued job.
	provider.mu.Lock()
	provider.dynamic = true
	provider.mu.Unlock()
	dictionaryRunner = NewRunner(db, &fakeRegistry{provider: provider})
	dictionaryRunner.heartbeatInterval = 0
	if err := dictionaryRunner.RunOnce(ctx); err != nil {
		t.Fatalf("dictionary runner: %v", err)
	}
	var succeeded int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE job_type = ? AND state = 'succeeded'`, JobType).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 {
		t.Fatalf("succeeded dictionary jobs = %d, want 1", succeeded)
	}
}

// TestRunnerProviderChangeFailsExplicitly proves removed and changed
// providers fail with stable codes instead of substituting a model.
func TestRunnerProviderChangeFailsExplicitly(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")

	// No provider in the registry at all.
	emptyRunner := NewRunner(db, &fakeRegistry{})
	emptyRunner.heartbeatInterval = 0
	service := resolvingService(t, db, "fp-1")
	if _, _, _, err := service.Start(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := emptyRunner.RunOnce(ctx); err != nil {
		t.Fatalf("runner: %v", err)
	}
	envelope, _, err := service.Lookup(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != StatusFailed || envelope.ErrorCode != CodeProviderUnavailable {
		t.Fatalf("envelope = %+v", envelope)
	}

	// A changed config fingerprint fails as provider_changed on retry.
	if _, _, started, err := service.Start(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}, true, false); err != nil || !started {
		t.Fatalf("retry: err=%v started=%t", err, started)
	}
	changed := newFakeProvider("")
	changed.descriptor.ConfigFingerprint = "fp-2"
	changedRunner := NewRunner(db, &fakeRegistry{provider: changed})
	changedRunner.heartbeatInterval = 0
	if err := changedRunner.RunOnce(ctx); err != nil {
		t.Fatalf("runner: %v", err)
	}
	envelope, _, err = service.Lookup(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != StatusFailed || envelope.ErrorCode != CodeProviderChanged {
		t.Fatalf("envelope = %+v", envelope)
	}
}

// TestRunnerExpiredLeaseNeverPublishes proves a lost lease ends as a failed
// entry that explicit Retry can regenerate.
func TestRunnerExpiredLeaseNeverPublishes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")

	provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
	registry := &fakeRegistry{provider: provider}
	runner := NewRunner(db, registry)
	runner.heartbeatInterval = 0
	service := resolvingService(t, db, "fp-1")

	envelope, _, started, err := service.Start(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}, false, false)
	if err != nil || !started {
		t.Fatalf("start: err=%v started=%t", err, started)
	}
	// Simulate a wedged worker: claim the job, then let its lease expire.
	if _, err := jobs.NewStore(db).ClaimMatching(ctx, jobs.TargetServer, "wedged-worker", func(job jobs.Job) bool {
		return job.JobType == JobType && job.ID.String() == envelope.JobID
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE job SET lease_expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.000Z"), envelope.JobID)
	if err := runner.RunOnce(ctx); !errors.Is(err, jobs.ErrNoWork) {
		t.Fatalf("runner after expiry: %v", err)
	}
	result, _, err := service.Lookup(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	// Explicit retry regenerates successfully with the same configuration.
	if _, _, started, err := service.Start(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}, true, false); err != nil || !started {
		t.Fatalf("retry: err=%v started=%t", err, started)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("runner: %v", err)
	}
	final, _, err := service.Lookup(ctx, library.ULID("01J00000000000000000000ART1"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")})
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != StatusReady || final.Document == nil {
		t.Fatalf("final = %+v", final)
	}
}

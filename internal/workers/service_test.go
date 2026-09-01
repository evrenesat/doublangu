package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/media"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

const workerArticleID = "01J0000000000000000000010"

func seedWorkerArticle(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, 'Worker test', 'nl', 'en', 'draft')`, workerArticleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J0000000000000000000011', ?, 0, 'paragraph', 'Een zin.')`, workerArticleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES ('01J0000000000000000000012', '01J0000000000000000000011', 0, 0, 8, 'Een zin.', 'source-hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, kind, role, shadow_policy, shadow_text, confidence_milli) VALUES ('01J0000000000000000000013', '01J0000000000000000000011', '01J0000000000000000000012', 'word', 'token', 'token', 'one', 900)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES ('01J0000000000000000000014', '01J0000000000000000000013', 0, 0, 3, 'Een')`); err != nil {
		t.Fatal(err)
	}
	if err := speech.NewStore(db).QueueArticleAudio(ctx, library.ULID(workerArticleID), false); err != nil {
		t.Fatal(err)
	}
}

func workerCapabilities() []speech.WorkerCapability {
	return []speech.WorkerCapability{{
		Engine: speech.ChatterboxEngine, Languages: []string{"nl"}, UnitKinds: []string{"sentence"},
		MaxBytes: 64 << 20, MaxDurationMS: 180000,
	}}
}

func fakeM4A() []byte {
	return []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2'}
}

func artifactFor(data []byte, requestHash string) speech.ArtifactMetadata {
	sum := sha256.Sum256(data)
	return speech.ArtifactMetadata{
		RequestHash: requestHash, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)),
		MIMEType: speech.AudioMIME, Codec: speech.AudioCodec, SampleRateHz: speech.AudioSampleRate,
		Channels: speech.AudioChannels, DurationMS: 100,
	}
}

func TestEnrollmentLeaseCompletionAndIdempotentUpload(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedWorkerArticle(t, db)
	mediaStore, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := NewService(db, mediaStore)
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input := EnrollInput{Name: "test-worker", ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities(), SoftwareVersion: "test"}
	worker, workerToken, err := service.Enroll(ctx, enrollment.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Enroll(ctx, enrollment.Token, input); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("reused enrollment = %v", err)
	}
	if _, err := service.Authenticate(ctx, "wrong-token"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong worker token = %v", err)
	}
	lease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	if lease.UnitKind != speech.UnitSentence || lease.JobType != jobs.ChatterboxJobType || lease.RequestHash == "" {
		t.Fatalf("sentence lease = %+v", lease)
	}
	heartbeat, err := service.Heartbeat(ctx, worker, lease.JobID, lease.LeaseToken, HeartbeatInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, ProgressPercent: 37})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.ProgressPercent != 37 || heartbeat.CancelRequested {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}

	data := fakeM4A()
	metadata := CompleteMetadata{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, LeaseToken: lease.LeaseToken, Artifact: artifactFor(data, lease.RequestHash)}
	if err := service.Complete(ctx, worker, lease.JobID, metadata, data); err != nil {
		t.Fatal(err)
	}
	if err := service.Complete(ctx, worker, lease.JobID, metadata, data); err != nil {
		t.Fatalf("duplicate completion = %v", err)
	}
	var renderID, state, digest string
	if err := db.QueryRow(ctx, `SELECT ar.id, ar.state, abr.blob_digest FROM audio_render ar JOIN audio_blob_reference abr ON abr.audio_render_id = ar.id WHERE ar.request_hash = ?`, lease.RequestHash).Scan(&renderID, &state, &digest); err != nil {
		t.Fatal(err)
	}
	if state != speech.RenderReady || digest == "" || renderID != lease.RenderID.String() {
		t.Fatalf("published render = %s/%s/%s", renderID, state, digest)
	}
	var jobState string
	if err := db.QueryRow(ctx, `SELECT state FROM job WHERE id = ?`, lease.JobID.String()).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != jobs.StateSucceeded {
		t.Fatalf("job state = %q", jobState)
	}
	read, _, err := mediaStore.Read(digest)
	if err != nil || string(read) != string(data) {
		t.Fatalf("published bytes = %d err=%v", len(read), err)
	}

	conflicting := fakeM4A()
	conflicting[len(conflicting)-1] = '3'
	conflictingMetadata := metadata
	conflictingMetadata.Artifact = artifactFor(conflicting, lease.RequestHash)
	if err := service.Complete(ctx, worker, lease.JobID, conflictingMetadata, conflicting); !errors.Is(err, ErrNondeterministic) {
		t.Fatalf("conflicting completion = %v", err)
	}
	if err := service.Revoke(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, workerToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked worker auth = %v", err)
	}
}

func TestWorkerLeaseExpiryAndMalformedCapabilities(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedWorkerArticle(t, db)
	mediaStore, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := NewService(db, mediaStore)
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worker, _, err := service.Enroll(ctx, enrollment.Token, EnrollInput{Name: "expiry-worker", ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities(), SoftwareVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: "wrong", Capabilities: workerCapabilities()}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("wrong protocol lease = %v", err)
	}
	lease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, lease.JobID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(ctx, worker, lease.JobID, lease.LeaseToken, HeartbeatInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, ProgressPercent: 1}); !errors.Is(err, jobs.ErrLeaseExpired) {
		t.Fatalf("expired worker heartbeat = %v", err)
	}
	if err := service.Fail(ctx, worker, lease.JobID, lease.LeaseToken, FailInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, ErrorCode: "invalid code", Retry: false}); err == nil {
		t.Fatal("invalid worker error code unexpectedly accepted")
	}
}

func TestWorkerFailureIsIdempotentAndMarksTerminalRender(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedWorkerArticle(t, db)
	mediaStore, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := NewService(db, mediaStore)
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worker, _, err := service.Enroll(ctx, enrollment.Token, EnrollInput{
		Name: "failure-worker", ProtocolVersion: speech.ProtocolVersion,
		Capabilities: workerCapabilities(), SoftwareVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.Lease(ctx, worker, LeaseRequest{ProtocolVersion: speech.ProtocolVersion, Capabilities: workerCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	failure := FailInput{ProtocolVersion: speech.ProtocolVersion, Attempt: lease.Attempt, ErrorCode: "v1.voice_unavailable", Retry: false}
	if err := service.Fail(ctx, worker, lease.JobID, lease.LeaseToken, failure); err != nil {
		t.Fatal(err)
	}
	if err := service.Fail(ctx, worker, lease.JobID, lease.LeaseToken, failure); err != nil {
		t.Fatalf("duplicate worker failure = %v", err)
	}
	var jobState, renderState, renderCode string
	if err := db.QueryRow(ctx, `SELECT state, error_code FROM job WHERE id = ?`, lease.JobID.String()).Scan(&jobState, &renderCode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT state, error_code FROM audio_render WHERE id = ?`, lease.RenderID.String()).Scan(&renderState, &renderCode); err != nil {
		t.Fatal(err)
	}
	if jobState != jobs.StateFailed || renderState != speech.RenderFailed || renderCode != failure.ErrorCode {
		t.Fatalf("failure state = job %q render %q/%q", jobState, renderState, renderCode)
	}
}

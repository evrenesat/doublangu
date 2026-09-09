package sentencetranslation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
	"doublangu/internal/store"
)

// --- fixtures ---------------------------------------------------------------

func sentenceSubject(t *testing.T, db *store.DB, source, paragraph string) (articleID, sentenceID library.ULID) {
	t.Helper()
	article, block, sentence := library.NewULID(), library.NewULID(), library.NewULID()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, ?, 'nl', 'en', 'ready')`,
		article.String(), "Fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, 0, 'paragraph', ?)`,
		block.String(), article.String(), paragraph); err != nil {
		t.Fatal(err)
	}
	hash := sourceHash(source)
	if _, err := db.Exec(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES (?, ?, 0, 0, ?, ?, ?)`,
		sentence.String(), block.String(), len([]rune(source)), source, hash); err != nil {
		t.Fatal(err)
	}
	return article, sentence
}

// testTranslationBinding builds a validated fake Translation binding snapshot.
func testTranslationBinding(t *testing.T, fingerprint string) pipeline.BindingSnapshot {
	t.Helper()
	options, err := json.Marshal(map[string]string{"reasoning_effort": "low"})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline.BindingSnapshot{
		StageID: pipeline.StageTranslation, ProviderID: "fake-provider", ProviderType: annotator.ProviderTypeCodexAppServer,
		ProviderConfigFingerprint: fingerprint, ModelID: "sentence-model", Options: options, OptionsHash: hash,
		ContractVersion: pipeline.TranslationContractVersion, PromptVersion: pipeline.TranslationPromptVersion,
	}
}

func testSentenceSnapshot(t *testing.T, promptType, instruction string) pipeline.PromptSnapshot {
	t.Helper()
	return pipeline.PromptSnapshot{
		Type: promptType, ID: "snap-" + promptType, Version: 1,
		ContentHash: prompts.ContentHashOf(instruction), InstructionText: instruction,
		EnvelopeVersion: pipeline.PromptCapturedEnvelopeVersion,
	}
}

type countingResolver struct {
	mu        sync.Mutex
	calls     int
	binding   pipeline.BindingSnapshot
	snapshots []pipeline.PromptSnapshot
	err       error
}

func (r *countingResolver) resolve(context.Context) (ResolvedSentenceGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return ResolvedSentenceGeneration{}, r.err
	}
	return ResolvedSentenceGeneration{
		Binding: r.binding, PromptSnapshots: r.snapshots,
		ProfileID: "profile-sentence", ProfileName: "Sentence profile",
	}, nil
}

func (r *countingResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func resolvingSentenceService(t *testing.T, db *store.DB, fingerprint string) (*Service, *countingResolver) {
	t.Helper()
	resolver := &countingResolver{
		binding: testTranslationBinding(t, fingerprint),
		snapshots: []pipeline.PromptSnapshot{
			testSentenceSnapshot(t, "sentence_translation", "Sentence generation instruction."),
			testSentenceSnapshot(t, "correction", "Sentence correction instruction."),
		},
	}
	return NewService(db, resolver.resolve), resolver
}

// --- service behavior --------------------------------------------------------

func TestEnsureMissingSentenceWritesNothing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, _ := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, resolver := resolvingSentenceService(t, db, "fp-1")

	ghost := library.NewULID()
	if _, _, err := service.Ensure(ctx, article, ghost, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := service.Lookup(ctx, article, ghost); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup err = %v", err)
	}
	// Cross-article membership also fails before any run or queue work.
	otherArticle, _ := sentenceSubject(t, db, "Hij loopt.", "Hij loopt.")
	_, sentence := sentenceSubject(t, db, "Zij staat.", "Zij staat.")
	if _, _, err := service.Ensure(ctx, otherArticle, sentence, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-article err = %v", err)
	}
	if resolver.callCount() != 0 {
		t.Fatal("resolver must not run for membership failures")
	}
	var counts [3]int
	queries := []string{
		`SELECT COUNT(*) FROM sentence_translation`,
		`SELECT COUNT(*) FROM job WHERE job_type = 'reader.sentence_translation.v1'`,
		`SELECT COUNT(*) FROM analysis_run WHERE operation_type = 'sentence_translation'`,
	}
	for i, query := range queries {
		if err := db.QueryRow(ctx, query).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
		if counts[i] != 0 {
			t.Fatalf("%s count = %d, want 0", query, counts[i])
		}
	}
}

func TestEnsureQueuesOneJobAndConcurrentJoins(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, resolver := resolvingSentenceService(t, db, "fp-1")

	envelope, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	if envelope.Status != StatusQueued || envelope.JobID == "" || envelope.RunID == "" {
		t.Fatalf("envelope = %+v", envelope)
	}

	again, startedAgain, err := service.Ensure(ctx, article, sentence, false)
	if err != nil {
		t.Fatal(err)
	}
	if startedAgain {
		t.Fatal("second ensure must join the active generation")
	}
	if again.JobID != envelope.JobID || again.RunID != envelope.RunID {
		t.Fatalf("job/run ids differ: %+v vs %+v", again, envelope)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.callCount())
	}

	var jobCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE job_type = ?`, JobType).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("job count = %d, want 1", jobCount)
	}
	job, err := jobs.NewStore(db).Get(ctx, library.ULID(envelope.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if job.MaxAttempts != 1 || job.ExecutionTarget != jobs.TargetServer || job.OwnerType != OwnerType || job.OwnerID != sentence.String() {
		t.Fatalf("job spec = %+v", job)
	}
	payload, err := DecodeJobPayload([]byte(job.PayloadJSON))
	if err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.RunID != envelope.RunID || payload.SentenceID != sentence.String() || payload.ArticleID != article.String() {
		t.Fatalf("payload identity = %+v", payload)
	}
	if payload.Input.SourceText != "Zij zit op een bank." || payload.Input.ParagraphText != "Zij zit op een bank." {
		t.Fatalf("payload input = %+v", payload.Input)
	}

	var operation, subject, runJob string
	if err := db.QueryRow(ctx, `SELECT operation_type, subject_id, job_id FROM analysis_run WHERE id = ?`, envelope.RunID).Scan(&operation, &subject, &runJob); err != nil {
		t.Fatal(err)
	}
	if operation != OperationType || subject != sentence.String() || runJob != envelope.JobID {
		t.Fatalf("run row = %q %q %q", operation, subject, runJob)
	}
}

func TestPreflightFailureRetainsRunAndConverges(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, resolver := resolvingSentenceService(t, db, "fp-1")
	resolver.err = errors.New("no usable profile")

	if _, _, err := service.Ensure(ctx, article, sentence, false); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v", err)
	}

	envelope, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != StatusFailed || envelope.ErrorCode != CodeProviderUnavailable || envelope.RunID == "" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.JobID != "" {
		t.Fatalf("preflight failure must not enqueue a job: %+v", envelope)
	}

	// A repeated hover observes the retained failure without re-resolving.
	again, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil {
		t.Fatal(err)
	}
	if started || again.Status != StatusFailed || again.RunID != envelope.RunID {
		t.Fatalf("repeat = %+v started=%t", again, started)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d, want 1: preflight failure must not retry on hover", resolver.callCount())
	}

	// An explicit regenerate replaces the run without mutating the earlier one.
	resolver.err = nil
	regenerated, started, err := service.Ensure(ctx, article, sentence, true)
	if err != nil || !started {
		t.Fatalf("regenerate: err=%v started=%t", err, started)
	}
	if regenerated.RunID == envelope.RunID || regenerated.JobID == "" {
		t.Fatalf("regenerated = %+v", regenerated)
	}
	oldRun, err := analysis.NewHistoryStore(db).GetRun(ctx, library.ULID(envelope.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if oldRun.Status != "failed" {
		t.Fatalf("earlier run must stay failed, got %q", oldRun.Status)
	}
}

func TestOrphanRunReconciliation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	envelope, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	// Simulate a crash between claiming and enqueueing: the run has no job
	// and the queued job never existed. Cancel the queued job (its pointer
	// clears) and backdate the unattached run so the sweeper treats it as
	// abandoned.
	if err := jobs.NewStore(db).Cancel(ctx, library.ULID(envelope.JobID), "v1.test_abandoned"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE analysis_run SET job_id = ?, started_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`,
		library.ULID("").String(), envelope.RunID); err != nil {
		t.Fatal(err)
	}
	affected, err := ReconcileOrphanRuns(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("reconciled = %d, want 1", affected)
	}
	run, err := analysis.NewHistoryStore(db).GetRun(ctx, library.ULID(envelope.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatalf("orphan run status = %q", run.Status)
	}

	// A fresh claim is younger than the abandonment window and survives.
	fresh, started, err := service.Ensure(ctx, article, sentence, true)
	if err != nil || !started {
		t.Fatalf("regenerate: err=%v started=%t", err, started)
	}
	affected, err = ReconcileOrphanRuns(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("reconciled = %d, want 0", affected)
	}
	stillRunning, err := analysis.NewHistoryStore(db).GetRun(ctx, library.ULID(fresh.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if stillRunning.Status != "running" {
		t.Fatalf("fresh run status = %q", stillRunning.Status)
	}
}

func TestPayloadValidation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	envelope, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	job, err := jobs.NewStore(db).Get(ctx, library.ULID(envelope.JobID))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodeJobPayload([]byte(job.PayloadJSON))
	if err != nil {
		t.Fatal(err)
	}

	mutants := map[string]func(*JobPayload){
		"wrong transport stage": func(p *JobPayload) {
			p.Binding.StageID = pipeline.StageLinguisticAnalysis
		},
		"wrong snapshot type": func(p *JobPayload) {
			p.PromptSnapshots[0].Type = "explore"
		},
		"single snapshot": func(p *JobPayload) {
			p.PromptSnapshots = p.PromptSnapshots[:1]
		},
		"bad envelope version": func(p *JobPayload) {
			p.PromptSnapshots[1].EnvelopeVersion = "doublangu.prompt-envelope.v1"
		},
		"hash mismatch": func(p *JobPayload) {
			p.Input.ParagraphText += " extra context"
		},
		"sentence mismatch": func(p *JobPayload) {
			p.SentenceID = library.NewULID().String()
		},
	}
	for name, mutate := range mutants {
		clone := payload
		clone.PromptSnapshots = append([]pipeline.PromptSnapshot(nil), payload.PromptSnapshots...)
		clone.Input = payload.Input
		mutate(&clone)
		if err := clone.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}

	tampered := payload
	tampered.PromptSnapshots = append([]pipeline.PromptSnapshot(nil), payload.PromptSnapshots...)
	tampered.PromptSnapshots[0].InstructionText += " extra"
	if _, err := sentenceStagePrompts(tampered); err == nil {
		t.Fatal("tampered instruction bytes must fail snapshot verification")
	}

	raw := []byte(job.PayloadJSON)
	if _, err := DecodeJobPayload(append(raw, ' ', '{', '}')); err == nil {
		t.Fatal("trailing JSON must fail")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["unknown_field"] = true
	doctored, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJobPayload(doctored); err == nil {
		t.Fatal("unknown fields must fail")
	}
	if !strings.Contains(CodeProviderUnavailable, "v1.sentence_") {
		t.Fatal("sentence error codes must use the sentence namespace")
	}
}

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
	"doublangu/internal/dictionary"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

// --- scripted provider and registry ------------------------------------------

type sentenceScriptedProvider struct {
	descriptor annotator.ProviderDescriptor
	turns      []string
	// dynamic ignores canned turns and answers each prompt with a valid
	// sentence.translation.v1 document for the SOURCE block in that prompt.
	dynamic  bool
	mu       sync.Mutex
	sessions int
}

func (p *sentenceScriptedProvider) Descriptor() annotator.ProviderDescriptor {
	return p.descriptor
}

func (p *sentenceScriptedProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return nil, nil
}

func (p *sentenceScriptedProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	p.mu.Lock()
	p.sessions++
	p.mu.Unlock()
	return &sentenceScriptedSession{provider: p}, nil
}

type sentenceScriptedSession struct{ provider *sentenceScriptedProvider }

func (s *sentenceScriptedSession) Turn(_ context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	if s.provider.dynamic {
		input, err := sentenceInputFromPrompt(request.Prompt)
		if err != nil {
			return annotator.Completion{}, err
		}
		return annotator.Completion{Text: validSentenceResponse(input), ReportedModel: "sentence-model"}, nil
	}
	if len(s.provider.turns) == 0 {
		return annotator.Completion{}, errors.New("no canned turn")
	}
	text := s.provider.turns[0]
	s.provider.turns = s.provider.turns[1:]
	return annotator.Completion{Text: text, ReportedModel: "sentence-model"}, nil
}

func (s *sentenceScriptedSession) Close() error { return nil }

type sentenceFakeRegistry struct{ provider annotator.Provider }

func (r *sentenceFakeRegistry) Provider(id string) (annotator.Provider, bool) {
	if r.provider == nil || r.provider.Descriptor().ID != id {
		return nil, false
	}
	return r.provider, true
}

func newSentenceFakeProvider(turns ...string) *sentenceScriptedProvider {
	return &sentenceScriptedProvider{
		descriptor: annotator.ProviderDescriptor{
			ID: "fake-provider", Type: annotator.ProviderTypeCodexAppServer,
			Enabled: true, ConfigFingerprint: "fp-1",
		},
		turns: turns,
	}
}

// sentenceInputFromPrompt extracts the quoted SOURCE block from a sentence
// prompt, mirroring the model-visible contract.
func sentenceInputFromPrompt(prompt string) (annotator.SentenceTranslationInput, error) {
	begin := strings.Index(prompt, "SOURCE_BEGIN\n")
	end := strings.Index(prompt, "\nSOURCE_END")
	if begin < 0 || end < 0 || end < begin {
		return annotator.SentenceTranslationInput{}, errors.New("prompt has no SOURCE block")
	}
	var input annotator.SentenceTranslationInput
	if err := json.Unmarshal([]byte(prompt[begin+len("SOURCE_BEGIN\n"):end]), &input); err != nil {
		return annotator.SentenceTranslationInput{}, err
	}
	return input, nil
}

func validSentenceResponse(input annotator.SentenceTranslationInput) string {
	document := annotator.SentenceTranslationDocument{
		Version: input.Version, SentenceID: input.SentenceID, SourceHash: input.SourceHash,
		TranslationEN: "EN: " + input.SourceText,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func drainSentenceRunner(t *testing.T, db *store.DB, runner *Runner, provider *sentenceScriptedProvider, turns ...string) {
	t.Helper()
	ctx := context.Background()
	provider.mu.Lock()
	provider.dynamic = false
	provider.turns = append([]string{}, turns...)
	provider.mu.Unlock()
	for {
		err := runner.RunOnce(ctx)
		if errors.Is(err, jobs.ErrNoWork) {
			return
		}
		if err != nil {
			t.Fatalf("runner: %v", err)
		}
	}
}

// --- runner behavior ----------------------------------------------------------

func TestRunnerPublishesTranslation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	envelope, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}

	provider := &sentenceScriptedProvider{descriptor: newSentenceFakeProvider("").descriptor, dynamic: true}
	runner := NewRunner(db, &sentenceFakeRegistry{provider: provider})
	runner.heartbeatInterval = 0
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("runner: %v", err)
	}

	ready, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != StatusReady || ready.Translation == nil || *ready.Translation != "EN: Zij zit op een bank." {
		t.Fatalf("ready = %+v", ready)
	}
	if ready.GenerationStatus != GenerationIdle || ready.ErrorCode != "" {
		t.Fatalf("generation = %+v", ready)
	}

	stored, err := NewStore(db).Get(ctx, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResultHash == "" || stored.ResultHash != sentenceResultHash("EN: Zij zit op een bank.") {
		t.Fatalf("result hash = %+v", stored)
	}
	if stored.ProvenanceJSON == "" || strings.Contains(stored.ProvenanceJSON, "endpoint") || strings.Contains(stored.ProvenanceJSON, "secret") {
		t.Fatalf("provenance = %q", stored.ProvenanceJSON)
	}
	if stored.LastJobID == nil || *stored.LastJobID != envelope.JobID || stored.LastRunID == nil || *stored.LastRunID != envelope.RunID {
		t.Fatalf("pointers = %+v", stored)
	}

	run, err := analysis.NewHistoryStore(db).GetRun(ctx, library.ULID(envelope.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" {
		t.Fatalf("run status = %q", run.Status)
	}

	// A later ensure reuses the saved translation without provider work.
	reuse, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || started {
		t.Fatalf("reuse: err=%v started=%t", err, started)
	}
	if reuse.Status != StatusReady || reuse.Translation == nil || *reuse.Translation != "EN: Zij zit op een bank." {
		t.Fatalf("reuse = %+v", reuse)
	}
	var jobCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE job_type = ?`, JobType).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("job count = %d, want 1", jobCount)
	}
}

func TestRunnerFailureNeedsExplicitRegenerateAndKeepsOldText(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	// Fail the initial generation terminally: three invalid responses
	// exhaust the initial turn plus two corrections.
	provider := newSentenceFakeProvider()
	runner := NewRunner(db, &sentenceFakeRegistry{provider: provider})
	runner.heartbeatInterval = 0
	if _, started, err := service.Ensure(ctx, article, sentence, false); err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	drainSentenceRunner(t, db, runner, provider, `{"broken`, `{"broken`, `{"broken`)

	failed, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.ErrorCode != CodeInvalidOutput || failed.Translation != nil {
		t.Fatalf("failed = %+v", failed)
	}
	// Hovering again does not retry: no new job, same run.
	again, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || started {
		t.Fatalf("repeat: err=%v started=%t", err, started)
	}
	if again.RunID != failed.RunID || again.Status != StatusFailed {
		t.Fatalf("repeat = %+v", again)
	}

	// Publish one success, then fail a regeneration: the old text survives.
	dynamic := &sentenceScriptedProvider{descriptor: newSentenceFakeProvider("").descriptor, dynamic: true}
	runner2 := NewRunner(db, &sentenceFakeRegistry{provider: dynamic})
	runner2.heartbeatInterval = 0
	regenerated, started, err := service.Ensure(ctx, article, sentence, true)
	if err != nil || !started {
		t.Fatalf("regenerate: err=%v started=%t", err, started)
	}
	if regenerated.RunID == failed.RunID {
		t.Fatal("regenerate must claim a fresh run")
	}
	if err := runner2.RunOnce(ctx); err != nil {
		t.Fatalf("runner2: %v", err)
	}
	ready, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != StatusReady || ready.Translation == nil {
		t.Fatalf("ready = %+v", ready)
	}

	brokenRegen, started, err := service.Ensure(ctx, article, sentence, true)
	if err != nil || !started {
		t.Fatalf("broken regenerate: err=%v started=%t", err, started)
	}
	drainSentenceRunner(t, db, runner, provider, `{"broken`, `{"broken`, `{"broken`)
	kept, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Status != StatusReady || kept.Translation == nil || *kept.Translation != *ready.Translation {
		t.Fatalf("kept = %+v", kept)
	}
	if kept.GenerationStatus != GenerationFailed || kept.ErrorCode != CodeInvalidOutput {
		t.Fatalf("failed regeneration must stay visible: %+v", kept)
	}
	if kept.JobID != brokenRegen.JobID || kept.RunID != brokenRegen.RunID {
		t.Fatalf("pointers must name the failed attempt: %+v vs %+v", kept, brokenRegen)
	}
}

func TestRunnerStaleAnchorNeverPublishes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	if _, started, err := service.Ensure(ctx, article, sentence, false); err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}

	// The anchor is recreated with different source text before the worker
	// runs: the stale job must not publish under the new anchor.
	if _, err := db.Exec(ctx, `UPDATE article_sentence SET source_text = 'Zij staat op een bank.', source_hash = ? WHERE id = ?`,
		sourceHash("Zij staat op een bank."), sentence.String()); err != nil {
		t.Fatal(err)
	}
	provider := &sentenceScriptedProvider{descriptor: newSentenceFakeProvider("").descriptor, dynamic: true}
	runner := NewRunner(db, &sentenceFakeRegistry{provider: provider})
	runner.heartbeatInterval = 0
	if err := runner.RunOnce(ctx); err == nil {
		t.Fatal("stale publish must fail")
	}
	stored, err := NewStore(db).Get(ctx, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TranslationText != nil {
		t.Fatalf("no translation may be stored under the new anchor: %+v", stored)
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM job WHERE job_type = ?`, JobType).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == jobs.StateSucceeded {
		t.Fatal("a stale job must never complete successfully")
	}
}

func TestRunnerSurvivesRestartWithEvidence(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	envelope, started, err := service.Ensure(ctx, article, sentence, false)
	if err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}

	// A fresh runner over the same database (as after a process restart)
	// picks the queued job up and completes it with full evidence.
	provider := &sentenceScriptedProvider{descriptor: newSentenceFakeProvider("").descriptor, dynamic: true}
	restarted := NewRunner(db, &sentenceFakeRegistry{provider: provider})
	restarted.heartbeatInterval = 0
	if err := restarted.RunOnce(ctx); err != nil {
		t.Fatalf("restarted runner: %v", err)
	}
	ready, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != StatusReady || ready.Translation == nil {
		t.Fatalf("ready = %+v", ready)
	}
	run, err := analysis.NewHistoryStore(db).GetRun(ctx, library.ULID(envelope.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" {
		t.Fatalf("run status = %q", run.Status)
	}
	var turns int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_stage_turn WHERE stage_attempt_id IN (SELECT id FROM analysis_stage_attempt WHERE run_id = ?)`, envelope.RunID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns == 0 {
		t.Fatal("restarted work must retain its turn evidence")
	}
}

func TestSentenceRunnerIgnoresOtherJobTypes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	foreign, err := jobs.NewStore(db).Enqueue(ctx, jobs.Spec{
		JobType: jobs.DictionaryJobType, ExecutionTarget: jobs.TargetServer,
		OwnerType: "dictionary_entry", OwnerID: library.NewULID().String(),
		IdempotencyKey: "test-foreign-" + library.NewULID().String(),
		InputHash:      "hash",
		PayloadJSON:    `{"contract_version":"reader.dictionary.v1"}`,
		MaxAttempts:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(db, &sentenceFakeRegistry{})
	runner.heartbeatInterval = 0
	if err := runner.RunOnce(ctx); !errors.Is(err, jobs.ErrNoWork) {
		t.Fatalf("sentence runner must ignore dictionary jobs, err = %v", err)
	}
	stillQueued, err := jobs.NewStore(db).Get(ctx, foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillQueued.State != jobs.StateQueued {
		t.Fatalf("foreign job state = %q", stillQueued.State)
	}

	// The reverse direction: a queued sentence job is not dictionary work.
	// Remove the foreign job first so the dictionary runner has only the
	// sentence job to (correctly) ignore.
	if _, err := db.Exec(ctx, `DELETE FROM job WHERE id = ?`, foreign.ID.String()); err != nil {
		t.Fatal(err)
	}
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")
	if _, started, err := service.Ensure(ctx, article, sentence, false); err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	dictionaryRunner := dictionary.NewRunner(db, &sentenceFakeRegistry{})
	if err := dictionaryRunner.RunOnce(ctx); !errors.Is(err, jobs.ErrNoWork) {
		t.Fatalf("dictionary runner must ignore sentence jobs, err = %v", err)
	}
}

func TestRunnerProviderChangeFailsExplicitly(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	article, sentence := sentenceSubject(t, db, "Zij zit op een bank.", "Zij zit op een bank.")
	service, _ := resolvingSentenceService(t, db, "fp-1")

	if _, started, err := service.Ensure(ctx, article, sentence, false); err != nil || !started {
		t.Fatalf("ensure: err=%v started=%t", err, started)
	}
	changed := newSentenceFakeProvider("")
	changed.descriptor.ConfigFingerprint = "fp-2"
	runner := NewRunner(db, &sentenceFakeRegistry{provider: changed})
	runner.heartbeatInterval = 0
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("runner: %v", err)
	}
	failed, err := service.Lookup(ctx, article, sentence)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.ErrorCode != CodeProviderChanged {
		t.Fatalf("failed = %+v", failed)
	}
	if failed.Translation != nil {
		t.Fatalf("no translation may be stored on preflight failure: %+v", failed)
	}
}

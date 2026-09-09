package dictionary

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// --- fixtures ---------------------------------------------------------------

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func fixtureArticle(t *testing.T, db *store.DB, id, title string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, ?, 'nl', 'en', 'ready')`, id, title)
}

func fixtureBlock(t *testing.T, db *store.DB, id, articleID, text string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, 0, 'paragraph', ?)`, id, articleID, text)
}

func fixtureSense(t *testing.T, db *store.DB, kind semantics.Kind, canonical, lemma, discriminator, translation string) *semantics.Sense {
	t.Helper()
	var sense *semantics.Sense
	err := db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		var err error
		sense, err = semantics.EnsureSenseTx(context.Background(), tx, "nl", "en", semantics.NewSense{
			Kind: kind, CanonicalForm: canonical, NormalizedForm: canonical, Lemma: lemma,
			SenseDiscriminator: discriminator, PrimaryTranslation: translation,
		}, "test-provider", "test-model")
		return err
	})
	if err != nil {
		t.Fatalf("ensure sense %s: %v", canonical, err)
	}
	return sense
}

func fixtureWordOccurrence(t *testing.T, db *store.DB, id, blockID, text, senseID string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article_occurrence (id, article_block_id, semantic_sense_id, kind, role, shadow_policy, shadow_text) VALUES (?, ?, ?, 'word', 'token', 'token', ?)`, id, blockID, nullable(senseID), text)
	mustExec(t, db, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, 0, 0, ?, ?)`, "span-"+id, id, len([]rune(text)), text)
}

func fixtureConstruction(t *testing.T, db *store.DB, id, blockID string, spans []string, memberIDs []string, senseID string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article_occurrence (id, article_block_id, semantic_sense_id, kind, role, shadow_policy, shadow_text) VALUES (?, ?, ?, 'idiom', 'contiguous_construction', 'group', ?)`, id, blockID, nullable(senseID), strings.Join(spans, " "))
	for index, span := range spans {
		mustExec(t, db, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, ?, ?, ?, ?)`,
			"span-"+id+"-"+string(rune('0'+index)), id, index, index*10, index*10+len([]rune(span)), span)
	}
	for index, member := range memberIDs {
		mustExec(t, db, `INSERT INTO article_construction_member (construction_occurrence_id, token_occurrence_id, member_index) VALUES (?, ?, ?)`, id, member, index)
	}
}

func fixtureAnnotation(t *testing.T, db *store.DB, id, blockID, text, kind string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article_annotation (id, article_block_id, start_utf16, end_utf16, source_text, kind, learning_key, primary_translation, suggest_shadow) VALUES (?, ?, 0, ?, ?, ?, 'key-'+?, 'vertaling', 1)`,
		id, blockID, len([]rune(text)), text, kind, id)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// testBinding builds a validated fake translation binding snapshot.
func testBinding(t *testing.T, fingerprint string) pipeline.BindingSnapshot {
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
		ProviderConfigFingerprint: fingerprint, ModelID: "dict-model", Options: options, OptionsHash: hash,
		ContractVersion: pipeline.TranslationContractVersion, PromptVersion: pipeline.TranslationPromptVersion,
	}
}

func validRunnerResponse(input semantics.DictionaryInput) string {
	document := semantics.DictionaryDocument{
		Version: input.Version, LookupForm: input.LookupForm, LookupKind: input.LookupKind,
		SourceLanguage: input.SourceLanguage, TargetLanguage: input.TargetLanguage,
		Senses: []semantics.DictionarySense{{
			PartOfSpeech: "verb", TranslationEN: "to throw down",
			MeaningEN: "To throw something down forcefully.",
			UsageEN:   "", PatternNL: "", Parts: []semantics.DictionaryPart{},
			Examples: []semantics.DictionaryExample{{TextNL: "Hij gooit de bal neer.", TranslationEN: "He throws the ball down."}},
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// --- scripted provider and registry ------------------------------------------

type scriptedProvider struct {
	descriptor annotator.ProviderDescriptor
	turns      []string
	// dynamic ignores canned turns and answers each prompt with a valid
	// dictionary document for the INPUT_DATA found in that prompt.
	dynamic  bool
	mu       sync.Mutex
	sessions int
}

func (p *scriptedProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }

func (p *scriptedProvider) ListModels(context.Context) ([]annotator.Model, error) { return nil, nil }

func (p *scriptedProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	p.mu.Lock()
	p.sessions++
	p.mu.Unlock()
	return &scriptedSession{provider: p}, nil
}

func (p *scriptedProvider) sessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions
}

type scriptedSession struct{ provider *scriptedProvider }

func (s *scriptedSession) Turn(_ context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	if s.provider.dynamic {
		input, err := inputFromPrompt(request.Prompt)
		if err != nil {
			return annotator.Completion{}, err
		}
		return annotator.Completion{Text: validRunnerResponse(input), ReportedModel: "dict-model"}, nil
	}
	if len(s.provider.turns) == 0 {
		return annotator.Completion{}, errors.New("no canned turn")
	}
	text := s.provider.turns[0]
	s.provider.turns = s.provider.turns[1:]
	return annotator.Completion{Text: text, ReportedModel: "dict-model"}, nil
}

// inputFromPrompt extracts the quoted INPUT_DATA block from a dictionary
// prompt, mirroring the model-visible contract.
func inputFromPrompt(prompt string) (semantics.DictionaryInput, error) {
	begin := strings.Index(prompt, "INPUT_DATA_BEGIN\n")
	end := strings.Index(prompt, "\nINPUT_DATA_END")
	if begin < 0 || end < 0 || end < begin {
		return semantics.DictionaryInput{}, errors.New("prompt has no INPUT_DATA block")
	}
	var input semantics.DictionaryInput
	if err := json.Unmarshal([]byte(prompt[begin+len("INPUT_DATA_BEGIN\n"):end]), &input); err != nil {
		return semantics.DictionaryInput{}, err
	}
	return input, nil
}

func (s *scriptedSession) Close() error { return nil }

type fakeRegistry struct{ provider annotator.Provider }

func (r *fakeRegistry) Provider(id string) (annotator.Provider, bool) {
	if r.provider == nil || r.provider.Descriptor().ID != id {
		return nil, false
	}
	return r.provider, true
}

func newFakeProvider(turns ...string) *scriptedProvider {
	return &scriptedProvider{
		descriptor: annotator.ProviderDescriptor{
			ID: "fake-provider", Type: annotator.ProviderTypeCodexAppServer,
			Enabled: true, ConfigFingerprint: "fp-1",
		},
		turns: turns,
	}
}

func resolvingService(t *testing.T, db *store.DB, fingerprint string) *Service {
	t.Helper()
	binding := testBinding(t, fingerprint)
	return NewService(db, func(context.Context) (ResolvedExploreGeneration, error) {
		return ResolvedExploreGeneration{
			Binding: binding,
			PromptSnapshots: []pipeline.PromptSnapshot{
				testPromptSnapshot(t, "explore", "Explore generation instruction."),
				testPromptSnapshot(t, "correction", "Explore correction instruction."),
			},
			ProfileID:   "profile-explore",
			ProfileName: "Explore profile",
		}, nil
	})
}

// testPromptSnapshot builds an internally consistent captured snapshot.
func testPromptSnapshot(t *testing.T, promptType, instruction string) pipeline.PromptSnapshot {
	t.Helper()
	return pipeline.PromptSnapshot{
		Type: promptType, ID: "snap-" + promptType, Version: 1,
		ContentHash: prompts.ContentHashOf(instruction), InstructionText: instruction,
		EnvelopeVersion: pipeline.PromptCapturedEnvelopeVersion,
	}
}

// --- subject resolution -------------------------------------------------------

func TestResolveSubjects(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "Ik gooit het bijltje erbij neergooien.")

	sense := fixtureSense(t, db, semantics.KindWord, "neergooien", "neergooien", "throw down", "throw down")
	idiomSense := fixtureSense(t, db, semantics.KindIdiom, "het bijltje erbij neergooien", "", "give up", "give up")

	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "neergooien", sense.ID.String())
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD2", "01J00000000000000000000BLK1", "42", "")
	fixtureConstruction(t, db, "01J00000000000000000000CNS1", "01J00000000000000000000BLK1",
		[]string{"het", "bijltje", "erbij", "neergooien"},
		[]string{"01J00000000000000000000WRD1"}, idiomSense.ID.String())
	fixtureAnnotation(t, db, "01J00000000000000000000ANN1", "01J00000000000000000000BLK1", "bank", "word")

	store := NewStore(db)
	articleID := library.ULID("01J00000000000000000000ART1")

	t.Run("word sense lemma", func(t *testing.T) {
		subject, err := store.ResolveArticleOccurrence(ctx, articleID, library.ULID("01J00000000000000000000WRD1"))
		if err != nil {
			t.Fatal(err)
		}
		if subject.LookupKind != semantics.DictionaryLookupWord || subject.NormalizedForm != "neergooien" {
			t.Fatalf("subject = %+v", subject)
		}
	})
	t.Run("expression canonical identity differs from member word", func(t *testing.T) {
		subject, err := store.ResolveArticleOccurrence(ctx, articleID, library.ULID("01J00000000000000000000CNS1"))
		if err != nil {
			t.Fatal(err)
		}
		if subject.LookupKind != semantics.DictionaryLookupExpression {
			t.Fatalf("kind = %q", subject.LookupKind)
		}
		if subject.NormalizedForm != "het bijltje erbij neergooien" {
			t.Fatalf("normalized = %q", subject.NormalizedForm)
		}
	})
	t.Run("construction span fallback without sense", func(t *testing.T) {
		mustExec(t, db, `UPDATE article_occurrence SET semantic_sense_id = NULL WHERE id = '01J00000000000000000000CNS1'`)
		subject, err := store.ResolveArticleOccurrence(ctx, articleID, library.ULID("01J00000000000000000000CNS1"))
		if err != nil {
			t.Fatal(err)
		}
		if subject.LookupForm != "het bijltje erbij neergooien" {
			t.Fatalf("fallback form = %q", subject.LookupForm)
		}
	})
	t.Run("annotation exact text", func(t *testing.T) {
		subject, err := store.ResolveArticleAnnotation(ctx, articleID, library.ULID("01J00000000000000000000ANN1"))
		if err != nil {
			t.Fatal(err)
		}
		if subject.LookupKind != semantics.DictionaryLookupWord || subject.LookupForm != "bank" {
			t.Fatalf("subject = %+v", subject)
		}
	})
	t.Run("nonlexical span rejected", func(t *testing.T) {
		if _, err := store.ResolveArticleOccurrence(ctx, articleID, library.ULID("01J00000000000000000000WRD2")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("cross-article reference rejected", func(t *testing.T) {
		fixtureArticle(t, db, "01J00000000000000000000ART2", "Other")
		if _, err := store.ResolveArticleOccurrence(ctx, library.ULID("01J00000000000000000000ART2"), library.ULID("01J00000000000000000000WRD1")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("unsupported language pair rejected", func(t *testing.T) {
		mustExec(t, db, `UPDATE article SET source_language = 'fr' WHERE id = '01J00000000000000000000ART2'`)
		fixtureBlock(t, db, "01J00000000000000000000BLK2", "01J00000000000000000000ART2", "Bonjour.")
		fixtureWordOccurrence(t, db, "01J00000000000000000000WRD3", "01J00000000000000000000BLK2", "bonjour", "")
		if _, err := store.ResolveArticleOccurrence(ctx, library.ULID("01J00000000000000000000ART2"), library.ULID("01J00000000000000000000WRD3")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// --- explicit start and reuse -------------------------------------------------

func TestServiceStartAndReuse(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")
	articleID := library.ULID("01J00000000000000000000ART1")
	ref := Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}

	t.Run("lookup without entry is missing and writes nothing", func(t *testing.T) {
		service := resolvingService(t, db, "fp-1")
		envelope, _, err := service.Lookup(ctx, articleID, ref)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Status != StatusMissing {
			t.Fatalf("status = %q", envelope.Status)
		}
		if envelope.Subject == nil || envelope.Subject.LookupForm != "bank" {
			t.Fatalf("subject = %+v", envelope.Subject)
		}
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM dictionary_entry`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("GET must not create a placeholder")
		}
	})

	t.Run("start queues one job and concurrent start joins it", func(t *testing.T) {
		service := resolvingService(t, db, "fp-1")
		envelope, _, started, err := service.Start(ctx, articleID, ref, false, false)
		if err != nil || !started {
			t.Fatalf("start: err=%v started=%t", err, started)
		}
		if envelope.Status != StatusQueued || envelope.JobID == "" {
			t.Fatalf("envelope = %+v", envelope)
		}
		again, _, startedAgain, err := service.Start(ctx, articleID, ref, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if startedAgain {
			t.Fatal("second start must join the active job")
		}
		if again.JobID != envelope.JobID {
			t.Fatalf("job ids differ: %q vs %q", again.JobID, envelope.JobID)
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
		if job.MaxAttempts != 1 || job.ExecutionTarget != jobs.TargetServer || job.OwnerType != OwnerType {
			t.Fatalf("job spec = %+v", job)
		}
		if _, err := DecodeJobPayload([]byte(job.PayloadJSON)); err != nil {
			t.Fatalf("payload decode: %v", err)
		}
	})

	t.Run("provider unavailable mutates nothing", func(t *testing.T) {
		second := fixtureFreshArticleRef(t, db, "01J00000000000000000000ART3", "01J00000000000000000000BLK3", "01J00000000000000000000WRD3", "hok")
		service := NewService(db, func(context.Context) (ResolvedExploreGeneration, error) {
			return ResolvedExploreGeneration{}, errors.New("no usable profile")
		})
		if _, _, _, err := service.Start(ctx, second.articleID, second.ref, false, false); !errors.Is(err, ErrProviderUnavailable) {
			t.Fatalf("err = %v", err)
		}
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE job_type = ? AND owner_id = '01J00000000000000000000WRD3'`, JobType).Scan(&count); err != nil {
			t.Fatal(err)
		}
		// No dictionary job may exist for the failed lookup subject.
		var entryCount int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM dictionary_entry WHERE normalized_lookup_form = 'hok'`).Scan(&entryCount); err != nil {
			t.Fatal(err)
		}
		if entryCount != 0 || count != 0 {
			t.Fatalf("entries=%d jobs=%d; setup failure must precede queue mutation", entryCount, count)
		}
	})

	t.Run("failed entry needs explicit retry", func(t *testing.T) {
		article, block, word := "01J00000000000000000000ART4", "01J00000000000000000000BLK4", "01J00000000000000000000WRD4"
		fixtureArticle(t, db, article, "Fixture 4")
		fixtureBlock(t, db, block, article, "Het plan.")
		fixtureWordOccurrence(t, db, word, block, "plan", "")
		service := resolvingService(t, db, "fp-1")
		// The worker fails terminally without publishing: three invalid
		// responses exhaust the initial turn plus two corrections.
		provider := newFakeProvider(`{"broken`, `{"broken`, `{"broken`)
		runner := NewRunner(db, &fakeRegistry{provider: provider})
		runner.heartbeatInterval = 0
		drain := func(turns ...string) {
			t.Helper()
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
		drain(`{"broken`, `{"broken`, `{"broken`) // any leftover queued jobs fail terminally
		if _, _, started, err := service.Start(ctx, library.ULID(article), Reference{OccurrenceID: library.ULID(word)}, false, false); err != nil || !started {
			t.Fatalf("start: err=%v started=%t", err, started)
		}
		drain(`{"broken`, `{"broken`, `{"broken`)
		envelope, _, err := service.Lookup(ctx, library.ULID(article), Reference{OccurrenceID: library.ULID(word)})
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Status != StatusFailed || envelope.ErrorCode != CodeInvalidOutput {
			t.Fatalf("envelope = %+v", envelope)
		}
		// Lookup without retry does not requeue.
		if _, _, started, err := service.Start(ctx, library.ULID(article), Reference{OccurrenceID: library.ULID(word)}, false, false); err != nil || started {
			t.Fatalf("non-retry start: err=%v started=%t", err, started)
		}
		// Explicit retry queues a new job.
		envelope, _, started, err := service.Start(ctx, library.ULID(article), Reference{OccurrenceID: library.ULID(word)}, true, false)
		if err != nil || !started {
			t.Fatalf("retry: err=%v started=%t", err, started)
		}
		if envelope.Status != StatusQueued {
			t.Fatalf("retry envelope = %+v", envelope)
		}
	})

	t.Run("ready entry never overwritten by retry", func(t *testing.T) {
		service := resolvingService(t, db, "fp-1")
		// Finish the retried job from the previous subtest with a valid document.
		provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
		runner := NewRunner(db, &fakeRegistry{provider: provider})
		runner.heartbeatInterval = 0
		for {
			err := runner.RunOnce(ctx)
			if errors.Is(err, jobs.ErrNoWork) {
				break
			}
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
		}
		envelope, _, err := service.Lookup(ctx, library.ULID("01J00000000000000000000ART4"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD4")})
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Status != StatusReady || envelope.Document == nil || len(envelope.Document.Senses) != 1 {
			t.Fatalf("envelope = %+v", envelope)
		}
		if _, _, started, err := service.Start(ctx, library.ULID("01J00000000000000000000ART4"), Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD4")}, true, false); err != nil || started {
			t.Fatalf("retry on ready: err=%v started=%t", err, started)
		}
	})
}

func TestServiceReadyReuseAcrossArticlesAndRestart(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/dictionary-reuse.db"
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixtureArticle(t, db, "01J00000000000000000000ART1", "First")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD1", "01J00000000000000000000BLK1", "bank", "")
	fixtureArticle(t, db, "01J00000000000000000000ART2", "Second")
	fixtureBlock(t, db, "01J00000000000000000000BLK2", "01J00000000000000000000ART2", "De bank staat daar.")
	fixtureWordOccurrence(t, db, "01J00000000000000000000WRD2", "01J00000000000000000000BLK2", "bank", "")

	provider := &scriptedProvider{descriptor: newFakeProvider("").descriptor, dynamic: true}
	registry := &fakeRegistry{provider: provider}
	runner := NewRunner(db, registry)
	runner.heartbeatInterval = 0
	service := NewService(db, func(context.Context) (ResolvedExploreGeneration, error) {
		return ResolvedExploreGeneration{Binding: testBinding(t, "fp-1"), PromptSnapshots: []pipeline.PromptSnapshot{
			testPromptSnapshot(t, "explore", "Explore generation instruction."),
			testPromptSnapshot(t, "correction", "Explore correction instruction."),
		}}, nil
	})

	first := library.ULID("01J00000000000000000000ART1")
	second := library.ULID("01J00000000000000000000ART2")
	ref1 := Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD1")}
	ref2 := Reference{OccurrenceID: library.ULID("01J00000000000000000000WRD2")}

	// Both articles resolve to one key; the second start joins the first job.
	envelope1, _, started1, err := service.Start(ctx, first, ref1, false, false)
	if err != nil || !started1 {
		t.Fatalf("start 1: err=%v started=%t", err, started1)
	}
	envelope2, _, started2, err := service.Start(ctx, second, ref2, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if started2 || envelope2.EntryID != envelope1.EntryID || envelope2.JobID != envelope1.JobID {
		t.Fatalf("second start must join: %+v vs %+v", envelope2, envelope1)
	}
	for {
		err := runner.RunOnce(ctx)
		if errors.Is(err, jobs.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatalf("runner: %v", err)
		}
	}
	ready1, _, err := service.Lookup(ctx, first, ref1)
	if err != nil {
		t.Fatal(err)
	}
	if ready1.Status != StatusReady || ready1.Document == nil || len(ready1.Document.Senses) != 1 {
		t.Fatalf("envelope 1 = %+v", ready1)
	}
	sessionsBefore := provider.sessionCount()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the saved document is reused with zero provider activity.
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := NewService(reopened, func(context.Context) (ResolvedExploreGeneration, error) {
		t.Error("binding must not be resolved for a ready entry")
		return ResolvedExploreGeneration{Binding: testBinding(t, "fp-changed"), PromptSnapshots: []pipeline.PromptSnapshot{
			testPromptSnapshot(t, "explore", "Explore generation instruction."),
			testPromptSnapshot(t, "correction", "Explore correction instruction."),
		}}, nil
	})
	afterRestart, _, err := restarted.Lookup(ctx, first, ref1)
	if err != nil {
		t.Fatal(err)
	}
	afterRestartJSON, _ := json.Marshal(afterRestart.Document)
	beforeJSON, _ := json.Marshal(ready1.Document)
	if string(afterRestartJSON) != string(beforeJSON) {
		t.Fatalf("document changed across restart:\n%s\n%s", beforeJSON, afterRestartJSON)
	}
	// The second article reads the same shared entry without generation.
	shared, _, err := restarted.Lookup(ctx, second, ref2)
	if err != nil {
		t.Fatal(err)
	}
	if shared.EntryID != afterRestart.EntryID || shared.Status != StatusReady {
		t.Fatalf("shared envelope = %+v", shared)
	}
	// An explicit start on a ready entry also resolves without a provider.
	if _, _, started, err := restarted.Start(ctx, second, ref2, false, false); err != nil || started {
		t.Fatalf("start on ready: err=%v started=%t", err, started)
	}
	if provider.sessionCount() != sessionsBefore {
		t.Fatalf("provider sessions = %d, want %d (no generation after commit)", provider.sessionCount(), sessionsBefore)
	}
}

type freshRef struct {
	articleID library.ULID
	ref       Reference
}

func fixtureFreshArticleRef(t *testing.T, db *store.DB, articleID, blockID, wordID, word string) freshRef {
	t.Helper()
	fixtureArticle(t, db, articleID, "Fixture "+word)
	fixtureBlock(t, db, blockID, articleID, word)
	fixtureWordOccurrence(t, db, wordID, blockID, word, "")
	return freshRef{articleID: library.ULID(articleID), ref: Reference{OccurrenceID: library.ULID(wordID)}}
}

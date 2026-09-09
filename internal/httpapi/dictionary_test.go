package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/analysis"
	"doublangu/internal/annotator"
	"doublangu/internal/dictionary"
	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

type dictionaryEnvelope struct {
	Status              string `json:"status"`
	EntryID             string `json:"entry_id"`
	JobID               string `json:"job_id"`
	RunID               string `json:"run_id"`
	GenerationStatus    string `json:"generation_status"`
	GenerationErrorCode string `json:"generation_error_code"`
	ErrorCode           string `json:"error_code"`
	Subject             *struct {
		LookupForm     string `json:"lookup_form"`
		LookupKind     string `json:"lookup_kind"`
		SourceLanguage string `json:"source_language"`
		TargetLanguage string `json:"target_language"`
	} `json:"subject"`
	Document *struct {
		Version    string `json:"version"`
		LookupForm string `json:"lookup_form"`
		Senses     []struct {
			PartOfSpeech  string `json:"part_of_speech"`
			TranslationEN string `json:"translation_en"`
		} `json:"senses"`
	} `json:"document"`
}

type dictionaryFixture struct {
	articleID    string
	annotationID string
	occurrenceID string
	freshArticle string
	freshAnnotID string
}

func dictionaryFixtureDB(t *testing.T) (*store.DB, dictionaryFixture) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fix := dictionaryFixture{
		articleID:    library.NewULID().String(),
		annotationID: library.NewULID().String(),
		occurrenceID: library.NewULID().String(),
		freshArticle: library.NewULID().String(),
		freshAnnotID: library.NewULID().String(),
	}
	mustExecDictionary(t, db, "INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES ('"+fix.articleID+"', 'Fixture', 'nl', 'en', 'ready')")
	mustExecDictionary(t, db, "INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('"+library.NewULID().String()+"', '"+fix.articleID+"', 0, 'paragraph', 'De bank.')")
	mustExecDictionary(t, db, "INSERT INTO article_occurrence (id, article_block_id, kind, role, shadow_policy, shadow_text) VALUES ('"+fix.occurrenceID+"', (SELECT id FROM article_block LIMIT 1), 'word', 'token', 'token', 'bank')")
	mustExecDictionary(t, db, "INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES ('"+library.NewULID().String()+"', '"+fix.occurrenceID+"', 0, 0, 4, 'bank')")
	mustExecDictionary(t, db, "INSERT INTO article_annotation (id, article_block_id, start_utf16, end_utf16, source_text, kind, learning_key, primary_translation, suggest_shadow) VALUES ('"+fix.annotationID+"', (SELECT id FROM article_block WHERE source_text = 'De bank.'), 0, 4, 'bank', 'word', 'key-1', 'vertaling', 1)")
	return db, fix
}

func mustExecDictionary(t *testing.T, db *store.DB, query string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// dictionaryEchoProvider answers every turn with a valid document for the
// INPUT_DATA found in the prompt, mirroring the real model contract.
type dictionaryEchoProvider struct {
	descriptor annotator.ProviderDescriptor
	mu         sync.Mutex
	sessions   int
}

func (p *dictionaryEchoProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *dictionaryEchoProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return []annotator.Model{{ID: "model-a", DisplayName: "Model A", SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "low"}}}}, nil
}
func (p *dictionaryEchoProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	p.mu.Lock()
	p.sessions++
	p.mu.Unlock()
	return &dictionaryEchoSession{}, nil
}
func (p *dictionaryEchoProvider) sessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions
}

type dictionaryEchoSession struct{}

func (s *dictionaryEchoSession) Turn(_ context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	begin := strings.Index(request.Prompt, "INPUT_DATA_BEGIN\n")
	end := strings.Index(request.Prompt, "\nINPUT_DATA_END")
	if begin < 0 || end < 0 {
		return annotator.Completion{}, errors.New("no INPUT_DATA block")
	}
	var input semantics.DictionaryInput
	if err := json.Unmarshal([]byte(request.Prompt[begin+len("INPUT_DATA_BEGIN\n"):end]), &input); err != nil {
		return annotator.Completion{}, err
	}
	input.KnownTranslations = nil
	raw, err := json.Marshal(semantics.DictionaryDocument{
		Version: input.Version, LookupForm: input.LookupForm, LookupKind: input.LookupKind,
		SourceLanguage: input.SourceLanguage, TargetLanguage: input.TargetLanguage,
		Senses: []semantics.DictionarySense{{
			PartOfSpeech: "noun", TranslationEN: "bench",
			MeaningEN: "A long seat for several people.", UsageEN: "", PatternNL: "",
			Parts:    []semantics.DictionaryPart{},
			Examples: []semantics.DictionaryExample{{TextNL: "Zij zit op een bank.", TranslationEN: "She is sitting on a bench."}},
		}},
	})
	if err != nil {
		return annotator.Completion{}, err
	}
	return annotator.Completion{Text: string(raw), ReportedModel: "model-a"}, nil
}
func (s *dictionaryEchoSession) Close() error { return nil }

// newDictionaryHarness returns a handler whose provider both serves the
// catalog and executes dictionary turns.
func newDictionaryHarness(t *testing.T, db *store.DB) (*httpapi.DictionaryHandler, *dictionaryEchoProvider) {
	t.Helper()
	provider := &dictionaryEchoProvider{descriptor: codexDescriptor()}
	registry := &catalogAndSessionsRegistry{
		descriptors: []annotator.ProviderDescriptor{provider.descriptor},
		providers:   map[string]annotator.Provider{provider.descriptor.ID: provider},
	}
	catalog := httpapi.NewProviderCatalogService(registry)
	handler := httpapi.NewDictionaryHandler(db, allowArticleCSRF{}, registry, catalog)
	return handler, provider
}

type catalogAndSessionsRegistry struct {
	descriptors []annotator.ProviderDescriptor
	providers   map[string]annotator.Provider
}

func (r *catalogAndSessionsRegistry) Provider(id string) (annotator.Provider, bool) {
	provider, ok := r.providers[id]
	return provider, ok
}

func (r *catalogAndSessionsRegistry) Descriptors() []annotator.ProviderDescriptor {
	return r.descriptors
}

func activateDictionaryProfile(t *testing.T, db *store.DB) {
	t.Helper()
	bindings := []pipeline.BindingSnapshot{apiBinding(pipeline.StageLinguisticAnalysis), apiBinding(pipeline.StageTranslation)}
	profiles := analysis.NewProfileStore(db)
	profile, err := profiles.Create(context.Background(), "Dictionary Fixture", bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Activate(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDictionaryExploreLookupAndValidation(t *testing.T) {
	db, fix := dictionaryFixtureDB(t)
	handler, _ := newDictionaryHarness(t, db)

	t.Run("GET missing entry resolves subject without writing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?occurrence_id="+fix.occurrenceID, "", "id", fix.articleID))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d cache=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
		envelope := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if envelope.Status != "missing" || envelope.EntryID != "" {
			t.Fatalf("envelope = %+v", envelope)
		}
		if envelope.Subject == nil || envelope.Subject.LookupForm != "bank" || envelope.Subject.LookupKind != "word" {
			t.Fatalf("subject = %+v", envelope.Subject)
		}
	})

	t.Run("GET via annotation reference", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?annotation_id="+fix.annotationID, "", "id", fix.articleID))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		envelope := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if envelope.Status != "missing" || envelope.Subject == nil || envelope.Subject.LookupForm != "bank" {
			t.Fatalf("envelope = %+v", envelope)
		}
	})

	t.Run("reference validation", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore", "", "id", fix.articleID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("no ref status = %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?occurrence_id="+fix.occurrenceID+"&annotation_id="+fix.annotationID, "", "id", fix.articleID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("both refs status = %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+library.NewULID().String()+"/explore?occurrence_id="+fix.occurrenceID, "", "id", library.NewULID().String()))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("bad article status = %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?occurrence_id=NOTAULID", "", "id", fix.articleID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad ulid status = %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodDelete, "/api/v1/articles/"+fix.articleID+"/explore", "", "id", fix.articleID))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("delete status = %d", rec.Code)
		}
	})
}

func TestDictionaryExploreStartQueuesJob(t *testing.T) {
	db, fix := dictionaryFixtureDB(t)
	handler, _ := newDictionaryHarness(t, db)
	activateDictionaryProfile(t, db)

	t.Run("POST requires retry flag", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
			`{"occurrence_id":"`+fix.occurrenceID+`"}`, "id", fix.articleID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("missing retry status = %d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("POST rejects unknown properties", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
			`{"occurrence_id":"`+fix.occurrenceID+`","retry":false,"headword":"bank"}`, "id", fix.articleID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unknown property status = %d", rec.Code)
		}
	})

	t.Run("POST queues and returns 202; second click joins", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
			`{"occurrence_id":"`+fix.occurrenceID+`","retry":false}`, "id", fix.articleID))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
		}
		envelope := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if envelope.Status != "queued" || envelope.JobID == "" {
			t.Fatalf("envelope = %+v", envelope)
		}
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
			`{"occurrence_id":"`+fix.occurrenceID+`","retry":false}`, "id", fix.articleID))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("join status = %d", rec.Code)
		}
		again := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if again.JobID != envelope.JobID || again.EntryID != envelope.EntryID {
			t.Fatalf("join envelope = %+v, want %+v", again, envelope)
		}
		var jobCount int
		if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM job WHERE job_type = 'reader.dictionary.v1'`).Scan(&jobCount); err != nil {
			t.Fatal(err)
		}
		if jobCount != 1 {
			t.Fatalf("job count = %d", jobCount)
		}
	})

	t.Run("entry endpoint reads the shared record without the article", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeEntry(rec, authedRequest(http.MethodGet, "/api/v1/dictionary/entries/UNKNOWN", ""))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("bad id status = %d", rec.Code)
		}
		var entryID string
		if err := db.QueryRow(context.Background(), `SELECT id FROM dictionary_entry LIMIT 1`).Scan(&entryID); err != nil {
			t.Fatal(err)
		}
		rec = httptest.NewRecorder()
		handler.ServeEntry(rec, authedRequest(http.MethodGet, "/api/v1/dictionary/entries/"+entryID, "", "id", entryID))
		if rec.Code != http.StatusOK {
			t.Fatalf("entry status = %d body=%s", rec.Code, rec.Body.String())
		}
		envelope := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if envelope.Status != "queued" {
			t.Fatalf("entry envelope = %+v", envelope)
		}
	})

	t.Run("POST without usable binding returns 503 and mutates nothing", func(t *testing.T) {
		mustExecDictionary(t, db, "INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES ('"+fix.freshArticle+"', 'Fresh', 'nl', 'en', 'ready')")
		mustExecDictionary(t, db, "INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('"+library.NewULID().String()+"', '"+fix.freshArticle+"', 0, 'paragraph', 'Het hok.')")
		mustExecDictionary(t, db, "INSERT INTO article_annotation (id, article_block_id, start_utf16, end_utf16, source_text, kind, learning_key, primary_translation, suggest_shadow) VALUES ('"+fix.freshAnnotID+"', (SELECT id FROM article_block WHERE source_text = 'Het hok.'), 0, 3, 'hok', 'word', 'key-9', 'vertaling', 1)")
		mustExecDictionary(t, db, `DELETE FROM analysis_pipeline_settings`)
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.freshArticle+"/explore",
			`{"annotation_id":"`+fix.freshAnnotID+`","retry":false}`, "id", fix.freshArticle))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "v1.dictionary_provider_unavailable") {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var count int
		if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM dictionary_entry WHERE normalized_lookup_form = 'hok'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("setup failure must precede queue mutation")
		}
	})
}

// TestDictionaryExploreHandlerToRunnerToReady is the thinnest full slice:
// the HTTP handler enqueues, the real dictionary runner publishes, and the
// article-free entry endpoint serves the ready document.
func TestDictionaryExploreHandlerToRunnerToReady(t *testing.T) {
	db, fix := dictionaryFixtureDB(t)
	handler, provider := newDictionaryHarness(t, db)
	activateDictionaryProfile(t, db)

	rec := httptest.NewRecorder()
	handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
		`{"occurrence_id":"`+fix.occurrenceID+`","retry":false}`, "id", fix.articleID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	envelope := decodeJSON[dictionaryEnvelope](t, rec.Body.String())

	// The production registry shape satisfies the runner seam.
	runner := dictionary.NewRunner(db, &catalogAndSessionsRegistry{
		descriptors: []annotator.ProviderDescriptor{provider.descriptor},
		providers:   map[string]annotator.Provider{provider.descriptor.ID: provider},
	})
	runner.SetHeartbeatInterval(0)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("runner: %v", err)
	}

	// Ready through the article surface...
	rec = httptest.NewRecorder()
	handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?occurrence_id="+fix.occurrenceID, "", "id", fix.articleID))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d body=%s", rec.Code, rec.Body.String())
	}
	ready := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
	if ready.Status != "ready" || ready.Document == nil || len(ready.Document.Senses) != 1 {
		t.Fatalf("ready envelope = %+v", ready)
	}
	// ...and through the entry surface, with no further generation.
	sessionsBefore := provider.sessionCount()
	rec = httptest.NewRecorder()
	handler.ServeEntry(rec, authedRequest(http.MethodGet, "/api/v1/dictionary/entries/"+envelope.EntryID, "", "id", envelope.EntryID))
	if rec.Code != http.StatusOK {
		t.Fatalf("entry ready status = %d", rec.Code)
	}
	entry := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
	if entry.Status != "ready" || entry.Document == nil {
		t.Fatalf("entry envelope = %+v", entry)
	}
	if provider.sessionCount() != sessionsBefore {
		t.Fatal("ready reads must not open provider sessions")
	}
	// A ready entry is never overwritten, even with retry=true.
	rec = httptest.NewRecorder()
	handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
		`{"occurrence_id":"`+fix.occurrenceID+`","retry":true}`, "id", fix.articleID))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry on ready status = %d", rec.Code)
	}
	if provider.sessionCount() != sessionsBefore {
		t.Fatal("retry on ready must not generate")
	}
}

// TestDictionaryExploreRegenerate covers the checkpoint 10 HTTP surface:
// retry+regenerate ambiguity rejects, regenerate on a ready entry starts a
// fresh job while the old document stays readable, and the generation fields
// track the attempt beside the retained result.
func TestDictionaryExploreRegenerate(t *testing.T) {
	db, fix := dictionaryFixtureDB(t)
	handler, provider := newDictionaryHarness(t, db)
	activateDictionaryProfile(t, db)

	t.Run("retry plus regenerate is ambiguous", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
			`{"occurrence_id":"`+fix.occurrenceID+`","retry":true,"regenerate":true}`, "id", fix.articleID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous status = %d body=%s", rec.Code, rec.Body.String())
		}
	})

	// Enqueue, run the worker to ready, then regenerate.
	rec := httptest.NewRecorder()
	handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
		`{"occurrence_id":"`+fix.occurrenceID+`","retry":false}`, "id", fix.articleID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	first := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
	if first.GenerationStatus != "queued" {
		t.Fatalf("queued generation status = %+v", first)
	}
	runner := dictionary.NewRunner(db, &catalogAndSessionsRegistry{
		descriptors: []annotator.ProviderDescriptor{provider.descriptor},
		providers:   map[string]annotator.Provider{provider.descriptor.ID: provider},
	})
	runner.SetHeartbeatInterval(0)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("runner: %v", err)
	}

	t.Run("regenerate keeps the old document readable", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodPost, "/api/v1/articles/"+fix.articleID+"/explore",
			`{"occurrence_id":"`+fix.occurrenceID+`","retry":false,"regenerate":true}`, "id", fix.articleID))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("regenerate status = %d body=%s", rec.Code, rec.Body.String())
		}
		regen := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if regen.JobID == "" || regen.JobID == first.JobID {
			t.Fatalf("regenerate must start a fresh job, got %+v", regen)
		}
		if regen.Status != "ready" || regen.Document == nil {
			t.Fatalf("old document must stay readable, got %+v", regen)
		}
		if regen.GenerationStatus != "queued" && regen.GenerationStatus != "running" {
			t.Fatalf("generation must track the fresh attempt, got %+v", regen)
		}
		// The saved result stays pollable while the replacement runs.
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?occurrence_id="+fix.occurrenceID, "", "id", fix.articleID))
		if rec.Code != http.StatusOK {
			t.Fatalf("poll status = %d body=%s", rec.Code, rec.Body.String())
		}
		poll := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if poll.Status != "ready" || poll.Document == nil || poll.JobID != regen.JobID {
			t.Fatalf("poll envelope = %+v, want %+v", poll, regen)
		}
		if err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("runner: %v", err)
		}
		rec = httptest.NewRecorder()
		handler.ServeExplore(rec, authedRequest(http.MethodGet, "/api/v1/articles/"+fix.articleID+"/explore?occurrence_id="+fix.occurrenceID, "", "id", fix.articleID))
		settled := decodeJSON[dictionaryEnvelope](t, rec.Body.String())
		if settled.Status != "ready" || settled.GenerationStatus != "idle" || settled.Document == nil {
			t.Fatalf("settled envelope = %+v", settled)
		}
		var jobCount int
		if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM job WHERE job_type = 'reader.dictionary.v1'`).Scan(&jobCount); err != nil {
			t.Fatal(err)
		}
		if jobCount != 2 {
			t.Fatalf("job count = %d", jobCount)
		}
	})
}

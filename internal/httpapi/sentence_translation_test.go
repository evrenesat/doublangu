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

	"doublangu/internal/annotator"
	"doublangu/internal/httpapi"
	"doublangu/internal/library"
	"doublangu/internal/sentencetranslation"
	"doublangu/internal/store"
)

type sentenceEnvelope struct {
	Status           string  `json:"status"`
	SentenceID       string  `json:"sentence_id"`
	Translation      *string `json:"translation"`
	JobID            string  `json:"job_id"`
	RunID            string  `json:"run_id"`
	GenerationStatus string  `json:"generation_status"`
	ErrorCode        string  `json:"error_code"`
	ErrorSummary     string  `json:"error_summary"`
}

type sentenceFixture struct {
	articleID  string
	blockID    string
	sentenceID string
	sourceHash string
}

const sentenceFixtureHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func sentenceFixtureDB(t *testing.T) (*store.DB, sentenceFixture) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fix := sentenceFixture{
		articleID:  library.NewULID().String(),
		blockID:    library.NewULID().String(),
		sentenceID: library.NewULID().String(),
		sourceHash: sentenceFixtureHash,
	}
	mustExecDictionary(t, db, "INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES ('"+fix.articleID+"', 'Fixture', 'nl', 'en', 'ready')")
	mustExecDictionary(t, db, "INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('"+fix.blockID+"', '"+fix.articleID+"', 0, 'paragraph', 'De kat zit op de mat.')")
	mustExecDictionary(t, db, "INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES ('"+fix.sentenceID+"', '"+fix.blockID+"', 0, 0, 22, 'De kat zit op de mat.', '"+fix.sourceHash+"')")
	return db, fix
}

type denySentenceCSRF struct{}

func (denySentenceCSRF) VerifyRequest(*http.Request) error { return errors.New("denied") }

// sentenceEchoProvider answers every turn with a valid translation for the
// SOURCE block found in the prompt, mirroring the real model contract.
type sentenceEchoProvider struct {
	descriptor annotator.ProviderDescriptor
	mu         sync.Mutex
	sessions   int
}

func (p *sentenceEchoProvider) Descriptor() annotator.ProviderDescriptor { return p.descriptor }
func (p *sentenceEchoProvider) ListModels(context.Context) ([]annotator.Model, error) {
	return []annotator.Model{{ID: "model-a", DisplayName: "Model A", SupportedReasoningEfforts: []annotator.ReasoningEffort{{Value: "low"}}}}, nil
}
func (p *sentenceEchoProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	p.mu.Lock()
	p.sessions++
	p.mu.Unlock()
	return &sentenceEchoSession{}, nil
}
func (p *sentenceEchoProvider) sessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions
}

type sentenceEchoSession struct{}

func (s *sentenceEchoSession) Turn(_ context.Context, request annotator.TurnRequest) (annotator.Completion, error) {
	begin := strings.Index(request.Prompt, "SOURCE_BEGIN\n")
	end := strings.Index(request.Prompt, "\nSOURCE_END")
	if begin < 0 || end < 0 || end < begin {
		return annotator.Completion{}, errors.New("no SOURCE block")
	}
	var input annotator.SentenceTranslationInput
	if err := json.Unmarshal([]byte(request.Prompt[begin+len("SOURCE_BEGIN\n"):end]), &input); err != nil {
		return annotator.Completion{}, err
	}
	raw, err := json.Marshal(annotator.SentenceTranslationDocument{
		Version: input.Version, SentenceID: input.SentenceID, SourceHash: input.SourceHash,
		TranslationEN: "The cat sits on the mat.",
	})
	if err != nil {
		return annotator.Completion{}, err
	}
	return annotator.Completion{Text: string(raw), ReportedModel: "model-a"}, nil
}
func (s *sentenceEchoSession) Close() error { return nil }

// newSentenceHarness returns a handler whose provider both serves the
// catalog and executes sentence turns.
func newSentenceHarness(t *testing.T, db *store.DB, csrf httpapi.CSRFVerifier) (*httpapi.SentenceHandler, *sentenceEchoProvider) {
	t.Helper()
	provider := &sentenceEchoProvider{descriptor: codexDescriptor()}
	registry := &catalogAndSessionsRegistry{
		descriptors: []annotator.ProviderDescriptor{provider.descriptor},
		providers:   map[string]annotator.Provider{provider.descriptor.ID: provider},
	}
	catalog := httpapi.NewProviderCatalogService(registry)
	handler := httpapi.NewSentenceHandler(db, csrf, registry, catalog)
	return handler, provider
}

func sentencePath(fix sentenceFixture) string {
	return "/api/v1/articles/" + fix.articleID + "/sentences/" + fix.sentenceID + "/translation"
}

func sentenceRequest(method string, fix sentenceFixture, body string) *http.Request {
	return authedRequest(method, sentencePath(fix), body, "id", fix.articleID, "sentence_id", fix.sentenceID)
}

func sentenceJobCount(t *testing.T, db *store.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM job WHERE job_type = 'reader.sentence_translation.v1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSentenceTranslationReadNeedsNoProvider(t *testing.T) {
	db, fix := sentenceFixtureDB(t)
	// No profile is activated: any provider resolution would fail, so a
	// successful read proves the read path never touches a provider.
	handler, _ := newSentenceHarness(t, db, allowArticleCSRF{})

	rec := httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodGet, fix, ""))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	envelope := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if envelope.Status != "missing" || envelope.SentenceID != fix.sentenceID || envelope.Translation != nil {
		t.Fatalf("envelope = %+v", envelope)
	}

	// A saved translation also reads without any profile or provider.
	mustExecDictionary(t, db, "INSERT INTO sentence_translation (sentence_id, source_hash, target_language, translation_text) VALUES ('"+fix.sentenceID+"', '"+fix.sourceHash+"', 'en', 'The cat sits on the mat.')")
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodGet, fix, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d body=%s", rec.Code, rec.Body.String())
	}
	ready := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if ready.Status != "ready" || ready.Translation == nil || *ready.Translation != "The cat sits on the mat." {
		t.Fatalf("ready envelope = %+v", ready)
	}
}

func TestSentenceTranslationRequestValidation(t *testing.T) {
	db, fix := sentenceFixtureDB(t)
	handler, _ := newSentenceHarness(t, db, allowArticleCSRF{})
	activateDictionaryProfile(t, db)

	cases := []struct {
		name   string
		method string
		target *http.Request
		want   int
	}{
		{"mode is required", http.MethodPost, sentenceRequest(http.MethodPost, fix, `{}`), http.StatusBadRequest},
		{"mode must be known", http.MethodPost, sentenceRequest(http.MethodPost, fix, `{"mode":"retry"}`), http.StatusBadRequest},
		{"unknown properties reject", http.MethodPost, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure","prompt":"hallo"}`), http.StatusBadRequest},
		{"method not allowed", http.MethodDelete, sentenceRequest(http.MethodDelete, fix, ""), http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeTranslation(rec, tc.target)
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("bad sentence id is not found", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeTranslation(rec, authedRequest(http.MethodGet, sentencePath(fix), "", "id", fix.articleID, "sentence_id", "NOTAULID"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("cross-article sentence cannot trigger generation", func(t *testing.T) {
		otherArticle := library.NewULID().String()
		rec := httptest.NewRecorder()
		handler.ServeTranslation(rec, authedRequest(http.MethodPost, sentencePath(fix), `{"mode":"ensure"}`, "id", otherArticle, "sentence_id", fix.sentenceID))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if got := sentenceJobCount(t, db); got != 0 {
			t.Fatalf("job count = %d", got)
		}
		var runs int
		if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM analysis_run`).Scan(&runs); err != nil {
			t.Fatal(err)
		}
		if runs != 0 {
			t.Fatalf("run count = %d", runs)
		}
	})

	t.Run("POST requires CSRF", func(t *testing.T) {
		denied, _ := newSentenceHarness(t, db, denySentenceCSRF{})
		rec := httptest.NewRecorder()
		denied.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if got := sentenceJobCount(t, db); got != 0 {
			t.Fatalf("job count = %d", got)
		}
	})
}

func TestSentenceTranslationEnsureDedupes(t *testing.T) {
	db, fix := sentenceFixtureDB(t)
	handler, _ := newSentenceHarness(t, db, allowArticleCSRF{})
	activateDictionaryProfile(t, db)

	rec := httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	first := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if first.Status != "queued" || first.JobID == "" || first.RunID == "" || first.GenerationStatus != "queued" {
		t.Fatalf("envelope = %+v", first)
	}

	// A concurrent hover converges on the same run and job.
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("join status = %d body=%s", rec.Code, rec.Body.String())
	}
	again := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if again.JobID != first.JobID || again.RunID != first.RunID {
		t.Fatalf("join envelope = %+v, want %+v", again, first)
	}
	if got := sentenceJobCount(t, db); got != 1 {
		t.Fatalf("job count = %d", got)
	}

	// Polling observes the active generation.
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodGet, fix, ""))
	poll := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if poll.Status != "queued" || poll.JobID != first.JobID || poll.RunID != first.RunID {
		t.Fatalf("poll envelope = %+v, want %+v", poll, first)
	}
}

func TestSentenceTranslationReadyAndRegenerate(t *testing.T) {
	db, fix := sentenceFixtureDB(t)
	handler, provider := newSentenceHarness(t, db, allowArticleCSRF{})
	activateDictionaryProfile(t, db)

	rec := httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	first := decodeJSON[sentenceEnvelope](t, rec.Body.String())

	runner := sentencetranslation.NewRunner(db, &catalogAndSessionsRegistry{
		descriptors: []annotator.ProviderDescriptor{provider.descriptor},
		providers:   map[string]annotator.Provider{provider.descriptor.ID: provider},
	})
	runner.SetHeartbeatInterval(0)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("runner: %v", err)
	}

	sessionsBefore := provider.sessionCount()
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodGet, fix, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d body=%s", rec.Code, rec.Body.String())
	}
	ready := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if ready.Status != "ready" || ready.Translation == nil || *ready.Translation != "The cat sits on the mat." {
		t.Fatalf("ready envelope = %+v", ready)
	}
	if provider.sessionCount() != sessionsBefore {
		t.Fatal("ready reads must not open provider sessions")
	}

	// A saved result returns 200 without new work, even with regenerate off.
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("ensure on ready status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := sentenceJobCount(t, db); got != 1 {
		t.Fatalf("job count = %d", got)
	}
	if provider.sessionCount() != sessionsBefore {
		t.Fatal("ensure on ready must not generate")
	}

	// Regenerate always starts a fresh request and keeps the old text.
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"regenerate"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("regenerate status = %d body=%s", rec.Code, rec.Body.String())
	}
	regen := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if regen.JobID == "" || regen.JobID == first.JobID {
		t.Fatalf("regenerate must start a fresh job, got %+v", regen)
	}
	if regen.Translation == nil || *regen.Translation != "The cat sits on the mat." {
		t.Fatalf("old text must stay readable, got %+v", regen)
	}
	if regen.GenerationStatus != "queued" && regen.GenerationStatus != "running" {
		t.Fatalf("generation must track the fresh attempt, got %+v", regen)
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("runner: %v", err)
	}
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodGet, fix, ""))
	settled := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if settled.Status != "ready" || settled.GenerationStatus != "idle" || settled.RunID == first.RunID {
		t.Fatalf("settled envelope = %+v", settled)
	}
	if got := sentenceJobCount(t, db); got != 2 {
		t.Fatalf("job count = %d", got)
	}
}

func TestSentenceTranslationPreflightFailureIsRetained(t *testing.T) {
	db, fix := sentenceFixtureDB(t)
	// No usable Translation binding: every POST fails preflight.
	handler, _ := newSentenceHarness(t, db, allowArticleCSRF{})

	rec := httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), sentencetranslation.CodeProviderUnavailable) {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := sentenceJobCount(t, db); got != 0 {
		t.Fatalf("job count = %d", got)
	}

	// The failed preflight claim stays visible: reads converge on the
	// retained failed run instead of retrying after reload.
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodGet, fix, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("poll status = %d body=%s", rec.Code, rec.Body.String())
	}
	failed := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if failed.Status != "failed" || failed.RunID == "" || failed.ErrorCode != sentencetranslation.CodeProviderUnavailable {
		t.Fatalf("failed envelope = %+v", failed)
	}

	// A repeated hover observes the retained failure instead of retrying:
	// no job is ever enqueued and the same run is returned.
	rec = httptest.NewRecorder()
	handler.ServeTranslation(rec, sentenceRequest(http.MethodPost, fix, `{"mode":"ensure"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("repeated status = %d body=%s", rec.Code, rec.Body.String())
	}
	again := decodeJSON[sentenceEnvelope](t, rec.Body.String())
	if again.Status != "failed" || again.RunID != failed.RunID {
		t.Fatalf("repeated envelope = %+v, want %+v", again, failed)
	}
	if got := sentenceJobCount(t, db); got != 0 {
		t.Fatalf("job count = %d", got)
	}
}

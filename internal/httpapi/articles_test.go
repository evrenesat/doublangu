package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/httpapi"
	"doublangu/internal/reader"
	"doublangu/internal/store"
)

type fakeArticleAnnotator struct {
	mu         sync.Mutex
	candidates []annotator.Candidate
	err        error
	started    chan struct{}
	release    chan struct{}
}

type allowArticleCSRF struct{}

func (allowArticleCSRF) VerifyRequest(*http.Request) error { return nil }

func (f *fakeArticleAnnotator) Annotate(ctx context.Context, _ annotator.ArticleInput) ([]annotator.Candidate, error) {
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]annotator.Candidate(nil), f.candidates...), nil
}

func (f *fakeArticleAnnotator) set(candidates []annotator.Candidate, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.candidates = candidates
	f.err = err
}

func newArticleHandler(t *testing.T, provider annotator.Annotator) (*httpapi.ArticleHandler, *store.DB) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return httpapi.NewArticleHandler(db, allowArticleCSRF{}, provider), db
}

func createTestArticle(t *testing.T, h *httpapi.ArticleHandler) reader.Article {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeArticles(response, authedRequest(http.MethodPost, "/api/v1/articles", `{"title":"Rust","body":"Ik wil tot rust komen.\n\nDat helpt.","source_language":"nl","target_language":"en"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	return decodeJSON[reader.Article](t, response.Body.String())
}

func enrichTestArticle(t *testing.T, h *httpapi.ArticleHandler, article reader.Article) *reader.Article {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeEnrich(response, authedRequest(http.MethodPost, "/api/v1/articles/"+article.ID.String()+"/enrich", "", "id", article.ID.String()))
	if response.Code != http.StatusOK {
		t.Fatalf("enrich status = %d, body = %s", response.Code, response.Body.String())
	}
	return pointer(decodeJSON[reader.Article](t, response.Body.String()))
}

func pointer[T any](value T) *T { return &value }

func TestArticleHTTPCreateEnrichAndLearningState(t *testing.T) {
	provider := &fakeArticleAnnotator{}
	provider.set([]annotator.Candidate{{
		BlockIndex:         0,
		SourceText:         "tot rust komen",
		Occurrence:         0,
		Kind:               reader.KindExpression,
		LearningKey:        "TOT RUST KOMEN",
		PrimaryTranslation: "to calm down",
		Alternatives:       []string{"to settle down"},
		MeaningNote:        "To become calm.",
		SuggestShadow:      true,
	}}, nil)
	h, _ := newArticleHandler(t, provider)
	article := createTestArticle(t, h)
	if len(article.Blocks) != 2 || article.Blocks[0].SourceText != "Ik wil tot rust komen." || article.EnrichmentStatus != reader.StatusDraft {
		t.Fatalf("created article = %+v", article)
	}

	ready := enrichTestArticle(t, h, article)
	annotation := ready.Blocks[0].Annotations[0]
	if ready.EnrichmentStatus != reader.StatusReady || annotation.SourceText != "tot rust komen" || !annotation.ShowShadow || annotation.LearningState != nil {
		t.Fatalf("enriched article = %+v", ready)
	}

	stateResponse := httptest.NewRecorder()
	h.ServeLearningState(stateResponse, authedRequest(http.MethodPut, "/api/v1/learning-state", `{"source_language":"NL","kind":"expression","learning_key":" tot\trust komen ","status":"learned"}`))
	if stateResponse.Code != http.StatusOK {
		t.Fatalf("learning state status = %d, body = %s", stateResponse.Code, stateResponse.Body.String())
	}
	state := decodeJSON[reader.LearningState](t, stateResponse.Body.String())
	if state.SourceLanguage != "nl" || state.LearningKey != "tot rust komen" || state.Status != reader.LearningStatusLearned {
		t.Fatalf("stored learning state = %+v", state)
	}

	getResponse := httptest.NewRecorder()
	h.ServeArticle(getResponse, authedRequest(http.MethodGet, "/api/v1/articles/"+article.ID.String(), ""))
	got := decodeJSON[reader.Article](t, getResponse.Body.String())
	if getResponse.Code != http.StatusOK || got.Blocks[0].Annotations[0].LearningState == nil || got.Blocks[0].Annotations[0].ShowShadow {
		t.Fatalf("learned article response = %+v, status=%d", got, getResponse.Code)
	}

	listResponse := httptest.NewRecorder()
	h.ServeArticles(listResponse, authedRequest(http.MethodGet, "/api/v1/articles", ""))
	list := decodeJSON[[]reader.ArticleSummary](t, listResponse.Body.String())
	if listResponse.Code != http.StatusOK || len(list) != 1 || list[0].ID != article.ID {
		t.Fatalf("article list = %+v, status=%d", list, listResponse.Code)
	}
}

func TestArticleHTTPFailurePreservesAnnotationsAndCanRetry(t *testing.T) {
	provider := &fakeArticleAnnotator{}
	provider.set([]annotator.Candidate{{
		BlockIndex: 0, SourceText: "woord", Occurrence: 0, Kind: reader.KindWord,
		LearningKey: "woord", PrimaryTranslation: "word", SuggestShadow: true,
	}}, nil)
	h, _ := newArticleHandler(t, provider)
	article := createTestArticleWithBody(t, h, "Een woord.")
	if ready := enrichTestArticle(t, h, article); len(ready.Blocks[0].Annotations) != 1 {
		t.Fatal("initial annotation was not stored")
	}

	provider.set(nil, &annotator.Error{Code: annotator.CodeProviderFailure, Err: errors.New("fake failure")})
	failure := httptest.NewRecorder()
	h.ServeEnrich(failure, authedRequest(http.MethodPost, "/api/v1/articles/"+article.ID.String()+"/enrich", "", "id", article.ID.String()))
	if failure.Code != http.StatusBadGateway || decodeAPIError(t, failure.Body.String()).Code != annotator.CodeProviderFailure {
		t.Fatalf("failure response = %d %s", failure.Code, failure.Body.String())
	}
	get := httptest.NewRecorder()
	h.ServeArticle(get, authedRequest(http.MethodGet, "/api/v1/articles/"+article.ID.String(), ""))
	failed := decodeJSON[reader.Article](t, get.Body.String())
	if failed.EnrichmentStatus != reader.StatusFailed || failed.EnrichmentErrorCode != annotator.CodeProviderFailure || len(failed.Blocks[0].Annotations) != 1 {
		t.Fatalf("failed article did not preserve good set: %+v", failed)
	}

	provider.set([]annotator.Candidate{{
		BlockIndex: 0, SourceText: "woord", Occurrence: 0, Kind: reader.KindWord,
		LearningKey: "woord", PrimaryTranslation: "word", SuggestShadow: true,
	}}, nil)
	if ready := enrichTestArticle(t, h, article); ready.EnrichmentStatus != reader.StatusReady {
		t.Fatalf("retry status = %q", ready.EnrichmentStatus)
	}
}

func createTestArticleWithBody(t *testing.T, h *httpapi.ArticleHandler, body string) reader.Article {
	t.Helper()
	request := authedRequest(http.MethodPost, "/api/v1/articles", "{\"title\":\"Test\",\"body\":"+quoteJSON(body)+",\"source_language\":\"nl\",\"target_language\":\"en\"}")
	response := httptest.NewRecorder()
	h.ServeArticles(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	return decodeJSON[reader.Article](t, response.Body.String())
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestArticleHTTPRejectsConcurrentEnrichment(t *testing.T) {
	provider := &fakeArticleAnnotator{started: make(chan struct{}, 1), release: make(chan struct{})}
	provider.set([]annotator.Candidate{{
		BlockIndex: 0, SourceText: "woord", Occurrence: 0, Kind: reader.KindWord,
		LearningKey: "woord", PrimaryTranslation: "word", SuggestShadow: true,
	}}, nil)
	h, _ := newArticleHandler(t, provider)
	article := createTestArticleWithBody(t, h, "Een woord.")

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		h.ServeEnrich(response, authedRequest(http.MethodPost, "/api/v1/articles/"+article.ID.String()+"/enrich", "", "id", article.ID.String()))
		firstDone <- response
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("first enrichment did not reach the provider")
	}
	second := httptest.NewRecorder()
	h.ServeEnrich(second, authedRequest(http.MethodPost, "/api/v1/articles/"+article.ID.String()+"/enrich", "", "id", article.ID.String()))
	if second.Code != http.StatusConflict || decodeAPIError(t, second.Body.String()).Code != httpapi.ErrCodeEnrichmentInProgress {
		t.Fatalf("duplicate response = %d %s", second.Code, second.Body.String())
	}
	close(provider.release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first response = %d %s", first.Code, first.Body.String())
	}
}

func TestArticleHTTPRequiresCSRFForMutations(t *testing.T) {
	provider := &fakeArticleAnnotator{}
	h, _ := newArticleHandler(t, provider)
	csrf := &testCSRF{shouldError: true}
	// Construct a handler with the failing verifier while retaining the same
	// database contract; no request body should be decoded before this check.
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h = httpapi.NewArticleHandler(db, csrf, provider)
	response := httptest.NewRecorder()
	h.ServeArticles(response, authedRequest(http.MethodPost, "/api/v1/articles", "{"))
	if response.Code != http.StatusForbidden || decodeAPIError(t, response.Body.String()).Code != httpapi.ErrCodeCSRF {
		t.Fatalf("csrf response = %d %s", response.Code, response.Body.String())
	}
}

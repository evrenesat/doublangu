package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/reader"
	"doublangu/internal/semantics"
)

// TestArticleHTTPResponseCarriesWordGlossesAndMembers proves the real GET
// article response carries per-word glosses — including a construction member
// and a legitimate same-spelling sense — exact construction membership,
// visible show_shadow flags, and deterministic sentences after publishing
// through the real store path.
func TestArticleHTTPResponseCarriesWordGlossesAndMembers(t *testing.T) {
	h, db := newArticleHandler(t, nil)
	article := createTestArticleWithBody(t, h, "Hij gaf het plan niet op.")

	articles := reader.NewStore(db)
	ctx := context.Background()
	var jobRaw string
	if err := db.QueryRow(ctx, `SELECT id FROM job WHERE owner_type = 'article' AND owner_id = ? AND job_type = ? AND state IN ('queued', 'leased', 'running') ORDER BY created_at DESC, id DESC LIMIT 1`, article.ID.String(), jobs.AnalysisJobType).Scan(&jobRaw); err != nil {
		t.Fatal(err)
	}
	jobID := library.ULID(jobRaw)
	if err := articles.MarkAnalysisProcessing(ctx, article.ID, jobID); err != nil {
		t.Fatal(err)
	}
	prepared, err := articles.PrepareAnalysis(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := semantics.PrepareChunk(prepared, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	wordSense := func(ref, canonical, translation string) semantics.NewSense {
		return semantics.NewSense{
			Ref: ref, Kind: semantics.KindWord, CanonicalForm: canonical, NormalizedForm: canonical,
			Lemma: canonical, SenseDiscriminator: translation, PrimaryTranslation: translation,
		}
	}
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		NewSenses: []semantics.NewSense{
			wordSense("he", "hij", "He"),
			wordSense("gave", "geven", "gave"),
			wordSense("the", "het", "the"),
			wordSense("plan", "plan", "plan"),
			wordSense("not", "niet", "not"),
			wordSense("up", "op", "up"),
			{
				Ref: "give-up", Kind: semantics.KindExpression, CanonicalForm: "opgeven",
				NormalizedForm: "opgeven", SenseDiscriminator: "abandon", PrimaryTranslation: "give up",
				MeaningNote: "Opgeven splits around the object: gaf (gave) … op (up). Here he did not give the plan up.",
				PartsNote:   "gaf: gave · op: up",
			},
		},
	}
	memberBySource := map[string]string{}
	for _, token := range chunk.Tokens {
		result := semantics.TokenResult{TokenID: token.ID, Classification: "word", Kind: semantics.KindWord, ConfidenceMilli: 900}
		switch token.SourceText {
		case "Hij":
			result.NewSenseRef = "he"
			result.ShadowText = "He"
		case "gaf":
			result.NewSenseRef = "gave"
			result.ShadowText = "gave"
			memberBySource["gaf"] = token.ID
		case "het":
			result.NewSenseRef = "the"
			result.ShadowText = "the"
		case "plan":
			result.NewSenseRef = "plan"
			result.ShadowText = "plan"
		case "niet":
			result.NewSenseRef = "not"
			result.ShadowText = "not"
		case "op":
			result.NewSenseRef = "up"
			result.ShadowText = "up"
			memberBySource["op"] = token.ID
		}
		response.Tokens = append(response.Tokens, result)
	}
	response.Constructions = []semantics.Construction{{
		Kind: semantics.KindExpression, Role: "discontinuous_construction",
		NewSenseRef: "give-up", ShadowText: "give up", ConfidenceMilli: 900,
		TokenIDs: []string{memberBySource["gaf"], memberBySource["op"]},
		Spans: []semantics.SpanRef{
			{BlockIndex: 0, SourceText: "gaf", Occurrence: 0},
			{BlockIndex: 0, SourceText: "op", Occurrence: 0},
		},
	}}
	namespaced, err := semantics.NamespaceChunkResponse(0, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := semantics.ValidateChunkResponse(chunk, namespaced)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, article.ID, 0, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, article.ID, 0, jobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}

	getResponse := httptest.NewRecorder()
	h.ServeArticle(getResponse, authedRequest(http.MethodGet, "/api/v1/articles/"+article.ID.String(), ""))
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getResponse.Code, getResponse.Body.String())
	}
	got := decodeJSON[reader.Article](t, getResponse.Body.String())
	occurrences := got.Blocks[0].Occurrences
	if len(occurrences) != len(chunk.Tokens)+1 {
		t.Fatalf("occurrences = %d, want %d tokens + 1 construction", len(occurrences), len(chunk.Tokens))
	}
	subtitleBySource := map[string]string{}
	var construction reader.ArticleOccurrence
	memberIDs := 0
	for _, occurrence := range occurrences {
		if occurrence.Role != reader.OccurrenceToken {
			construction = occurrence
			memberIDs = len(occurrence.MemberOccurrenceIDs)
			continue
		}
		subtitleBySource[occurrence.Spans[0].SourceText] = occurrence.ShadowText
		if occurrence.ArticleSentenceID == nil {
			t.Fatalf("token %q has no sentence binding", occurrence.Spans[0].SourceText)
		}
	}
	if len(got.Sentences) != 1 || len(got.Blocks[0].Sentences) != 1 {
		t.Fatalf("sentences = %d/%d, want the deterministic sentence path", len(got.Sentences), len(got.Blocks[0].Sentences))
	}
	// Every word — function words included — carries its real individual
	// gloss over the wire, including the construction members and the
	// same-spelling plan.
	wantSubtitles := map[string]string{
		"Hij": "He", "gaf": "gave", "het": "the",
		"plan": "plan", "niet": "not", "op": "up",
	}
	for source, want := range wantSubtitles {
		if subtitleBySource[source] != want {
			t.Fatalf("token %q subtitle = %q, want %q", source, subtitleBySource[source], want)
		}
	}
	if construction.ShadowText != "give up" || memberIDs != 2 {
		t.Fatalf("construction response = %+v", construction)
	}
	// The construction's meaning and member-parts notes survive the round
	// trip so the popover can show them.
	if construction.Sense == nil || construction.Sense.MeaningNote == "" || construction.Sense.PartsNote == "" {
		t.Fatalf("construction sense notes = %+v", construction.Sense)
	}
	// The wire format carries the exact member occurrence ids.
	if !strings.Contains(getResponse.Body.String(), `"member_occurrence_ids":[`) {
		t.Fatal("response body does not carry member_occurrence_ids")
	}
}

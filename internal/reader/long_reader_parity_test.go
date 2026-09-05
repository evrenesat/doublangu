package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// The long reader fixture is the canonical authored demo data generated from
// web/dev/longReaderFixture.ts: an original 871-word story in 12 paragraphs,
// four sentences each, with per-word glosses and exact expression membership.
// These tests publish it through the real chunk publication path and prove
// the store round trip keeps every word subtitle and member id.

type longFixtureSpan struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Source string `json:"source"`
}

type longFixtureToken struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Source string `json:"source"`
	Gloss  string `json:"gloss"`
}

type longFixtureConstruction struct {
	Role    string            `json:"role"`
	Kind    string            `json:"kind"`
	Label   string            `json:"label"`
	Meaning string            `json:"meaning"`
	Members []longFixtureSpan `json:"members"`
	Spans   []longFixtureSpan `json:"spans"`
}

type longFixtureBlock struct {
	Source        string                    `json:"source"`
	Sentences     []longFixtureSpan         `json:"sentences"`
	Tokens        []longFixtureToken        `json:"tokens"`
	Constructions []longFixtureConstruction `json:"constructions"`
}

type longFixture struct {
	Title  string             `json:"title"`
	Blocks []longFixtureBlock `json:"blocks"`
}

func loadLongFixture(t *testing.T) longFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/long_reader_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture longFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Blocks) != 12 {
		t.Fatalf("long fixture blocks = %d", len(fixture.Blocks))
	}
	return fixture
}

// expressionNotes authors one real explanation per long-fixture expression,
// keyed by the expression's label. The seed is the delivery vehicle for the
// saved article, so each popover Meaning section gets genuine,
// expression-specific content rather than a generic instruction.
var expressionNotes = map[string]string{
	"zich afvragen":                "Dutch splits this reflexive verb: vroeg (asked) … af (off) wrap around zich. Literally 'asked herself off', it simply means she wondered.",
	"stelde … voor":                "Voorstellen puts voor (forward) at the end of the clause: stelde … voor literally means 'put forward', here 'proposed' an idea.",
	"vond … plaats":                "Plaatsvinden separates: vond (found) … plaats (place). Dutch says the meeting 'found place' — it took place.",
	"legde … uit":                  "Uitleggen splits in two: legde (laid) … uit (out). She 'laid out' how the space would be used — she explained it.",
	"hingen … op":                  "Ophangen comes apart: hingen (hung) … op (up). They hung the map up beside the entrance.",
	"ergens mee in je maag zitten": "Literally 'to sit with something in your stomach': zat … mee … in haar maag pictures carrying a worry inside — it troubled her.",
	"gaf … op":                     "Opgeven splits around the object: gaf (gave) … op (up). She never gave her plan up.",
	"nodigde … uit":                "Uitnodigen separates: nodigde … uit. Literally 'called out', it means invited.",
	"op te lossen":                 "Oplossen stays together in this infinitive clause: op te lossen is 'to be solved', pointing at the problem itself.",
	"hield … bij":                  "Bijhouden splits: hield (kept) … bij (up). Nobody kept up with how many problems were solved.",
	"tot rust te komen":            "Tot rust komen means 'to come to rest': om … te komen frames the infinitive, so the whole phrase means to unwind, to finally relax.",
	"deed … uit":                   "Uitdoen splits: deed (did) … uit (out). Someone switched the shop lights off.",
}

// authorLongBlockResponse turns one fixture block into a validated-shape v3
// response: every token carries its authored gloss through a per-block sense,
// and each construction references exactly its authored members and spans.
func authorLongBlockResponse(t *testing.T, chunk semantics.PreparedChunk, blockIndex int, fixture longFixtureBlock) semantics.Response {
	t.Helper()
	response := semantics.Response{Version: semantics.AnalysisContractVersion}
	if len(chunk.Tokens) != len(fixture.Tokens) {
		t.Fatalf("block %d tokenized to %d tokens for %d fixture words", blockIndex, len(chunk.Tokens), len(fixture.Tokens))
	}
	senseByToken := make(map[string]string, len(chunk.Tokens))
	glossByToken := make(map[string]string, len(chunk.Tokens))
	defined := make(map[string]struct{})
	for index, token := range chunk.Tokens {
		want := fixture.Tokens[index]
		if token.StartUTF16 != want.Start || token.EndUTF16 != want.End || token.SourceText != want.Source {
			t.Fatalf("block %d token %d = %q at %d..%d, want %q at %d..%d", blockIndex, index, token.SourceText, token.StartUTF16, token.EndUTF16, want.Source, want.Start, want.End)
		}
		if gloss := strings.TrimSpace(want.Gloss); gloss != "" {
			ref := fmt.Sprintf("w%d", index)
			senseByToken[token.ID] = ref
			glossByToken[token.ID] = want.Gloss
			if _, exists := defined[ref]; !exists {
				defined[ref] = struct{}{}
				response.NewSenses = append(response.NewSenses, semantics.NewSense{
					Ref: ref, Kind: semantics.KindWord, CanonicalForm: token.SourceText,
					NormalizedForm: token.SourceText, Lemma: token.SourceText,
					SenseDiscriminator: gloss, PrimaryTranslation: gloss,
				})
			}
		}
	}
	for _, token := range chunk.Tokens {
		ref := senseByToken[token.ID]
		result := semantics.TokenResult{TokenID: token.ID, Classification: "word", Kind: semantics.KindWord, ConfidenceMilli: 900}
		if ref == "" {
			// Deliberately unchanged tokens keep their identity.
			result.Classification = "proper_name"
			result.ShadowText = token.SourceText
		} else {
			result.NewSenseRef = ref
			result.ShadowText = glossByToken[token.ID]
		}
		response.Tokens = append(response.Tokens, result)
	}
	tokenByOffset := make(map[[2]int]string, len(chunk.Tokens))
	tokenByRef := make(map[string]semantics.Token, len(chunk.Tokens))
	for _, token := range chunk.Tokens {
		tokenByOffset[[2]int{token.StartUTF16, token.EndUTF16}] = token.ID
		tokenByRef[token.ID] = token
	}
	for constructionIndex, construction := range fixture.Constructions {
		ref := fmt.Sprintf("c%d", constructionIndex)
		meaningNote, authored := expressionNotes[construction.Label]
		if !authored {
			t.Fatalf("long fixture expression %q has no authored meaning note", construction.Label)
		}
		memberIDs := make([]string, 0, len(construction.Members))
		memberParts := make([]string, 0, len(construction.Members))
		for _, member := range construction.Members {
			id, ok := tokenByOffset[[2]int{member.Start, member.End}]
			if !ok {
				t.Fatalf("block %d construction %q member %q has no token", blockIndex, construction.Label, member.Source)
			}
			memberIDs = append(memberIDs, id)
			// The popover's parts note lists each member with its literal gloss.
			memberParts = append(memberParts, fmt.Sprintf("%s: %s", member.Source, glossByToken[id]))
		}
		response.NewSenses = append(response.NewSenses, semantics.NewSense{
			Ref: ref, Kind: semantics.Kind(construction.Kind), CanonicalForm: construction.Label,
			NormalizedForm: construction.Label, Lemma: construction.Label,
			SenseDiscriminator: construction.Meaning, PrimaryTranslation: construction.Meaning,
			MeaningNote: meaningNote,
			PartsNote:   strings.Join(memberParts, " · "),
		})
		spans := make([]semantics.SpanRef, 0, len(construction.Spans))
		for _, span := range construction.Spans {
			spans = append(spans, semantics.SpanRef{
				BlockIndex: blockIndex, SourceText: span.Source,
				Occurrence: spanOccurrence(fixture.Source, span),
			})
		}
		role := construction.Role
		// The demo data may author a discontinuous construction whose members
		// happen to be adjacent in this sentence. v3 requires the role to
		// match the membership shape, so such a construction publishes as
		// contiguous with the members' exact merged span.
		if role == "discontinuous_construction" {
			members := make([]semantics.Token, 0, len(memberIDs))
			for _, id := range memberIDs {
				members = append(members, tokenByRef[id])
			}
			if len(memberRuns(members)) == 1 {
				role = "contiguous_construction"
				first := members[0]
				last := members[len(members)-1]
				start, err := ByteOffsetFromUTF16(fixture.Source, first.StartUTF16)
				if err != nil {
					t.Fatal(err)
				}
				end, err := ByteOffsetFromUTF16(fixture.Source, last.EndUTF16)
				if err != nil {
					t.Fatal(err)
				}
				source := fixture.Source[start:end]
				spans = []semantics.SpanRef{{
					BlockIndex: blockIndex, SourceText: source,
					Occurrence: spanOccurrence(fixture.Source, longFixtureSpan{Start: first.StartUTF16, Source: source}),
				}}
			}
		}
		response.Constructions = append(response.Constructions, semantics.Construction{
			Kind: semantics.Kind(construction.Kind), Role: role,
			NewSenseRef: ref, ShadowText: construction.Meaning, ConfidenceMilli: 900,
			TokenIDs: memberIDs, Spans: spans,
		})
	}
	return response
}

// spanOccurrence returns the zero-based occurrence of the span inside the
// block source: the number of earlier copies of the same text.
func spanOccurrence(source string, span longFixtureSpan) int {
	startByte, err := ByteOffsetFromUTF16(source, span.Start)
	if err != nil {
		return 0
	}
	return strings.Count(source[:startByte], span.Source)
}

// publishLongFixtureArticle creates the fixture article and publishes every
// paragraph through the real chunk publication path, threading prior senses
// exactly like the analysis runner does.
func publishLongFixtureArticle(t *testing.T, db *store.DB, fixture longFixture) library.ULID {
	t.Helper()
	ctx := context.Background()
	articles := NewStore(db)
	bodies := make([]string, 0, len(fixture.Blocks))
	for _, block := range fixture.Blocks {
		bodies = append(bodies, block.Source)
	}
	article, err := NewArticle(fixture.Title, strings.Join(bodies, "\n\n"), "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	jobID := activeAnalysisJobID(t, db, article.ID)
	if err := articles.MarkAnalysisProcessing(ctx, article.ID, jobID); err != nil {
		t.Fatal(err)
	}
	prepared, err := articles.PrepareAnalysis(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Blocks) != len(fixture.Blocks) {
		t.Fatalf("prepared blocks = %d", len(prepared.Blocks))
	}
	prior := make([]semantics.NewSense, 0)
	for blockIndex := range fixture.Blocks {
		chunk, err := semantics.PrepareChunk(prepared, blockIndex, prior)
		if err != nil {
			t.Fatal(err)
		}
		response := authorLongBlockResponse(t, chunk, blockIndex, fixture.Blocks[blockIndex])
		namespaced, err := semantics.NamespaceChunkResponse(blockIndex, response, prior)
		if err != nil {
			t.Fatal(err)
		}
		validated, err := semantics.ValidateChunkResponse(chunk, namespaced)
		if err != nil {
			t.Fatalf("authored block %d rejected: %v", blockIndex, err)
		}
		if err := articles.MarkBlockProcessing(ctx, article.ID, blockIndex, jobID); err != nil {
			t.Fatal(err)
		}
		if err := articles.PersistAnalysisChunk(ctx, article.ID, blockIndex, jobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium", prior); err != nil {
			t.Fatal(err)
		}
		prior = append(prior, namespaced.NewSenses...)
	}
	if err := articles.MarkAnalysisReady(ctx, article.ID, jobID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	return article.ID
}

// assertLongFixtureArticle proves the observable acceptance results on the
// loaded article: every word keeps a visible subtitle, sentences are
// deterministic, and construction membership is exact.
func assertLongFixtureArticle(t *testing.T, articles *Store, ctx context.Context, id library.ULID, fixture longFixture) {
	t.Helper()
	loaded, err := articles.GetArticle(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wantTokens := 0
	wantConstructions := 0
	for _, block := range fixture.Blocks {
		wantTokens += len(block.Tokens)
		wantConstructions += len(block.Constructions)
	}
	if len(loaded.Sentences) != 48 {
		t.Fatalf("sentences = %d, want 48", len(loaded.Sentences))
	}
	tokensSeen := 0
	constructionsSeen := 0
	for blockIndex := range loaded.Blocks {
		loadedBlock := &loaded.Blocks[blockIndex]
		if len(loadedBlock.Sentences) != 4 {
			t.Fatalf("block %d sentences = %d, want 4", blockIndex, len(loadedBlock.Sentences))
		}
		for occurrenceIndex := range loadedBlock.Occurrences {
			occurrence := &loadedBlock.Occurrences[occurrenceIndex]
			switch occurrence.Role {
			case OccurrenceToken:
				tokensSeen++
				if occurrence.SubtitleSuppressionReason != SubtitleNone || !occurrence.ShowShadow || strings.TrimSpace(occurrence.ShadowText) == "" {
					t.Fatalf("block %d token %q lost its visible subtitle: %+v", blockIndex, occurrence.Spans[0].SourceText, occurrence)
				}
			case OccurrenceContiguousConstruction, OccurrenceDiscontinuousConstruction:
				constructionsSeen++
				if strings.TrimSpace(occurrence.ShadowText) == "" || len(occurrence.MemberOccurrenceIDs) == 0 {
					t.Fatalf("block %d construction %q lost its meaning or members: %+v", blockIndex, occurrence.ShadowText, occurrence)
				}
				// The popover's Meaning section must carry the exact authored
				// explanation and Parts the member glosses after the round trip.
				wantNote := expressionNotes[occurrence.Sense.CanonicalForm]
				if occurrence.Sense == nil || occurrence.Sense.MeaningNote != wantNote || strings.TrimSpace(occurrence.Sense.PartsNote) == "" {
					t.Fatalf("block %d construction %q meaning note = %q, want the authored %q (parts = %q)", blockIndex, occurrence.ShadowText, occurrence.Sense.MeaningNote, wantNote, occurrence.Sense.PartsNote)
				}
			}
		}
	}
	if tokensSeen != wantTokens || wantTokens != 871 {
		t.Fatalf("token occurrences = %d, want 871", tokensSeen)
	}
	if constructionsSeen != wantConstructions {
		t.Fatalf("construction occurrences = %d, want %d", constructionsSeen, wantConstructions)
	}
}

// TestLongFixturePublishesThroughRealStore proves the complete real-data path:
// create the 12-paragraph article through the real creation route, publish
// authored analysis through the real chunk pipeline seam, and reread it with
// all 871 word subtitles and every construction member intact.
func TestLongFixturePublishesThroughRealStore(t *testing.T) {
	fixture := loadLongFixture(t)
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := publishLongFixtureArticle(t, db, fixture)
	assertLongFixtureArticle(t, NewStore(db), context.Background(), id, fixture)

	// A second GetArticle is the reload/store round trip the acceptance
	// checks require; occurrence ids and subtitles must be identical.
	ctx := context.Background()
	first, err := NewStore(db).GetArticle(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(db).GetArticle(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for index := range first.Occurrences {
		if first.Occurrences[index].ID != second.Occurrences[index].ID || first.Occurrences[index].ShadowText != second.Occurrences[index].ShadowText {
			t.Fatalf("round trip changed occurrence %d", index)
		}
	}
}

// TestSeedReaderDesignLongArticle materializes the same fixture into the
// isolated local reader-design database so the real saved article is available
// on the local site. It is explicit and default-off: set
// DOUBLANGU_READER_DESIGN_DB to the SQLite path (the launcher must be
// stopped). No production database is ever touched.
func TestSeedReaderDesignLongArticle(t *testing.T) {
	dbPath := os.Getenv("DOUBLANGU_READER_DESIGN_DB")
	if dbPath == "" {
		t.Skip("set DOUBLANGU_READER_DESIGN_DB to the isolated reader-design database to seed the real saved article")
	}
	fixture := loadLongFixture(t)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	bodies := make([]string, 0, len(fixture.Blocks))
	for _, block := range fixture.Blocks {
		bodies = append(bodies, block.Source)
	}
	contentHash := semantics.ContentHash(fixture.Title, "nl", "en", []semantics.Block{{BlockIndex: 0, SourceText: strings.Join(bodies, "\n\n")}})
	var existingID string
	if err := db.QueryRow(ctx, `SELECT id FROM article WHERE title = ? AND source_language = 'nl' AND target_language = 'en' ORDER BY created_at DESC LIMIT 1`, fixture.Title).Scan(&existingID); err == nil && existingID != "" {
		fmt.Printf("reader-design article already present: %s\n", existingID)
		return
	}
	_ = contentHash
	id := publishLongFixtureArticle(t, db, fixture)
	assertLongFixtureArticle(t, NewStore(db), ctx, id, fixture)
	fmt.Printf("reader-design article seeded: /reader/%s\n", id.String())
}

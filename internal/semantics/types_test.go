package semantics

import (
	"fmt"
	"strings"
	"testing"
)

// glossedTokens authors glossed ordinary-word token results (and their
// backing senses) for every supplied token, replacing the former unchanged
// fillers: validation no longer accepts a word without a sense and gloss.
func glossedTokens(input PreparedArticle, translation string) ([]NewSense, []TokenResult) {
	senses := make([]NewSense, 0, len(input.Tokens))
	tokens := make([]TokenResult, 0, len(input.Tokens))
	for index, token := range input.Tokens {
		ref := fmt.Sprintf("fill-%d", index)
		senses = append(senses, NewSense{
			Ref: ref, Kind: KindWord, CanonicalForm: token.SourceText, NormalizedForm: token.SourceText,
			Lemma: token.SourceText, SenseDiscriminator: translation, PrimaryTranslation: translation,
		})
		tokens = append(tokens, TokenResult{
			TokenID: token.ID, Classification: "word", Kind: KindWord,
			NewSenseRef: ref, ShadowText: translation, ConfidenceMilli: 900,
		})
	}
	return senses, tokens
}

func TestPrepareUsesExactContentIdentityAndUTF16TokenAnchors(t *testing.T) {
	input, err := Prepare("Leesles", "nl", "en", []Block{{BlockIndex: 0, SourceText: "😀 bank café"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Tokens) != 2 {
		t.Fatalf("tokens = %+v", input.Tokens)
	}
	if got := input.Tokens[0]; got.SourceText != "bank" || got.ID != "b0:t0" || got.StartUTF16 != 3 || got.EndUTF16 != 7 {
		t.Fatalf("bank token = %+v", got)
	}
	if got := input.Tokens[1]; got.SourceText != "café" || got.StartUTF16 != 8 || got.EndUTF16 != 12 {
		t.Fatalf("café token = %+v", got)
	}
	if input.ContentHash == ContentHash("Leesles", "nl", "en", []Block{{BlockIndex: 0, SourceText: "😀 bank café"}}) {
		t.Fatal("canonically different source bytes unexpectedly share content hash")
	}
	if input.ContentHash == ContentHash("Leesles", "nl", "en", []Block{{BlockIndex: 0, SourceText: "😀 bank café!"}}) {
		t.Fatal("source edit unexpectedly preserved content hash")
	}
	if input.ContentHash == ContentHash("Other", "nl", "en", []Block{{BlockIndex: 0, SourceText: "😀 bank café"}}) {
		t.Fatal("title edit unexpectedly preserved content hash")
	}
}

// attachWholeBlockAnchors labels each block's full source text as its single
// source sentence so validation tests focus on the rule under test.
func attachWholeBlockAnchors(t *testing.T, input PreparedArticle) PreparedArticle {
	t.Helper()
	for index := range input.Blocks {
		block := input.Blocks[index]
		span, err := ResolveSpan(block, block.SourceText, 0)
		if err != nil {
			t.Fatalf("resolve whole block %d anchor: %v", index, err)
		}
		input.Sentences = append(input.Sentences, ResolvedSentence{Index: len(byBlockIndex(input.Sentences, block.BlockIndex)), Span: span})
	}
	return input
}

func byBlockIndex(sentences []ResolvedSentence, blockIndex int) []ResolvedSentence {
	var result []ResolvedSentence
	for _, sentence := range sentences {
		if sentence.Span.BlockIndex == blockIndex {
			result = append(result, sentence)
		}
	}
	return result
}

func anchoredFixture(t *testing.T, text string) PreparedArticle {
	t.Helper()
	input, err := Prepare("Construeren", "nl", "en", []Block{{BlockIndex: 0, SourceText: text}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return attachWholeBlockAnchors(t, input)
}

func TestValidateResponseAcceptsContiguousAndDiscontinuousLayers(t *testing.T) {
	input := anchoredFixture(t, "Ik geef het op. Zij kijkt uit.")
	// Two sentence anchors matching the two clauses; the second block anchor
	// below is replaced by finer anchors for this response.
	input.Sentences = nil
	for _, block := range input.Blocks {
		for index, text := range []string{"Ik geef het op.", "Zij kijkt uit."} {
			span, err := ResolveSpan(block, text, 0)
			if err != nil {
				t.Fatal(err)
			}
			input.Sentences = append(input.Sentences, ResolvedSentence{Index: index, Span: span})
		}
	}
	response := Response{
		Version: AnalysisContractVersion,
		NewSenses: []NewSense{
			{Ref: "give-up", Kind: KindExpression, CanonicalForm: "opgeven", NormalizedForm: "opgeven", SenseDiscriminator: "abandon", PrimaryTranslation: "give up", Alternatives: []string{"quit"}, CanonicalPronunciationText: "opgeven"},
			{Ref: "look-out", Kind: KindExpression, CanonicalForm: "uitkijken", NormalizedForm: "uitkijken", SenseDiscriminator: "watch", PrimaryTranslation: "look out", CanonicalPronunciationText: "uitkijken"},
		},
		Constructions: []Construction{
			{
				Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "give-up", ShadowText: "give up", ConfidenceMilli: 900,
				TokenIDs: []string{"b0:t1", "b0:t3"}, // geef ... op: het is not a member
				Spans:    []SpanRef{{BlockIndex: 0, SourceText: "geef", Occurrence: 0}, {BlockIndex: 0, SourceText: "op", Occurrence: 0}},
			},
			{
				Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "look-out", ShadowText: "look out", ConfidenceMilli: 850,
				TokenIDs: []string{"b0:t5", "b0:t6"},
				Spans:    []SpanRef{{BlockIndex: 0, SourceText: "kijkt uit", Occurrence: 0}},
			},
		},
	}
	layerSenses, layerTokens := glossedTokens(input, "gloss")
	response.Tokens = layerTokens
	response.NewSenses = append(response.NewSenses, layerSenses...)
	validated, err := ValidateResponse(input, response)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Tokens) != len(input.Tokens) || len(validated.Sentences) != 2 || len(validated.Constructions) != 2 {
		t.Fatalf("validated response = %+v", validated)
	}
	if validated.Constructions[0].Spans[1].StartUTF16 <= validated.Constructions[0].Spans[0].EndUTF16 {
		t.Fatal("discontinuous construction spans were not kept separate")
	}
}

func TestValidateResponseRejectsSourceCopiesAndRuleViolations(t *testing.T) {
	input := anchoredFixture(t, "Een bank.")
	base := Response{Version: AnalysisContractVersion}
	baseSenses, baseTokens := glossedTokens(input, "gloss")
	base.Tokens = baseTokens
	base.NewSenses = baseSenses
	assertInvalid := func(name string, response Response) {
		t.Helper()
		if _, err := ValidateResponse(input, response); err == nil {
			t.Fatalf("%s unexpectedly accepted", name)
		}
	}

	duplicate := base
	duplicate.Tokens = append([]TokenResult(nil), base.Tokens...)
	duplicate.Tokens[1].TokenID = duplicate.Tokens[0].TokenID
	assertInvalid("duplicate token", duplicate)

	unsafe := base
	unsafe.Tokens = append([]TokenResult(nil), base.Tokens...)
	unsafe.Tokens[0].ShadowText = "<strong>unsafe</strong>"
	assertInvalid("unsafe subtitle", unsafe)

	missingSubtitle := base
	missingSubtitle.Tokens = append([]TokenResult(nil), base.Tokens...)
	missingSubtitle.Tokens[0] = TokenResult{
		TokenID: input.Tokens[0].ID, Classification: "article", Kind: KindWord,
		NewSenseRef: "fill-0", ConfidenceMilli: 900,
	}
	assertInvalid("missing translated token subtitle", missingSubtitle)

	// An ordinary token whose subtitle copies its own Dutch source spelling is
	// a source copy, never an English subtitle, unless the referenced sense's
	// own translation is spelled the same.
	dutchCopyOrdinary := base
	dutchCopyOrdinary.Tokens = append([]TokenResult(nil), base.Tokens...)
	dutchCopyOrdinary.NewSenses = append(append([]NewSense(nil), base.NewSenses...), NewSense{
		Ref: "bank-sofa", Kind: KindWord, CanonicalForm: "bank", NormalizedForm: "bank",
		SenseDiscriminator: "sofa", PrimaryTranslation: "sofa",
	})
	dutchCopyOrdinary.Tokens[1] = TokenResult{
		TokenID: input.Tokens[1].ID, Classification: "word", Kind: KindWord,
		NewSenseRef: "bank-sofa", ShadowText: "bank", ConfidenceMilli: 900,
	}
	assertInvalid("ordinary source-copy subtitle", dutchCopyOrdinary)

	// An ordinary word classified unchanged is rejected outright: it would
	// bypass the required individual gloss, and same-spelling senses cover
	// words that legitimately read the same in English.
	plainUnchanged := base
	plainUnchanged.Tokens = append([]TokenResult(nil), base.Tokens...)
	plainUnchanged.Tokens[0] = TokenResult{
		TokenID: input.Tokens[0].ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000,
	}
	assertInvalid("ordinary word classified unchanged", plainUnchanged)

	// The same rejection applies when the unchanged token carries a sense.
	sensedUnchanged := base
	sensedUnchanged.Tokens = append([]TokenResult(nil), base.Tokens...)
	sensedUnchanged.NewSenses = append(append([]NewSense(nil), base.NewSenses...), NewSense{
		Ref: "een-article", Kind: KindWord, CanonicalForm: "een", NormalizedForm: "een",
		SenseDiscriminator: "article", PrimaryTranslation: "a",
	})
	sensedUnchanged.Tokens[0] = TokenResult{
		TokenID: input.Tokens[0].ID, Classification: "unchanged", Kind: KindWord,
		NewSenseRef: "een-article", ConfidenceMilli: 1000,
	}
	assertInvalid("sensed unchanged token", sensedUnchanged)

	// A special token may now display its visible identity label: a proper
	// name displays its name and a number its value, so a same-spelling
	// subtitle is a legitimate label rather than an untranslated copy.
	specialIdentity := base
	specialIdentity.Tokens = append([]TokenResult(nil), base.Tokens...)
	specialIdentity.Tokens[0] = TokenResult{
		TokenID: input.Tokens[0].ID, Classification: "proper_name", Kind: KindWord,
		ShadowText: "Een", ConfidenceMilli: 1000,
	}
	if _, err := ValidateResponse(input, specialIdentity); err != nil {
		t.Fatalf("special identity label rejected: %v", err)
	}

	// A construction subtitle that copies the joined Dutch member text must
	// enter correction.
	dutchCopyConstruction := base
	dutchCopyConstruction.Tokens = append([]TokenResult(nil), base.Tokens...)
	dutchCopyConstruction.NewSenses = append(append([]NewSense(nil), base.NewSenses...), NewSense{
		Ref: "bank-phrase", Kind: KindExpression, CanonicalForm: "een bank", NormalizedForm: "een bank",
		SenseDiscriminator: "sofa", PrimaryTranslation: "a bench",
	})
	dutchCopyConstruction.Constructions = []Construction{{
		Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "bank-phrase",
		ShadowText: "een bank", ConfidenceMilli: 900,
		TokenIDs: []string{"b0:t0", "b0:t1"}, Spans: []SpanRef{{BlockIndex: 0, SourceText: "Een bank", Occurrence: 0}},
	}}
	assertInvalid("Dutch-copy construction subtitle", dutchCopyConstruction)

	badConstruction := base
	badConstruction.NewSenses = append(append([]NewSense(nil), base.NewSenses...), NewSense{Ref: "bad", Kind: KindExpression, CanonicalForm: "bank", NormalizedForm: "bank", SenseDiscriminator: "bad", PrimaryTranslation: "bad", CanonicalPronunciationText: "bank"})
	badConstruction.Constructions = []Construction{{Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "bad", ShadowText: "bad", TokenIDs: []string{"b0:t0"}, Spans: []SpanRef{{BlockIndex: 0, SourceText: "bank", Occurrence: 0}}}}
	// A construction with two references is not a legal provider identity.
	badConstruction.Constructions[0].SemanticSenseID = "candidate"
	assertInvalid("two construction senses", badConstruction)
}

func TestValidateResponseRejectsDiscontinuousSingleRunAndCrossSentenceMembers(t *testing.T) {
	input := anchoredFixture(t, "Hij geeft het op.")
	response := Response{
		Version: AnalysisContractVersion,
		NewSenses: []NewSense{{
			Ref: "give-up", Kind: KindExpression, CanonicalForm: "opgeven", NormalizedForm: "opgeven",
			SenseDiscriminator: "resign", PrimaryTranslation: "give up",
		}},
		Constructions: []Construction{{
			Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "give-up",
			ShadowText: "give up", ConfidenceMilli: 900,
			TokenIDs: []string{"b0:t1", "b0:t3"}, // geeft ... op: two separate runs
			Spans: []SpanRef{
				{BlockIndex: 0, SourceText: "geeft", Occurrence: 0},
				{BlockIndex: 0, SourceText: "op", Occurrence: 0},
			},
		}},
	}
	fillSenses, fillTokens := glossedTokens(input, "gloss")
	response.Tokens = fillTokens
	response.NewSenses = append(response.NewSenses, fillSenses...)
	if _, err := ValidateResponse(input, response); err != nil {
		t.Fatalf("valid discontinuous construction rejected: %v", err)
	}

	// The same role is invalid when the members form only one adjacent run:
	// a discontinuous construction needs at least two runs.
	singleRun := response
	singleRun.Constructions[0].TokenIDs = []string{"b0:t1", "b0:t2"}
	singleRun.Constructions[0].Spans = []SpanRef{{BlockIndex: 0, SourceText: "geeft het", Occurrence: 0}, {BlockIndex: 0, SourceText: "op", Occurrence: 0}}
	if _, err := ValidateResponse(input, singleRun); err == nil {
		t.Fatal("single-run discontinuous construction unexpectedly accepted")
	}

	// Members in source order are required: out-of-order ids must fail.
	outOfOrder := response
	outOfOrder.Constructions[0].TokenIDs = []string{"b0:t3", "b0:t1"}
	if _, err := ValidateResponse(input, outOfOrder); err == nil {
		t.Fatal("out-of-order members unexpectedly accepted")
	}

	// A construction whose members cross a source sentence boundary is
	// rejected: cross-sentence constructions never exist in v3.
	crossSentenceInput := anchoredFixture(t, "Ik geef het op. Zij kijkt uit.")
	crossSentenceInput.Sentences = nil
	for index, text := range []string{"Ik geef het op.", "Zij kijkt uit."} {
		span, err := ResolveSpan(crossSentenceInput.Blocks[0], text, 0)
		if err != nil {
			t.Fatal(err)
		}
		crossSentenceInput.Sentences = append(crossSentenceInput.Sentences, ResolvedSentence{Index: index, Span: span})
	}
	cross := Response{Version: AnalysisContractVersion}
	crossSenses, crossTokens := glossedTokens(crossSentenceInput, "gloss")
	cross.Tokens = crossTokens
	cross.NewSenses = append(cross.NewSenses, crossSenses...)
	cross.NewSenses = []NewSense{{
		Ref: "give-up", Kind: KindExpression, CanonicalForm: "opgeven", NormalizedForm: "opgeven",
		SenseDiscriminator: "resign", PrimaryTranslation: "give up",
	}}
	cross.Constructions = []Construction{{
		Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "give-up",
		ShadowText: "give up", ConfidenceMilli: 900,
		TokenIDs: []string{"b0:t1", "b0:t5"}, // geef ... kijkt across the sentence boundary
		Spans: []SpanRef{
			{BlockIndex: 0, SourceText: "geef", Occurrence: 0},
			{BlockIndex: 0, SourceText: "kijkt", Occurrence: 0},
		},
	}}
	if _, err := ValidateResponse(crossSentenceInput, cross); err == nil {
		t.Fatal("cross-sentence construction unexpectedly accepted")
	}
}

func TestValidateResponseRejectsContiguousSplitRuns(t *testing.T) {
	input := anchoredFixture(t, "Hij loopt snel.")
	response := Response{Version: AnalysisContractVersion}
	runSenses, runTokens := glossedTokens(input, "gloss")
	response.Tokens = runTokens
	response.NewSenses = append(runSenses, NewSense{
		Ref: "run-fast", Kind: KindExpression, CanonicalForm: "snel lopen", NormalizedForm: "snel lopen",
		SenseDiscriminator: "speed", PrimaryTranslation: "run fast",
	})
	// Members Hij and snel are not adjacent: loopt sits between them, so a
	// contiguous construction may never list them together.
	response.Constructions = []Construction{{
		Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "run-fast",
		ShadowText: "run fast", ConfidenceMilli: 900,
		TokenIDs: []string{"b0:t0", "b0:t2"},
		Spans:    []SpanRef{{BlockIndex: 0, SourceText: "Hij loopt snel", Occurrence: 0}},
	}}
	if _, err := ValidateResponse(input, response); err == nil {
		t.Fatal("split-run contiguous construction unexpectedly accepted")
	}
}

func TestValidateResponseRejectsAnchorGapsAndOverlaps(t *testing.T) {
	input := anchoredFixture(t, "Een bank.")
	base := Response{Version: AnalysisContractVersion}
	baseSenses, baseTokens := glossedTokens(input, "gloss")
	base.Tokens = baseTokens
	base.NewSenses = baseSenses
	// Dropping the anchor that covers the second token makes it uncovered.
	partial := input
	partial.Sentences = partial.Sentences[:0]
	span, err := ResolveSpan(partial.Blocks[0], "Een", 0)
	if err != nil {
		t.Fatal(err)
	}
	partial.Sentences = append(partial.Sentences, ResolvedSentence{Index: 0, Span: span})
	if _, err := ValidateResponse(partial, base); err == nil {
		t.Fatal("token coverage gap unexpectedly accepted")
	}
	// Overlapping anchors within one block are invalid.
	overlap := input
	overlap.Sentences = nil
	first, _ := ResolveSpan(overlap.Blocks[0], "Een bank", 0)
	second, _ := ResolveSpan(overlap.Blocks[0], "bank.", 0)
	overlap.Sentences = []ResolvedSentence{{Index: 0, Span: first}, {Index: 1, Span: second}}
	if _, err := ValidateResponse(overlap, base); err == nil {
		t.Fatal("overlapping anchors unexpectedly accepted")
	}
}

func TestDecodeResponseIsStrictAndRejectsTrailingJSON(t *testing.T) {
	valid := `{"version":"reader.analysis.v3","tokens":[],"new_senses":[],"constructions":[]}`
	if _, err := DecodeResponse([]byte(valid + " {}")); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing response error = %v", err)
	}
	if _, err := DecodeResponse([]byte(`{"version":"reader.analysis.v3","sentences":[],"tokens":[],"new_senses":[],"constructions":[],"extra":1}`)); err == nil {
		t.Fatal("provider-authored sentences or unknown fields unexpectedly accepted")
	}
}

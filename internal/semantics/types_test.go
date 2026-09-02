package semantics

import (
	"strings"
	"testing"
)

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

func TestValidateResponseAcceptsContiguousAndDiscontinuousLayers(t *testing.T) {
	input, err := Prepare("Construeren", "nl", "en", []Block{{BlockIndex: 0, SourceText: "Ik geef het op. Zij kijkt uit."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := Response{
		Version: AnalysisContractVersion,
		Sentences: []Sentence{
			{Source: SpanRef{BlockIndex: 0, SourceText: "Ik geef het op.", Occurrence: 0}},
			{Source: SpanRef{BlockIndex: 0, SourceText: "Zij kijkt uit.", Occurrence: 0}},
		},
		NewSenses: []NewSense{
			{Ref: "give-up", Kind: KindExpression, CanonicalForm: "opgeven", NormalizedForm: "opgeven", SenseDiscriminator: "abandon", PrimaryTranslation: "give up", Alternatives: []string{"quit"}, CanonicalPronunciationText: "opgeven"},
			{Ref: "look-out", Kind: KindExpression, CanonicalForm: "uitkijken", NormalizedForm: "uitkijken", SenseDiscriminator: "watch", PrimaryTranslation: "look out", CanonicalPronunciationText: "uitkijken"},
		},
		Constructions: []Construction{
			{
				Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "give-up", ShadowText: "give up", ConfidenceMilli: 900,
				TokenIDs: []string{"b0:t1", "b0:t2", "b0:t3"},
				Spans:    []SpanRef{{BlockIndex: 0, SourceText: "geef het", Occurrence: 0}, {BlockIndex: 0, SourceText: "op", Occurrence: 0}},
			},
			{
				Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "look-out", ShadowText: "look out", ConfidenceMilli: 850,
				TokenIDs: []string{"b0:t5", "b0:t6"},
				Spans:    []SpanRef{{BlockIndex: 0, SourceText: "kijkt uit", Occurrence: 0}},
			},
		},
	}
	for _, token := range input.Tokens {
		response.Tokens = append(response.Tokens, TokenResult{
			TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000,
		})
	}
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

func TestValidateResponseRejectsCoverageOrderAndUnsafeOutput(t *testing.T) {
	input, err := Prepare("Test", "nl", "en", []Block{{BlockIndex: 0, SourceText: "Een bank."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := Response{
		Version:   AnalysisContractVersion,
		Sentences: []Sentence{{Source: SpanRef{BlockIndex: 0, SourceText: "Een bank.", Occurrence: 0}}},
	}
	for _, token := range input.Tokens {
		base.Tokens = append(base.Tokens, TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
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

	missingCoverage := base
	missingCoverage.Sentences = []Sentence{{Source: SpanRef{BlockIndex: 0, SourceText: "Een", Occurrence: 0}}}
	assertInvalid("sentence coverage gap", missingCoverage)

	outOfOrder := base
	outOfOrder.Sentences = []Sentence{{Source: SpanRef{BlockIndex: 0, SourceText: "bank.", Occurrence: 0}}, {Source: SpanRef{BlockIndex: 0, SourceText: "Een", Occurrence: 0}}}
	assertInvalid("sentence order", outOfOrder)

	unsafe := base
	unsafe.Tokens = append([]TokenResult(nil), base.Tokens...)
	unsafe.Tokens[0].ShadowText = "<strong>unsafe</strong>"
	assertInvalid("unsafe subtitle", unsafe)

	missingSubtitle := base
	missingSubtitle.Tokens = append([]TokenResult(nil), base.Tokens...)
	missingSubtitle.NewSenses = []NewSense{{
		Ref: "translated", Kind: KindWord, CanonicalForm: "Een", NormalizedForm: "een",
		SenseDiscriminator: "article", PrimaryTranslation: "a",
	}}
	missingSubtitle.Tokens[0] = TokenResult{
		TokenID: input.Tokens[0].ID, Classification: "article", Kind: KindWord,
		NewSenseRef: "translated", ConfidenceMilli: 900,
	}
	assertInvalid("missing translated token subtitle", missingSubtitle)

	missingConstructionSubtitle := base
	missingConstructionSubtitle.NewSenses = []NewSense{{
		Ref: "bank-expression", Kind: KindExpression, CanonicalForm: "bank", NormalizedForm: "bank",
		SenseDiscriminator: "test", PrimaryTranslation: "bench",
	}}
	missingConstructionSubtitle.Constructions = []Construction{{
		Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "bank-expression",
		TokenIDs: []string{"b0:t1"}, Spans: []SpanRef{{BlockIndex: 0, SourceText: "bank", Occurrence: 0}},
	}}
	assertInvalid("missing construction subtitle", missingConstructionSubtitle)

	badNormalizedSense := base
	badNormalizedSense.NewSenses = []NewSense{{
		Ref: "bad-normalized", Kind: KindWord, CanonicalForm: "Één keer", NormalizedForm: "een andere vorm",
		SenseDiscriminator: "test", PrimaryTranslation: "one time",
	}}
	assertInvalid("sense normalized form mismatch", badNormalizedSense)

	badConstruction := base
	badConstruction.NewSenses = []NewSense{{Ref: "bad", Kind: KindExpression, CanonicalForm: "bank", NormalizedForm: "bank", SenseDiscriminator: "bad", PrimaryTranslation: "bad", CanonicalPronunciationText: "bank"}}
	badConstruction.Constructions = []Construction{{Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "bad", ShadowText: "bad", TokenIDs: []string{"b0:t0"}, Spans: []SpanRef{{BlockIndex: 0, SourceText: "bank", Occurrence: 0}}}}
	// A construction with two references is not a legal provider identity.
	badConstruction.Constructions[0].SemanticSenseID = "candidate"
	assertInvalid("two construction senses", badConstruction)
}

func TestDecodeResponseIsStrictAndRejectsTrailingJSON(t *testing.T) {
	valid := `{"version":"reader.analysis.v2","sentences":[],"tokens":[],"new_senses":[],"constructions":[]}`
	if _, err := DecodeResponse([]byte(valid + " {}")); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing response error = %v", err)
	}
	if _, err := DecodeResponse([]byte(`{"version":"reader.analysis.v2","sentences":[],"tokens":[],"new_senses":[],"constructions":[],"extra":1}`)); err == nil {
		t.Fatal("unknown response field unexpectedly accepted")
	}
}

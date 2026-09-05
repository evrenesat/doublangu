package semantics

import (
	"strings"
	"testing"

	"doublangu/internal/pipeline"
)

// The regression fixtures below mirror the authored reader-demo sample: every
// word carries its own contextually appropriate subtitle, including function
// words and idiom members, and same-spelling English senses (plan, the
// financial bank, in) stay distinct from translated senses such as the sofa
// bank. Inserted modifiers are never construction members.

type parityWord struct {
	source   string
	gloss    string
	identity bool // proper name or number displayed by its own identity
}

type parityConstruction struct {
	role      string
	kind      Kind
	members   []string // member source words in source order
	spans     []string // exact source spans, ordered and non-overlapping
	label     string
	meaning   string
	meaningOf string // canonical form for the construction sense
}

type paritySentence struct {
	source       string
	words        []parityWord
	construction []parityConstruction
}

func paritySenseRef(word parityWord, index int) string {
	return "ref-" + word.source + "-" + word.gloss + "-" + strings.TrimSpace(intToString(index))
}

func intToString(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// parityResponse authors the full v3 response for one sentence fixture. Sense
// refs are deduplicated per (source, gloss) so several tokens reuse one sense.
func parityResponse(t *testing.T, sentence paritySentence, input PreparedArticle) Response {
	t.Helper()
	response := Response{Version: AnalysisContractVersion}
	refByToken := make(map[string]string, len(sentence.words))
	defined := make(map[string]struct{})
	for index, word := range sentence.words {
		if word.identity {
			continue
		}
		ref := paritySenseRef(word, index)
		refByToken[input.Tokens[index].ID] = ref
		if _, exists := defined[ref]; exists {
			continue
		}
		defined[ref] = struct{}{}
		response.NewSenses = append(response.NewSenses, NewSense{
			Ref: ref, Kind: KindWord, CanonicalForm: word.source, NormalizedForm: word.source,
			Lemma: word.source, PartOfSpeech: "word", SenseDiscriminator: word.gloss,
			PrimaryTranslation: word.gloss,
		})
	}
	for _, token := range input.Tokens {
		result := TokenResult{TokenID: token.ID, Classification: "word", Kind: KindWord, ConfidenceMilli: 900}
		if ref, ok := refByToken[token.ID]; ok {
			result.NewSenseRef = ref
			result.ShadowText = sentence.words[tokenIndexByID(input, token.ID)].gloss
		} else {
			// Identity words display their own name or value.
			result.Classification = "proper_name"
			result.ShadowText = token.SourceText
		}
		response.Tokens = append(response.Tokens, result)
	}
	for constructionIndex, construction := range sentence.construction {
		ref := "con-" + intToString(constructionIndex)
		response.NewSenses = append(response.NewSenses, NewSense{
			Ref: ref, Kind: construction.kind, CanonicalForm: construction.meaningOf,
			NormalizedForm: construction.meaningOf, Lemma: construction.meaningOf,
			SenseDiscriminator: construction.label, PrimaryTranslation: construction.meaning,
		})
		memberIDs := make([]string, 0, len(construction.members))
		for _, member := range construction.members {
			memberIDs = append(memberIDs, tokenIDForSource(t, input, member))
		}
		spans := make([]SpanRef, 0, len(construction.spans))
		for _, span := range construction.spans {
			spans = append(spans, SpanRef{BlockIndex: 0, SourceText: span, Occurrence: 0})
		}
		response.Constructions = append(response.Constructions, Construction{
			Kind: construction.kind, Role: construction.role, NewSenseRef: ref,
			ShadowText: construction.meaning, ConfidenceMilli: 900,
			TokenIDs: memberIDs, Spans: spans,
		})
	}
	return response
}

func tokenIndexByID(input PreparedArticle, id string) int {
	for index, token := range input.Tokens {
		if token.ID == id {
			return index
		}
	}
	return -1
}

func tokenIDForSource(t *testing.T, input PreparedArticle, source string) string {
	t.Helper()
	for _, token := range input.Tokens {
		if token.SourceText == source {
			return token.ID
		}
	}
	t.Fatalf("fixture word %q is not a supplied token", source)
	return ""
}

func validatedParitySentence(t *testing.T, sentence paritySentence) (PreparedArticle, ValidatedResponse) {
	t.Helper()
	input, err := Prepare("Parity", "nl", "en", []Block{{BlockIndex: 0, SourceText: sentence.source}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Tokens) != len(sentence.words) {
		t.Fatalf("sentence %q tokenized to %d tokens for %d fixture words", sentence.source, len(input.Tokens), len(sentence.words))
	}
	for index, word := range sentence.words {
		if input.Tokens[index].SourceText != word.source {
			t.Fatalf("token %d = %q, want %q", index, input.Tokens[index].SourceText, word.source)
		}
	}
	input = attachWholeBlockAnchors(t, input)
	validated, err := ValidateResponse(input, parityResponse(t, sentence, input))
	if err != nil {
		t.Fatalf("authored parity sentence rejected: %v", err)
	}
	return input, validated
}

// paritySample returns the six demo sentences: every short-sample sentence
// with authored per-word glosses and exact construction membership.
func paritySample() []paritySentence {
	return []paritySentence{
		{
			source: "Na het werk ging Noor op de bank zitten om tot rust te komen.",
			words: []parityWord{
				{"Na", "After", false}, {"het", "the", false}, {"werk", "work", false},
				{"ging", "went", false}, {"Noor", "Noor", true}, {"op", "on", false},
				{"de", "the", false}, {"bank", "sofa", false}, {"zitten", "sit", false},
				{"om", "to", false}, {"tot", "to", false}, {"rust", "rest", false},
				{"te", "to", false}, {"komen", "come", false},
			},
			construction: []parityConstruction{{
				role: "contiguous_construction", kind: KindIdiom,
				members: []string{"tot", "rust", "te", "komen"},
				spans:   []string{"tot rust te komen"},
				label:   "tot rust te komen", meaning: "to calm down", meaningOf: "tot rust komen",
			}},
		},
		{
			source: "Ze gaf haar plan niet op en belde een vriend.",
			words: []parityWord{
				{"Ze", "She", false}, {"gaf", "gave", false}, {"haar", "her", false},
				{"plan", "plan", false}, {"niet", "not", false}, {"op", "up", false},
				{"en", "and", false}, {"belde", "called", false}, {"een", "a", false},
				{"vriend", "friend", false},
			},
			construction: []parityConstruction{{
				role: "discontinuous_construction", kind: KindExpression,
				members: []string{"gaf", "op"},
				spans:   []string{"gaf", "op"},
				label:   "gaf … op", meaning: "gave up", meaningOf: "opgeven",
			}},
		},
		{
			source: "De bank keurde haar aanvraag goed.",
			words: []parityWord{
				{"De", "The", false}, {"bank", "bank", false}, {"keurde", "assessed", false},
				{"haar", "her", false}, {"aanvraag", "application", false}, {"goed", "good", false},
			},
			construction: []parityConstruction{{
				role: "discontinuous_construction", kind: KindExpression,
				members: []string{"keurde", "goed"},
				spans:   []string{"keurde", "goed"},
				label:   "keurde … goed", meaning: "approved", meaningOf: "goedkeuren",
			}},
		},
		{
			source: "Hij gooide gisteren bijna het bijltje erbij neer.",
			words: []parityWord{
				{"Hij", "He", false}, {"gooide", "threw", false}, {"gisteren", "yesterday", false},
				{"bijna", "almost", false}, {"het", "the", false}, {"bijltje", "little axe", false},
				{"erbij", "therewith", false}, {"neer", "down", false},
			},
			construction: []parityConstruction{{
				role: "discontinuous_construction", kind: KindIdiom,
				members: []string{"gooide", "bijltje", "erbij", "neer"},
				spans:   []string{"gooide", "het bijltje erbij neer"},
				label:   "gooide … het bijltje erbij neer", meaning: "gave up", meaningOf: "het bijltje erbij neergooien",
			}},
		},
		{
			source: "Ze viel met de deur in huis, maar hij luisterde rustig.",
			words: []parityWord{
				{"Ze", "She", false}, {"viel", "fell", false}, {"met", "with", false},
				{"de", "the", false}, {"deur", "door", false}, {"in", "in", false},
				{"huis", "house", false}, {"maar", "but", false}, {"hij", "he", false},
				{"luisterde", "listened", false}, {"rustig", "calmly", false},
			},
			construction: []parityConstruction{{
				role: "contiguous_construction", kind: KindIdiom,
				members: []string{"viel", "met", "de", "deur", "in", "huis"},
				spans:   []string{"viel met de deur in huis"},
				label:   "met de deur in huis vallen", meaning: "get straight to the point", meaningOf: "met de deur in huis vallen",
			}},
		},
		{
			source: "Hij gaf het plan dat hij samen met zijn vrienden zorgvuldig had uitgewerkt uiteindelijk toch niet op.",
			words: []parityWord{
				{"Hij", "He", false}, {"gaf", "gave", false}, {"het", "the", false},
				{"plan", "plan", false}, {"dat", "that", false}, {"hij", "he", false},
				{"samen", "together", false}, {"met", "with", false}, {"zijn", "his", false},
				{"vrienden", "friends", false}, {"zorgvuldig", "carefully", false},
				{"had", "had", false}, {"uitgewerkt", "developed", false},
				{"uiteindelijk", "eventually", false}, {"toch", "after all", false},
				{"niet", "not", false}, {"op", "up", false},
			},
			construction: []parityConstruction{{
				role: "discontinuous_construction", kind: KindExpression,
				members: []string{"gaf", "op"},
				spans:   []string{"gaf", "op"},
				label:   "gaf … op", meaning: "gave up", meaningOf: "opgeven",
			}},
		},
	}
}

// TestShortSampleKeepsIndividualMeanings proves the demo requirement on every
// short-sample sentence: 66 lexical words, every one with a visible
// individual subtitle, exact source slicing, and exact construction
// membership, including idiom members' literal glosses.
func TestShortSampleKeepsIndividualMeanings(t *testing.T) {
	totalWords := 0
	for _, sentence := range paritySample() {
		input, validated := validatedParitySentence(t, sentence)
		totalWords += len(sentence.words)
		for index, resolved := range validated.Tokens {
			result := resolved.Result
			if strings.TrimSpace(result.ShadowText) == "" {
				t.Fatalf("token %q has no individual subtitle", resolved.Token.SourceText)
			}
			if result.ShadowText != sentence.words[index].gloss {
				t.Fatalf("token %q subtitle = %q, want %q", resolved.Token.SourceText, result.ShadowText, sentence.words[index].gloss)
			}
			span := resolved.Token
			sliced := input.Blocks[0].SourceText[span.StartUTF16:span.EndUTF16]
			if sliced != span.SourceText {
				t.Fatalf("token %q span does not slice its exact source", span.SourceText)
			}
		}
		for constructionIndex, resolved := range validated.Constructions {
			want := sentence.construction[constructionIndex]
			if len(resolved.Construction.TokenIDs) != len(want.members) {
				t.Fatalf("construction %q members = %v, want %v", want.label, resolved.Construction.TokenIDs, want.members)
			} else {
				for memberIndex, memberID := range resolved.Construction.TokenIDs {
					if input.Tokens[tokenIndexByID(input, memberID)].SourceText != want.members[memberIndex] {
						t.Fatalf("construction %q member %d = %q, want %q", want.label, memberIndex, input.Tokens[tokenIndexByID(input, memberID)].SourceText, want.members[memberIndex])
					}
				}
			}
			// Idiom members keep their own literal gloss next to the
			// construction's independent meaning.
			if resolved.Construction.ShadowText == want.meaning {
				for _, memberID := range resolved.Construction.TokenIDs {
					member := validated.Tokens[tokenIndexByID(input, memberID)]
					if strings.TrimSpace(member.Result.ShadowText) == "" {
						t.Fatalf("idiom member %q lost its literal gloss", member.Token.SourceText)
					}
				}
			}
		}
	}
	if totalWords != 66 {
		t.Fatalf("short sample has %d words, want 66", totalWords)
	}
}

// TestSameSpellingSensesStaySemantic proves legitimate same-spelling English
// senses are accepted while a sofa bank pretending to be the financial bank is
// rejected: learning stays semantic-sense keyed.
func TestSameSpellingSensesStaySemantic(t *testing.T) {
	// The sample already contains plan → plan, financial bank → bank, and
	// in → in; they validate as part of the whole sample.
	for _, sentence := range paritySample() {
		validatedParitySentence(t, sentence)
	}

	// The sofa sense of bank must never take the same-spelling subtitle.
	sofa := paritySample()[2]
	sofa.words[1] = parityWord{source: "bank", gloss: "sofa"}
	input, err := Prepare("Parity", "nl", "en", []Block{{BlockIndex: 0, SourceText: sofa.source}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input = attachWholeBlockAnchors(t, input)
	response := parityResponse(t, sofa, input)
	bankID := tokenIDForSource(t, input, "bank")
	for index := range response.Tokens {
		if response.Tokens[index].TokenID == bankID {
			response.Tokens[index].ShadowText = "bank"
		}
	}
	if _, err := ValidateResponse(input, response); err == nil {
		t.Fatal("sofa bank with the financial same-spelling subtitle unexpectedly accepted")
	}
}

// TestSpecialTokensDisplayIdentityLabels proves names and numbers display
// their identity, not a fabricated translation or an untranslated mistake.
func TestSpecialTokensDisplayIdentityLabels(t *testing.T) {
	input := anchoredFixture(t, "Noor telde tot 12.")
	response := Response{Version: AnalysisContractVersion,
		NewSenses: []NewSense{
			{Ref: "telde", Kind: KindWord, CanonicalForm: "telden", NormalizedForm: "telden", SenseDiscriminator: "counted", PrimaryTranslation: "counted"},
			{Ref: "tot", Kind: KindWord, CanonicalForm: "tot", NormalizedForm: "tot", SenseDiscriminator: "until", PrimaryTranslation: "until"},
		},
	}
	for _, token := range input.Tokens {
		result := TokenResult{TokenID: token.ID, Classification: "word", Kind: KindWord, ConfidenceMilli: 900}
		switch token.SourceText {
		case "Noor":
			result.Classification = "proper_name"
			result.ShadowText = "Noor"
		case "12":
			result.Classification = "number"
			result.ShadowText = "12"
		case "telde":
			result.NewSenseRef = "telde"
			result.ShadowText = "counted"
		case "tot":
			result.NewSenseRef = "tot"
			result.ShadowText = "until"
		}
		response.Tokens = append(response.Tokens, result)
	}
	validated, err := ValidateResponse(input, response)
	if err != nil {
		t.Fatalf("identity labels rejected: %v", err)
	}
	for _, resolved := range validated.Tokens {
		if strings.TrimSpace(resolved.Result.ShadowText) == "" {
			t.Fatalf("special token %q lost its identity label", resolved.Token.SourceText)
		}
	}
}

// TestTwoConstructionsInOneSentence proves two constructions in one sentence
// keep separate identities and exact members.
func TestTwoConstructionsInOneSentence(t *testing.T) {
	sentence := paritySentence{
		source: "Ze gaf haar plan niet op en nodigde haar vrienden uit.",
		words: []parityWord{
			{"Ze", "She", false}, {"gaf", "gave", false}, {"haar", "her", false},
			{"plan", "plan", false}, {"niet", "not", false}, {"op", "up", false},
			{"en", "and", false}, {"nodigde", "invited", false}, {"haar", "her", false},
			{"vrienden", "friends", false}, {"uit", "out", false},
		},
		construction: []parityConstruction{
			{role: "discontinuous_construction", kind: KindExpression, members: []string{"gaf", "op"}, spans: []string{"gaf", "op"}, label: "gaf … op", meaning: "gave up", meaningOf: "opgeven"},
			{role: "discontinuous_construction", kind: KindExpression, members: []string{"nodigde", "uit"}, spans: []string{"nodigde", "uit"}, label: "nodigde … uit", meaning: "invited", meaningOf: "uitnodigen"},
		},
	}
	_, validated := validatedParitySentence(t, sentence)
	if len(validated.Constructions) != 2 {
		t.Fatalf("constructions = %d, want 2", len(validated.Constructions))
	}
	first := validated.Constructions[0].Construction.TokenIDs
	second := validated.Constructions[1].Construction.TokenIDs
	if len(first) != 2 || len(second) != 2 || first[0] == second[0] {
		t.Fatalf("construction members overlap: %v / %v", first, second)
	}
}

// TestInsertedModifiersAreNeverMembers proves gisteren and bijna cannot become
// members of the bijltje construction: they would merge the runs.
func TestInsertedModifiersAreNeverMembers(t *testing.T) {
	sentence := paritySample()[3]
	input, err := Prepare("Parity", "nl", "en", []Block{{BlockIndex: 0, SourceText: sentence.source}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input = attachWholeBlockAnchors(t, input)
	response := parityResponse(t, sentence, input)
	response.Constructions[0].TokenIDs = []string{
		tokenIDForSource(t, input, "gooide"), tokenIDForSource(t, input, "gisteren"),
		tokenIDForSource(t, input, "bijna"), tokenIDForSource(t, input, "het"),
		tokenIDForSource(t, input, "bijltje"), tokenIDForSource(t, input, "erbij"),
		tokenIDForSource(t, input, "neer"),
	}
	response.Constructions[0].Spans = []SpanRef{{BlockIndex: 0, SourceText: "gooide gisteren bijna het bijltje erbij neer", Occurrence: 0}}
	if _, err := ValidateResponse(input, response); err == nil {
		t.Fatal("discontinuous construction with inserted modifiers unexpectedly accepted")
	}
}

// TestLongBlockRelativeUTF16Offsets proves token and construction offsets stay
// block-relative: a block-1 construction resolves inside block 1, not at an
// article-absolute offset.
func TestLongBlockRelativeUTF16Offsets(t *testing.T) {
	first := strings.Repeat("De oude kaart hing naast de ingang. ", 12)
	second := "Hij gaf het plan dat hij had uitgewerkt niet op."
	input, err := Prepare("Parity", "nl", "en", []Block{
		{BlockIndex: 0, SourceText: strings.TrimSpace(first)},
		{BlockIndex: 1, SourceText: second},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input = attachWholeBlockAnchors(t, input)
	response := Response{Version: AnalysisContractVersion,
		NewSenses: []NewSense{
			{Ref: "give-up", Kind: KindExpression, CanonicalForm: "opgeven", NormalizedForm: "opgeven", SenseDiscriminator: "abandon", PrimaryTranslation: "gave up"},
			{Ref: "gloss", Kind: KindWord, CanonicalForm: "woord", NormalizedForm: "woord", SenseDiscriminator: "context gloss", PrimaryTranslation: "gloss"},
			{Ref: "gave", Kind: KindWord, CanonicalForm: "geven", NormalizedForm: "geven", SenseDiscriminator: "gave", PrimaryTranslation: "gave"},
			{Ref: "up", Kind: KindWord, CanonicalForm: "op", NormalizedForm: "op", SenseDiscriminator: "up", PrimaryTranslation: "up"},
		},
		Constructions: []Construction{{
			Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "give-up",
			ShadowText: "gave up", ConfidenceMilli: 900,
			TokenIDs: []string{tokenIDForSource(t, input, "gaf"), tokenIDForSource(t, input, "op")},
			Spans:    []SpanRef{{BlockIndex: 1, SourceText: "gaf", Occurrence: 0}, {BlockIndex: 1, SourceText: "op", Occurrence: 0}},
		}},
	}
	for _, token := range input.Tokens {
		result := TokenResult{TokenID: token.ID, Classification: "word", Kind: KindWord, ConfidenceMilli: 900, NewSenseRef: "gloss", ShadowText: "gloss"}
		switch token.SourceText {
		case "gaf":
			result.NewSenseRef = "gave"
			result.ShadowText = "gave"
		case "op":
			result.NewSenseRef = "up"
			result.ShadowText = "up"
		}
		response.Tokens = append(response.Tokens, result)
	}
	validated, err := ValidateResponse(input, response)
	if err != nil {
		t.Fatalf("two-block response rejected: %v", err)
	}
	block1First := -1
	for _, resolved := range validated.Tokens {
		if resolved.Token.BlockIndex == 1 {
			block1First = resolved.Token.StartUTF16
			break
		}
	}
	if block1First < 0 || block1First > len(second) {
		t.Fatalf("block 1 offsets are not block-relative (first = %d)", block1First)
	}
	for _, resolved := range validated.Constructions {
		for _, span := range resolved.Spans {
			if span.BlockIndex == 1 && span.StartUTF16 >= len(second) {
				t.Fatalf("block 1 span is not block-relative: %+v", span)
			}
			sliced := input.Blocks[span.BlockIndex].SourceText[span.StartUTF16:span.EndUTF16]
			if sliced != span.SourceText {
				t.Fatalf("span %q does not slice its exact source", span.SourceText)
			}
		}
	}
}

// TestCoverageAndReferenceRejections proves missing words and invalid member
// references can never publish.
func TestCoverageAndReferenceRejections(t *testing.T) {
	sentence := paritySample()[1]
	input, err := Prepare("Parity", "nl", "en", []Block{{BlockIndex: 0, SourceText: sentence.source}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input = attachWholeBlockAnchors(t, input)

	missingWord := parityResponse(t, sentence, input)
	missingWord.Tokens = missingWord.Tokens[1:]
	if _, err := ValidateResponse(input, missingWord); err == nil {
		t.Fatal("response missing a word unexpectedly accepted")
	}

	unknownMember := parityResponse(t, sentence, input)
	unknownMember.Constructions[0].TokenIDs = append(unknownMember.Constructions[0].TokenIDs, "b0:t999")
	if _, err := ValidateResponse(input, unknownMember); err == nil {
		t.Fatal("construction referencing an unknown token unexpectedly accepted")
	}
}

// TestTranslationStageAcceptsSameSpellingSenses proves the two-stage path
// keeps the same rule: a same-spelling subtitle passes exactly when the
// referenced sense's English translation is spelled the same.
func TestTranslationStageAcceptsSameSpellingSenses(t *testing.T) {
	build := func(source, canonical, discriminator, primary, shadow string) (PreparedChunk, *ValidatedLinguistic, TranslationArtifact) {
		input, err := Prepare("Stages", "nl", "en", []Block{{BlockIndex: 0, SourceText: source}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		input = attachWholeBlockAnchors(t, input)
		chunk, err := PrepareChunk(input, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		artifact := LinguisticArtifact{Version: pipeline.LinguisticContractVersion,
			NewSenses: []LinguisticNewSense{{
				Ref: "sense", Kind: KindWord, CanonicalForm: canonical, NormalizedForm: canonical,
				Lemma: canonical, SenseDiscriminator: discriminator, CanonicalPronunciationText: canonical,
			}},
		}
		for _, token := range chunk.Tokens {
			result := LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000}
			if token.NormalizedForm == canonical {
				result.Classification = "lexical"
				result.NewSenseRef = "sense"
			}
			artifact.Tokens = append(artifact.Tokens, result)
		}
		validated, err := ValidateLinguistic(chunk, artifact)
		if err != nil {
			t.Fatalf("linguistic artifact rejected: %v", err)
		}
		translation := TranslationArtifact{Version: pipeline.TranslationContractVersion}
		for _, token := range validated.Tokens {
			item := TranslationTokenResult{TokenID: token.TokenID}
			if token.NewSenseRef == "sense" {
				item.ShadowText = shadow
			}
			translation.Tokens = append(translation.Tokens, item)
		}
		translation.NewSenses = []TranslationNewSense{{Ref: "sense", PrimaryTranslation: primary}}
		return chunk, validated, translation
	}

	// Dutch plan with the English sense plan: same-spelling subtitle accepted.
	chunk, linguistic, translation := build("Haar plan werkt.", "plan", "scheme", "plan", "plan")
	if err := ValidateTranslation(chunk, linguistic, translation); err != nil {
		t.Fatalf("legitimate same-spelling subtitle rejected: %v", err)
	}
	merged, err := MergeLinguisticTranslation(linguistic, translation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateChunkResponse(chunk, merged); err != nil {
		t.Fatalf("merged same-spelling response failed v3 validation: %v", err)
	}

	// Dutch bank with the sofa sense: the same-spelling subtitle is an
	// untranslated copy and must enter correction.
	chunk, linguistic, translation = build("De bank staat daar.", "bank", "sofa", "sofa", "bank")
	if err := ValidateTranslation(chunk, linguistic, translation); err == nil {
		t.Fatal("sofa sense with same-spelling subtitle unexpectedly accepted")
	}
}

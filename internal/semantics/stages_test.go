package semantics

import (
	"strings"
	"testing"

	"doublangu/internal/pipeline"
)

// linguisticFixtureParagraph builds a two-sentence paragraph chunk and a
// fully valid linguistic artifact for it.
func linguisticFixtureParagraph(t *testing.T) (PreparedChunk, *ValidatedLinguistic) {
	t.Helper()
	input, err := Prepare("Stages", "nl", "en", []Block{
		{BlockIndex: 0, SourceText: "Hij gooit het bijltje erbij neer. Zij kijkt uit."},
	}, []SenseCandidate{
		{ID: "sense-bank", SemanticItemID: "item-bank", SourceLanguage: "nl", TargetLanguage: "en", Kind: KindWord, CanonicalForm: "bank", NormalizedForm: "bank", PrimaryTranslation: "bench", SenseDiscriminator: "furniture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	input = attachWholeBlockAnchors(t, input)
	chunk, err := PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact := LinguisticArtifact{
		Version: pipeline.LinguisticContractVersion,
		NewSenses: []LinguisticNewSense{
			{
				Ref: "gooi-ref", Kind: KindExpression, CanonicalForm: "het bijltje erbij neergooien",
				NormalizedForm: "het bijltje erbij neergooien", Lemma: "het bijltje erbij neergooien",
				SenseDiscriminator: "resign", MeaningNote: "to give up", CanonicalPronunciationText: "het bijltje erbij neergooien",
			},
			{
				Ref: "bijltje-sense", Kind: KindWord, CanonicalForm: "bijltje", NormalizedForm: "bijltje",
				Lemma: "bijltje", SenseDiscriminator: "tool", MeaningNote: "a small axe", CanonicalPronunciationText: "bijltje",
			},
		},
		Constructions: []LinguisticConstruction{{
			Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "gooi-ref",
			CanonicalPronunciationText: "het bijltje erbij neergooien", ConfidenceMilli: 900,
			TokenIDs: []string{"b0:t1", "b0:t3", "b0:t5"},
			Spans: []SpanRef{
				{BlockIndex: 0, SourceText: "gooit", Occurrence: 0},
				{BlockIndex: 0, SourceText: "bijltje", Occurrence: 0},
				{BlockIndex: 0, SourceText: "neer", Occurrence: 0},
			},
		}},
	}
	for _, token := range chunk.Tokens {
		result := LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000}
		if token.NormalizedForm == "bijltje" {
			result.Classification = "lexical"
			result.NewSenseRef = "bijltje-sense"
		}
		artifact.Tokens = append(artifact.Tokens, result)
	}
	validated, err := ValidateLinguistic(chunk, artifact)
	if err != nil {
		t.Fatalf("valid linguistic artifact rejected: %v", err)
	}
	return chunk, validated
}

func validTranslationFixture(chunk PreparedChunk, linguistic *ValidatedLinguistic) TranslationArtifact {
	artifact := TranslationArtifact{Version: pipeline.TranslationContractVersion}
	for _, token := range linguistic.Tokens {
		shadow := ""
		if token.Classification != "unchanged" {
			shadow = "little axe"
		}
		artifact.Tokens = append(artifact.Tokens, TranslationTokenResult{TokenID: token.TokenID, ShadowText: shadow})
	}
	translations := map[string]string{"gooi-ref": "give up", "bijltje-sense": "little axe"}
	for _, sense := range linguistic.NewSenses {
		artifact.NewSenses = append(artifact.NewSenses, TranslationNewSense{
			Ref: sense.Ref, PrimaryTranslation: translations[sense.Ref], Alternatives: []string{"throw in the towel"},
		})
	}
	for _, construction := range linguistic.Constructions {
		artifact.Constructions = append(artifact.Constructions, TranslationConstruction{
			ConstructionID: construction.ConstructionID, ShadowText: "give up",
		})
	}
	return artifact
}

func TestLinguisticArtifactRejectsTranslationOwnedFields(t *testing.T) {
	raw := `{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[],"shadow_text":"x"}`
	if _, err := DecodeLinguisticArtifact([]byte(raw)); err == nil {
		t.Fatal("linguistic artifact with shadow_text accepted")
	}
	raw = `{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[{"shadow_text":"x"}]}`
	if _, err := DecodeLinguisticArtifact([]byte(raw)); err == nil {
		t.Fatal("linguistic construction with shadow_text accepted")
	}
	wrongVersion := LinguisticArtifact{Version: AnalysisContractVersion}
	if _, err := ValidateLinguistic(PreparedChunk{}, wrongVersion); err == nil || !strings.Contains(err.Error(), "unsupported linguistic artifact version") {
		t.Fatalf("wrong linguistic version error = %v", err)
	}
	raw = `{"version":"reader.translation.v1","tokens":[{"token_id":"t","shadow_text":"x","classification":"word"}],"new_senses":[],"constructions":[]}`
	if _, err := DecodeTranslationArtifact([]byte(raw)); err == nil {
		t.Fatal("translation token with classification accepted")
	}
	raw = `{"version":"reader.translation.v1","tokens":[],"new_senses":[],"constructions":[],"tokens_extra":1}`
	if _, err := DecodeTranslationArtifact([]byte(raw)); err == nil {
		t.Fatal("unknown translation field accepted")
	}
}

func TestValidateLinguisticAssignsSortedConstructionIDs(t *testing.T) {
	chunk, validated := linguisticFixtureParagraph(t)
	if len(validated.Constructions) != 1 {
		t.Fatalf("constructions = %+v", validated.Constructions)
	}
	construction := validated.Constructions[0]
	if construction.ConstructionID != "b0:c0" {
		t.Fatalf("construction id = %q", construction.ConstructionID)
	}
	_ = chunk

	// A second construction appearing later in source must be sorted after
	// the first even when the provider lists it first.
	input, err := Prepare("Stages", "nl", "en", []Block{{BlockIndex: 0, SourceText: "Hij gooit het op. Hij kijkt uit."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input = attachWholeBlockAnchors(t, input)
	twoChunk, err := PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact := LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	artifact.Tokens = make([]LinguisticTokenResult, 0, len(twoChunk.Tokens))
	for _, token := range twoChunk.Tokens {
		artifact.Tokens = append(artifact.Tokens, LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
	artifact.NewSenses = []LinguisticNewSense{
		{Ref: "give-up", Kind: KindExpression, CanonicalForm: "opgeven", NormalizedForm: "opgeven", SenseDiscriminator: "resign", CanonicalPronunciationText: "opgeven"},
		{Ref: "look-out", Kind: KindExpression, CanonicalForm: "uitkijken", NormalizedForm: "uitkijken", SenseDiscriminator: "watch", CanonicalPronunciationText: "uitkijken"},
	}
	// Provider order: look-out (later in source) first.
	artifact.Constructions = []LinguisticConstruction{
		{Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "look-out", ConfidenceMilli: 900, TokenIDs: []string{"b0:t5", "b0:t6"}, Spans: []SpanRef{{BlockIndex: 0, SourceText: "kijkt uit", Occurrence: 0}}},
		{Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "give-up", ConfidenceMilli: 900, TokenIDs: []string{"b0:t1", "b0:t3"}, Spans: []SpanRef{{BlockIndex: 0, SourceText: "gooit", Occurrence: 0}, {BlockIndex: 0, SourceText: "op", Occurrence: 0}}},
	}
	twoValidated, err := ValidateLinguistic(twoChunk, artifact)
	if err != nil {
		t.Fatalf("two-construction artifact rejected: %v", err)
	}
	if len(twoValidated.Constructions) != 2 {
		t.Fatalf("constructions = %+v", twoValidated.Constructions)
	}
	if twoValidated.Constructions[0].Construction.TokenIDs[0] != "b0:t1" || twoValidated.Constructions[1].Construction.TokenIDs[0] != "b0:t5" {
		t.Fatalf("constructions not sorted by first member: %+v", twoValidated.Constructions)
	}
	if twoValidated.Constructions[0].ConstructionID != "b0:c0" || twoValidated.Constructions[1].ConstructionID != "b0:c1" {
		t.Fatalf("construction ids = %q/%q", twoValidated.Constructions[0].ConstructionID, twoValidated.Constructions[1].ConstructionID)
	}
}

func TestValidateLinguisticRejectsCoverageAndMembershipViolations(t *testing.T) {
	chunk, _ := linguisticFixtureParagraph(t)

	missing := LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		missing.Tokens = append(missing.Tokens, LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
	missing.Tokens = missing.Tokens[1:]
	if _, err := ValidateLinguistic(chunk, missing); err == nil {
		t.Fatal("missing token coverage accepted")
	}

	duplicate := LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		duplicate.Tokens = append(duplicate.Tokens, LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
	duplicate.Tokens = append(duplicate.Tokens, duplicate.Tokens[0])
	if _, err := ValidateLinguistic(chunk, duplicate); err == nil {
		t.Fatal("duplicate token accepted")
	}

	sensedUnchanged := LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		sensedUnchanged.Tokens = append(sensedUnchanged.Tokens, LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
	sensedUnchanged.NewSenses = []LinguisticNewSense{{
		Ref: "een-article", Kind: KindWord, CanonicalForm: "hij", NormalizedForm: "hij",
		SenseDiscriminator: "article", CanonicalPronunciationText: "hij",
	}}
	sensedUnchanged.Tokens[0].NewSenseRef = "een-article"
	if _, err := ValidateLinguistic(chunk, sensedUnchanged); err == nil {
		t.Fatal("sensed unchanged token accepted")
	}

	splitRun := LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		splitRun.Tokens = append(splitRun.Tokens, LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
	splitRun.NewSenses = []LinguisticNewSense{{
		Ref: "run-fast", Kind: KindExpression, CanonicalForm: "uitkijken", NormalizedForm: "uitkijken",
		SenseDiscriminator: "watch", CanonicalPronunciationText: "uitkijken",
	}}
	splitRun.Constructions = []LinguisticConstruction{{
		Kind: KindExpression, Role: "contiguous_construction", NewSenseRef: "run-fast", ConfidenceMilli: 900,
		TokenIDs: []string{"b0:t4", "b0:t6"},
		Spans:    []SpanRef{{BlockIndex: 0, SourceText: "Zij kijkt uit", Occurrence: 0}},
	}}
	if _, err := ValidateLinguistic(chunk, splitRun); err == nil {
		t.Fatal("split-run contiguous construction accepted")
	}

	crossSentence := LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		crossSentence.Tokens = append(crossSentence.Tokens, LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	}
	crossSentence.NewSenses = []LinguisticNewSense{{
		Ref: "mix", Kind: KindExpression, CanonicalForm: "gooien kijken", NormalizedForm: "gooien kijken",
		SenseDiscriminator: "test", CanonicalPronunciationText: "gooien kijken",
	}}
	crossSentence.Constructions = []LinguisticConstruction{{
		Kind: KindExpression, Role: "discontinuous_construction", NewSenseRef: "mix", ConfidenceMilli: 900,
		TokenIDs: []string{"b0:t1", "b0:t5"},
		Spans: []SpanRef{
			{BlockIndex: 0, SourceText: "gooit", Occurrence: 0},
			{BlockIndex: 0, SourceText: "kijkt", Occurrence: 0},
		},
	}}
	if _, err := ValidateLinguistic(chunk, crossSentence); err == nil {
		t.Fatal("cross-sentence construction accepted")
	}
}

func TestValidateTranslationEnforcesExactCorrespondence(t *testing.T) {
	chunk, linguistic := linguisticFixtureParagraph(t)

	valid := validTranslationFixture(chunk, linguistic)
	if err := ValidateTranslation(chunk, linguistic, valid); err != nil {
		t.Fatalf("valid translation rejected: %v", err)
	}

	missingToken := valid
	missingToken.Tokens = missingToken.Tokens[1:]
	if err := ValidateTranslation(chunk, linguistic, missingToken); err == nil {
		t.Fatal("missing translation token accepted")
	}

	extraToken := valid
	extraToken.Tokens = append(append([]TranslationTokenResult(nil), valid.Tokens...), TranslationTokenResult{TokenID: "b0:t99", ShadowText: "x"})
	if err := ValidateTranslation(chunk, linguistic, extraToken); err == nil {
		t.Fatal("extra translation token accepted")
	}

	duplicateConstruction := valid
	duplicateConstruction.Constructions = append(append([]TranslationConstruction(nil), valid.Constructions...), valid.Constructions[0])
	if err := ValidateTranslation(chunk, linguistic, duplicateConstruction); err == nil {
		t.Fatal("duplicate translation construction accepted")
	}

	blankOrdinary := valid
	for index := range blankOrdinary.Tokens {
		if blankOrdinary.Tokens[index].ShadowText != "" {
			blankOrdinary.Tokens[index].ShadowText = ""
			break
		}
	}
	if err := ValidateTranslation(chunk, linguistic, blankOrdinary); err == nil {
		t.Fatal("blank ordinary translation accepted")
	}

	unchangedTranslation := valid
	unchangedTranslation.Tokens[0].ShadowText = "a real translation"
	if err := ValidateTranslation(chunk, linguistic, unchangedTranslation); err == nil {
		t.Fatal("translated unchanged token accepted")
	}

	dutchCopy := valid
	for index := range dutchCopy.Tokens {
		if dutchCopy.Tokens[index].ShadowText == "little axe" {
			dutchCopy.Tokens[index].ShadowText = "bijltje"
			break
		}
	}
	if err := ValidateTranslation(chunk, linguistic, dutchCopy); err == nil {
		t.Fatal("Dutch source-copy translation accepted")
	}

	constructionCopy := valid
	constructionCopy.Constructions[0].ShadowText = "gooit bijltje neer"
	if err := ValidateTranslation(chunk, linguistic, constructionCopy); err == nil {
		t.Fatal("construction translation copying members accepted")
	}

	blankConstruction := valid
	blankConstruction.Constructions[0].ShadowText = ""
	if err := ValidateTranslation(chunk, linguistic, blankConstruction); err == nil {
		t.Fatal("blank construction translation accepted")
	}

	missingSense := valid
	missingSense.NewSenses = nil
	if err := ValidateTranslation(chunk, linguistic, missingSense); err == nil {
		t.Fatal("missing new sense translation accepted")
	}

	badAlternatives := valid
	badAlternatives.NewSenses[0].Alternatives = []string{"give up", "give up"}
	if err := ValidateTranslation(chunk, linguistic, badAlternatives); err == nil {
		t.Fatal("duplicated alternative accepted")
	}
	tooMany := valid
	tooMany.NewSenses[0].Alternatives = []string{"a", "b", "c", "d"}
	if err := ValidateTranslation(chunk, linguistic, tooMany); err == nil {
		t.Fatal("too many alternatives accepted")
	}
}

func TestMergeLinguisticTranslationPassesChunkValidation(t *testing.T) {
	chunk, linguistic := linguisticFixtureParagraph(t)
	translation := validTranslationFixture(chunk, linguistic)
	merged, err := MergeLinguisticTranslation(linguistic, translation)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if merged.Version != AnalysisContractVersion {
		t.Fatalf("merged version = %q", merged.Version)
	}
	if len(merged.Tokens) != len(chunk.Tokens) || len(merged.Constructions) != 1 || len(merged.NewSenses) != 2 {
		t.Fatalf("merged = %d tokens %d constructions %d senses", len(merged.Tokens), len(merged.Constructions), len(merged.NewSenses))
	}
	// Identity is unchanged: token ids/spans and construction members equal
	// the linguistic artifact exactly.
	for index, token := range merged.Tokens {
		if token.TokenID != chunk.Tokens[index].ID {
			t.Fatalf("token order changed at %d", index)
		}
	}
	construction := merged.Constructions[0]
	if len(construction.TokenIDs) != 3 || construction.TokenIDs[0] != "b0:t1" {
		t.Fatalf("merged construction = %+v", construction)
	}
	if _, err := ValidateChunkResponse(chunk, merged); err != nil {
		t.Fatalf("merged response failed v3 validation: %v", err)
	}
	// A construction whose translation is missing fails the merge defensively.
	broken := translation
	broken.Constructions = nil
	if _, err := MergeLinguisticTranslation(linguistic, broken); err == nil {
		t.Fatal("merge with missing construction translation succeeded")
	}
}

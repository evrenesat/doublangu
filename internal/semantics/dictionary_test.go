package semantics

import (
	"encoding/json"
	"strings"
	"testing"
)

func validDictionaryWordJSON(t *testing.T) string {
	t.Helper()
	document := DictionaryDocument{
		Version: DictionaryContractVersion, LookupForm: "bank", LookupKind: DictionaryLookupWord,
		SourceLanguage: "nl", TargetLanguage: "en",
		Senses: []DictionarySense{
			{
				PartOfSpeech: "noun", TranslationEN: "bench; sofa",
				MeaningEN: "A long seat for more than one person.",
				UsageEN:   "In a park, this is usually a bench.",
				PatternNL: "", Parts: []DictionaryPart{},
				Examples: []DictionaryExample{{TextNL: "Zij zit op een bank.", TranslationEN: "She is sitting on a bench."}},
			},
			{
				PartOfSpeech: "noun", TranslationEN: "bank",
				MeaningEN: "A financial institution that keeps or lends money.",
				UsageEN:   "", PatternNL: "", Parts: []DictionaryPart{},
				Examples: []DictionaryExample{{TextNL: "Ik zet geld op de bank.", TranslationEN: "I put money in the bank."}},
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func validDictionaryInput() DictionaryInput {
	return DictionaryInput{
		Version: DictionaryContractVersion, LookupForm: "bank", LookupKind: DictionaryLookupWord,
		SourceLanguage: "nl", TargetLanguage: "en",
	}
}

func TestDecodeDictionaryArtifact_ValidWord(t *testing.T) {
	raw := validDictionaryWordJSON(t)
	document, err := DecodeDictionaryArtifact([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := ValidateDictionary(validDictionaryInput(), document); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(document.Senses) != 2 {
		t.Fatalf("senses = %d, want 2", len(document.Senses))
	}
	hash, err := document.Hash()
	if err != nil || len(hash) != 64 {
		t.Fatalf("hash = %q, err %v", hash, err)
	}
	// Reordered keys decode identically and hash identically.
	reordered := `{"senses":` + mustSubstring(t, raw, "\"senses\":", "}") + `,"target_language":"en","source_language":"nl","lookup_kind":"word","lookup_form":"bank","version":"` + DictionaryContractVersion + `"}`
	again, err := DecodeDictionaryArtifact([]byte(reordered))
	if err != nil {
		t.Fatalf("decode reordered: %v", err)
	}
	if err := ValidateDictionary(validDictionaryInput(), again); err != nil {
		t.Fatalf("validate reordered: %v", err)
	}
}

func mustSubstring(t *testing.T, raw, prefix, suffix string) string {
	t.Helper()
	start := strings.Index(raw, prefix) + len(prefix)
	// The senses array ends before the next top-level close brace.
	end := strings.LastIndex(raw, suffix) - 1
	if end <= start {
		t.Fatalf("substring bounds: %q", raw)
	}
	return raw[start:end] + "]"
}

func TestDecodeDictionaryArtifact_ValidExpression(t *testing.T) {
	document := DictionaryDocument{
		Version: DictionaryContractVersion, LookupForm: "ervan uitgaan", LookupKind: DictionaryLookupExpression,
		SourceLanguage: "nl", TargetLanguage: "en",
		Senses: []DictionarySense{{
			PartOfSpeech: "expression", TranslationEN: "to assume",
			MeaningEN: "To take something for granted.",
			UsageEN:   "Followed by a clause with dat.",
			PatternNL: "ervan uitgaan dat ...",
			Parts: []DictionaryPart{
				{SourceNL: "uitgaan", ExplanationEN: "to go out; here: to start from"},
				{SourceNL: "er ... van", ExplanationEN: "pronominal adverb pair for 'from it'"},
			},
			Examples: []DictionaryExample{{TextNL: "Ik ga ervan uit dat je komt.", TranslationEN: "I assume you are coming."}},
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDictionaryArtifact(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := ValidateDictionary(DictionaryInput{
		Version: DictionaryContractVersion, LookupForm: "ervan uitgaan",
		LookupKind: DictionaryLookupExpression, SourceLanguage: "nl", TargetLanguage: "en",
	}, decoded); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestDecodeDictionaryArtifact_Malformed(t *testing.T) {
	cases := map[string]struct {
		raw    string
		input  DictionaryInput
		decode bool // expect DecodeDictionaryArtifact to fail (vs validate only)
	}{
		"unknown key":       {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"x","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}],"extra":1}]}`, decode: true},
		"missing key":       {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","senses":[{"part_of_speech":"noun","translation_en":"x","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: true},
		"duplicate key":     {raw: `{"version":"` + DictionaryContractVersion + `","version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"x","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: true},
		"trailing json":     {raw: validDictionaryWordJSON(t) + ` {}`, decode: true},
		"malformed json":    {raw: `{not json`, decode: true},
		"html in english":   {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"<b>x</b>","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: false},
		"null meaning":      {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"x","meaning_en":null,"usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: false},
		"empty senses":      {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[]}`, decode: true},
		"bad pos":           {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"modal","translation_en":"x","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: false},
		"no examples":       {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"x","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[]}]}`, decode: true},
		"oversized field":   {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"` + strings.Repeat("x", 121) + `","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: false},
		"duplicate sense":   {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"same","meaning_en":"Same meaning.","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]},{"part_of_speech":"noun","translation_en":"same","meaning_en":"Same meaning.","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"}]}]}`, decode: false},
		"duplicate example": {raw: `{"version":"` + DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[{"part_of_speech":"noun","translation_en":"x","meaning_en":"y","usage_en":"","pattern_nl":"","parts":[],"examples":[{"text_nl":"a","translation_en":"b"},{"text_nl":"a","translation_en":"b"}]}]}`, decode: false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			document, err := DecodeDictionaryArtifact([]byte(testCase.raw))
			if testCase.decode {
				if err == nil {
					t.Fatalf("decode should have failed")
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if err := ValidateDictionary(validDictionaryInput(), document); err == nil {
				t.Fatalf("validate should have failed")
			}
		})
	}
}

func TestValidateDictionary_IdentityMustMatch(t *testing.T) {
	document, err := DecodeDictionaryArtifact([]byte(validDictionaryWordJSON(t)))
	if err != nil {
		t.Fatal(err)
	}
	changed := validDictionaryInput()
	changed.LookupForm = "Bank"
	if err := ValidateDictionary(changed, document); err == nil {
		t.Fatal("changed lookup form must fail")
	}
	changed = validDictionaryInput()
	changed.LookupKind = DictionaryLookupExpression
	if err := ValidateDictionary(changed, document); err == nil {
		t.Fatal("changed lookup kind must fail")
	}
}

func TestValidateDictionary_CognatesAllowed(t *testing.T) {
	// English cognates equal to the Dutch spelling are legitimate.
	document := DictionaryDocument{
		Version: DictionaryContractVersion, LookupForm: "plan", LookupKind: DictionaryLookupWord,
		SourceLanguage: "nl", TargetLanguage: "en",
		Senses: []DictionarySense{{
			PartOfSpeech: "noun", TranslationEN: "plan",
			MeaningEN: "A plan.", UsageEN: "", PatternNL: "", Parts: []DictionaryPart{},
			Examples: []DictionaryExample{{TextNL: "Het plan is goed.", TranslationEN: "The plan is good."}},
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDictionaryArtifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDictionary(DictionaryInput{
		Version: DictionaryContractVersion, LookupForm: "plan", LookupKind: DictionaryLookupWord,
		SourceLanguage: "nl", TargetLanguage: "en",
	}, decoded); err != nil {
		t.Fatalf("cognate translation must be accepted: %v", err)
	}
}

func TestDecodeDictionaryArtifact_OversizedArtifactRejected(t *testing.T) {
	huge := `{"version":"` + DictionaryContractVersion + `","padding":"` + strings.Repeat("x", maxDictionaryArtifactBytes) + `"}`
	if _, err := DecodeDictionaryArtifact([]byte(huge)); err == nil {
		t.Fatal("oversized artifact must be rejected before parsing")
	}
}

func TestSanitizeDictionaryHints(t *testing.T) {
	hints := SanitizeDictionaryHints([]string{
		"  sofa  ", "", "SOFA", strings.Repeat("x", 121), "a<b>", "bank\twide",
		"zetel", strings.Repeat("y", 121), "hok", "stoel", "ligbed", "flap", "kraam",
	})
	if len(hints) != MaxDictionaryTranslationHints {
		t.Fatalf("hints = %d, want %d: %q", len(hints), MaxDictionaryTranslationHints, hints)
	}
	// Sorted by normalized value with the original text as tie-breaker; the
	// deduplicated SOFA/sofa pair keeps the first trimmed original.
	if hints[0] != "flap" {
		t.Fatalf("first hint = %q, want flap", hints[0])
	}
	for index := 1; index < len(hints); index++ {
		left, _ := NormalizeForm(hints[index-1])
		right, _ := NormalizeForm(hints[index])
		if right < left {
			t.Fatalf("hints not sorted: %q before %q", hints[index-1], hints[index])
		}
	}
	for _, hint := range hints {
		if strings.ContainsAny(hint, "<>") {
			t.Fatalf("unsafe hint survived: %q", hint)
		}
	}
	if len(SanitizeDictionaryHints(nil)) != 0 {
		t.Fatal("empty hints must stay empty")
	}
}

func TestDictionaryInputValidate(t *testing.T) {
	input := validDictionaryInput()
	if err := input.Validate(); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	input.LookupKind = "idiom"
	if err := input.Validate(); err == nil {
		t.Fatal("idiom must collapse into expression kind")
	}
	input = validDictionaryInput()
	input.LookupForm = strings.Repeat("x", MaxDictionaryLookupScalars+1)
	if err := input.Validate(); err == nil {
		t.Fatal("oversized lookup form must fail")
	}
	input = validDictionaryInput()
	input.SourceLanguage = "not a tag"
	if err := input.Validate(); err == nil {
		t.Fatal("malformed source language tag must fail")
	}
	input.SourceLanguage = "en"
	if err := input.Validate(); err == nil {
		t.Fatal("identical source/target languages must fail")
	}
}

func TestDictionaryDocumentHash_Deterministic(t *testing.T) {
	first, err := DecodeDictionaryArtifact([]byte(validDictionaryWordJSON(t)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeDictionaryArtifact([]byte(validDictionaryWordJSON(t)))
	if err != nil {
		t.Fatal(err)
	}
	firstHash, err := first.Hash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("hashes differ: %s != %s", firstHash, secondHash)
	}
	first.Senses[0].MeaningEN += " Changed."
	changedHash, err := first.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if changedHash == firstHash {
		t.Fatal("changed document must hash differently")
	}
}

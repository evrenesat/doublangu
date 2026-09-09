package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"doublangu/internal/config"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
)

func dictionaryWordInput() semantics.DictionaryInput {
	return semantics.DictionaryInput{
		Version: semantics.DictionaryContractVersion, LookupForm: "bank",
		LookupKind: semantics.DictionaryLookupWord, SourceLanguage: "nl", TargetLanguage: "en",
		KnownTranslations: semantics.SanitizeDictionaryHints([]string{"sofa", "bench"}),
	}
}

func dictionaryValidResponse(input semantics.DictionaryInput) string {
	document := semantics.DictionaryDocument{
		Version: input.Version, LookupForm: input.LookupForm, LookupKind: input.LookupKind,
		SourceLanguage: input.SourceLanguage, TargetLanguage: input.TargetLanguage,
		Senses: []semantics.DictionarySense{{
			PartOfSpeech: "noun", TranslationEN: "bench",
			MeaningEN: "A long seat.", UsageEN: "Common in parks.", PatternNL: "",
			Parts: []semantics.DictionaryPart{},
			Examples: []semantics.DictionaryExample{
				{TextNL: "Zij zit op een bank.", TranslationEN: "She sits on a bench."},
			},
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func dictionaryBinding(t *testing.T) ResolvedBinding {
	t.Helper()
	options, err := config.CanonicalizeProviderOptions(ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	return ResolvedBinding{
		StageID: pipeline.StageTranslation, ProviderID: "codex-app-server",
		ProviderType: ProviderTypeCodexAppServer, ConfigFingerprint: "fp", ModelID: "test-model",
		Options: options, OptionsHash: hash,
		ContractVersion: pipeline.TranslationContractVersion, PromptVersion: pipeline.TranslationPromptVersion,
	}
}

func TestDictionaryPrompt_InstructionsAndQuotedInput(t *testing.T) {
	input := dictionaryWordInput()
	prompt, err := DictionaryPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"translation_en, meaning_en, usage_en and each explanation_en in plain,",
		"Do not use HTML,",
		"one to six common, genuinely distinct meanings",
		"Do not change the lookup identity",
		"Known translations are hints",
		"INPUT_DATA_BEGIN",
		`"lookup_form":"bank"`,
		`"known_translations":["bench","sofa"]`,
		"INPUT_DATA_END",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("dictionary prompt missing %q", expected)
		}
	}
	if strings.Contains(prompt, "meaning_note") || strings.Contains(prompt, "usage_note") || strings.Contains(prompt, "parts_note") {
		t.Error("dictionary prompt must not carry old note fields")
	}
}

func TestDictionaryPrompt_RejectsInvalidInput(t *testing.T) {
	input := dictionaryWordInput()
	input.LookupForm = ""
	if _, err := DictionaryPrompt(input); err == nil {
		t.Fatal("empty lookup form must fail before any provider call")
	}
}

func TestDictionaryOutputSchema_ClosedAndBounded(t *testing.T) {
	var schema struct {
		AdditionalProperties bool           `json:"additionalProperties"`
		Required             []string       `json:"required"`
		Properties           map[string]any `json:"properties"`
		Senses               map[string]any `json:"-"`
	}
	raw := DictionaryOutputSchema()
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties {
		t.Fatal("top-level object must be closed")
	}
	sensesRaw, err := json.Marshal(schema.Properties["senses"])
	if err != nil {
		t.Fatal(err)
	}
	var senses struct {
		MinItems int            `json:"minItems"`
		MaxItems int            `json:"maxItems"`
		Items    map[string]any `json:"items"`
	}
	if err := json.Unmarshal(sensesRaw, &senses); err != nil {
		t.Fatal(err)
	}
	if senses.MinItems != 1 || senses.MaxItems != semantics.MaxDictionarySenses {
		t.Fatalf("senses bounds = %d-%d", senses.MinItems, senses.MaxItems)
	}
	itemsRaw, err := json.Marshal(senses.Items)
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		AdditionalProperties bool           `json:"additionalProperties"`
		Required             []string       `json:"required"`
		Properties           map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(itemsRaw, &item); err != nil {
		t.Fatal(err)
	}
	if item.AdditionalProperties {
		t.Fatal("sense objects must be closed")
	}
	posRaw, err := json.Marshal(item.Properties["part_of_speech"])
	if err != nil {
		t.Fatal(err)
	}
	var pos struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(posRaw, &pos); err != nil {
		t.Fatal(err)
	}
	if len(pos.Enum) != 13 {
		t.Fatalf("part-of-speech enum = %v", pos.Enum)
	}
}

func TestGenerateDictionary_Success(t *testing.T) {
	input := dictionaryWordInput()
	provider := &scriptedSessionProvider{descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true}, turns: []string{dictionaryValidResponse(input)}}
	result, err := GenerateDictionary(context.Background(), provider, dictionaryBinding(t), input)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(result.Document.Senses) != 1 || result.Document.Senses[0].TranslationEN != "bench" {
		t.Fatalf("document = %+v", result.Document)
	}
	if result.Attempt.ReportedModel != "test-model" {
		t.Fatalf("attempt model = %q", result.Attempt.ReportedModel)
	}
	if len(provider.schemas) != 1 {
		t.Fatalf("schema turns = %d", len(provider.schemas))
	}
}

func TestGenerateDictionary_CorrectiveTurnRepairs(t *testing.T) {
	input := dictionaryWordInput()
	broken := strings.Replace(dictionaryValidResponse(input), `"meaning_en":"A long seat."`, `"meaning_en":"A long seat.","oops":1`, 1)
	provider := &scriptedSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []string{broken, dictionaryValidResponse(input)},
	}
	result, err := GenerateDictionary(context.Background(), provider, dictionaryBinding(t), input)
	if err != nil {
		t.Fatalf("generate with correction: %v", err)
	}
	if len(result.Document.Senses) != 1 {
		t.Fatalf("document = %+v", result.Document)
	}
	if len(provider.prompts) != 2 {
		t.Fatalf("turns = %d, want 2", len(provider.prompts))
	}
	if !strings.Contains(provider.prompts[1], "VALIDATION_ERRORS_BEGIN") {
		t.Fatal("second turn must be the corrective prompt")
	}
	if len(result.Attempt.Turns) != 2 || result.Attempt.Turns[1].TurnKind != "corrective" {
		t.Fatalf("turn kinds = %+v", result.Attempt.Turns)
	}
}

func TestGenerateDictionary_CorrectionExhaustionFails(t *testing.T) {
	input := dictionaryWordInput()
	broken := `{"version":"` + semantics.DictionaryContractVersion + `","lookup_form":"bank","lookup_kind":"word","source_language":"nl","target_language":"en","senses":[]}`
	provider := &scriptedSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []string{broken, broken, broken},
	}
	result, err := GenerateDictionary(context.Background(), provider, dictionaryBinding(t), input)
	if err == nil {
		t.Fatal("exhausted corrections must fail")
	}
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Code != CodeInvalidOutput {
		t.Fatalf("error = %v", err)
	}
	// Result-plus-error: the retained attempt (every executed turn) survives
	// the failure so diagnostics keep the evidence.
	if result == nil {
		t.Fatal("result-plus-error contract broken: nil result on failure")
	}
	if len(result.Attempt.Turns) != 3 {
		t.Fatalf("retained turns = %d, want 3 (initial + 2 corrections)", len(result.Attempt.Turns))
	}
	if len(provider.prompts) != 3 {
		t.Fatalf("turns = %d, want 3 (initial + 2 corrections)", len(provider.prompts))
	}
}

func TestGenerateDictionary_WrongIdentityRejected(t *testing.T) {
	input := dictionaryWordInput()
	wrong := strings.Replace(dictionaryValidResponse(input), `"lookup_form":"bank"`, `"lookup_form":"banktje"`, 1)
	provider := &scriptedSessionProvider{descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true}, turns: []string{wrong}}
	if _, err := GenerateDictionary(context.Background(), provider, dictionaryBinding(t), input); err == nil {
		t.Fatal("changed lookup identity must fail validation")
	}
}

// TestGenerateDictionary_TransportFamiliesShareValidationBoundary proves the
// openai-compatible and mac_relay transport descriptors reach the same local
// dictionary validation through the shared executor and scripted sessions.
func TestGenerateDictionary_TransportFamiliesShareValidationBoundary(t *testing.T) {
	input := dictionaryWordInput()
	for _, family := range []string{ProviderTypeOpenAICompatible, ProviderTypeMacRelay, ProviderTypeCodexAppServer} {
		t.Run(family, func(t *testing.T) {
			provider := &scriptedSessionProvider{
				descriptor: ProviderDescriptor{ID: "family-" + family, Type: family, Enabled: true},
				turns:      []string{dictionaryValidResponse(input)},
			}
			result, err := GenerateDictionary(context.Background(), provider, dictionaryBinding(t), input)
			if err != nil {
				t.Fatalf("generate via %s: %v", family, err)
			}
			if result.Document.Senses[0].TranslationEN != "bench" {
				t.Fatalf("family %s document = %+v", family, result.Document)
			}
		})
	}
}

// TestLiveDictionary exercises the real configured provider for the actual
// dictionary contract. It is opt-in through DOUBLANGU_TEST_CODEX_LIVE and
// requires the owner's configured model/effort inputs; it never persists.
func TestLiveDictionary(t *testing.T) {
	if os.Getenv("DOUBLANGU_TEST_CODEX_LIVE") != "1" {
		t.Skip("set DOUBLANGU_TEST_CODEX_LIVE=1 to run the live dictionary contract test")
	}
	model := os.Getenv("DOUBLANGU_TEST_CODEX_MODEL")
	effort := os.Getenv("DOUBLANGU_TEST_CODEX_EFFORT")
	if strings.TrimSpace(model) == "" {
		t.Fatal("DOUBLANGU_TEST_CODEX_MODEL must be set to the actual authorized model")
	}
	options, err := config.CanonicalizeProviderOptions(ProviderTypeCodexAppServer, json.RawMessage(fmt.Sprintf(`{"reasoning_effort":%q}`, effort)))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	binding := ResolvedBinding{
		StageID: pipeline.StageTranslation, ProviderID: "codex-app-server",
		ProviderType: ProviderTypeCodexAppServer, ConfigFingerprint: "live", ModelID: model,
		Options: options, OptionsHash: hash,
		ContractVersion: pipeline.TranslationContractVersion, PromptVersion: pipeline.TranslationPromptVersion,
	}
	provider := &codexStageProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Label: "Codex app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		binary:     "codex", timeout: 600 * time.Second,
	}
	for _, input := range []semantics.DictionaryInput{
		{
			Version: semantics.DictionaryContractVersion, LookupForm: "bank",
			LookupKind: semantics.DictionaryLookupWord, SourceLanguage: "nl", TargetLanguage: "en",
		},
		{
			Version: semantics.DictionaryContractVersion, LookupForm: "ervan uitgaan",
			LookupKind: semantics.DictionaryLookupExpression, SourceLanguage: "nl", TargetLanguage: "en",
		},
	} {
		result, err := GenerateDictionary(context.Background(), provider, binding, input)
		if err != nil {
			t.Fatalf("live dictionary %q: %v", input.LookupForm, err)
		}
		if len(result.Document.Senses) < 1 {
			t.Fatalf("live dictionary %q returned no senses", input.LookupForm)
		}
		t.Logf("live dictionary %q: %d senses, first translation %q", input.LookupForm, len(result.Document.Senses), result.Document.Senses[0].TranslationEN)
	}
}

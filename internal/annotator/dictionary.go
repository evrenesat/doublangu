// Dictionary generation adapter: builds the reader-dictionary-prompt.v1
// instruction, the closed JSON schema handed to the provider's structured
// output mechanism, and runs one bounded dictionary turn through the shared
// stage executor. The transport binding stays the active translation binding;
// the dictionary operation versions identify this use, not a new stage.
package annotator

import (
	"context"
	"encoding/json"
	"fmt"

	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
	"doublangu/internal/semantics"
)

// DictionaryGenerateResult is the validated dictionary document plus the
// bounded attempt record for provenance and diagnostics.
type DictionaryGenerateResult struct {
	Document semantics.DictionaryDocument
	Attempt  StageAttemptResult
}

// DefaultExploreStagePrompts returns the builtin explore generation and
// correction instruction pair: the exact historical builtin bytes, used by
// legacy callers and conformance fixtures.
func DefaultExploreStagePrompts() StagePrompts {
	return StagePrompts{
		Generation: prompts.DefaultInstruction(prompts.TypeExplore),
		Correction: prompts.DefaultInstruction(prompts.TypeCorrection),
	}
}

// DictionaryPrompt builds the exact instruction text for one dictionary
// request with the builtin default instruction (the legacy-contract entry
// point). INPUT_DATA is quoted data, never instructions; known translations
// are hints, not an exhaustive list.
func DictionaryPrompt(input semantics.DictionaryInput) (string, error) {
	return BuildExploreStagePrompt(prompts.DefaultInstruction(prompts.TypeExplore), input)
}

// BuildExploreStagePrompt renders the explore prompt from one exact
// instruction plus the deterministic code-owned INPUT_DATA envelope. The
// envelope is quoted data and is never owner-editable.
func BuildExploreStagePrompt(instruction string, input semantics.DictionaryInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", fmt.Errorf("annotator: dictionary input: %w", err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("annotator: encode dictionary input: %w", err)
	}
	return instruction + `INPUT_DATA_BEGIN
` + string(encoded) + `
INPUT_DATA_END`, nil
}

// DictionaryOutputSchema builds the closed JSON schema: exactly the listed
// keys, additionalProperties false everywhere, bounded array sizes, the
// part-of-speech enum, and every required key present even when empty.
func DictionaryOutputSchema() json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"version", "lookup_form", "lookup_kind", "source_language", "target_language", "senses"},
		"properties": map[string]any{
			"version":         map[string]any{"type": "string", "enum": []string{semantics.DictionaryContractVersion}},
			"lookup_form":     map[string]any{"type": "string", "minLength": 1, "maxLength": semantics.MaxDictionaryLookupScalars},
			"lookup_kind":     map[string]any{"type": "string", "enum": []string{semantics.DictionaryLookupWord, semantics.DictionaryLookupExpression}},
			"source_language": map[string]any{"type": "string", "minLength": 1, "maxLength": 20},
			"target_language": map[string]any{"type": "string", "minLength": 1, "maxLength": 20},
			"senses": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": semantics.MaxDictionarySenses,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"part_of_speech", "translation_en", "meaning_en", "usage_en", "pattern_nl", "parts", "examples"},
					"properties": map[string]any{
						"part_of_speech": map[string]any{"type": "string", "enum": []string{
							"noun", "verb", "adjective", "adverb", "pronoun", "determiner",
							"preposition", "conjunction", "interjection", "numeral",
							"proper_noun", "expression", "other",
						}},
						"translation_en": map[string]any{"type": "string", "minLength": 1, "maxLength": 120},
						"meaning_en":     map[string]any{"type": "string", "minLength": 1, "maxLength": 600},
						"usage_en":       map[string]any{"type": "string", "maxLength": 400},
						"pattern_nl":     map[string]any{"type": "string", "maxLength": 160},
						"parts": map[string]any{
							"type":     "array",
							"minItems": 0,
							"maxItems": semantics.MaxDictionaryParts,
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": false,
								"required":             []string{"source_nl", "explanation_en"},
								"properties": map[string]any{
									"source_nl":      map[string]any{"type": "string", "minLength": 1, "maxLength": 120},
									"explanation_en": map[string]any{"type": "string", "minLength": 1, "maxLength": 300},
								},
							},
						},
						"examples": map[string]any{
							"type":     "array",
							"minItems": 1,
							"maxItems": semantics.MaxDictionaryExamples,
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": false,
								"required":             []string{"text_nl", "translation_en"},
								"properties": map[string]any{
									"text_nl":        map[string]any{"type": "string", "minLength": 1, "maxLength": 240},
									"translation_en": map[string]any{"type": "string", "minLength": 1, "maxLength": 240},
								},
							},
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// dictionaryStageAdapter adapts the dictionary operation to the shared
// bounded stage executor. The StageID is the translation binding's transport
// identity; the operation identity lives in the dictionary job/document.
// Captured rendering inserts the fixed code-owned data boundary between the
// editable instruction and the INPUT_DATA envelope.
type dictionaryStageAdapter struct {
	input        semantics.DictionaryInput
	stagePrompts StagePrompts
}

func (a *dictionaryStageAdapter) StageID() pipeline.StageID { return pipeline.StageTranslation }

func (a *dictionaryStageAdapter) Prompt() string {
	instruction := a.stagePrompts.Generation
	if a.stagePrompts.Captured {
		instruction = a.stagePrompts.generationPrefix()
	} else {
		instruction = prompts.DefaultInstruction(prompts.TypeExplore)
	}
	prompt, err := BuildExploreStagePrompt(instruction, a.input)
	if err != nil {
		return ""
	}
	return prompt
}

// CorrectivePrompt renders corrective turns from the stage's correction
// prefix (captured instruction plus fixed data boundary, or the builtin
// default) plus code-serialized feedback and the rejected response.
func (a *dictionaryStageAdapter) CorrectivePrompt(validationError, previousResponse string) string {
	return BuildStageCorrectionPromptWithInstruction(a.stagePrompts.correctionPrefix(), validationError, previousResponse)
}

func (a *dictionaryStageAdapter) OutputSchema() json.RawMessage { return DictionaryOutputSchema() }

func (a *dictionaryStageAdapter) Validate(raw string) error {
	document, err := semantics.DecodeDictionaryArtifact([]byte(raw))
	if err != nil {
		return err
	}
	return semantics.ValidateDictionary(a.input, document)
}

// GenerateDictionary runs the bounded dictionary generation: one initial turn
// plus at most two corrective turns through the shared executor, then a final
// strict decode+validate of the accepted artifact.
func GenerateDictionary(ctx context.Context, provider Provider, binding ResolvedBinding, input semantics.DictionaryInput, stagePrompts StagePrompts, opts ...StageOption) (*DictionaryGenerateResult, error) {
	if err := input.Validate(); err != nil {
		return nil, &StageError{Stage: pipeline.StageTranslation, Phase: "provider", Code: CodeInvalidInput, Err: err}
	}
	adapter := &dictionaryStageAdapter{input: input, stagePrompts: stagePrompts}
	raw, attempt, err := executeStage(ctx, provider, binding, adapter, opts...)
	// Result-plus-error: the accumulated attempt (every retained turn) is
	// returned even when the final document is invalid, so callers can
	// preserve available diagnostics instead of losing them with a nil.
	result := &DictionaryGenerateResult{Attempt: attempt}
	if err != nil {
		return result, err
	}
	document, decodeErr := semantics.DecodeDictionaryArtifact([]byte(raw))
	if decodeErr != nil {
		return result, &StageError{Stage: pipeline.StageTranslation, Phase: "stage_validation", Code: CodeInvalidOutput, Err: decodeErr}
	}
	if validateErr := semantics.ValidateDictionary(input, document); validateErr != nil {
		return result, &StageError{Stage: pipeline.StageTranslation, Phase: "stage_validation", Code: CodeInvalidOutput, Err: validateErr}
	}
	result.Document = document
	return result, nil
}

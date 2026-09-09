// Sentence translation adapter: builds the sentence.translation.v1
// instruction, the closed JSON schema handed to the provider's structured
// output mechanism, and runs one bounded translation turn through the shared
// stage executor. The transport binding stays the active translation binding;
// the sentence operation identity lives in the translation job/document, not
// in a new analysis stage.
package annotator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
)

const (
	// SentenceTranslationContractVersion identifies the sentence translation
	// output contract. It is copied from server input into every accepted
	// translation and is part of validation, not a promotion mechanism.
	SentenceTranslationContractVersion = "sentence.translation.v1"

	// MaxSentenceTranslationScalars bounds one accepted translation: nonblank
	// plain text up to 8,000 Unicode scalars.
	MaxSentenceTranslationScalars = 8000

	// maxSentenceArtifactBytes rejects oversized completed artifacts before
	// JSON parsing.
	maxSentenceArtifactBytes = 48 << 10
)

// SentenceTranslationInput is the canonical provider input for one sentence
// translation. Identity fields are server-derived: the exact sentence anchor
// plus the article's target language, which the article owns.
type SentenceTranslationInput struct {
	Version        string `json:"version"`
	SentenceID     string `json:"sentence_id"`
	SourceHash     string `json:"source_hash"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	SourceText     string `json:"source_text"`
	ParagraphText  string `json:"paragraph_context"`
}

// Validate checks the bounded server-side input rules: contract identity,
// sentence identity, language tags, and source hygiene.
func (in SentenceTranslationInput) Validate() error {
	if in.Version != SentenceTranslationContractVersion {
		return fmt.Errorf("sentence translation input version must be %s", SentenceTranslationContractVersion)
	}
	if strings.TrimSpace(in.SentenceID) == "" {
		return errors.New("sentence translation sentence_id is required")
	}
	if strings.TrimSpace(in.SourceHash) == "" {
		return errors.New("sentence translation source_hash is required")
	}
	if strings.TrimSpace(in.SourceText) == "" {
		return errors.New("sentence translation source_text is required")
	}
	if !utf8.ValidString(in.SourceText) {
		return errors.New("sentence translation source_text must be valid UTF-8")
	}
	if !utf8.ValidString(in.ParagraphText) {
		return errors.New("sentence translation paragraph_context must be valid UTF-8")
	}
	source, err := library.ParseBCP47(in.SourceLanguage)
	if err != nil {
		return fmt.Errorf("sentence translation source_language: %w", err)
	}
	target, err := library.ParseBCP47(in.TargetLanguage)
	if err != nil {
		return fmt.Errorf("sentence translation target_language: %w", err)
	}
	if source == target {
		return errors.New("sentence translation source_language and target_language must differ")
	}
	return nil
}

// SentenceTranslationDocument is the validated, typed translation persisted
// in sentence_translation.translation_text.
type SentenceTranslationDocument struct {
	Version       string `json:"version"`
	SentenceID    string `json:"sentence_id"`
	SourceHash    string `json:"source_hash"`
	TranslationEN string `json:"translation_en"`
}

// sentenceTranslationKeys is the exact closed key set of the output contract.
var sentenceTranslationKeys = []string{"version", "sentence_id", "source_hash", "translation_en"}

// DecodeSentenceTranslationArtifact strictly decodes the raw provider
// completion into the closed sentence.translation.v1 shape. Unknown, missing,
// and duplicate keys all fail; trailing JSON fails; the raw artifact is
// size-bounded before parsing.
func DecodeSentenceTranslationArtifact(raw []byte) (SentenceTranslationDocument, error) {
	var document SentenceTranslationDocument
	if len(raw) > maxSentenceArtifactBytes {
		return document, fmt.Errorf("sentence translation artifact exceeds %d bytes", maxSentenceArtifactBytes)
	}
	if !utf8.Valid(raw) {
		return document, errors.New("sentence translation artifact must be valid UTF-8")
	}
	fields, err := decodeStrictObject(raw)
	if err != nil {
		return document, fmt.Errorf("sentence translation artifact: %w", err)
	}
	if len(fields) != len(sentenceTranslationKeys) {
		return document, fmt.Errorf("sentence translation artifact must carry exactly keys %v", sentenceTranslationKeys)
	}
	for _, key := range sentenceTranslationKeys {
		if _, ok := fields[key]; !ok {
			return document, fmt.Errorf("sentence translation artifact is missing key %q", key)
		}
	}
	stringField := func(name string) (string, error) {
		var value string
		if err := json.Unmarshal(fields[name], &value); err != nil {
			return "", fmt.Errorf("sentence translation artifact key %q: %w", name, err)
		}
		return value, nil
	}
	if document.Version, err = stringField("version"); err != nil {
		return document, err
	}
	if document.SentenceID, err = stringField("sentence_id"); err != nil {
		return document, err
	}
	if document.SourceHash, err = stringField("source_hash"); err != nil {
		return document, err
	}
	if document.TranslationEN, err = stringField("translation_en"); err != nil {
		return document, err
	}
	return document, nil
}

// decodeStrictObject decodes one top-level JSON object with duplicate-key
// detection and trailing-data rejection.
func decodeStrictObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("expected a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("expected a string object key")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("expected the closing object brace")
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing data after JSON object")
		}
		return nil, fmt.Errorf("trailing data: %w", err)
	}
	return fields, nil
}

// ValidateSentenceTranslation enforces the strict output contract against the
// captured input: exact const identity fields and one nonblank plain-text
// translation within bounds. HTML markup is rejected.
func ValidateSentenceTranslation(input SentenceTranslationInput, document SentenceTranslationDocument) error {
	if document.Version != SentenceTranslationContractVersion {
		return fmt.Errorf("sentence translation version must be %s", SentenceTranslationContractVersion)
	}
	if document.SentenceID != input.SentenceID {
		return errors.New("sentence translation sentence_id does not match the requested sentence")
	}
	if document.SourceHash != input.SourceHash {
		return errors.New("sentence translation source_hash does not match the requested anchor")
	}
	translation := strings.TrimSpace(document.TranslationEN)
	if translation == "" {
		return errors.New("sentence translation translation_en must not be blank")
	}
	if !utf8.ValidString(translation) {
		return errors.New("sentence translation translation_en must be valid UTF-8")
	}
	if strings.ContainsAny(translation, "<>") {
		return errors.New("sentence translation translation_en must not contain markup delimiters")
	}
	for _, r := range translation {
		if unicode.IsControl(r) {
			return errors.New("sentence translation translation_en must not contain control characters")
		}
	}
	if utf8.RuneCountInString(translation) > MaxSentenceTranslationScalars {
		return fmt.Errorf("sentence translation translation_en exceeds %d characters", MaxSentenceTranslationScalars)
	}
	return nil
}

// DefaultSentenceTranslationStagePrompts returns the builtin sentence
// generation and correction instruction pair: the exact historical builtin
// bytes, used by legacy callers and conformance fixtures.
func DefaultSentenceTranslationStagePrompts() StagePrompts {
	return StagePrompts{
		Generation: prompts.DefaultInstruction(prompts.TypeSentenceTranslation),
		Correction: prompts.DefaultInstruction(prompts.TypeCorrection),
	}
}

// SentenceTranslationPrompt builds the exact instruction text for one
// sentence request with the builtin default instruction (the legacy-contract
// entry point). SOURCE and PARAGRAPH_CONTEXT sections are quoted data, never
// instructions; the paragraph is context only and must never be translated.
func SentenceTranslationPrompt(input SentenceTranslationInput) (string, error) {
	return BuildSentenceTranslationStagePrompt(prompts.DefaultInstruction(prompts.TypeSentenceTranslation), input)
}

// BuildSentenceTranslationStagePrompt renders the sentence prompt from one
// exact instruction plus the deterministic code-owned data envelope. The
// envelope is quoted data and is never owner-editable.
func BuildSentenceTranslationStagePrompt(instruction string, input SentenceTranslationInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", fmt.Errorf("annotator: sentence translation input: %w", err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("annotator: encode sentence translation input: %w", err)
	}
	return instruction + `SOURCE_BEGIN
` + string(encoded) + `
SOURCE_END
PARAGRAPH_CONTEXT_BEGIN
` + input.ParagraphText + `
PARAGRAPH_CONTEXT_END`, nil
}

// SentenceTranslationOutputSchema builds the closed JSON schema: exactly the
// contract keys, additionalProperties false, const identity fields, and the
// bounded plain-text translation.
func SentenceTranslationOutputSchema() json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"version", "sentence_id", "source_hash", "translation_en"},
		"properties": map[string]any{
			"version":     map[string]any{"type": "string", "enum": []string{SentenceTranslationContractVersion}},
			"sentence_id": map[string]any{"type": "string", "minLength": 1},
			"source_hash": map[string]any{"type": "string", "minLength": 1},
			"translation_en": map[string]any{
				"type": "string", "minLength": 1, "maxLength": MaxSentenceTranslationScalars,
			},
		},
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// sentenceTranslationStageAdapter adapts the sentence operation to the shared
// bounded stage executor. The StageID is the translation binding's transport
// identity; the operation identity lives in the sentence job/document.
// Captured rendering inserts the fixed code-owned data boundary between the
// editable instruction and the data envelope.
type sentenceTranslationStageAdapter struct {
	input        SentenceTranslationInput
	stagePrompts StagePrompts
}

func (a *sentenceTranslationStageAdapter) StageID() pipeline.StageID {
	return pipeline.StageTranslation
}

func (a *sentenceTranslationStageAdapter) Prompt() string {
	instruction := a.stagePrompts.Generation
	if a.stagePrompts.Captured {
		instruction = a.stagePrompts.generationPrefix()
	} else {
		instruction = prompts.DefaultInstruction(prompts.TypeSentenceTranslation)
	}
	prompt, err := BuildSentenceTranslationStagePrompt(instruction, a.input)
	if err != nil {
		return ""
	}
	return prompt
}

// CorrectivePrompt renders corrective turns from the stage's correction
// prefix (captured instruction plus fixed data boundary, or the builtin
// default) plus code-serialized feedback and the rejected response.
func (a *sentenceTranslationStageAdapter) CorrectivePrompt(validationError, previousResponse string) string {
	return BuildStageCorrectionPromptWithInstruction(a.stagePrompts.correctionPrefix(), validationError, previousResponse)
}

func (a *sentenceTranslationStageAdapter) OutputSchema() json.RawMessage {
	return SentenceTranslationOutputSchema()
}

// SentenceTranslationGenerateResult is the validated translation plus the
// bounded attempt record for provenance and diagnostics.
type SentenceTranslationGenerateResult struct {
	Document SentenceTranslationDocument
	Attempt  StageAttemptResult
}

func (a *sentenceTranslationStageAdapter) Validate(raw string) error {
	document, err := DecodeSentenceTranslationArtifact([]byte(raw))
	if err != nil {
		return err
	}
	return ValidateSentenceTranslation(a.input, document)
}

// GenerateSentenceTranslation runs the bounded sentence generation: one
// initial turn plus at most two corrective turns through the shared executor,
// then a final strict decode+validate of the accepted artifact.
func GenerateSentenceTranslation(ctx context.Context, provider Provider, binding ResolvedBinding, input SentenceTranslationInput, stagePrompts StagePrompts, opts ...StageOption) (*SentenceTranslationGenerateResult, error) {
	if err := input.Validate(); err != nil {
		return nil, &StageError{Stage: pipeline.StageTranslation, Phase: "provider", Code: CodeInvalidInput, Err: err}
	}
	adapter := &sentenceTranslationStageAdapter{input: input, stagePrompts: stagePrompts}
	raw, attempt, err := executeStage(ctx, provider, binding, adapter, opts...)
	// Result-plus-error: the accumulated attempt (every retained turn) is
	// returned even when the final translation is invalid, so callers can
	// preserve available diagnostics instead of losing them with a nil.
	result := &SentenceTranslationGenerateResult{Attempt: attempt}
	if err != nil {
		return result, err
	}
	document, decodeErr := DecodeSentenceTranslationArtifact([]byte(raw))
	if decodeErr != nil {
		return result, &StageError{Stage: pipeline.StageTranslation, Phase: "stage_validation", Code: CodeInvalidOutput, Err: decodeErr}
	}
	if validateErr := ValidateSentenceTranslation(input, document); validateErr != nil {
		return result, &StageError{Stage: pipeline.StageTranslation, Phase: "stage_validation", Code: CodeInvalidOutput, Err: validateErr}
	}
	result.Document = document
	return result, nil
}

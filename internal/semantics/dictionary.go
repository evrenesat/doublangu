// Dictionary explore contract: the pure response types, strict decoder, and
// deterministic local validation shared by every provider transport. This file
// owns the reader.dictionary.v1 document identity and must not import any
// provider package; internal/annotator consumes it.
package semantics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"doublangu/internal/library"
)

const (
	// DictionaryContractVersion identifies the dictionary document schema.
	// It is copied from server input into every accepted document and is
	// part of the durable dictionary key's validation, not its identity.
	DictionaryContractVersion = "reader.dictionary.v1"
	// DictionaryPromptVersion versions the dictionary instruction text.
	DictionaryPromptVersion = "reader-dictionary-prompt.v1"

	// DictionaryLookupWord marks a single lexical token subject.
	DictionaryLookupWord = "word"
	// DictionaryLookupExpression marks a phrase/idiom/expression/proverb
	// subject. Expression subtypes share one kind so a model's subtype
	// label can never split the shared cache.
	DictionaryLookupExpression = "expression"

	// MaxDictionaryLookupScalars bounds the stored/normalized lookup form.
	MaxDictionaryLookupScalars = 200
	// MaxDictionaryTranslationHints bounds the hint count.
	MaxDictionaryTranslationHints = 6
	// MaxDictionaryHintScalars bounds one known-translation hint.
	MaxDictionaryHintScalars = 120
	// MaxDictionarySenses bounds the senses array.
	MaxDictionarySenses = 6
	// MaxDictionaryParts bounds one sense's parts array.
	MaxDictionaryParts = 6
	// MaxDictionaryExamples bounds one sense's examples array.
	MaxDictionaryExamples = 2

	// maxDictionaryArtifactBytes rejects oversized completed artifacts
	// before JSON parsing.
	maxDictionaryArtifactBytes = 64 << 10
)

// DictionaryLookupKindValid reports whether kind is a supported lookup kind.
func DictionaryLookupKindValid(kind string) bool {
	return kind == DictionaryLookupWord || kind == DictionaryLookupExpression
}

// DictionaryPartOfSpeech is the closed part-of-speech enum.
var dictionaryPartOfSpeech = map[string]struct{}{
	"noun": {}, "verb": {}, "adjective": {}, "adverb": {}, "pronoun": {},
	"determiner": {}, "preposition": {}, "conjunction": {}, "interjection": {},
	"numeral": {}, "proper_noun": {}, "expression": {}, "other": {},
}

// DictionaryInput is the canonical provider input for one dictionary
// generation. Identity fields are server-derived; KnownTranslations are
// existing primary translations used as hints only.
type DictionaryInput struct {
	Version           string   `json:"version"`
	LookupForm        string   `json:"lookup_form"`
	LookupKind        string   `json:"lookup_kind"`
	SourceLanguage    string   `json:"source_language"`
	TargetLanguage    string   `json:"target_language"`
	KnownTranslations []string `json:"known_translations"`
}

// Validate checks the bounded server-side input rules: identity fields,
// lookup kind, lookup-form length, and hint hygiene.
func (in DictionaryInput) Validate() error {
	if in.Version != DictionaryContractVersion {
		return fmt.Errorf("dictionary input version must be %s", DictionaryContractVersion)
	}
	if !DictionaryLookupKindValid(in.LookupKind) {
		return fmt.Errorf("dictionary input lookup kind %q is not supported", in.LookupKind)
	}
	if err := safeProviderText("dictionary lookup_form", in.LookupForm, MaxDictionaryLookupScalars); err != nil {
		return err
	}
	if _, err := NormalizeForm(in.LookupForm); err != nil {
		return fmt.Errorf("dictionary lookup_form: %w", err)
	}
	source, err := library.ParseBCP47(in.SourceLanguage)
	if err != nil {
		return fmt.Errorf("dictionary source_language: %w", err)
	}
	target, err := library.ParseBCP47(in.TargetLanguage)
	if err != nil {
		return fmt.Errorf("dictionary target_language: %w", err)
	}
	if source == target {
		return errors.New("dictionary source_language and target_language must differ")
	}
	for index, hint := range in.KnownTranslations {
		if err := safeProviderText(fmt.Sprintf("known_translations[%d]", index), hint, MaxDictionaryHintScalars); err != nil {
			return err
		}
	}
	if len(in.KnownTranslations) > MaxDictionaryTranslationHints {
		return fmt.Errorf("dictionary input accepts at most %d translation hints", MaxDictionaryTranslationHints)
	}
	return nil
}

// SanitizeDictionaryHints trims, discards blank/oversized/unsafe hints,
// deduplicates by normalized value, sorts by normalized value with the
// original text as tie-breaker, and takes at most six hints.
func SanitizeDictionaryHints(hints []string) []string {
	type normalizedHint struct{ normalized, original string }
	kept := make([]normalizedHint, 0, len(hints))
	seen := make(map[string]struct{}, len(hints))
	for _, hint := range hints {
		trimmed := strings.TrimSpace(hint)
		if trimmed == "" || !utf8.ValidString(trimmed) {
			continue
		}
		if strings.ContainsAny(trimmed, "<>") || strings.ContainsAny(trimmed, "\x00\x7f") {
			continue
		}
		unsafe := false
		for _, r := range trimmed {
			if r < 0x20 {
				unsafe = true
				break
			}
		}
		if unsafe || utf8.RuneCountInString(trimmed) > MaxDictionaryHintScalars {
			continue
		}
		normalized, err := NormalizeForm(trimmed)
		if err != nil {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		kept = append(kept, normalizedHint{normalized: normalized, original: trimmed})
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].normalized != kept[j].normalized {
			return kept[i].normalized < kept[j].normalized
		}
		return kept[i].original < kept[j].original
	})
	if len(kept) > MaxDictionaryTranslationHints {
		kept = kept[:MaxDictionaryTranslationHints]
	}
	out := make([]string, 0, len(kept))
	for _, hint := range kept {
		out = append(out, hint.original)
	}
	return out
}

// DictionaryDocument is the validated, typed dictionary bundle persisted in
// dictionary_entry.document_json.
type DictionaryDocument struct {
	Version        string            `json:"version"`
	LookupForm     string            `json:"lookup_form"`
	LookupKind     string            `json:"lookup_kind"`
	SourceLanguage string            `json:"source_language"`
	TargetLanguage string            `json:"target_language"`
	Senses         []DictionarySense `json:"senses"`
}

// DictionarySense is one common meaning of the lookup form.
type DictionarySense struct {
	PartOfSpeech  string              `json:"part_of_speech"`
	TranslationEN string              `json:"translation_en"`
	MeaningEN     string              `json:"meaning_en"`
	UsageEN       string              `json:"usage_en"`
	PatternNL     string              `json:"pattern_nl"`
	Parts         []DictionaryPart    `json:"parts"`
	Examples      []DictionaryExample `json:"examples"`
}

// DictionaryPart is one morphology/particle breakdown entry.
type DictionaryPart struct {
	SourceNL      string `json:"source_nl"`
	ExplanationEN string `json:"explanation_en"`
}

// DictionaryExample is one Dutch example with its English translation.
type DictionaryExample struct {
	TextNL        string `json:"text_nl"`
	TranslationEN string `json:"translation_en"`
}

// Hash returns the canonical document hash: the deterministic struct
// marshaling of the validated document, SHA-256 digested.
func (d DictionaryDocument) Hash() (string, error) {
	encoded, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// strictObject decodes one JSON object requiring every key in `keys`, no
// unknown keys, and no duplicate keys. values receives each raw value in
// declaration order of `keys`.
type strictObject map[string]json.RawMessage

var errDuplicateKey = errors.New("duplicate object key")

// decodeDictionaryObject walks the JSON token stream so duplicate keys fail
// instead of silently overwriting, as map decoding would.
func decodeDictionaryObject(data []byte) (strictObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("expected a JSON object")
	}
	object := strictObject{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key must be a string")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, fmt.Errorf("%w: %q", errDuplicateKey, key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON after the document")
		}
		return nil, fmt.Errorf("malformed trailing JSON: %w", err)
	}
	return object, nil
}

// requireKeys verifies exactly the expected keys are present.
func (o strictObject) requireKeys(keys []string) error {
	if len(o) != len(keys) {
		return fmt.Errorf("object must have exactly the keys %v", keys)
	}
	for _, key := range keys {
		if _, ok := o[key]; !ok {
			return fmt.Errorf("object is missing required key %q", key)
		}
	}
	return nil
}

func (o strictObject) jsonString(key string) (string, error) {
	raw, ok := o[key]
	if !ok {
		return "", fmt.Errorf("missing key %q", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("key %q must be a string", key)
	}
	return value, nil
}

func (o strictObject) stringArray(key string, min, max int) ([]string, error) {
	raw, ok := o[key]
	if !ok {
		return nil, fmt.Errorf("missing key %q", key)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("key %q must be an array of strings", key)
	}
	if len(values) < min || len(values) > max {
		return nil, fmt.Errorf("key %q must contain %d-%d items", key, min, max)
	}
	return values, nil
}

// validateDictionaryText applies UTF-8, markup, control-character, blankness,
// and length rules to one field.
func validateDictionaryText(name, value string, max int, nonblank bool) error {
	if err := safeProviderText(name, value, max); err != nil {
		return err
	}
	// safeProviderText allows \n\r\t; dictionary prose must not contain them.
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			return fmt.Errorf("%s must not contain line breaks or tabs", name)
		}
	}
	if nonblank && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be blank", name)
	}
	// Reject Markdown emphasis/link delimiters the same way as HTML.
	if strings.Contains(value, "`") || strings.Contains(value, "](") || strings.Contains(value, "![") {
		return fmt.Errorf("%s must not contain markup delimiters", name)
	}
	return nil
}

// DecodeDictionaryArtifact strictly decodes the raw provider completion into
// the closed reader.dictionary.v1 shape. Unknown, missing, and duplicate keys
// all fail; trailing JSON fails; the raw artifact is size-bounded before
// parsing.
func DecodeDictionaryArtifact(raw []byte) (DictionaryDocument, error) {
	var document DictionaryDocument
	if len(raw) > maxDictionaryArtifactBytes {
		return document, fmt.Errorf("dictionary artifact exceeds %d bytes", maxDictionaryArtifactBytes)
	}
	if !utf8.Valid(raw) {
		return document, errors.New("dictionary artifact must be valid UTF-8")
	}
	top, err := decodeDictionaryObject(raw)
	if err != nil {
		return document, fmt.Errorf("dictionary artifact: %w", err)
	}
	topKeys := []string{"version", "lookup_form", "lookup_kind", "source_language", "target_language", "senses"}
	if err := top.requireKeys(topKeys); err != nil {
		return document, fmt.Errorf("dictionary artifact: %w", err)
	}
	if document.Version, err = top.jsonString("version"); err != nil {
		return document, err
	}
	if document.LookupForm, err = top.jsonString("lookup_form"); err != nil {
		return document, err
	}
	if document.LookupKind, err = top.jsonString("lookup_kind"); err != nil {
		return document, err
	}
	if document.SourceLanguage, err = top.jsonString("source_language"); err != nil {
		return document, err
	}
	if document.TargetLanguage, err = top.jsonString("target_language"); err != nil {
		return document, err
	}
	sensesRaw, ok := top["senses"]
	if !ok {
		return document, errors.New("dictionary artifact is missing key \"senses\"")
	}
	var senses []strictObject
	senses, err = decodeDictionaryArray(sensesRaw)
	if err != nil {
		return document, fmt.Errorf("dictionary senses: %w", err)
	}
	if len(senses) < 1 || len(senses) > MaxDictionarySenses {
		return document, fmt.Errorf("dictionary senses must contain 1-%d entries", MaxDictionarySenses)
	}
	document.Senses = make([]DictionarySense, 0, len(senses))
	for index, senseObject := range senses {
		sense, senseErr := decodeDictionarySense(senseObject)
		if senseErr != nil {
			return document, fmt.Errorf("senses[%d]: %w", index, senseErr)
		}
		document.Senses = append(document.Senses, sense)
	}
	return document, nil
}

// decodeDictionaryArray decodes a JSON array whose items are all objects with
// duplicate-key detection.
func decodeDictionaryArray(raw json.RawMessage) ([]strictObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return nil, errors.New("expected a JSON array")
	}
	items := []strictObject{}
	for decoder.More() {
		var itemRaw json.RawMessage
		if err := decoder.Decode(&itemRaw); err != nil {
			return nil, err
		}
		item, err := decodeDictionaryObject(itemRaw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeDictionarySense(object strictObject) (DictionarySense, error) {
	var sense DictionarySense
	senseKeys := []string{"part_of_speech", "translation_en", "meaning_en", "usage_en", "pattern_nl", "parts", "examples"}
	if err := object.requireKeys(senseKeys); err != nil {
		return sense, err
	}
	var err error
	if sense.PartOfSpeech, err = object.jsonString("part_of_speech"); err != nil {
		return sense, err
	}
	if sense.TranslationEN, err = object.jsonString("translation_en"); err != nil {
		return sense, err
	}
	if sense.MeaningEN, err = object.jsonString("meaning_en"); err != nil {
		return sense, err
	}
	if sense.UsageEN, err = object.jsonString("usage_en"); err != nil {
		return sense, err
	}
	if sense.PatternNL, err = object.jsonString("pattern_nl"); err != nil {
		return sense, err
	}
	partsRaw, ok := object["parts"]
	if !ok {
		return sense, errors.New("missing key \"parts\"")
	}
	parts, err := decodeDictionaryArray(partsRaw)
	if err != nil {
		return sense, fmt.Errorf("parts: %w", err)
	}
	if len(parts) > MaxDictionaryParts {
		return sense, fmt.Errorf("parts must contain at most %d items", MaxDictionaryParts)
	}
	sense.Parts = make([]DictionaryPart, 0, len(parts))
	for index, partObject := range parts {
		var part DictionaryPart
		if err := partObject.requireKeys([]string{"source_nl", "explanation_en"}); err != nil {
			return sense, fmt.Errorf("parts[%d]: %w", index, err)
		}
		if part.SourceNL, err = partObject.jsonString("source_nl"); err != nil {
			return sense, fmt.Errorf("parts[%d]: %w", index, err)
		}
		if part.ExplanationEN, err = partObject.jsonString("explanation_en"); err != nil {
			return sense, fmt.Errorf("parts[%d]: %w", index, err)
		}
		sense.Parts = append(sense.Parts, part)
	}
	examplesRaw, ok := object["examples"]
	if !ok {
		return sense, errors.New("missing key \"examples\"")
	}
	examples, err := decodeDictionaryArray(examplesRaw)
	if err != nil {
		return sense, fmt.Errorf("examples: %w", err)
	}
	if len(examples) < 1 || len(examples) > MaxDictionaryExamples {
		return sense, fmt.Errorf("examples must contain 1-%d items", MaxDictionaryExamples)
	}
	sense.Examples = make([]DictionaryExample, 0, len(examples))
	for index, exampleObject := range examples {
		var example DictionaryExample
		if err := exampleObject.requireKeys([]string{"text_nl", "translation_en"}); err != nil {
			return sense, fmt.Errorf("examples[%d]: %w", index, err)
		}
		if example.TextNL, err = exampleObject.jsonString("text_nl"); err != nil {
			return sense, fmt.Errorf("examples[%d]: %w", index, err)
		}
		if example.TranslationEN, err = exampleObject.jsonString("translation_en"); err != nil {
			return sense, fmt.Errorf("examples[%d]: %w", index, err)
		}
		sense.Examples = append(sense.Examples, example)
	}
	return sense, nil
}

// ValidateDictionary checks the decoded document against the request input
// and the closed deterministic rules: identity preservation, enum, text
// bounds, and duplicate senses/examples. Meaning order from the provider is
// preserved.
func ValidateDictionary(input DictionaryInput, document DictionaryDocument) error {
	if err := input.Validate(); err != nil {
		return fmt.Errorf("dictionary input: %w", err)
	}
	if document.Version != DictionaryContractVersion {
		return fmt.Errorf("document version must be %s", DictionaryContractVersion)
	}
	if document.LookupForm != input.LookupForm {
		return errors.New("document lookup_form must exactly match the request")
	}
	if document.LookupKind != input.LookupKind {
		return errors.New("document lookup_kind must exactly match the request")
	}
	if document.SourceLanguage != input.SourceLanguage || document.TargetLanguage != input.TargetLanguage {
		return errors.New("document languages must exactly match the request")
	}
	if _, err := libraryParseAndCompare(document.SourceLanguage, document.TargetLanguage); err != nil {
		return err
	}
	seenSenses := make(map[string]struct{}, len(document.Senses))
	for index, sense := range document.Senses {
		name := fmt.Sprintf("senses[%d]", index)
		if _, ok := dictionaryPartOfSpeech[sense.PartOfSpeech]; !ok {
			return fmt.Errorf("%s.part_of_speech %q is not a supported part of speech", name, sense.PartOfSpeech)
		}
		if err := validateDictionaryText(name+".translation_en", sense.TranslationEN, 120, true); err != nil {
			return err
		}
		if err := validateDictionaryText(name+".meaning_en", sense.MeaningEN, 600, true); err != nil {
			return err
		}
		if err := validateDictionaryText(name+".usage_en", sense.UsageEN, 400, false); err != nil {
			return err
		}
		if err := validateDictionaryText(name+".pattern_nl", sense.PatternNL, 160, false); err != nil {
			return err
		}
		if len(sense.Parts) > MaxDictionaryParts {
			return fmt.Errorf("%s.parts must contain at most %d items", name, MaxDictionaryParts)
		}
		for partIndex, part := range sense.Parts {
			if err := validateDictionaryText(fmt.Sprintf("%s.parts[%d].source_nl", name, partIndex), part.SourceNL, 120, true); err != nil {
				return err
			}
			if err := validateDictionaryText(fmt.Sprintf("%s.parts[%d].explanation_en", name, partIndex), part.ExplanationEN, 300, true); err != nil {
				return err
			}
		}
		if len(sense.Examples) < 1 || len(sense.Examples) > MaxDictionaryExamples {
			return fmt.Errorf("%s.examples must contain 1-%d items", name, MaxDictionaryExamples)
		}
		seenExamples := make(map[string]struct{}, len(sense.Examples))
		for exampleIndex, example := range sense.Examples {
			if err := validateDictionaryText(fmt.Sprintf("%s.examples[%d].text_nl", name, exampleIndex), example.TextNL, 240, true); err != nil {
				return err
			}
			if err := validateDictionaryText(fmt.Sprintf("%s.examples[%d].translation_en", name, exampleIndex), example.TranslationEN, 240, true); err != nil {
				return err
			}
			key := sense.ExampleKey(exampleIndex)
			if _, duplicate := seenExamples[key]; duplicate {
				return fmt.Errorf("%s.examples[%d] duplicates another example", name, exampleIndex)
			}
			seenExamples[key] = struct{}{}
		}
		senseKey, err := sense.deduplicationKey()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, duplicate := seenSenses[senseKey]; duplicate {
			return fmt.Errorf("%s duplicates another sense", name)
		}
		seenSenses[senseKey] = struct{}{}
	}
	return nil
}

// deduplicationKey is the normalized (part_of_speech, translation_en,
// meaning_en) triple. These checks do not claim to detect semantic
// paraphrases; they reject exact restatements.
func (s DictionarySense) deduplicationKey() (string, error) {
	translation, err := NormalizeForm(s.TranslationEN)
	if err != nil {
		return "", err
	}
	meaning, err := NormalizeForm(s.MeaningEN)
	if err != nil {
		return "", err
	}
	return s.PartOfSpeech + "\x1f" + translation + "\x1f" + meaning, nil
}

// ExampleKey is the normalized example pair identity.
func (s DictionarySense) ExampleKey(index int) string {
	if index < 0 || index >= len(s.Examples) {
		return ""
	}
	example := s.Examples[index]
	return example.TextNL + "\x1f" + example.TranslationEN
}

// libraryParseAndCompare validates both language tags through the existing
// BCP-47 helper and rejects an identical source/target pair.
func libraryParseAndCompare(sourceLanguage, targetLanguage string) (bool, error) {
	source, err := library.ParseBCP47(sourceLanguage)
	if err != nil {
		return false, fmt.Errorf("source_language: %w", err)
	}
	target, err := library.ParseBCP47(targetLanguage)
	if err != nil {
		return false, fmt.Errorf("target_language: %w", err)
	}
	if source == target {
		return false, errors.New("source_language and target_language must differ")
	}
	return true, nil
}

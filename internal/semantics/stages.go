package semantics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"doublangu/internal/pipeline"
)

// This file owns the two typed pipeline stage artifacts and their pure local
// validators and deterministic merge. Provider protocol code never lives
// here; the merged output is a normal semantics.Response that still has to
// pass the unchanged v3 chunk validator before publication.

// ---------------------------------------------------------------------------
// Linguistic artifact

// LinguisticTokenResult is the source-side token classification without any
// translation-owned field.
type LinguisticTokenResult struct {
	TokenID                 string `json:"token_id"`
	Classification          string `json:"classification"`
	Kind                    Kind   `json:"kind"`
	SemanticSenseID         string `json:"semantic_sense_id"`
	NewSenseRef             string `json:"new_sense_ref"`
	CanonicalPronunciation  string `json:"canonical_pronunciation_text"`
	ContextPronunciationKey string `json:"context_pronunciation_key"`
	ConfidenceMilli         int    `json:"confidence_milli"`
}

// LinguisticNewSense is a sense proposal without target-language fields.
type LinguisticNewSense struct {
	Ref                        string `json:"ref"`
	Kind                       Kind   `json:"kind"`
	CanonicalForm              string `json:"canonical_form"`
	NormalizedForm             string `json:"normalized_form"`
	Lemma                      string `json:"lemma"`
	PartOfSpeech               string `json:"part_of_speech"`
	SenseDiscriminator         string `json:"sense_discriminator"`
	MeaningNote                string `json:"meaning_note"`
	UsageNote                  string `json:"usage_note"`
	PartsNote                  string `json:"parts_note"`
	CanonicalPronunciationText string `json:"canonical_pronunciation_text"`
}

// LinguisticConstruction is a source-side construction without shadow_text.
type LinguisticConstruction struct {
	Kind                       Kind      `json:"kind"`
	Role                       string    `json:"role"`
	SemanticSenseID            string    `json:"semantic_sense_id"`
	NewSenseRef                string    `json:"new_sense_ref"`
	CanonicalPronunciationText string    `json:"canonical_pronunciation_text"`
	ContextPronunciationKey    string    `json:"context_pronunciation_key"`
	ConfidenceMilli            int       `json:"confidence_milli"`
	TokenIDs                   []string  `json:"token_ids"`
	Spans                      []SpanRef `json:"spans"`
}

// LinguisticArtifact is the exact closed provider payload of the linguistic
// stage. It must not contain shadow_text, primary_translation, alternatives,
// or literal_translation.
type LinguisticArtifact struct {
	Version       string                   `json:"version"`
	Tokens        []LinguisticTokenResult  `json:"tokens"`
	NewSenses     []LinguisticNewSense     `json:"new_senses"`
	Constructions []LinguisticConstruction `json:"constructions"`
}

// ValidatedLinguisticConstruction pairs a validated source construction with
// its server-assigned id and resolved spans. IDs are part of the validated
// artifact and of the translation input; the linguistic provider never
// invents them.
type ValidatedLinguisticConstruction struct {
	ConstructionID string                 `json:"construction_id"`
	Construction   LinguisticConstruction `json:"construction"`
	Spans          []ResolvedSpan         `json:"-"`
}

// ValidatedLinguistic is the validated artifact: deterministic token order,
// and constructions sorted deterministically with server-assigned ids.
type ValidatedLinguistic struct {
	Version       string                            `json:"version"`
	Tokens        []LinguisticTokenResult           `json:"tokens"`
	NewSenses     []LinguisticNewSense              `json:"new_senses"`
	Constructions []ValidatedLinguisticConstruction `json:"constructions"`
}

// ---------------------------------------------------------------------------
// Translation artifact

// TranslationTokenResult adds only shadow_text to one linguistic token.
type TranslationTokenResult struct {
	TokenID    string `json:"token_id"`
	ShadowText string `json:"shadow_text"`
}

// TranslationNewSense adds target-language fields to one linguistic ref.
type TranslationNewSense struct {
	Ref                string   `json:"ref"`
	PrimaryTranslation string   `json:"primary_translation"`
	Alternatives       []string `json:"alternatives"`
	LiteralTranslation string   `json:"literal_translation"`
}

// TranslationConstruction adds only shadow_text to one server-assigned
// construction id.
type TranslationConstruction struct {
	ConstructionID string `json:"construction_id"`
	ShadowText     string `json:"shadow_text"`
}

// TranslationArtifact is the exact closed provider payload of the translation
// stage. It has no field capable of retokenizing, reclassifying, changing a
// sense link, or changing construction spans or members.
type TranslationArtifact struct {
	Version       string                    `json:"version"`
	Tokens        []TranslationTokenResult  `json:"tokens"`
	NewSenses     []TranslationNewSense     `json:"new_senses"`
	Constructions []TranslationConstruction `json:"constructions"`
}

// ---------------------------------------------------------------------------
// Strict decoding

func decodeStrictObject(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("payload contains trailing JSON")
		}
		return fmt.Errorf("payload contains malformed trailing JSON: %w", err)
	}
	return nil
}

// DecodeLinguisticArtifact performs strict JSON object decoding.
func DecodeLinguisticArtifact(data []byte) (LinguisticArtifact, error) {
	var artifact LinguisticArtifact
	if err := decodeStrictObject(data, &artifact); err != nil {
		return LinguisticArtifact{}, err
	}
	return artifact, nil
}

// DecodeTranslationArtifact performs strict JSON object decoding.
func DecodeTranslationArtifact(data []byte) (TranslationArtifact, error) {
	var artifact TranslationArtifact
	if err := decodeStrictObject(data, &artifact); err != nil {
		return TranslationArtifact{}, err
	}
	return artifact, nil
}

// ---------------------------------------------------------------------------
// Linguistic validation

// ValidateLinguistic validates one linguistic artifact for one prepared
// paragraph and returns the deterministic validated artifact with
// server-assigned construction ids. Translation-owned invariants are not
// checked here because the artifact cannot express them.
func ValidateLinguistic(chunk PreparedChunk, artifact LinguisticArtifact) (*ValidatedLinguistic, error) {
	if artifact.Version != pipeline.LinguisticContractVersion {
		return nil, fmt.Errorf("unsupported linguistic artifact version %q", artifact.Version)
	}
	input := chunkAsPrepared(chunk)
	if len(artifact.Tokens) != len(input.Tokens) {
		return nil, fmt.Errorf("linguistic token coverage has %d results for %d supplied tokens", len(artifact.Tokens), len(input.Tokens))
	}
	tokenByID := make(map[string]Token, len(input.Tokens))
	for _, token := range input.Tokens {
		tokenByID[token.ID] = token
	}
	seenTokens := make(map[string]struct{}, len(artifact.Tokens))
	candidateByID := make(map[string]SenseCandidate, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if candidate.ID != "" {
			candidateByID[candidate.ID] = candidate
		}
	}
	prior := make([]NewSense, len(chunk.PriorValidatedSenses))
	copy(prior, chunk.PriorValidatedSenses)
	newByRef := make(map[string]LinguisticNewSense, len(prior)+len(artifact.NewSenses))
	for index, sense := range prior {
		if sense.Ref == "" {
			return nil, fmt.Errorf("prior validated new_senses[%d] has no ref", index)
		}
		if err := safeProviderText(fmt.Sprintf("prior validated new_senses[%d].ref", index), sense.Ref, 120); err != nil {
			return nil, err
		}
		if _, exists := newByRef[sense.Ref]; exists {
			return nil, fmt.Errorf("prior validated sense ref %q is duplicated", sense.Ref)
		}
		newByRef[sense.Ref] = LinguisticNewSense{
			Kind: Kind(sense.Kind), CanonicalForm: sense.CanonicalForm, NormalizedForm: sense.NormalizedForm,
			Lemma: sense.Lemma, PartOfSpeech: sense.PartOfSpeech, SenseDiscriminator: sense.SenseDiscriminator,
			CanonicalPronunciationText: sense.CanonicalPronunciationText,
		}
	}
	for index, sense := range artifact.NewSenses {
		if sense.Ref == "" {
			return nil, fmt.Errorf("new_senses[%d] has no ref", index)
		}
		if err := safeProviderText(fmt.Sprintf("new_senses[%d].ref", index), sense.Ref, 120); err != nil {
			return nil, err
		}
		if _, exists := newByRef[sense.Ref]; exists {
			return nil, fmt.Errorf("new sense ref %q is duplicated", sense.Ref)
		}
		if err := validateLinguisticNewSense(sense, fmt.Sprintf("new_senses[%d]", index)); err != nil {
			return nil, err
		}
		newByRef[sense.Ref] = sense
	}
	validatedTokens := make([]LinguisticTokenResult, 0, len(artifact.Tokens))
	for index, result := range artifact.Tokens {
		token, ok := tokenByID[result.TokenID]
		if !ok {
			return nil, fmt.Errorf("linguistic tokens[%d] references unknown token %q", index, result.TokenID)
		}
		if _, exists := seenTokens[result.TokenID]; exists {
			return nil, fmt.Errorf("linguistic token %q appears more than once", result.TokenID)
		}
		seenTokens[result.TokenID] = struct{}{}
		if err := validateLinguisticTokenResult(result, token, input.SourceLanguage, input.TargetLanguage, candidateByID, newByRef); err != nil {
			return nil, fmt.Errorf("linguistic token %q: %w", result.TokenID, err)
		}
		validatedTokens = append(validatedTokens, result)
	}
	for _, token := range input.Tokens {
		if _, ok := seenTokens[token.ID]; !ok {
			return nil, fmt.Errorf("linguistic token %q is missing from the artifact", token.ID)
		}
	}
	anchors, err := validateSourceSentenceAnchors(input)
	if err != nil {
		return nil, err
	}
	sentenceForToken := func(token Token) int {
		for _, anchor := range anchors {
			if anchor.Span.BlockIndex == token.BlockIndex && token.StartUTF16 >= anchor.Span.StartUTF16 && token.EndUTF16 <= anchor.Span.EndUTF16 {
				return anchor.Index
			}
		}
		return -1
	}
	for _, token := range input.Tokens {
		if sentenceForToken(token) < 0 {
			return nil, fmt.Errorf("linguistic token %q is outside the supplied source sentence anchors", token.ID)
		}
	}

	validatedConstructions := make([]linguisticResolvedConstruction, 0, len(artifact.Constructions))
	for index, construction := range artifact.Constructions {
		if construction.Role != "contiguous_construction" && construction.Role != "discontinuous_construction" {
			return nil, fmt.Errorf("linguistic constructions[%d] has invalid role %q", index, construction.Role)
		}
		if !construction.Kind.Valid() || construction.Kind == KindWord {
			return nil, fmt.Errorf("linguistic constructions[%d] has invalid kind", index)
		}
		if construction.SemanticSenseID != "" && construction.NewSenseRef != "" {
			return nil, fmt.Errorf("linguistic constructions[%d] has two sense references", index)
		}
		if err := validateLinguisticConstructionReference(construction.SemanticSenseID, construction.NewSenseRef, construction.Kind, candidateByID, newByRef); err != nil {
			return nil, fmt.Errorf("linguistic constructions[%d]: %w", index, err)
		}
		if err := safeProviderText("linguistic construction canonical_pronunciation_text", construction.CanonicalPronunciationText, MaxPronunciationScalars); err != nil {
			return nil, fmt.Errorf("linguistic constructions[%d]: %w", index, err)
		}
		if err := safeProviderText("linguistic construction context_pronunciation_key", construction.ContextPronunciationKey, MaxPronunciationScalars); err != nil {
			return nil, fmt.Errorf("linguistic constructions[%d]: %w", index, err)
		}
		if construction.ConfidenceMilli < 0 || construction.ConfidenceMilli > 1000 {
			return nil, fmt.Errorf("linguistic constructions[%d] confidence is outside 0..1000", index)
		}
		wantSpans := 1
		if construction.Role == "discontinuous_construction" {
			wantSpans = 2
		}
		if len(construction.Spans) < wantSpans {
			return nil, fmt.Errorf("linguistic constructions[%d] requires at least %d spans", index, wantSpans)
		}
		if len(construction.TokenIDs) == 0 {
			return nil, fmt.Errorf("linguistic constructions[%d] must reference at least one token", index)
		}
		if construction.Role == "contiguous_construction" && len(construction.Spans) != 1 {
			return nil, fmt.Errorf("linguistic constructions[%d] must have exactly one span", index)
		}
		resolved := make([]ResolvedSpan, 0, len(construction.Spans))
		constructionTokenIDs := make(map[string]struct{}, len(construction.TokenIDs))
		for spanIndex, ref := range construction.Spans {
			if ref.BlockIndex < 0 || ref.BlockIndex >= len(input.Blocks) {
				return nil, fmt.Errorf("linguistic constructions[%d].spans[%d] has invalid block index", index, spanIndex)
			}
			span, err := ResolveSpan(input.Blocks[ref.BlockIndex], ref.SourceText, ref.Occurrence)
			if err != nil {
				return nil, fmt.Errorf("linguistic constructions[%d].spans[%d]: %w", index, spanIndex, err)
			}
			if spanIndex > 0 {
				previous := resolved[spanIndex-1]
				if span.BlockIndex != previous.BlockIndex || span.StartUTF16 < previous.EndUTF16 {
					return nil, fmt.Errorf("linguistic constructions[%d] spans are not ordered and non-overlapping", index)
				}
			}
			resolved = append(resolved, span)
		}
		seenConstructionTokens := make(map[string]struct{}, len(construction.TokenIDs))
		memberTokens := make([]Token, 0, len(construction.TokenIDs))
		sentenceIndex := -1
		lastStart := -1
		lastBlock := -1
		var memberParts []string
		for _, tokenID := range construction.TokenIDs {
			token, ok := tokenByID[tokenID]
			if !ok {
				return nil, fmt.Errorf("linguistic constructions[%d] references unknown token %q", index, tokenID)
			}
			if _, exists := seenConstructionTokens[tokenID]; exists {
				return nil, fmt.Errorf("linguistic constructions[%d] repeats token %q", index, tokenID)
			}
			seenConstructionTokens[tokenID] = struct{}{}
			constructionTokenIDs[tokenID] = struct{}{}
			if lastBlock >= 0 && (token.BlockIndex != lastBlock || token.StartUTF16 <= lastStart) {
				return nil, fmt.Errorf("linguistic constructions[%d] member tokens are not in source order", index)
			}
			lastBlock, lastStart = token.BlockIndex, token.StartUTF16
			covered := false
			for _, span := range resolved {
				if span.BlockIndex == token.BlockIndex && token.StartUTF16 >= span.StartUTF16 && token.EndUTF16 <= span.EndUTF16 {
					covered = true
					break
				}
			}
			if !covered {
				return nil, fmt.Errorf("linguistic constructions[%d] token %q is outside its spans", index, tokenID)
			}
			owner := sentenceForToken(token)
			if owner < 0 {
				return nil, fmt.Errorf("linguistic constructions[%d] token %q is outside the sentence coverage", index, tokenID)
			}
			if sentenceIndex < 0 {
				sentenceIndex = owner
			} else if owner != sentenceIndex {
				return nil, fmt.Errorf("linguistic constructions[%d] crosses a source sentence boundary", index)
			}
			memberTokens = append(memberTokens, token)
			memberParts = append(memberParts, token.SourceText)
		}
		if runs := memberRunCount(memberTokens); runs != 1 && construction.Role == "contiguous_construction" {
			return nil, fmt.Errorf("linguistic constructions[%d] contiguous members do not form exactly one run", index)
		} else if runs < 2 && construction.Role == "discontinuous_construction" {
			return nil, fmt.Errorf("linguistic constructions[%d] discontinuous members form fewer than two runs", index)
		}
		if construction.Role == "contiguous_construction" {
			container := resolved[0]
			for _, token := range input.Tokens {
				if token.BlockIndex == container.BlockIndex && token.StartUTF16 >= container.StartUTF16 && token.EndUTF16 <= container.EndUTF16 {
					if _, ok := constructionTokenIDs[token.ID]; !ok {
						return nil, fmt.Errorf("linguistic constructions[%d] omits component token %q", index, token.ID)
					}
				}
			}
		}
		validatedConstructions = append(validatedConstructions, linguisticResolvedConstruction{
			construction: construction, spans: resolved, members: memberTokens,
		})
	}
	// Contiguous constructions may not overlap each other.
	for leftIndex, left := range validatedConstructions {
		if left.construction.Role != "contiguous_construction" || len(left.spans) != 1 {
			continue
		}
		for rightIndex := leftIndex + 1; rightIndex < len(validatedConstructions); rightIndex++ {
			right := validatedConstructions[rightIndex]
			if right.construction.Role != "contiguous_construction" || len(right.spans) != 1 {
				continue
			}
			if spanOverlap(left.spans[0], right.spans[0]) {
				return nil, fmt.Errorf("linguistic contiguous constructions %d and %d overlap", leftIndex, rightIndex)
			}
		}
	}
	sort.SliceStable(validatedConstructions, func(i, j int) bool {
		return constructionSortLess(validatedConstructions[i], validatedConstructions[j])
	})
	result := &ValidatedLinguistic{
		Version:   pipeline.LinguisticContractVersion,
		Tokens:    validatedTokens,
		NewSenses: artifact.NewSenses,
	}
	for index, item := range validatedConstructions {
		result.Constructions = append(result.Constructions, ValidatedLinguisticConstruction{
			ConstructionID: fmt.Sprintf("b%d:c%d", chunk.Block.BlockIndex, index),
			Construction:   item.construction,
			Spans:          item.spans,
		})
	}
	return result, nil
}

func chunkAsPrepared(chunk PreparedChunk) PreparedArticle {
	blocks := make([]Block, chunk.Block.BlockIndex+1)
	blocks[chunk.Block.BlockIndex] = chunk.Block
	return PreparedArticle{
		Title: chunk.Title, SourceLanguage: chunk.SourceLanguage, TargetLanguage: chunk.TargetLanguage,
		ContentHash: chunk.ContentHash, Blocks: blocks,
		Tokens:     append([]Token(nil), chunk.Tokens...),
		Candidates: append([]SenseCandidate(nil), chunk.Candidates...),
		Sentences:  append([]ResolvedSentence(nil), chunk.Sentences...),
	}
}

type linguisticResolvedConstruction struct {
	construction LinguisticConstruction
	spans        []ResolvedSpan
	members      []Token
}

func constructionSortLess(left, right linguisticResolvedConstruction) bool {
	leftFirst := left.members[0]
	rightFirst := right.members[0]
	if leftFirst.TokenIndex != rightFirst.TokenIndex {
		return leftFirst.TokenIndex < rightFirst.TokenIndex
	}
	leftLast := left.members[len(left.members)-1]
	rightLast := right.members[len(right.members)-1]
	if leftLast.TokenIndex != rightLast.TokenIndex {
		return leftLast.TokenIndex < rightLast.TokenIndex
	}
	if left.construction.Role != right.construction.Role {
		return left.construction.Role < right.construction.Role
	}
	if left.construction.Kind != right.construction.Kind {
		return left.construction.Kind < right.construction.Kind
	}
	leftJoined := strings.Join(left.construction.TokenIDs, "\x00")
	rightJoined := strings.Join(right.construction.TokenIDs, "\x00")
	if leftJoined != rightJoined {
		return leftJoined < rightJoined
	}
	leftSpans := spanTexts(left.spans)
	rightSpans := spanTexts(right.spans)
	return strings.Join(leftSpans, "\x00") < strings.Join(rightSpans, "\x00")
}

func spanTexts(spans []ResolvedSpan) []string {
	texts := make([]string, len(spans))
	for index, span := range spans {
		texts[index] = span.SourceText
	}
	return texts
}

func validateLinguisticNewSense(sense LinguisticNewSense, name string) error {
	if !sense.Kind.Valid() {
		return fmt.Errorf("%s has invalid kind %q", name, sense.Kind)
	}
	for field, value := range map[string]string{
		"canonical_form": sense.CanonicalForm, "normalized_form": sense.NormalizedForm,
		"sense_discriminator": sense.SenseDiscriminator, "meaning_note": sense.MeaningNote,
		"usage_note": sense.UsageNote, "parts_note": sense.PartsNote,
		"canonical_pronunciation_text": sense.CanonicalPronunciationText,
	} {
		if (field == "canonical_form" || field == "normalized_form" || field == "sense_discriminator") && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.%s must not be empty", name, field)
		}
		max := MaxNoteScalars
		if field == "canonical_pronunciation_text" {
			max = MaxPronunciationScalars
		}
		if err := safeProviderText(name+"."+field, value, max); err != nil {
			return err
		}
	}
	canonicalNormalized, err := NormalizeForm(sense.CanonicalForm)
	if err != nil {
		return fmt.Errorf("%s.canonical_form: %w", name, err)
	}
	suppliedNormalized, err := NormalizeForm(sense.NormalizedForm)
	if err != nil {
		return fmt.Errorf("%s.normalized_form: %w", name, err)
	}
	if suppliedNormalized != canonicalNormalized {
		return fmt.Errorf("%s.normalized_form must equal the deterministic normalization of canonical_form", name)
	}
	return nil
}

// validateLinguisticTokenResult applies the v3 classification/sense rules that
// do not require translation-owned fields.
func validateLinguisticTokenResult(result LinguisticTokenResult, token Token, sourceLanguage, targetLanguage string, candidates map[string]SenseCandidate, newSenses map[string]LinguisticNewSense) error {
	if result.Classification == "" {
		return errors.New("classification is required")
	}
	if err := safeProviderText("classification", result.Classification, MaxNoteScalars); err != nil {
		return err
	}
	if !result.Kind.Valid() {
		return fmt.Errorf("invalid kind %q", result.Kind)
	}
	if result.ConfidenceMilli < 0 || result.ConfidenceMilli > 1000 {
		return errors.New("confidence is outside 0..1000")
	}
	if err := safeProviderText("canonical_pronunciation_text", result.CanonicalPronunciation, MaxPronunciationScalars); err != nil {
		return err
	}
	if err := safeProviderText("context_pronunciation_key", result.ContextPronunciationKey, MaxPronunciationScalars); err != nil {
		return err
	}
	if result.SemanticSenseID != "" && result.NewSenseRef != "" {
		return errors.New("semantic_sense_id and new_sense_ref are mutually exclusive")
	}
	unchanged := result.Classification == "unchanged"
	special := unchanged || result.Classification == "proper_name" || result.Classification == "number" || result.Classification == "acronym"
	if result.SemanticSenseID == "" && result.NewSenseRef == "" && !special {
		return errors.New("a semantic sense reference is required")
	}
	if unchanged && (result.SemanticSenseID != "" || result.NewSenseRef != "") {
		return errors.New("an unchanged token must not reference a semantic sense")
	}
	return validateLinguisticSenseReference(result.SemanticSenseID, result.NewSenseRef, result.Kind, sourceLanguage, targetLanguage, candidates, newSenses)
}

func validateLinguisticSenseReference(id, ref string, kind Kind, sourceLanguage, targetLanguage string, candidates map[string]SenseCandidate, newSenses map[string]LinguisticNewSense) error {
	if id == "" && ref == "" {
		return nil
	}
	if id != "" {
		candidate, ok := candidates[id]
		if !ok {
			return fmt.Errorf("semantic sense %q was not supplied as a candidate", id)
		}
		if candidate.Kind != kind {
			return fmt.Errorf("semantic sense %q has kind %q, want %q", id, candidate.Kind, kind)
		}
		if candidate.SourceLanguage != sourceLanguage || candidate.TargetLanguage != targetLanguage {
			return fmt.Errorf("semantic sense %q does not match article language", id)
		}
		return nil
	}
	sense, ok := newSenses[ref]
	if !ok {
		return fmt.Errorf("new sense ref %q is not defined", ref)
	}
	if sense.Kind != kind {
		return fmt.Errorf("new sense ref %q has kind %q, want %q", ref, sense.Kind, kind)
	}
	return nil
}

func validateLinguisticConstructionReference(id, ref string, kind Kind, candidates map[string]SenseCandidate, newSenses map[string]LinguisticNewSense) error {
	if id == "" && ref == "" {
		return nil
	}
	if id != "" {
		candidate, ok := candidates[id]
		if !ok {
			return fmt.Errorf("construction semantic sense %q was not supplied as a candidate", id)
		}
		if candidate.Kind != kind {
			return fmt.Errorf("construction semantic sense %q has kind %q, want %q", id, candidate.Kind, kind)
		}
		return nil
	}
	sense, ok := newSenses[ref]
	if !ok {
		return fmt.Errorf("construction new sense ref %q is not defined", ref)
	}
	if sense.Kind != kind {
		return fmt.Errorf("construction new sense ref %q has kind %q, want %q", ref, sense.Kind, kind)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Translation validation and merge

// ValidateTranslation validates a translation artifact against the exact
// validated linguistic artifact of the same paragraph. Every id must
// correspond exactly; no field can retokenize or relink anything.
func ValidateTranslation(chunk PreparedChunk, linguistic *ValidatedLinguistic, artifact TranslationArtifact) error {
	if artifact.Version != pipeline.TranslationContractVersion {
		return fmt.Errorf("unsupported translation artifact version %q", artifact.Version)
	}
	if linguistic == nil || len(linguistic.Tokens) == 0 {
		return errors.New("translation requires a validated linguistic artifact")
	}
	tokenSource := make(map[string]string, len(chunk.Tokens))
	for _, token := range chunk.Tokens {
		tokenSource[token.ID] = token.SourceText
	}
	classifications := make(map[string]string, len(linguistic.Tokens))
	for _, result := range linguistic.Tokens {
		classifications[result.TokenID] = result.Classification
	}
	seenTranslationTokens := make(map[string]struct{}, len(artifact.Tokens))
	if len(artifact.Tokens) != len(linguistic.Tokens) {
		return fmt.Errorf("translation covers %d tokens for %d linguistic tokens", len(artifact.Tokens), len(linguistic.Tokens))
	}
	for index, result := range artifact.Tokens {
		if _, exists := seenTranslationTokens[result.TokenID]; exists {
			return fmt.Errorf("translation tokens[%d] repeats token %q", index, result.TokenID)
		}
		seenTranslationTokens[result.TokenID] = struct{}{}
		if _, ok := classifications[result.TokenID]; !ok {
			return fmt.Errorf("translation tokens[%d] references unknown token %q", index, result.TokenID)
		}
		if err := safeProviderText(fmt.Sprintf("translation tokens[%d].shadow_text", index), result.ShadowText, MaxShadowScalars); err != nil {
			return err
		}
		if err := validateTranslatedSubtitle(result.TokenID, classifications[result.TokenID], tokenSource[result.TokenID], result.ShadowText); err != nil {
			return fmt.Errorf("translation token %q: %w", result.TokenID, err)
		}
	}
	for _, token := range linguistic.Tokens {
		if _, ok := seenTranslationTokens[token.TokenID]; !ok {
			return fmt.Errorf("translation omits token %q", token.TokenID)
		}
	}

	newSenseByRef := make(map[string]LinguisticNewSense, len(linguistic.NewSenses))
	for _, sense := range linguistic.NewSenses {
		newSenseByRef[sense.Ref] = sense
	}
	seenTranslationSenses := make(map[string]struct{}, len(artifact.NewSenses))
	if len(artifact.NewSenses) != len(linguistic.NewSenses) {
		return fmt.Errorf("translation covers %d new senses for %d linguistic refs", len(artifact.NewSenses), len(linguistic.NewSenses))
	}
	for index, translated := range artifact.NewSenses {
		if _, exists := seenTranslationSenses[translated.Ref]; exists {
			return fmt.Errorf("translation new_senses[%d] repeats ref %q", index, translated.Ref)
		}
		seenTranslationSenses[translated.Ref] = struct{}{}
		if _, ok := newSenseByRef[translated.Ref]; !ok {
			return fmt.Errorf("translation new_senses[%d] references unknown ref %q", index, translated.Ref)
		}
		if err := validateTranslatedSense(translated, fmt.Sprintf("translation new_senses[%d]", index)); err != nil {
			return err
		}
	}
	for _, sense := range linguistic.NewSenses {
		if _, ok := seenTranslationSenses[sense.Ref]; !ok {
			return fmt.Errorf("translation omits new sense ref %q", sense.Ref)
		}
	}

	priorByRef := make(map[string]NewSense, len(chunk.PriorValidatedSenses))
	for _, sense := range chunk.PriorValidatedSenses {
		if sense.Ref != "" {
			priorByRef[sense.Ref] = sense
		}
	}
	candidatesByID := make(map[string]SenseCandidate, len(chunk.Candidates))
	for _, candidate := range chunk.Candidates {
		if candidate.ID != "" {
			candidatesByID[candidate.ID] = candidate
		}
	}
	canonicalOf := func(id, ref string) string {
		if id != "" {
			if candidate, ok := candidatesByID[id]; ok {
				return candidate.CanonicalForm
			}
		}
		if ref != "" {
			if sense, ok := newSenseByRef[ref]; ok {
				return sense.CanonicalForm
			}
			if sense, ok := priorByRef[ref]; ok {
				return sense.CanonicalForm
			}
		}
		return ""
	}
	seenTranslationConstructions := make(map[string]struct{}, len(artifact.Constructions))
	if len(artifact.Constructions) != len(linguistic.Constructions) {
		return fmt.Errorf("translation covers %d constructions for %d linguistic ids", len(artifact.Constructions), len(linguistic.Constructions))
	}
	for index, translated := range artifact.Constructions {
		if _, exists := seenTranslationConstructions[translated.ConstructionID]; exists {
			return fmt.Errorf("translation constructions[%d] repeats construction_id %q", index, translated.ConstructionID)
		}
		seenTranslationConstructions[translated.ConstructionID] = struct{}{}
		var matched *ValidatedLinguisticConstruction
		for constructionIndex := range linguistic.Constructions {
			if linguistic.Constructions[constructionIndex].ConstructionID == translated.ConstructionID {
				matched = &linguistic.Constructions[constructionIndex]
				break
			}
		}
		if matched == nil {
			return fmt.Errorf("translation constructions[%d] references unknown construction_id %q", index, translated.ConstructionID)
		}
		if strings.TrimSpace(translated.ShadowText) == "" {
			return fmt.Errorf("translation construction %q has a blank shadow_text", translated.ConstructionID)
		}
		if err := safeProviderText(fmt.Sprintf("translation constructions[%d].shadow_text", index), translated.ShadowText, MaxShadowScalars); err != nil {
			return err
		}
		forbids := []string{canonicalOf(matched.Construction.SemanticSenseID, matched.Construction.NewSenseRef)}
		var memberParts []string
		for _, tokenID := range matched.Construction.TokenIDs {
			memberParts = append(memberParts, tokenSource[tokenID])
		}
		forbids = append(forbids, strings.Join(memberParts, " "))
		for _, span := range matched.Spans {
			forbids = append(forbids, span.SourceText)
		}
		if err := validateConstructionSubtitle(translated.ShadowText, forbids); err != nil {
			return fmt.Errorf("translation construction %q: %w", translated.ConstructionID, err)
		}
	}
	for _, construction := range linguistic.Constructions {
		if _, ok := seenTranslationConstructions[construction.ConstructionID]; !ok {
			return fmt.Errorf("translation omits construction %q", construction.ConstructionID)
		}
	}
	return nil
}

// validateTranslatedSubtitle applies the v3 subtitle invariants to one
// translation token.
func validateTranslatedSubtitle(tokenID, classification, source, shadow string) error {
	normalizedSource, sourceErr := NormalizeForm(source)
	normalizedShadow := ""
	if strings.TrimSpace(shadow) != "" {
		var err error
		normalizedShadow, err = NormalizeForm(shadow)
		if err != nil {
			return err
		}
	}
	switch classification {
	case "unchanged":
		if sourceErr == nil && normalizedShadow != "" && normalizedShadow != normalizedSource {
			return errors.New("an unchanged token shadow_text must be empty or match the source text")
		}
	case "proper_name", "number", "acronym":
		if sourceErr == nil && normalizedShadow != "" && normalizedShadow == normalizedSource {
			return errors.New("source-copy shadow_text is not a translation")
		}
	default:
		if normalizedShadow == "" {
			return fmt.Errorf("shadow_text is required for translated token %q", tokenID)
		}
		if sourceErr == nil && normalizedShadow == normalizedSource {
			return errors.New("shadow_text copies the Dutch source text; the subtitle must be an English translation")
		}
	}
	return nil
}

func validateTranslatedSense(translated TranslationNewSense, name string) error {
	if strings.TrimSpace(translated.PrimaryTranslation) == "" {
		return fmt.Errorf("%s.primary_translation must not be empty", name)
	}
	if err := safeProviderText(name+".primary_translation", translated.PrimaryTranslation, MaxNoteScalars); err != nil {
		return err
	}
	if err := safeProviderText(name+".literal_translation", translated.LiteralTranslation, MaxNoteScalars); err != nil {
		return err
	}
	if len(translated.Alternatives) > MaxAlternatives {
		return fmt.Errorf("%s has too many alternatives", name)
	}
	seen := make(map[string]struct{}, len(translated.Alternatives))
	for index, alternative := range translated.Alternatives {
		if err := safeProviderText(fmt.Sprintf("%s.alternatives[%d]", name, index), alternative, MaxNoteScalars); err != nil {
			return err
		}
		value := strings.TrimSpace(alternative)
		if value == "" {
			return fmt.Errorf("%s alternative is empty", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s alternative is duplicated", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// MergeLinguisticTranslation merges the validated artifacts into the normal
// v3 response by exact ids. The server-only construction ids are discarded.
// Callers must still run ValidateChunkResponse over the merged response.
func MergeLinguisticTranslation(linguistic *ValidatedLinguistic, artifact TranslationArtifact) (Response, error) {
	if linguistic == nil {
		return Response{}, errors.New("merge requires a validated linguistic artifact")
	}
	shadowByToken := make(map[string]string, len(artifact.Tokens))
	for _, result := range artifact.Tokens {
		shadowByToken[result.TokenID] = result.ShadowText
	}
	translationByRef := make(map[string]TranslationNewSense, len(artifact.NewSenses))
	for _, sense := range artifact.NewSenses {
		translationByRef[sense.Ref] = sense
	}
	shadowByConstruction := make(map[string]string, len(artifact.Constructions))
	for _, construction := range artifact.Constructions {
		shadowByConstruction[construction.ConstructionID] = construction.ShadowText
	}
	merged := Response{Version: AnalysisContractVersion}
	for _, token := range linguistic.Tokens {
		shadow, ok := shadowByToken[token.TokenID]
		if !ok {
			return Response{}, fmt.Errorf("merge: translation omits token %q", token.TokenID)
		}
		merged.Tokens = append(merged.Tokens, TokenResult{
			TokenID: token.TokenID, Classification: token.Classification, Kind: token.Kind,
			SemanticSenseID: token.SemanticSenseID, NewSenseRef: token.NewSenseRef,
			ShadowText: shadow, CanonicalPronunciation: token.CanonicalPronunciation,
			ContextPronunciationKey: token.ContextPronunciationKey, ConfidenceMilli: token.ConfidenceMilli,
		})
	}
	for _, sense := range linguistic.NewSenses {
		translated, ok := translationByRef[sense.Ref]
		if !ok {
			return Response{}, fmt.Errorf("merge: translation omits new sense ref %q", sense.Ref)
		}
		merged.NewSenses = append(merged.NewSenses, NewSense{
			Ref: sense.Ref, Kind: sense.Kind, CanonicalForm: sense.CanonicalForm,
			NormalizedForm: sense.NormalizedForm, Lemma: sense.Lemma, PartOfSpeech: sense.PartOfSpeech,
			SenseDiscriminator: sense.SenseDiscriminator, PrimaryTranslation: translated.PrimaryTranslation,
			Alternatives:       append([]string(nil), translated.Alternatives...),
			LiteralTranslation: translated.LiteralTranslation, MeaningNote: sense.MeaningNote,
			UsageNote: sense.UsageNote, PartsNote: sense.PartsNote,
			CanonicalPronunciationText: sense.CanonicalPronunciationText,
		})
	}
	for _, construction := range linguistic.Constructions {
		shadow, ok := shadowByConstruction[construction.ConstructionID]
		if !ok {
			return Response{}, fmt.Errorf("merge: translation omits construction %q", construction.ConstructionID)
		}
		merged.Constructions = append(merged.Constructions, Construction{
			Kind: construction.Construction.Kind, Role: construction.Construction.Role,
			SemanticSenseID: construction.Construction.SemanticSenseID,
			NewSenseRef:     construction.Construction.NewSenseRef, ShadowText: shadow,
			CanonicalPronunciationText: construction.Construction.CanonicalPronunciationText,
			ContextPronunciationKey:    construction.Construction.ContextPronunciationKey,
			ConfidenceMilli:            construction.Construction.ConfidenceMilli,
			TokenIDs:                   append([]string(nil), construction.Construction.TokenIDs...),
			Spans:                      append([]SpanRef(nil), construction.Construction.Spans...),
		})
	}
	return merged, nil
}

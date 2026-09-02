// Package semantics contains the deterministic preparation and validation
// boundary for the reader's versioned semantic compiler. It deliberately has
// no provider or HTTP dependencies: provider output is untrusted data until it
// has passed this package's checks.
package semantics

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"doublangu/internal/library"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// AnalysisContractVersion is part of the article cache key. A contract
	// change is intentionally an explicit cache invalidation event.
	AnalysisContractVersion = "reader.analysis.v2"
	PromptVersion           = "reader-analysis-prompt.v2"
	ProviderID              = "codex-app-server"
	MaxAlternatives         = 3
	MaxShadowScalars        = 160
	MaxPronunciationScalars = 640
	MaxNoteScalars          = 1000
)

// Kind is the semantic vocabulary shared by lexical items and constructions.
type Kind string

const (
	KindWord       Kind = "word"
	KindPhrase     Kind = "phrase"
	KindIdiom      Kind = "idiom"
	KindExpression Kind = "expression"
	KindProverb    Kind = "proverb"
)

func (k Kind) Valid() bool {
	return k == KindWord || k == KindPhrase || k == KindIdiom || k == KindExpression || k == KindProverb
}

// Block is the exact, ordered source supplied to semantic analysis.
type Block struct {
	BlockIndex int    `json:"block_index"`
	SourceText string `json:"source_text"`
}

// Token is a deterministic source anchor. Its ID and offsets never come from
// the provider and are stable for an unchanged article revision.
type Token struct {
	ID             string `json:"token_id"`
	BlockIndex     int    `json:"block_index"`
	TokenIndex     int    `json:"token_index"`
	StartUTF16     int    `json:"start_utf16"`
	EndUTF16       int    `json:"end_utf16"`
	SourceText     string `json:"source_text"`
	NormalizedForm string `json:"normalized_form"`
	Lemma          string `json:"lemma"`
}

// SenseCandidate is a small local-lexicon entry supplied to Codex for only
// the words and forms present in this article.
type SenseCandidate struct {
	ID                 string `json:"id"`
	SemanticItemID     string `json:"semantic_item_id"`
	SourceLanguage     string `json:"source_language"`
	TargetLanguage     string `json:"target_language"`
	Kind               Kind   `json:"kind"`
	CanonicalForm      string `json:"canonical_form"`
	NormalizedForm     string `json:"normalized_form"`
	Lemma              string `json:"lemma"`
	PartOfSpeech       string `json:"part_of_speech"`
	PrimaryTranslation string `json:"primary_translation"`
	SenseDiscriminator string `json:"sense_discriminator"`
}

// PreparedArticle is the complete deterministic input to a provider turn.
type PreparedArticle struct {
	Title          string
	SourceLanguage string
	TargetLanguage string
	ContentHash    string
	Blocks         []Block
	Tokens         []Token
	Candidates     []SenseCandidate
}

// TokenResult is one lexical classification. Exactly one result is required
// for every PreparedArticle token.
type TokenResult struct {
	TokenID                 string `json:"token_id"`
	Classification          string `json:"classification"`
	Kind                    Kind   `json:"kind"`
	SemanticSenseID         string `json:"semantic_sense_id"`
	NewSenseRef             string `json:"new_sense_ref"`
	ShadowText              string `json:"shadow_text"`
	CanonicalPronunciation  string `json:"canonical_pronunciation_text"`
	ContextPronunciationKey string `json:"context_pronunciation_key"`
	ConfidenceMilli         int    `json:"confidence_milli"`
}

// NewSense is a provider proposal. The server assigns its durable ID only
// after the complete response has been validated.
type NewSense struct {
	Ref                        string   `json:"ref"`
	Kind                       Kind     `json:"kind"`
	CanonicalForm              string   `json:"canonical_form"`
	NormalizedForm             string   `json:"normalized_form"`
	Lemma                      string   `json:"lemma"`
	PartOfSpeech               string   `json:"part_of_speech"`
	SenseDiscriminator         string   `json:"sense_discriminator"`
	PrimaryTranslation         string   `json:"primary_translation"`
	Alternatives               []string `json:"alternatives"`
	LiteralTranslation         string   `json:"literal_translation"`
	MeaningNote                string   `json:"meaning_note"`
	UsageNote                  string   `json:"usage_note"`
	PartsNote                  string   `json:"parts_note"`
	CanonicalPronunciationText string   `json:"canonical_pronunciation_text"`
}

// SpanRef identifies an exact source occurrence. The provider supplies text
// and occurrence, while validation computes the browser offsets.
type SpanRef struct {
	BlockIndex int    `json:"block_index"`
	SourceText string `json:"source_text"`
	Occurrence int    `json:"occurrence"`
}

// Sentence is an exact source sentence occurrence.
type Sentence struct {
	Source SpanRef `json:"source"`
}

// Construction describes either one contiguous phrase or a discontinuous
// construction whose members must remain in source order.
type Construction struct {
	Kind                       Kind      `json:"kind"`
	Role                       string    `json:"role"`
	SemanticSenseID            string    `json:"semantic_sense_id"`
	NewSenseRef                string    `json:"new_sense_ref"`
	ShadowText                 string    `json:"shadow_text"`
	CanonicalPronunciationText string    `json:"canonical_pronunciation_text"`
	ContextPronunciationKey    string    `json:"context_pronunciation_key"`
	ConfidenceMilli            int       `json:"confidence_milli"`
	TokenIDs                   []string  `json:"token_ids"`
	Spans                      []SpanRef `json:"spans"`
}

// Response is the only accepted v2 provider payload.
type Response struct {
	Version       string         `json:"version"`
	Sentences     []Sentence     `json:"sentences"`
	Tokens        []TokenResult  `json:"tokens"`
	NewSenses     []NewSense     `json:"new_senses"`
	Constructions []Construction `json:"constructions"`
}

// ResolvedSpan is a validated provider span with browser offsets.
type ResolvedSpan struct {
	BlockIndex int
	StartUTF16 int
	EndUTF16   int
	SourceText string
}

type ResolvedSentence struct {
	Index int
	Span  ResolvedSpan
}

type ResolvedToken struct {
	Token  Token
	Result TokenResult
}

type ResolvedConstruction struct {
	Construction Construction
	Spans        []ResolvedSpan
}

// ValidatedResponse contains the original response plus deterministic spans.
type ValidatedResponse struct {
	Response      Response
	Sentences     []ResolvedSentence
	Tokens        []ResolvedToken
	Constructions []ResolvedConstruction
}

var foldCase = cases.Fold()

// NormalizeForm is the semantic lookup normalization: NFC, Unicode case
// folding, and collapsed whitespace.
func NormalizeForm(input string) (string, error) {
	if !utf8.ValidString(input) {
		return "", errors.New("text must be valid UTF-8")
	}
	value := norm.NFC.String(foldCase.String(norm.NFC.String(input)))
	var b strings.Builder
	spacePending := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			if b.Len() > 0 {
				spacePending = true
			}
			continue
		}
		if unicode.IsControl(r) {
			return "", errors.New("text contains a control character")
		}
		if spacePending {
			b.WriteByte(' ')
			spacePending = false
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "", errors.New("text must not be empty")
	}
	return norm.NFC.String(b.String()), nil
}

// ContentHash returns the versioned length-delimited article identity. Exact
// source bytes are included, so normalization cannot merge distinct articles.
func ContentHash(title, sourceLanguage, targetLanguage string, blocks []Block) string {
	var b bytes.Buffer
	writePart := func(value []byte) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		b.Write(length[:])
		b.Write(value)
	}
	writePart([]byte("doublangu.article-content.v1"))
	writePart([]byte(title))
	writePart([]byte(sourceLanguage))
	writePart([]byte(targetLanguage))
	for _, block := range blocks {
		var index [8]byte
		binary.BigEndian.PutUint64(index[:], uint64(block.BlockIndex))
		b.Write(index[:])
		writePart([]byte(block.SourceText))
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

// Prepare validates languages and produces deterministic token anchors.
func Prepare(title, sourceLanguage, targetLanguage string, blocks []Block, candidates []SenseCandidate) (PreparedArticle, error) {
	if strings.TrimSpace(title) == "" || !utf8.ValidString(title) {
		return PreparedArticle{}, errors.New("title is required and must be valid UTF-8")
	}
	sourceLanguage, err := library.ParseBCP47(sourceLanguage)
	if err != nil {
		return PreparedArticle{}, fmt.Errorf("source language: %w", err)
	}
	targetLanguage, err = library.ParseBCP47(targetLanguage)
	if err != nil {
		return PreparedArticle{}, fmt.Errorf("target language: %w", err)
	}
	if len(blocks) == 0 {
		return PreparedArticle{}, errors.New("at least one source block is required")
	}
	prepared := PreparedArticle{
		Title: title, SourceLanguage: sourceLanguage, TargetLanguage: targetLanguage,
		Blocks: append([]Block(nil), blocks...), Candidates: append([]SenseCandidate(nil), candidates...),
	}
	for index := range prepared.Blocks {
		block := &prepared.Blocks[index]
		if block.BlockIndex != index || block.SourceText == "" || !utf8.ValidString(block.SourceText) {
			return PreparedArticle{}, fmt.Errorf("invalid source block %d", index)
		}
		for _, token := range tokenize(index, block.SourceText) {
			token.TokenIndex = len(prepared.Tokens)
			token.ID = fmt.Sprintf("b%d:t%d", index, token.TokenIndex)
			prepared.Tokens = append(prepared.Tokens, token)
		}
	}
	prepared.ContentHash = ContentHash(title, sourceLanguage, targetLanguage, prepared.Blocks)
	return prepared, nil
}

func tokenize(blockIndex int, text string) []Token {
	tokens := make([]Token, 0)
	start := -1
	lastWordEnd := -1
	for byteIndex, r := range text {
		if start < 0 {
			if isWordRune(r) {
				start = byteIndex
				lastWordEnd = byteIndex + utf8.RuneLen(r)
			}
			continue
		}
		if isWordRune(r) || unicode.IsMark(r) {
			lastWordEnd = byteIndex + utf8.RuneLen(r)
			continue
		}
		if isJoiner(r) && hasWordAfter(text, byteIndex+utf8.RuneLen(r)) {
			continue
		}
		tokens = append(tokens, makeToken(blockIndex, text, start, lastWordEnd))
		start, lastWordEnd = -1, -1
		if isWordRune(r) {
			start = byteIndex
			lastWordEnd = byteIndex + utf8.RuneLen(r)
		}
	}
	if start >= 0 {
		tokens = append(tokens, makeToken(blockIndex, text, start, lastWordEnd))
	}
	return tokens
}

func makeToken(blockIndex int, text string, start, end int) Token {
	value := text[start:end]
	normalized, _ := NormalizeForm(value)
	startUTF16 := utf16Len(text[:start])
	return Token{
		BlockIndex: blockIndex, StartUTF16: startUTF16, EndUTF16: startUTF16 + utf16Len(value),
		SourceText: value, NormalizedForm: normalized,
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

func isJoiner(r rune) bool { return r == '\'' || r == '\u2019' || r == '-' || r == '\u2010' }

func hasWordAfter(text string, byteIndex int) bool {
	if byteIndex >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[byteIndex:])
	return isWordRune(r)
}

func utf16Len(value string) int { return len(utf16.Encode([]rune(value))) }

// ResolveSpan finds an exact, zero-based occurrence and converts it to UTF-16.
func ResolveSpan(block Block, sourceText string, occurrence int) (ResolvedSpan, error) {
	if occurrence < 0 || sourceText == "" {
		return ResolvedSpan{}, errors.New("span source and occurrence are invalid")
	}
	start := 0
	for current := 0; current <= occurrence; current++ {
		relative := strings.Index(block.SourceText[start:], sourceText)
		if relative < 0 {
			return ResolvedSpan{}, fmt.Errorf("source occurrence %d was not found in block %d", occurrence, block.BlockIndex)
		}
		found := start + relative
		if current == occurrence {
			return ResolvedSpan{BlockIndex: block.BlockIndex, StartUTF16: utf16Len(block.SourceText[:found]), EndUTF16: utf16Len(block.SourceText[:found+len(sourceText)]), SourceText: sourceText}, nil
		}
		start = found + len(sourceText)
	}
	return ResolvedSpan{}, errors.New("source occurrence was not found")
}

func spanOverlap(a, b ResolvedSpan) bool {
	return a.BlockIndex == b.BlockIndex && a.StartUTF16 < b.EndUTF16 && b.StartUTF16 < a.EndUTF16
}

func safeProviderText(name, value string, max int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if strings.ContainsAny(value, "<>") {
		return fmt.Errorf("%s must not contain markup delimiters", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	if max > 0 && utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s is too long", name)
	}
	return nil
}

// ValidateResponse enforces source identity, token coverage, sense references,
// ordered sentence spans, and legal construction layering.
func ValidateResponse(input PreparedArticle, response Response) (ValidatedResponse, error) {
	return validateResponse(input, response, nil)
}

// ValidateResponseWithPrior validates a paragraph response against reusable
// senses that were validated in earlier paragraphs. The prior senses are
// references only; the current response must not redefine one of them.
func ValidateResponseWithPrior(input PreparedArticle, response Response, prior []NewSense) (ValidatedResponse, error) {
	return validateResponse(input, response, prior)
}

func validateResponse(input PreparedArticle, response Response, prior []NewSense) (ValidatedResponse, error) {
	if response.Version != AnalysisContractVersion {
		return ValidatedResponse{}, fmt.Errorf("unsupported analysis response version %q", response.Version)
	}
	if len(response.Tokens) != len(input.Tokens) {
		return ValidatedResponse{}, fmt.Errorf("token coverage has %d results for %d supplied tokens", len(response.Tokens), len(input.Tokens))
	}
	tokenByID := make(map[string]Token, len(input.Tokens))
	for _, token := range input.Tokens {
		tokenByID[token.ID] = token
	}
	seenTokens := make(map[string]struct{}, len(response.Tokens))
	candidateByID := make(map[string]SenseCandidate, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if candidate.ID != "" {
			candidateByID[candidate.ID] = candidate
		}
	}
	newByRef := make(map[string]NewSense, len(prior)+len(response.NewSenses))
	for index, sense := range prior {
		if sense.Ref == "" {
			return ValidatedResponse{}, fmt.Errorf("prior new_senses[%d] has no ref", index)
		}
		if err := safeProviderText(fmt.Sprintf("prior new_senses[%d].ref", index), sense.Ref, 120); err != nil {
			return ValidatedResponse{}, err
		}
		if _, exists := newByRef[sense.Ref]; exists {
			return ValidatedResponse{}, fmt.Errorf("prior new sense ref %q is duplicated", sense.Ref)
		}
		if err := validateSense(sense, fmt.Sprintf("prior new_senses[%d]", index)); err != nil {
			return ValidatedResponse{}, err
		}
		newByRef[sense.Ref] = sense
	}
	for index, sense := range response.NewSenses {
		if sense.Ref == "" {
			return ValidatedResponse{}, fmt.Errorf("new_senses[%d] has no ref", index)
		}
		if err := safeProviderText(fmt.Sprintf("new_senses[%d].ref", index), sense.Ref, 120); err != nil {
			return ValidatedResponse{}, err
		}
		if _, exists := newByRef[sense.Ref]; exists {
			return ValidatedResponse{}, fmt.Errorf("new sense ref %q is duplicated", sense.Ref)
		}
		if err := validateSense(sense, fmt.Sprintf("new_senses[%d]", index)); err != nil {
			return ValidatedResponse{}, err
		}
		newByRef[sense.Ref] = sense
	}
	validated := ValidatedResponse{Response: response, Tokens: make([]ResolvedToken, 0, len(response.Tokens))}
	for index, result := range response.Tokens {
		token, ok := tokenByID[result.TokenID]
		if !ok {
			return ValidatedResponse{}, fmt.Errorf("tokens[%d] references unknown token %q", index, result.TokenID)
		}
		if _, exists := seenTokens[result.TokenID]; exists {
			return ValidatedResponse{}, fmt.Errorf("token %q appears more than once", result.TokenID)
		}
		seenTokens[result.TokenID] = struct{}{}
		if err := validateTokenResult(result, input.SourceLanguage, input.TargetLanguage, candidateByID, newByRef); err != nil {
			return ValidatedResponse{}, fmt.Errorf("token %q: %w", result.TokenID, err)
		}
		validated.Tokens = append(validated.Tokens, ResolvedToken{Token: token, Result: result})
	}
	for _, token := range input.Tokens {
		if _, ok := seenTokens[token.ID]; !ok {
			return ValidatedResponse{}, fmt.Errorf("token %q is missing from response", token.ID)
		}
	}

	var previousSentence *ResolvedSpan
	for index, sentence := range response.Sentences {
		if sentence.Source.BlockIndex < 0 || sentence.Source.BlockIndex >= len(input.Blocks) {
			return ValidatedResponse{}, fmt.Errorf("sentences[%d] has invalid block index", index)
		}
		span, err := ResolveSpan(input.Blocks[sentence.Source.BlockIndex], sentence.Source.SourceText, sentence.Source.Occurrence)
		if err != nil {
			return ValidatedResponse{}, fmt.Errorf("sentences[%d]: %w", index, err)
		}
		if previousSentence != nil {
			if span.BlockIndex < previousSentence.BlockIndex || (span.BlockIndex == previousSentence.BlockIndex && span.StartUTF16 < previousSentence.StartUTF16) {
				return ValidatedResponse{}, fmt.Errorf("sentences[%d] is out of source order", index)
			}
			if spanOverlap(*previousSentence, span) {
				return ValidatedResponse{}, fmt.Errorf("sentences[%d] overlaps a prior sentence", index)
			}
		}
		copySpan := span
		previousSentence = &copySpan
		validated.Sentences = append(validated.Sentences, ResolvedSentence{Index: index, Span: span})
	}
	if len(validated.Sentences) == 0 {
		return ValidatedResponse{}, errors.New("at least one sentence is required")
	}
	for _, token := range input.Tokens {
		covered := false
		for _, sentence := range validated.Sentences {
			if sentence.Span.BlockIndex == token.BlockIndex && token.StartUTF16 >= sentence.Span.StartUTF16 && token.EndUTF16 <= sentence.Span.EndUTF16 {
				covered = true
				break
			}
		}
		if !covered {
			return ValidatedResponse{}, fmt.Errorf("token %q is outside the sentence coverage", token.ID)
		}
	}

	for index, construction := range response.Constructions {
		if construction.Role != "contiguous_construction" && construction.Role != "discontinuous_construction" {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] has invalid role %q", index, construction.Role)
		}
		if !construction.Kind.Valid() || construction.Kind == KindWord {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] has invalid kind", index)
		}
		if construction.SemanticSenseID != "" && construction.NewSenseRef != "" {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] has two sense references", index)
		}
		if err := validateSenseReference(construction.SemanticSenseID, construction.NewSenseRef, construction.Kind, input.SourceLanguage, input.TargetLanguage, candidateByID, newByRef); err != nil {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d]: %w", index, err)
		}
		if err := safeProviderText("construction shadow_text", construction.ShadowText, MaxShadowScalars); err != nil {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d]: %w", index, err)
		}
		if err := safeProviderText("construction canonical_pronunciation_text", construction.CanonicalPronunciationText, MaxPronunciationScalars); err != nil {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d]: %w", index, err)
		}
		if err := safeProviderText("construction context_pronunciation_key", construction.ContextPronunciationKey, MaxPronunciationScalars); err != nil {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d]: %w", index, err)
		}
		if construction.ConfidenceMilli < 0 || construction.ConfidenceMilli > 1000 {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] confidence is outside 0..1000", index)
		}
		wantSpans := 1
		if construction.Role == "discontinuous_construction" {
			wantSpans = 2
		}
		if len(construction.Spans) < wantSpans {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] requires at least %d spans", index, wantSpans)
		}
		if len(construction.TokenIDs) == 0 {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] must reference at least one token", index)
		}
		if construction.Role == "contiguous_construction" && len(construction.Spans) != 1 {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] must have exactly one span", index)
		}
		resolved := make([]ResolvedSpan, 0, len(construction.Spans))
		constructionTokenIDs := make(map[string]struct{}, len(construction.TokenIDs))
		for spanIndex, ref := range construction.Spans {
			if ref.BlockIndex < 0 || ref.BlockIndex >= len(input.Blocks) {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d].spans[%d] has invalid block index", index, spanIndex)
			}
			span, err := ResolveSpan(input.Blocks[ref.BlockIndex], ref.SourceText, ref.Occurrence)
			if err != nil {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d].spans[%d]: %w", index, spanIndex, err)
			}
			if spanIndex > 0 {
				previous := resolved[spanIndex-1]
				if span.BlockIndex != previous.BlockIndex || span.StartUTF16 < previous.EndUTF16 {
					return ValidatedResponse{}, fmt.Errorf("constructions[%d] spans are not ordered and non-overlapping", index)
				}
			}
			resolved = append(resolved, span)
		}
		seenConstructionTokens := make(map[string]struct{}, len(construction.TokenIDs))
		for _, tokenID := range construction.TokenIDs {
			token, ok := tokenByID[tokenID]
			if !ok {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] references unknown token %q", index, tokenID)
			}
			if _, exists := seenConstructionTokens[tokenID]; exists {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] repeats token %q", index, tokenID)
			}
			seenConstructionTokens[tokenID] = struct{}{}
			constructionTokenIDs[tokenID] = struct{}{}
			covered := false
			for _, span := range resolved {
				if span.BlockIndex == token.BlockIndex && token.StartUTF16 >= span.StartUTF16 && token.EndUTF16 <= span.EndUTF16 {
					covered = true
					break
				}
			}
			if !covered {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] token %q is outside its spans", index, tokenID)
			}
		}
		if construction.Role == "contiguous_construction" {
			container := resolved[0]
			for _, token := range input.Tokens {
				if token.BlockIndex == container.BlockIndex && token.StartUTF16 >= container.StartUTF16 && token.EndUTF16 <= container.EndUTF16 {
					if _, ok := constructionTokenIDs[token.ID]; !ok {
						return ValidatedResponse{}, fmt.Errorf("constructions[%d] omits component token %q", index, token.ID)
					}
				}
			}
		}
		validated.Constructions = append(validated.Constructions, ResolvedConstruction{Construction: construction, Spans: resolved})
	}
	for leftIndex, left := range validated.Constructions {
		if left.Construction.Role != "contiguous_construction" || len(left.Spans) != 1 {
			continue
		}
		for rightIndex := leftIndex + 1; rightIndex < len(validated.Constructions); rightIndex++ {
			right := validated.Constructions[rightIndex]
			if right.Construction.Role != "contiguous_construction" || len(right.Spans) != 1 {
				continue
			}
			if spanOverlap(left.Spans[0], right.Spans[0]) {
				return ValidatedResponse{}, fmt.Errorf("contiguous constructions %d and %d overlap", leftIndex, rightIndex)
			}
		}
	}
	return validated, nil
}

func validateTokenResult(result TokenResult, sourceLanguage, targetLanguage string, candidates map[string]SenseCandidate, newSenses map[string]NewSense) error {
	if result.Classification == "" {
		return errors.New("classification is required")
	}
	if err := safeProviderText("classification", result.Classification, MaxNoteScalars); err != nil {
		return err
	}
	if result.Kind.Valid() == false {
		return fmt.Errorf("invalid kind %q", result.Kind)
	}
	if result.ConfidenceMilli < 0 || result.ConfidenceMilli > 1000 {
		return errors.New("confidence is outside 0..1000")
	}
	if err := safeProviderText("shadow_text", result.ShadowText, MaxShadowScalars); err != nil {
		return err
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
	special := result.Classification == "proper_name" || result.Classification == "number" || result.Classification == "acronym" || result.Classification == "unchanged"
	if result.SemanticSenseID == "" && result.NewSenseRef == "" && !special {
		return errors.New("a semantic sense reference is required")
	}
	return validateSenseReference(result.SemanticSenseID, result.NewSenseRef, result.Kind, sourceLanguage, targetLanguage, candidates, newSenses)
}

func validateSenseReference(id, ref string, kind Kind, sourceLanguage, targetLanguage string, candidates map[string]SenseCandidate, newSenses map[string]NewSense) error {
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

func validateSense(sense NewSense, name string) error {
	if !sense.Kind.Valid() {
		return fmt.Errorf("%s has invalid kind %q", name, sense.Kind)
	}
	for field, value := range map[string]string{
		"canonical_form": sense.CanonicalForm, "normalized_form": sense.NormalizedForm,
		"sense_discriminator": sense.SenseDiscriminator, "primary_translation": sense.PrimaryTranslation,
		"literal_translation": sense.LiteralTranslation, "meaning_note": sense.MeaningNote,
		"usage_note": sense.UsageNote, "parts_note": sense.PartsNote,
		"canonical_pronunciation_text": sense.CanonicalPronunciationText,
	} {
		if (field == "canonical_form" || field == "normalized_form") && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.%s must not be empty", name, field)
		}
		if field == "primary_translation" && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.%s must not be empty", name, field)
		}
		if field == "sense_discriminator" && strings.TrimSpace(value) == "" {
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
	if len(sense.Alternatives) > MaxAlternatives {
		return fmt.Errorf("%s has too many alternatives", name)
	}
	seen := make(map[string]struct{}, len(sense.Alternatives))
	for index, alternative := range sense.Alternatives {
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

// DecodeResponse performs strict JSON object decoding and rejects trailing
// values. It is used by the Codex adapter and is also convenient for tests.
func DecodeResponse(data []byte) (Response, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Response{}, errors.New("response contains trailing JSON")
		}
		return Response{}, fmt.Errorf("response contains malformed trailing JSON: %w", err)
	}
	return response, nil
}

// SortSpans returns a stable source-order copy for deterministic persistence.
func SortSpans(spans []ResolvedSpan) []ResolvedSpan {
	copySpans := append([]ResolvedSpan(nil), spans...)
	sort.SliceStable(copySpans, func(i, j int) bool {
		if copySpans[i].BlockIndex != copySpans[j].BlockIndex {
			return copySpans[i].BlockIndex < copySpans[j].BlockIndex
		}
		if copySpans[i].StartUTF16 != copySpans[j].StartUTF16 {
			return copySpans[i].StartUTF16 < copySpans[j].StartUTF16
		}
		return copySpans[i].EndUTF16 < copySpans[j].EndUTF16
	})
	return copySpans
}

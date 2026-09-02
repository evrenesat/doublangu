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
	// change is intentionally an explicit cache invalidation event. Contract
	// v3 supplies stable server-owned source sentence anchors in every
	// prepared chunk; caches from earlier contracts never satisfy v3 work.
	AnalysisContractVersion = "reader.analysis.v3"
	PromptVersion           = "reader-analysis-prompt.v6"
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
// Sentences are the stable, source-owned narration anchors: the server
// supplies them (created deterministically with the article and preserved
// across reanalysis), never the provider.
type PreparedArticle struct {
	Title          string
	SourceLanguage string
	TargetLanguage string
	ContentHash    string
	Blocks         []Block
	Tokens         []Token
	Candidates     []SenseCandidate
	Sentences      []ResolvedSentence
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

// Response is the only accepted provider payload. Contract v3 contains no
// provider-authored sentences: the server supplies stable source sentence
// anchors and the provider annotates tokens and constructions only.
type Response struct {
	Version       string         `json:"version"`
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
	// Index is the sentence's position within its own block, starting at
	// zero for the block's first sentence.
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
		if err := validateTokenResult(result, token, input.SourceLanguage, input.TargetLanguage, candidateByID, newByRef); err != nil {
			return ValidatedResponse{}, fmt.Errorf("token %q: %w", result.TokenID, err)
		}
		validated.Tokens = append(validated.Tokens, ResolvedToken{Token: token, Result: result})
	}
	for _, token := range input.Tokens {
		if _, ok := seenTokens[token.ID]; !ok {
			return ValidatedResponse{}, fmt.Errorf("token %q is missing from response", token.ID)
		}
	}

	// Source sentence anchors are server-owned and never provider-authored.
	// They must be internally consistent and cover every supplied token.
	anchors, err := validateSourceSentenceAnchors(input)
	if err != nil {
		return ValidatedResponse{}, err
	}
	validated.Sentences = anchors
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
			return ValidatedResponse{}, fmt.Errorf("token %q is outside the supplied source sentence anchors", token.ID)
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
		if strings.TrimSpace(construction.ShadowText) == "" {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d]: shadow_text is required", index)
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
		// Members are exact fixed lexical items: they are unique, ordered in
		// source order, inside the construction's spans, and all inside one
		// supplied source sentence.
		seenConstructionTokens := make(map[string]struct{}, len(construction.TokenIDs))
		memberTokens := make([]Token, 0, len(construction.TokenIDs))
		sentenceIndex := -1
		lastStart := -1
		lastBlock := -1
		var memberParts []string
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
			if lastBlock >= 0 && (token.BlockIndex != lastBlock || token.StartUTF16 <= lastStart) {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] member tokens are not in source order", index)
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
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] token %q is outside its spans", index, tokenID)
			}
			owner := sentenceForToken(token)
			if owner < 0 {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] token %q is outside the sentence coverage", index, tokenID)
			}
			if sentenceIndex < 0 {
				sentenceIndex = owner
			} else if owner != sentenceIndex {
				return ValidatedResponse{}, fmt.Errorf("constructions[%d] crosses a source sentence boundary", index)
			}
			memberTokens = append(memberTokens, token)
			memberParts = append(memberParts, token.SourceText)
		}
		// The role must match the membership shape: a contiguous construction
		// is exactly one adjacent run of members; a discontinuous construction
		// has at least two runs (inserted modifiers and context words are not
		// members and never merge runs).
		if runs := memberRunCount(memberTokens); runs != 1 && construction.Role == "contiguous_construction" {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] contiguous members do not form exactly one run", index)
		} else if runs < 2 && construction.Role == "discontinuous_construction" {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d] discontinuous members form fewer than two runs", index)
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
		if err := validateConstructionSubtitle(construction.ShadowText, constructionSubtitleForbids(input, construction, memberParts, resolved, candidateByID, newByRef)); err != nil {
			return ValidatedResponse{}, fmt.Errorf("constructions[%d]: %w", index, err)
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

func validateTokenResult(result TokenResult, token Token, sourceLanguage, targetLanguage string, candidates map[string]SenseCandidate, newSenses map[string]NewSense) error {
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
	if !special && strings.TrimSpace(result.ShadowText) == "" {
		return errors.New("shadow_text is required for a translated token")
	}
	normalizedSource, sourceErr := NormalizeForm(token.SourceText)
	normalizedShadow := ""
	shadowErr := error(nil)
	if strings.TrimSpace(result.ShadowText) != "" {
		normalizedShadow, shadowErr = NormalizeForm(result.ShadowText)
	}
	switch result.Classification {
	case "unchanged":
		// Deliberately untranslated: a real English translation is invalid,
		// and so is a sense reference (a sense would imply a translation).
		if result.SemanticSenseID != "" || result.NewSenseRef != "" {
			return errors.New("an unchanged token must not reference a semantic sense")
		}
		if shadowErr != nil {
			return shadowErr
		}
		if sourceErr == nil && normalizedShadow != "" && normalizedShadow != normalizedSource {
			return errors.New("an unchanged token shadow_text must be empty or match the source text")
		}
	case "proper_name", "number", "acronym":
		// These may omit a subtitle. When they carry one it must be a real
		// translation, never a copy of the Dutch source spelling.
		if shadowErr != nil {
			return shadowErr
		}
		if sourceErr == nil && normalizedShadow != "" && normalizedShadow == normalizedSource {
			return errors.New("source-copy shadow_text is not a translation")
		}
	default:
		// Ordinary translated token: subtitle required (checked above); a
		// normalized copy of the Dutch source spelling is never an English
		// subtitle and must enter correction.
		if normalizedShadow == "" {
			return errors.New("shadow_text is required for a translated token")
		}
		if shadowErr != nil {
			return shadowErr
		}
		if sourceErr == nil && normalizedShadow == normalizedSource {
			return errors.New("shadow_text copies the Dutch source text; the subtitle must be an English translation")
		}
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

// validateSourceSentenceAnchors checks the server-supplied source sentence
// anchors: exact source slices at their UTF-16 offsets, sequential per-block
// indexes, and non-overlapping in-block order. The returned list is sorted by
// block and then by index.
func validateSourceSentenceAnchors(input PreparedArticle) ([]ResolvedSentence, error) {
	byBlock := make(map[int][]ResolvedSentence)
	for _, sentence := range input.Sentences {
		blockIndex := sentence.Span.BlockIndex
		if blockIndex < 0 || blockIndex >= len(input.Blocks) {
			return nil, fmt.Errorf("sentence anchor %q references invalid block %d", sentence.Span.SourceText, blockIndex)
		}
		blockText := input.Blocks[blockIndex].SourceText
		if sentence.Span.StartUTF16 < 0 || sentence.Span.EndUTF16 <= sentence.Span.StartUTF16 {
			return nil, fmt.Errorf("sentence anchor in block %d has an invalid UTF-16 span", blockIndex)
		}
		text, err := sliceForUTF16(blockText, sentence.Span.StartUTF16, sentence.Span.EndUTF16)
		if err != nil {
			return nil, fmt.Errorf("sentence anchor in block %d: %w", blockIndex, err)
		}
		if text != sentence.Span.SourceText {
			return nil, fmt.Errorf("sentence anchor %q does not match block %d source text", sentence.Span.SourceText, blockIndex)
		}
		block := byBlock[blockIndex]
		previous := ResolvedSentence{Index: -1}
		if len(block) > 0 {
			previous = block[len(block)-1]
		}
		if sentence.Index != len(block) {
			return nil, fmt.Errorf("sentence anchor indexes in block %d are not sequential", blockIndex)
		}
		if sentence.Span.StartUTF16 < previous.Span.EndUTF16 {
			return nil, fmt.Errorf("sentence anchors in block %d overlap or are out of order", blockIndex)
		}
		byBlock[blockIndex] = append(block, sentence)
	}
	ordered := make([]ResolvedSentence, 0, len(input.Sentences))
	for blockIndex := range input.Blocks {
		ordered = append(ordered, byBlock[blockIndex]...)
	}
	return ordered, nil
}

// sliceForUTF16 returns the exact source slice for a browser UTF-16 span.
func sliceForUTF16(value string, start, end int) (string, error) {
	startByte, err := byteOffsetForUTF16(value, start)
	if err != nil {
		return "", err
	}
	endByte, err := byteOffsetForUTF16(value, end)
	if err != nil {
		return "", err
	}
	if endByte <= startByte {
		return "", errors.New("UTF-16 span must have positive length")
	}
	return value[startByte:endByte], nil
}

func byteOffsetForUTF16(value string, offset int) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("UTF-16 offset %d is negative", offset)
	}
	units := 0
	for byteOffset, r := range value {
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units == offset {
			return byteOffset, nil
		}
		if units+runeUnits > offset {
			return 0, fmt.Errorf("UTF-16 offset %d splits a surrogate pair", offset)
		}
		units += runeUnits
	}
	if units == offset {
		return len(value), nil
	}
	return 0, fmt.Errorf("UTF-16 offset %d exceeds text length %d", offset, units)
}

// memberRunCount counts maximal adjacent runs of member tokens (consecutive
// TokenIndex values within one block).
func memberRunCount(members []Token) int {
	runs := 0
	var previous *Token
	for index := range members {
		member := &members[index]
		if previous == nil || member.BlockIndex != previous.BlockIndex || member.TokenIndex != previous.TokenIndex+1 {
			runs++
		}
		previous = member
	}
	return runs
}

// constructionSubtitleForbids returns the Dutch source forms a construction
// subtitle must never copy: the joined member text, every complete provider
// span, and the referenced sense's canonical form.
func constructionSubtitleForbids(input PreparedArticle, construction Construction, memberParts []string, spans []ResolvedSpan, candidates map[string]SenseCandidate, newSenses map[string]NewSense) []string {
	forbids := make([]string, 0, 1+len(spans)+1)
	if len(memberParts) > 0 {
		forbids = append(forbids, strings.Join(memberParts, " "))
	}
	for _, span := range spans {
		forbids = append(forbids, span.SourceText)
	}
	if construction.SemanticSenseID != "" {
		if candidate, ok := candidates[construction.SemanticSenseID]; ok {
			forbids = append(forbids, candidate.CanonicalForm)
		}
	}
	if construction.NewSenseRef != "" {
		if sense, ok := newSenses[construction.NewSenseRef]; ok {
			forbids = append(forbids, sense.CanonicalForm)
		}
	}
	return forbids
}

// validateConstructionSubtitle rejects a construction subtitle that copies any
// Dutch source form: translations must be English, and a source copy is
// invalid even when the provider believes it explains the phrase.
func validateConstructionSubtitle(shadow string, forbids []string) error {
	normalizedShadow, err := NormalizeForm(shadow)
	if err != nil {
		return err
	}
	for _, forbidden := range forbids {
		normalizedForbidden, err := NormalizeForm(forbidden)
		if err != nil || normalizedForbidden == "" {
			continue
		}
		if normalizedForbidden == normalizedShadow {
			return errors.New("shadow_text copies Dutch source text; the subtitle must be an English translation")
		}
	}
	return nil
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

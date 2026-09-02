package reader

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type candidateSpan struct {
	annotation Annotation
	blockIndex int
	provider   int
}

// NormalizeCandidates validates provider candidates, resolves exact source
// occurrences into UTF-16 spans, removes overlaps deterministically, and
// applies the reader density budgets.
func NormalizeCandidates(article *Article, candidates []Candidate) (NormalizationResult, error) {
	if article == nil {
		return NormalizationResult{}, &Error{Op: "normalize annotations", Kind: KindValidation, Err: errors.New("article is nil")}
	}
	if err := article.Validate(); err != nil {
		return NormalizationResult{}, &Error{Op: "normalize annotations", Kind: KindValidation, Err: err}
	}
	result := NormalizationResult{Diagnostic: NormalizationDiagnostic{InputCandidates: len(candidates)}}
	spans := make([]candidateSpan, 0, len(candidates))
	for providerIndex, candidate := range candidates {
		span, err := normalizeCandidate(article, candidate, providerIndex)
		if err != nil {
			return NormalizationResult{}, &Error{Op: "normalize annotations", Kind: KindValidation, Err: fmt.Errorf("candidate %d: %w", providerIndex, err)}
		}
		result.Diagnostic.ValidCandidates++
		spans = append(spans, span)
	}

	sort.SliceStable(spans, func(i, j int) bool {
		left, right := spans[i], spans[j]
		if groupPriority(left.annotation.Kind) != groupPriority(right.annotation.Kind) {
			return groupPriority(left.annotation.Kind) < groupPriority(right.annotation.Kind)
		}
		leftLength := left.annotation.EndUTF16 - left.annotation.StartUTF16
		rightLength := right.annotation.EndUTF16 - right.annotation.StartUTF16
		if leftLength != rightLength {
			return leftLength > rightLength
		}
		if left.annotation.StartUTF16 != right.annotation.StartUTF16 {
			return left.annotation.StartUTF16 < right.annotation.StartUTF16
		}
		return left.provider < right.provider
	})

	accepted := make([]candidateSpan, 0, len(spans))
	for _, span := range spans {
		overlaps := false
		for _, existing := range accepted {
			if span.blockIndex == existing.blockIndex && spansOverlap(span.annotation, existing.annotation) {
				overlaps = true
				break
			}
		}
		if overlaps {
			result.Diagnostic.OverlapsDropped++
			continue
		}
		accepted = append(accepted, span)
	}
	result.Diagnostic.AcceptedCandidates = len(accepted)

	sort.SliceStable(accepted, func(i, j int) bool {
		left, right := accepted[i], accepted[j]
		if left.blockIndex != right.blockIndex {
			return left.blockIndex < right.blockIndex
		}
		if left.annotation.StartUTF16 != right.annotation.StartUTF16 {
			return left.annotation.StartUTF16 < right.annotation.StartUTF16
		}
		return left.annotation.EndUTF16 < right.annotation.EndUTF16
	})

	wordCount := countArticleWords(article)
	buckets := (wordCount + 149) / 150
	if buckets < 1 {
		buckets = 1
	}
	result.Diagnostic.AnnotationBudget = buckets * MaxAnnotationsPer150Words
	result.Diagnostic.ShadowBudget = buckets * MaxShadowsPer150Words
	result.Diagnostic.BudgetExceeded = len(accepted) > result.Diagnostic.AnnotationBudget

	// Groups are protected first, followed by provider-requested subtitles, then
	// the remaining source-order candidates. This keeps useful expressions even
	// when a provider sends an overly dense response.
	priority := make([]candidateSpan, 0, len(accepted))
	add := func(predicate func(candidateSpan) bool) {
		for _, span := range accepted {
			if predicate(span) {
				priority = append(priority, span)
			}
		}
	}
	add(func(span candidateSpan) bool { return span.annotation.Kind != KindWord })
	add(func(span candidateSpan) bool {
		return span.annotation.Kind == KindWord && span.annotation.SuggestShadow
	})
	add(func(span candidateSpan) bool {
		return span.annotation.Kind == KindWord && !span.annotation.SuggestShadow
	})

	retained := make([]Annotation, 0, len(priority))
	shadowCount := 0
	for _, span := range priority {
		if len(retained) >= result.Diagnostic.AnnotationBudget {
			result.Diagnostic.DroppedCandidates++
			continue
		}
		annotation := span.annotation
		if annotation.SuggestShadow {
			if shadowCount >= result.Diagnostic.ShadowBudget {
				// Keep the hover detail, but do not let an over-eager provider
				// turn it into a passive subtitle.
				annotation.SuggestShadow = false
				result.Diagnostic.ShadowSuppressed++
			} else {
				shadowCount++
			}
		}
		retained = append(retained, annotation)
	}
	result.Diagnostic.RetainedCandidates = len(retained)
	result.Annotations = retained
	return result, nil
}

// ValidateCandidates checks the provider-facing semantics without resolving
// overlaps. The app-server adapter uses this before accepting a final payload;
// the HTTP boundary performs the full normalization pass afterward.
func ValidateCandidates(input ArticleInput, candidates []Candidate) error {
	if len(input.Blocks) == 0 {
		return errors.New("provider input has no blocks")
	}
	for index, candidate := range candidates {
		if candidate.BlockIndex < 0 || candidate.BlockIndex >= len(input.Blocks) {
			return fmt.Errorf("candidate %d has an invalid block_index", index)
		}
		if err := validateCandidateFields(candidate); err != nil {
			return fmt.Errorf("candidate %d: %w", index, err)
		}
		if _, err := occurrenceByteOffset(input.Blocks[candidate.BlockIndex].SourceText, candidate.SourceText, candidate.Occurrence); err != nil {
			return fmt.Errorf("candidate %d: %w", index, err)
		}
	}
	return nil
}

func normalizeCandidate(article *Article, candidate Candidate, providerIndex int) (candidateSpan, error) {
	if candidate.BlockIndex < 0 || candidate.BlockIndex >= len(article.Blocks) {
		return candidateSpan{}, errors.New("block_index is out of range")
	}
	if err := validateCandidateFields(candidate); err != nil {
		return candidateSpan{}, err
	}
	block := article.Blocks[candidate.BlockIndex]
	byteStart, err := occurrenceByteOffset(block.SourceText, candidate.SourceText, candidate.Occurrence)
	if err != nil {
		return candidateSpan{}, err
	}
	byteEnd := byteStart + len(candidate.SourceText)
	startUTF16, err := UTF16Offset(block.SourceText, byteStart)
	if err != nil {
		return candidateSpan{}, err
	}
	endUTF16, err := UTF16Offset(block.SourceText, byteEnd)
	if err != nil {
		return candidateSpan{}, err
	}
	learningKey, err := NormalizeLearningKey(candidate.LearningKey)
	if err != nil {
		return candidateSpan{}, err
	}
	alternatives := make([]string, len(candidate.Alternatives))
	for index, alternative := range candidate.Alternatives {
		alternatives[index] = strings.TrimSpace(alternative)
	}
	annotation := Annotation{
		ArticleBlockID:     block.ID,
		StartUTF16:         startUTF16,
		EndUTF16:           endUTF16,
		SourceText:         candidate.SourceText,
		Kind:               candidate.Kind,
		LearningKey:        learningKey,
		PrimaryTranslation: strings.TrimSpace(candidate.PrimaryTranslation),
		Alternatives:       alternatives,
		LiteralTranslation: strings.TrimSpace(candidate.LiteralTranslation),
		MeaningNote:        strings.TrimSpace(candidate.MeaningNote),
		UsageNote:          strings.TrimSpace(candidate.UsageNote),
		PartsNote:          strings.TrimSpace(candidate.PartsNote),
		SuggestShadow:      candidate.SuggestShadow,
	}
	return candidateSpan{annotation: annotation, blockIndex: candidate.BlockIndex, provider: providerIndex}, nil
}

func validateCandidateFields(candidate Candidate) error {
	for field, value := range map[string]string{
		"source_text":         candidate.SourceText,
		"learning_key":        candidate.LearningKey,
		"primary_translation": candidate.PrimaryTranslation,
		"literal_translation": candidate.LiteralTranslation,
		"meaning_note":        candidate.MeaningNote,
		"usage_note":          candidate.UsageNote,
		"parts_note":          candidate.PartsNote,
	} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%s must be valid UTF-8", field)
		}
	}
	if candidate.SourceText == "" {
		return errors.New("source_text must not be empty")
	}
	if candidate.Occurrence < 0 {
		return errors.New("occurrence must not be negative")
	}
	if !candidate.Kind.Valid() {
		return fmt.Errorf("invalid kind %q", candidate.Kind)
	}
	if _, err := NormalizeLearningKey(candidate.LearningKey); err != nil {
		return fmt.Errorf("learning_key: %w", err)
	}
	if strings.TrimSpace(candidate.PrimaryTranslation) == "" {
		return errors.New("primary_translation must not be empty")
	}
	if len(candidate.Alternatives) > 3 {
		return errors.New("alternatives must contain at most three strings")
	}
	seen := make(map[string]struct{}, len(candidate.Alternatives))
	for index, alternative := range candidate.Alternatives {
		alternative = strings.TrimSpace(alternative)
		if alternative == "" {
			return fmt.Errorf("alternatives[%d] must not be empty", index)
		}
		if _, exists := seen[alternative]; exists {
			return fmt.Errorf("alternatives[%d] is duplicated", index)
		}
		seen[alternative] = struct{}{}
	}
	for field, value := range map[string]string{
		"source_text":         candidate.SourceText,
		"primary_translation": candidate.PrimaryTranslation,
		"literal_translation": candidate.LiteralTranslation,
		"meaning_note":        candidate.MeaningNote,
		"usage_note":          candidate.UsageNote,
		"parts_note":          candidate.PartsNote,
	} {
		for _, r := range value {
			if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
				return fmt.Errorf("%s contains a control character", field)
			}
		}
	}
	return nil
}

func occurrenceByteOffset(value, needle string, occurrence int) (int, error) {
	start := 0
	for current := 0; current <= occurrence; current++ {
		relative := strings.Index(value[start:], needle)
		if relative < 0 {
			return 0, fmt.Errorf("source_text occurrence %d was not found", occurrence)
		}
		found := start + relative
		if current == occurrence {
			return found, nil
		}
		start = found + len(needle)
	}
	return 0, fmt.Errorf("source_text occurrence %d was not found", occurrence)
}

func spansOverlap(left, right Annotation) bool {
	return left.StartUTF16 < right.EndUTF16 && right.StartUTF16 < left.EndUTF16
}

func groupPriority(kind AnnotationKind) int {
	if kind == KindWord {
		return 1
	}
	return 0
}

func (k AnnotationKind) Valid() bool {
	return k == KindWord || k == KindPhrase || k == KindIdiom || k == KindExpression || k == KindProverb
}

func countArticleWords(article *Article) int {
	count := 0
	for _, block := range article.Blocks {
		count += countWords(block.SourceText)
	}
	if count == 0 {
		return 1
	}
	return count
}

func countWords(value string) int {
	count := 0
	inWord := false
	for _, r := range value {
		isWord := unicode.IsLetter(r) || unicode.IsNumber(r)
		if isWord && !inWord {
			count++
		}
		inWord = isWord
	}
	return count
}

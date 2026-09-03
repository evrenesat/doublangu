// Package reader contains the article-reader domain model, annotation
// normalization rules, and transactional persistence boundary.
package reader

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"doublangu/internal/library"
)

const (
	MaxArticleBodyBytes       = 100_000
	MaxEnrichmentBodyBytes    = 20_000
	MaxArticleTitleScalars    = 200
	MaxAnnotationsPer150Words = 16
	MaxShadowsPer150Words     = 8
	// AnalysisContractVersion mirrors semantics.AnalysisContractVersion for
	// reader callers that must not import the semantics package.
	AnalysisContractVersion = "reader.analysis.v3"
)

// SentenceRevision records where an article's source sentence rows came from.
const (
	// SentenceRevisionLegacyAnalysis marks article_sentence rows preserved
	// from pre-v3 provider output; they stay authoritative and are never
	// silently re-segmented.
	SentenceRevisionLegacyAnalysis = "legacy.analysis"
	// SentenceRevisionSourceSentencesV1 marks deterministic rows created by
	// the local segmenter at article creation or lazy preparation.
	SentenceRevisionSourceSentencesV1 = "source-sentence.v1"
)

// AnnotationKind is the vocabulary used by the reader and annotator.
type AnnotationKind string

const (
	KindWord       AnnotationKind = "word"
	KindPhrase     AnnotationKind = "phrase"
	KindIdiom      AnnotationKind = "idiom"
	KindExpression AnnotationKind = "expression"
	KindProverb    AnnotationKind = "proverb"
)

// EnrichmentStatus describes the provider lifecycle for an article.
type EnrichmentStatus string

const (
	StatusDraft      EnrichmentStatus = "draft"
	StatusProcessing EnrichmentStatus = "processing"
	StatusReady      EnrichmentStatus = "ready"
	StatusFailed     EnrichmentStatus = "failed"
)

// AnalysisStatus is the durable background lifecycle for semantic analysis.
type AnalysisStatus string

const (
	AnalysisNeedsAnalysis AnalysisStatus = "needs_analysis"
	AnalysisQueued        AnalysisStatus = "queued"
	AnalysisProcessing    AnalysisStatus = "processing"
	AnalysisReady         AnalysisStatus = "ready"
	AnalysisFailed        AnalysisStatus = "failed"
)

// NarrationStatus is independent from text analysis readiness.
type NarrationStatus string

const (
	NarrationNotRequested NarrationStatus = "not_requested"
	NarrationQueued       NarrationStatus = "queued"
	NarrationGenerating   NarrationStatus = "generating"
	NarrationPartial      NarrationStatus = "partial"
	NarrationReady        NarrationStatus = "ready"
	NarrationFailed       NarrationStatus = "failed"
	NarrationPurged       NarrationStatus = "purged"
)

// LearningStatus is the explicit learner override for an annotation.
type LearningStatus string

const (
	LearningStatusLearned   LearningStatus = "learned"
	LearningStatusUnlearned LearningStatus = "unlearned"
)

// Article is a pasted article and its ordered paragraph blocks.
type Article struct {
	ID                  library.ULID        `json:"id"`
	Title               string              `json:"title"`
	SourceLanguage      string              `json:"source_language"`
	TargetLanguage      string              `json:"target_language"`
	EnrichmentStatus    EnrichmentStatus    `json:"enrichment_status"`
	EnrichmentErrorCode string              `json:"enrichment_error_code"`
	CreatedAt           string              `json:"created_at"`
	UpdatedAt           string              `json:"updated_at"`
	Blocks              []ArticleBlock      `json:"blocks"`
	ContentHash         string              `json:"content_hash"`
	AnalysisStatus      AnalysisStatus      `json:"analysis_status"`
	AnalysisRevision    string              `json:"analysis_revision"`
	AnalysisErrorCode   string              `json:"analysis_error_code"`
	AnalysisModel       string              `json:"analysis_model"`
	AnalysisEffort      string              `json:"analysis_effort"`
	NarrationStatus     NarrationStatus     `json:"narration_status"`
	NarrationErrorCode  string              `json:"narration_error_code"`
	AnalysisProgress    AnalysisProgress    `json:"analysis_progress"`
	Sentences           []ArticleSentence   `json:"sentences"`
	Occurrences         []ArticleOccurrence `json:"occurrences"`
	Narration           NarrationSummary    `json:"narration"`
	// SentenceRevision is the source-sentence provenance marker. It is
	// internal state, not part of the owner-facing article payload.
	SentenceRevision string `json:"-"`
	// AnalysisJobID is the durable job that owns the current run. It is
	// internal state; lease tokens and job payloads are never exposed.
	AnalysisJobID string `json:"-"`
	// Pipeline is the immutable analysis profile provenance when the article
	// was created or last analyzed through the configurable pipeline.
	Pipeline *ArticlePipelineProvenance `json:"analysis_pipeline,omitempty"`
}

// ArticlePipelineProvenance is the owner-visible article pipeline identity.
type ArticlePipelineProvenance struct {
	ProfileID    string `json:"profile_id"`
	ProfileName  string `json:"profile_name"`
	SnapshotHash string `json:"snapshot_hash"`
}

// ArticleSummary is the compact item returned by the article list endpoint.
type ArticleSummary struct {
	ID                  library.ULID     `json:"id"`
	Title               string           `json:"title"`
	SourceLanguage      string           `json:"source_language"`
	TargetLanguage      string           `json:"target_language"`
	EnrichmentStatus    EnrichmentStatus `json:"enrichment_status"`
	EnrichmentErrorCode string           `json:"enrichment_error_code"`
	CreatedAt           string           `json:"created_at"`
	UpdatedAt           string           `json:"updated_at"`
	ContentHash         string           `json:"content_hash"`
	AnalysisStatus      AnalysisStatus   `json:"analysis_status"`
	AnalysisErrorCode   string           `json:"analysis_error_code"`
	AnalysisModel       string           `json:"analysis_model"`
	AnalysisEffort      string           `json:"analysis_effort"`
	NarrationStatus     NarrationStatus  `json:"narration_status"`
	NarrationErrorCode  string           `json:"narration_error_code"`
	AnalysisProgress    AnalysisProgress `json:"analysis_progress"`
}

// AnalysisProgress is the durable paragraph-level progress of the article's
// active analysis job. completed_paragraphs counts only paragraphs that were
// published by that job; stale materializations from older runs never count.
type AnalysisProgress struct {
	TotalParagraphs     int `json:"total_paragraphs"`
	CompletedParagraphs int `json:"completed_paragraphs"`
	CurrentBlockIndex   int `json:"current_block_index"`
	FailedBlockIndex    int `json:"failed_block_index"`
}

// BlockAnalysisStatus is the per-paragraph lifecycle under an analysis job.
type BlockAnalysisStatus string

const (
	BlockPending    BlockAnalysisStatus = "pending"
	BlockProcessing BlockAnalysisStatus = "processing"
	BlockReady      BlockAnalysisStatus = "ready"
	BlockFailed     BlockAnalysisStatus = "failed"
)

// ArticleBlock is one preserved paragraph of the pasted source.
type ArticleBlock struct {
	ID          library.ULID        `json:"id"`
	ArticleID   library.ULID        `json:"article_id"`
	BlockIndex  int                 `json:"block_index"`
	Kind        string              `json:"kind"`
	SourceText  string              `json:"source_text"`
	Annotations []Annotation        `json:"annotations"`
	Sentences   []ArticleSentence   `json:"sentences"`
	Occurrences []ArticleOccurrence `json:"occurrences"`
	// AnalysisStatus is the block's own lifecycle state. A block can remain
	// readable with older accepted semantics while a newer run is pending.
	AnalysisStatus    BlockAnalysisStatus `json:"analysis_status"`
	AnalysisErrorCode string              `json:"analysis_error_code"`
	HasAnalysis       bool                `json:"has_analysis"`
	AnalysisIsCurrent bool                `json:"analysis_is_current"`
	PublishedRevision string              `json:"published_analysis_revision,omitempty"`
	PublishedModel    string              `json:"published_analysis_model,omitempty"`
	PublishedEffort   string              `json:"published_analysis_effort,omitempty"`
	PublishedAt       string              `json:"published_at,omitempty"`
	analysisJobID     string
	publishedJobID    string
}

// ArticleSentence is a source-ordered sentence with an optional server audio
// reference. It remains useful when narration is unavailable.
type ArticleSentence struct {
	ID             library.ULID `json:"id"`
	ArticleBlockID library.ULID `json:"article_block_id"`
	SentenceIndex  int          `json:"sentence_index"`
	StartUTF16     int          `json:"start_utf16"`
	EndUTF16       int          `json:"end_utf16"`
	SourceText     string       `json:"source_text"`
	SourceHash     string       `json:"source_hash"`
	Audio          *AudioRef    `json:"audio"`
}

type OccurrenceRole string

const (
	OccurrenceToken                     OccurrenceRole = "token"
	OccurrenceContiguousConstruction    OccurrenceRole = "contiguous_construction"
	OccurrenceDiscontinuousConstruction OccurrenceRole = "discontinuous_construction"
)

type ShadowPolicy string

const (
	ShadowToken  ShadowPolicy = "token"
	ShadowGroup  ShadowPolicy = "group"
	ShadowMarker ShadowPolicy = "marker"
	ShadowNone   ShadowPolicy = "none"
)

type ArticleOccurrenceSpan struct {
	ID                  library.ULID `json:"id"`
	ArticleOccurrenceID library.ULID `json:"article_occurrence_id"`
	SpanIndex           int          `json:"span_index"`
	StartUTF16          int          `json:"start_utf16"`
	EndUTF16            int          `json:"end_utf16"`
	SourceText          string       `json:"source_text"`
}

type SemanticSense struct {
	ID                         library.ULID   `json:"id"`
	SemanticItemID             library.ULID   `json:"semantic_item_id"`
	Kind                       AnnotationKind `json:"kind"`
	CanonicalForm              string         `json:"canonical_form"`
	SenseDiscriminator         string         `json:"sense_discriminator"`
	PrimaryTranslation         string         `json:"primary_translation"`
	Alternatives               []string       `json:"alternatives"`
	LiteralTranslation         string         `json:"literal_translation"`
	MeaningNote                string         `json:"meaning_note"`
	UsageNote                  string         `json:"usage_note"`
	PartsNote                  string         `json:"parts_note"`
	CanonicalPronunciationText string         `json:"canonical_pronunciation_text"`
}

type SemanticLearningState struct {
	SemanticSenseID library.ULID   `json:"semantic_sense_id"`
	Status          LearningStatus `json:"status"`
	UpdatedAt       string         `json:"updated_at"`
}

// SubtitleSuppressionReason explains why an unlearned occurrence with an
// effective English subtitle does not display it.
type SubtitleSuppressionReason string

const (
	// SubtitleNone means the occurrence's effective subtitle is visible when
	// the occurrence is unlearned.
	SubtitleNone SubtitleSuppressionReason = "none"
	// SubtitleSpecialToken marks tokens that have no effective subtitle
	// (proper names, numbers, acronyms, and deliberately unchanged tokens
	// without a translation). Only special classifications produce it.
	SubtitleSpecialToken SubtitleSuppressionReason = "special_token"
	// SubtitleContiguousGroupMember marks exact lexical members of a
	// contiguous construction; the construction subtitle replaces theirs.
	SubtitleContiguousGroupMember SubtitleSuppressionReason = "contiguous_group_member"
)

type ArticleOccurrence struct {
	ID                         library.ULID              `json:"id"`
	ArticleBlockID             library.ULID              `json:"article_block_id"`
	ArticleSentenceID          *library.ULID             `json:"article_sentence_id"`
	SemanticSenseID            *library.ULID             `json:"semantic_sense_id"`
	Kind                       AnnotationKind            `json:"kind"`
	Role                       OccurrenceRole            `json:"role"`
	ShadowPolicy               ShadowPolicy              `json:"shadow_policy"`
	ShadowText                 string                    `json:"shadow_text"`
	SubtitleSuppressionReason  SubtitleSuppressionReason `json:"subtitle_suppression_reason"`
	CanonicalPronunciationText string                    `json:"canonical_pronunciation_text"`
	ContextPronunciationKey    string                    `json:"context_pronunciation_key"`
	ConfidenceMilli            int                       `json:"confidence_milli"`
	Sense                      *SemanticSense            `json:"sense"`
	LearningState              *SemanticLearningState    `json:"learning_state"`
	ShowShadow                 bool                      `json:"show_shadow"`
	MemberOccurrenceIDs        []string                  `json:"member_occurrence_ids"`
	Pronunciation              *AudioRef                 `json:"pronunciation"`
	Spans                      []ArticleOccurrenceSpan   `json:"spans"`
}

// AudioRef is a compact authenticated server URL, never inline audio bytes.
type AudioRef struct {
	RenderID   library.ULID `json:"render_id"`
	URL        string       `json:"url"`
	Ready      bool         `json:"ready"`
	DurationMS int64        `json:"duration_ms"`
	SizeBytes  int64        `json:"size_bytes"`
	ErrorCode  string       `json:"error_code"`
}

type NarrationSummary struct {
	Status           NarrationStatus `json:"status"`
	ErrorCode        string          `json:"error_code,omitempty"`
	SentenceCount    int             `json:"sentence_count"`
	ReadyCount       int             `json:"ready_count"`
	DurationMS       int64           `json:"duration_ms"`
	SizeBytes        int64           `json:"size_bytes"`
	ReclaimableBytes int64           `json:"reclaimable_bytes"`
}

// Annotation is a non-overlapping source span with English learning details.
// StartUTF16 and EndUTF16 are browser UTF-16 code-unit offsets.
type Annotation struct {
	ID                 library.ULID   `json:"id"`
	ArticleBlockID     library.ULID   `json:"article_block_id"`
	StartUTF16         int            `json:"start_utf16"`
	EndUTF16           int            `json:"end_utf16"`
	SourceText         string         `json:"source_text"`
	Kind               AnnotationKind `json:"kind"`
	LearningKey        string         `json:"learning_key"`
	PrimaryTranslation string         `json:"primary_translation"`
	Alternatives       []string       `json:"alternatives"`
	LiteralTranslation string         `json:"literal_translation"`
	MeaningNote        string         `json:"meaning_note"`
	UsageNote          string         `json:"usage_note"`
	PartsNote          string         `json:"parts_note"`
	SuggestShadow      bool           `json:"suggest_shadow"`
	LearningState      *LearningState `json:"learning_state"`
	ShowShadow         bool           `json:"show_shadow"`
}

// LearningState is keyed by source language, annotation kind, and normalized
// learning key so the same word can be learned independently from a phrase.
type LearningState struct {
	SourceLanguage string         `json:"source_language"`
	Kind           AnnotationKind `json:"kind"`
	LearningKey    string         `json:"learning_key"`
	Status         LearningStatus `json:"status"`
	UpdatedAt      string         `json:"updated_at"`
}

// ArticleInput is the provider-facing form of an article.
type ArticleInput struct {
	Title          string              `json:"title"`
	SourceLanguage string              `json:"source_language"`
	TargetLanguage string              `json:"target_language"`
	Blocks         []ArticleInputBlock `json:"blocks"`
}

// ArticleInputBlock preserves the source block index required in provider
// candidates.
type ArticleInputBlock struct {
	BlockIndex int    `json:"block_index"`
	SourceText string `json:"source_text"`
}

// AnalysisSelection is the owner-selected provider configuration snapshotted
// into every queued analysis job.
type AnalysisSelection struct {
	Model  string
	Effort string
}

// Candidate is the strict provider output before local span normalization.
type Candidate struct {
	BlockIndex         int            `json:"block_index"`
	SourceText         string         `json:"source_text"`
	Occurrence         int            `json:"occurrence"`
	Kind               AnnotationKind `json:"kind"`
	LearningKey        string         `json:"learning_key"`
	PrimaryTranslation string         `json:"primary_translation"`
	Alternatives       []string       `json:"alternatives"`
	LiteralTranslation string         `json:"literal_translation"`
	MeaningNote        string         `json:"meaning_note"`
	UsageNote          string         `json:"usage_note"`
	PartsNote          string         `json:"parts_note"`
	SuggestShadow      bool           `json:"suggest_shadow"`
}

// NormalizationDiagnostic is intentionally returned to tests and logs rather
// than exposed in the article API.
type NormalizationDiagnostic struct {
	InputCandidates    int  `json:"input_candidates"`
	ValidCandidates    int  `json:"valid_candidates"`
	AcceptedCandidates int  `json:"accepted_candidates"`
	RetainedCandidates int  `json:"retained_candidates"`
	DroppedCandidates  int  `json:"dropped_candidates"`
	OverlapsDropped    int  `json:"overlaps_dropped"`
	ShadowSuppressed   int  `json:"shadow_suppressed"`
	AnnotationBudget   int  `json:"annotation_budget"`
	ShadowBudget       int  `json:"shadow_budget"`
	BudgetExceeded     bool `json:"budget_exceeded"`
}

// NormalizationResult contains the persistence-ready annotations and the
// test-visible density accounting.
type NormalizationResult struct {
	Annotations []Annotation
	Diagnostic  NormalizationDiagnostic
}

// ErrorKind classifies reader store and input failures.
type ErrorKind int

const (
	KindNotFound ErrorKind = iota + 1
	KindValidation
	KindConflict
	KindInProgress
)

// Error is the reader boundary's typed error. HTTP callers can map it without
// exposing database or provider internals.
type Error struct {
	Op   string
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("reader %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("reader %s: %s", e.Op, e.Kind)
}

func (e *Error) Unwrap() error { return e.Err }

func (k ErrorKind) String() string {
	switch k {
	case KindNotFound:
		return "not found"
	case KindValidation:
		return "validation"
	case KindConflict:
		return "conflict"
	case KindInProgress:
		return "in progress"
	default:
		return "unknown"
	}
}

var (
	ErrArticleBodyTooLarge    = errors.New("article body exceeds 100000 UTF-8 bytes")
	ErrEnrichmentBodyTooLarge = errors.New("article exceeds 20000 UTF-8 bytes per enrichment request")
	ErrNoArticleBlocks        = errors.New("article must contain at least one non-empty paragraph")
)

// NewArticle validates and constructs an article while preserving its pasted
// paragraph text exactly apart from leading/trailing blank lines.
func NewArticle(title, body, sourceLanguage, targetLanguage string) (Article, error) {
	title = strings.TrimSpace(title)
	if !utf8.ValidString(title) || title == "" {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: errors.New("title is required")}
	}
	if !utf8.ValidString(body) {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: errors.New("article body must be valid UTF-8")}
	}
	if scalarCount(title) > MaxArticleTitleScalars {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: fmt.Errorf("title must be at most %d Unicode scalar values", MaxArticleTitleScalars)}
	}
	if len(body) > MaxArticleBodyBytes {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: ErrArticleBodyTooLarge}
	}
	blocks, err := ParseParagraphs(body)
	if err != nil {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	sourceLanguage, err = library.ParseBCP47(sourceLanguage)
	if err != nil {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: err}
	}
	targetLanguage, err = library.ParseBCP47(targetLanguage)
	if err != nil {
		return Article{}, &Error{Op: "create article", Kind: KindValidation, Err: err}
	}

	article := Article{
		ID:                  library.NewULID(),
		Title:               title,
		SourceLanguage:      sourceLanguage,
		TargetLanguage:      targetLanguage,
		EnrichmentStatus:    StatusDraft,
		EnrichmentErrorCode: "",
		AnalysisStatus:      AnalysisNeedsAnalysis,
		NarrationStatus:     NarrationNotRequested,
		Blocks:              make([]ArticleBlock, len(blocks)),
	}
	for index, text := range blocks {
		article.Blocks[index] = ArticleBlock{
			ID:          library.NewULID(),
			ArticleID:   article.ID,
			BlockIndex:  index,
			Kind:        "paragraph",
			SourceText:  text,
			Annotations: []Annotation{},
		}
	}
	return article, nil
}

// Validate checks a complete in-memory article representation.
func (a *Article) Validate() error {
	if a == nil {
		return errors.New("article is nil")
	}
	parsedID, err := library.ParseULID(a.ID.String())
	if err != nil || parsedID.IsZero() {
		return fmt.Errorf("invalid article id")
	}
	a.ID = parsedID
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" || scalarCount(a.Title) > MaxArticleTitleScalars {
		return fmt.Errorf("title must contain 1-%d Unicode scalar values", MaxArticleTitleScalars)
	}
	if !utf8.ValidString(a.Title) {
		return errors.New("title must be valid UTF-8")
	}
	if a.EnrichmentStatus != StatusDraft && a.EnrichmentStatus != StatusProcessing && a.EnrichmentStatus != StatusReady && a.EnrichmentStatus != StatusFailed {
		return fmt.Errorf("invalid enrichment status %q", a.EnrichmentStatus)
	}
	a.SourceLanguage, err = library.ParseBCP47(a.SourceLanguage)
	if err != nil {
		return err
	}
	a.TargetLanguage, err = library.ParseBCP47(a.TargetLanguage)
	if err != nil {
		return err
	}
	if len(a.Blocks) == 0 {
		return ErrNoArticleBlocks
	}
	totalBodyBytes := 0
	for index := range a.Blocks {
		block := &a.Blocks[index]
		if block.BlockIndex != index || block.Kind != "paragraph" || strings.TrimSpace(block.SourceText) == "" {
			return fmt.Errorf("invalid article block at index %d", index)
		}
		if parsedBlockID, err := library.ParseULID(block.ID.String()); err != nil || parsedBlockID.IsZero() {
			return fmt.Errorf("article block %d has an invalid id", index)
		} else {
			block.ID = parsedBlockID
		}
		if block.ArticleID != a.ID {
			return fmt.Errorf("article block %d has the wrong article id", index)
		}
		if !utf8.ValidString(block.SourceText) {
			return fmt.Errorf("article block %d has invalid UTF-8", index)
		}
		totalBodyBytes += len(block.SourceText)
		if totalBodyBytes > MaxArticleBodyBytes {
			return ErrArticleBodyTooLarge
		}
	}
	return nil
}

// AnnotatorInput returns the provider payload, enforcing the smaller request
// limit without changing the persisted source text.
func (a *Article) AnnotatorInput() (ArticleInput, error) {
	if err := a.Validate(); err != nil {
		return ArticleInput{}, &Error{Op: "build enrichment input", Kind: KindValidation, Err: err}
	}
	var total int
	input := ArticleInput{
		Title:          a.Title,
		SourceLanguage: a.SourceLanguage,
		TargetLanguage: a.TargetLanguage,
		Blocks:         make([]ArticleInputBlock, len(a.Blocks)),
	}
	for index, block := range a.Blocks {
		total += len(block.SourceText)
		if total > MaxEnrichmentBodyBytes {
			return ArticleInput{}, &Error{Op: "build enrichment input", Kind: KindValidation, Err: ErrEnrichmentBodyTooLarge}
		}
		input.Blocks[index] = ArticleInputBlock{BlockIndex: block.BlockIndex, SourceText: block.SourceText}
	}
	return input, nil
}

func scalarCount(value string) int { return utf8.RuneCountInString(value) }

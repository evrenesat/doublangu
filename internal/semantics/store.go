package semantics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

// Sense is the server-owned reusable semantic record.
type Sense struct {
	ID                         library.ULID
	SemanticItemID             library.ULID
	SourceLanguage             string
	TargetLanguage             string
	Kind                       Kind
	CanonicalForm              string
	NormalizedForm             string
	Lemma                      string
	PartOfSpeech               string
	SenseDiscriminator         string
	PrimaryTranslation         string
	Alternatives               []string
	LiteralTranslation         string
	MeaningNote                string
	UsageNote                  string
	PartsNote                  string
	CanonicalPronunciationText string
	ProviderID                 string
	ProviderModel              string
	AnalysisContractVersion    string
	CreatedAt                  string
	UpdatedAt                  string
}

type Store struct{ db *store.DB }

func NewStore(db *store.DB) *Store { return &Store{db: db} }

// LookupCandidates restricts the local lexicon to normalized forms and lemmas
// observed in the prepared article. It never returns the whole dictionary.
func (s *Store) LookupCandidates(ctx context.Context, input PreparedArticle) ([]SenseCandidate, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("semantics: nil database")
	}
	return s.lookup(ctx, s.db.Conn(), input)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) lookup(ctx context.Context, q queryer, input PreparedArticle) ([]SenseCandidate, error) {
	seen := make(map[string]struct{})
	result := make([]SenseCandidate, 0)
	for _, token := range input.Tokens {
		rows, err := q.QueryContext(ctx, `
			SELECT s.id, i.id, i.source_language, s.target_language, i.kind,
			       i.canonical_form, i.normalized_form, i.lemma, i.part_of_speech,
			       s.primary_translation, s.sense_discriminator
			FROM semantic_item i JOIN semantic_sense s ON s.semantic_item_id = i.id
			WHERE i.source_language = ? AND s.target_language = ? AND s.retired_at = ''
			  AND (i.normalized_form = ? OR (i.lemma <> '' AND i.lemma = ?))
			ORDER BY s.id
		`, input.SourceLanguage, input.TargetLanguage, token.NormalizedForm, token.NormalizedForm)
		if err != nil {
			return nil, fmt.Errorf("semantics lookup %q: %w", token.NormalizedForm, err)
		}
		for rows.Next() {
			var candidate SenseCandidate
			if err := rows.Scan(&candidate.ID, &candidate.SemanticItemID, &candidate.SourceLanguage, &candidate.TargetLanguage, &candidate.Kind, &candidate.CanonicalForm, &candidate.NormalizedForm, &candidate.Lemma, &candidate.PartOfSpeech, &candidate.PrimaryTranslation, &candidate.SenseDiscriminator); err != nil {
				rows.Close()
				return nil, fmt.Errorf("semantics scan candidate: %w", err)
			}
			if _, ok := seen[candidate.ID]; !ok {
				seen[candidate.ID] = struct{}{}
				result = append(result, candidate)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("semantics candidates: %w", err)
		}
		rows.Close()
	}
	return result, nil
}

// GetSense loads a reusable active or retired sense by ID.
func (s *Store) GetSense(ctx context.Context, id library.ULID) (*Sense, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("semantics: nil database")
	}
	return getSense(ctx, s.db.Conn(), id.String())
}

func getSense(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*Sense, error) {
	var sense Sense
	var kind, alternatives string
	err := q.QueryRowContext(ctx, `
		SELECT s.id, s.semantic_item_id, i.source_language, s.target_language,
		       i.kind, i.canonical_form, i.normalized_form, i.lemma, i.part_of_speech,
		       s.sense_discriminator, s.primary_translation, s.alternatives_json,
		       s.literal_translation, s.meaning_note, s.usage_note, s.parts_note,
		       s.canonical_pronunciation_text, s.provider_id, s.provider_model,
		       s.analysis_contract_version, s.created_at, s.updated_at
		FROM semantic_sense s JOIN semantic_item i ON i.id = s.semantic_item_id
		WHERE s.id = ?
	`, id).Scan(&sense.ID, &sense.SemanticItemID, &sense.SourceLanguage, &sense.TargetLanguage,
		&kind, &sense.CanonicalForm, &sense.NormalizedForm, &sense.Lemma, &sense.PartOfSpeech,
		&sense.SenseDiscriminator, &sense.PrimaryTranslation, &alternatives,
		&sense.LiteralTranslation, &sense.MeaningNote, &sense.UsageNote, &sense.PartsNote,
		&sense.CanonicalPronunciationText, &sense.ProviderID, &sense.ProviderModel,
		&sense.AnalysisContractVersion, &sense.CreatedAt, &sense.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sense.Kind = Kind(kind)
	if err := json.Unmarshal([]byte(alternatives), &sense.Alternatives); err != nil {
		return nil, fmt.Errorf("decode sense alternatives: %w", err)
	}
	if sense.Alternatives == nil {
		sense.Alternatives = []string{}
	}
	return &sense, nil
}

// EnsureSenseTx returns an existing active sense or creates the semantic item
// and sense in the caller's transaction. Sense identity is discriminator-based
// so same-spelling contextual homonyms remain independent.
func EnsureSenseTx(ctx context.Context, tx *sql.Tx, sourceLanguage, targetLanguage string, proposal NewSense, providerID, providerModel string) (*Sense, error) {
	if !proposal.Kind.Valid() || strings.TrimSpace(proposal.CanonicalForm) == "" || strings.TrimSpace(proposal.PrimaryTranslation) == "" || strings.TrimSpace(proposal.SenseDiscriminator) == "" {
		return nil, errors.New("invalid sense proposal")
	}
	normalized, err := NormalizeForm(proposal.CanonicalForm)
	if err != nil {
		return nil, fmt.Errorf("sense canonical form: %w", err)
	}
	if proposal.NormalizedForm != "" {
		suppliedNormalized, normalizeErr := NormalizeForm(proposal.NormalizedForm)
		if normalizeErr != nil {
			return nil, fmt.Errorf("sense normalized form: %w", normalizeErr)
		}
		if suppliedNormalized != normalized {
			return nil, errors.New("sense normalized form does not match canonical form")
		}
		normalized = suppliedNormalized
	}
	var itemID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM semantic_item WHERE source_language = ? AND kind = ? AND normalized_form = ? LIMIT 1`, sourceLanguage, proposal.Kind, normalized).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		itemID = library.NewULID().String()
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_item (id, source_language, kind, canonical_form, normalized_form, lemma, part_of_speech) VALUES (?, ?, ?, ?, ?, ?, ?)`, itemID, sourceLanguage, proposal.Kind, proposal.CanonicalForm, normalized, proposal.Lemma, proposal.PartOfSpeech); err != nil {
			return nil, fmt.Errorf("insert semantic item: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find semantic item: %w", err)
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM semantic_sense WHERE semantic_item_id = ? AND target_language = ? AND sense_discriminator = ? AND retired_at = ''`, itemID, targetLanguage, proposal.SenseDiscriminator).Scan(&existingID)
	if err == nil {
		return getSense(ctx, tx, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find semantic sense: %w", err)
	}
	if proposal.SenseDiscriminator == "" {
		return nil, errors.New("sense discriminator is required")
	}
	alternatives, err := json.Marshal(proposal.Alternatives)
	if err != nil {
		return nil, err
	}
	now := store.NowUTC()
	senseID := library.NewULID().String()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO semantic_sense (
			id, semantic_item_id, target_language, sense_discriminator,
			primary_translation, alternatives_json, literal_translation, meaning_note,
			usage_note, parts_note, canonical_pronunciation_text, provider_id,
			provider_model, analysis_contract_version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, senseID, itemID, targetLanguage, proposal.SenseDiscriminator, proposal.PrimaryTranslation,
		string(alternatives), proposal.LiteralTranslation, proposal.MeaningNote, proposal.UsageNote,
		proposal.PartsNote, proposal.CanonicalPronunciationText, providerID, providerModel,
		AnalysisContractVersion, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert semantic sense: %w", err)
	}
	return getSense(ctx, tx, senseID)
}

// EnsureExistingSenseTx verifies that an existing candidate is valid for the
// article's language and returns its persisted details.
func EnsureExistingSenseTx(ctx context.Context, tx *sql.Tx, senseID string, sourceLanguage, targetLanguage string, kind Kind) (*Sense, error) {
	if senseID == "" {
		return nil, errors.New("semantic sense id is required")
	}
	sense, err := getSense(ctx, tx, senseID)
	if err != nil {
		return nil, err
	}
	if sense.SourceLanguage != sourceLanguage || sense.TargetLanguage != targetLanguage || sense.Kind != kind {
		return nil, errors.New("semantic sense does not match article language or kind")
	}
	return sense, nil
}

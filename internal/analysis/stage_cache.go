package analysis

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
)

// StageCacheSpec is the exact cache identity for one stage artifact.
type StageCacheSpec struct {
	StageID         pipeline.StageID
	InputHash       string
	UpstreamHash    string
	ContractVersion string
	PromptVersion   string
	ProviderID      string
	ModelID         string
	OptionsHash     string
}

// inputIdentity carries the deterministic paragraph-level inputs that define
// stage input hashes. Prior senses are stripped to their source-side fields.
type inputIdentity struct {
	SourceLanguage string
	TargetLanguage string
	ContentHash    string
	BlockIndex     int
	BlockHash      string
	Sentences      []sentenceIdentity
	Tokens         []tokenIdentity
	Candidates     []semantics.SenseCandidate
	Prior          []priorSenseIdentity
}

type sentenceIdentity struct {
	Index int
	Start int
	End   int
	Text  string
}

type tokenIdentity struct {
	ID         string
	TokenIndex int
	Start      int
	End        int
	Source     string
	Normalized string
	Lemma      string
}

type priorSenseIdentity struct {
	Ref                        string
	Kind                       string
	CanonicalForm              string
	NormalizedForm             string
	Lemma                      string
	PartOfSpeech               string
	SenseDiscriminator         string
	CanonicalPronunciationText string
}

func canonicalHash(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ChunkInputHash computes the exact stage input identity for one prepared
// paragraph: source/context/tokens/candidates and prior senses stripped to
// linguistic fields. The same hash defines both stage inputs; the translation
// stage additionally carries the linguistic artifact hash as its upstream.
func ChunkInputHash(chunk semantics.PreparedChunk) (string, error) {
	identity := inputIdentity{
		SourceLanguage: chunk.SourceLanguage,
		TargetLanguage: chunk.TargetLanguage,
		ContentHash:    chunk.ContentHash,
		BlockIndex:     chunk.Block.BlockIndex,
		BlockHash:      semantics.BlockHash(chunk.Block),
	}
	for _, sentence := range chunk.Sentences {
		identity.Sentences = append(identity.Sentences, sentenceIdentity{
			Index: sentence.Index, Start: sentence.Span.StartUTF16,
			End: sentence.Span.EndUTF16, Text: sentence.Span.SourceText,
		})
	}
	for _, token := range chunk.Tokens {
		identity.Tokens = append(identity.Tokens, tokenIdentity{
			ID: token.ID, TokenIndex: token.TokenIndex, Start: token.StartUTF16,
			End: token.EndUTF16, Source: token.SourceText,
			Normalized: token.NormalizedForm, Lemma: token.Lemma,
		})
	}
	identity.Candidates = append([]semantics.SenseCandidate(nil), chunk.Candidates...)
	for _, sense := range chunk.PriorValidatedSenses {
		identity.Prior = append(identity.Prior, priorSenseIdentity{
			Ref: sense.Ref, Kind: string(sense.Kind), CanonicalForm: sense.CanonicalForm,
			NormalizedForm: sense.NormalizedForm, Lemma: sense.Lemma,
			PartOfSpeech: sense.PartOfSpeech, SenseDiscriminator: sense.SenseDiscriminator,
			CanonicalPronunciationText: sense.CanonicalPronunciationText,
		})
	}
	return canonicalHash(identity)
}

// ArtifactHashOf returns the deterministic artifact hash for cache validation.
func ArtifactHashOf(payload any) (string, error) {
	return canonicalHash(payload)
}

// SaveStageCache stores one locally validated stage artifact. A fresh run
// performs no cache read but may still store nothing here (bypassed).
func (s *HistoryStore) SaveStageCache(ctx context.Context, spec StageCacheSpec, artifactJSON string, artifactHash string, sourceRunID string) error {
	if s == nil || s.db == nil {
		return errors.New("analysis history: nil database")
	}
	if spec.StageID == "" || spec.InputHash == "" || spec.ContractVersion == "" ||
		spec.PromptVersion == "" || spec.ProviderID == "" || spec.ModelID == "" || spec.OptionsHash == "" {
		return errors.New("analysis history: invalid stage cache spec")
	}
	if artifactJSON == "" || artifactHash == "" {
		return errors.New("analysis history: invalid cached artifact")
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_stage_cache (
			id, stage_id, input_hash, upstream_artifact_hash, contract_version,
			prompt_version, provider_id, provider_type, provider_config_fingerprint,
			model_id, options_hash, validated_artifact_json, artifact_hash, source_run_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?)
		ON CONFLICT(stage_id, input_hash, upstream_artifact_hash, contract_version,
			prompt_version, provider_id, model_id, options_hash)
		DO UPDATE SET validated_artifact_json = excluded.validated_artifact_json,
			artifact_hash = excluded.artifact_hash, source_run_id = excluded.source_run_id,
			created_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, library.NewULID().String(), spec.StageID, spec.InputHash, spec.UpstreamHash,
		spec.ContractVersion, spec.PromptVersion, spec.ProviderID, spec.ModelID,
		spec.OptionsHash, artifactJSON, artifactHash, sourceRunID)
	if err != nil {
		return fmt.Errorf("save stage cache: %w", err)
	}
	return nil
}

// StageCacheHit is one exact cache row whose artifact hash matched.
type StageCacheHit struct {
	ArtifactJSON string
	ArtifactHash string
	SourceRunID  string
	CacheID      string
}

// ReadStageCache returns an exact identity row. The caller must revalidate
// the artifact; a validation failure is treated as a miss by the runner.
func (s *HistoryStore) ReadStageCache(ctx context.Context, spec StageCacheSpec) (*StageCacheHit, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("analysis history: nil database")
	}
	var hit StageCacheHit
	err := s.db.QueryRow(ctx, `
		SELECT id, validated_artifact_json, artifact_hash, source_run_id
		FROM analysis_stage_cache
		WHERE stage_id = ? AND input_hash = ? AND upstream_artifact_hash = ?
		  AND contract_version = ? AND prompt_version = ?
		  AND provider_id = ? AND model_id = ? AND options_hash = ?
	`, spec.StageID, spec.InputHash, spec.UpstreamHash, spec.ContractVersion,
		spec.PromptVersion, spec.ProviderID, spec.ModelID, spec.OptionsHash).
		Scan(&hit.CacheID, &hit.ArtifactJSON, &hit.ArtifactHash, &hit.SourceRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &hit, nil
}

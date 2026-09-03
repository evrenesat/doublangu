package analysis

import (
	"context"
	"testing"

	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// TestTranslationHashChangesWithPriorEnglish proves the stage-split input
// identity: changing only a prior sense's English translation fields must
// invalidate the translation hash while leaving the linguistic hash
// untouched, and the two stage hashes must differ for the same chunk.
func TestTranslationHashChangesWithPriorEnglish(t *testing.T) {
	prior := semantics.NewSense{
		Ref: "s1", Kind: semantics.KindWord, CanonicalForm: "huis",
		NormalizedForm: "huis", Lemma: "huis", PartOfSpeech: "noun",
		SenseDiscriminator: "building", PrimaryTranslation: "house",
		Alternatives:               []string{"home"},
		LiteralTranslation:         "house",
		MeaningNote:                "a dwelling",
		CanonicalPronunciationText: "hœys",
	}
	chunk := semantics.PreparedChunk{
		SourceLanguage: "nl", TargetLanguage: "en", ContentHash: "content",
		Block:                semantics.Block{BlockIndex: 1, SourceText: "Het huis."},
		PriorValidatedSenses: []semantics.NewSense{prior},
	}
	regenerated := chunk
	regenerated.PriorValidatedSenses = []semantics.NewSense{func() semantics.NewSense {
		changed := prior
		changed.PrimaryTranslation = "residence"
		changed.Alternatives = []string{"dwelling", "abode"}
		return changed
	}()}

	linguistic, err := ChunkInputHash(chunk)
	if err != nil {
		t.Fatalf("linguistic hash: %v", err)
	}
	linguisticAgain, err := ChunkInputHash(regenerated)
	if err != nil {
		t.Fatalf("linguistic hash after English change: %v", err)
	}
	if linguistic != linguisticAgain {
		t.Fatalf("linguistic hash changed on English-only edit: %s vs %s", linguistic, linguisticAgain)
	}
	translation, err := TranslationChunkInputHash(chunk)
	if err != nil {
		t.Fatalf("translation hash: %v", err)
	}
	translationAgain, err := TranslationChunkInputHash(regenerated)
	if err != nil {
		t.Fatalf("translation hash after English change: %v", err)
	}
	if translation == translationAgain {
		t.Fatalf("translation hash unchanged on English-only edit: %s", translation)
	}
	if linguistic == translation {
		t.Fatalf("stage hashes collide without domain separation: %s", linguistic)
	}
}

// TestStageCacheMissesAfterProviderReconfiguration proves the provider
// connection identity is part of the cache key: artifacts stored under one
// configuration fingerprint are invisible to a run whose snapshot carries a
// different fingerprint, for both stages, even when provider ID, model, and
// options are unchanged.
func TestStageCacheMissesAfterProviderReconfiguration(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	history := NewHistoryStore(db)

	base := StageCacheSpec{
		InputHash: "input-hash", UpstreamHash: "", ContractVersion: "c1", PromptVersion: "p1",
		ProviderID: "mac-omlx", ProviderType: "openai_compatible",
		ConfigFingerprint: "fingerprint-a", ModelID: "m", OptionsHash: "o",
	}
	specs := map[pipeline.StageID]StageCacheSpec{
		pipeline.StageLinguisticAnalysis: base,
		pipeline.StageTranslation: func() StageCacheSpec {
			s := base
			s.StageID = pipeline.StageTranslation
			s.UpstreamHash = "upstream-hash"
			return s
		}(),
	}
	base.StageID = pipeline.StageLinguisticAnalysis
	specs[pipeline.StageLinguisticAnalysis] = base

	for stage, spec := range specs {
		if err := history.SaveStageCache(ctx, spec, `{"ok":true}`, "artifact-hash", "run-a"); err != nil {
			t.Fatalf("save %s: %v", stage, err)
		}
	}

	// Same identity reads hit.
	for stage, spec := range specs {
		hit, err := history.ReadStageCache(ctx, spec)
		if err != nil || hit == nil {
			t.Fatalf("read %s with stored fingerprint: hit=%v err=%v, want hit", stage, hit, err)
		}
	}

	// Only the fingerprint changes (same ID/type/model/options): both miss.
	for stage, spec := range specs {
		changed := spec
		changed.ConfigFingerprint = "fingerprint-b"
		hit, err := history.ReadStageCache(ctx, changed)
		if err != nil {
			t.Fatalf("read %s with new fingerprint: %v", stage, err)
		}
		if hit != nil {
			t.Fatalf("read %s with new fingerprint: got hit %+v, want miss", stage, hit)
		}
	}

	// Only the provider type changes: both miss.
	for stage, spec := range specs {
		changed := spec
		changed.ProviderType = "codex_app_server"
		hit, err := history.ReadStageCache(ctx, changed)
		if err != nil {
			t.Fatalf("read %s with new type: %v", stage, err)
		}
		if hit != nil {
			t.Fatalf("read %s with new type: got hit %+v, want miss", stage, hit)
		}
	}
}

// TestStageCacheIgnoresLegacyRowsWithoutFingerprint proves rows written
// before the provider-identity migration (empty type/fingerprint) never
// match lookups built from resolved bindings, which always carry both.
func TestStageCacheIgnoresLegacyRowsWithoutFingerprint(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO analysis_stage_cache (
			id, stage_id, input_hash, upstream_artifact_hash, contract_version,
			prompt_version, provider_id, provider_type, provider_config_fingerprint,
			model_id, options_hash, validated_artifact_json, artifact_hash, source_run_id
		) VALUES ('legacy-row', 'linguistic_analysis', 'input-hash', '', 'c1',
			'p1', 'mac-omlx', '', '', 'm', 'o', '{"ok":true}', 'artifact-hash', 'run-old')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	history := NewHistoryStore(db)
	hit, err := history.ReadStageCache(ctx, StageCacheSpec{
		StageID: pipeline.StageLinguisticAnalysis, InputHash: "input-hash",
		ContractVersion: "c1", PromptVersion: "p1", ProviderID: "mac-omlx",
		ProviderType: "openai_compatible", ConfigFingerprint: "fingerprint-a",
		ModelID: "m", OptionsHash: "o",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if hit != nil {
		t.Fatalf("legacy row matched: %+v, want miss", hit)
	}
}

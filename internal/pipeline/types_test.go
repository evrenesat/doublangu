package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleBinding(stage StageID) BindingSnapshot {
	options := json.RawMessage(`{"reasoning_effort":"medium"}`)
	hash, _ := OptionsHashOf(options)
	contract, prompt, _ := StageContracts(stage)
	return BindingSnapshot{
		StageID: stage, ProviderID: "codex-app-server", ProviderType: "codex_app_server",
		ProviderConfigFingerprint: "fingerprint", ModelID: "model-a", Options: options,
		OptionsHash: hash, ContractVersion: contract, PromptVersion: prompt,
	}
}

func validProfile() ProfileSnapshot {
	return ProfileSnapshot{
		ID: "profile-id", Name: "Codex + Mac OMLX",
		Bindings: []BindingSnapshot{sampleBinding(StageLinguisticAnalysis), sampleBinding(StageTranslation)},
	}
}

func TestRegisteredStagesOrderAndContracts(t *testing.T) {
	stages := RegisteredStages()
	if len(stages) != 2 || stages[0] != StageLinguisticAnalysis || stages[1] != StageTranslation {
		t.Fatalf("registered stages = %v", stages)
	}
	if contract, prompt, ok := StageContracts(StageLinguisticAnalysis); !ok || contract != LinguisticContractVersion || prompt != LinguisticPromptVersion {
		t.Fatalf("linguistic identity = %q/%q/%v", contract, prompt, ok)
	}
	if contract, prompt, ok := StageContracts(StageTranslation); !ok || contract != TranslationContractVersion || prompt != TranslationPromptVersion {
		t.Fatalf("translation identity = %q/%q/%v", contract, prompt, ok)
	}
	if _, _, ok := StageContracts(StageID("unknown")); ok {
		t.Fatal("unknown stage unexpectedly registered")
	}
	if StageID("unknown").Valid() || !StageLinguisticAnalysis.Valid() || !StageTranslation.Valid() {
		t.Fatal("stage validity check is wrong")
	}
}

func TestProfileSnapshotValidation(t *testing.T) {
	profile := validProfile()
	if err := profile.Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}

	missingTranslation := profile
	missingTranslation.Bindings = profile.Bindings[:1]
	if err := missingTranslation.Validate(); err == nil {
		t.Fatal("profile without translation accepted")
	}

	duplicate := profile
	duplicate.Bindings = []BindingSnapshot{sampleBinding(StageTranslation), sampleBinding(StageLinguisticAnalysis), sampleBinding(StageLinguisticAnalysis)}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("profile with duplicate stage accepted")
	}

	outOfOrder := profile
	outOfOrder.Bindings = []BindingSnapshot{sampleBinding(StageTranslation), sampleBinding(StageLinguisticAnalysis)}
	if err := outOfOrder.Validate(); err == nil {
		t.Fatal("out-of-order profile accepted")
	}

	wrongVersion := profile
	wrongVersion.Bindings[1].ContractVersion = TranslationContractVersion
	wrongVersion.Bindings[1].PromptVersion = "reader-translation-prompt.v2"
	if err := wrongVersion.Validate(); err == nil {
		t.Fatal("profile with wrong prompt version accepted")
	}

	blank := profile
	blank.Bindings[0].ModelID = ""
	if err := blank.Validate(); err == nil {
		t.Fatal("profile with blank model accepted")
	}

	badName := profile
	badName.Name = strings.Repeat("x", 81)
	if err := badName.Validate(); err == nil {
		t.Fatal("overlong profile name accepted")
	}
	if err := ValidateProfileName(" \t "); err == nil {
		t.Fatal("blank profile name accepted")
	}
	if err := ValidateProfileName("ok name"); err != nil {
		t.Fatalf("valid profile name rejected: %v", err)
	}
}

func TestSortBindingsOrdersAndRejectsDuplicates(t *testing.T) {
	unordered := []BindingSnapshot{sampleBinding(StageTranslation), sampleBinding(StageLinguisticAnalysis)}
	sorted, err := SortBindings(unordered)
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0].StageID != StageLinguisticAnalysis || sorted[1].StageID != StageTranslation {
		t.Fatalf("sorted bindings = %v", sorted)
	}
	if _, err := SortBindings([]BindingSnapshot{sampleBinding(StageTranslation), sampleBinding(StageTranslation)}); err == nil {
		t.Fatal("duplicate stages accepted by SortBindings")
	}
}

func cloneProfile(profile ProfileSnapshot) ProfileSnapshot {
	clone := profile
	clone.Bindings = append([]BindingSnapshot(nil), profile.Bindings...)
	for index := range clone.Bindings {
		binding := clone.Bindings[index]
		binding.Options = append(json.RawMessage(nil), binding.Options...)
		clone.Bindings[index] = binding
	}
	return clone
}

func TestSnapshotHashIsDeterministicAndSensitiveToContent(t *testing.T) {
	profile := validProfile()
	first, err := profile.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	second, err := profile.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("snapshot hash not deterministic: %q", first)
	}

	changedModel := cloneProfile(profile)
	changedModel.Bindings[1].ModelID = "model-b"
	other, err := changedModel.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("changed model produced the same snapshot hash")
	}

	renamed := cloneProfile(profile)
	renamed.Name = "Another name"
	renamedHash, err := renamed.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	if renamedHash == first {
		t.Fatal("renamed profile produced the same snapshot hash")
	}

	// Whitespace in options JSON must not change the hash (canonicalization).
	whitespaceOptions := cloneProfile(profile)
	whitespaceOptions.Bindings[0].Options = json.RawMessage("{\n  \"reasoning_effort\": \"medium\"\n}")
	whitespaceHash, err := whitespaceOptions.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	if whitespaceHash != first {
		t.Fatal("whitespace-only options change produced a different snapshot hash")
	}
}

func TestOptionsHashIsDomainSeparated(t *testing.T) {
	options := json.RawMessage(`{"reasoning_effort":"medium"}`)
	hash, err := OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || len(hash) != 64 {
		t.Fatalf("options hash = %q", hash)
	}
	if _, err := OptionsHashOf(json.RawMessage(`not-json`)); err == nil {
		t.Fatal("invalid options JSON accepted")
	}
	if _, err := OptionsHashOf(json.RawMessage(``)); err == nil {
		t.Fatal("empty options accepted")
	}
}

func TestJobPayloadStrictDecodeAndVerification(t *testing.T) {
	profile := validProfile()
	snapshotHash, err := profile.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	payload := JobPayload{
		ArticleID: "article-1", ContentHash: "content",
		AnalysisContractVersion: AnalysisContractVersion,
		PipelineVersion:         PipelineVersion, Fresh: false,
		Profile: profile, ProfileSnapshotHash: snapshotHash,
	}
	encoded, err := EncodeJobPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeJobPayload(encoded)
	if err != nil {
		t.Fatalf("round trip decode: %v", err)
	}
	if decoded.ArticleID != payload.ArticleID || len(decoded.Profile.Bindings) != 2 {
		t.Fatalf("decoded = %+v", decoded)
	}
	// Unknown top-level fields are rejected.
	if _, err := DecodeJobPayload(append(encoded, []byte(`,"secret":"x"`)...)); err == nil {
		t.Fatal("unknown payload field accepted")
	}
	// A tampered profile hash is rejected.
	tampered := payload
	tampered.ProfileSnapshotHash = strings.Repeat("0", 64)
	if err := tampered.Validate(); err == nil {
		t.Fatal("mismatched snapshot hash accepted")
	}
	wrongContract := payload
	wrongContract.AnalysisContractVersion = "reader.analysis.v2"
	if err := wrongContract.Validate(); err == nil {
		t.Fatal("wrong analysis contract accepted")
	}
	wrongPipeline := payload
	wrongPipeline.PipelineVersion = "reader-analysis-pipeline.v2"
	if err := wrongPipeline.Validate(); err == nil {
		t.Fatal("wrong pipeline version accepted")
	}
}

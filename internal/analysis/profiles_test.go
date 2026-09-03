package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"doublangu/internal/config"
	"doublangu/internal/pipeline"
	"doublangu/internal/store"
)

func profileBindingForTest(t *testing.T, stage pipeline.StageID) pipeline.BindingSnapshot {
	t.Helper()
	options, err := config.CanonicalizeProviderOptions(config.ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	contract, prompt, _ := pipeline.StageContracts(stage)
	return pipeline.BindingSnapshot{
		StageID: stage, ProviderID: "codex-app-server", ProviderType: "codex_app_server",
		ProviderConfigFingerprint: "fp", ModelID: "model-a", Options: options,
		OptionsHash: hash, ContractVersion: contract, PromptVersion: prompt,
	}
}

func sampleBindingsForTest(t *testing.T) []pipeline.BindingSnapshot {
	t.Helper()
	return []pipeline.BindingSnapshot{
		profileBindingForTest(t, pipeline.StageLinguisticAnalysis),
		profileBindingForTest(t, pipeline.StageTranslation),
	}
}

func TestProfileStoreCRUDAndActivation(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	profiles := NewProfileStore(db)

	created, err := profiles.Create(ctx, "Codex Only", sampleBindingsForTest(t))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Name != "Codex Only" || len(created.Bindings) != 2 {
		t.Fatalf("created = %+v", created)
	}
	if _, err := profiles.Create(ctx, "Codex Only", sampleBindingsForTest(t)); err == nil {
		t.Fatal("duplicate name accepted")
	}
	renamed, err := profiles.Rename(ctx, created.ID, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Renamed" {
		t.Fatalf("renamed = %+v", renamed)
	}
	if err := profiles.Activate(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	activeID, err := profiles.ActiveProfile(ctx)
	if err != nil || activeID != created.ID {
		t.Fatalf("active = %q err=%v", activeID, err)
	}
	loaded, err := profiles.Get(ctx, created.ID)
	if err != nil || !loaded.IsActive || len(loaded.Bindings) != 2 {
		t.Fatalf("loaded = %+v err=%v", loaded, err)
	}
	// Replacing the full object keeps the active id and updates bindings.
	other := sampleBindingsForTest(t)
	other[1].ModelID = "model-b"
	replaced, err := profiles.Replace(ctx, created.ID, "Codex + More", other)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Bindings[1].ModelID != "model-b" || replaced.Name != "Codex + More" {
		t.Fatalf("replaced = %+v", replaced)
	}
	if err := profiles.Delete(ctx, created.ID); err == nil {
		t.Fatal("active profile deletion accepted")
	}
	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
}

func TestProfileStoreValidationAndSeed(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	profiles := NewProfileStore(db)

	bad := sampleBindingsForTest(t)
	bad = bad[:1]
	if _, err := profiles.Create(ctx, "Partial", bad); err == nil {
		t.Fatal("partial profile accepted")
	}
	unknown := sampleBindingsForTest(t)
	unknown[0].StageID = pipeline.StageID("bogus")
	if _, err := profiles.Create(ctx, "Bogus", unknown); err == nil {
		t.Fatal("unknown stage accepted")
	}
	wrongType := sampleBindingsForTest(t)
	wrongType[0].ProviderType = "openai_compatible"
	if _, err := profiles.Create(ctx, "WrongType", wrongType); err == nil {
		t.Fatal("wrong provider type accepted")
	}

	seeded, err := profiles.Seed(ctx, []SeedProfile{
		{Name: "Imported Codex", Bindings: sampleBindingsForTest(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 1 || seeded[0].Name != "Imported Codex" {
		t.Fatalf("seeded = %+v", seeded)
	}
	active, err := profiles.ActiveProfile(ctx)
	if err != nil || active != seeded[0].ID {
		t.Fatalf("seed activation = %q err=%v", active, err)
	}
	// Seeding again is a no-op once profiles exist.
	again, err := profiles.Seed(ctx, []SeedProfile{{Name: "Second", Bindings: sampleBindingsForTest(t)}})
	if err != nil || len(again) != 0 {
		t.Fatalf("second seed = %+v err=%v", again, err)
	}
}

func TestCanonicalizeBindings(t *testing.T) {
	bindings := []pipeline.BindingSnapshot{
		{
			StageID: pipeline.StageTranslation, ProviderID: "codex-app-server",
			ModelID: "m", Options: json.RawMessage(`{"reasoning_effort":"high"}`),
		},
		{
			StageID: pipeline.StageLinguisticAnalysis, ProviderID: "codex-app-server",
			ModelID: "m", Options: json.RawMessage(`{ "reasoning_effort" : "high" }`),
		},
	}
	canonical, err := CanonicalizeBindings(map[string]string{"codex-app-server": "codex_app_server"}, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 2 || canonical[0].StageID != pipeline.StageLinguisticAnalysis {
		t.Fatalf("canonical = %+v", canonical)
	}
	for _, binding := range canonical {
		if binding.ProviderType != "codex_app_server" || binding.OptionsHash == "" || binding.ContractVersion == "" || binding.PromptVersion == "" {
			t.Fatalf("binding not canonicalized: %+v", binding)
		}
		if strings.Contains(string(binding.Options), " ") {
			t.Fatalf("options not compacted: %s", binding.Options)
		}
	}
	if _, err := CanonicalizeBindings(map[string]string{}, canonical); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

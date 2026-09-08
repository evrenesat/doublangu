package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"doublangu/internal/annotator"
	"doublangu/internal/config"
	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func mustCanonicalOptions(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	options, err := config.CanonicalizeProviderOptions(config.ProviderTypeCodexAppServer, json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func mustOptionsHash(t *testing.T, options json.RawMessage) string {
	t.Helper()
	hash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// TestProfileCreateSeedsExploreCopyAndDefaultSelections proves that every new
// profile starts with an Explore binding copied from its translation binding
// and the five prompt selections pinned to the seeded defaults — while the
// saved profile JSON response shape stays unchanged.
func TestProfileCreateSeedsExploreCopyAndDefaultSelections(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	profiles := NewProfileStore(db)
	promptStore := prompts.NewStore(db)

	created, err := profiles.Create(ctx, "Codex Only", sampleBindingsForTest(t))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Explore starts as the exact translation binding.
	explore, err := profiles.ExploreBinding(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explore == nil {
		t.Fatal("created profile has no Explore binding")
	}
	translation := created.Bindings[1]
	if translation.StageID != "translation" {
		t.Fatalf("bindings out of order: %+v", created.Bindings)
	}
	if explore.ProviderID != translation.ProviderID || explore.ModelID != translation.ModelID ||
		explore.OptionsHash != translation.OptionsHash {
		t.Fatalf("explore binding %+v does not copy translation %+v", explore, translation)
	}

	// The five prompt selections pin the seeded defaults.
	seedErr := promptStore.EnsureSeed(ctx)
	if seedErr != nil {
		t.Fatalf("seed defaults: %v", seedErr)
	}
	selections, err := profiles.PromptSelections(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != len(prompts.Types) {
		t.Fatalf("selections = %+v", selections)
	}
	for _, promptType := range prompts.Types {
		version, err := promptStore.Resolve(ctx, promptType, selections[promptType])
		if err != nil {
			t.Fatalf("selection of %s: %v", promptType, err)
		}
		if version.Version != 1 {
			t.Fatalf("%s selection is v%d, want the seeded v1", promptType, version.Version)
		}
	}

	// The stored profile response shape carries no new fields.
	encoded := mustJSON(t, created)
	for _, leaked := range []string{"explore_binding", "prompt_versions", "prompt_selections"} {
		if strings.Contains(encoded, leaked) {
			t.Fatalf("profile JSON leaked new field %q: %s", leaked, encoded)
		}
	}
}

// TestExploreBindingsAndSelectionsAreIndependent proves two profiles keep
// independent Explore bindings and prompt selections: changing one profile's
// explore binding or selection never moves the other profile's.
func TestExploreBindingsAndSelectionsAreIndependent(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	profiles := NewProfileStore(db)

	first, err := profiles.Create(ctx, "First", sampleBindingsForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := profiles.Create(ctx, "Second", sampleBindingsForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	originalFirst, err := profiles.ExploreBinding(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Repoint the first profile's Explore binding at another model.
	firstExploreOptions := mustCanonicalOptions(t, `{"reasoning_effort":"high"}`)
	if err := profiles.SaveExploreBinding(ctx, first.ID, "codex-app-server", "model-b",
		firstExploreOptions, mustOptionsHash(t, firstExploreOptions)); err != nil {
		t.Fatalf("save explore binding: %v", err)
	}
	firstAfter, err := profiles.ExploreBinding(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstAfter.ModelID != "model-b" {
		t.Fatalf("first explore binding = %+v, want model-b", firstAfter)
	}
	secondExplore, err := profiles.ExploreBinding(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondExplore.ModelID != originalFirst.ModelID {
		t.Fatalf("second explore binding moved: %+v", secondExplore)
	}

	// Repoint the first profile's explore prompt selection; the second
	// profile keeps the default pin.
	promptStore := prompts.NewStore(db)
	custom, err := promptStore.Save(ctx, prompts.TypeExplore, "Custom explore instruction", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE profile_prompt_selection SET prompt_version_id = ? WHERE profile_id = ? AND prompt_type = ?`,
		custom.ID, first.ID, prompts.TypeExplore); err != nil {
		t.Fatal(err)
	}
	firstSelections, err := profiles.PromptSelections(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondSelections, err := profiles.PromptSelections(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstSelections[prompts.TypeExplore] != custom.ID {
		t.Fatalf("first explore selection = %q, want %q", firstSelections[prompts.TypeExplore], custom.ID)
	}
	if secondSelections[prompts.TypeExplore] == custom.ID {
		t.Fatal("second profile's explore selection moved with the first profile's")
	}

	// Replacing a profile's bindings leaves its explore binding and prompt
	// selections untouched.
	if _, err := profiles.Replace(ctx, first.ID, "First renamed", sampleBindingsForTest(t)); err != nil {
		t.Fatal(err)
	}
	stillCustom, err := profiles.PromptSelections(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillCustom[prompts.TypeExplore] != custom.ID {
		t.Fatal("profile replace moved the prompt selection")
	}
	stillExplore, err := profiles.ExploreBinding(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillExplore.ModelID != "model-b" {
		t.Fatal("profile replace moved the explore binding")
	}

	// Deleting a profile cascades its explore binding and selections but
	// never touches the immutable prompt versions.
	if err := profiles.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if explore, err := profiles.ExploreBinding(ctx, first.ID); err != nil || explore != nil {
		t.Fatalf("explore binding survived profile delete: %+v %v", explore, err)
	}
	var counts struct{ selections, versions int }
	if err := db.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM profile_prompt_selection WHERE profile_id = ?),
		(SELECT COUNT(*) FROM prompt_version)`, first.ID).Scan(&counts.selections, &counts.versions); err != nil {
		t.Fatal(err)
	}
	if counts.selections != 0 {
		t.Fatalf("%d selections survived profile delete", counts.selections)
	}
	if counts.versions < len(prompts.Types) {
		t.Fatalf("prompt versions lost by profile delete: %d", counts.versions)
	}
}

// TestSeededDefaultsMatchBuiltinBuilders guards the seeding bytes: every
// builtin default must remain the exact instruction prefix its annotator
// builder emits, so seeded v1 rows reproduce builtin behavior verbatim.
func TestSeededDefaultsMatchBuiltinBuilders(t *testing.T) {
	chunk := semantics.PreparedChunk{
		Title: "Een titel", SourceLanguage: "nl", TargetLanguage: "en",
		ContentHash: "hash", Block: semantics.Block{BlockIndex: 0, SourceText: "Een zin."},
		InputHash: "input-hash",
	}
	linguistic := annotator.BuildLinguisticChunkPrompt(chunk)
	if prefix := prompts.DefaultInstruction(prompts.TypeLinguisticAnalysis); !strings.HasPrefix(linguistic, prefix) {
		t.Fatal("linguistic_analysis default diverged from the builtin builder prefix")
	}
	translation := annotator.BuildTranslationChunkPrompt(chunk, nil)
	if prefix := prompts.DefaultInstruction(prompts.TypeArticleTranslation); !strings.HasPrefix(translation, prefix) {
		t.Fatal("article_translation default diverged from the builtin builder prefix")
	}
	correction := annotator.BuildStageCorrectionPrompt("some validation error", "previous response")
	if prefix := prompts.DefaultInstruction(prompts.TypeCorrection); !strings.HasPrefix(correction, prefix) {
		t.Fatal("correction default diverged from the builtin builder prefix")
	}
	dictionaryPrompt, err := annotator.DictionaryPrompt(semantics.DictionaryInput{
		Version: semantics.DictionaryContractVersion, LookupForm: "huis", LookupKind: semantics.DictionaryLookupWord,
		SourceLanguage: "nl", TargetLanguage: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prefix := prompts.DefaultInstruction(prompts.TypeExplore); !strings.HasPrefix(dictionaryPrompt, prefix) {
		t.Fatal("explore default diverged from the builtin builder prefix")
	}
}

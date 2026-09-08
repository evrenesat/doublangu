package prompts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/store"
)

func openPromptStore(t *testing.T) (*Store, *store.DB) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db), db
}

func TestDefaultsCoverEveryFixedType(t *testing.T) {
	defaults := Defaults()
	if len(defaults) != len(Types) {
		t.Fatalf("Defaults covers %d types, want %d", len(defaults), len(Types))
	}
	seen := make(map[PromptType]bool, len(Types))
	for index, prompt := range defaults {
		if prompt.Type != Types[index] {
			t.Fatalf("Defaults[%d] is %s, want fixed order entry %s", index, prompt.Type, Types[index])
		}
		if seen[prompt.Type] {
			t.Fatalf("duplicate default for %s", prompt.Type)
		}
		seen[prompt.Type] = true
		if err := ValidateInstruction(NormalizeInstruction(prompt.Instruction)); err != nil {
			t.Fatalf("default %s invalid: %v", prompt.Type, err)
		}
		if strings.Contains(prompt.Instruction, "\r") {
			t.Fatalf("default %s contains carriage returns", prompt.Type)
		}
	}
	for _, promptType := range Types {
		if DefaultInstruction(promptType) == "" {
			t.Fatalf("default instruction for %s is empty", promptType)
		}
	}
	if DefaultInstruction("poetry") != "" {
		t.Fatal("unknown prompt type returned a default instruction")
	}
}

func TestSaveAllocatesImmutableAscendingVersions(t *testing.T) {
	store, _ := openPromptStore(t)
	ctx := context.Background()

	first, err := store.Save(ctx, TypeExplore, "First instruction\r\nsecond line", "  Owner label  ")
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	if first.Version != 1 || first.Label != "Owner label" {
		t.Fatalf("first save = %+v", first)
	}
	// CRLF normalizes to LF before hash; every other byte is preserved.
	if strings.Contains(first.InstructionText, "\r") {
		t.Fatal("saved instruction kept carriage returns")
	}
	wantHash := sha256.Sum256([]byte("First instruction\nsecond line"))
	if first.ContentHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("content hash = %s", first.ContentHash)
	}

	second, err := store.Save(ctx, TypeExplore, "Second instruction", "")
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second save version = %d", second.Version)
	}

	// Versions of another type allocate their own stream.
	otherType, err := store.Save(ctx, TypeCorrection, "Correct the response.", "")
	if err != nil {
		t.Fatalf("save correction: %v", err)
	}
	if otherType.Version != 1 {
		t.Fatalf("correction save version = %d, want 1 in its own stream", otherType.Version)
	}

	listed, err := store.List(ctx, TypeExplore)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Version != 2 || listed[1].Version != 1 {
		t.Fatalf("explore list = %+v", listed)
	}

	// Stored content round-trips byte-exact: two reads return the identical
	// immutable row, including the database-assigned creation timestamp.
	reloaded, err := store.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.InstructionText != first.InstructionText || reloaded.ContentHash != first.ContentHash {
		t.Fatalf("stored instruction/hash differ from the save result: %+v vs %+v", reloaded, first)
	}
	again, err := store.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *again != *reloaded {
		t.Fatalf("stored version mutated between reads: %+v vs %+v", again, reloaded)
	}
	if reloaded.CreatedAt == "" {
		t.Fatal("stored version has no created_at")
	}
}

func TestSaveRejectsInvalidInput(t *testing.T) {
	store, _ := openPromptStore(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		promptType  PromptType
		instruction string
		label       string
	}{
		{"unknown type", "poetry", "text", ""},
		{"blank instruction", TypeExplore, "   \n\t ", ""},
		{"empty instruction", TypeExplore, "", ""},
		{"oversized instruction", TypeExplore, strings.Repeat("a", MaxInstructionBytes+1), ""},
		{"invalid utf-8 instruction", TypeExplore, "broken\xff utf8", ""},
		{"oversized label", TypeExplore, "text", strings.Repeat("l", MaxLabelScalars+1)},
		{"invalid utf-8 label", TypeExplore, "text", "bad\xff label"},
	}
	for _, testCase := range cases {
		if _, err := store.Save(ctx, testCase.promptType, testCase.instruction, testCase.label); err == nil {
			t.Fatalf("%s: save accepted invalid input", testCase.name)
		}
	}

	// Exactly 64 KiB of valid text and an 80-scalar label (astral chars count
	// as one scalar each) are accepted.
	exactlyMax := strings.Repeat("a", MaxInstructionBytes)
	if _, err := store.Save(ctx, TypeExplore, exactlyMax, strings.Repeat("\U0001F600", MaxLabelScalars)); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
}

func TestResolveEnforcesMatchingType(t *testing.T) {
	store, _ := openPromptStore(t)
	ctx := context.Background()

	saved, err := store.Save(ctx, TypeExplore, "Explore instruction", "")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.Resolve(ctx, TypeExplore, saved.ID)
	if err != nil {
		t.Fatalf("resolve same type: %v", err)
	}
	if resolved.ID != saved.ID {
		t.Fatalf("resolved = %+v", resolved)
	}
	if _, err := store.Resolve(ctx, TypeCorrection, saved.ID); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("cross-type error = %v", err)
	}
	if _, err := store.Resolve(ctx, TypeExplore, "missing"); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("unknown version error = %v", err)
	}
}

func TestEnsureSeedIsIdempotentAndKeepsExistingSelections(t *testing.T) {
	store, db := openPromptStore(t)
	ctx := context.Background()

	// An existing profile from before the prompt library existed, activated
	// as the singleton the way a real installation would have.
	if _, err := db.Exec(ctx, `INSERT INTO analysis_pipeline_profile (id, name) VALUES ('profile-1', 'Main')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO analysis_pipeline_settings (id, active_profile_id, updated_at)
		VALUES (1, 'profile-1', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}

	if err := store.EnsureSeed(ctx); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	firstPass := make(map[PromptType]Version, len(Types))
	for _, promptType := range Types {
		versions, err := store.List(ctx, promptType)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 1 || versions[0].Version != 1 {
			t.Fatalf("%s seeded versions = %+v", promptType, versions)
		}
		firstPass[promptType] = versions[0]
		if versions[0].InstructionText != NormalizeInstruction(DefaultInstruction(promptType)) {
			t.Fatalf("seeded %s instruction differs from the builtin default", promptType)
		}
	}

	// A second startup seeding neither duplicates nor rewrites anything.
	if err := store.EnsureSeed(ctx); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	for _, promptType := range Types {
		versions, err := store.List(ctx, promptType)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 1 || versions[0].ID != firstPass[promptType].ID ||
			versions[0].ContentHash != firstPass[promptType].ContentHash {
			t.Fatalf("second seeding changed %s: %+v vs %+v", promptType, versions, firstPass[promptType])
		}
	}

	// Existing profiles receive default selections for every type.
	selections, err := store.SelectionsByProfile(ctx, "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != len(Types) {
		t.Fatalf("seeded selections = %+v", selections)
	}
	for _, promptType := range Types {
		if selections[promptType].VersionID != firstPass[promptType].ID {
			t.Fatalf("%s selection = %+v, want seeded v1", promptType, selections[promptType])
		}
	}

	// The pre-existing active profile survives both seedings untouched.
	var activeProfileID string
	if err := db.QueryRow(ctx, `SELECT active_profile_id FROM analysis_pipeline_settings WHERE id = 1`).Scan(&activeProfileID); err != nil {
		t.Fatal(err)
	}
	if activeProfileID != "profile-1" {
		t.Fatalf("active profile after seeding = %q, want profile-1", activeProfileID)
	}
}

func TestConcurrentSavesAllocateDistinctVersions(t *testing.T) {
	store, _ := openPromptStore(t)
	ctx := context.Background()

	const writers = 6
	errs := make([]error, writers)
	versions := make([]int, writers)
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			saved, err := store.Save(ctx, TypeExplore, "Concurrent instruction", "")
			if err == nil {
				versions[index] = saved.Version
			}
			errs[index] = err
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent save %d: %v", index, err)
		}
	}
	seen := make(map[int]bool, writers)
	for _, version := range versions {
		if version == 0 || seen[version] {
			t.Fatalf("versions not distinct: %v", versions)
		}
		seen[version] = true
	}
}

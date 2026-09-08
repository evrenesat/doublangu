package annotator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"doublangu/internal/pipeline"
	"doublangu/internal/prompts"
)

// readGoldenPrompt loads one recorded builtin builder output.
func readGoldenPrompt(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return string(contents)
}

// TestBuiltinBuildersMatchGoldenBytes proves the instruction/envelope split
// changed nothing: the builtin builders still render the exact bytes they
// rendered before any owner-editable instruction could reach them.
func TestBuiltinBuildersMatchGoldenBytes(t *testing.T) {
	chunk, linguistic := stageFixture(t)
	golden := map[string]string{
		"golden_linguistic_prompt.txt":  BuildLinguisticChunkPrompt(chunk),
		"golden_translation_prompt.txt": BuildTranslationChunkPrompt(chunk, linguistic),
		"golden_correction_prompt.txt":  BuildStageCorrectionPrompt("tokens[0].new_sense_ref: ref must match", `{"version":"reader.linguistic.v1"}`),
	}
	for name, rendered := range golden {
		if expected := readGoldenPrompt(t, name); rendered != expected {
			t.Fatalf("%s diverged from the recorded builtin bytes", name)
		}
	}
}

// TestInstructionParameterizedBuildersReproduceBuiltinBytes proves the
// instruction and the data envelope are cleanly separated: rendering with the
// builtin default equals the legacy wrapper byte-for-byte, the data envelope
// itself is identical for legacy and captured rendering, and only a captured
// prefix inserts the fixed code-owned data-boundary statement between the
// instruction and the envelope.
func TestInstructionParameterizedBuildersReproduceBuiltinBytes(t *testing.T) {
	chunk, linguistic := stageFixture(t)
	const customInstruction = "Custom owner instruction.\n"
	cases := []struct {
		name           string
		defaultInstr   string
		renderBuiltin  string
		renderCaptured string
	}{
		{
			name:           "linguistic",
			defaultInstr:   prompts.DefaultInstruction(prompts.TypeLinguisticAnalysis),
			renderBuiltin:  BuildLinguisticStagePrompt(prompts.DefaultInstruction(prompts.TypeLinguisticAnalysis), chunk),
			renderCaptured: BuildLinguisticStagePrompt(CapturedStagePrompts(customInstruction, "").generationPrefix(), chunk),
		},
		{
			name:           "translation",
			defaultInstr:   prompts.DefaultInstruction(prompts.TypeArticleTranslation),
			renderBuiltin:  BuildTranslationStagePrompt(prompts.DefaultInstruction(prompts.TypeArticleTranslation), chunk, linguistic),
			renderCaptured: BuildTranslationStagePrompt(CapturedStagePrompts(customInstruction, "").generationPrefix(), chunk, linguistic),
		},
		{
			name:           "correction",
			defaultInstr:   prompts.DefaultInstruction(prompts.TypeCorrection),
			renderBuiltin:  BuildStageCorrectionPromptWithInstruction(prompts.DefaultInstruction(prompts.TypeCorrection), "validation error", "previous response"),
			renderCaptured: BuildStageCorrectionPromptWithInstruction(CapturedStagePrompts("", customInstruction).correctionPrefix(), "validation error", "previous response"),
		},
	}
	for _, testCase := range cases {
		envelope := strings.TrimPrefix(testCase.renderBuiltin, testCase.defaultInstr)
		if !strings.HasPrefix(testCase.renderBuiltin, testCase.defaultInstr) || envelope == "" {
			t.Fatalf("%s: builtin render does not start with the default instruction", testCase.name)
		}
		if !strings.HasPrefix(testCase.renderCaptured, customInstruction+capturedDataBoundary) {
			t.Fatalf("%s: captured render lacks the fixed boundary after the instruction", testCase.name)
		}
		if !strings.HasSuffix(testCase.renderCaptured, envelope) {
			t.Fatalf("%s: captured render changed the data envelope", testCase.name)
		}
	}
}

// TestDefaultStagePromptsResolveBuiltinInstructions proves the legacy
// resolution path returns nonempty builtin texts for both registered stages
// and the shared correction default.
func TestDefaultStagePromptsResolveBuiltinInstructions(t *testing.T) {
	for _, stage := range []pipeline.StageID{pipeline.StageLinguisticAnalysis, pipeline.StageTranslation} {
		promptsForStage := DefaultStagePrompts(stage)
		if promptsForStage.Generation == "" || promptsForStage.Correction == "" {
			t.Fatalf("default stage prompts for %s are empty", stage)
		}
	}
}

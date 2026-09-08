package annotator

import (
	"context"
	"strings"
	"testing"

	"doublangu/internal/pipeline"
)

// TestCapturedPromptsKeepDataBoundaryWithoutTrailingNewline proves the
// code-owned boundary survives full instruction replacement: a captured
// instruction with no trailing newline still ends up separated from the data
// envelope, and the fixed data-only statement labels every following section
// for linguistic, translation, and correction rendering.
func TestCapturedPromptsKeepDataBoundaryWithoutTrailingNewline(t *testing.T) {
	chunk := executorChunk(t)
	const instruction = "Translate concisely."
	captured := CapturedStagePrompts(instruction, instruction)

	linguistic := BuildLinguisticStagePrompt(captured.generationPrefix(), chunk)
	translation := BuildTranslationStagePrompt(captured.generationPrefix(), chunk, nil)
	correction := BuildStageCorrectionPromptWithInstruction(captured.correctionPrefix(), "some error", "previous response")

	for name, rendered := range map[string]string{
		"linguistic": linguistic, "translation": translation, "correction": correction,
	} {
		if !strings.Contains(rendered, capturedDataBoundary) {
			t.Fatalf("%s: fixed data-only statement missing", name)
		}
		if !strings.Contains(rendered, instruction+capturedDataBoundary) {
			t.Fatalf("%s: boundary not separated from the newline-less instruction", name)
		}
		if strings.Contains(rendered, instruction+"\nversion:") || strings.Contains(rendered, instruction+"\nVALIDATION_ERRORS_BEGIN") {
			t.Fatalf("%s: instruction joined directly to a data section", name)
		}
	}

	// The envelope still opens with its own code-owned lines after the
	// boundary, and the boundary sits between instruction and data only.
	if !strings.Contains(linguistic, capturedDataBoundary+"version: "+pipeline.LinguisticContractVersion+"\n") {
		t.Fatal("linguistic envelope did not start on its own line after the boundary")
	}
	if !strings.Contains(correction, capturedDataBoundary+"VALIDATION_ERRORS_BEGIN\n") {
		t.Fatal("correction data sections did not start after the boundary")
	}
	if strings.Count(correction, capturedDataBoundary) != 1 {
		t.Fatal("correction boundary rendered more than once")
	}
	// The default legacy linguistic instruction never needed the boundary and
	// must not gain one through the captured path.
	if strings.Contains(BuildLinguisticChunkPrompt(chunk), capturedDataBoundary) {
		t.Fatal("legacy builtin render unexpectedly contains the captured boundary")
	}
}

// TestCapturedExecutionAddsBoundaryAndLegacyExecutionDoesNot proves the
// legacy/captured distinction through real execution, corrective turns
// included: a captured stage renders the boundary in initial and corrective
// prompts while a legacy stage stays byte-identical to the historical
// builders with no boundary text.
func TestCapturedExecutionAddsBoundaryAndLegacyExecutionDoesNot(t *testing.T) {
	chunk := executorChunk(t)
	invalid := `{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`
	valid := validLinguisticRaw(t, chunk)
	const generation = "Custom generation instruction."
	const correction = "Custom correction instruction."

	for _, testCase := range []struct {
		name    string
		prompts StagePrompts
	}{
		{name: "captured", prompts: CapturedStagePrompts(generation, correction)},
		{name: "legacy", prompts: DefaultStagePrompts(pipeline.StageLinguisticAnalysis)},
	} {
		provider := &scriptedSessionProvider{
			descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
			turns:      []string{invalid, valid},
		}
		_, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk, testCase.prompts)
		if err != nil {
			t.Fatalf("%s: execution failed: %v", testCase.name, err)
		}
		if len(result.Turns) != 2 {
			t.Fatalf("%s: turns = %d, want initial plus corrective", testCase.name, len(result.Turns))
		}
		initial, corrective := provider.prompts[0], provider.prompts[1]

		if testCase.name == "captured" {
			if !strings.Contains(initial, capturedDataBoundary) || !strings.Contains(corrective, capturedDataBoundary) {
				t.Fatalf("captured execution lost the data boundary: initial %q corrective %q", initial[:40], corrective[:40])
			}
			if !strings.HasPrefix(initial, generation+capturedDataBoundary) {
				t.Fatal("captured initial prompt did not separate the newline-less instruction")
			}
			if !strings.HasPrefix(corrective, correction+capturedDataBoundary) {
				t.Fatal("captured corrective prompt did not separate the newline-less correction instruction")
			}
			if !strings.Contains(corrective, "VALIDATION_ERRORS_BEGIN\n") || !strings.Contains(corrective, "PREVIOUS_RESPONSE_BEGIN\n") {
				t.Fatal("captured corrective prompt lost the code-owned data sections")
			}
			continue
		}

		// Legacy compatibility through execution, not wrappers alone.
		if initial != BuildLinguisticChunkPrompt(chunk) {
			t.Fatal("legacy initial prompt diverged from the historical builtin bytes")
		}
		wantCorrective := BuildStageCorrectionPrompt(result.Turns[0].ValidationError, result.Turns[0].CompletedResponse)
		if corrective != wantCorrective {
			t.Fatal("legacy corrective prompt diverged from the historical builtin bytes")
		}
		if strings.Contains(corrective, capturedDataBoundary) {
			t.Fatal("legacy corrective prompt unexpectedly contains the captured boundary")
		}
	}
}

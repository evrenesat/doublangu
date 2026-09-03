package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/config"
	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
)

// scriptedSessionProvider is a provider whose session serves canned turn
// texts, optionally failing specific turns.
type scriptedSessionProvider struct {
	descriptor ProviderDescriptor
	turns      []string
	turnErrors []error
	usages     []string
	timings    []string
	mu         sync.Mutex
	calls      int
	schemas    []json.RawMessage
	prompts    []string
}

func (p *scriptedSessionProvider) Descriptor() ProviderDescriptor { return p.descriptor }

func (p *scriptedSessionProvider) ListModels(context.Context) ([]Model, error) {
	return nil, errors.New("not implemented")
}

func (p *scriptedSessionProvider) OpenSession(context.Context, ResolvedBinding) (Session, error) {
	return &scriptedSession{provider: p}, nil
}

type scriptedSession struct {
	provider *scriptedSessionProvider
}

func (s *scriptedSession) Turn(_ context.Context, request TurnRequest) (Completion, error) {
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	index := s.provider.calls
	s.provider.calls++
	s.provider.schemas = append(s.provider.schemas, request.OutputSchema)
	s.provider.prompts = append(s.provider.prompts, request.Prompt)
	if index < len(s.provider.turnErrors) && s.provider.turnErrors[index] != nil {
		return Completion{}, s.provider.turnErrors[index]
	}
	if index < len(s.provider.turns) {
		usage := ""
		if index < len(s.provider.usages) {
			usage = s.provider.usages[index]
		}
		timing := ""
		if index < len(s.provider.timings) {
			timing = s.provider.timings[index]
		}
		return Completion{Text: s.provider.turns[index], ReportedModel: "test-model", UsageJSON: usage, TimingJSON: timing}, nil
	}
	return Completion{}, errors.New("no canned turn")
}

func (s *scriptedSession) Close() error { return nil }

func executorBinding(t *testing.T) ResolvedBinding {
	t.Helper()
	options, err := config.CanonicalizeProviderOptions(ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := pipeline.OptionsHashOf(options)
	if err != nil {
		t.Fatal(err)
	}
	return ResolvedBinding{
		StageID: pipeline.StageLinguisticAnalysis, ProviderID: "codex-app-server",
		ProviderType: ProviderTypeCodexAppServer, ConfigFingerprint: "fp", ModelID: "test-model",
		Options: options, OptionsHash: hash,
		ContractVersion: pipeline.LinguisticContractVersion, PromptVersion: pipeline.LinguisticPromptVersion,
	}
}

func executorChunk(t *testing.T) semantics.PreparedChunk {
	t.Helper()
	input, err := semantics.Prepare("Executor", "nl", "en", []semantics.Block{{BlockIndex: 0, SourceText: "De bank."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	span, err := semantics.ResolveSpan(input.Blocks[0], input.Blocks[0].SourceText, 0)
	if err != nil {
		t.Fatal(err)
	}
	input.Sentences = []semantics.ResolvedSentence{{Index: 0, Span: span}}
	chunk, err := semantics.PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return chunk
}

func validLinguisticRaw(t *testing.T, chunk semantics.PreparedChunk) string {
	t.Helper()
	artifact := semantics.LinguisticArtifact{Version: pipeline.LinguisticContractVersion}
	for _, token := range chunk.Tokens {
		artifact.Tokens = append(artifact.Tokens, semantics.LinguisticTokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestExecuteStageAggregatesUsageAcrossCorrections proves corrective turns do
// not overwrite diagnostics: usage and timing totals accumulate across the
// initial rejected completion and the accepted correction, while per-turn
// completion metadata stays on the individual turn records.
func TestExecuteStageAggregatesUsageAcrossCorrections(t *testing.T) {
	chunk := executorChunk(t)
	valid := validLinguisticRaw(t, chunk)
	provider := &scriptedSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []string{`{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`, valid},
		usages: []string{
			`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`,
			`{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}`,
		},
		timings: []string{
			`{"time_to_first_token":200,"total_time":900}`,
			`{"time_to_first_token":150,"total_time":700}`,
		},
	}
	_, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk)
	if err != nil {
		t.Fatalf("linguistic stage failed: %v", err)
	}
	if len(result.Turns) != 2 {
		t.Fatalf("turns = %d, want initial plus corrective", len(result.Turns))
	}
	var usage map[string]float64
	if err := json.Unmarshal([]byte(result.UsageJSON), &usage); err != nil {
		t.Fatalf("usage = %q: %v", result.UsageJSON, err)
	}
	if usage["prompt_tokens"] != 18 || usage["completion_tokens"] != 8 || usage["total_tokens"] != 26 {
		t.Fatalf("aggregated usage = %v, want summed totals across both turns", usage)
	}
	var timing map[string]float64
	if err := json.Unmarshal([]byte(result.TimingJSON), &timing); err != nil {
		t.Fatalf("timing = %q: %v", result.TimingJSON, err)
	}
	if timing["time_to_first_token"] != 350 || timing["total_time"] != 1600 {
		t.Fatalf("aggregated timing = %v, want summed totals across both turns", timing)
	}
}

func TestExecuteLinguisticStageCorrectsOnceAndValidates(t *testing.T) {
	chunk := executorChunk(t)
	valid := validLinguisticRaw(t, chunk)
	provider := &scriptedSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []string{`{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`, valid},
	}
	validated, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk)
	if err != nil {
		t.Fatalf("linguistic stage failed: %v", err)
	}
	if validated == nil || len(validated.Tokens) != len(chunk.Tokens) {
		t.Fatalf("validated = %+v", validated)
	}
	if len(result.Turns) != 2 || result.Turns[0].TurnKind != "initial" || result.Turns[1].TurnKind != "corrective" {
		t.Fatalf("turns = %+v", result.Turns)
	}
	if result.Turns[0].ValidationError == "" || result.Turns[1].ValidationError != "" {
		t.Fatalf("validation errors = %q / %q", result.Turns[0].ValidationError, result.Turns[1].ValidationError)
	}
	if result.Turns[0].ResponseHash == "" || result.Turns[1].ResponseHash == "" {
		t.Fatalf("response hashes missing: %+v", result.Turns)
	}
	// The correction reuses the same logical session (one OpenSession call).
	provider.mu.Lock()
	calls := provider.calls
	promptCount := len(provider.prompts)
	provider.mu.Unlock()
	if calls != 2 || promptCount != 2 {
		t.Fatalf("session calls = %d prompts = %d", calls, promptCount)
	}
	if len(provider.schemas) != 2 {
		t.Fatal("schema not reused across corrections")
	}
	// The corrective prompt embeds the rejected raw artifact so the provider
	// sees exactly what failed validation.
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if strings.Contains(provider.prompts[0], "VALIDATION_ERRORS_BEGIN") || strings.Contains(provider.prompts[0], `"tokens":[]`) {
		t.Fatal("initial prompt unexpectedly contains correction framing or a rejected artifact")
	}
	if !strings.Contains(provider.prompts[1], "VALIDATION_ERRORS_BEGIN") ||
		!strings.Contains(provider.prompts[1], "PREVIOUS_RESPONSE_BEGIN") ||
		!strings.Contains(provider.prompts[1], `"reader.linguistic.v1"`) ||
		!strings.Contains(provider.prompts[1], `"tokens":[]`) {
		t.Fatalf("corrective prompt lacks the rejected artifact: %q", provider.prompts[1])
	}
}

func TestExecuteLinguisticStageExhaustsCorrections(t *testing.T) {
	chunk := executorChunk(t)
	provider := &scriptedSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns: []string{
			`{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`,
			`{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`,
			`{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`,
		},
	}
	_, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk)
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Phase != "stage_validation" || stageErr.Code != CodeInvalidOutput {
		t.Fatalf("exhaustion error = %v", err)
	}
	if len(result.Turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(result.Turns))
	}
	for index, record := range result.Turns {
		if index > 0 && record.TurnKind != "corrective" {
			t.Fatalf("turn %d kind = %q", index, record.TurnKind)
		}
	}
}

func TestExecuteTranslationStageReportsFinalValidation(t *testing.T) {
	chunk := executorChunk(t)
	valid := validLinguisticRaw(t, chunk)
	linguisticProvider := &scriptedSessionProvider{turns: []string{valid}}
	linguistic, _, err := ExecuteLinguisticStage(context.Background(), linguisticProvider, executorBinding(t), chunk)
	if err != nil {
		t.Fatal(err)
	}
	// A translation artifact missing every construction translates to a
	// stage_validation failure; a syntactically complete but semantically
	// wrong merge is hard to arrange here, so verify the phase plumbing with
	// a blank artifact (final decode failure path).
	bad := `{"version":"reader.translation.v1","tokens":[],"new_senses":[],"constructions":[]}`
	translationProvider := &scriptedSessionProvider{turns: []string{bad, bad, bad}}
	options, _ := config.CanonicalizeProviderOptions(ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0,"max_output_tokens":16384}`))
	hash, _ := pipeline.OptionsHashOf(options)
	binding := ResolvedBinding{
		StageID: pipeline.StageTranslation, ProviderID: "omlx", ProviderType: ProviderTypeOpenAICompatible,
		ConfigFingerprint: "fp", ModelID: "model", Options: options, OptionsHash: hash,
		ContractVersion: pipeline.TranslationContractVersion, PromptVersion: pipeline.TranslationPromptVersion,
	}
	_, _, err = ExecuteTranslationStage(context.Background(), translationProvider, binding, chunk, linguistic)
	var stageErr *StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("translation error = %v", err)
	}
	if stageErr.Phase != "stage_validation" {
		t.Fatalf("phase = %q", stageErr.Phase)
	}
}

func TestExecuteStageProviderFailureIsRecorded(t *testing.T) {
	chunk := executorChunk(t)
	provider := &scriptedSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turnErrors: []error{&Error{Code: CodeProviderFailure, Err: errors.New("provider down")}},
	}
	_, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk)
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Phase != "provider" || stageErr.Code != CodeProviderFailure {
		t.Fatalf("provider error = %v", err)
	}
	if len(result.Turns) != 1 || result.Turns[0].Status != "failed" || result.Turns[0].ProviderError == "" {
		t.Fatalf("turns = %+v", result.Turns)
	}
}

func TestStageErrorPhaseAndCodeHelpers(t *testing.T) {
	err := &StageError{Stage: pipeline.StageTranslation, Phase: "final_validation", Code: CodeInvalidOutput, Err: errors.New("bad merge")}
	if StageErrorCode(err) != CodeInvalidOutput || StageErrorPhase(err) != "translation final_validation" {
		t.Fatalf("helpers = %q / %q", StageErrorCode(err), StageErrorPhase(err))
	}
	if StageErrorCode(&Error{Code: CodeTimeout}) != CodeTimeout {
		t.Fatal("code helper does not unwrap plain errors")
	}
}

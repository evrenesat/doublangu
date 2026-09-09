package annotator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"doublangu/internal/pipeline"
)

// scriptedTurn couples one canned completion (possibly partial) with the
// error to return alongside it, modeling providers that stream a partial
// response before failing.
type scriptedTurn struct {
	text string
	err  error
}

type recorderFixtureProvider struct {
	descriptor ProviderDescriptor
	turns      []scriptedTurn
	calls      int
}

func (p *recorderFixtureProvider) Descriptor() ProviderDescriptor { return p.descriptor }
func (p *recorderFixtureProvider) ListModels(context.Context) ([]Model, error) {
	return nil, errors.New("not used")
}
func (p *recorderFixtureProvider) OpenSession(context.Context, ResolvedBinding) (Session, error) {
	return &recorderFixtureSession{provider: p}, nil
}

type recorderFixtureSession struct {
	provider *recorderFixtureProvider
}

func (s *recorderFixtureSession) Turn(_ context.Context, _ TurnRequest) (Completion, error) {
	index := s.provider.calls
	s.provider.calls++
	if index >= len(s.provider.turns) {
		return Completion{}, errors.New("no canned turn")
	}
	turn := s.provider.turns[index]
	completion := Completion{Text: turn.text, ReportedModel: "test-model"}
	return completion, turn.err
}

func (s *recorderFixtureSession) Close() error { return nil }

// TestTurnRecorderSeesEveryTurnPromptly proves the recorder receives every
// turn — initial and corrective — as it completes, and that a recorder
// storage failure surfaces as an explicit storage phase error.
func TestTurnRecorderSeesEveryTurnPromptly(t *testing.T) {
	chunk := executorChunk(t)
	invalid := `{"version":"reader.linguistic.v1","tokens":[],"new_senses":[],"constructions":[]}`
	valid := validLinguisticRaw(t, chunk)
	provider := &recorderFixtureProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []scriptedTurn{{text: invalid}, {text: valid}},
	}

	var recorded []StageTurnRecord
	validated, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk,
		DefaultStagePrompts(pipeline.StageLinguisticAnalysis), WithTurnRecorder(func(_ context.Context, turn StageTurnRecord) error {
			recorded = append(recorded, turn)
			return nil
		}))
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}
	if validated == nil {
		t.Fatal("no validated artifact")
	}
	if len(recorded) != 2 || recorded[0].TurnKind != "initial" || recorded[1].TurnKind != "corrective" {
		t.Fatalf("recorded kinds = %v, want initial then corrective", recorded)
	}
	// The rejected initial turn keeps its exact validation error in the
	// durable callback; the accepted correction carries none.
	if strings.TrimSpace(recorded[0].ValidationError) == "" {
		t.Fatal("invalid initial callback must carry a nonempty validation error")
	}
	if strings.TrimSpace(recorded[1].ValidationError) != "" {
		t.Fatalf("valid correction callback must carry no validation error, got %q", recorded[1].ValidationError)
	}
	// Callback records agree with the returned in-memory turns.
	if len(result.Turns) != len(recorded) {
		t.Fatalf("returned turns = %d, recorded = %d, want agreement", len(result.Turns), len(recorded))
	}
	for i := range recorded {
		if result.Turns[i].ValidationError != recorded[i].ValidationError {
			t.Fatalf("turn %d validation mismatch: returned %q vs recorded %q", i, result.Turns[i].ValidationError, recorded[i].ValidationError)
		}
	}
	if strings.TrimSpace(result.Turns[0].ValidationError) == "" || strings.TrimSpace(result.Turns[1].ValidationError) != "" {
		t.Fatalf("returned turns validation = %q/%q, want error then empty", result.Turns[0].ValidationError, result.Turns[1].ValidationError)
	}
	// Each provider turn is recorded exactly once.
	if provider.calls != 2 || len(recorded) != 2 {
		t.Fatalf("provider calls = %d, recorded = %d, want 2 each", provider.calls, len(recorded))
	}

	// A recorder failure is an explicit storage error, not a silent drop.
	failProvider := &recorderFixtureProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []scriptedTurn{{text: valid}},
	}
	_, _, err = ExecuteLinguisticStage(context.Background(), failProvider, executorBinding(t), chunk,
		DefaultStagePrompts(pipeline.StageLinguisticAnalysis), WithTurnRecorder(func(_ context.Context, turn StageTurnRecord) error {
			return errors.New("history storage unavailable")
		}))
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Phase != "storage" || stageErr.Code != CodeStorageFailed {
		t.Fatalf("recorder failure error = %v, want a storage phase StageError", err)
	}
	// A storage failure stops the stage without issuing further turns.
	if failProvider.calls != 1 {
		t.Fatalf("storage failure issued %d provider turns, want exactly 1", failProvider.calls)
	}
}

// TestPartialResponseCapturedOnProviderError proves a provider error that
// still carried completion text keeps that text on the failed turn, marked
// failed and never validated.
func TestPartialResponseCapturedOnProviderError(t *testing.T) {
	chunk := executorChunk(t)
	partial := `{"version":"reader.linguistic.v1","tokens":[],"new_senses":[{"ref":"half-done"}`
	provider := &recorderFixtureProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		turns:      []scriptedTurn{{text: partial, err: errors.New("connection reset mid-response")}},
	}
	_, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk,
		DefaultStagePrompts(pipeline.StageLinguisticAnalysis))
	if err == nil {
		t.Fatal("provider error must fail the stage")
	}
	if len(result.Turns) != 1 || result.Turns[0].Status != "failed" {
		t.Fatalf("turns = %+v, want one failed turn", result.Turns)
	}
	if result.Turns[0].CompletedResponse != partial {
		t.Fatalf("partial response not captured: %q", result.Turns[0].CompletedResponse)
	}
	if !strings.Contains(result.Turns[0].ProviderError, "connection reset") {
		t.Fatalf("provider error not retained: %q", result.Turns[0].ProviderError)
	}
}

// TestOpenSessionFailureYieldsZeroTurns proves an open-session failure is a
// pre-turn provider failure with zero recorded turns.
func TestOpenSessionFailureYieldsZeroTurns(t *testing.T) {
	chunk := executorChunk(t)
	provider := &failingOpenSessionProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		openErr:    errors.New("app-server process exited"),
	}
	_, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk,
		DefaultStagePrompts(pipeline.StageLinguisticAnalysis))
	if err == nil {
		t.Fatal("open-session failure must fail the stage")
	}
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Phase != "provider" {
		t.Fatalf("open-session error = %v, want a provider phase error", err)
	}
	if len(result.Turns) != 0 {
		t.Fatalf("open-session failure recorded %d turns, want zero", len(result.Turns))
	}
}

type failingOpenSessionProvider struct {
	descriptor ProviderDescriptor
	openErr    error
}

func (p *failingOpenSessionProvider) Descriptor() ProviderDescriptor { return p.descriptor }
func (p *failingOpenSessionProvider) ListModels(context.Context) ([]Model, error) {
	return nil, errors.New("not used")
}
func (p *failingOpenSessionProvider) OpenSession(context.Context, ResolvedBinding) (Session, error) {
	return nil, p.openErr
}

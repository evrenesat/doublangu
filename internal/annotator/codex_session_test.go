package annotator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"doublangu/internal/pipeline"
)

func TestCodexStageSessionRunsCorrectionsInSameThread(t *testing.T) {
	chunk := executorChunk(t)
	valid := validLinguisticRaw(t, chunk)
	oldLaunch := launchAppServer
	launchAppServer = func(_ context.Context, _, _ string) (*appServerProcess, error) {
		_, process := newScriptedAppServer(valid, 2)
		return process, nil
	}
	defer func() { launchAppServer = oldLaunch }()

	provider := &codexStageProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		binary:     "true", timeout: 10 * time.Second,
	}
	session, err := provider.OpenSession(context.Background(), executorBinding(t))
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	schema, err := json.Marshal(LinguisticOutputSchema(chunk))
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.Turn(context.Background(), TurnRequest{
		StageID: pipeline.StageLinguisticAnalysis, Prompt: "analyze", OutputSchema: schema,
	})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if first.Text == valid {
		t.Fatalf("first turn unexpectedly valid: %q", first.Text)
	}
	second, err := session.Turn(context.Background(), TurnRequest{
		StageID: pipeline.StageLinguisticAnalysis, Prompt: "fix", OutputSchema: schema,
	})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if second.Text != valid {
		t.Fatalf("second turn text = %q", second.Text)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

// TestCodexStageExecutorEndToEnd drives the full linguistic stage through the
// scripted app-server process (initial invalid, one correction, validated
// artifact out).
func TestCodexStageExecutorEndToEnd(t *testing.T) {
	chunk := executorChunk(t)
	valid := validLinguisticRaw(t, chunk)
	oldLaunch := launchAppServer
	launchAppServer = func(_ context.Context, _, _ string) (*appServerProcess, error) {
		_, process := newScriptedAppServer(valid, 2)
		return process, nil
	}
	defer func() { launchAppServer = oldLaunch }()

	provider := &codexStageProvider{
		descriptor: ProviderDescriptor{ID: "codex-app-server", Type: ProviderTypeCodexAppServer, Enabled: true},
		binary:     "true", timeout: 10 * time.Second,
	}
	validated, result, err := ExecuteLinguisticStage(context.Background(), provider, executorBinding(t), chunk)
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	if validated == nil || len(validated.Tokens) != len(chunk.Tokens) {
		t.Fatalf("validated = %+v", validated)
	}
	if len(result.Turns) != 2 || result.Turns[0].TurnKind != "initial" || result.Turns[1].TurnKind != "corrective" {
		t.Fatalf("turns = %+v", result.Turns)
	}
	if result.Turns[0].ValidationError == "" {
		t.Fatalf("first turn validation error missing: %+v", result.Turns[0])
	}
}

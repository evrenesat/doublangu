package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"doublangu/internal/config"
)

type fakeRelayExecutor struct {
	models    []Model
	modelsErr error
	chat      func(context.Context, RelayChatParams) (RelayChatOutcome, error)
	calls     []RelayChatParams
}

func (f *fakeRelayExecutor) ListRelayModels(ctx context.Context) ([]Model, error) {
	if f.modelsErr != nil {
		return nil, f.modelsErr
	}
	return f.models, nil
}

func (f *fakeRelayExecutor) ChatCompletion(ctx context.Context, params RelayChatParams) (RelayChatOutcome, error) {
	f.calls = append(f.calls, params)
	if f.chat != nil {
		return f.chat(ctx, params)
	}
	return RelayChatOutcome{
		Text: "{}", ReportedModel: "qwen-test", ProviderRequestID: "chatcmpl-1",
		FinishReason: "stop", UsageJSON: `{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}`,
		RelayJobID: "01J000000000000000000000C1", RelayRequestID: "01J000000000000000000000C0", Model: params.Model,
	}, nil
}

func relayTestBinding() ResolvedBinding {
	return ResolvedBinding{
		StageID: "linguistic_analysis", ProviderID: "mac-omlx", ProviderType: ProviderTypeMacRelay,
		ModelID: "qwen-test", Options: json.RawMessage(`{"temperature_milli":0,"max_output_tokens":16384}`),
	}
}

func relayTestProvider(executor RelayExecutor) *macRelayProvider {
	return &macRelayProvider{
		descriptor: ProviderDescriptor{ID: "mac-omlx", Type: ProviderTypeMacRelay, Enabled: true},
		timeout:    600 * time.Second, relay: executor,
	}
}

func openRelayTestSession(t *testing.T, provider *macRelayProvider) Session {
	t.Helper()
	session, err := provider.OpenSession(context.Background(), relayTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestMacRelaySuccessProvenance(t *testing.T) {
	executor := &fakeRelayExecutor{models: []Model{{ID: "qwen-test", DisplayName: "qwen-test"}}}
	provider := relayTestProvider(executor)
	session := openRelayTestSession(t, provider)
	completion, err := session.Turn(context.Background(), TurnRequest{StageID: "linguistic_analysis", Prompt: "Vertaal.", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != "{}" || completion.ReportedModel != "qwen-test" || completion.RequestID != "chatcmpl-1" || completion.FinishReason != "stop" {
		t.Fatalf("completion = %+v", completion)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(completion.ProviderMetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"relay_job_id", "relay_request_id", "provider_request_id", "model", "finish_reason"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("metadata missing %q: %s", key, completion.ProviderMetadataJSON)
		}
	}
	if metadata["relay_job_id"] != "01J000000000000000000000C1" || metadata["model"] != "qwen-test" {
		t.Fatalf("metadata = %s", completion.ProviderMetadataJSON)
	}
	if len(executor.calls) != 1 || len(executor.calls[0].Messages) != 1 || executor.calls[0].Messages[0].Content != "Vertaal." {
		t.Fatalf("executor calls = %+v", executor.calls)
	}
}

func TestMacRelayCatalog(t *testing.T) {
	executor := &fakeRelayExecutor{models: []Model{{ID: "a", DisplayName: "a"}, {ID: "b", DisplayName: "b"}}}
	provider := relayTestProvider(executor)
	models, err := provider.ListModels(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("catalog = %+v err=%v", models, err)
	}
	empty := &fakeRelayExecutor{models: []Model{}}
	if _, err := relayTestProvider(empty).ListModels(context.Background()); CodeOf(err) != CodeUnavailable {
		t.Fatalf("empty catalog = %v", err)
	}
}

func TestMacRelayFailureMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"offline", &Error{Code: CodeUnavailable, Err: errors.New("no relay-capable worker is present")}, CodeUnavailable},
		{"auth", &Error{Code: CodeNotAuthenticated, Err: errors.New("relay authentication failed")}, CodeNotAuthenticated},
		{"invalid", &Error{Code: CodeInvalidOutput, Err: errors.New("relay returned an invalid response")}, CodeInvalidOutput},
	}
	for _, tc := range cases {
		executor := &fakeRelayExecutor{}
		executor.chat = func(context.Context, RelayChatParams) (RelayChatOutcome, error) {
			return RelayChatOutcome{}, tc.err
		}
		session := openRelayTestSession(t, relayTestProvider(executor))
		if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "x"}); CodeOf(err) != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.name, CodeOf(err), tc.code)
		}
	}
}

func TestMacRelayTimeout(t *testing.T) {
	executor := &fakeRelayExecutor{}
	executor.chat = func(ctx context.Context, _ RelayChatParams) (RelayChatOutcome, error) {
		<-ctx.Done()
		return RelayChatOutcome{}, ctx.Err()
	}
	provider := &macRelayProvider{
		descriptor: ProviderDescriptor{ID: "mac-omlx", Type: ProviderTypeMacRelay, Enabled: true},
		timeout:    50 * time.Millisecond, relay: executor,
	}
	session := openRelayTestSession(t, provider)
	start := time.Now()
	_, err := session.Turn(context.Background(), TurnRequest{Prompt: "x"})
	if CodeOf(err) != CodeTimeout {
		t.Fatalf("timeout code = %q err=%v", CodeOf(err), err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestMacRelayCancellationMidTurn(t *testing.T) {
	executor := &fakeRelayExecutor{}
	release := make(chan struct{})
	executor.chat = func(ctx context.Context, _ RelayChatParams) (RelayChatOutcome, error) {
		select {
		case <-release:
			return RelayChatOutcome{}, errors.New("released after cancel")
		case <-ctx.Done():
			return RelayChatOutcome{}, ctx.Err()
		}
	}
	session := openRelayTestSession(t, relayTestProvider(executor))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.Turn(ctx, TurnRequest{Prompt: "x"})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must be preserved, got %v", err)
	}
}

func TestMacRelayCorrectiveTurns(t *testing.T) {
	contents := []string{`{"bad":1}`, `{"still-bad":2}`, `{"good":3}`}
	executor := &fakeRelayExecutor{}
	executor.chat = func(_ context.Context, params RelayChatParams) (RelayChatOutcome, error) {
		index := len(executor.calls) - 1
		return RelayChatOutcome{
			Text: contents[index], ReportedModel: "qwen-test", ProviderRequestID: "chatcmpl-1",
			FinishReason: "stop", RelayJobID: "job", RelayRequestID: "req", Model: params.Model,
		}, nil
	}
	session := openRelayTestSession(t, relayTestProvider(executor))
	if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "fix it"}); err != nil {
		t.Fatal(err)
	}
	completion, err := session.Turn(context.Background(), TurnRequest{Prompt: "fix it again"})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != `{"good":3}` {
		t.Fatalf("third turn = %+v", completion)
	}
	if len(executor.calls) != 3 {
		t.Fatalf("calls = %d", len(executor.calls))
	}
	third := executor.calls[2].Messages
	want := []string{"first", `{"bad":1}`, "fix it", `{"still-bad":2}`, "fix it again"}
	if len(third) != len(want) {
		t.Fatalf("corrective history = %+v", third)
	}
	for i, content := range want {
		if third[i].Content != content {
			t.Fatalf("history[%d] = %+v, want %q", i, third[i], content)
		}
	}
}

func TestMacRelayRegistryWiring(t *testing.T) {
	file := &config.ProviderConfigFile{
		Version: config.ProviderConfigVersion,
		Providers: []config.ProviderEntry{{
			ID: "mac-omlx", Label: "Mac", EndpointLabel: "Relay",
			Type: config.ProviderTypeMacRelay, Enabled: true, RequestTimeoutSeconds: 600,
		}},
	}
	if _, err := NewRegistry(file, "codex", nil); err == nil {
		t.Fatal("enabled mac_relay without an executor must fail")
	}
	executor := &fakeRelayExecutor{models: []Model{{ID: "qwen-test"}}}
	registry, err := NewRegistry(file, "codex", nil, executor)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := registry.Provider("mac-omlx")
	if !ok {
		t.Fatal("mac_relay instance missing")
	}
	if provider.Descriptor().Type != ProviderTypeMacRelay || provider.Descriptor().RequestTimeoutMS != 600000 {
		t.Fatalf("descriptor = %+v", provider.Descriptor())
	}
	if _, err := provider.OpenSession(context.Background(), relayTestBinding()); err != nil {
		t.Fatalf("open session = %v", err)
	}
	wrong := relayTestBinding()
	wrong.ProviderType = ProviderTypeOpenAICompatible
	if _, err := provider.OpenSession(context.Background(), wrong); err == nil {
		t.Fatal("wrong binding type accepted")
	}
}

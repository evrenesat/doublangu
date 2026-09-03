package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"doublangu/internal/config"
	"doublangu/internal/pipeline"
)

func canonicalOmlxOptions() (json.RawMessage, error) {
	return config.CanonicalizeProviderOptions(ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0,"max_output_tokens":16384}`))
}

func pipelineOptionsHash(options json.RawMessage) (string, error) {
	return pipeline.OptionsHashOf(options)
}

func omlxProvider(serverURL string, timeout time.Duration) *openAICompatibleProvider {
	return &openAICompatibleProvider{
		descriptor: ProviderDescriptor{ID: "mac-omlx", Type: ProviderTypeOpenAICompatible, Enabled: true},
		baseURL:    serverURL, apiKey: "super-secret-value", timeout: timeout,
		client: newOpenAIHTTPClient(timeout),
	}
}

func omlxBinding(t *testing.T) ResolvedBinding {
	t.Helper()
	options, err := canonicalOmlxOptions()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := pipelineOptionsHash(options)
	if err != nil {
		t.Fatal(err)
	}
	return ResolvedBinding{
		StageID: "translation", ProviderID: "mac-omlx", ProviderType: ProviderTypeOpenAICompatible,
		ConfigFingerprint: "fp", ModelID: "omlx-model", Options: options, OptionsHash: hash,
		ContractVersion: "reader.translation.v1", PromptVersion: "reader-translation-prompt.v1",
	}
}

func chatCompletion(body string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-1", "model": "omlx-model",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": body},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}

func TestOpenAICompatibleCatalogAndChat(t *testing.T) {
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"id": "omlx-model", "object": "model", "owned_by": "omlx"},
			}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			_ = json.NewEncoder(w).Encode(chatCompletion(`{"version":"reader.translation.v1","ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	provider := omlxProvider(server.URL, 5*time.Second)
	models, err := provider.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "omlx-model" {
		t.Fatalf("catalog = %+v err=%v", models, err)
	}
	session, err := provider.OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	completion, err := session.Turn(context.Background(), TurnRequest{Prompt: "translate", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != `{"version":"reader.translation.v1","ok":true}` || completion.RequestID != "chatcmpl-1" {
		t.Fatalf("completion = %+v", completion)
	}
	if !strings.Contains(completion.UsageJSON, `"total_tokens":15`) {
		t.Fatalf("usage = %s", completion.UsageJSON)
	}
	// Authorization and JSON content-type on every request.
	for _, request := range requests {
		if request.Header.Get("Authorization") != "Bearer super-secret-value" {
			t.Fatalf("auth header = %q", request.Header.Get("Authorization"))
		}
	}
	// Secret never appears in a Completion.
	if strings.Contains(completion.ProviderMetadataJSON+completion.UsageJSON+completion.TimingJSON, "super-secret") {
		t.Fatal("secret leaked into completion metadata")
	}
}

func TestOpenAICompatibleCorrectionKeepsHistory(t *testing.T) {
	var mu sync.Mutex
	var lastBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		lastBody = body
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(chatCompletion(`{"version":"reader.translation.v1","ok":true}`))
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "translate this", OutputSchema: json.RawMessage(`{"type":"object"}`)}); err != nil {
		t.Fatal(err)
	}
	corrective, ok := session.(*openAICompatibleSession)
	if !ok {
		t.Fatal("session type")
	}
	if _, err := corrective.CompleteWithCorrection(context.Background(), "fix it", json.RawMessage(`{"type":"object"}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	messages, ok := lastBody["messages"].([]any)
	if !ok || len(messages) < 3 {
		t.Fatalf("messages = %#v", lastBody["messages"])
	}
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		entry, ok := message.(map[string]any)
		if !ok {
			t.Fatal("message shape")
		}
		roles = append(roles, fmt.Sprint(entry["role"]))
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("roles = %v", roles)
	}
}

// TestOpenAICompatibleTurnToTurnKeepsHistory proves the executor-style
// corrective loop (repeated Session.Turn calls on one session) retains the
// full conversation: the second request must carry the original user
// instructions, the rejected assistant artifact, and the corrective prompt.
func TestOpenAICompatibleTurnToTurnKeepsHistory(t *testing.T) {
	var mu sync.Mutex
	var lastBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		lastBody = body
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(chatCompletion(`{"version":"reader.translation.v1","ok":true}`))
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object"}`)
	if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "translate this paragraph", OutputSchema: schema}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "repair the rejected artifact", OutputSchema: schema}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	messages, ok := lastBody["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("second request messages = %#v", lastBody["messages"])
	}
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		entry, ok := message.(map[string]any)
		if !ok {
			t.Fatal("message shape")
		}
		roles = append(roles, fmt.Sprint(entry["role"]))
		content := fmt.Sprint(entry["content"])
		if entry["role"] == "assistant" && !strings.Contains(content, "reader.translation.v1") {
			t.Fatalf("assistant artifact missing from history: %q", content)
		}
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("roles = %v", roles)
	}
	if _, ok := lastBody["response_format"]; !ok {
		t.Fatal("output schema missing from the corrective request")
	}
}

func TestOpenAICompatibleFailuresClassifyAndRedact(t *testing.T) {
	// 401 with a body echoing the secret must classify auth and redact.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("invalid api key: super-secret-value"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err == nil {
		t.Fatal("auth failure not surfaced")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeNotAuthenticated {
		t.Fatalf("auth error = %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("secret leaked into error: %v", err)
	}
}

func TestOpenAICompatibleMalformedAndLimits(t *testing.T) {
	// Envelope above the 2 MiB bound.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxResponseEnvelopeBytes+1))
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err == nil || !strings.Contains(err.Error(), "exceeds the accepted limit") {
		t.Fatalf("oversize error = %v", err)
	}
	// Malformed envelope JSON.
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices": [`))
	}))
	defer server.Close()
	session, err = omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err == nil {
		t.Fatal("malformed envelope accepted")
	}
	// Refusal-style finish reason.
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-r", "model": "omlx-model",
			"choices": []any{map[string]any{"finish_reason": "content_filter", "message": map[string]any{"content": "nope"}}},
		})
	}))
	defer server.Close()
	session, err = omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err == nil || !strings.Contains(err.Error(), "finish_reason") {
		t.Fatalf("refusal error = %v", err)
	}
}

func TestOpenAICompatibleTimeoutClassifies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(chatCompletion("{}"))
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 30*time.Millisecond).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeTimeout {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestOpenAICompatibleRedirectsAreNotFollowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example/v1/chat/completions", http.StatusFound)
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err == nil {
		t.Fatal("redirect silently followed")
	}
}

func TestOMLXTransportThroughLinguisticExecutor(t *testing.T) {
	chunk := executorChunk(t)
	valid := validLinguisticRaw(t, chunk)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			_ = json.NewEncoder(w).Encode(chatCompletion(valid))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	provider := omlxProvider(server.URL, 5*time.Second)
	validated, result, err := ExecuteLinguisticStage(context.Background(), provider, omlxBinding(t), chunk)
	if err != nil {
		t.Fatalf("omlx executor: %v", err)
	}
	if validated == nil || len(validated.Tokens) != len(chunk.Tokens) {
		t.Fatalf("validated = %+v", validated)
	}
	if len(result.Turns) != 1 || result.Turns[0].Status != "completed" {
		t.Fatalf("turns = %+v", result.Turns)
	}
	if !strings.Contains(result.UsageJSON, "total_tokens") {
		t.Fatalf("usage missing: %q", result.UsageJSON)
	}
}

func TestOpenAICatalogRedactsEchoedKey(t *testing.T) {
	// A /models 401 echoing the resolved key must classify auth without
	// leaking the key or the echoed body into the error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid token: super-secret-value"))
	}))
	defer server.Close()
	_, err := omlxProvider(server.URL, 5*time.Second).ListModels(context.Background())
	if err == nil {
		t.Fatal("auth failure not surfaced")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeNotAuthenticated {
		t.Fatalf("auth error = %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-value") || strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("secret or body leaked into catalog error: %v", err)
	}
}

func closedPortURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()
	return "http://" + address
}

func TestOpenAITransportErrorsHideEndpointURL(t *testing.T) {
	endpoint := closedPortURL(t)
	provider := omlxProvider(endpoint, 5*time.Second)
	if _, err := provider.ListModels(context.Background()); err == nil {
		t.Fatal("unreachable catalog accepted")
	} else if strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("endpoint URL leaked into catalog error: %v", err)
	}
	session, err := provider.OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)}); err == nil {
		t.Fatal("unreachable turn accepted")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != CodeUnavailable {
			t.Fatalf("transport error = %v", err)
		}
		if strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("endpoint URL leaked into turn error: %v", err)
		}
	}
}

func TestOpenAIReportedModelUsesEnvelopeValue(t *testing.T) {
	envelope := func(model string) map[string]any {
		body := chatCompletion(`{"version":"reader.translation.v1","ok":true}`)
		if model == "" {
			delete(body, "model")
		} else {
			body["model"] = model
		}
		return body
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope("served-concrete-model"))
	}))
	defer server.Close()
	session, err := omlxProvider(server.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	completion, err := session.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if completion.ReportedModel != "served-concrete-model" {
		t.Fatalf("reported model = %q, want the envelope value", completion.ReportedModel)
	}

	omitted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope(""))
	}))
	defer omitted.Close()
	omittedSession, err := omlxProvider(omitted.URL, 5*time.Second).OpenSession(context.Background(), omlxBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	defer omittedSession.Close()
	omittedCompletion, err := omittedSession.Turn(context.Background(), TurnRequest{Prompt: "x", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if omittedCompletion.ReportedModel != "" {
		t.Fatalf("omitted model reported = %q, want empty", omittedCompletion.ReportedModel)
	}
}

func TestOpenAIProviderReusesBoundedTransport(t *testing.T) {
	provider := omlxProvider("http://127.0.0.1:9", 5*time.Second)
	if provider.client == nil {
		t.Fatal("provider has no owned HTTP client")
	}
	transport, ok := provider.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", provider.client.Transport)
	}
	if transport.MaxIdleConnsPerHost <= 0 || transport.IdleConnTimeout <= 0 {
		t.Fatalf("unbounded idle connections: %+v", transport)
	}
	other := omlxProvider("http://127.0.0.1:9", 5*time.Second)
	if other.client == provider.client {
		t.Fatal("providers share one transport instead of owning their own")
	}
}

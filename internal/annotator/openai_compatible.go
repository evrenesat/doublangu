package annotator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Bounds for the OpenAI-compatible transport.
const (
	maxResponseEnvelopeBytes = 2 << 20  // 2 MiB
	maxAssistantContentBytes = 1 << 20  // 1 MiB
	maxMetadataBytes         = 64 << 10 // 64 KiB
)

// openAICompatibleProvider is the bounded synchronous Chat Completions
// transport. The resolved secret lives only in this instance, and the HTTP
// client (with its connection pool) is owned by the instance so every
// catalog and completion call reuses one transport instead of leaking one
// idle connection per turn.
type openAICompatibleProvider struct {
	descriptor ProviderDescriptor
	baseURL    string
	apiKey     string
	timeout    time.Duration
	client     *http.Client
}

func (p *openAICompatibleProvider) Descriptor() ProviderDescriptor { return p.descriptor }

// ListModels fetches GET <base>/models and maps the OpenAI-style catalog.
func (p *openAICompatibleProvider) ListModels(ctx context.Context) ([]Model, error) {
	endpoint, err := p.join("models")
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Err: err}
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, classifyHTTPTransport(ctx, err)
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxResponseEnvelopeBytes)
	if err != nil {
		return nil, &Error{Code: CodeProtocol, Err: err}
	}
	if response.StatusCode != http.StatusOK {
		// The response body is never copied into the error: remote bodies
		// may carry hostnames, paths, traces, or third-party secrets.
		return nil, classifyHTTPStatus(response.StatusCode)
	}
	var envelope struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, &Error{Code: CodeProtocol, Err: fmt.Errorf("malformed model catalog: %w", err)}
	}
	models := make([]Model, 0, len(envelope.Data))
	for _, entry := range envelope.Data {
		if entry.ID == "" {
			continue
		}
		model := Model{ID: entry.ID, DisplayName: entry.ID}
		if entry.ID != "" && entry.OwnedBy != "" {
			model.DisplayName = entry.OwnedBy + " / " + entry.ID
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("model catalog returned no models")}
	}
	return models, nil
}

func (p *openAICompatibleProvider) OpenSession(ctx context.Context, binding ResolvedBinding) (Session, error) {
	if binding.ProviderType != ProviderTypeOpenAICompatible {
		return nil, fmt.Errorf("provider %q is not openai-compatible", binding.ProviderID)
	}
	var options configOpenAIOptions
	if err := decodeOpenAIOptions(binding.Options, &options); err != nil {
		return nil, err
	}
	return &openAICompatibleSession{
		provider: p, binding: binding, options: options,
		messages: make([]map[string]any, 0, 8),
	}, nil
}

func (p *openAICompatibleProvider) join(pathValue string) (string, error) {
	base, err := url.Parse(strings.TrimSuffix(p.baseURL, "/"))
	if err != nil {
		return "", err
	}
	copy := *base
	copy.Path = strings.TrimSuffix(copy.Path, "/") + "/" + strings.TrimPrefix(pathValue, "/")
	return copy.String(), nil
}

// newOpenAIHTTPClient builds the one transport a provider instance reuses
// for catalog and completion calls, with bounded idle connections so a busy
// article cannot accumulate one idle connection per turn.
func newOpenAIHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	// No automatic cross-host redirects: the request must stay on the
	// configured origin.
	transport.Proxy = http.ProxyFromEnvironment
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// providerStatusPhrase is the allowlisted human phrase for one HTTP failure
// class. Error text carries only this phrase and the numeric status: response
// bodies never reach errors, persistence, or browser state.
func providerStatusPhrase(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication failed"
	case http.StatusNotFound:
		return "resource not found"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "request timed out"
	case http.StatusBadRequest:
		return "request rejected"
	case http.StatusTooManyRequests:
		return "rate limited"
	}
	if status >= 500 {
		return "server error"
	}
	return "request failed"
}

// openAICompatibleSession preserves the message history across separate HTTP
// requests so corrective turns see their own rejected output.
type openAICompatibleSession struct {
	provider *openAICompatibleProvider
	binding  ResolvedBinding
	options  configOpenAIOptions
	messages []map[string]any
}

func (s *openAICompatibleSession) Turn(ctx context.Context, request TurnRequest) (Completion, error) {
	// Corrections happen through repeated Turn calls in the same session;
	// the executor sends a compact corrective prompt and expects the full
	// conversation (its own previous instructions and rejected artifact) to
	// precede it. The schema travels with every request, so retaining the
	// history never leaks a schema-less follow-up.
	result, err := s.complete(ctx, request.Prompt, request.OutputSchema, true)
	if err != nil {
		return Completion{}, err
	}
	// Keep the history: the original user instructions plus the assistant
	// artifact precede any corrective user message sent by the executor.
	s.messages = append(s.messages,
		map[string]any{"role": "user", "content": request.Prompt},
		map[string]any{"role": "assistant", "content": result.content},
	)
	return Completion{
		Text: result.content, ReportedModel: result.reportedModel,
		RequestID: result.requestID, FinishReason: result.finishReason,
		UsageJSON: result.usageJSON, TimingJSON: result.timingJSON,
		ProviderMetadataJSON: result.metadataJSON,
	}, nil
}

// CompleteWithCorrection sends a corrective user message after the rejected
// assistant text and returns the new assistant content plus metadata.
func (s *openAICompatibleSession) CompleteWithCorrection(ctx context.Context, correctionPrompt string, schema json.RawMessage) (string, error) {
	s.messages = append(s.messages, map[string]any{"role": "user", "content": correctionPrompt})
	result, err := s.complete(ctx, "", schema, true)
	if err != nil {
		return "", err
	}
	s.messages = append(s.messages, map[string]any{"role": "assistant", "content": result.content})
	return result.content, nil
}

type chatCompletionResult struct {
	content, requestID, finishReason, usageJSON, timingJSON, metadataJSON string
	// reportedModel is the concrete model the provider reports in the
	// response envelope, empty only when the provider omits it.
	reportedModel string
}

// maxReportedModelBytes bounds the provider-reported model identity kept in
// stage provenance.
const maxReportedModelBytes = 256

func (s *openAICompatibleSession) complete(ctx context.Context, prompt string, schema json.RawMessage, keepHistory bool) (chatCompletionResult, error) {
	// Rebuild the body each time: system prompt, original user prompt,
	// history of assistant artifacts and corrective messages, then the new
	// request. The executor keeps corrections in this same session.
	var messages []map[string]any
	if keepHistory {
		messages = append(messages, s.messages...)
	} else {
		s.messages = nil
	}
	if prompt != "" {
		messages = append(messages, map[string]any{"role": "user", "content": prompt})
	}
	options := s.options
	body := map[string]any{
		"model":       s.binding.ModelID,
		"messages":    messages,
		"stream":      false,
		"max_tokens":  options.MaxOutputTokens,
		"temperature": float64(options.TemperatureMilli) / 1000.0,
	}
	if len(schema) > 0 {
		var schemaObject map[string]any
		if err := json.Unmarshal(schema, &schemaObject); err != nil {
			return chatCompletionResult{}, &Error{Code: CodeProtocol, Err: fmt.Errorf("invalid output schema: %w", err)}
		}
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "doublangu_stage_artifact",
				"strict": true,
				"schema": schemaObject,
			},
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return chatCompletionResult{}, &Error{Code: CodeProtocol, Err: err}
	}
	endpoint, err := s.provider.join("chat/completions")
	if err != nil {
		return chatCompletionResult{}, &Error{Code: CodeUnavailable, Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return chatCompletionResult{}, &Error{Code: CodeUnavailable, Err: err}
	}
	request.Header.Set("Authorization", "Bearer "+s.provider.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.provider.client.Do(request)
	if err != nil {
		return chatCompletionResult{}, classifyHTTPTransport(ctx, err)
	}
	defer response.Body.Close()
	raw, err := readBounded(response.Body, maxResponseEnvelopeBytes)
	if err != nil {
		return chatCompletionResult{}, &Error{Code: CodeProtocol, Err: err}
	}
	if response.StatusCode != http.StatusOK {
		return chatCompletionResult{}, classifyHTTPStatus(response.StatusCode)
	}
	var envelope struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		// OMLX timing extensions and any other fields are tolerated by
		// decoding only known fields above.
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return chatCompletionResult{}, &Error{Code: CodeProtocol, Err: fmt.Errorf("malformed chat completion envelope: %w", err)}
	}
	if len(envelope.Choices) != 1 {
		return chatCompletionResult{}, &Error{Code: CodeInvalidOutput, Err: fmt.Errorf("expected exactly one choice, got %d", len(envelope.Choices))}
	}
	choice := envelope.Choices[0]
	if choice.Message.Content == "" {
		return chatCompletionResult{}, &Error{Code: CodeInvalidOutput, Err: errors.New("assistant content is empty")}
	}
	if len(choice.Message.Content) > maxAssistantContentBytes {
		return chatCompletionResult{}, &Error{Code: CodeProtocol, Err: errors.New("assistant content exceeds the accepted limit")}
	}
	switch choice.FinishReason {
	case "stop", "":
	default:
		return chatCompletionResult{}, &Error{Code: CodeInvalidOutput, Err: fmt.Errorf("unexpected finish_reason %q", choice.FinishReason)}
	}
	var envelopeMap map[string]json.RawMessage
	_ = json.Unmarshal(raw, &envelopeMap)
	timing := map[string]any{}
	for _, key := range []string{"model_load_duration", "time_to_first_token", "prompt_eval_duration", "generation_duration", "total_time"} {
		if value, ok := envelopeMap[key]; ok {
			var number float64
			if err := json.Unmarshal(value, &number); err == nil {
				timing[key] = number
			}
		}
	}
	timingJSON := ""
	if len(timing) > 0 {
		if encoded, err := json.Marshal(timing); err == nil {
			timingJSON = string(encoded)
		}
	}
	usageJSON, _ := json.Marshal(envelope.Usage)
	metadata, _ := json.Marshal(map[string]any{
		"request_id": envelope.ID, "model": envelope.Model, "finish_reason": choice.FinishReason,
	})
	reportedModel := envelope.Model
	if len(reportedModel) > maxReportedModelBytes {
		reportedModel = reportedModel[:maxReportedModelBytes]
	}
	return chatCompletionResult{
		content: choice.Message.Content, requestID: envelope.ID, finishReason: choice.FinishReason,
		usageJSON: string(usageJSON), timingJSON: timingJSON, metadataJSON: string(metadata),
		reportedModel: reportedModel,
	}, nil
}

func (s *openAICompatibleSession) Close() error {
	s.messages = nil
	return nil
}

func decodeOpenAIOptions(raw json.RawMessage, target *configOpenAIOptions) error {
	if len(raw) == 0 {
		return errors.New("openai-compatible options are required")
	}
	var options configOpenAIOptions
	if err := json.Unmarshal(raw, &options); err != nil {
		return &Error{Code: CodeInvalidInput, Err: err}
	}
	if options.TemperatureMilli < 0 || options.TemperatureMilli > 2000 {
		return &Error{Code: CodeInvalidInput, Err: fmt.Errorf("temperature_milli must be 0..2000")}
	}
	if options.MaxOutputTokens < 1024 || options.MaxOutputTokens > 65536 {
		return &Error{Code: CodeInvalidInput, Err: fmt.Errorf("max_output_tokens must be 1024..65536")}
	}
	*target = options
	return nil
}

// configOpenAIOptions mirrors config.OpenAICompatibleOptions without the
// import so the annotator package stays dependency-light for the session.
type configOpenAIOptions struct {
	TemperatureMilli int `json:"temperature_milli"`
	MaxOutputTokens  int `json:"max_output_tokens"`
}

func readBounded(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("response body exceeds the accepted limit")
	}
	return data, nil
}

func classifyHTTPTransport(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Err: errors.New("provider request timed out")}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Code: CodeProviderFailure, Err: ctx.Err()}
	}
	var timeoutError net.Error
	if errors.As(err, &timeoutError) && timeoutError.Timeout() {
		return &Error{Code: CodeTimeout, Err: errors.New("provider request timed out")}
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		// The wrapped error string carries the configured request URL, so
		// it must never reach persistence or the browser: map it to a
		// stable message while preserving the unavailable code.
		return &Error{Code: CodeUnavailable, Err: errors.New("provider endpoint unreachable")}
	}
	return &Error{Code: CodeProviderFailure, Err: err}
}

func classifyHTTPStatus(status int) error {
	code := CodeProviderFailure
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = CodeNotAuthenticated
	case http.StatusNotFound:
		code = CodeUnavailable
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		code = CodeTimeout
	case http.StatusBadRequest:
		code = CodeInvalidOutput
	}
	return &Error{Code: code, Err: fmt.Errorf("provider returned HTTP %d: %s", status, providerStatusPhrase(status))}
}

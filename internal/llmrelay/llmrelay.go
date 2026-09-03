// Package llmrelay owns the server side of the Mac LLM relay: the strict
// `llm.relay.v1` wire contract, request hashing, durable enqueue/wait, relay
// worker availability, and atomic result persistence. Relay jobs never carry
// a URL or credential; the Mac chooses the destination from local config.
// The database is the authority for completion; there is no in-process
// waiter registry.
package llmrelay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

// Shared wire constants (handoff §5.1).
const (
	ProtocolVersion = "speech-worker.v1"
	JobType         = "llm.relay.v1"
	OwnerType       = "llm_relay"

	OperationChatCompletion = "chat_completion"
	OperationListModels     = "list_models"

	PayloadLimitBytes    = 2 << 20 // 2 MiB
	ResultLimitBytes     = 2 << 20 // 2 MiB
	ContentLimitBytes    = 1 << 20 // 1 MiB
	MaxCompletionBytes   = 2 << 20 // 2 MiB
	MaxIdentifierBytes   = 256
	MaxMessages          = 16
	MaxModels            = 4096
	MaxStructuredBytes   = 64 << 10 // 64 KiB per canonical usage/timing JSON
	ResponseSchemaName   = "doublangu_stage_artifact"
	RequestHashDomain    = "doublangu.llm-relay-request.v1"
	RelayCapabilityBytes = 2 << 20
)

// Relay worker failure codes (handoff §5.7). The server rejects any other
// relay failure code.
const (
	CodeUnreachable     = "v1.relay_unreachable"
	CodeAuth            = "v1.relay_auth"
	CodeInvalidResponse = "v1.relay_invalid_response"
	CodeModelUnknown    = "v1.relay_model_unknown"
	CodeCanceled        = "v1.relay_canceled"
	// CodeParentCanceled is recorded when the server cancels a relay child
	// job because its parent provider context ended.
	CodeParentCanceled = "v1.relay_parent_canceled"
	// CodeNondeterministic is recorded when a second completion carries
	// different bytes than the accepted result.
	CodeNondeterministic = "v1.relay_nondeterministic_result"
	// CodeUnavailable reports fail-fast unavailability: no relay-capable
	// worker is currently present.
	CodeUnavailable = "v1.relay_unavailable"
)

// Known OMLX timing keys. Unknown timing keys are rejected.
var knownTimingKeys = []string{
	"model_load_duration",
	"time_to_first_token",
	"prompt_eval_duration",
	"generation_duration",
	"total_time",
}

// Error carries a stable relay code for terminal relay outcomes.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e == nil || e.Code == "" {
		return "llmrelay error"
	}
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CodeOf returns the stable relay code for an error.
func CodeOf(err error) string {
	var typed *Error
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	return ""
}

// Message is one relay chat message. Roles are only user or assistant.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat is the fixed structured-output wrapper. Schema must be a
// JSON object.
type ResponseFormat struct {
	Type       string `json:"type"`
	JSONSchema struct {
		Name   string          `json:"name"`
		Strict bool            `json:"strict"`
		Schema json.RawMessage `json:"schema"`
	} `json:"json_schema"`
}

// Options are the per-turn generation bounds.
type Options struct {
	TemperatureMilli int `json:"temperature_milli"`
	MaxOutputTokens  int `json:"max_output_tokens"`
}

// Limits carries the relay byte bound.
type Limits struct {
	MaxCompletionBytes int64 `json:"max_completion_bytes"`
}

// ChatCompletionRequest is the validated `chat_completion` payload.
type ChatCompletionRequest struct {
	ProtocolVersion string         `json:"protocol_version"`
	Operation       string         `json:"operation"`
	RequestID       string         `json:"request_id"`
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	ResponseFormat  ResponseFormat `json:"response_format"`
	Options         Options        `json:"options"`
	Limits          Limits         `json:"limits"`
}

// ListModelsRequest is the validated `list_models` payload.
type ListModelsRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Operation       string `json:"operation"`
	RequestID       string `json:"request_id"`
	Limits          Limits `json:"limits"`
}

// Usage carries non-negative token counts.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ChatResult is the validated `chat_completion` result.
type ChatResult struct {
	RequestID         string         `json:"request_id"`
	Content           string         `json:"content"`
	ReportedModel     string         `json:"reported_model"`
	ProviderRequestID string         `json:"provider_request_id"`
	FinishReason      string         `json:"finish_reason"`
	Usage             Usage          `json:"usage"`
	Timing            map[string]any `json:"-"`
	// UsageJSON and TimingJSON are the canonical bounded encodings.
	UsageJSON  string `json:"-"`
	TimingJSON string `json:"-"`
}

// ListModelsResult is the validated `list_models` result.
type ListModelsResult struct {
	RequestID string   `json:"request_id"`
	Models    []string `json:"models"`
}

// RelayCapability is the single enrolled relay support entry.
type RelayCapability struct {
	MaxCompletionBytes int64 `json:"max_completion_bytes"`
}

// HashRequest computes the relay input hash over the exact payload bytes:
// SHA256("doublangu.llm-relay-request.v1" + NUL + exact_payload_bytes).
func HashRequest(payload []byte) string {
	h := sha256.New()
	h.Write([]byte(RequestHashDomain))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// IdempotencyKey returns the job idempotency key for an input hash.
func IdempotencyKey(inputHash string) string { return "llm.relay:" + inputHash }

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	return len(value) <= MaxIdentifierBytes
}

// DecodeChatCompletionRequest strictly decodes and validates a
// `chat_completion` payload.
func DecodeChatCompletionRequest(data []byte) (*ChatCompletionRequest, error) {
	var request ChatCompletionRequest
	if err := decodeStrict(data, &request); err != nil {
		return nil, fmt.Errorf("llmrelay decode chat_completion: %w", err)
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &request, nil
}

// Validate checks every `chat_completion` rule.
func (r *ChatCompletionRequest) Validate() error {
	if r == nil {
		return errors.New("llmrelay chat_completion request is required")
	}
	if r.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("llmrelay unsupported protocol %q", r.ProtocolVersion)
	}
	if r.Operation != OperationChatCompletion {
		return fmt.Errorf("llmrelay unsupported operation %q", r.Operation)
	}
	if !validIdentifier(r.RequestID) {
		return errors.New("llmrelay request_id must be 1..256 bytes")
	}
	if !validIdentifier(r.Model) {
		return errors.New("llmrelay model must be 1..256 bytes")
	}
	if len(r.Messages) < 1 || len(r.Messages) > MaxMessages {
		return fmt.Errorf("llmrelay messages must contain 1..%d entries", MaxMessages)
	}
	for i, message := range r.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return fmt.Errorf("llmrelay message %d has unsupported role %q", i, message.Role)
		}
		if strings.TrimSpace(message.Content) == "" || !utf8.ValidString(message.Content) {
			return fmt.Errorf("llmrelay message %d content must be non-empty valid UTF-8", i)
		}
	}
	if r.ResponseFormat.Type != "json_schema" {
		return errors.New("llmrelay response_format type must be json_schema")
	}
	if r.ResponseFormat.JSONSchema.Name != ResponseSchemaName || !r.ResponseFormat.JSONSchema.Strict {
		return fmt.Errorf("llmrelay response_format must name %q with strict true", ResponseSchemaName)
	}
	schema := bytes.TrimSpace(r.ResponseFormat.JSONSchema.Schema)
	if len(schema) == 0 || schema[0] != '{' || !json.Valid(schema) {
		return errors.New("llmrelay response_format schema must be a JSON object")
	}
	if r.Options.TemperatureMilli < 0 || r.Options.TemperatureMilli > 2000 {
		return fmt.Errorf("llmrelay temperature_milli must be 0..2000, got %d", r.Options.TemperatureMilli)
	}
	if r.Options.MaxOutputTokens < 1024 || r.Options.MaxOutputTokens > 65536 {
		return fmt.Errorf("llmrelay max_output_tokens must be 1024..65536, got %d", r.Options.MaxOutputTokens)
	}
	if err := r.Limits.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate checks the relay byte bound.
func (l Limits) Validate() error {
	if l.MaxCompletionBytes <= 0 || l.MaxCompletionBytes > MaxCompletionBytes {
		return fmt.Errorf("llmrelay max_completion_bytes must be positive and at most %d", MaxCompletionBytes)
	}
	return nil
}

// DecodeListModelsRequest strictly decodes and validates a `list_models`
// payload. Model, messages, response_format, and options are forbidden.
func DecodeListModelsRequest(data []byte) (*ListModelsRequest, error) {
	var raw map[string]json.RawMessage
	if err := decodeStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("llmrelay decode list_models: %w", err)
	}
	for _, forbidden := range []string{"model", "messages", "response_format", "options"} {
		if _, ok := raw[forbidden]; ok {
			return nil, fmt.Errorf("llmrelay list_models forbids %q", forbidden)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var request ListModelsRequest
	if err := decodeStrict(encoded, &request); err != nil {
		return nil, fmt.Errorf("llmrelay decode list_models: %w", err)
	}
	if request.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("llmrelay unsupported protocol %q", request.ProtocolVersion)
	}
	if request.Operation != OperationListModels {
		return nil, fmt.Errorf("llmrelay unsupported operation %q", request.Operation)
	}
	if !validIdentifier(request.RequestID) {
		return nil, errors.New("llmrelay request_id must be 1..256 bytes")
	}
	if err := request.Limits.Validate(); err != nil {
		return nil, err
	}
	return &request, nil
}

// DecodeChatResult strictly decodes and validates a `chat_completion`
// result against its request: the echoed request id must match, and the
// result must respect every hard bound.
func DecodeChatResult(data []byte, requestID string, maxBytes int64) (*ChatResult, error) {
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("llmrelay result exceeds the %d-byte job bound", maxBytes)
	}
	var raw struct {
		RequestID         string         `json:"request_id"`
		Content           string         `json:"content"`
		ReportedModel     string         `json:"reported_model"`
		ProviderRequestID string         `json:"provider_request_id"`
		FinishReason      string         `json:"finish_reason"`
		Usage             Usage          `json:"usage"`
		Timing            map[string]any `json:"timing"`
	}
	if err := decodeStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("llmrelay decode chat result: %w", err)
	}
	if raw.RequestID != requestID {
		return nil, errors.New("llmrelay result request_id does not match the relay request")
	}
	if raw.Content == "" || len(raw.Content) > ContentLimitBytes {
		return nil, fmt.Errorf("llmrelay result content must be 1..%d bytes", ContentLimitBytes)
	}
	if len(raw.ReportedModel) > MaxIdentifierBytes || len(raw.ProviderRequestID) > MaxIdentifierBytes {
		return nil, fmt.Errorf("llmrelay reported model and provider request id must be at most %d bytes", MaxIdentifierBytes)
	}
	if !utf8.ValidString(raw.ReportedModel) || !utf8.ValidString(raw.ProviderRequestID) {
		return nil, errors.New("llmrelay reported model and provider request id must be valid UTF-8")
	}
	switch raw.FinishReason {
	case "stop", "":
	default:
		return nil, fmt.Errorf("llmrelay unexpected finish_reason %q", raw.FinishReason)
	}
	if raw.Usage.PromptTokens < 0 || raw.Usage.CompletionTokens < 0 || raw.Usage.TotalTokens < 0 {
		return nil, errors.New("llmrelay usage token counts must be non-negative")
	}
	timing := make(map[string]any, len(raw.Timing))
	for key, value := range raw.Timing {
		known := false
		for _, allowed := range knownTimingKeys {
			if key == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("llmrelay unknown timing key %q", key)
		}
		number, ok := value.(float64)
		if !ok {
			return nil, fmt.Errorf("llmrelay timing key %q must be a JSON number", key)
		}
		if err := checkFinite(number); err != nil {
			return nil, fmt.Errorf("llmrelay timing key %q: %w", key, err)
		}
		timing[key] = number
	}
	usageJSON, err := json.Marshal(raw.Usage)
	if err != nil {
		return nil, err
	}
	timingJSON := ""
	if len(timing) > 0 {
		encoded, err := json.Marshal(timing)
		if err != nil {
			return nil, err
		}
		timingJSON = string(encoded)
	}
	if len(usageJSON) > MaxStructuredBytes || len(timingJSON) > MaxStructuredBytes {
		return nil, fmt.Errorf("llmrelay usage/timing JSON must each fit %d bytes", MaxStructuredBytes)
	}
	return &ChatResult{
		RequestID: raw.RequestID, Content: raw.Content, ReportedModel: raw.ReportedModel,
		ProviderRequestID: raw.ProviderRequestID, FinishReason: raw.FinishReason,
		Usage: raw.Usage, Timing: timing, UsageJSON: string(usageJSON), TimingJSON: timingJSON,
	}, nil
}

func checkFinite(value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return errors.New("must be a finite JSON number")
	}
	return nil
}

// CanonicalChatResult returns the deterministic canonical encoding of a
// validated chat result. The SHA-256 of these bytes is the stored result
// hash used for duplicate-completion comparison.
func CanonicalChatResult(result *ChatResult) ([]byte, error) {
	if result == nil {
		return nil, errors.New("llmrelay chat result is required")
	}
	var timing json.RawMessage
	if result.TimingJSON != "" {
		if !json.Valid([]byte(result.TimingJSON)) {
			return nil, errors.New("llmrelay timing JSON is invalid")
		}
		timing = json.RawMessage(result.TimingJSON)
	} else {
		timing = json.RawMessage("{}")
	}
	var usage json.RawMessage
	if result.UsageJSON != "" {
		if !json.Valid([]byte(result.UsageJSON)) {
			return nil, errors.New("llmrelay usage JSON is invalid")
		}
		usage = json.RawMessage(result.UsageJSON)
	} else {
		encoded, err := json.Marshal(result.Usage)
		if err != nil {
			return nil, err
		}
		usage = encoded
	}
	canonical, err := json.Marshal(struct {
		RequestID         string          `json:"request_id"`
		Content           string          `json:"content"`
		ReportedModel     string          `json:"reported_model"`
		ProviderRequestID string          `json:"provider_request_id"`
		FinishReason      string          `json:"finish_reason"`
		Usage             json.RawMessage `json:"usage"`
		Timing            json.RawMessage `json:"timing"`
	}{
		RequestID: result.RequestID, Content: result.Content,
		ReportedModel: result.ReportedModel, ProviderRequestID: result.ProviderRequestID,
		FinishReason: result.FinishReason, Usage: usage, Timing: timing,
	})
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// CanonicalListModelsResult returns the deterministic canonical encoding of
// a validated list_models result.
func CanonicalListModelsResult(result *ListModelsResult) ([]byte, error) {
	if result == nil {
		return nil, errors.New("llmrelay list_models result is required")
	}
	return json.Marshal(struct {
		RequestID string   `json:"request_id"`
		Models    []string `json:"models"`
	}{RequestID: result.RequestID, Models: result.Models})
}

// DecodeListModelsResult strictly decodes and validates a `list_models`
// result against its request.
func DecodeListModelsResult(data []byte, requestID string, maxBytes int64) (*ListModelsResult, error) {
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("llmrelay result exceeds the %d-byte job bound", maxBytes)
	}
	var raw struct {
		RequestID string   `json:"request_id"`
		Models    []string `json:"models"`
	}
	if err := decodeStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("llmrelay decode list_models result: %w", err)
	}
	if raw.RequestID != requestID {
		return nil, errors.New("llmrelay result request_id does not match the relay request")
	}
	if raw.Models == nil {
		return nil, errors.New("llmrelay models must be an array")
	}
	if len(raw.Models) > MaxModels {
		return nil, fmt.Errorf("llmrelay models must contain at most %d entries", MaxModels)
	}
	seen := make(map[string]bool, len(raw.Models))
	for _, model := range raw.Models {
		if !validIdentifier(model) {
			return nil, errors.New("llmrelay models must be non-empty strings of at most 256 bytes")
		}
		if seen[model] {
			return nil, fmt.Errorf("llmrelay duplicate model %q", model)
		}
		seen[model] = true
	}
	if raw.Models == nil {
		raw.Models = []string{}
	}
	return &ListModelsResult{RequestID: raw.RequestID, Models: raw.Models}, nil
}

// DecodeRelayCapabilities strictly decodes the optional relay capability
// list. Beta admits exactly zero or one entry.
func DecodeRelayCapabilities(data json.RawMessage) ([]RelayCapability, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var capabilities []RelayCapability
	if err := decodeStrict(data, &capabilities); err != nil {
		return nil, fmt.Errorf("llmrelay decode capabilities: %w", err)
	}
	if len(capabilities) > 1 {
		return nil, errors.New("llmrelay admits at most one relay capability")
	}
	for _, capability := range capabilities {
		if capability.MaxCompletionBytes != RelayCapabilityBytes {
			return nil, fmt.Errorf("llmrelay capability must advertise exactly %d max_completion_bytes", RelayCapabilityBytes)
		}
	}
	if capabilities == nil {
		return nil, nil
	}
	return capabilities, nil
}

// RelayCapabilitySubset reports whether every requested relay capability is
// covered by the enrolled capabilities.
func RelayCapabilitySubset(requested, enrolled []RelayCapability) bool {
	for _, want := range requested {
		covered := false
		for _, have := range enrolled {
			if want.MaxCompletionBytes > 0 && want.MaxCompletionBytes <= have.MaxCompletionBytes {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// ValidateFailure enforces the relay code/retry matrix: unknown codes are
// rejected, and only `v1.relay_unreachable` may request scheduler retry.
func ValidateFailure(code string, retry bool) error {
	switch code {
	case CodeUnreachable:
		return nil
	case CodeAuth, CodeInvalidResponse, CodeModelUnknown, CodeCanceled:
		if retry {
			return fmt.Errorf("llmrelay failure %s must not request retry", code)
		}
		return nil
	default:
		return fmt.Errorf("llmrelay unknown failure code %q", code)
	}
}

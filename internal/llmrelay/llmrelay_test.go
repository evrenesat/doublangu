package llmrelay

import (
	"encoding/json"
	"strings"
	"testing"

	"doublangu/internal/library"
)

func testMessages() []Message {
	return []Message{{Role: "user", Content: "Vertaal dit: de bank"}}
}

func testSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"translation":{"type":"string"}}}`)
}

func TestBuildChatCompletionRoundTrip(t *testing.T) {
	id := library.NewULID()
	payload, inputHash, err := BuildChatCompletion(id, "qwen-test", testMessages(), testSchema(), 0, 16384)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > PayloadLimitBytes {
		t.Fatalf("payload exceeds bound: %d", len(payload))
	}
	if inputHash != HashRequest(payload) {
		t.Fatal("input hash does not cover the exact payload bytes")
	}
	if IdempotencyKey(inputHash) != "llm.relay:"+inputHash {
		t.Fatal("idempotency key shape changed")
	}
	request, err := DecodeChatCompletionRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestID != id.String() || request.Model != "qwen-test" {
		t.Fatalf("round trip mismatch: %+v", request)
	}
}

func TestChatCompletionValidationMatrix(t *testing.T) {
	base := func() ChatCompletionRequest {
		return ChatCompletionRequest{
			ProtocolVersion: ProtocolVersion, Operation: OperationChatCompletion,
			RequestID: library.NewULID().String(), Model: "m",
			Messages: testMessages(),
			Options:  Options{TemperatureMilli: 0, MaxOutputTokens: 16384},
			Limits:   Limits{MaxCompletionBytes: MaxCompletionBytes},
		}
	}
	withFormat := func(r ChatCompletionRequest) ChatCompletionRequest {
		r.ResponseFormat.Type = "json_schema"
		r.ResponseFormat.JSONSchema.Name = ResponseSchemaName
		r.ResponseFormat.JSONSchema.Strict = true
		r.ResponseFormat.JSONSchema.Schema = testSchema()
		return r
	}
	cases := map[string]func(*ChatCompletionRequest){
		"bad protocol":      func(r *ChatCompletionRequest) { r.ProtocolVersion = "other.v9" },
		"bad operation":     func(r *ChatCompletionRequest) { r.Operation = "list_models" },
		"empty request id":  func(r *ChatCompletionRequest) { r.RequestID = "" },
		"empty model":       func(r *ChatCompletionRequest) { r.Model = "" },
		"no messages":       func(r *ChatCompletionRequest) { r.Messages = nil },
		"bad role":          func(r *ChatCompletionRequest) { r.Messages[0].Role = "system" },
		"empty content":     func(r *ChatCompletionRequest) { r.Messages[0].Content = "   " },
		"bad temperature":   func(r *ChatCompletionRequest) { r.Options.TemperatureMilli = 2001 },
		"bad max tokens":    func(r *ChatCompletionRequest) { r.Options.MaxOutputTokens = 128 },
		"bad limit":         func(r *ChatCompletionRequest) { r.Limits.MaxCompletionBytes = 0 },
		"oversized limit":   func(r *ChatCompletionRequest) { r.Limits.MaxCompletionBytes = MaxCompletionBytes + 1 },
		"wrong format type": func(r *ChatCompletionRequest) { r.ResponseFormat.Type = "json_object" },
		"wrong schema name": func(r *ChatCompletionRequest) { r.ResponseFormat.JSONSchema.Name = "other" },
		"non-strict":        func(r *ChatCompletionRequest) { r.ResponseFormat.JSONSchema.Strict = false },
		"array schema":      func(r *ChatCompletionRequest) { r.ResponseFormat.JSONSchema.Schema = json.RawMessage(`[]`) },
	}
	for name, mutate := range cases {
		request := withFormat(base())
		mutate(&request)
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeChatCompletionRequest(encoded); err == nil {
			t.Errorf("%s: expected validation failure", name)
		}
	}
	tooMany := withFormat(base())
	for i := 0; i < MaxMessages+1; i++ {
		tooMany.Messages = append(tooMany.Messages, Message{Role: "user", Content: "x"})
	}
	encoded, _ := json.Marshal(tooMany)
	if _, err := DecodeChatCompletionRequest(encoded); err == nil {
		t.Error("17 messages: expected validation failure")
	}
	// Unknown keys and trailing JSON are rejected.
	valid := withFormat(base())
	encoded, _ = json.Marshal(valid)
	withUnknown := strings.Replace(string(encoded), `"operation"`, `"operation","extra":1`, 1)
	if _, err := DecodeChatCompletionRequest([]byte(withUnknown)); err == nil {
		t.Error("unknown key: expected rejection")
	}
	if _, err := DecodeChatCompletionRequest(append(encoded, []byte(`{}`)...)); err == nil {
		t.Error("trailing JSON value: expected rejection")
	} else if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("trailing data: wrong error %v", err)
	}
}

func TestListModelsValidation(t *testing.T) {
	id := library.NewULID()
	payload, _, err := BuildListModels(id)
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeListModelsRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestID != id.String() {
		t.Fatal("request id mismatch")
	}
	for _, forbidden := range []string{"model", "messages", "response_format", "options"} {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(payload, &raw); err != nil {
			t.Fatal(err)
		}
		raw[forbidden] = json.RawMessage(`1`)
		encoded, _ := json.Marshal(raw)
		if _, err := DecodeListModelsRequest(encoded); err == nil {
			t.Errorf("list_models with %q: expected rejection", forbidden)
		}
	}
}

func TestChatResultValidation(t *testing.T) {
	requestID := library.NewULID().String()
	build := func() map[string]any {
		return map[string]any{
			"request_id": requestID, "content": `{"translation":"de bank"}`,
			"reported_model": "qwen", "provider_request_id": "chatcmpl-1",
			"finish_reason": "stop",
			"usage":         map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
			"timing":        map[string]any{"generation_duration": 1.5},
		}
	}
	encode := func(value map[string]any) []byte {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	result, err := DecodeChatResult(encode(build()), requestID, MaxCompletionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.PromptTokens != 1 || result.Timing["generation_duration"] != 1.5 {
		t.Fatalf("result mismatch: %+v", result)
	}
	canonical, err := CanonicalChatResult(result)
	if err != nil {
		t.Fatal(err)
	}
	again, err := CanonicalChatResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(again) {
		t.Fatal("canonical encoding is not deterministic")
	}
	failures := map[string]func(map[string]any){
		"wrong request id": func(m map[string]any) { m["request_id"] = "other" },
		"empty content":    func(m map[string]any) { m["content"] = "" },
		"bad finish":       func(m map[string]any) { m["finish_reason"] = "length" },
		"negative usage":   func(m map[string]any) { m["usage"] = map[string]any{"prompt_tokens": -1} },
		"unknown timing":   func(m map[string]any) { m["timing"] = map[string]any{"nope": 1.0} },
		"string timing":    func(m map[string]any) { m["timing"] = map[string]any{"generation_duration": "fast"} },
	}
	for name, mutate := range failures {
		value := build()
		mutate(value)
		if _, err := DecodeChatResult(encode(value), requestID, MaxCompletionBytes); err == nil {
			t.Errorf("%s: expected validation failure", name)
		}
	}
	oversized := strings.Repeat("x", ContentLimitBytes+1)
	value := build()
	value["content"] = oversized
	if _, err := DecodeChatResult(encode(value), requestID, ResultLimitBytes); err == nil {
		t.Error("oversized content: expected rejection")
	}
	longID := strings.Repeat("y", MaxIdentifierBytes+1)
	value = build()
	value["reported_model"] = longID
	if _, err := DecodeChatResult(encode(value), requestID, ResultLimitBytes); err == nil {
		t.Error("oversized reported_model: expected rejection")
	}
}

func TestListModelsResultValidation(t *testing.T) {
	requestID := library.NewULID().String()
	encode := func(models any) []byte {
		encoded, _ := json.Marshal(map[string]any{"request_id": requestID, "models": models})
		return encoded
	}
	result, err := DecodeListModelsResult(encode([]string{"a", "b"}), requestID, MaxCompletionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 2 {
		t.Fatal("model list mismatch")
	}
	// An empty model list is a valid transport result.
	empty, err := DecodeListModelsResult(encode([]string{}), requestID, MaxCompletionBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Models) != 0 {
		t.Fatal("empty list must stay empty")
	}
	if _, err := DecodeListModelsResult(encode([]string{"a", "a"}), requestID, MaxCompletionBytes); err == nil {
		t.Error("duplicate models: expected rejection")
	}
	if _, err := DecodeListModelsResult(encode([]string{""}), requestID, MaxCompletionBytes); err == nil {
		t.Error("empty model id: expected rejection")
	}
	if _, err := DecodeListModelsResult(encode(nil), requestID, MaxCompletionBytes); err == nil {
		t.Error("null models: expected rejection")
	}
	if _, err := DecodeListModelsResult(encode([]string{"a"}), "other", MaxCompletionBytes); err == nil {
		t.Error("wrong request id: expected rejection")
	}
}

func TestRelayCapabilityValidation(t *testing.T) {
	caps, err := DecodeRelayCapabilities(json.RawMessage(`[{"max_completion_bytes":2097152}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 1 {
		t.Fatal("capability mismatch")
	}
	if _, err := DecodeRelayCapabilities(json.RawMessage(`[]`)); err != nil {
		t.Fatal("zero capabilities must be valid")
	}
	if _, err := DecodeRelayCapabilities(json.RawMessage(`[{"max_completion_bytes":2097152},{"max_completion_bytes":2097152}]`)); err == nil {
		t.Error("two capabilities: expected rejection")
	}
	if _, err := DecodeRelayCapabilities(json.RawMessage(`[{"max_completion_bytes":1}]`)); err == nil {
		t.Error("wrong byte bound: expected rejection")
	}
	if !RelayCapabilitySubset(caps, caps) {
		t.Error("identical capabilities must be a subset")
	}
	if RelayCapabilitySubset([]RelayCapability{{MaxCompletionBytes: 1 << 20}}, nil) {
		t.Error("request against no enrollment must not be a subset")
	}
}

func TestFailureMatrix(t *testing.T) {
	if err := ValidateFailure(CodeUnreachable, true); err != nil {
		t.Fatal("unreachable may request retry")
	}
	if err := ValidateFailure(CodeUnreachable, false); err != nil {
		t.Fatal("unreachable without retry must pass")
	}
	for _, code := range []string{CodeAuth, CodeInvalidResponse, CodeModelUnknown, CodeCanceled} {
		if err := ValidateFailure(code, false); err != nil {
			t.Errorf("%s without retry must pass: %v", code, err)
		}
		if err := ValidateFailure(code, true); err == nil {
			t.Errorf("%s with retry must fail", code)
		}
	}
	if err := ValidateFailure("v1.something_else", false); err == nil {
		t.Error("unknown code: expected rejection")
	}
	if err := ValidateFailure("nope", false); err == nil {
		t.Error("non-v1 code: expected rejection")
	}
}

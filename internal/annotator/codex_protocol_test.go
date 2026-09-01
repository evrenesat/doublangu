package annotator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestProtocolRequestsMatchGeneratedAppServerShapes(t *testing.T) {
	var wire bytes.Buffer
	client := newProtocolClient(&wire, strings.NewReader(""))
	if err := client.send(1, "initialize", initializeParams{
		ClientInfo:   initializeClientInfo{Name: "doublangu", Version: "0.1.0"},
		Capabilities: &initializeCapabilities{ExperimentalAPI: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.send(2, "thread/start", threadStartParams{
		ApprovalPolicy: "never", Sandbox: "read-only", CWD: "/tmp/doublangu-codex",
		Ephemeral: true, DynamicTools: []any{},
	}); err != nil {
		t.Fatal(err)
	}
	schema, err := outputSchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.send(3, "turn/start", turnStartParams{
		ThreadID: "thread", Input: []textInput{{Type: "text", Text: "prompt"}}, Effort: "medium", OutputSchema: schema,
	}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(wire.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("wire lines = %d: %q", len(lines), wire.String())
	}
	var initialize map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialize); err != nil {
		t.Fatal(err)
	}
	if initialize["method"] != "initialize" || initialize["id"] != float64(1) {
		t.Fatalf("initialize = %#v", initialize)
	}
	var thread map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &thread); err != nil {
		t.Fatal(err)
	}
	params := thread["params"].(map[string]any)
	if params["ephemeral"] != true || params["approvalPolicy"] != "never" || params["sandbox"] != "read-only" || params["dynamicTools"].([]any) == nil {
		t.Fatalf("thread params = %#v", params)
	}
	var turn map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &turn); err != nil {
		t.Fatal(err)
	}
	turnParams := turn["params"].(map[string]any)
	if turnParams["threadId"] != "thread" || turnParams["input"].([]any) == nil || turnParams["outputSchema"] == nil {
		t.Fatalf("turn params = %#v", turnParams)
	}
}

func TestReadProtocolLineBoundsAndEOF(t *testing.T) {
	line, err := readProtocolLine(bufio.NewReader(strings.NewReader("hello\n")), 8)
	if err != nil || string(line) != "hello" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	if _, err := readProtocolLine(bufio.NewReader(strings.NewReader("123456789\n")), 8); err == nil {
		t.Fatal("oversized line accepted")
	}
	line, err = readProtocolLine(bufio.NewReader(strings.NewReader("eof")), 8)
	if err != nil || string(line) != "eof" {
		t.Fatalf("EOF line=%q err=%v", line, err)
	}
	_, err = readProtocolLine(bufio.NewReader(strings.NewReader("")), 8)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("empty EOF error = %v", err)
	}
}

func TestRunTurnRejectsToolItemsAndApprovalRequests(t *testing.T) {
	for name, event := range map[string]string{
		"tool item":        `{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"item","type":"commandExecution","text":""}}}`,
		"approval request": `{"method":"item/commandExecution/requestApproval","params":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			stream := `{"id":1,"result":{"turn":{"id":"turn"}}}` + "\n" + event + "\n"
			client := newProtocolClient(io.Discard, strings.NewReader(stream))
			err := error(nil)
			_, err = client.runTurn(context.Background(), 1, "thread", "prompt", "medium", "", []byte(`{}`))
			var typed *Error
			if err == nil || !strings.Contains(err.Error(), "unsupported") || !errors.As(err, &typed) || typed.Code != CodeProtocol {
				t.Fatalf("runTurn error = %v", err)
			}
		})
	}
}

func TestDecodeCandidatePayloadStrictlyValidatesShapeAndOccurrence(t *testing.T) {
	input := ArticleInput{Title: "Test", SourceLanguage: "nl", TargetLanguage: "en", Blocks: []ArticleInputBlock{{BlockIndex: 0, SourceText: "Ik wil tot rust komen."}}}
	valid := `{"annotations":[{"block_index":0,"source_text":"tot rust komen","occurrence":0,"kind":"expression","learning_key":"tot rust komen","primary_translation":"to calm down","alternatives":["to settle down"],"literal_translation":"to come to rest","meaning_note":"Become calm.","usage_note":"After stress.","parts_note":"tot rust + komen","suggest_shadow":true}]}`
	candidates, err := decodeCandidatePayload(input, valid)
	if err != nil || len(candidates) != 1 || candidates[0].Kind != "expression" {
		t.Fatalf("valid candidates=%+v err=%v", candidates, err)
	}
	for name, response := range map[string]string{
		"additional property": strings.Replace(valid, `}]}`, `,"extra":"no"}]}`, 1),
		"missing field":       strings.Replace(valid, `,"suggest_shadow":true`, "", 1),
		"wrong occurrence":    strings.Replace(valid, `"occurrence":0`, `"occurrence":2`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCandidatePayload(input, response); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestDisabledAnnotatorReturnsStableUnavailableCode(t *testing.T) {
	_, err := (Disabled{}).Annotate(context.Background(), ArticleInput{})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeUnavailable {
		t.Fatalf("error=%v", err)
	}
}

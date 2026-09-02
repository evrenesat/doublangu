package annotator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"doublangu/internal/semantics"
)

type scriptedAppServer struct {
	mu         sync.Mutex
	output     chan []byte
	stdout     *io.PipeWriter
	turnParams []map[string]any
	turnNumber int
	validJSON  string
	validAfter int
}

type scriptedAppServerInput struct {
	server *scriptedAppServer
	once   sync.Once
}

func (s *scriptedAppServerInput) Write(data []byte) (int, error) {
	var request struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	var params map[string]any
	_ = json.Unmarshal(request.Params, &params)
	s.server.mu.Lock()
	if request.Method == "turn/start" {
		s.server.turnNumber++
		s.server.turnParams = append(s.server.turnParams, params)
	}
	turnNumber := s.server.turnNumber
	s.server.mu.Unlock()
	switch request.Method {
	case "initialize":
		s.server.emit(map[string]any{"id": request.ID, "result": map[string]any{}})
	case "thread/start":
		s.server.emit(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
	case "turn/start":
		turnID := fmt.Sprintf("turn-%d", turnNumber)
		s.server.emit(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": turnID}}})
		text := `{"version":"wrong"}`
		if turnNumber >= s.server.validAfter {
			text = s.server.validJSON
		}
		s.server.emit(map[string]any{"method": "item/completed", "params": map[string]any{
			"threadId": "thread-1", "turnId": turnID, "item": map[string]any{"type": "agentMessage", "text": text},
		}})
		s.server.emit(map[string]any{"method": "turn/completed", "params": map[string]any{
			"threadId": "thread-1", "turn": map[string]any{"id": turnID, "status": "completed", "model": "reported-model"},
		}})
	}
	return len(data), nil
}

func (s *scriptedAppServerInput) Close() error {
	s.once.Do(func() {
		close(s.server.output)
	})
	return nil
}

func (s *scriptedAppServer) emit(value any) {
	data, _ := json.Marshal(value)
	data = append(data, '\n')
	s.output <- data
}

func newScriptedAppServer(validJSON string, validAfter ...int) (*scriptedAppServer, *appServerProcess) {
	stdoutReader, stdoutWriter := io.Pipe()
	validTurn := 2
	if len(validAfter) > 0 {
		validTurn = validAfter[0]
	}
	server := &scriptedAppServer{output: make(chan []byte, 16), stdout: stdoutWriter, validJSON: validJSON, validAfter: validTurn}
	go func() {
		for data := range server.output {
			_, _ = server.stdout.Write(data)
		}
		_ = server.stdout.Close()
	}()
	return server, &appServerProcess{
		cmd:    exec.Command("true"),
		stdin:  &scriptedAppServerInput{server: server},
		stdout: stdoutReader,
		stderr: &boundedBuffer{limit: maxStderrBytes},
	}
}

func TestAnalyzeChunkUsesSecondBoundedCorrection(t *testing.T) {
	chunk := testPreparedChunk(t)
	validJSONBytes, err := json.Marshal(testValidChunkResponse(chunk))
	if err != nil {
		t.Fatal(err)
	}
	oldLaunch := launchAppServer
	launchAppServer = func(context.Context, string, string) (*appServerProcess, error) {
		_, process := newScriptedAppServer(string(validJSONBytes), 3)
		return process, nil
	}
	defer func() { launchAppServer = oldLaunch }()

	attempt, err := NewCodexAppServer(CodexConfig{Binary: "true"}).AnalyzeChunk(context.Background(), chunk, AnalysisOptions{Model: "requested-model", Effort: "low"})
	if err != nil {
		t.Fatalf("AnalyzeChunk: %v", err)
	}
	if len(attempt.Turns) != 3 || attempt.Turns[1].TurnKind != "corrective" || attempt.Turns[2].TurnKind != "corrective" {
		t.Fatalf("bounded correction turns = %+v", attempt.Turns)
	}
}

func testPreparedChunk(t *testing.T) semantics.PreparedChunk {
	t.Helper()
	input, err := semantics.Prepare("Chunk", "nl", "en", []semantics.Block{{BlockIndex: 0, SourceText: "De bank."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := semantics.PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return chunk
}

func testValidChunkResponse(chunk semantics.PreparedChunk) semantics.Response {
	response := semantics.Response{
		Version:   semantics.AnalysisContractVersion,
		Sentences: []semantics.Sentence{{Source: semantics.SpanRef{BlockIndex: 0, SourceText: chunk.Block.SourceText, Occurrence: 0}}},
		Tokens:    make([]semantics.TokenResult, 0, len(chunk.Tokens)), NewSenses: []semantics.NewSense{}, Constructions: []semantics.Construction{},
	}
	for _, token := range chunk.Tokens {
		response.Tokens = append(response.Tokens, semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000})
	}
	return response
}

func TestAnalyzeChunkUsesOneSchemaForInitialAndCorrectionAndReturnsArtifacts(t *testing.T) {
	chunk := testPreparedChunk(t)
	validJSONBytes, err := json.Marshal(testValidChunkResponse(chunk))
	if err != nil {
		t.Fatal(err)
	}
	var server *scriptedAppServer
	oldLaunch := launchAppServer
	launchAppServer = func(context.Context, string, string) (*appServerProcess, error) {
		var process *appServerProcess
		server, process = newScriptedAppServer(string(validJSONBytes))
		return process, nil
	}
	defer func() { launchAppServer = oldLaunch }()

	attempt, err := NewCodexAppServer(CodexConfig{Binary: "true"}).AnalyzeChunk(context.Background(), chunk, AnalysisOptions{Model: "requested-model", Effort: "low"})
	if err != nil {
		t.Fatalf("AnalyzeChunk: %v", err)
	}
	if attempt.Response.Version != semantics.AnalysisContractVersion || attempt.ReportedModel != "reported-model" || len(attempt.Turns) != 2 {
		t.Fatalf("attempt = %+v", attempt)
	}
	if attempt.Turns[0].ValidationError == "" || attempt.Turns[0].CompletedResponse != `{"version":"wrong"}` {
		t.Fatalf("initial turn artifact = %+v", attempt.Turns[0])
	}
	if attempt.Turns[1].ValidationError != "" || attempt.Turns[1].CompletedResponse != string(validJSONBytes) {
		t.Fatalf("corrective turn artifact = %+v", attempt.Turns[1])
	}
	server.mu.Lock()
	params := append([]map[string]any(nil), server.turnParams...)
	server.mu.Unlock()
	if len(params) != 2 || params[0]["model"] != "requested-model" || params[0]["effort"] != "low" || params[1]["model"] != "requested-model" || params[1]["effort"] != "low" {
		t.Fatalf("turn params = %#v", params)
	}
	firstSchema, _ := json.Marshal(params[0]["outputSchema"])
	secondSchema, _ := json.Marshal(params[1]["outputSchema"])
	if string(firstSchema) != string(secondSchema) || !strings.Contains(string(firstSchema), `"const":"reader.analysis.v2"`) {
		t.Fatalf("schema reuse = %s / %s", firstSchema, secondSchema)
	}
}

package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"doublangu/internal/library"
	"doublangu/internal/reader"
)

const defaultCodexTimeout = 5 * time.Minute

// CodexConfig configures the installed local Codex app-server adapter.
type CodexConfig struct {
	Binary  string
	Model   string
	Effort  string
	Timeout time.Duration
}

// CodexAppServer implements Annotator through one short-lived authenticated
// `codex app-server --stdio` process per enrichment request.
type CodexAppServer struct {
	binary  string
	model   string
	effort  string
	timeout time.Duration
}

// NewCodexAppServer constructs the production Codex annotator. Empty values
// select the installed `codex` binary, medium effort, and the five-minute limit.
func NewCodexAppServer(config CodexConfig) *CodexAppServer {
	if config.Binary == "" {
		config.Binary = "codex"
	}
	if config.Effort == "" {
		config.Effort = "medium"
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultCodexTimeout
	}
	return &CodexAppServer{binary: config.Binary, model: config.Model, effort: config.Effort, timeout: config.Timeout}
}

// Annotate starts a private ephemeral thread, obtains strict JSON, and permits
// one corrective turn when the provider payload fails local validation.
func (c *CodexAppServer) Annotate(ctx context.Context, input ArticleInput) ([]Candidate, error) {
	if c == nil {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("nil Codex app-server adapter")}
	}
	if err := validateArticleInput(input); err != nil {
		return nil, &Error{Code: CodeInvalidInput, Err: err}
	}
	runContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	workingDirectory, err := os.MkdirTemp("", "doublangu-codex-")
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Err: fmt.Errorf("create private app-server directory: %w", err)}
	}
	defer os.RemoveAll(workingDirectory)

	process, err := launchAppServer(runContext, c.binary, workingDirectory)
	if err != nil {
		return nil, c.classify(runContext, nil, err, CodeUnavailable)
	}
	defer process.close()
	protocol := newProtocolClient(process.stdin, process.stdout)
	outputSchema, err := outputSchemaJSON()
	if err != nil {
		return nil, &Error{Code: CodeProtocol, Err: err}
	}

	nextID := int64(1)
	if err := protocol.call(runContext, nextID, "initialize", initializeParams{
		ClientInfo:   initializeClientInfo{Name: "doublangu", Version: "0.1.0"},
		Capabilities: &initializeCapabilities{ExperimentalAPI: true},
	}, &map[string]any{}); err != nil {
		return nil, c.classify(runContext, process, err, CodeProtocol)
	}
	nextID++
	threadParams := threadStartParams{
		ApprovalPolicy: "never",
		Sandbox:        "read-only",
		CWD:            workingDirectory,
		Ephemeral:      true,
		DynamicTools:   []any{},
		Model:          c.model,
	}
	var threadResponse threadStartResponse
	if err := protocol.call(runContext, nextID, "thread/start", threadParams, &threadResponse); err != nil {
		return nil, c.classify(runContext, process, err, CodeProtocol)
	}
	threadID := threadResponse.Thread.ID
	if threadID == "" {
		return nil, &Error{Code: CodeProtocol, Err: errors.New("thread/start returned no thread id")}
	}
	nextID++

	response, err := protocol.runTurn(runContext, nextID, threadID, BuildPrompt(input), c.effort, c.model, outputSchema)
	if err != nil {
		return nil, c.classify(runContext, process, err, CodeProviderFailure)
	}
	candidates, validationErr := decodeCandidatePayload(input, response)
	if validationErr != nil {
		// The corrective turn stays in the same ephemeral thread and contains
		// only the validation diagnostics plus the original model response.
		nextID++
		correction := BuildCorrectionPrompt(validationErr.Error(), response)
		response, err = protocol.runTurn(runContext, nextID, threadID, correction, c.effort, c.model, outputSchema)
		if err != nil {
			return nil, c.classify(runContext, process, err, CodeInvalidOutput)
		}
		candidates, validationErr = decodeCandidatePayload(input, response)
	}
	if validationErr != nil {
		return nil, c.classify(runContext, process, validationErr, CodeInvalidOutput)
	}
	return candidates, nil
}

func (c *CodexAppServer) classify(ctx context.Context, process *appServerProcess, err error, fallback string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Err: errors.New("Codex app-server request timed out")}
	}
	if process != nil && hasAuthenticationFailure(process.stderr.String()) {
		return &Error{Code: CodeNotAuthenticated, Err: errors.New("Codex is not authenticated")}
	}
	if typed := new(Error); errors.As(err, &typed) {
		return typed
	}
	return &Error{Code: fallback, Err: err}
}

func validateArticleInput(input ArticleInput) error {
	if strings.TrimSpace(input.Title) == "" {
		return errors.New("article title is required")
	}
	if _, err := library.ParseBCP47(input.SourceLanguage); err != nil {
		return fmt.Errorf("source_language: %w", err)
	}
	if _, err := library.ParseBCP47(input.TargetLanguage); err != nil {
		return fmt.Errorf("target_language: %w", err)
	}
	if len(input.Blocks) == 0 {
		return errors.New("article must contain at least one block")
	}
	total := 0
	for index, block := range input.Blocks {
		if block.BlockIndex != index {
			return fmt.Errorf("block %d has non-sequential block_index", index)
		}
		if block.SourceText == "" || !utf8.ValidString(block.SourceText) {
			return fmt.Errorf("block %d has invalid source text", index)
		}
		total += len(block.SourceText)
	}
	if total > reader.MaxEnrichmentBodyBytes {
		return reader.ErrEnrichmentBodyTooLarge
	}
	return nil
}

func decodeCandidatePayload(input ArticleInput, response string) ([]Candidate, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response), &top); err != nil {
		return nil, fmt.Errorf("response is not a JSON object: %w", err)
	}
	if top == nil {
		return nil, errors.New("response must be an object")
	}
	if len(top) != 1 {
		return nil, errors.New("response must contain only annotations")
	}
	annotationsRaw, ok := top["annotations"]
	if !ok || string(annotationsRaw) == "null" {
		return nil, errors.New("annotations is required")
	}
	var itemRaw []json.RawMessage
	if err := json.Unmarshal(annotationsRaw, &itemRaw); err != nil {
		return nil, fmt.Errorf("annotations must be an array: %w", err)
	}
	candidates := make([]Candidate, len(itemRaw))
	for index, raw := range itemRaw {
		if err := validateCandidateJSONShape(raw); err != nil {
			return nil, fmt.Errorf("annotation %d: %w", index, err)
		}
		var candidate Candidate
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return nil, fmt.Errorf("annotation %d: %w", index, err)
		}
		candidates[index] = candidate
	}
	if err := reader.ValidateCandidates(input, candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

var candidateJSONFields = map[string]struct{}{
	"block_index": {}, "source_text": {}, "occurrence": {}, "kind": {}, "learning_key": {},
	"primary_translation": {}, "alternatives": {}, "literal_translation": {}, "meaning_note": {},
	"usage_note": {}, "parts_note": {}, "suggest_shadow": {},
}

func validateCandidateJSONShape(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("annotation must be an object")
	}
	if len(object) != len(candidateJSONFields) {
		return errors.New("annotation has missing or additional fields")
	}
	for field := range candidateJSONFields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing %s", field)
		}
	}
	for _, field := range []string{"source_text", "kind", "learning_key", "primary_translation", "literal_translation", "meaning_note", "usage_note", "parts_note"} {
		if err := requireJSONType(object[field], field, '"'); err != nil {
			return err
		}
	}
	for _, field := range []string{"block_index", "occurrence"} {
		if err := requireJSONNumber(object[field], field); err != nil {
			return err
		}
	}
	trimmedBoolean := strings.TrimSpace(string(object["suggest_shadow"]))
	if trimmedBoolean != "true" && trimmedBoolean != "false" {
		return errors.New("suggest_shadow must be a boolean")
	}
	if string(object["alternatives"]) == "null" {
		return errors.New("alternatives must be an array")
	}
	var alternatives []json.RawMessage
	if err := json.Unmarshal(object["alternatives"], &alternatives); err != nil {
		return errors.New("alternatives must be an array")
	}
	for index, alternative := range alternatives {
		if err := requireJSONType(alternative, fmt.Sprintf("alternatives[%d]", index), '"'); err != nil {
			return err
		}
	}
	return nil
}

func requireJSONType(raw json.RawMessage, field string, first byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != first {
		return fmt.Errorf("%s has the wrong JSON type", field)
	}
	return nil
}

func requireJSONNumber(raw json.RawMessage, field string) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || strings.ContainsAny(trimmed, ".eE") {
		return fmt.Errorf("%s must be an integer", field)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
		return fmt.Errorf("%s must be a non-negative integer", field)
	}
	return nil
}

func (p *protocolClient) call(ctx context.Context, id int64, method string, params any, result any) error {
	if err := p.send(id, method, params); err != nil {
		return err
	}
	for {
		message, err := p.next(ctx)
		if err != nil {
			return protocolError(err)
		}
		if !responseFor(message, id) {
			continue
		}
		return decodeResult(message, result)
	}
}

func (p *protocolClient) runTurn(ctx context.Context, id int64, threadID, prompt, effort, model string, outputSchema json.RawMessage) (string, error) {
	result, err := p.runTurnDetailed(ctx, id, threadID, prompt, effort, model, outputSchema)
	return result.Text, err
}

type protocolTurnResult struct {
	Text          string
	MetadataJSON  string
	ReportedModel string
	StartedAt     string
	Duration      time.Duration
}

func (p *protocolClient) runTurnDetailed(ctx context.Context, id int64, threadID, prompt, effort, model string, outputSchema json.RawMessage) (protocolTurnResult, error) {
	started := time.Now()
	result := protocolTurnResult{StartedAt: started.UTC().Format(time.RFC3339Nano)}
	finish := func(err error) (protocolTurnResult, error) {
		result.Duration = time.Since(started)
		return result, err
	}
	if err := p.send(id, "turn/start", turnStartParams{
		ThreadID:     threadID,
		Input:        []textInput{{Type: "text", Text: prompt}},
		Effort:       effort,
		Model:        model,
		OutputSchema: outputSchema,
	}); err != nil {
		return finish(protocolFailure(err))
	}
	turnID := ""
	responseSeen := false
	for {
		message, err := p.next(ctx)
		if err != nil {
			return finish(protocolError(err))
		}
		if unsupportedServerMethod(message.Method) {
			return finish(protocolFailure(fmt.Errorf("app-server sent unsupported tool or approval request %q", message.Method)))
		}
		if responseFor(message, id) {
			var response turnStartResponse
			if err := decodeResult(message, &response); err != nil {
				if message.Error != nil {
					return finish(err)
				}
				return finish(protocolFailure(err))
			}
			turnID = response.Turn.ID
			responseSeen = true
			continue
		}
		switch message.Method {
		case "item/completed":
			var params itemCompletedParams
			if err := json.Unmarshal(message.Params, &params); err != nil {
				return finish(protocolFailure(fmt.Errorf("decode item/completed: %w", err)))
			}
			if params.ThreadID == threadID && (turnID == "" || params.TurnID == turnID) {
				switch params.Item.Type {
				case "agentMessage":
					result.Text = params.Item.Text
				case "", "userMessage", "reasoning":
					// The server may echo input and emit reasoning telemetry.
				default:
					if unsupportedItemType(params.Item.Type) {
						return finish(protocolFailure(fmt.Errorf("app-server completed unsupported item type %q", params.Item.Type)))
					}
				}
			}
		case "turn/completed":
			var params turnCompletedParams
			if err := json.Unmarshal(message.Params, &params); err != nil {
				return finish(protocolFailure(fmt.Errorf("decode turn/completed: %w", err)))
			}
			if params.ThreadID != threadID || (turnID != "" && params.Turn.ID != "" && params.Turn.ID != turnID) {
				continue
			}
			result.MetadataJSON = string(message.Params)
			result.ReportedModel = params.Turn.Model
			if params.Turn.Status != "completed" {
				if params.Turn.Error != nil && params.Turn.Error.Message != "" {
					return finish(errors.New("Codex turn failed: " + params.Turn.Error.Message))
				}
				return finish(fmt.Errorf("Codex turn ended with status %q", params.Turn.Status))
			}
			if !responseSeen {
				return finish(protocolFailure(errors.New("turn completed before turn/start response")))
			}
			if result.Text == "" {
				return finish(protocolFailure(errors.New("turn completed without an assistant message")))
			}
			return finish(nil)
		}
	}
}

func protocolFailure(err error) error {
	return &Error{Code: CodeProtocol, Err: err}
}

func unsupportedServerMethod(method string) bool {
	lower := strings.ToLower(method)
	for _, marker := range []string{"approval", "permission", "tool", "commandexecution", "filechange"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func unsupportedItemType(itemType string) bool {
	lower := strings.ToLower(itemType)
	for _, marker := range []string{"approval", "permission", "tool", "command", "filechange", "websearch"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// cliVersion is diagnostic metadata only. A version lookup failure must not
// make an otherwise usable analysis request fail.
func (c *CodexAppServer) cliVersion(ctx context.Context) string {
	if c == nil || c.binary == "" {
		return ""
	}
	versionContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(versionContext, c.binary, "--version").Output()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(out))
	if len(version) > 512 {
		version = version[:512]
	}
	return version
}

func (c *CodexAppServer) CLIVersion(ctx context.Context) string {
	return c.cliVersion(ctx)
}

var _ Annotator = (*CodexAppServer)(nil)

package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReasoningEffort is an effort value advertised by the installed provider.
type ReasoningEffort struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// Model is the provider's model-list entry. IDs are protocol values and must
// be sent back unchanged when a run is started.
type Model struct {
	ID                        string            `json:"id"`
	DisplayName               string            `json:"display_name"`
	IsDefault                 bool              `json:"is_default"`
	Hidden                    bool              `json:"hidden"`
	SupportedReasoningEfforts []ReasoningEffort `json:"supported_reasoning_efforts"`
}

// ModelCatalogProvider is the optional provider capability used by settings
// validation and the owner-facing model picker.
type ModelCatalogProvider interface {
	ListModels(context.Context) ([]Model, error)
}

type modelListParams struct {
	Cursor        string `json:"cursor,omitempty"`
	IncludeHidden bool   `json:"includeHidden"`
}

// ListModels obtains the complete hidden-inclusive catalog from one fresh
// app-server process. Caching and last-good retention belong to the HTTP
// layer, where they can be reported to the owner.
func (c *CodexAppServer) ListModels(ctx context.Context) ([]Model, error) {
	if c == nil {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("nil Codex app-server adapter")}
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultCodexTimeout
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	workingDirectory, err := os.MkdirTemp("", "doublangu-codex-models-")
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
	if err := protocol.call(runContext, 1, "initialize", initializeParams{
		ClientInfo:   initializeClientInfo{Name: "doublangu", Version: "0.1.0"},
		Capabilities: &initializeCapabilities{ExperimentalAPI: true},
	}, &map[string]any{}); err != nil {
		return nil, c.classify(runContext, process, err, CodeProtocol)
	}

	models := make([]Model, 0)
	cursor := ""
	seenCursors := map[string]struct{}{}
	nextID := int64(2)
	for {
		var raw json.RawMessage
		if err := protocol.call(runContext, nextID, "model/list", modelListParams{Cursor: cursor, IncludeHidden: true}, &raw); err != nil {
			return nil, c.classify(runContext, process, err, CodeProtocol)
		}
		page, nextCursor, err := decodeModelListPage(raw)
		if err != nil {
			return nil, &Error{Code: CodeProtocol, Err: err}
		}
		models = append(models, page...)
		if nextCursor == "" {
			break
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, &Error{Code: CodeProtocol, Err: errors.New("model/list returned a repeated cursor")}
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
		nextID++
	}
	if len(models) == 0 {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("model/list returned no models")}
	}
	return models, nil
}

func decodeModelListPage(raw json.RawMessage) ([]Model, string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, "", errors.New("model/list result must be an object")
	}
	modelsRaw := object["data"]
	if len(modelsRaw) == 0 {
		modelsRaw = object["models"]
	}
	if len(modelsRaw) == 0 {
		return nil, "", errors.New("model/list result has no data or models array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(modelsRaw, &entries); err != nil {
		return nil, "", fmt.Errorf("model/list data must be an array: %w", err)
	}
	models := make([]Model, 0, len(entries))
	for index, entry := range entries {
		model, err := decodeModel(entry)
		if err != nil {
			return nil, "", fmt.Errorf("model/list model %d: %w", index, err)
		}
		models = append(models, model)
	}
	return models, firstString(object, "nextCursor", "next_cursor"), nil
}

func decodeModel(raw json.RawMessage) (Model, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return Model{}, errors.New("model entry must be an object")
	}
	model := Model{ID: firstString(object, "id", "model"), DisplayName: firstString(object, "displayName", "display_name", "name")}
	if model.ID == "" {
		return Model{}, errors.New("model entry has no id")
	}
	if model.DisplayName == "" {
		model.DisplayName = model.ID
	}
	model.IsDefault = firstBool(object, "isDefault", "is_default", "default")
	model.Hidden = firstBool(object, "hidden", "isHidden", "is_hidden")
	effortsRaw := firstRaw(object, "supportedReasoningEfforts", "supported_reasoning_efforts", "reasoningEfforts", "reasoning_efforts", "supportedEfforts")
	if len(effortsRaw) != 0 && string(effortsRaw) != "null" {
		var entries []json.RawMessage
		if err := json.Unmarshal(effortsRaw, &entries); err != nil {
			return Model{}, fmt.Errorf("supported reasoning efforts must be an array: %w", err)
		}
		for index, entry := range entries {
			effort, err := decodeEffort(entry)
			if err != nil {
				return Model{}, fmt.Errorf("reasoning effort %d: %w", index, err)
			}
			if effort.Value != "" && !hasEffort(model.SupportedReasoningEfforts, effort.Value) {
				model.SupportedReasoningEfforts = append(model.SupportedReasoningEfforts, effort)
			}
		}
	}
	return model, nil
}

func decodeEffort(raw json.RawMessage) (ReasoningEffort, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "\"") {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return ReasoningEffort{}, err
		}
		return ReasoningEffort{Value: value}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return ReasoningEffort{}, errors.New("effort must be a string or object")
	}
	return ReasoningEffort{
		Value:       firstString(object, "value", "effort", "reasoningEffort", "reasoning_effort", "name", "id"),
		Description: firstString(object, "description", "label"),
	}, nil
}

func firstRaw(object map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

func firstString(object map[string]json.RawMessage, keys ...string) string {
	raw := firstRaw(object, keys...)
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstBool(object map[string]json.RawMessage, keys ...string) bool {
	raw := firstRaw(object, keys...)
	if len(raw) == 0 {
		return false
	}
	var value bool
	_ = json.Unmarshal(raw, &value)
	return value
}

func hasEffort(efforts []ReasoningEffort, value string) bool {
	for _, effort := range efforts {
		if effort.Value == value {
			return true
		}
	}
	return false
}

func SupportsSelection(models []Model, modelID, effort string) bool {
	for _, model := range models {
		if model.ID != modelID {
			continue
		}
		return hasEffort(model.SupportedReasoningEfforts, effort)
	}
	return false
}

var _ ModelCatalogProvider = (*CodexAppServer)(nil)

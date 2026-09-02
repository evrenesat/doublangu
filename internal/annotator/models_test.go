package annotator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type modelListWriteCloser struct {
	bytes.Buffer
}

func (w *modelListWriteCloser) Close() error { return nil }

func TestListModelsFollowsPagesAndPreservesHiddenEfforts(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":1,"result":{}}`,
		`{"id":2,"result":{"data":[{"id":"gpt-one","displayName":"One","isDefault":true,"hidden":false,"supportedReasoningEfforts":[{"value":"low","description":"Fast"}],"futureField":"ignored"}],"nextCursor":"page-2"}}`,
		`{"id":3,"result":{"data":[{"id":"gpt-two","displayName":"Two","hidden":true,"supportedReasoningEfforts":["high"]}],"nextCursor":null}}`,
	}, "\n") + "\n"
	stdin := &modelListWriteCloser{}
	oldLaunch := launchAppServer
	launchAppServer = func(context.Context, string, string) (*appServerProcess, error) {
		return &appServerProcess{
			cmd:    exec.Command("true"),
			stdin:  stdin,
			stdout: io.NopCloser(strings.NewReader(stream)),
			stderr: &boundedBuffer{limit: maxStderrBytes},
		}, nil
	}
	defer func() { launchAppServer = oldLaunch }()

	models, err := NewCodexAppServer(CodexConfig{Binary: "fake", Timeout: 2 * time.Second}).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-one" || models[1].ID != "gpt-two" {
		t.Fatalf("models = %+v", models)
	}
	if !models[0].IsDefault || models[0].Hidden || len(models[0].SupportedReasoningEfforts) != 1 || models[0].SupportedReasoningEfforts[0].Value != "low" {
		t.Fatalf("first model = %+v", models[0])
	}
	if !models[1].Hidden || len(models[1].SupportedReasoningEfforts) != 1 || models[1].SupportedReasoningEfforts[0].Value != "high" {
		t.Fatalf("second model = %+v", models[1])
	}

	requests := strings.Split(strings.TrimSpace(stdin.String()), "\n")
	if len(requests) != 3 {
		t.Fatalf("requests = %q", stdin.String())
	}
	var firstList, secondList map[string]any
	if err := json.Unmarshal([]byte(requests[1]), &firstList); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(requests[2]), &secondList); err != nil {
		t.Fatal(err)
	}
	firstParams := firstList["params"].(map[string]any)
	secondParams := secondList["params"].(map[string]any)
	if firstList["method"] != "model/list" || firstParams["includeHidden"] != true || firstParams["cursor"] != nil {
		t.Fatalf("first model/list request = %#v", firstList)
	}
	if secondParams["cursor"] != "page-2" || secondParams["includeHidden"] != true {
		t.Fatalf("second model/list request = %#v", secondList)
	}
}

func TestDecodeModelListPageAcceptsKnownShapesAndIgnoresUnknownFields(t *testing.T) {
	page, cursor, err := decodeModelListPage(json.RawMessage(`{"models":[{"model":"gpt","name":"GPT","reasoning_efforts":[{"name":"medium","label":"Balanced"}],"unknown":true}],"next_cursor":"next"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "next" || len(page) != 1 || page[0].ID != "gpt" || page[0].DisplayName != "GPT" || len(page[0].SupportedReasoningEfforts) != 1 || page[0].SupportedReasoningEfforts[0].Value != "medium" {
		t.Fatalf("page=%+v cursor=%q", page, cursor)
	}
	if SupportsSelection(page, "gpt", "medium") == false || SupportsSelection(page, "gpt", "low") {
		t.Fatal("model effort selection was not mapped exactly")
	}
}

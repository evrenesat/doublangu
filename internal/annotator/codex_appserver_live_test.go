package annotator

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"doublangu/internal/semantics"
)

func TestLiveCodexAppServer(t *testing.T) {
	requireLiveCodex(t)

	adapter := NewCodexAppServer(CodexConfig{Timeout: 120 * time.Second})
	candidates, err := adapter.Annotate(context.Background(), ArticleInput{
		Title:          "Live smoke",
		SourceLanguage: "nl",
		TargetLanguage: "en",
		Blocks: []ArticleInputBlock{{
			BlockIndex: 0,
			SourceText: "Ik wil tot rust komen.",
		}},
	})
	if err != nil {
		t.Fatalf("live app-server enrichment: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("live app-server returned no Dutch annotation")
	}
	for index, candidate := range candidates {
		if candidate.SourceText == "" || candidate.PrimaryTranslation == "" {
			t.Fatalf("live candidate %d is missing source or English translation: %+v", index, candidate)
		}
	}
}

func TestLiveCodexChunk(t *testing.T) {
	requireLiveCodex(t)
	model := strings.TrimSpace(os.Getenv("DOUBLANGU_TEST_CODEX_MODEL"))
	if model == "" {
		t.Skip("set DOUBLANGU_TEST_CODEX_MODEL to run the authenticated chunk smoke")
	}
	effort := strings.TrimSpace(os.Getenv("DOUBLANGU_TEST_CODEX_EFFORT"))
	if effort == "" {
		effort = "medium"
	}
	prepared, err := semantics.Prepare("Live chunk smoke", "nl", "en", []semantics.Block{{
		BlockIndex: 0,
		SourceText: "Ik denk: wat gebeurt hier? Het dorp is meestal rustig. In één keer sta je in de belangstelling.",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := semantics.PrepareChunk(prepared, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewCodexAppServer(CodexConfig{Timeout: 10 * time.Minute})
	attempt, err := adapter.AnalyzeChunk(context.Background(), chunk, AnalysisOptions{Model: model, Effort: effort})
	if err != nil {
		for _, turn := range attempt.Turns {
			t.Logf("turn %d %s validation=%q provider=%q", turn.TurnIndex, turn.TurnKind, turn.ValidationError, turn.ProviderError)
		}
		t.Fatalf("live app-server chunk (%s/%s): %v", model, effort, err)
	}
	if _, err := semantics.ValidateChunkResponse(chunk, attempt.Response); err != nil {
		t.Fatalf("live app-server chunk validation (%s/%s): %v", model, effort, err)
	}
}

func requireLiveCodex(t *testing.T) {
	t.Helper()
	if os.Getenv("DOUBLANGU_TEST_CODEX_LIVE") != "1" {
		t.Skip("set DOUBLANGU_TEST_CODEX_LIVE=1 to run the authenticated app-server smoke")
	}
	status, err := exec.Command("codex", "login", "status").CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(status)), "not logged") || strings.Contains(strings.ToLower(string(status)), "not authenticated") {
			t.Skipf("Codex is demonstrably unauthenticated: %s", strings.TrimSpace(string(status)))
		}
		t.Fatalf("codex login status: %v: %s", err, strings.TrimSpace(string(status)))
	}
	if lower := strings.ToLower(string(status)); strings.Contains(lower, "not logged") || strings.Contains(lower, "not authenticated") || !strings.Contains(lower, "logged in") {
		t.Skipf("Codex login status is not authenticated: %s", strings.TrimSpace(string(status)))
	}
}

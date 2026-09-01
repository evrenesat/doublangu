package annotator

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLiveCodexAppServer(t *testing.T) {
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

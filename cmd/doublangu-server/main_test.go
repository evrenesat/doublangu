package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	manifest "doublangu/internal/plugins"
)

func TestServerHandlerAssemblyDoesNotUseGlobalMux(t *testing.T) {
	registry := manifest.NewRegistry()
	first := newHandler(registry, &manifest.ParsedSchema{})
	second := newHandler(registry, &manifest.ParsedSchema{})

	health := httptest.NewRecorder()
	first.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || health.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("health response = status %d content-type %q", health.Code, health.Header().Get("Content-Type"))
	}
	var report manifest.DiagnosticsReport
	if err := json.Unmarshal(health.Body.Bytes(), &report); err != nil {
		t.Fatalf("health JSON: %v", err)
	}
	if !report.CoreReady || !report.LoaderReady || report.PluginCount != 0 {
		t.Errorf("health report = %+v", report)
	}

	unknown := httptest.NewRecorder()
	second.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/not-registered", nil))
	if unknown.Code != http.StatusNotFound {
		t.Errorf("separate mux status = %d, want 404", unknown.Code)
	}
}

func TestZeroPluginServerSmoke(t *testing.T) {
	repositoryRoot := findRepositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "doublangu-server")
	build := exec.Command("go", "build", "-o", binary, "./cmd/doublangu-server")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}

	command := exec.Command(binary)
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "DOUBLANGU_LISTEN=127.0.0.1:0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("server stdout pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	var startup []string
	var address string
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for address == "" {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("server stopped before listening: %s", stderr.String())
			}
			startup = append(startup, line)
			if strings.HasPrefix(line, "listening on ") {
				address = strings.TrimPrefix(line, "listening on ")
			}
		case <-timeout.C:
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
			t.Fatalf("server did not announce an ephemeral listener: %s", stderr.String())
		}
	}

	endpoint := "http:" + "//" + address + "/health"
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		t.Fatalf("request health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	var report manifest.DiagnosticsReport
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !report.CoreReady || !report.LoaderReady || !report.SchemaAvailable || report.RegistryState != "empty" || report.PluginCount != 0 || report.RegistrationCount != 0 || len(report.PluginIDs) != 0 {
		t.Errorf("health report = %+v", report)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt server: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Errorf("server exit = %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("server did not terminate after interrupt")
	}
	if !strings.Contains(strings.Join(startup, "\n"), "feature plugins: 0") {
		t.Errorf("startup banner = %q", startup)
	}
	if stderr.Len() != 0 {
		t.Errorf("server stderr = %q", stderr.String())
	}
}

func findRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("current directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod not found")
		}
		directory = parent
	}
}

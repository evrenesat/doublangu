package annotator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"doublangu/internal/config"
)

func sampleProviderFile(t *testing.T) *config.ProviderConfigFile {
	t.Helper()
	raw := `{
		"version": 1,
		"allow_insecure_tailscale_http": true,
		"providers": [
			{ "id": "codex-app-server", "label": "Codex", "endpoint_label": "Local Codex", "type": "codex_app_server", "enabled": true },
			{ "id": "mac-omlx", "label": "OMLX", "endpoint_label": "Mac", "type": "openai_compatible", "enabled": true, "base_url": "http://100.64.0.10:8899/v1", "api_key_env": "DOUBLANGU_OMLX_API_KEY" },
			{ "id": "disabled-provider", "label": "Off", "endpoint_label": "Off", "type": "codex_app_server", "enabled": false }
		]
	}`
	file, err := config.DecodeProviderConfig([]byte(raw), func(key string) string {
		if key == "DOUBLANGU_OMLX_API_KEY" {
			return "secret"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestRegistryBuildsEnabledInstancesAndSanitizedDescriptors(t *testing.T) {
	file := sampleProviderFile(t)
	registry, err := NewRegistry(file, "codex", func(key string) (string, error) {
		if key != "DOUBLANGU_OMLX_API_KEY" {
			return "", errors.New("missing")
		}
		return "secret", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Provider("codex-app-server"); !ok {
		t.Fatal("codex provider missing")
	}
	if _, ok := registry.Provider("mac-omlx"); !ok {
		t.Fatal("omlx provider missing")
	}
	if _, ok := registry.Provider("disabled-provider"); ok {
		t.Fatal("disabled provider got an instance")
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 3 {
		t.Fatalf("descriptors = %+v", descriptors)
	}
	for _, descriptor := range descriptors {
		if strings.Contains(descriptor.EndpointLabel+descriptor.Label+descriptor.ID, "http") || descriptor.ConfigFingerprint == "" {
			t.Fatalf("unsanitized descriptor = %+v", descriptor)
		}
	}
	if _, err := registry.ListModels(context.Background(), "missing"); err == nil {
		t.Fatal("unknown provider catalog succeeded")
	}
	if _, ok := registry.Provider("mac-omlx"); !ok {
		t.Fatal("omlx provider missing")
	}
}

// TestRegistryDerivesCodexTimeoutFromEntry proves a Codex provider instance
// observes its own configured request_timeout_seconds instead of a
// constructor-wide value: the instance deadline and the descriptor's
// advertised timeout agree with the entry.
func TestRegistryDerivesCodexTimeoutFromEntry(t *testing.T) {
	file := sampleProviderFile(t)
	for i := range file.Providers {
		if file.Providers[i].ID == "codex-app-server" {
			file.Providers[i].RequestTimeoutSeconds = 30
		}
	}
	registry, err := NewRegistry(file, "codex", func(string) (string, error) { return "secret", nil })
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := registry.Provider("codex-app-server")
	if !ok {
		t.Fatal("codex provider missing")
	}
	codex, ok := instance.(*codexStageProvider)
	if !ok {
		t.Fatalf("codex provider type = %T", instance)
	}
	if codex.timeout != 30*time.Second {
		t.Fatalf("codex timeout = %v, want 30s", codex.timeout)
	}
	for _, descriptor := range registry.Descriptors() {
		if descriptor.ID == "codex-app-server" && descriptor.RequestTimeoutMS != 30_000 {
			t.Fatalf("descriptor timeout = %d, want 30000", descriptor.RequestTimeoutMS)
		}
	}
}

func TestRegistryRejectsMissingSecret(t *testing.T) {
	file := sampleProviderFile(t)
	_, err := NewRegistry(file, "codex", func(string) (string, error) { return "", nil })
	if err == nil || !strings.Contains(err.Error(), "secret resolution failed") {
		t.Fatalf("missing secret error = %v", err)
	}
}

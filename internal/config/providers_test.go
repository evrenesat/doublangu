package config

import (
	"encoding/json"
	"strings"
	"testing"

	"doublangu/internal/pipeline"
)

func decodeFile(t *testing.T, raw string) (*ProviderConfigFile, error) {
	t.Helper()
	env := func(key string) string {
		if key == "DOUBLANGU_OMLX_API_KEY" {
			return "present-secret"
		}
		return ""
	}
	file, err := DecodeProviderConfig([]byte(raw), env)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func sampleConfig() string {
	return `{
		"version": 1,
		"allow_insecure_tailscale_http": true,
		"providers": [
			{ "id": "codex-app-server", "label": "Codex app-server", "endpoint_label": "Local Codex", "type": "codex_app_server", "enabled": true },
			{ "id": "mac-omlx", "label": "Mac OMLX", "endpoint_label": "Mac over Tailscale", "type": "openai_compatible", "enabled": true, "base_url": "http://100.64.0.10:8899/v1", "api_key_env": "DOUBLANGU_OMLX_API_KEY" }
		],
		"bootstrap_profile": {
			"name": "Codex + Mac OMLX",
			"bindings": {
				"linguistic_analysis": { "provider_id": "codex-app-server", "model_id": "gpt-5.6-luna", "options": { "reasoning_effort": "medium" } },
				"translation": { "provider_id": "mac-omlx", "model_id": "configured-omlx-model", "options": { "temperature_milli": 0, "max_output_tokens": 16384 } }
			}
		}
	}`
}

func TestProviderConfigAcceptsSampleAndComputesFingerprints(t *testing.T) {
	file, err := decodeFile(t, sampleConfig())
	if err != nil {
		t.Fatalf("sample config rejected: %v", err)
	}
	if len(file.Providers) != 2 || !file.Providers[0].Enabled {
		t.Fatalf("providers = %+v", file.Providers)
	}
	first := ProviderConfigFingerprint(file.Providers[0])
	second := ProviderConfigFingerprint(file.Providers[1])
	if first == "" || first == second || len(first) != 64 {
		t.Fatalf("fingerprints = %q / %q", first, second)
	}
	if ProviderConfigFingerprint(file.Providers[0]) != first {
		t.Fatal("fingerprint is not deterministic")
	}
	if file.BootstrapProfile == nil || len(file.BootstrapProfile.Bindings) != 2 {
		t.Fatalf("bootstrap = %+v", file.BootstrapProfile)
	}
}

func TestProviderConfigRejectsUnsafeShapes(t *testing.T) {
	missing := strings.Replace(sampleConfig(), `"version": 1`, `"version": 2`, 1)
	if _, err := decodeFile(t, missing); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("bad version error = %v", err)
	}
	trailing := sampleConfig() + ` {}`
	if _, err := decodeFile(t, trailing); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing JSON error = %v", err)
	}
	inlineSecret := strings.Replace(sampleConfig(), `"bootstrap_profile":`, `"api_key": "raw-secret", "bootstrap_profile":`, 1)
	if _, err := decodeFile(t, inlineSecret); err == nil {
		t.Fatal("inline raw secret accepted")
	}
	badID := strings.Replace(sampleConfig(), `"id": "codex-app-server"`, `"id": "Bad ID!"`, 1)
	if _, err := decodeFile(t, badID); err == nil || !strings.Contains(err.Error(), "invalid provider id") {
		t.Fatalf("bad provider id error = %v", err)
	}
	duplicate := strings.Replace(sampleConfig(), `"mac-omlx"`, `"codex-app-server"`, 1)
	if _, err := decodeFile(t, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate provider id") {
		t.Fatalf("duplicate id error = %v", err)
	}
	badEnv := strings.Replace(sampleConfig(), `"api_key_env": "DOUBLANGU_OMLX_API_KEY"`, `"api_key_env": "lowercase-key"`, 1)
	if _, err := decodeFile(t, badEnv); err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("bad env name error = %v", err)
	}
	unknownStage := strings.Replace(sampleConfig(), `"translation":`, `"bogus_stage":`, 1)
	if _, err := decodeFile(t, unknownStage); err == nil || !strings.Contains(err.Error(), "missing stage") {
		t.Fatalf("unknown stage error = %v", err)
	}
	unknownProvider := strings.Replace(sampleConfig(), `"mac-omlx"`, `"missing-provider"`, 1)
	if _, err := decodeFile(t, unknownProvider); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("unknown bootstrap provider error = %v", err)
	}
	badModelOptions := strings.Replace(sampleConfig(), `"reasoning_effort": "medium"`, `"reasoning_effort": "medium", "temperature_milli": 0`, 1)
	if _, err := decodeFile(t, badModelOptions); err == nil || !strings.Contains(err.Error(), "codex options") {
		t.Fatalf("bad codex options error = %v", err)
	}
}

func TestProviderConfigRejectsUnsafeURLsAndSecrets(t *testing.T) {
	allowHTTP := true
	base := `{"version":1,"allow_insecure_tailscale_http":` + "true" + `,"providers":[{"id":"p","label":"P","endpoint_label":"E","type":"openai_compatible","enabled":true,"base_url":`

	for _, test := range []struct {
		url   string
		check string
	}{
		{`"http://example.com/v1"`, "DNS name"},
		{`"http://8.8.8.8/v1"`, "literal loopback"},
		{`"https://example.com/v1/"`, ""}, // trailing slash trimmed
		{`"https://user:pass@example.com/v1"`, "user info"},
		{`"https://example.com/v1?x=1"`, "query"},
		{`"https://example.com/v1#f"`, "fragment"},
		{`"https://example.com/v2"`, "exactly /v1"},
		{`"http://127.0.0.1:8899/v1"`, ""},
		{`"http://100.64.7.1:8899/v1"`, ""},
		{`"http://100.127.255.1/v1"`, ""},
		{`"http://100.128.1.1/v1"`, "literal loopback"},
	} {
		raw := base + test.url + `,"api_key_env":"K"}]}`
		file, err := DecodeProviderConfig([]byte(raw), func(key string) string {
			if key == "K" {
				return "secret-value"
			}
			return ""
		})
		if test.check == "" {
			if err != nil {
				t.Errorf("url %s rejected: %v", test.url, err)
			}
			_ = file
			continue
		}
		if err == nil || !strings.Contains(err.Error(), test.check) {
			t.Errorf("url %s error = %v, want %q", test.url, err, test.check)
		}
	}
	_ = allowHTTP
	noHTTP := strings.Replace(sampleConfig(), `"allow_insecure_tailscale_http": true`, `"allow_insecure_tailscale_http": false`, 1)
	if _, err := decodeFile(t, noHTTP); err == nil || !strings.Contains(err.Error(), "http is not allowed") {
		t.Fatalf("http-without-allow error = %v", err)
	}
	missingSecret := strings.Replace(sampleConfig(), `"DOUBLANGU_OMLX_API_KEY"`, `"DOUBLANGU_MISSING_KEY"`, 1)
	if _, err := decodeFile(t, missingSecret); err == nil || !strings.Contains(err.Error(), "missing or blank secret") {
		t.Fatalf("missing secret error = %v", err)
	}
}

func TestCanonicalizeProviderOptions(t *testing.T) {
	codex, err := CanonicalizeProviderOptions(ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"high"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(codex) != `{"reasoning_effort":"high"}` {
		t.Fatalf("codex canonical = %s", codex)
	}
	if _, err := CanonicalizeProviderOptions(ProviderTypeCodexAppServer, json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty codex options accepted")
	}
	if _, err := CanonicalizeProviderOptions(ProviderTypeCodexAppServer, json.RawMessage(`{"reasoning_effort":"x","extra":1}`)); err == nil {
		t.Fatal("unknown codex option accepted")
	}
	omlx, err := CanonicalizeProviderOptions(ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0,"max_output_tokens":16384}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(omlx) != `{"temperature_milli":0,"max_output_tokens":16384}` {
		t.Fatalf("omlx canonical = %s", omlx)
	}
	if _, err := CanonicalizeProviderOptions(ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0}`)); err == nil {
		t.Fatal("missing max_output_tokens accepted")
	}
	if _, err := CanonicalizeProviderOptions(ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":2001,"max_output_tokens":16384}`)); err == nil {
		t.Fatal("out-of-range temperature accepted")
	}
	if _, err := CanonicalizeProviderOptions(ProviderTypeOpenAICompatible, json.RawMessage(`{"temperature_milli":0,"max_output_tokens":1023}`)); err == nil {
		t.Fatal("out-of-range max tokens accepted")
	}
	if _, err := CanonicalizeProviderOptions("unknown-type", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown provider type options accepted")
	}
}

func TestProviderFingerprintIgnoresSecretValue(t *testing.T) {
	left := ProviderEntry{ID: "a", Type: ProviderTypeOpenAICompatible, Enabled: true, BaseURL: "https://host/v1", APIKeyEnv: "K", RequestTimeoutSeconds: 600}
	right := left
	if ProviderConfigFingerprint(left) != ProviderConfigFingerprint(right) {
		t.Fatal("identical entries differ")
	}
	changedType := left
	changedType.Type = ProviderTypeCodexAppServer
	if ProviderConfigFingerprint(left) == ProviderConfigFingerprint(changedType) {
		t.Fatal("type change not reflected in fingerprint")
	}
	changedURL := left
	changedURL.BaseURL = "https://other/v1"
	if ProviderConfigFingerprint(left) == ProviderConfigFingerprint(changedURL) {
		t.Fatal("base url change not reflected in fingerprint")
	}
}

func TestBootstrapBindingsUseRegisteredStages(t *testing.T) {
	file, err := decodeFile(t, sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	var stages []pipeline.StageID
	for stage := range file.BootstrapProfile.Bindings {
		stages = append(stages, stage)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %v", stages)
	}
	ordered := make([]pipeline.StageID, 0, 2)
	for _, stage := range pipeline.RegisteredStages() {
		if _, ok := file.BootstrapProfile.Bindings[stage]; ok {
			ordered = append(ordered, stage)
		}
	}
	if len(ordered) != 2 {
		t.Fatalf("registered stages = %v", ordered)
	}
}

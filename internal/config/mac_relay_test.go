package config

import (
	"strings"
	"testing"
)

func macRelayEntry() string {
	return `{ "id": "mac-omlx", "label": "Mac OMLX relay", "endpoint_label": "Mac relay", "type": "mac_relay", "enabled": true, "request_timeout_seconds": 600 }`
}

func macRelayConfig(extra string) string {
	return `{
		"version": 1,
		"providers": [
			{ "id": "codex-app-server", "label": "Codex app-server", "endpoint_label": "Local Codex", "type": "codex_app_server", "enabled": true },
			` + extra + `
		],
		"bootstrap_profile": {
			"name": "Relay proof",
			"bindings": {
				"linguistic_analysis": { "provider_id": "codex-app-server", "model_id": "m", "options": { "reasoning_effort": "low" } },
				"translation": { "provider_id": "mac-omlx", "model_id": "qwen-test", "options": { "temperature_milli": 0, "max_output_tokens": 16384 } }
			}
		}
	}`
}

func TestMacRelayConfigValidation(t *testing.T) {
	file, err := decodeFile(t, macRelayConfig(macRelayEntry()))
	if err != nil {
		t.Fatalf("mac_relay config rejected: %v", err)
	}
	var relay *ProviderEntry
	for i := range file.Providers {
		if file.Providers[i].ID == "mac-omlx" {
			relay = &file.Providers[i]
		}
	}
	if relay == nil || relay.Type != ProviderTypeMacRelay {
		t.Fatalf("relay entry missing: %+v", file.Providers)
	}
	// Fingerprint covers type/enabled/timeout with blank endpoint/secret.
	fingerprint := ProviderConfigFingerprint(*relay)
	if fingerprint == "" || len(fingerprint) != 64 {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	altered := *relay
	altered.RequestTimeoutSeconds = 300
	if ProviderConfigFingerprint(altered) == fingerprint {
		t.Fatal("timeout change must change the fingerprint")
	}
	// Relay entries must not carry endpoint or secret references.
	for _, extra := range []string{
		`"base_url": "http://127.0.0.1:8899/v1"`,
		`"api_key_env": "DOUBLANGU_OMLX_API_KEY"`,
	} {
		entry := strings.TrimSuffix(macRelayEntry(), " }") + ", " + extra + " }"
		if _, err := decodeFile(t, macRelayConfig(entry)); err == nil {
			t.Errorf("mac_relay with %s accepted", extra)
		}
	}
	// Unknown provider types still fail.
	unknown := strings.Replace(macRelayEntry(), `"mac_relay"`, `"carrier_pigeon"`, 1)
	if _, err := decodeFile(t, macRelayConfig(unknown)); err == nil {
		t.Error("unknown provider type accepted")
	}
}

func TestMacRelayOptionsShareOpenAICompatibleCodec(t *testing.T) {
	valid := `{"temperature_milli": 100, "max_output_tokens": 4096}`
	for _, providerType := range []string{ProviderTypeOpenAICompatible, ProviderTypeMacRelay} {
		canonical, err := CanonicalizeProviderOptions(providerType, []byte(valid))
		if err != nil {
			t.Fatalf("%s valid options rejected: %v", providerType, err)
		}
		if string(canonical) != `{"temperature_milli":100,"max_output_tokens":4096}` {
			t.Fatalf("%s canonical = %s", providerType, canonical)
		}
	}
	for _, raw := range []string{
		`{"temperature_milli": 0}`,
		`{"temperature_milli": 0, "max_output_tokens": 16384, "extra": 1}`,
		`{"temperature_milli": 3000, "max_output_tokens": 16384}`,
		`{"temperature_milli": 0, "max_output_tokens": 128}`,
	} {
		if _, err := CanonicalizeProviderOptions(ProviderTypeMacRelay, []byte(raw)); err == nil {
			t.Errorf("mac_relay accepted %s", raw)
		}
	}
	// Codex options stay exclusive to codex providers.
	if _, err := CanonicalizeProviderOptions(ProviderTypeMacRelay, []byte(`{"reasoning_effort":"low"}`)); err == nil {
		t.Error("mac_relay accepted codex options")
	}
}

package manifest

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiagnosticZeroPluginReadiness(t *testing.T) {
	schema, err := LoadSchema()
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	report := CollectDiagnostics(NewRegistry(), schema)
	if !report.CoreReady || !report.LoaderReady || !report.SchemaAvailable {
		t.Errorf("ready zero-plugin report = %+v", report)
	}
	if report.RegistryState != "empty" || report.PluginCount != 0 || report.RegistrationCount != 0 || len(report.PluginIDs) != 0 {
		t.Errorf("zero-plugin report = %+v", report)
	}

	missingDependencies := CollectDiagnostics(nil, nil)
	if missingDependencies.CoreReady || missingDependencies.LoaderReady || missingDependencies.SchemaAvailable || missingDependencies.RegistryState != "unavailable" {
		t.Errorf("missing dependency report = %+v", missingDependencies)
	}

	missingSchema := CollectDiagnostics(NewRegistry(), nil)
	if !missingSchema.CoreReady || missingSchema.LoaderReady || missingSchema.SchemaAvailable {
		t.Errorf("missing schema report = %+v", missingSchema)
	}
}

func TestDiagnosticPluginIDsAreUniqueAndSorted(t *testing.T) {
	registry := NewRegistry()
	registry.providers["provider-z"] = providerEntry{pluginID: "plugin-z"}
	registry.commands["command-a"] = commandEntry{pluginID: "plugin-a"}
	registry.uis["ui-z"] = uiEntry{pluginID: "plugin-z"}

	report := CollectDiagnostics(registry, &ParsedSchema{})
	if report.PluginCount != 2 || report.RegistrationCount != 3 {
		t.Errorf("counts = plugins=%d registrations=%d, want 2 and 3", report.PluginCount, report.RegistrationCount)
	}
	if want := []string{"plugin-a", "plugin-z"}; !reflect.DeepEqual(report.PluginIDs, want) {
		t.Errorf("plugin IDs = %v, want %v", report.PluginIDs, want)
	}
	if report.RegistryState != "populated" {
		t.Errorf("registry state = %q, want populated", report.RegistryState)
	}
}

func TestDiagnosticJSONAndBannerExposeReadiness(t *testing.T) {
	schema, err := LoadSchema()
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	registry := NewRegistry()
	json := DiagnosticsJSON(registry, schema)
	for _, field := range []string{"core_ready", "loader_ready", "plugin_count", "registration_count"} {
		if !strings.Contains(json, field) {
			t.Errorf("diagnostics JSON missing %q: %s", field, json)
		}
	}
	banner := ZeroPluginBanner(registry, schema)
	for _, line := range []string{"core: ready", "loader: ready", "feature plugins: 0", "registrations: 0"} {
		if !strings.Contains(banner, line) {
			t.Errorf("banner missing %q: %s", line, banner)
		}
	}
}

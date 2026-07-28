package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DiagnosticsReport reports independently useful core and loader readiness.
// PluginCount is the number of unique contributing plugin IDs; RegistrationCount
// is the total number of registrations across the eight registry surfaces.
type DiagnosticsReport struct {
	CoreReady         bool     `json:"core_ready"`
	LoaderReady       bool     `json:"loader_ready"`
	SchemaAvailable   bool     `json:"schema_available"`
	RegistryState     string   `json:"registry_state"`
	PluginCount       int      `json:"plugin_count"`
	RegistrationCount int      `json:"registration_count"`
	PluginIDs         []string `json:"plugin_ids"`
}

// CollectDiagnostics gathers deterministic startup diagnostics. A core can be
// ready with zero plugins; the loader additionally requires its schema and a
// registry transaction target.
func CollectDiagnostics(registry *Registry, schema *ParsedSchema) DiagnosticsReport {
	report := DiagnosticsReport{
		SchemaAvailable: schema != nil,
		CoreReady:       registry != nil,
		LoaderReady:     schema != nil && registry != nil,
	}
	if registry == nil {
		report.RegistryState = "unavailable"
		return report
	}

	report.RegistrationCount = totalRegistrationCount(registry)
	report.PluginIDs = collectPluginIDs(registry)
	report.PluginCount = len(report.PluginIDs)
	if report.PluginCount == 0 {
		report.RegistryState = "empty"
	} else {
		report.RegistryState = "populated"
	}
	return report
}

func totalRegistrationCount(registry *Registry) int {
	return registry.ProviderCount() + registry.TransformerCount() +
		registry.ValidatorCount() + registry.ObserverCount() +
		registry.JobHandlerCount() + registry.EventHandlerCount() +
		registry.CommandCount() + registry.UICount()
}

func collectPluginIDs(registry *Registry) []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	ids := make(map[string]struct{})
	for _, entry := range registry.providers {
		ids[entry.pluginID] = struct{}{}
	}
	for _, entry := range registry.transformers {
		ids[entry.pluginID] = struct{}{}
	}
	for _, entries := range registry.validators {
		for _, entry := range entries {
			ids[entry.pluginID] = struct{}{}
		}
	}
	for _, entry := range registry.observers {
		ids[entry.pluginID] = struct{}{}
	}
	for _, entry := range registry.jobHandlers {
		ids[entry.pluginID] = struct{}{}
	}
	for _, entry := range registry.eventHandlers {
		ids[entry.pluginID] = struct{}{}
	}
	for _, entry := range registry.commands {
		ids[entry.pluginID] = struct{}{}
	}
	for _, entry := range registry.uis {
		ids[entry.pluginID] = struct{}{}
	}

	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// DiagnosticsJSON returns one deterministic JSON object.
func DiagnosticsJSON(registry *Registry, schema *ParsedSchema) string {
	encoded, err := json.Marshal(CollectDiagnostics(registry, schema))
	if err != nil {
		return `{"error":"diagnostics marshal failed"}`
	}
	return string(encoded)
}

// ZeroPluginBanner gives startup operators a compact, human-readable summary.
func ZeroPluginBanner(registry *Registry, schema *ParsedSchema) string {
	report := CollectDiagnostics(registry, schema)
	var banner strings.Builder
	banner.WriteString("=== Doublangu Server ===\n")
	_, _ = fmt.Fprintf(&banner, "core: %s\n", readinessWord(report.CoreReady))
	_, _ = fmt.Fprintf(&banner, "loader: %s\n", readinessWord(report.LoaderReady))
	_, _ = fmt.Fprintf(&banner, "feature plugins: %d\n", report.PluginCount)
	_, _ = fmt.Fprintf(&banner, "registrations: %d\n", report.RegistrationCount)
	return banner.String()
}

func readinessWord(ready bool) string {
	if ready {
		return "ready"
	}
	return "unavailable"
}

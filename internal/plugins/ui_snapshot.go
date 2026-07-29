package manifest

import (
	"fmt"
	"sort"
	"strings"

	v1 "doublangu/pkg/pluginapi/v1"
)

// UIContributionsVersion identifies the wire format consumed by the Svelte UI
// host. The fields deliberately use the Go API's snake_case JSON spelling.
const UIContributionsVersion = "v1"

type UIContributionSnapshot struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Type      string `json:"type"`
	Container string `json:"container"`
	Priority  int    `json:"priority"`
	Icon      string `json:"icon"`
	SourceURL string `json:"source_url"`
	PluginID  string `json:"plugin_id"`
}

type UIContributionsSnapshot struct {
	Version       string                   `json:"version"`
	Contributions []UIContributionSnapshot `json:"contributions"`
}

// UIContributions returns a stable, validated, read-only view of committed UI
// registrations. It intentionally exposes no registry mutation or handlers.
func (r *Registry) UIContributions() (UIContributionsSnapshot, error) {
	r.mu.Lock()
	entries := make([]uiEntry, 0, len(r.uis))
	for _, entry := range r.uis {
		entries = append(entries, entry)
	}
	r.mu.Unlock()

	result := UIContributionsSnapshot{Version: UIContributionsVersion, Contributions: make([]UIContributionSnapshot, 0, len(entries))}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		item := UIContributionSnapshot{ID: entry.id, Label: entry.label, Type: string(entry.uiType), Container: entry.container, Priority: int(entry.priority), Icon: entry.icon, SourceURL: entry.sourceURL, PluginID: entry.pluginID}
		if err := validateUIContribution(item); err != nil {
			return UIContributionsSnapshot{}, err
		}
		if _, exists := seen[item.ID]; exists {
			return UIContributionsSnapshot{}, fmt.Errorf("UI contribution %q: duplicate ID", item.ID)
		}
		seen[item.ID] = struct{}{}
		result.Contributions = append(result.Contributions, item)
	}
	sort.Slice(result.Contributions, func(i, j int) bool {
		left, right := result.Contributions[i], result.Contributions[j]
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		if left.PluginID != right.PluginID {
			return left.PluginID < right.PluginID
		}
		return left.ID < right.ID
	})
	return result, nil
}

func validateUIContribution(item UIContributionSnapshot) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.PluginID) == "" {
		return fmt.Errorf("UI contribution: id and plugin_id are required")
	}
	if strings.TrimSpace(item.Label) == "" {
		return fmt.Errorf("UI contribution %q: label is required", item.ID)
	}
	if item.Type != string(v1.UITypePanel) && item.Type != string(v1.UITypeView) && item.Type != string(v1.UITypeWidget) {
		return fmt.Errorf("UI contribution %q: invalid type %q", item.ID, item.Type)
	}
	if item.Priority < -10000 || item.Priority > 10000 {
		return fmt.Errorf("UI contribution %q: invalid priority", item.ID)
	}
	if !strings.HasPrefix(item.SourceURL, "/api/v1/plugins/assets/") || strings.ContainsAny(item.SourceURL, "?#\\") || strings.Contains(item.SourceURL, "..") {
		return fmt.Errorf("UI contribution %q: unauthorized source_url", item.ID)
	}
	return nil
}

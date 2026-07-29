package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "doublangu/pkg/pluginapi/v1"
)

func TestUIContributionsSerializesDeterministicSnakeCaseSnapshot(t *testing.T) {
	registry := NewRegistry()
	transaction := registry.Begin("plugin.sample")
	for _, registration := range []v1.UIRegistration{
		{ID: "later", Label: "Later", Type: v1.UITypePanel, Priority: 100, SourceURL: "/api/v1/plugins/assets/v1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/module.js"},
		{ID: "first", Label: "First", Type: v1.UITypeView, Priority: 10, SourceURL: "/api/v1/plugins/assets/v1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/module.js"},
	} {
		if err := transaction.AddUI(registration); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	snapshot, err := registry.UIContributions()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != UIContributionsVersion || len(snapshot.Contributions) != 2 || snapshot.Contributions[0].ID != "first" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"version":"v1","contributions":[{"id":"first","label":"First","type":"view","container":"","priority":10,"icon":"","source_url":"/api/v1/plugins/assets/v1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/module.js","plugin_id":"plugin.sample"},{"id":"later","label":"Later","type":"panel","container":"","priority":100,"icon":"","source_url":"/api/v1/plugins/assets/v1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/module.js","plugin_id":"plugin.sample"}]}` {
		t.Fatalf("wire JSON = %s", encoded)
	}
}

func TestUIContributionsRejectsInvalidCommittedEntries(t *testing.T) {
	registry := NewRegistry()
	registry.uis["bad"] = uiEntry{pluginID: "plugin", id: "bad", label: "Bad", uiType: "invalid", sourceURL: "/api/v1/plugins/assets/v1/hash/module.js"}
	_, err := registry.UIContributions()
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("error = %v", err)
	}
}

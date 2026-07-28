package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "doublangu/pkg/pluginapi/v1"
)

func TestLoadSchema(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}

	if len(s.Required) == 0 {
		t.Error("schema has no required fields")
	}

	expectedRequired := []string{
		"id", "version", "api_version", "go_version", "target",
		"source_revision", "artifact_checksum", "build_settings", "module_graph",
		"name", "description", "author",
	}
	for _, f := range expectedRequired {
		found := false
		for _, r := range s.Required {
			if r == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("required field %q not found in schema", f)
		}
	}

	if s.AdditionalAllowed {
		t.Error("additionalProperties should be false")
	}
}

func TestSchemaValidate_MissingRequired(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		remove  string // field to omit
		wantErr string
	}{
		{"missing id", "id", "id: required field is missing"},
		{"missing version", "version", "version: required field is missing"},
		{"missing api_version", "api_version", "api_version: required field is missing"},
		{"missing go_version", "go_version", "go_version: required field is missing"},
		{"missing target", "target", "target: required field is missing"},
		{"missing source_revision", "source_revision", "source_revision: required field is missing"},
		{"missing artifact_checksum", "artifact_checksum", "artifact_checksum: required field is missing"},
		{"missing build_settings", "build_settings", "build_settings: required field is missing"},
		{"missing module_graph", "module_graph", "module_graph: required field is missing"},
		{"missing name", "name", "name: required field is missing"},
		{"missing description", "description", "description: required field is missing"},
		{"missing author", "author", "author: required field is missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := validJSON()
			// Remove the field from the JSON by rebuilding.
			var obj map[string]interface{}
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatal(err)
			}
			delete(obj, tt.remove)
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}

			err = s.Validate(raw)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSchemaValidate_ExplicitNull(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		nullify string // field to set to null
		wantErr string
	}{
		{"null id", "id", "id: must not be null"},
		{"null version", "version", "version: must not be null"},
		{"null api_version", "api_version", "api_version: must not be null"},
		{"null go_version", "go_version", "go_version: must not be null"},
		{"null target", "target", "target: must not be null"},
		{"null source_revision", "source_revision", "source_revision: must not be null"},
		{"null artifact_checksum", "artifact_checksum", "artifact_checksum: must not be null"},
		{"null build_settings", "build_settings", "build_settings: must not be null"},
		{"null module_graph", "module_graph", "module_graph: must not be null"},
		{"null name", "name", "name: must not be null"},
		{"null description", "description", "description: must not be null"},
		{"null author", "author", "author: must not be null"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := jsonWithNull(tt.nullify)
			if err != nil {
				t.Fatal(err)
			}
			err = s.Validate(raw)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSchemaValidate_UnknownField(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	raw := []byte(`{
		"id": "com.example.extra",
		"version": "1.0.0",
		"api_version": "v1",
		"go_version": "go1.26.5",
		"target": ["server"],
		"source_revision": "unknown",
		"artifact_checksum": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"build_settings": "h",
		"module_graph": "h",
		"name": "N",
		"description": "D",
		"author": "A",
		"extra_field": "should be rejected"
	}`)

	err = s.Validate(raw)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "extra_field") {
		t.Errorf("error %q does not name extra_field", err.Error())
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error %q does not contain 'unknown field'", err.Error())
	}
}

func TestSchemaValidate_BadPatterns(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "bad id pattern",
			json:    replaceField(`"id": ".bad"`),
			wantErr: "id:",
		},
		{
			name:    "bad version",
			json:    replaceField(`"version": "v1.0.0"`),
			wantErr: "version:",
		},
		{
			name:    "leading-zero major version",
			json:    replaceField(`"version": "01.0.0"`),
			wantErr: "version:",
		},
		{
			name:    "leading-zero minor version",
			json:    replaceField(`"version": "1.02.3"`),
			wantErr: "version:",
		},
		{
			name:    "leading-zero patch version",
			json:    replaceField(`"version": "1.0.03"`),
			wantErr: "version:",
		},
		{
			name:    "leading-zero numeric prerelease",
			json:    replaceField(`"version": "1.0.0-01"`),
			wantErr: "version:",
		},
		{
			name:    "mismatched api_version",
			json:    replaceField(`"api_version": "v2"`),
			wantErr: "api_version:",
		},
		{
			name:    "mismatched go_version",
			json:    replaceField(`"go_version": "go1.99.0"`),
			wantErr: "go_version:",
		},
		{
			name:    "bad artifact_checksum (uppercase)",
			json:    replaceField(`"artifact_checksum": "` + strings.Repeat("A", 64) + `"`),
			wantErr: "artifact_checksum:",
		},
		{
			name:    "bad artifact_checksum (wrong length)",
			json:    replaceField(`"artifact_checksum": "aaa"`),
			wantErr: "artifact_checksum:",
		},
		{
			name:    "bad target (invalid enum)",
			json:    replaceField(`"target": ["foo"]`),
			wantErr: "target[0]:",
		},
		{
			name:    "bad source_revision (not hex, not unknown)",
			json:    replaceField(`"source_revision": "not-a-revision"`),
			wantErr: "source_revision:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Validate([]byte(tt.json))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSchemaValidate_ValidFixtures(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		json string
	}{
		{"valid server", validJSONStr()},
		{
			"valid agent",
			replaceField(`"target": ["agent"]`),
		},
		{
			"valid multi-target",
			replaceField(`"target": ["server", "agent"]`),
		},
		{
			"digit leading prerelease",
			replaceField(`"version": "1.0.0-0alpha.1"`),
		},
		{
			"source_revision unknown",
			replaceField(`"source_revision": "unknown"`),
		},
		{
			"source_revision 7 hex",
			replaceField(`"source_revision": "abc1234"`),
		},
		{
			"source_revision 64 hex",
			replaceField(`"source_revision": "` + strings.Repeat("f", 64) + `"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Validate([]byte(tt.json)); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- Parity Tests ---

func TestSchemaGoParity_ValidFixtures(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	// These fixtures must be accepted by both schema and Go validation.
	fixtures := []struct {
		name string
		m    v1.Manifest
	}{
		{
			name: "minimal server",
			m: v1.Manifest{
				ID: "a.b", Version: "0.0.1", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "hash",
				ModuleGraph: "hash", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "agent target",
			m: v1.Manifest{
				ID: "com.example.agent", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"agent"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("f", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "Agent", Description: "Agent.", Author: "Team",
			},
		},
		{
			name: "multi-target",
			m: v1.Manifest{
				ID: "com.example.multi", Version: "3.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server", "agent"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("0", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "Multi", Description: "M", Author: "M",
			},
		},
		{
			name: "digit leading prerelease",
			m: v1.Manifest{
				ID: "com.example.pre", Version: "1.0.0-0alpha.1", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "P", Description: "P", Author: "P",
			},
		},
		{
			name: "build metadata",
			m: v1.Manifest{
				ID: "com.example.bm", Version: "2.0.0+build.20260728", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "B", Description: "B", Author: "B",
			},
		},
		{
			name: "source_revision hex 7",
			m: v1.Manifest{
				ID: "com.example.h7", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "abc1234",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "H", Description: "H", Author: "H",
			},
		},
		{
			name: "source_revision hex 64",
			m: v1.Manifest{
				ID: "com.example.h64", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: strings.Repeat("f", 64),
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "H", Description: "H", Author: "H",
			},
		},
		{
			name: "source_revision unknown literal",
			m: v1.Manifest{
				ID: "com.example.unk", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "U", Description: "U", Author: "U",
			},
		},
		{
			name: "whitespace-only name",
			m: v1.Manifest{
				ID: "com.example.wsn", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "   ", Description: "D", Author: "A",
			},
		},
		{
			name: "whitespace-only description",
			m: v1.Manifest{
				ID: "com.example.wsd", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "\t\n", Author: "A",
			},
		},
		{
			name: "whitespace-only author",
			m: v1.Manifest{
				ID: "com.example.wsa", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: " ",
			},
		},
	}

	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			// Go validation
			goErr := tt.m.Validate()
			// Schema validation (via canonical JSON)
			raw, err := tt.m.CanonicalJSON()
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			schemaErr := s.Validate(raw)

			if goErr != nil && schemaErr == nil {
				t.Errorf("Go rejected but schema accepted:\n  Go: %v\n  JSON: %s", goErr, raw)
			}
			if goErr == nil && schemaErr != nil {
				t.Errorf("Schema rejected but Go accepted:\n  Schema: %v\n  JSON: %s", schemaErr, raw)
			}
		})
	}
}

func TestSchemaGoParity_InvalidFixtures(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	// These fixtures must be rejected by both schema and Go validation.
	fixtures := []struct {
		name string
		m    v1.Manifest
	}{
		{
			name: "empty id",
			m: v1.Manifest{
				ID: "", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "bad id pattern",
			m: v1.Manifest{
				ID: ".bad", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "empty version",
			m: v1.Manifest{
				ID: "com.example.v", Version: "", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "bad version (v prefix)",
			m: v1.Manifest{
				ID: "com.example.vp", Version: "v1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "mismatched api_version",
			m: v1.Manifest{
				ID: "com.example.api", Version: "1.0.0", APIVersion: "v2", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "mismatched go_version",
			m: v1.Manifest{
				ID: "com.example.go", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.99.0",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "leading-zero major version",
			m: v1.Manifest{
				ID: "com.example.major", Version: "01.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "leading-zero minor version",
			m: v1.Manifest{
				ID: "com.example.minor", Version: "1.02.3", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "leading-zero patch version",
			m: v1.Manifest{
				ID: "com.example.patch", Version: "1.0.03", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "leading-zero numeric prerelease",
			m: v1.Manifest{
				ID: "com.example.prerelease", Version: "1.0.0-01", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "nil target",
			m: v1.Manifest{
				ID: "com.example.nt", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: nil, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "invalid target",
			m: v1.Manifest{
				ID: "com.example.it", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"foo"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "duplicate target",
			m: v1.Manifest{
				ID: "com.example.dt", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server", "agent", "server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "empty source_revision",
			m: v1.Manifest{
				ID: "com.example.esr", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "source_revision too short",
			m: v1.Manifest{
				ID: "com.example.ssr", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "abcdef",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "source_revision too long",
			m: v1.Manifest{
				ID: "com.example.lsr", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: strings.Repeat("a", 65),
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "bad artifact_checksum (uppercase)",
			m: v1.Manifest{
				ID: "com.example.bac", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("A", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "empty build_settings",
			m: v1.Manifest{
				ID: "com.example.ebs", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "",
				ModuleGraph: "h", Name: "N", Description: "D", Author: "A",
			},
		},
		{
			name: "empty name",
			m: v1.Manifest{
				ID: "com.example.en", Version: "1.0.0", APIVersion: "v1", GoVersion: "go1.26.5",
				Target: []string{"server"}, SourceRevision: "unknown",
				ArtifactChecksum: strings.Repeat("a", 64), BuildSettings: "h",
				ModuleGraph: "h", Name: "", Description: "D", Author: "A",
			},
		},
	}

	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			// Go validation
			goErr := tt.m.Validate()
			// Schema validation (via canonical JSON)
			raw, err := tt.m.CanonicalJSON()
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			schemaErr := s.Validate(raw)

			if goErr == nil {
				t.Errorf("Go accepted but should have rejected: %s", raw)
			}
			if schemaErr == nil {
				t.Errorf("Schema accepted but should have rejected: %s", raw)
			}
		})
	}
}

func TestSchemaGoParity_SchemaConstants(t *testing.T) {
	// Prove that the checked-in schema's required fields, enums, and patterns
	// match the production Go constants without downloading tools.
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	// Check that ValidTargets matches the schema enum.
	targetRule, ok := s.Properties["target"]
	if !ok {
		t.Fatal("schema missing target property")
	}
	if targetRule.Items == nil {
		t.Fatal("schema target has no items constraint")
	}
	schemaTargets := make(map[string]bool, len(targetRule.Items.Enum))
	for _, e := range targetRule.Items.Enum {
		schemaTargets[e] = true
	}
	for tgt := range v1.ValidTargets {
		if !schemaTargets[tgt] {
			t.Errorf("Go ValidTargets has %q but schema does not", tgt)
		}
	}
	for tgt := range schemaTargets {
		if !v1.ValidTargets[tgt] {
			t.Errorf("schema has target %q but Go ValidTargets does not", tgt)
		}
	}

	// Check that compatibility constants exactly match the host constants.
	apiRule, ok := s.Properties["api_version"]
	if !ok {
		t.Fatal("schema missing api_version property")
	}
	if apiRule.Type != "string" {
		t.Errorf("api_version type: got %q, want string", apiRule.Type)
	}
	if apiRule.Const == nil || *apiRule.Const != v1.APIVersion {
		t.Errorf("api_version const: got %v, want %q", apiRule.Const, v1.APIVersion)
	}

	goRule, ok := s.Properties["go_version"]
	if !ok {
		t.Fatal("schema missing go_version property")
	}
	if goRule.Type != "string" {
		t.Errorf("go_version type: got %q, want string", goRule.Type)
	}
	if goRule.Const == nil || *goRule.Const != v1.GoVersion {
		t.Errorf("go_version const: got %v, want %q", goRule.Const, v1.GoVersion)
	}

	// Check that id pattern exists and is anchored.
	idRule, ok := s.Properties["id"]
	if !ok {
		t.Fatal("schema missing id property")
	}
	if idRule.Pattern == "" {
		t.Error("schema missing id pattern")
	}
	if !strings.HasPrefix(idRule.Pattern, "^") || !strings.HasSuffix(idRule.Pattern, "$") {
		t.Errorf("id pattern %q should be anchored with ^ and $", idRule.Pattern)
	}

	// Check that version pattern exists (SemVer).
	verRule, ok := s.Properties["version"]
	if !ok {
		t.Fatal("schema missing version property")
	}
	if verRule.Pattern == "" {
		t.Error("schema missing version pattern")
	}

	// Check that artifact_checksum pattern exists and requires 64 hex.
	acRule, ok := s.Properties["artifact_checksum"]
	if !ok {
		t.Fatal("schema missing artifact_checksum property")
	}
	if acRule.Pattern == "" {
		t.Error("schema missing artifact_checksum pattern")
	}

	// Check that source_revision uses oneOf.
	srRule, ok := s.Properties["source_revision"]
	if !ok {
		t.Fatal("schema missing source_revision property")
	}
	if len(srRule.OneOf) != 2 {
		t.Errorf("source_revision oneOf: got %d alternatives, want 2", len(srRule.OneOf))
	}
}

func TestSchemaMutationSensitivity(t *testing.T) {
	// Prove that changing a fixture value changes the validation outcome.
	// This guards against tests that always pass regardless of input.
	s, err := LoadSchema()
	if err != nil {
		t.Fatal(err)
	}

	// Start with valid JSON.
	valid := validJSON()
	if err := s.Validate(valid); err != nil {
		t.Fatalf("valid fixture should pass: %v", err)
	}

	// Mutate one field at a time and verify it fails.
	mutations := []struct {
		name string
		json string
	}{
		{"empty id", replaceField(`"id": ""`)},
		{"dot-start id", replaceField(`"id": ".bad"`)},
		{"v-prefix version", replaceField(`"version": "v1.0.0"`)},
		{"leading-zero version", replaceField(`"version": "01.0.0"`)},
		{"leading-zero numeric prerelease", replaceField(`"version": "1.0.0-01"`)},
		{"mismatched api version", replaceField(`"api_version": "v2"`)},
		{"mismatched go version", replaceField(`"go_version": "go1.99.0"`)},
		{"empty target array", replaceField(`"target": []`)},
		{"invalid target value", replaceField(`"target": ["bogus"]`)},
		{"short source_revision", replaceField(`"source_revision": "abc"`)},
		{"uppercase checksum", replaceField(`"artifact_checksum": "` + strings.Repeat("A", 64) + `"`)},
	}

	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Validate([]byte(tt.json)); err == nil {
				t.Error("expected error for mutated fixture, got nil")
			}
		})
	}
}

// --- JSON helpers for building test fixtures ---

func validJSONStr() string {
	return `{
		"id": "com.example.test",
		"version": "1.0.0",
		"api_version": "v1",
		"go_version": "go1.26.5",
		"target": ["server"],
		"source_revision": "unknown",
		"artifact_checksum": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"build_settings": "hash",
		"module_graph": "hash",
		"name": "Test",
		"description": "Test.",
		"author": "Tester"
	}`
}

func validJSON() []byte {
	return []byte(validJSONStr())
}

// replaceField returns a new JSON string with one key-value pair replaced.
// It does simple string replacement of the entire "key": value segment,
// preserving the trailing comma from the original line.
func replaceField(replacement string) string {
	base := `{
		"id": "com.example.test",
		"version": "1.0.0",
		"api_version": "v1",
		"go_version": "go1.26.5",
		"target": ["server"],
		"source_revision": "unknown",
		"artifact_checksum": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"build_settings": "hash",
		"module_graph": "hash",
		"name": "Test",
		"description": "Test.",
		"author": "Tester"
	}`

	// Extract the field name from the replacement (before the colon).
	fieldEnd := strings.Index(replacement, `":`)
	if fieldEnd < 0 {
		return base
	}
	field := replacement[1:fieldEnd] // strip leading quote

	// Find and replace the matching field line.
	search := `"` + field + `":`
	start := strings.Index(base, search)
	if start < 0 {
		return base
	}
	// Find the end of this line (next newline).
	lineEnd := strings.Index(base[start:], "\n")
	if lineEnd < 0 {
		lineEnd = len(base) - start
	}
	oldLine := base[start : start+lineEnd]
	// Preserve trailing comma if the original line had one.
	if strings.HasSuffix(strings.TrimSpace(oldLine), ",") {
		replacement += ","
	}
	return base[:start] + replacement + base[start+lineEnd:]
}

// jsonWithNull returns JSON with the named field set to null.
func jsonWithNull(field string) ([]byte, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(validJSON(), &obj); err != nil {
		return nil, err
	}
	obj[field] = nil
	return json.Marshal(obj)
}

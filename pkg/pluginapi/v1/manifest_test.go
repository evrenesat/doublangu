package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManifestValidate_MissingFields(t *testing.T) {
	// validBase is a fully valid manifest used as a starting point.
	validBase := func() Manifest {
		return Manifest{
			ID:               "com.example.test",
			Version:          "1.0.0",
			APIVersion:       "v1",
			GoVersion:        "go1.26.5",
			Target:           []string{"server"},
			SourceRevision:   "abcdef1234567890abcdef1234567890abcdef12",
			ArtifactChecksum: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			BuildSettings:    "abc123",
			ModuleGraph:      "def456",
			Name:             "Test Plugin",
			Description:      "A test plugin.",
			Author:           "Tester",
		}
	}

	tests := []struct {
		name    string
		mutate  func(m *Manifest)
		wantErr string // substring that must appear in the error
	}{
		{
			name:    "empty id",
			mutate:  func(m *Manifest) { m.ID = "" },
			wantErr: "id: must not be empty",
		},
		{
			name:    "bad id pattern (starts with dot)",
			mutate:  func(m *Manifest) { m.ID = ".bad" },
			wantErr: "id:",
		},
		{
			name:    "bad id pattern (too short)",
			mutate:  func(m *Manifest) { m.ID = "a" },
			wantErr: "id:",
		},
		{
			name:    "bad id pattern (special chars)",
			mutate:  func(m *Manifest) { m.ID = "has space" },
			wantErr: "id:",
		},
		{
			name:    "empty version",
			mutate:  func(m *Manifest) { m.Version = "" },
			wantErr: "version: must not be empty",
		},
		{
			name:    "bad version (not SemVer)",
			mutate:  func(m *Manifest) { m.Version = "not-a-version" },
			wantErr: "version:",
		},
		{
			name:    "bad version (v prefix)",
			mutate:  func(m *Manifest) { m.Version = "v1.0.0" },
			wantErr: "version:",
		},
		{
			name:    "bad version (partial)",
			mutate:  func(m *Manifest) { m.Version = "1.0" },
			wantErr: "version:",
		},
		{
			name:    "bad version (leading-zero major)",
			mutate:  func(m *Manifest) { m.Version = "01.0.0" },
			wantErr: "version:",
		},
		{
			name:    "bad version (leading-zero minor)",
			mutate:  func(m *Manifest) { m.Version = "1.02.3" },
			wantErr: "version:",
		},
		{
			name:    "bad version (leading-zero patch)",
			mutate:  func(m *Manifest) { m.Version = "1.0.03" },
			wantErr: "version:",
		},
		{
			name:    "bad version (leading-zero numeric prerelease)",
			mutate:  func(m *Manifest) { m.Version = "1.0.0-01" },
			wantErr: "version:",
		},
		{
			name:    "empty api_version",
			mutate:  func(m *Manifest) { m.APIVersion = "" },
			wantErr: "api_version: must not be empty",
		},
		{
			name:    "mismatched api_version",
			mutate:  func(m *Manifest) { m.APIVersion = "v2" },
			wantErr: "api_version:",
		},
		{
			name:    "empty go_version",
			mutate:  func(m *Manifest) { m.GoVersion = "" },
			wantErr: "go_version: must not be empty",
		},
		{
			name:    "mismatched go_version",
			mutate:  func(m *Manifest) { m.GoVersion = "go1.99.0" },
			wantErr: "go_version:",
		},
		{
			name:    "empty target",
			mutate:  func(m *Manifest) { m.Target = nil },
			wantErr: "target: must not be empty",
		},
		{
			name:    "nil target",
			mutate:  func(m *Manifest) { m.Target = nil },
			wantErr: "target: must not be empty",
		},
		{
			name:    "empty target element",
			mutate:  func(m *Manifest) { m.Target = []string{"server", ""} },
			wantErr: "target[1]: must not be empty",
		},
		{
			name:    "invalid target",
			mutate:  func(m *Manifest) { m.Target = []string{"invalid"} },
			wantErr: "target[0]:",
		},
		{
			name:    "duplicate target",
			mutate:  func(m *Manifest) { m.Target = []string{"server", "agent", "server"} },
			wantErr: "target[2]: duplicate target",
		},
		{
			name:    "empty source_revision",
			mutate:  func(m *Manifest) { m.SourceRevision = "" },
			wantErr: "source_revision: must not be empty",
		},
		{
			name:    "source_revision too short (6 hex)",
			mutate:  func(m *Manifest) { m.SourceRevision = "abcdef" },
			wantErr: "source_revision:",
		},
		{
			name:    "source_revision too long (65 hex)",
			mutate:  func(m *Manifest) { m.SourceRevision = strings.Repeat("a", 65) },
			wantErr: "source_revision:",
		},
		{
			name:    "source_revision with leading dash",
			mutate:  func(m *Manifest) { m.SourceRevision = "-abcdef1234567890" },
			wantErr: "source_revision: must not start with '-'",
		},
		{
			name:    "source_revision with whitespace",
			mutate:  func(m *Manifest) { m.SourceRevision = "abc def12345678901234567890abcd" },
			wantErr: "source_revision: must not contain whitespace",
		},
		{
			name:    "source_revision with control char",
			mutate:  func(m *Manifest) { m.SourceRevision = "abcdef\x001234567890abcdef1234567890" },
			wantErr: "source_revision: must not contain control characters",
		},
		{
			name:    "empty artifact_checksum",
			mutate:  func(m *Manifest) { m.ArtifactChecksum = "" },
			wantErr: "artifact_checksum: must not be empty",
		},
		{
			name:    "bad artifact_checksum (uppercase)",
			mutate:  func(m *Manifest) { m.ArtifactChecksum = strings.Repeat("A", 64) },
			wantErr: "artifact_checksum:",
		},
		{
			name:    "bad artifact_checksum (wrong length)",
			mutate:  func(m *Manifest) { m.ArtifactChecksum = strings.Repeat("a", 63) },
			wantErr: "artifact_checksum:",
		},
		{
			name:    "bad artifact_checksum (non-hex)",
			mutate:  func(m *Manifest) { m.ArtifactChecksum = strings.Repeat("g", 64) },
			wantErr: "artifact_checksum:",
		},
		{
			name:    "empty build_settings",
			mutate:  func(m *Manifest) { m.BuildSettings = "" },
			wantErr: "build_settings: must not be empty",
		},
		{
			name:    "empty module_graph",
			mutate:  func(m *Manifest) { m.ModuleGraph = "" },
			wantErr: "module_graph: must not be empty",
		},
		{
			name:    "empty name",
			mutate:  func(m *Manifest) { m.Name = "" },
			wantErr: "name: must not be empty",
		},
		{
			name:    "empty description",
			mutate:  func(m *Manifest) { m.Description = "" },
			wantErr: "description: must not be empty",
		},
		{
			name:    "empty author",
			mutate:  func(m *Manifest) { m.Author = "" },
			wantErr: "author: must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validBase()
			tt.mutate(&m)
			err := m.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestManifestValidate_ValidCases(t *testing.T) {
	tests := []struct {
		name string
		m    Manifest
	}{
		{
			name: "minimal valid server",
			m: Manifest{
				ID:               "a.b",
				Version:          "0.0.1",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "hash1",
				ModuleGraph:      "hash2",
				Name:             "X",
				Description:      "X",
				Author:           "X",
			},
		},
		{
			name: "valid agent target",
			m: Manifest{
				ID:               "com.example.agent",
				Version:          "2.1.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"agent"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("f", 64),
				BuildSettings:    "abc",
				ModuleGraph:      "def",
				Name:             "Agent Plugin",
				Description:      "Runs on agent.",
				Author:           "Team",
			},
		},
		{
			name: "multi-target server,agent",
			m: Manifest{
				ID:               "com.example.multi",
				Version:          "3.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server", "agent"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("0", 64),
				BuildSettings:    "hash",
				ModuleGraph:      "hash",
				Name:             "Multi",
				Description:      "Multi.",
				Author:           "M",
			},
		},
		{
			name: "digit leading prerelease",
			m: Manifest{
				ID:               "com.example.prerelease",
				Version:          "1.0.0-0alpha.1",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "P",
				Description:      "P",
				Author:           "P",
			},
		},
		{
			name: "SemVer with build metadata",
			m: Manifest{
				ID:               "com.example.buildmeta",
				Version:          "2.0.0+build.20260728",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "B",
				Description:      "B",
				Author:           "B",
			},
		},
		{
			name: "SemVer with prerelease and build",
			m: Manifest{
				ID:               "com.example.full",
				Version:          "10.20.30-rc.1.beta+build.42",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "F",
				Description:      "F",
				Author:           "F",
			},
		},
		{
			name: "source_revision hex (min length 7)",
			m: Manifest{
				ID:               "com.example.hex",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "abc1234",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "H",
				Description:      "H",
				Author:           "H",
			},
		},
		{
			name: "source_revision hex (max length 64)",
			m: Manifest{
				ID:               "com.example.maxhex",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   strings.Repeat("f", 64),
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "MX",
				Description:      "MX",
				Author:           "MX",
			},
		},
		{
			name: "source_revision unknown literal",
			m: Manifest{
				ID:               "com.example.unknown",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "U",
				Description:      "U",
				Author:           "U",
			},
		},
		{
			name: "whitespace-only name",
			m: Manifest{
				ID:               "com.example.wsname",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "   ",
				Description:      "D",
				Author:           "A",
			},
		},
		{
			name: "whitespace-only description",
			m: Manifest{
				ID:               "com.example.wsdesc",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "\t\n",
				Author:           "A",
			},
		},
		{
			name: "whitespace-only author",
			m: Manifest{
				ID:               "com.example.wsauthor",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           " ",
			},
		},
		{
			name: "id with dots dashes underscores",
			m: Manifest{
				ID:               "com.example_plugin-v2.test",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.m.Validate()
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestManifestValidate_InvalidCases(t *testing.T) {
	// Tests that specifically assert the error is for the right field, not a generic message.
	tests := []struct {
		name    string
		m       Manifest
		wantErr string
	}{
		{
			name: "null-like empty id names the id field",
			m: Manifest{
				ID:               "",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "id: must not be empty",
		},
		{
			name: "null-like empty version names the version field",
			m: Manifest{
				ID:               "com.example.v",
				Version:          "",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "version: must not be empty",
		},
		{
			name: "null-like empty api_version names the field",
			m: Manifest{
				ID:               "com.example.av",
				Version:          "1.0.0",
				APIVersion:       "",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "api_version: must not be empty",
		},
		{
			name: "null-like nil target names the target field",
			m: Manifest{
				ID:               "com.example.t",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           nil,
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "target: must not be empty",
		},
		{
			name: "null-like empty source_revision names the field",
			m: Manifest{
				ID:               "com.example.sr",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "source_revision: must not be empty",
		},
		{
			name: "null-like empty artifact_checksum names the field",
			m: Manifest{
				ID:               "com.example.ac",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: "",
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "artifact_checksum: must not be empty",
		},
		{
			name: "null-like empty build_settings names the field",
			m: Manifest{
				ID:               "com.example.bs",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "build_settings: must not be empty",
		},
		{
			name: "null-like empty module_graph names the field",
			m: Manifest{
				ID:               "com.example.mg",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "",
				Name:             "N",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "module_graph: must not be empty",
		},
		{
			name: "null-like empty name names the field",
			m: Manifest{
				ID:               "com.example.n",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "",
				Description:      "D",
				Author:           "A",
			},
			wantErr: "name: must not be empty",
		},
		{
			name: "null-like empty description names the field",
			m: Manifest{
				ID:               "com.example.d",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "",
				Author:           "A",
			},
			wantErr: "description: must not be empty",
		},
		{
			name: "null-like empty author names the field",
			m: Manifest{
				ID:               "com.example.a",
				Version:          "1.0.0",
				APIVersion:       "v1",
				GoVersion:        "go1.26.5",
				Target:           []string{"server"},
				SourceRevision:   "unknown",
				ArtifactChecksum: strings.Repeat("a", 64),
				BuildSettings:    "h",
				ModuleGraph:      "h",
				Name:             "N",
				Description:      "D",
				Author:           "",
			},
			wantErr: "author: must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.m.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestCanonicalJSON_Stable(t *testing.T) {
	m := Manifest{
		ID:               "com.example.stable",
		Version:          "1.0.0",
		APIVersion:       "v1",
		GoVersion:        "go1.26.5",
		Target:           []string{"server", "agent"},
		SourceRevision:   "unknown",
		ArtifactChecksum: strings.Repeat("a", 64),
		BuildSettings:    "hash",
		ModuleGraph:      "hash",
		Name:             "Stable",
		Description:      "Stable manifest.",
		Author:           "Tester",
	}

	b1, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Errorf("CanonicalJSON not stable:\n  first:  %s\n  second: %s", b1, b2)
	}

	// Verify it is valid JSON and fields are present.
	var raw map[string]interface{}
	if err := json.Unmarshal(b1, &raw); err != nil {
		t.Fatalf("CanonicalJSON output is not valid JSON: %v", err)
	}
	requiredFields := []string{"id", "version", "api_version", "go_version", "target",
		"source_revision", "artifact_checksum", "build_settings", "module_graph",
		"name", "description", "author"}
	for _, f := range requiredFields {
		if _, ok := raw[f]; !ok {
			t.Errorf("CanonicalJSON missing field %q", f)
		}
	}
}

func TestCanonicalEquals(t *testing.T) {
	m1 := Manifest{
		ID:               "com.example.eq",
		Version:          "1.0.0",
		APIVersion:       "v1",
		GoVersion:        "go1.26.5",
		Target:           []string{"server"},
		SourceRevision:   "unknown",
		ArtifactChecksum: strings.Repeat("a", 64),
		BuildSettings:    "h",
		ModuleGraph:      "h",
		Name:             "N",
		Description:      "D",
		Author:           "A",
	}

	// Same values, different struct instance.
	m2 := Manifest{
		ID:               "com.example.eq",
		Version:          "1.0.0",
		APIVersion:       "v1",
		GoVersion:        "go1.26.5",
		Target:           []string{"server"},
		SourceRevision:   "unknown",
		ArtifactChecksum: strings.Repeat("a", 64),
		BuildSettings:    "h",
		ModuleGraph:      "h",
		Name:             "N",
		Description:      "D",
		Author:           "A",
	}

	if !CanonicalEquals(m1, m2) {
		t.Error("identical manifests should be canonically equal")
	}
	if !CanonicalEquals(m1, m1) {
		t.Error("self-equality should hold")
	}

	m3 := m1
	m3.Version = "2.0.0"
	if CanonicalEquals(m1, m3) {
		t.Error("manifests with different versions should not be canonically equal")
	}

	m4 := m1
	m4.Target = []string{"agent"}
	if CanonicalEquals(m1, m4) {
		t.Error("manifests with different targets should not be canonically equal")
	}
}

func TestManifestValidate_OneOf_SourceRevision(t *testing.T) {
	// source_revision must be either "unknown" or 7-64 hex characters.
	// These are specific regression cases for the oneOf-style validation.
	validBase := func() Manifest {
		return Manifest{
			ID:               "com.example.oneof",
			Version:          "1.0.0",
			APIVersion:       "v1",
			GoVersion:        "go1.26.5",
			Target:           []string{"server"},
			SourceRevision:   "unknown",
			ArtifactChecksum: strings.Repeat("a", 64),
			BuildSettings:    "h",
			ModuleGraph:      "h",
			Name:             "N",
			Description:      "D",
			Author:           "A",
		}
	}

	t.Run("unknown literal is valid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = "unknown"
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for 'unknown': %v", err)
		}
	})

	t.Run("7-char hex is valid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = "abc1234"
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for 7-char hex: %v", err)
		}
	})

	t.Run("64-char hex is valid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = strings.Repeat("f", 64)
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for 64-char hex: %v", err)
		}
	})

	t.Run("6-char hex is invalid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = "abcdef"
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for 6-char hex")
		}
		if !strings.Contains(err.Error(), "source_revision") {
			t.Errorf("error %q does not contain source_revision", err.Error())
		}
	})

	t.Run("65-char hex is invalid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = strings.Repeat("a", 65)
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for 65-char hex")
		}
		if !strings.Contains(err.Error(), "source_revision") {
			t.Errorf("error %q does not contain source_revision", err.Error())
		}
	})

	t.Run("empty string is invalid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = ""
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for empty source_revision")
		}
		if !strings.Contains(err.Error(), "source_revision") {
			t.Errorf("error %q does not contain source_revision", err.Error())
		}
	})

	t.Run("mixed hex and non-hex is invalid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = "abc1234xyz"
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for non-hex source_revision")
		}
	})

	t.Run("leading dash is invalid", func(t *testing.T) {
		m := validBase()
		m.SourceRevision = "-abc1234567890"
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for leading dash")
		}
		if !strings.Contains(err.Error(), "must not start with '-'") {
			t.Errorf("error %q does not contain must not start with '-'", err.Error())
		}
	})
}

func TestManifestValidate_ArrayItem_Target(t *testing.T) {
	// Specific regression cases for array-item (target) validation.
	validBase := func() Manifest {
		return Manifest{
			ID:               "com.example.target",
			Version:          "1.0.0",
			APIVersion:       "v1",
			GoVersion:        "go1.26.5",
			Target:           []string{"server"},
			SourceRevision:   "unknown",
			ArtifactChecksum: strings.Repeat("a", 64),
			BuildSettings:    "h",
			ModuleGraph:      "h",
			Name:             "N",
			Description:      "D",
			Author:           "A",
		}
	}

	t.Run("server is valid", func(t *testing.T) {
		m := validBase()
		m.Target = []string{"server"}
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for server: %v", err)
		}
	})

	t.Run("agent is valid", func(t *testing.T) {
		m := validBase()
		m.Target = []string{"agent"}
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for agent: %v", err)
		}
	})

	t.Run("server,agent is valid", func(t *testing.T) {
		m := validBase()
		m.Target = []string{"server", "agent"}
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for server,agent: %v", err)
		}
	})

	t.Run("agent,server is valid", func(t *testing.T) {
		m := validBase()
		m.Target = []string{"agent", "server"}
		if err := m.Validate(); err != nil {
			t.Errorf("unexpected error for agent,server: %v", err)
		}
	})

	t.Run("invalid target foo is rejected", func(t *testing.T) {
		m := validBase()
		m.Target = []string{"foo"}
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for invalid target")
		}
		if !strings.Contains(err.Error(), "target[0]") {
			t.Errorf("error %q does not contain target[0]", err.Error())
		}
	})

	t.Run("empty string in target array is rejected", func(t *testing.T) {
		m := validBase()
		m.Target = []string{""}
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for empty target element")
		}
		if !strings.Contains(err.Error(), "target[0]") {
			t.Errorf("error %q does not contain target[0]", err.Error())
		}
	})

	t.Run("duplicate server is rejected", func(t *testing.T) {
		m := validBase()
		m.Target = []string{"server", "agent", "server"}
		err := m.Validate()
		if err == nil {
			t.Fatal("expected error for duplicate target")
		}
		if !strings.Contains(err.Error(), "target[2]") {
			t.Errorf("error %q does not contain target[2]", err.Error())
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error %q does not contain 'duplicate'", err.Error())
		}
	})
}

func TestManifestValidation_DoesNotUseProductionCopy(t *testing.T) {
	// This test verifies that validation goes through the production Validate()
	// method rather than copying production rules into the test.
	// If someone copies the regexes into this test, a change to production
	// rules won't fail here and this test becomes a tautology.
	m := Manifest{
		ID:               "com.example.real",
		Version:          "1.0.0",
		APIVersion:       "v1",
		GoVersion:        "go1.26.5",
		Target:           []string{"server"},
		SourceRevision:   "unknown",
		ArtifactChecksum: strings.Repeat("a", 64),
		BuildSettings:    "h",
		ModuleGraph:      "h",
		Name:             "N",
		Description:      "D",
		Author:           "A",
	}

	// This uses the production path.
	if err := m.Validate(); err != nil {
		t.Fatalf("production Validate() failed on valid manifest: %v", err)
	}

	// Negative control: a manifest that passes our base but fails Validate.
	bad := m
	bad.ID = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for empty ID through production Validate()")
	}
}

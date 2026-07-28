// Package v1 defines the versioned public plugin API for Doublangu.
// Plugins import only this package and must not depend on internal packages.
package v1

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// APIVersion is the current plugin API version. It must match exactly in sidecar
// and embedded manifests.
const APIVersion = "v1"

// GoVersion is the exact Go toolchain version required for native plugin
// compatibility.
const GoVersion = "go1.26.5"

// ValidTargets is the set of allowed host-role targets.
var ValidTargets = map[string]bool{
	"server": true,
	"agent":  true,
}

// Manifest is the typed representation of a native plugin manifest. Every field
// participates in canonical equality: two manifests are equal iff their canonical
// JSON byte representations are identical.
type Manifest struct {
	// Identification
	ID      string `json:"id"`      // stable plugin identifier
	Version string `json:"version"` // strict SemVer

	// Compatibility
	APIVersion       string   `json:"api_version"`       // must equal APIVersion constant
	GoVersion        string   `json:"go_version"`        // must equal GoVersion constant
	Target           []string `json:"target"`            // non-empty, unique, from ValidTargets
	SourceRevision   string   `json:"source_revision"`   // 7-64 hex chars or "unknown"
	ArtifactChecksum string   `json:"artifact_checksum"` // SHA-256 lowercase hex (64 chars)
	BuildSettings    string   `json:"build_settings"`    // canonical build-settings hash
	ModuleGraph      string   `json:"module_graph"`      // canonical module-graph hash

	// Metadata
	Name        string `json:"name"`        // human-readable plugin name
	Description string `json:"description"` // human-readable description
	Author      string `json:"author"`      // plugin author
}

// Validate checks the manifest against production rules and returns a
// field-prefixed error for the first violation, or nil when the manifest is
// valid. This is the single Go validation entry point; tests must route through
// it rather than copying the rules.
func (m Manifest) Validate() error {
	if m.ID == "" {
		return fmt.Errorf("id: must not be empty")
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id: %q does not match required pattern", m.ID)
	}

	if err := validateSemVer(m.Version, "version"); err != nil {
		return err
	}

	if m.APIVersion == "" {
		return fmt.Errorf("api_version: must not be empty")
	}
	if m.APIVersion != APIVersion {
		return fmt.Errorf("api_version: %q must equal %q", m.APIVersion, APIVersion)
	}

	if m.GoVersion == "" {
		return fmt.Errorf("go_version: must not be empty")
	}
	if m.GoVersion != GoVersion {
		return fmt.Errorf("go_version: %q must equal %q", m.GoVersion, GoVersion)
	}

	if len(m.Target) == 0 {
		return fmt.Errorf("target: must not be empty")
	}
	seen := make(map[string]bool, len(m.Target))
	for i, t := range m.Target {
		if t == "" {
			return fmt.Errorf("target[%d]: must not be empty", i)
		}
		if !ValidTargets[t] {
			return fmt.Errorf("target[%d]: %q is not a valid target", i, t)
		}
		if seen[t] {
			return fmt.Errorf("target[%d]: duplicate target %q", i, t)
		}
		seen[t] = true
	}

	if err := validateSourceRevision(m.SourceRevision); err != nil {
		return err
	}

	if m.ArtifactChecksum == "" {
		return fmt.Errorf("artifact_checksum: must not be empty")
	}
	if !sha256Pattern.MatchString(m.ArtifactChecksum) {
		return fmt.Errorf("artifact_checksum: must be 64 lowercase hex characters")
	}

	if m.BuildSettings == "" {
		return fmt.Errorf("build_settings: must not be empty")
	}

	if m.ModuleGraph == "" {
		return fmt.Errorf("module_graph: must not be empty")
	}

	// Metadata fields have no format constraints, only presence.
	if m.Name == "" {
		return fmt.Errorf("name: must not be empty")
	}
	if m.Description == "" {
		return fmt.Errorf("description: must not be empty")
	}
	if m.Author == "" {
		return fmt.Errorf("author: must not be empty")
	}

	return nil
}

// CanonicalJSON returns the manifest serialized with stable field ordering,
// compact formatting (no extra whitespace), and no duplicate keys. Two
// manifests that are semantically identical produce the same bytes.
func (m Manifest) CanonicalJSON() ([]byte, error) {
	// json.Marshal with a struct (not map) gives stable field ordering per the
	// struct definition. Compact formatting omits indentation and extra spaces.
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal: %w", err)
	}
	return b, nil
}

// CanonicalEquals reports whether two manifests are canonically equal by
// comparing their compact JSON representations.
func CanonicalEquals(a, b Manifest) bool {
	ja, err := a.CanonicalJSON()
	if err != nil {
		return false
	}
	jb, err := b.CanonicalJSON()
	if err != nil {
		return false
	}
	return string(ja) == string(jb)
}

// --- patterns ---

// idPattern: alphanumeric plus dots, dashes, underscores; must start and end
// with an alphanumeric character; 1-128 characters.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,126}[a-zA-Z0-9]$`)

// semVerPattern: strict SemVer 2.0.0 MAJOR.MINOR.PATCH with optional
// -PRERELEASE and +BUILD. Core and numeric prerelease identifiers disallow
// leading zeroes; digit-leading alphanumeric prerelease identifiers are valid.
var semVerPattern = regexp.MustCompile(
	`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
		`(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?` +
		`(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`,
)

// sha256Pattern: exactly 64 lowercase hex characters.
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// sourceRevisionPattern: 7-64 ASCII hexadecimal characters.
var sourceRevisionHexPattern = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)

// --- helpers ---

func validateSemVer(v, field string) error {
	if v == "" {
		return fmt.Errorf("%s: must not be empty", field)
	}
	if !semVerPattern.MatchString(v) {
		return fmt.Errorf("%s: %q is not a valid SemVer", field, v)
	}
	return nil
}

func validateSourceRevision(rev string) error {
	if rev == "" {
		return fmt.Errorf("source_revision: must not be empty")
	}
	if rev == "unknown" {
		return nil
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("source_revision: must not start with '-'")
	}
	// Reject control characters
	for _, r := range rev {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("source_revision: must not contain control characters")
		}
	}
	// Reject whitespace
	if strings.ContainsAny(rev, " \t\n\r") {
		return fmt.Errorf("source_revision: must not contain whitespace")
	}
	if !sourceRevisionHexPattern.MatchString(rev) {
		return fmt.Errorf("source_revision: must be 7-64 hex characters or \"unknown\"")
	}
	return nil
}

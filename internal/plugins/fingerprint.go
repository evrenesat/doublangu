// Package manifest provides schema-based manifest validation, plugin loading,
// transactional registries, and the canonical fingerprint model for native
// plugin compatibility.
package manifest

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"
)

// BuildSettingsEntry is a single key-value pair extracted from build info.
// The canonical JSON representation uses lowercase field names.
type BuildSettingsEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ModuleEntry is a single module extracted from build info for canonical
// hashing. Main modules, dependencies, and replacements share this shape.
type ModuleEntry struct {
	Path    string       `json:"path"`
	Version string       `json:"version"`
	Sum     string       `json:"sum,omitempty"`
	Replace *ModuleEntry `json:"replace,omitempty"`
}

// ModuleGraphSnapshot holds the canonical module-graph input: the main
// module and sorted dependency slices.
type ModuleGraphSnapshot struct {
	Main *ModuleEntry  `json:"main,omitempty"`
	Deps []ModuleEntry `json:"deps,omitempty"`
}

// --- Build settings fingerprint ---
// ComputeBuildSettingsHash returns a deterministic SHA-256 hash of the
// canonical sorted build settings. Excluded keys (vcs, vcs.*, -buildmode,
// -ldflags) are filtered out before hashing. Unknown non-VCS keys remain
// significant and participate in the hash.
func ComputeBuildSettingsHash(settings []debug.BuildSetting) string {
	filtered := filterBuildSettings(settings)
	sortBuildSettings(filtered)
	return hashBuildSettings(filtered)
}

// filterBuildSettings removes settings whose key matches an exclusion class.
func filterBuildSettings(settings []debug.BuildSetting) []BuildSettingsEntry {
	result := make([]BuildSettingsEntry, 0, len(settings))
	for _, s := range settings {
		if isExcludedSetting(s.Key) {
			continue
		}
		result = append(result, BuildSettingsEntry{Key: s.Key, Value: s.Value})
	}
	return result
}

// isExcludedSetting reports whether a setting key should be excluded.
func isExcludedSetting(key string) bool {
	// Exact matches.
	if key == "vcs" || key == "-buildmode" || key == "-ldflags" {
		return true
	}
	// vcs.* prefix match.
	if strings.HasPrefix(key, "vcs.") {
		return true
	}
	return false
}

// sortBuildSettings sorts entries by key, then value, for deterministic output.
func sortBuildSettings(settings []BuildSettingsEntry) {
	sort.Slice(settings, func(i, j int) bool {
		if settings[i].Key != settings[j].Key {
			return settings[i].Key < settings[j].Key
		}
		return settings[i].Value < settings[j].Value
	})
}

// hashBuildSettings produces a SHA-256 hash of the compact canonical JSON
// of the filtered, sorted settings slice.
func hashBuildSettings(settings []BuildSettingsEntry) string {
	encoded, err := json.Marshal(settings)
	if err != nil {
		// json.Marshal on a slice of simple structs cannot fail.
		panic(fmt.Sprintf("fingerprint: build settings marshal: %v", err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// --- Module graph fingerprint ---

// ComputeModuleGraphHash returns a deterministic SHA-256 hash of the main
// module, sorted dependencies, and replacement graph. It returns the literal
// "no-deps" only when neither usable main-module nor dependency data exists
// in the provided build info.
func ComputeModuleGraphHash(info *debug.BuildInfo) string {
	if info == nil {
		return "no-deps"
	}
	graph := extractModuleGraph(info)
	if graph.Main == nil && len(graph.Deps) == 0 {
		return "no-deps"
	}
	return hashModuleGraph(graph)
}

// extractModuleGraph reads the main module and dependency tree from
// runtime/debug.BuildInfo into the canonical snapshot shape.
func extractModuleGraph(info *debug.BuildInfo) ModuleGraphSnapshot {
	snapshot := ModuleGraphSnapshot{}
	snapshot.Main = canonicalModuleEntry(&info.Main)
	for _, dep := range info.Deps {
		if entry := canonicalModuleEntry(dep); entry != nil {
			snapshot.Deps = append(snapshot.Deps, *entry)
		}
	}
	// Sort dependencies by every canonical field. This prevents input ordering
	// from changing the graph hash when path and version are equal.
	sort.Slice(snapshot.Deps, func(i, j int) bool {
		return compareModuleEntries(snapshot.Deps[i], snapshot.Deps[j]) < 0
	})
	return snapshot
}

// canonicalModuleEntry converts a debug module into the canonical structure.
// A nil or wholly empty module carries no graph information and is omitted.
func canonicalModuleEntry(module *debug.Module) *ModuleEntry {
	if module == nil {
		return nil
	}
	entry := &ModuleEntry{
		Path:    module.Path,
		Version: module.Version,
		Sum:     module.Sum,
	}
	entry.Replace = canonicalModuleEntry(module.Replace)
	if moduleEntryEmpty(*entry) {
		return nil
	}
	return entry
}

func moduleEntryEmpty(entry ModuleEntry) bool {
	return entry.Path == "" && entry.Version == "" && entry.Sum == "" && entry.Replace == nil
}

// compareModuleEntries orders every canonical field, including replacement
// data. It returns -1, 0, or +1.
func compareModuleEntries(a, b ModuleEntry) int {
	for _, fields := range [][2]string{{a.Path, b.Path}, {a.Version, b.Version}, {a.Sum, b.Sum}} {
		if fields[0] < fields[1] {
			return -1
		}
		if fields[0] > fields[1] {
			return 1
		}
	}
	if a.Replace == nil && b.Replace == nil {
		return 0
	}
	if a.Replace == nil {
		return -1
	}
	if b.Replace == nil {
		return 1
	}
	return compareModuleEntries(*a.Replace, *b.Replace)
}

// hashModuleGraph produces a SHA-256 hash from the compact canonical JSON
// of the module graph snapshot.
func hashModuleGraph(graph ModuleGraphSnapshot) string {
	encoded, err := json.Marshal(graph)
	if err != nil {
		panic(fmt.Sprintf("fingerprint: module graph marshal: %v", err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// --- Target parsing ---

// ParseTarget validates and normalises a target list. Accepted values are
// ["server"], ["agent"], and ["server", "agent"]. The returned slice is
// a copy sorted in canonical order (agent before server, the only non-trivial
// ordering). Empty input, empty members, duplicates, and unknown values are
// rejected.
func ParseTarget(targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("target: must not be empty")
	}

	// Defensive copy.
	input := make([]string, len(targets))
	copy(input, targets)

	// Sort input to detect duplicates and validate.
	sort.Strings(input)

	seen := make(map[string]bool, len(input))
	canonical := make([]string, 0, len(input))
	for _, t := range input {
		if t == "" {
			return nil, fmt.Errorf("target: empty member not allowed")
		}
		if t != "server" && t != "agent" {
			return nil, fmt.Errorf("target: unknown member %q; allowed values are server, agent", t)
		}
		if seen[t] {
			return nil, fmt.Errorf("target: duplicate member %q", t)
		}
		seen[t] = true
		canonical = append(canonical, t)
	}
	return canonical, nil
}

// NormaliseTarget sorts a validated target slice into canonical order.
// The caller must have already validated the input.
func NormaliseTarget(targets []string) []string {
	if len(targets) == 0 {
		return targets
	}
	result := make([]string, len(targets))
	copy(result, targets)
	sort.Strings(result)
	return result
}

// --- Source revision ---

// DetermineSourceRevision computes a source revision using the documented
// precedence:
//
//  1. explicitRevision, when non-empty and valid (7-64 hex chars or "unknown")
//  2. vcs.revision from the artifact build info
//  3. git rev-parse HEAD in gitWorkingDir (when non-empty)
//  4. literal "unknown"
//
// An empty explicitRevision skips to the next precedence level. A non-empty
// explicitRevision that is invalid causes an error.
func DetermineSourceRevision(explicitRevision string, info *debug.BuildInfo, gitWorkingDir string) (string, error) {
	// Precedence 1: explicit revision.
	if explicitRevision != "" {
		if err := validateRevisionInput(explicitRevision); err != nil {
			return "", fmt.Errorf("source_revision: %w", err)
		}
		return explicitRevision, nil
	}

	// Precedence 2: vcs.revision from artifact build info.
	if info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				candidate := setting.Value
				if err := validateRevisionInput(candidate); err == nil {
					return candidate, nil
				}
				// Malformed vcs.revision: fall through.
				break
			}
		}
	}

	// Precedence 3: git rev-parse.
	if gitWorkingDir != "" {
		if rev, err := gitHeadRevision(gitWorkingDir); err == nil {
			return rev, nil
		}
	}

	// Precedence 4: unknown.
	return "unknown", nil
}

// validateRevisionInput checks the documented revision rules: 7-64 ASCII hex
// characters or the literal "unknown". Empty, whitespace, control characters,
// leading dashes, and other formats are rejected.
func validateRevisionInput(rev string) error {
	if rev == "" {
		return fmt.Errorf("must not be empty")
	}
	if rev == "unknown" {
		return nil
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("must not start with '-'")
	}
	for _, r := range rev {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("must not contain control characters")
		}
	}
	if strings.ContainsAny(rev, " \t\n\r") {
		return fmt.Errorf("must not contain whitespace")
	}
	if len(rev) < 7 || len(rev) > 64 {
		return fmt.Errorf("must be 7-64 characters")
	}
	// All characters must be hex.
	for _, r := range rev {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return fmt.Errorf("must contain only hex characters or be \"unknown\"")
		}
	}
	return nil
}

// gitHeadRevision returns the full SHA-1 of HEAD in the given working tree.
func gitHeadRevision(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	rev := strings.TrimSpace(string(out))
	if err := validateRevisionInput(rev); err != nil {
		return "", err
	}
	return rev, nil
}

// --- Final artifact report ---

// ArtifactReport is the final build report returned by the build tool after
// a plugin artifact has been produced.
type ArtifactReport struct {
	Path             string `json:"path"`
	ArtifactChecksum string `json:"artifact_checksum"`
	BuildSettings    string `json:"build_settings"`
	ModuleGraph      string `json:"module_graph"`
	SourceRevision   string `json:"source_revision"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
}

// --- BuildInfo helpers ---

// ReadBuildInfo reads the debug.BuildInfo embedded in the binary at path.
// This works for both executables and plugin (.so) files.
func ReadBuildInfo(path string) (*debug.BuildInfo, error) {
	return buildinfo.ReadFile(path)
}

// ComputeArtifactChecksum returns the SHA-256 hex digest of the file at path.
func ComputeArtifactChecksum(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("artifact checksum: %w", err)
	}
	return sha256Hex(data), nil
}

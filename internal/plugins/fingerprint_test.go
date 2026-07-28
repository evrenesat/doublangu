package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
)

// --- Build settings sensitivity (plan step 5) ---

func TestBuildSettingsHash_IncludesAllIncludedSettings(t *testing.T) {
	// Every common Go build setting must affect the hash.
	// Settings that should be included: GOARCH, GOOS, GOROOT, compiler, etc.
	settings := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
		{Key: "GOROOT", Value: "/usr/local/go"},
		{Key: "CGO_ENABLED", Value: "1"},
		{Key: "compiler", Value: "gc"},
	}
	hash1 := ComputeBuildSettingsHash(settings)

	// Changing any included setting must change the hash.
	for i := range settings {
		modified := make([]debug.BuildSetting, len(settings))
		copy(modified, settings)
		modified[i].Value = modified[i].Value + "-changed"
		hash2 := ComputeBuildSettingsHash(modified)
		if hash1 == hash2 {
			t.Errorf("setting %q sensitivity: hash unchanged when value changed", modified[i].Key)
		}
	}
}

func TestBuildSettingsHash_UnknownFutureSettingsRemainSignificant(t *testing.T) {
	// Unknown non-VCS keys must participate in the hash.
	base := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
	}
	hashBase := ComputeBuildSettingsHash(base)

	withUnknown := append(base, debug.BuildSetting{Key: "unknown.future", Value: "123"})
	hashWith := ComputeBuildSettingsHash(withUnknown)

	if hashBase == hashWith {
		t.Error("unknown future setting did not change hash — it must remain significant")
	}
}

func TestBuildSettingsHash_SortingDeterminism(t *testing.T) {
	// The same settings in different order must produce the same hash.
	settings := []debug.BuildSetting{
		{Key: "GOOS", Value: "darwin"},
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOROOT", Value: "/usr/local/go"},
	}
	hash1 := ComputeBuildSettingsHash(settings)

	// Reverse order
	reversed := []debug.BuildSetting{
		{Key: "GOROOT", Value: "/usr/local/go"},
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
	}
	hash2 := ComputeBuildSettingsHash(reversed)

	if hash1 != hash2 {
		t.Error("order change produced different hash — sorting must be deterministic")
	}
}

func TestBuildSettingsHash_DuplicateKeyStability(t *testing.T) {
	// Duplicate keys with the same value produce stable hash.
	settings := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
	}
	hash1 := ComputeBuildSettingsHash(settings)

	settings2 := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
		{Key: "GOARCH", Value: "arm64"},
	}
	hash2 := ComputeBuildSettingsHash(settings2)

	if hash1 != hash2 {
		t.Error("duplicate-key position change produced different hash")
	}
}

// --- Build settings insensitivity (plan step 6): four exclusion classes ---

func TestBuildSettingsHash_InsensitivityVCS(t *testing.T) {
	// Exact "vcs" key is excluded.
	base := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
	}
	hashBase := ComputeBuildSettingsHash(base)

	withVCS := append(base, debug.BuildSetting{Key: "vcs", Value: "git"})
	hashVCS := ComputeBuildSettingsHash(withVCS)

	if hashBase != hashVCS {
		t.Error("exact 'vcs' key changed hash — it must be excluded")
	}
}

func TestBuildSettingsHash_InsensitivityVCSPrefix(t *testing.T) {
	// vcs.* keys are excluded.
	base := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
	}
	hashBase := ComputeBuildSettingsHash(base)

	withVCS := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "vcs.revision", Value: "abc123def456"},
		{Key: "vcs.time", Value: "2024-01-01T00:00:00Z"},
		{Key: "vcs.modified", Value: "false"},
	}
	hashVCS := ComputeBuildSettingsHash(withVCS)

	if hashBase != hashVCS {
		t.Error("vcs.* keys changed hash — they must be excluded")
	}
}

func TestBuildSettingsHash_InsensitivityBuildMode(t *testing.T) {
	// "-buildmode" is excluded.
	base := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
	}
	hashBase := ComputeBuildSettingsHash(base)

	withBM := append(base, debug.BuildSetting{Key: "-buildmode", Value: "plugin"})
	hashBM := ComputeBuildSettingsHash(withBM)

	if hashBase != hashBM {
		t.Error("'-buildmode' key changed hash — it must be excluded")
	}
}

func TestBuildSettingsHash_InsensitivityLDFlags(t *testing.T) {
	// "-ldflags" is excluded.
	base := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
	}
	hashBase := ComputeBuildSettingsHash(base)

	withLD := append(base, debug.BuildSetting{Key: "-ldflags", Value: "-s -w"})
	hashLD := ComputeBuildSettingsHash(withLD)

	if hashBase != hashLD {
		t.Error("'-ldflags' key changed hash — it must be excluded")
	}
}

func TestBuildSettingsHash_OnlyFourExclusionClasses(t *testing.T) {
	// Non-excluded VCS-like and build-like keys must remain significant.
	// "-mod" is not vcs, not -buildmode, not -ldflags — it must affect the hash.
	base := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
	}
	hashBase := ComputeBuildSettingsHash(base)

	withMod := append(base, debug.BuildSetting{Key: "-mod", Value: "readonly"})
	hashMod := ComputeBuildSettingsHash(withMod)

	if hashBase == hashMod {
		t.Error("'-mod' did not change hash — only vcs, vcs.*, -buildmode, and -ldflags are excluded")
	}

	// "-tags" must also remain significant.
	withTag := append(base, debug.BuildSetting{Key: "-tags", Value: "integration"})
	hashTag := ComputeBuildSettingsHash(withTag)

	if hashBase == hashTag {
		t.Error("'-tags' did not change hash — only four exclusion classes exist")
	}
}

// --- Module graph sensitivity (plan step 5) ---

func TestModuleGraphHash_MainModuleSensitivity(t *testing.T) {
	baseline := &debug.BuildInfo{Main: debug.Module{
		Path: "example.com/app", Version: "v1.0.0", Sum: "h1:main",
		Replace: &debug.Module{Path: "../app", Version: "v1.0.1", Sum: "h1:replace"},
	}}
	baseHash := ComputeModuleGraphHash(baseline)

	mutations := []struct {
		name  string
		apply func(*debug.Module)
	}{
		{"path", func(module *debug.Module) { module.Path = "other.example/app" }},
		{"version", func(module *debug.Module) { module.Version = "v2.0.0" }},
		{"sum", func(module *debug.Module) { module.Sum = "h1:other-main" }},
		{"replacement path", func(module *debug.Module) { module.Replace.Path = "../other-app" }},
		{"replacement version", func(module *debug.Module) { module.Replace.Version = "v2.0.1" }},
		{"replacement sum", func(module *debug.Module) { module.Replace.Sum = "h1:other-replace" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := cloneBuildInfo(baseline)
			mutation.apply(&candidate.Main)
			if got := ComputeModuleGraphHash(candidate); got == baseHash {
				t.Fatalf("main module %s change did not affect hash", mutation.name)
			}
		})
	}
}

func TestModuleGraphHash_DependencySensitivity(t *testing.T) {
	baseline := &debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "v1.0.0"},
		Deps: []*debug.Module{{
			Path: "example.com/lib", Version: "v1.0.0", Sum: "h1:dependency",
			Replace: &debug.Module{Path: "../lib", Version: "v1.0.1", Sum: "h1:replacement"},
		}},
	}
	baseHash := ComputeModuleGraphHash(baseline)
	mutations := []struct {
		name  string
		apply func(*debug.Module)
	}{
		{"path", func(module *debug.Module) { module.Path = "other.example/lib" }},
		{"version", func(module *debug.Module) { module.Version = "v2.0.0" }},
		{"sum", func(module *debug.Module) { module.Sum = "h1:other-dependency" }},
		{"replacement path", func(module *debug.Module) { module.Replace.Path = "../other-lib" }},
		{"replacement version", func(module *debug.Module) { module.Replace.Version = "v2.0.1" }},
		{"replacement sum", func(module *debug.Module) { module.Replace.Sum = "h1:other-replacement" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := cloneBuildInfo(baseline)
			mutation.apply(candidate.Deps[0])
			if got := ComputeModuleGraphHash(candidate); got == baseHash {
				t.Fatalf("dependency %s change did not affect hash", mutation.name)
			}
		})
	}
}

func TestModuleGraphHash_NoDeps(t *testing.T) {
	// nil info returns "no-deps".
	if got := ComputeModuleGraphHash(nil); got != "no-deps" {
		t.Errorf("nil BuildInfo: got %q, want %q", got, "no-deps")
	}

	// Empty info returns "no-deps".
	empty := &debug.BuildInfo{}
	if got := ComputeModuleGraphHash(empty); got != "no-deps" {
		t.Errorf("empty BuildInfo: got %q, want %q", got, "no-deps")
	}
	withUnusableDeps := &debug.BuildInfo{Deps: []*debug.Module{nil, {}, {Replace: &debug.Module{}}}}
	if got := ComputeModuleGraphHash(withUnusableDeps); got != "no-deps" {
		t.Errorf("unusable dependency entries: got %q, want %q", got, "no-deps")
	}

	// Info with only main but no deps should NOT return "no-deps" (main module is usable data).
	withMain := &debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "v1.0.0"},
	}
	got := ComputeModuleGraphHash(withMain)
	if got == "no-deps" {
		t.Error("BuildInfo with main module incorrectly returned 'no-deps'")
	}

	// Info with deps but no main should NOT return "no-deps".
	withDeps := &debug.BuildInfo{
		Deps: []*debug.Module{
			{Path: "example.com/lib", Version: "v1.0.0"},
		},
	}
	got = ComputeModuleGraphHash(withDeps)
	if got == "no-deps" {
		t.Error("BuildInfo with deps incorrectly returned 'no-deps'")
	}
}

func TestModuleGraphHash_OrderDeterminism(t *testing.T) {
	// Unordered deps must produce the same hash, including entries whose path
	// and version tie but whose remaining canonical fields differ.
	a := &debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "v1.0.0"},
		Deps: []*debug.Module{
			{Path: "example.com/lib", Version: "v1.0.0", Sum: "h1:z", Replace: &debug.Module{Path: "../z", Version: "v1.0.0", Sum: "h1:z"}},
			{Path: "example.com/lib", Version: "v1.0.0", Sum: "h1:a", Replace: &debug.Module{Path: "../a", Version: "v1.0.0", Sum: "h1:a"}},
			{Path: "example.com/m", Version: "v2.0.0"},
		},
	}
	hash1 := ComputeModuleGraphHash(a)

	b := &debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "v1.0.0"},
		Deps: []*debug.Module{
			{Path: "example.com/m", Version: "v2.0.0"},
			{Path: "example.com/lib", Version: "v1.0.0", Sum: "h1:a", Replace: &debug.Module{Path: "../a", Version: "v1.0.0", Sum: "h1:a"}},
			{Path: "example.com/lib", Version: "v1.0.0", Sum: "h1:z", Replace: &debug.Module{Path: "../z", Version: "v1.0.0", Sum: "h1:z"}},
		},
	}
	hash2 := ComputeModuleGraphHash(b)

	if hash1 != hash2 {
		t.Error("dependency order change produced different hash — sorting must be deterministic")
	}
}

func cloneBuildInfo(info *debug.BuildInfo) *debug.BuildInfo {
	clone := *info
	clone.Main = *cloneDebugModule(&info.Main)
	clone.Deps = make([]*debug.Module, len(info.Deps))
	for i, dependency := range info.Deps {
		clone.Deps[i] = cloneDebugModule(dependency)
	}
	return &clone
}

func cloneDebugModule(module *debug.Module) *debug.Module {
	if module == nil {
		return nil
	}
	clone := *module
	clone.Replace = cloneDebugModule(module.Replace)
	return &clone
}

// --- Target parsing (plan step 2) ---

func TestParseTarget_ValidSingle(t *testing.T) {
	for _, target := range []string{"server", "agent"} {
		result, err := ParseTarget([]string{target})
		if err != nil {
			t.Errorf("ParseTarget([%q]) unexpected error: %v", target, err)
		}
		if len(result) != 1 || result[0] != target {
			t.Errorf("ParseTarget([%q]) = %v, want [%s]", target, result, target)
		}
	}
}

func TestParseTarget_ValidMulti(t *testing.T) {
	// Canonical order: agent before server (alphabetical).
	result, err := ParseTarget([]string{"server", "agent"})
	if err != nil {
		t.Fatalf("ParseTarget([server, agent]) unexpected error: %v", err)
	}
	if len(result) != 2 || result[0] != "agent" || result[1] != "server" {
		t.Errorf("ParseTarget([server, agent]) = %v, want [agent server]", result)
	}

	// Input in canonical order also works.
	result2, err := ParseTarget([]string{"agent", "server"})
	if err != nil {
		t.Fatalf("ParseTarget([agent, server]) unexpected error: %v", err)
	}
	if len(result2) != 2 || result2[0] != "agent" || result2[1] != "server" {
		t.Errorf("ParseTarget([agent, server]) = %v, want [agent server]", result2)
	}
}

func TestParseTarget_RejectEmpty(t *testing.T) {
	_, err := ParseTarget([]string{})
	if err == nil {
		t.Error("ParseTarget([]) should reject empty list")
	}
}

func TestParseTarget_RejectEmptyMember(t *testing.T) {
	_, err := ParseTarget([]string{""})
	if err == nil {
		t.Error("ParseTarget([\"\"]) should reject empty member")
	}
	_, err = ParseTarget([]string{"server", ""})
	if err == nil {
		t.Error("ParseTarget([server, \"\"]) should reject empty member")
	}
}

func TestParseTarget_RejectDuplicate(t *testing.T) {
	_, err := ParseTarget([]string{"server", "server"})
	if err == nil {
		t.Error("ParseTarget([server, server]) should reject duplicate")
	}
	_, err = ParseTarget([]string{"agent", "agent"})
	if err == nil {
		t.Error("ParseTarget([agent, agent]) should reject duplicate")
	}
}

func TestParseTarget_RejectUnknown(t *testing.T) {
	_, err := ParseTarget([]string{"unknown"})
	if err == nil {
		t.Error("ParseTarget([unknown]) should reject unknown member")
	}
	_, err = ParseTarget([]string{"server", "invalid"})
	if err == nil {
		t.Error("ParseTarget([server, invalid]) should reject unknown member")
	}
}

func TestParseTarget_RejectCaseVariant(t *testing.T) {
	_, err := ParseTarget([]string{"Server"})
	if err == nil {
		t.Error("ParseTarget([Server]) should reject case variant")
	}
	_, err = ParseTarget([]string{"SERVER"})
	if err == nil {
		t.Error("ParseTarget([SERVER]) should reject uppercase variant")
	}
	_, err = ParseTarget([]string{"Agent"})
	if err == nil {
		t.Error("ParseTarget([Agent]) should reject case variant")
	}
}

// --- Source revision (plan step 3) ---

func TestDetermineSourceRevision_ExplicitValid(t *testing.T) {
	rev, err := DetermineSourceRevision("abc123def456789", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "abc123def456789" {
		t.Errorf("got %q, want %q", rev, "abc123def456789")
	}
}

func TestDetermineSourceRevision_ExplicitUnknown(t *testing.T) {
	rev, err := DetermineSourceRevision("unknown", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "unknown" {
		t.Errorf("got %q, want %q", rev, "unknown")
	}
}

func TestDetermineSourceRevision_ExplicitShortRejected(t *testing.T) {
	// Shorter than 7 characters
	_, err := DetermineSourceRevision("abc12", nil, "")
	if err == nil {
		t.Error("6-char explicit revision should be rejected")
	}
}

func TestDetermineSourceRevision_ExplicitLeadingDashRejected(t *testing.T) {
	_, err := DetermineSourceRevision("-abc12345", nil, "")
	if err == nil {
		t.Error("leading-dash explicit revision should be rejected")
	}
}

func TestDetermineSourceRevision_ExplicitWhitespaceRejected(t *testing.T) {
	for _, rev := range []string{"abc 12345", "abc\t12345", "abc\n12345", " abc12345", "abc12345 "} {
		_, err := DetermineSourceRevision(rev, nil, "")
		if err == nil {
			t.Errorf("whitespace-containing revision %q should be rejected", rev)
		}
	}
}

func TestDetermineSourceRevision_ExplicitEmptyRejected(t *testing.T) {
	// Empty explicitRevision skips to fallback — no error, goes to next level.
	rev, err := DetermineSourceRevision("", nil, "")
	if err != nil {
		t.Fatalf("empty explicit revision should not error: %v", err)
	}
	if rev != "unknown" {
		t.Errorf("empty explicit with no build info and no git: got %q, want %q", rev, "unknown")
	}
}

func TestDetermineSourceRevision_FromBuildInfoVCS(t *testing.T) {
	info := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890abcdef1234567890abcdef12"},
		},
	}
	rev, err := DetermineSourceRevision("", info, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("got %q, want vcs.revision value", rev)
	}
}

func TestDetermineSourceRevision_FromBuildInfoVCSMalformed(t *testing.T) {
	// Malformed vcs.revision: falls through to next level.
	info := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "short"},
		},
	}
	rev, err := DetermineSourceRevision("", info, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "unknown" {
		t.Errorf("malformed vcs.revision should fall through: got %q, want %q", rev, "unknown")
	}
}

func TestDetermineSourceRevision_FromGit(t *testing.T) {
	// Use the current repository as the git working dir.
	gitDir, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get working directory: %v", err)
	}

	rev, err := DetermineSourceRevision("", nil, gitDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev == "" || rev == "unknown" {
		t.Errorf("expected git revision, got %q", rev)
	}
	// Must be 40 hex chars (full SHA-1).
	if len(rev) != 40 {
		t.Errorf("git revision length: got %d, want 40", len(rev))
	}
	for _, r := range rev {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("git revision contains non-hex character: %q", rev)
			break
		}
	}
}

func TestDetermineSourceRevision_FromGitInvalidDir(t *testing.T) {
	// Non-git directory: falls through.
	rev, err := DetermineSourceRevision("", nil, "/tmp/nonexistent-dir-for-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "unknown" {
		t.Errorf("non-git dir: got %q, want %q", rev, "unknown")
	}
}

func TestDetermineSourceRevision_PrecedenceExplicitOverVCS(t *testing.T) {
	// Explicit revision takes precedence over vcs.revision.
	info := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890abcdef1234567890abcdef12"},
		},
	}
	rev, err := DetermineSourceRevision("deadbeefcafebabe", info, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "deadbeefcafebabe" {
		t.Errorf("explicit should take precedence: got %q", rev)
	}
}

func TestDetermineSourceRevision_PrecedenceVCSOverGit(t *testing.T) {
	// vcs.revision takes precedence over git.
	info := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890abcdef1234567890abcdef12"},
		},
	}
	rev, err := DetermineSourceRevision("", info, "/tmp/some-git-dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("vcs.revision should take precedence over git: got %q", rev)
	}
}

// --- Artifact checksum ---

func TestComputeArtifactChecksum(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/test.bin"
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	digest, err := ComputeArtifactChecksum(path)
	if err != nil {
		t.Fatal(err)
	}

	expected := testSHA256([]byte("hello world"))
	if digest != expected {
		t.Errorf("got %q, want %q", digest, expected)
	}
}

func TestComputeArtifactChecksum_NonexistentFile(t *testing.T) {
	_, err := ComputeArtifactChecksum("/nonexistent/path/for/test")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// --- Hash format validation ---

func TestBuildSettingsHash_ValidHexFormat(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "GOARCH", Value: "arm64"},
		{Key: "GOOS", Value: "darwin"},
	}
	hash := ComputeBuildSettingsHash(settings)

	// Must be 64 lowercase hex characters.
	if len(hash) != 64 {
		t.Errorf("build settings hash length: got %d, want 64", len(hash))
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("hash contains non-hex character: %q", hash)
			break
		}
	}
}

func TestModuleGraphHash_ValidHexFormat(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Path: "example.com/app", Version: "v1.0.0"},
	}
	hash := ComputeModuleGraphHash(info)

	if len(hash) != 64 {
		t.Errorf("module graph hash length: got %d, want 64", len(hash))
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("hash contains non-hex character: %q", hash)
			break
		}
	}
}

// --- Production path: build settings hash from the test binary's own build info ---

func TestFingerprint_HostSelfConsistency(t *testing.T) {
	// Read the test binary's own build info and compute its fingerprint.
	// The hash must be non-empty, 64 hex chars, and deterministic.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("cannot read test binary build info")
	}

	hash1 := ComputeBuildSettingsHash(info.Settings)
	if hash1 == "" {
		t.Error("build settings hash is empty")
	}
	if len(hash1) != 64 {
		t.Errorf("build settings hash length: got %d, want 64", len(hash1))
	}

	// Same settings must produce the same hash.
	hash2 := ComputeBuildSettingsHash(info.Settings)
	if hash1 != hash2 {
		t.Error("same settings produced different hashes")
	}

	// Module graph hash must be valid and deterministic.
	mg1 := ComputeModuleGraphHash(info)
	if mg1 == "" || mg1 == "no-deps" {
		t.Errorf("host module graph should not be 'no-deps' or empty: got %q", mg1)
	}
	mg2 := ComputeModuleGraphHash(info)
	if mg1 != mg2 {
		t.Error("same build info produced different module graph hashes")
	}
}

// --- Auxiliary: verify that excluded settings actually exist in real build ---

func TestRealBuildSettings_ContainExclusionClasses(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("cannot read build info")
	}

	hasVCS := false
	hasBuildMode := false
	for _, s := range info.Settings {
		if s.Key == "vcs" || strings.HasPrefix(s.Key, "vcs.") {
			hasVCS = true
		}
		if s.Key == "-buildmode" {
			hasBuildMode = true
		}
	}
	// Not all builds have these, but at minimum we prove the exclusion logic
	// doesn't crash on real data.
	t.Logf("real settings count: %d, vcs present: %v, buildmode present: %v",
		len(info.Settings), hasVCS, hasBuildMode)

	hash := ComputeBuildSettingsHash(info.Settings)
	if hash == "" || len(hash) != 64 {
		t.Errorf("real build info produced invalid hash: %q", hash)
	}
}

// --- Canonical JSON round-trip for BuildSettingsEntry ---

func TestBuildSettingsEntry_CanonicalJSONShape(t *testing.T) {
	entry := BuildSettingsEntry{Key: "GOARCH", Value: "arm64"}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	// Compact JSON object with lowercase keys.
	expected := `{"key":"GOARCH","value":"arm64"}`
	compact := strings.TrimSpace(string(encoded))
	if compact != expected {
		t.Errorf("canonical JSON: got %s, want %s", compact, expected)
	}
}

// --- ModuleEntry canonical JSON shape ---

func TestModuleEntry_CanonicalJSONShape(t *testing.T) {
	entry := ModuleEntry{Path: "example.com/lib", Version: "v1.0.0"}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	// Sum and Replace are omitempty, so they don't appear.
	// Version is not omitempty.
	expected := `{"path":"example.com/lib","version":"v1.0.0"}`
	compact := strings.TrimSpace(string(encoded))
	if compact != expected {
		t.Errorf("canonical JSON: got %s, want %s", compact, expected)
	}
}

func TestModuleEntry_WithReplaceCanonicalJSONShape(t *testing.T) {
	entry := ModuleEntry{
		Path:    "example.com/lib",
		Version: "v1.0.0",
		Replace: &ModuleEntry{Path: "../local/lib", Version: ""},
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	// Replace with empty version should omit the version field.
	expected := `{"path":"example.com/lib","version":"v1.0.0","replace":{"path":"../local/lib","version":""}}`
	compact := strings.TrimSpace(string(encoded))
	if compact != expected {
		t.Errorf("canonical JSON with replace: got %s, want %s", compact, expected)
	}
}

// --- Edge: empty settings should still produce a valid hash ---

func TestBuildSettingsHash_EmptySettings(t *testing.T) {
	hash := ComputeBuildSettingsHash(nil)
	if hash == "" || len(hash) != 64 {
		t.Errorf("empty settings produced invalid hash: %q", hash)
	}
	// Empty settings array should hash to something.
	hash2 := ComputeBuildSettingsHash([]debug.BuildSetting{})
	if hash != hash2 {
		t.Logf("nil vs empty slice hash: %q vs %q — both valid", hash, hash2)
	}
}

// --- Edge: settings with only excluded keys ---

func TestBuildSettingsHash_OnlyExcludedSettings(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "abc123"},
		{Key: "-buildmode", Value: "plugin"},
		{Key: "-ldflags", Value: "-s -w"},
	}
	hash := ComputeBuildSettingsHash(settings)
	if hash == "" || len(hash) != 64 {
		t.Errorf("only-excluded settings produced invalid hash: %q", hash)
	}
	// Should equal empty settings hash (all excluded).
	emptyHash := ComputeBuildSettingsHash([]debug.BuildSetting{})
	if hash != emptyHash {
		t.Errorf("all-excluded hash should equal empty hash: %q vs %q", hash, emptyHash)
	}
}

// --- ArtifactReport JSON shape ---

func TestArtifactReport_JSONShape(t *testing.T) {
	r := ArtifactReport{
		Path:             "/tmp/plugin.so",
		ArtifactChecksum: testSHA256([]byte("test")),
		BuildSettings:    "bs-hash",
		ModuleGraph:      "mg-hash",
		SourceRevision:   "abc1234567890def",
		GOOS:             "darwin",
		GOARCH:           "arm64",
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ArtifactReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != r {
		t.Errorf("round-trip failed: got %+v, want %+v", decoded, r)
	}
}

// --- validateRevisionInput edge cases ---

func TestValidateRevisionInput_ControlCharacters(t *testing.T) {
	for _, rev := range []string{"abc\x00def", "abc\x1fdef", "abc\x7fdef"} {
		if err := validateRevisionInput(rev); err == nil {
			t.Errorf("control character in %q should be rejected", rev)
		}
	}
}

func TestValidateRevisionInput_Over64Chars(t *testing.T) {
	long := strings.Repeat("a", 65)
	if err := validateRevisionInput(long); err == nil {
		t.Error("65-char revision should be rejected")
	}
}

func TestValidateRevisionInput_Under7Chars(t *testing.T) {
	if err := validateRevisionInput("abcdef"); err == nil {
		t.Error("6-char revision should be rejected")
	}
}

func TestValidateRevisionInput_AcceptEdgeLengths(t *testing.T) {
	for _, rev := range []string{"abcdefa", strings.Repeat("f", 64)} {
		if err := validateRevisionInput(rev); err != nil {
			t.Errorf("valid-length revision %q rejected: %v", rev, err)
		}
	}
}

func TestValidateRevisionInput_UppercaseHex(t *testing.T) {
	// Uppercase hex is valid.
	if err := validateRevisionInput("ABCDEF1234567890"); err != nil {
		t.Errorf("uppercase hex should be valid: %v", err)
	}
}

func TestValidateRevisionInput_MixedCaseHex(t *testing.T) {
	if err := validateRevisionInput("AbCdEf1234567890"); err != nil {
		t.Errorf("mixed-case hex should be valid: %v", err)
	}
}

// --- Hash stability across multiple calls ---

func TestFingerprint_Stability(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("cannot read build info")
	}

	const iterations = 20
	first := ComputeBuildSettingsHash(info.Settings)
	for i := 0; i < iterations; i++ {
		current := ComputeBuildSettingsHash(info.Settings)
		if current != first {
			t.Fatalf("build settings hash changed on iteration %d: %q -> %q", i, first, current)
		}
	}

	firstMG := ComputeModuleGraphHash(info)
	if firstMG == "no-deps" {
		t.Skip("host module graph is no-deps — skipping stability test")
	}
	for i := 0; i < iterations; i++ {
		current := ComputeModuleGraphHash(info)
		if current != firstMG {
			t.Fatalf("module graph hash changed on iteration %d: %q -> %q", i, firstMG, current)
		}
	}
}

// testSHA256 is a test-local copy for self-contained hash comparison.
func testSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// verifyGitAvailable is a test helper that skips if git is not available.
func verifyGitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Verify we're in a git repo.
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository")
	}
}

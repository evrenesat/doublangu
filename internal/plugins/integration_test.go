package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	v1 "doublangu/pkg/pluginapi/v1"
)

const integrationSubprocessEnv = "DOUBLANGU_INTEGRATION_SUBPROCESS"

type builtIntegrationPlugin struct {
	artifact   string
	sidecar    string
	report     ArtifactReport
	workingDir string
}

type integrationSubprocessRequest struct {
	Artifact string `json:"artifact"`
	Sidecar  string `json:"sidecar"`
	Role     string `json:"role"`
}

type integrationSubprocessResult struct {
	Stage             Stage        `json:"stage"`
	Code              ErrorCode    `json:"code"`
	OpenCalls         int          `json:"open_calls"`
	RegistrationCount int          `json:"registration_count"`
	PluginCount       int          `json:"plugin_count"`
	Embedded          *v1.Manifest `json:"embedded,omitempty"`
}

type countingRealOpener struct{ calls int }

func (opener *countingRealOpener) Open(path string) (NativePlugin, error) {
	opener.calls++
	return realOpener{}.Open(path)
}

// TestIntegration_FullMatrix runs every real native load in a fresh test
// process. Go permits one load of a plugin package path per process, so this
// protocol proves the complete target/host matrix without weakening native
// loading or the production comparator path.
func TestIntegration_FullMatrix(t *testing.T) {
	if encoded := os.Getenv(integrationSubprocessEnv); encoded != "" {
		runIntegrationSubprocess(encoded)
		return
	}
	requireNativePluginHost(t)

	repository := repoRoot(t)
	secondWorkingDir := filepath.Join(repository, "internal")
	if repository == secondWorkingDir {
		t.Fatal("cross-working-directory build selected the same directory twice")
	}
	if info, err := os.Stat(secondWorkingDir); err != nil || !info.IsDir() {
		t.Fatalf("second helper working directory %q is unavailable: %v", secondWorkingDir, err)
	}
	hostInfo, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("cannot read test binary build info")
	}
	raceBuild := isRaceBuild(hostInfo)

	multi := buildIntegrationPlugin(t, "server,agent", true, raceBuild, repository)
	server := buildIntegrationPlugin(t, "server", true, raceBuild, repository)
	agent := buildIntegrationPlugin(t, "agent", true, raceBuild, repository)
	noVCSRoot := buildIntegrationPlugin(t, "server,agent", false, raceBuild, repository)
	noVCSSubdir := buildIntegrationPlugin(t, "server,agent", false, raceBuild, secondWorkingDir)

	for _, plugin := range []builtIntegrationPlugin{multi, server, agent, noVCSRoot, noVCSSubdir} {
		verifyArtifactReportAndSidecar(t, plugin)
	}

	multiSidecar := readSidecarManifest(t, multi.sidecar)
	if got := ComputeBuildSettingsHash(hostInfo.Settings); multiSidecar.BuildSettings != got {
		t.Fatalf("host/plugin build settings differ: sidecar=%q host=%q", multiSidecar.BuildSettings, got)
	}
	if got := ComputeModuleGraphHash(hostInfo); multiSidecar.ModuleGraph != got {
		t.Fatalf("host/plugin module graph differs: sidecar=%q host=%q", multiSidecar.ModuleGraph, got)
	}

	if noVCSRoot.workingDir == noVCSSubdir.workingDir {
		t.Fatal("-buildvcs=false commands used one working directory")
	}
	rootSidecar := readSidecarManifest(t, noVCSRoot.sidecar)
	subdirSidecar := readSidecarManifest(t, noVCSSubdir.sidecar)
	if rootSidecar.BuildSettings != subdirSidecar.BuildSettings || rootSidecar.ModuleGraph != subdirSidecar.ModuleGraph {
		t.Fatalf("-buildvcs=false fingerprints differ across working directories: root=%+v subdir=%+v", rootSidecar, subdirSidecar)
	}
	expectedRevision, err := gitHeadRevision(repository)
	if err != nil {
		t.Fatalf("read helper git fallback revision: %v", err)
	}
	for _, sidecar := range []v1.Manifest{rootSidecar, subdirSidecar} {
		if sidecar.SourceRevision != expectedRevision {
			t.Fatalf("-buildvcs=false source revision = %q, want helper git fallback %q", sidecar.SourceRevision, expectedRevision)
		}
	}
	for _, plugin := range []builtIntegrationPlugin{noVCSRoot, noVCSSubdir} {
		info, err := ReadBuildInfo(plugin.artifact)
		if err != nil {
			t.Fatal(err)
		}
		if hasBuildSetting(info.Settings, "vcs.revision") {
			t.Fatalf("-buildvcs=false final artifact %q retained vcs.revision", plugin.artifact)
		}
	}

	cases := []struct {
		name    string
		plugin  builtIntegrationPlugin
		role    string
		target  []string
		allowed bool
	}{
		{"server only on server", server, "server", []string{"server"}, true},
		{"agent only on agent", agent, "agent", []string{"agent"}, true},
		{"multi target on server", multi, "server", []string{"agent", "server"}, true},
		{"multi target on agent", multi, "agent", []string{"agent", "server"}, true},
		{"server only on agent", server, "agent", []string{"server"}, false},
		{"agent only on server", agent, "server", []string{"agent"}, false},
		{"buildvcs false root real load", noVCSRoot, "server", []string{"agent", "server"}, true},
		{"buildvcs false subdir real load", noVCSSubdir, "server", []string{"agent", "server"}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := runIntegrationCase(t, test.plugin, test.role)
			assertIntegrationCase(t, result, readSidecarManifest(t, test.plugin.sidecar), test.target, test.allowed)
		})
	}
}

func requireNativePluginHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("plugin integration tests require darwin or linux, got %s", runtime.GOOS)
	}
	if runtime.GOARCH != "arm64" {
		t.Skipf("plugin integration tests require arm64, got %s", runtime.GOARCH)
	}
}

func buildIntegrationPlugin(t *testing.T, target string, buildVCS, raceBuild bool, workingDir string) builtIntegrationPlugin {
	t.Helper()
	directory := t.TempDir()
	helperPath := filepath.Join(directory, "pluginbuild")
	helperBuild := exec.Command("go", "build", "-o", helperPath, "doublangu/tools/pluginbuild")
	helperBuild.Dir = repoRoot(t)
	if output, err := helperBuild.CombinedOutput(); err != nil {
		t.Fatalf("build pluginbuild: %v\n%s", err, output)
	}
	args := []string{
		"-src", "doublangu/plugins/official/sample",
		"-out", directory,
		"-name", "sample",
		"-target", target,
		fmt.Sprintf("-buildvcs=%t", buildVCS),
	}
	if raceBuild {
		args = append(args, "-race")
	}
	command := exec.Command(helperPath, args...)
	command.Dir = workingDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("pluginbuild target=%s buildvcs=%t race=%t: %v\n%s", target, buildVCS, raceBuild, err, output)
	}
	var report ArtifactReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("decode helper ArtifactReport %q: %v", output, err)
	}
	return builtIntegrationPlugin{
		artifact:   filepath.Join(directory, "sample.so"),
		sidecar:    filepath.Join(directory, "sample.so.json"),
		report:     report,
		workingDir: workingDir,
	}
}

func verifyArtifactReportAndSidecar(t *testing.T, plugin builtIntegrationPlugin) {
	t.Helper()
	sidecar := readSidecarManifest(t, plugin.sidecar)
	checksum, err := ComputeArtifactChecksum(plugin.artifact)
	if err != nil {
		t.Fatal(err)
	}
	buildInfo, err := ReadBuildInfo(plugin.artifact)
	if err != nil {
		t.Fatal(err)
	}
	if plugin.report.Path != plugin.artifact {
		t.Fatalf("ArtifactReport path = %q, want final artifact %q", plugin.report.Path, plugin.artifact)
	}
	if plugin.report.ArtifactChecksum != checksum {
		t.Fatalf("ArtifactReport checksum = %q, want final artifact checksum %q", plugin.report.ArtifactChecksum, checksum)
	}
	if want := ComputeBuildSettingsHash(buildInfo.Settings); plugin.report.BuildSettings != want {
		t.Fatalf("ArtifactReport build settings = %q, want final artifact value %q", plugin.report.BuildSettings, want)
	}
	if want := ComputeModuleGraphHash(buildInfo); plugin.report.ModuleGraph != want {
		t.Fatalf("ArtifactReport module graph = %q, want final artifact value %q", plugin.report.ModuleGraph, want)
	}
	if plugin.report.SourceRevision != sidecar.SourceRevision {
		t.Fatalf("ArtifactReport source revision = %q, want sidecar value %q", plugin.report.SourceRevision, sidecar.SourceRevision)
	}
	if want := buildInfoSetting(buildInfo.Settings, "GOOS"); plugin.report.GOOS != want {
		t.Fatalf("ArtifactReport GOOS = %q, want final artifact value %q", plugin.report.GOOS, want)
	}
	if want := buildInfoSetting(buildInfo.Settings, "GOARCH"); plugin.report.GOARCH != want {
		t.Fatalf("ArtifactReport GOARCH = %q, want final artifact value %q", plugin.report.GOARCH, want)
	}
	if sidecar.ArtifactChecksum != checksum || sidecar.BuildSettings != plugin.report.BuildSettings || sidecar.ModuleGraph != plugin.report.ModuleGraph {
		t.Fatalf("sidecar does not describe final artifact: %+v", sidecar)
	}
}

func runIntegrationCase(t *testing.T, plugin builtIntegrationPlugin, role string) integrationSubprocessResult {
	t.Helper()
	requestBytes, err := json.Marshal(integrationSubprocessRequest{Artifact: plugin.artifact, Sidecar: plugin.sidecar, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestIntegration_FullMatrix$")
	command.Env = append(os.Environ(), integrationSubprocessEnv+"="+string(requestBytes))
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("integration subprocess: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("integration subprocess stderr: %s", stderr.String())
	}
	var result integrationSubprocessResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode integration subprocess %q: %v", stdout.String(), err)
	}
	return result
}

func runIntegrationSubprocess(encoded string) {
	var request integrationSubprocessRequest
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		integrationSubprocessFailure(fmt.Errorf("decode request: %w", err))
	}
	schema, err := LoadSchema()
	if err != nil {
		integrationSubprocessFailure(fmt.Errorf("load schema: %w", err))
	}
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		integrationSubprocessFailure(fmt.Errorf("read test binary build info"))
	}
	opener := &countingRealOpener{}
	registry := NewRegistry()
	result := Load(context.Background(), request.Artifact, request.Sidecar, LoaderConfig{
		Host:     integrationHostIdentity(request.Role, buildInfo),
		Schema:   schema,
		Registry: registry,
		Opener:   opener,
		Services: HostServices{},
	})
	response := integrationSubprocessResult{
		Stage:             result.Stage,
		Code:              result.Code,
		OpenCalls:         opener.calls,
		RegistrationCount: CollectDiagnostics(registry, schema).RegistrationCount,
		PluginCount:       CollectDiagnostics(registry, schema).PluginCount,
	}
	if result.Plugin != nil {
		embedded := result.Plugin.Manifest()
		response.Embedded = &embedded
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		integrationSubprocessFailure(fmt.Errorf("encode response: %w", err))
	}
	os.Exit(0)
}

func integrationSubprocessFailure(err error) {
	fmt.Fprintln(os.Stderr, "integration subprocess:", err)
	os.Exit(2)
}

func integrationHostIdentity(role string, info *debug.BuildInfo) HostIdentity {
	return HostIdentity{
		APIVersion:    v1.APIVersion,
		GoVersion:     v1.GoVersion,
		Role:          role,
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		BuildSettings: ComputeBuildSettingsHash(info.Settings),
		ModuleGraph:   ComputeModuleGraphHash(info),
	}
}

func assertIntegrationCase(t *testing.T, result integrationSubprocessResult, sidecar v1.Manifest, target []string, allowed bool) {
	t.Helper()
	if got := strings.Join(sidecar.Target, ","); got != strings.Join(target, ",") {
		t.Fatalf("sidecar target = %v, want %v", sidecar.Target, target)
	}
	if !allowed {
		if result.Stage != StageCompatibility || result.Code != ErrorCode(StageCompatibility) {
			t.Fatalf("disallowed role result = %+v, want compatibility rejection", result)
		}
		if result.OpenCalls != 0 || result.RegistrationCount != 0 || result.PluginCount != 0 || result.Embedded != nil {
			t.Fatalf("disallowed role crossed native boundary: %+v", result)
		}
		return
	}
	if result.Stage != StageComplete || result.Code != CodeOK {
		t.Fatalf("allowed role result = %+v, want complete", result)
	}
	if result.OpenCalls != 1 || result.RegistrationCount != 1 || result.PluginCount != 1 || result.Embedded == nil {
		t.Fatalf("allowed role did not open once and register once: %+v", result)
	}
	if got := strings.Join(result.Embedded.Target, ","); got != strings.Join(target, ",") {
		t.Fatalf("loaded plugin target = %v, want %v", result.Embedded.Target, target)
	}
	if !manifestEqualExcludingChecksum(*result.Embedded, sidecar) {
		t.Fatalf("loaded embedded manifest does not match sidecar except checksum: embedded=%+v sidecar=%+v", *result.Embedded, sidecar)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	command := exec.Command("go", "env", "GOMOD")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("determine repository root: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(output)))
}

func isRaceBuild(info *debug.BuildInfo) bool {
	return hasBuildSetting(info.Settings, "-race") && buildInfoSetting(info.Settings, "-race") == "true"
}

func hasBuildSetting(settings []debug.BuildSetting, key string) bool {
	for _, setting := range settings {
		if setting.Key == key {
			return true
		}
	}
	return false
}

func buildInfoSetting(settings []debug.BuildSetting, key string) string {
	for _, setting := range settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return "unknown"
}

func readSidecarManifest(t *testing.T, path string) v1.Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar %s: %v", path, err)
	}
	var manifest v1.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	return manifest
}

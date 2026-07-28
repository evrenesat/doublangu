package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"plugin"
	"reflect"
	"strings"
	"testing"

	v1 "doublangu/pkg/pluginapi/v1"
)

type callTrace []string

func (trace *callTrace) add(call string) { *trace = append(*trace, call) }

type traceOpener struct {
	trace      *callTrace
	native     NativePlugin
	err        error
	panicValue any
}

func (opener traceOpener) Open(string) (NativePlugin, error) {
	opener.trace.add("open")
	if opener.panicValue != nil {
		panic(opener.panicValue)
	}
	return opener.native, opener.err
}

type traceNativePlugin struct {
	trace      *callTrace
	symbol     plugin.Symbol
	err        error
	panicValue any
}

func (native traceNativePlugin) Lookup(string) (plugin.Symbol, error) {
	native.trace.add("lookup")
	if native.panicValue != nil {
		panic(native.panicValue)
	}
	return native.symbol, native.err
}

type staticInspector struct {
	info       ArtifactInfo
	err        error
	panicValue any
}

func (inspector staticInspector) Inspect(string) (ArtifactInfo, error) {
	if inspector.panicValue != nil {
		panic(inspector.panicValue)
	}
	return inspector.info, inspector.err
}

type traceComparator struct {
	trace      *callTrace
	equal      bool
	panicValue any
}

func (comparator traceComparator) Equal(v1.Manifest, v1.Manifest) bool {
	comparator.trace.add("compare")
	if comparator.panicValue != nil {
		panic(comparator.panicValue)
	}
	return comparator.equal
}

type traceRegistrar struct {
	trace      *callTrace
	err        error
	panicValue any
	delegate   bool
}

func (registrar traceRegistrar) Register(ctx context.Context, registry *Registry, id string, loaded v1.Plugin, services HostServices) error {
	registrar.trace.add("register")
	if registrar.panicValue != nil {
		panic(registrar.panicValue)
	}
	if registrar.err != nil {
		return registrar.err
	}
	if registrar.delegate {
		return registryRegistrar{}.Register(ctx, registry, id, loaded, services)
	}
	return nil
}

type fixturePlugin struct {
	manifest      v1.Manifest
	trace         *callTrace
	manifestPanic any
	register      func(context.Context, v1.Host) error
}

func (loaded *fixturePlugin) Manifest() v1.Manifest {
	loaded.trace.add("manifest")
	if loaded.manifestPanic != nil {
		panic(loaded.manifestPanic)
	}
	return loaded.manifest
}

func (loaded *fixturePlugin) Register(ctx context.Context, host v1.Host) error {
	if loaded.register != nil {
		return loaded.register(ctx, host)
	}
	return nil
}

type commandHandler struct{}

func (commandHandler) Execute(context.Context, v1.CommandInput) (v1.CommandOutput, error) {
	return v1.CommandOutput{}, nil
}

type loaderFixture struct {
	manifest   v1.Manifest
	artifact   string
	sidecar    string
	config     LoaderConfig
	trace      *callTrace
	plugin     *fixturePlugin
	native     *traceNativePlugin
	comparator *traceComparator
	registrar  *traceRegistrar
}

func newLoaderFixture(t *testing.T) *loaderFixture {
	t.Helper()
	schema, err := LoadSchema()
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	trace := callTrace{}
	manifest := testManifest()
	artifactBytes := []byte("deterministic loader fixture artifact")
	manifest.ArtifactChecksum = sha256Hex(artifactBytes)
	directory := t.TempDir()
	artifact := filepath.Join(directory, "fixture.so")
	if err := os.WriteFile(artifact, artifactBytes, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	sidecar := filepath.Join(directory, "fixture.json")
	writeManifest(t, sidecar, manifest)

	fixture := &loaderFixture{
		manifest: manifest,
		artifact: artifact,
		sidecar:  sidecar,
		trace:    &trace,
		config: LoaderConfig{
			Host: HostIdentity{
				APIVersion:    v1.APIVersion,
				GoVersion:     v1.GoVersion,
				Role:          "server",
				GOOS:          "linux",
				GOARCH:        "arm64",
				BuildSettings: manifest.BuildSettings,
				ModuleGraph:   manifest.ModuleGraph,
			},
			Schema:    schema,
			Registry:  NewRegistry(),
			Inspector: staticInspector{info: ArtifactInfo{GOOS: "linux", GOARCH: "arm64"}},
		},
	}
	fixture.installGoodPostOpenPath()
	return fixture
}

func (fixture *loaderFixture) installGoodPostOpenPath() {
	fixture.plugin = &fixturePlugin{manifest: fixture.manifest, trace: fixture.trace}
	fixture.native = &traceNativePlugin{trace: fixture.trace, symbol: v1.Plugin(fixture.plugin)}
	fixture.comparator = &traceComparator{trace: fixture.trace, equal: true}
	fixture.registrar = &traceRegistrar{trace: fixture.trace, delegate: true}
	fixture.config.Opener = traceOpener{trace: fixture.trace, native: fixture.native}
	fixture.config.Comparator = fixture.comparator
	fixture.config.Registrar = fixture.registrar
}

func testManifest() v1.Manifest {
	return v1.Manifest{
		ID:             "test-plugin",
		Version:        "1.0.0",
		APIVersion:     v1.APIVersion,
		GoVersion:      v1.GoVersion,
		Target:         []string{"server"},
		SourceRevision: "abcdef1234567890abcdef1234567890abcdef12",
		BuildSettings:  "build-settings-hash",
		ModuleGraph:    "module-graph-hash",
		Name:           "Test Plugin",
		Description:    "Loader test fixture",
		Author:         "Doublangu tests",
	}
}

func writeManifest(t *testing.T, path string, manifest v1.Manifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

func writeRawSidecar(t *testing.T, fixture *loaderFixture, raw []byte) {
	t.Helper()
	if err := os.WriteFile(fixture.sidecar, raw, 0o600); err != nil {
		t.Fatalf("write raw sidecar: %v", err)
	}
}

func assertFixtureLoad(t *testing.T, fixture *loaderFixture, result LoadResult, stage Stage, want callTrace) {
	t.Helper()
	if result.Stage != stage {
		t.Errorf("stage = %q, want %q (error: %v)", result.Stage, stage, result.Err)
	}
	wantCode := ErrorCode(stage)
	if stage == StageComplete {
		wantCode = CodeOK
	}
	if result.Code != wantCode {
		t.Errorf("code = %q, want %q", result.Code, wantCode)
	}
	if stage == StageComplete {
		if result.Err != nil || result.Plugin == nil {
			t.Errorf("success = %+v", result)
		}
	} else if result.Err == nil || result.Plugin != nil {
		t.Errorf("failure = %+v", result)
	}
	if !sameTrace(*fixture.trace, want) {
		t.Errorf("observed trace = %v, want %v", *fixture.trace, want)
	}
}

func sameTrace(got, want callTrace) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestLoaderPreOpenFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *loaderFixture)
		stage Stage
	}{
		{
			name: "sidecar read", stage: StageSidecarRead,
			setup: func(_ *testing.T, fixture *loaderFixture) {
				fixture.sidecar = filepath.Join(t.TempDir(), "missing.json")
			},
		},
		{
			name: "schema unavailable", stage: StageSchema,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Schema = nil },
		},
		{
			name: "malformed JSON", stage: StageSchema,
			setup: func(t *testing.T, fixture *loaderFixture) { writeRawSidecar(t, fixture, []byte("{")) },
		},
		{
			name: "unknown key", stage: StageSchema,
			setup: func(t *testing.T, fixture *loaderFixture) {
				raw, _ := json.Marshal(fixture.manifest)
				writeRawSidecar(t, fixture, append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"unknown":true}`)...))
			},
		},
		{
			name: "duplicate key", stage: StageSchema,
			setup: func(t *testing.T, fixture *loaderFixture) {
				raw, _ := json.Marshal(fixture.manifest)
				writeRawSidecar(t, fixture, append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"id":"duplicate"}`)...))
			},
		},
		{
			name: "null", stage: StageSchema,
			setup: func(t *testing.T, fixture *loaderFixture) {
				raw, _ := json.Marshal(fixture.manifest)
				writeRawSidecar(t, fixture, bytes.Replace(raw, []byte(`"name":"Test Plugin"`), []byte(`"name":null`), 1))
			},
		},
		{
			name: "manifest API version", stage: StageSchema,
			setup: func(t *testing.T, fixture *loaderFixture) {
				fixture.manifest.APIVersion = "v999"
				writeManifest(t, fixture.sidecar, fixture.manifest)
			},
		},
		{
			name: "manifest Go version", stage: StageSchema,
			setup: func(t *testing.T, fixture *loaderFixture) {
				fixture.manifest.GoVersion = "go9.9.9"
				writeManifest(t, fixture.sidecar, fixture.manifest)
			},
		},
		{
			name: "checksum", stage: StageChecksum,
			setup: func(t *testing.T, fixture *loaderFixture) {
				if err := os.WriteFile(fixture.artifact, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "host API version", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.APIVersion = "v999" },
		},
		{
			name: "host Go version", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.GoVersion = "go9.9.9" },
		},
		{
			name: "role", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.Role = "agent" },
		},
		{
			name: "artifact GOOS", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) {
				fixture.config.Inspector = staticInspector{info: ArtifactInfo{GOOS: "darwin", GOARCH: "arm64"}}
			},
		},
		{
			name: "artifact GOARCH", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) {
				fixture.config.Inspector = staticInspector{info: ArtifactInfo{GOOS: "linux", GOARCH: "amd64"}}
			},
		},
		{
			name: "build settings hash", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.BuildSettings = "other" },
		},
		{
			name: "module graph hash", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.ModuleGraph = "other" },
		},
		{
			name: "missing expected build settings hash", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.BuildSettings = "" },
		},
		{
			name: "missing expected module graph hash", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) { fixture.config.Host.ModuleGraph = "" },
		},
		{
			name: "unreadable artifact format", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) {
				fixture.config.Inspector = staticInspector{err: errors.New("unknown format")}
			},
		},
		{
			name: "inspector panic", stage: StageCompatibility,
			setup: func(_ *testing.T, fixture *loaderFixture) {
				fixture.config.Inspector = staticInspector{panicValue: "inspect"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLoaderFixture(t)
			test.setup(t, fixture)
			assertFixtureLoad(t, fixture, Load(context.Background(), fixture.artifact, fixture.sidecar, fixture.config), test.stage, nil)
		})
	}
}

func TestLoaderPostOpenFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*loaderFixture)
		stage Stage
		trace callTrace
	}{
		{"open error", func(f *loaderFixture) { f.config.Opener = traceOpener{trace: f.trace, err: errors.New("open failed")} }, StageOpen, callTrace{"open"}},
		{"open panic", func(f *loaderFixture) { f.config.Opener = traceOpener{trace: f.trace, panicValue: "open"} }, StageOpen, callTrace{"open"}},
		{"nil native plugin", func(f *loaderFixture) { f.config.Opener = traceOpener{trace: f.trace} }, StageOpen, callTrace{"open"}},
		{"lookup error", func(f *loaderFixture) { f.native.err = errors.New("lookup failed") }, StageLookup, callTrace{"open", "lookup"}},
		{"lookup panic", func(f *loaderFixture) { f.native.panicValue = "lookup" }, StageLookup, callTrace{"open", "lookup"}},
		{"wrong symbol", func(f *loaderFixture) { f.native.symbol = "wrong" }, StageSymbolContract, callTrace{"open", "lookup"}},
		{"nil interface symbol", func(f *loaderFixture) { var loaded v1.Plugin; f.native.symbol = loaded }, StageSymbolContract, callTrace{"open", "lookup"}},
		{"typed nil direct symbol", func(f *loaderFixture) { var loaded *fixturePlugin; f.native.symbol = v1.Plugin(loaded) }, StageSymbolContract, callTrace{"open", "lookup"}},
		{"nil Plugin pointer", func(f *loaderFixture) { var loaded *v1.Plugin; f.native.symbol = loaded }, StageSymbolContract, callTrace{"open", "lookup"}},
		{"nil inner Plugin", func(f *loaderFixture) { var loaded v1.Plugin; f.native.symbol = &loaded }, StageSymbolContract, callTrace{"open", "lookup"}},
		{"typed nil inner Plugin", func(f *loaderFixture) { var loaded v1.Plugin = (*fixturePlugin)(nil); f.native.symbol = &loaded }, StageSymbolContract, callTrace{"open", "lookup"}},
		{"manifest panic", func(f *loaderFixture) { f.plugin.manifestPanic = "manifest" }, StageEmbeddedEquality, callTrace{"open", "lookup", "manifest"}},
		{"embedded mismatch", func(f *loaderFixture) { f.comparator.equal = false }, StageEmbeddedEquality, callTrace{"open", "lookup", "manifest", "compare"}},
		{"comparison panic", func(f *loaderFixture) { f.comparator.panicValue = "compare" }, StageEmbeddedEquality, callTrace{"open", "lookup", "manifest", "compare"}},
		{"registration error", func(f *loaderFixture) { f.registrar.err = errors.New("registration failed") }, StageRegistration, callTrace{"open", "lookup", "manifest", "compare", "register"}},
		{"registration panic", func(f *loaderFixture) { f.registrar.panicValue = "register" }, StageRegistration, callTrace{"open", "lookup", "manifest", "compare", "register"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLoaderFixture(t)
			test.setup(fixture)
			assertFixtureLoad(t, fixture, Load(context.Background(), fixture.artifact, fixture.sidecar, fixture.config), test.stage, test.trace)
		})
	}
}

func TestLoaderAcceptedSymbolShapesAndSuccess(t *testing.T) {
	tests := []struct {
		name   string
		symbol func(*loaderFixture) plugin.Symbol
	}{
		{"Plugin", func(fixture *loaderFixture) plugin.Symbol { return v1.Plugin(fixture.plugin) }},
		{"pointer to Plugin", func(fixture *loaderFixture) plugin.Symbol {
			loaded := v1.Plugin(fixture.plugin)
			return &loaded
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLoaderFixture(t)
			fixture.native.symbol = test.symbol(fixture)
			assertFixtureLoad(t, fixture, Load(context.Background(), fixture.artifact, fixture.sidecar, fixture.config), StageComplete, callTrace{"open", "lookup", "manifest", "compare", "register"})
		})
	}
}

func TestLoaderRegistrationRollback(t *testing.T) {
	fixture := newLoaderFixture(t)
	fixture.plugin.register = func(_ context.Context, host v1.Host) error {
		if err := host.RegisterCommand(v1.CommandRegistration{
			ID: "rollback.command", Label: "Rollback", Description: "rollback", Category: "test", Handler: commandHandler{},
		}); err != nil {
			return err
		}
		return errors.New("forced registration failure")
	}
	result := Load(context.Background(), fixture.artifact, fixture.sidecar, fixture.config)
	assertFixtureLoad(t, fixture, result, StageRegistration, callTrace{"open", "lookup", "manifest", "compare", "register"})
	if count := fixture.config.Registry.CommandCount(); count != 0 {
		t.Fatalf("registration rollback left %d commands", count)
	}
}

func TestLoaderSubprocess(t *testing.T) {
	if testCase := os.Getenv("DOUBLANGU_LOADER_SUBPROCESS"); testCase != "" {
		fixture := newLoaderFixture(t)
		switch testCase {
		case "open":
			fixture.config.Opener = traceOpener{trace: fixture.trace, err: errors.New("open failed")}
		case "lookup":
			fixture.native.err = errors.New("lookup failed")
		case "symbol":
			fixture.native.symbol = "wrong"
		case "embedded":
			fixture.comparator.equal = false
		case "registration":
			fixture.registrar.err = errors.New("registration failed")
		case "panic":
			fixture.native.panicValue = "lookup panic"
		default:
			os.Exit(2)
		}
		result := Load(context.Background(), fixture.artifact, fixture.sidecar, fixture.config)
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Stage Stage     `json:"stage"`
			Code  ErrorCode `json:"code"`
			Trace callTrace `json:"trace"`
		}{Stage: result.Stage, Code: result.Code, Trace: *fixture.trace})
		os.Exit(0)
	}

	cases := []struct {
		name  string
		stage Stage
		trace callTrace
	}{
		{"open", StageOpen, callTrace{"open"}},
		{"lookup", StageLookup, callTrace{"open", "lookup"}},
		{"symbol", StageSymbolContract, callTrace{"open", "lookup"}},
		{"embedded", StageEmbeddedEquality, callTrace{"open", "lookup", "manifest", "compare"}},
		{"registration", StageRegistration, callTrace{"open", "lookup", "manifest", "compare", "register"}},
		{"panic", StageLookup, callTrace{"open", "lookup"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestLoaderSubprocess$")
			command.Env = append(os.Environ(), "DOUBLANGU_LOADER_SUBPROCESS="+test.name)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if err != nil {
				t.Fatalf("subprocess: %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("subprocess stderr = %q, want empty", stderr.String())
			}
			if strings.Count(stdout.String(), "\n") != 1 || !strings.HasSuffix(stdout.String(), "\n") {
				t.Fatalf("stdout protocol = %q, want one JSON line", stdout.String())
			}
			var got struct {
				Stage Stage     `json:"stage"`
				Code  ErrorCode `json:"code"`
				Trace callTrace `json:"trace"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode protocol: %v", err)
			}
			if got.Stage != test.stage || got.Code != ErrorCode(test.stage) || !reflect.DeepEqual(got.Trace, test.trace) {
				t.Errorf("protocol = %+v, want stage=%s code=%s trace=%v", got, test.stage, test.stage, test.trace)
			}
		})
	}
}

func TestLoaderRealArtifactInspectorRecognizesRequiredTargets(t *testing.T) {
	directory := t.TempDir()
	tests := []struct {
		name string
		data []byte
		want ArtifactInfo
	}{
		{"Linux ARM64 ELF", minimalELFArm64(), ArtifactInfo{GOOS: "linux", GOARCH: "arm64"}},
		{"macOS ARM64 Mach-O", minimalMachOArm64(), ArtifactInfo{GOOS: "darwin", GOARCH: "arm64"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+".bin")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := (realArtifactInspector{}).Inspect(path)
			if err != nil || got != test.want {
				t.Fatalf("Inspect() = %+v, %v; want %+v, nil", got, err, test.want)
			}
		})
	}
}

func minimalELFArm64() []byte {
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	data[16] = 3
	data[18] = 183
	data[19] = 0
	data[20] = 1
	data[52] = 64
	data[54] = 0
	return data
}

func minimalMachOArm64() []byte {
	data := make([]byte, 32)
	copy(data, []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01})
	data[8] = 0
	data[12] = 6
	data[16] = 0
	data[20] = 0
	data[24] = 0
	data[28] = 0
	return data
}

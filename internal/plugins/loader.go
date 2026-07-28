package manifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"plugin"
	"reflect"
	"runtime"

	v1 "doublangu/pkg/pluginapi/v1"
)

// Stage identifies a loader pipeline stage. The first four stages must finish
// before native code is opened; complete is a successful terminal result only.
type Stage string

const (
	StageSidecarRead      Stage = "sidecar_read"
	StageSchema           Stage = "schema"
	StageChecksum         Stage = "checksum"
	StageCompatibility    Stage = "compatibility"
	StageOpen             Stage = "open"
	StageLookup           Stage = "lookup"
	StageSymbolContract   Stage = "symbol_contract"
	StageEmbeddedEquality Stage = "embedded_equality"
	StageRegistration     Stage = "registration"
	StageComplete         Stage = "complete"
)

// ErrorCode is a stable, machine-readable summary of a Load result.
type ErrorCode string

const CodeOK ErrorCode = "ok"

// NativePlugin abstracts a loaded native plugin. Production code wraps
// *plugin.Plugin; tests inject fakes to prove the native boundary.
type NativePlugin interface {
	Lookup(symbol string) (plugin.Symbol, error)
}

// PluginOpener is the native-open seam.
type PluginOpener interface {
	Open(path string) (NativePlugin, error)
}

// ArtifactInfo describes a candidate artifact without executing it.
type ArtifactInfo struct {
	GOOS   string
	GOARCH string
}

// ArtifactInspector identifies a candidate artifact's target before it is
// opened as a Go plugin.
type ArtifactInspector interface {
	Inspect(path string) (ArtifactInfo, error)
}

// ManifestComparator compares the embedded and sidecar manifests.
type ManifestComparator interface {
	Equal(embedded, sidecar v1.Manifest) bool
}

// PluginRegistrar performs the accepted registry transaction.
type PluginRegistrar interface {
	Register(context.Context, *Registry, string, v1.Plugin, HostServices) error
}

// HostIdentity is caller-supplied loader compatibility data. BuildSettings and
// ModuleGraph are fingerprints produced outside this loader (Checkpoint 6 owns
// deriving them); their exact expected values are mandatory here.
type HostIdentity struct {
	APIVersion    string
	GoVersion     string
	Role          string
	GOOS          string
	GOARCH        string
	BuildSettings string
	ModuleGraph   string
}

// HostServices are passed unchanged to the accepted registry transaction.
type HostServices struct {
	Settings   v1.Settings
	Library    v1.Library
	Blobs      v1.BlobStore
	Logger     v1.Logger
	HTTPClient *http.Client
	EventBus   v1.EventBus
}

// LoaderConfig holds caller identity, registration services, and optional test
// seams. Nil seams use the production implementations.
type LoaderConfig struct {
	Host HostIdentity

	Schema     *ParsedSchema
	Registry   *Registry
	Opener     PluginOpener
	Inspector  ArtifactInspector
	Comparator ManifestComparator
	Registrar  PluginRegistrar
	Services   HostServices
}

// LoadResult captures the typed result. Call evidence belongs to the injected
// collaborators, not loader-authored counters.
type LoadResult struct {
	Stage  Stage
	Code   ErrorCode
	Err    error
	Plugin v1.Plugin
}

// Load validates the sidecar and artifact before native open, then performs
// lookup, embedded-manifest comparison, and one atomic registry transaction.
func Load(ctx context.Context, artifactPath, sidecarPath string, cfg LoaderConfig) LoadResult {
	sidecarRaw, err := os.ReadFile(sidecarPath)
	if err != nil {
		return failed(StageSidecarRead, fmt.Errorf("sidecar read: %w", err))
	}
	if cfg.Schema == nil {
		return failed(StageSchema, fmt.Errorf("manifest schema is unavailable"))
	}
	if err := cfg.Schema.Validate(sidecarRaw); err != nil {
		return failed(StageSchema, fmt.Errorf("sidecar schema: %w", err))
	}

	sidecar, err := decodeSidecarManifest(sidecarRaw)
	if err != nil {
		return failed(StageSchema, fmt.Errorf("sidecar schema: %w", err))
	}
	if err := sidecar.Validate(); err != nil {
		return failed(StageSchema, fmt.Errorf("sidecar validation: %w", err))
	}

	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		return failed(StageChecksum, fmt.Errorf("checksum read artifact: %w", err))
	}
	if actual := sha256Hex(artifactBytes); actual != sidecar.ArtifactChecksum {
		return failed(StageChecksum, fmt.Errorf("checksum mismatch: expected %s, got %s", sidecar.ArtifactChecksum, actual))
	}

	inspector := cfg.Inspector
	if inspector == nil {
		inspector = realArtifactInspector{}
	}
	artifact, err := safelyInspect(inspector, artifactPath)
	if err != nil {
		return failed(StageCompatibility, fmt.Errorf("artifact inspection: %w", err))
	}
	if err := validateCompatibility(sidecar, normalizedHostIdentity(cfg.Host), artifact); err != nil {
		return failed(StageCompatibility, err)
	}

	opener := cfg.Opener
	if opener == nil {
		opener = realOpener{}
	}
	nativePlugin, err := safelyOpen(opener, artifactPath)
	if err != nil {
		return failed(StageOpen, err)
	}
	if isNilValue(nativePlugin) {
		return failed(StageOpen, fmt.Errorf("open: native plugin is nil"))
	}

	symbol, err := safelyLookup(nativePlugin, "DoublanguPlugin")
	if err != nil {
		return failed(StageLookup, err)
	}
	loadedPlugin, err := resolveSymbolContract(symbol)
	if err != nil {
		return failed(StageSymbolContract, err)
	}

	embedded, err := safelyManifest(loadedPlugin)
	if err != nil {
		return failed(StageEmbeddedEquality, err)
	}
	comparator := cfg.Comparator
	if comparator == nil {
		comparator = artifactParityComparator{}
	}
	equal, err := safelyCompare(comparator, embedded, sidecar)
	if err != nil {
		return failed(StageEmbeddedEquality, err)
	}
	if !equal {
		return failed(StageEmbeddedEquality, fmt.Errorf("embedded manifest does not equal sidecar"))
	}
	if cfg.Registry == nil {
		return failed(StageRegistration, fmt.Errorf("registry is unavailable"))
	}
	registrar := cfg.Registrar
	if registrar == nil {
		registrar = registryRegistrar{}
	}
	if err := safelyRegister(registrar, ctx, cfg.Registry, sidecar.ID, loadedPlugin, cfg.Services); err != nil {
		return failed(StageRegistration, err)
	}

	return LoadResult{Stage: StageComplete, Code: CodeOK, Plugin: loadedPlugin}
}

func failed(stage Stage, err error) LoadResult {
	return LoadResult{Stage: stage, Code: ErrorCode(stage), Err: err}
}

// decodeSidecarManifest rejects duplicate object keys before unmarshalling.
// encoding/json otherwise accepts duplicates by silently retaining the last
// value, which is incompatible with the checked-in strict manifest contract.
func decodeSidecarManifest(raw []byte) (v1.Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := validateJSONValue(decoder); err != nil {
		return v1.Manifest{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return v1.Manifest{}, fmt.Errorf("multiple JSON values")
		}
		return v1.Manifest{}, fmt.Errorf("invalid JSON: %w", err)
	}

	var manifest v1.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return v1.Manifest{}, fmt.Errorf("invalid JSON: %w", err)
	}
	return manifest, nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("invalid JSON object: %w", err)
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("invalid JSON array: %w", err)
		}
	}
	return nil
}

func normalizedHostIdentity(host HostIdentity) HostIdentity {
	if host.APIVersion == "" {
		host.APIVersion = v1.APIVersion
	}
	if host.GoVersion == "" {
		host.GoVersion = v1.GoVersion
	}
	if host.GOOS == "" {
		host.GOOS = runtime.GOOS
	}
	if host.GOARCH == "" {
		host.GOARCH = runtime.GOARCH
	}
	return host
}

// validateCompatibility compares every loader-owned compatibility input before
// plugin.Open. Fingerprint derivation intentionally remains outside the loader.
func validateCompatibility(sidecar v1.Manifest, host HostIdentity, artifact ArtifactInfo) error {
	if host.APIVersion == "" {
		return fmt.Errorf("expected host api_version is missing")
	}
	if sidecar.APIVersion != host.APIVersion {
		return fmt.Errorf("api_version %q does not equal host %q", sidecar.APIVersion, host.APIVersion)
	}
	if host.GoVersion == "" {
		return fmt.Errorf("expected host go_version is missing")
	}
	if sidecar.GoVersion != host.GoVersion {
		return fmt.Errorf("go_version %q does not equal host %q", sidecar.GoVersion, host.GoVersion)
	}
	if host.Role == "" {
		return fmt.Errorf("expected host role is missing")
	}
	matchedRole := false
	for _, target := range sidecar.Target {
		if target == host.Role {
			matchedRole = true
			break
		}
	}
	if !matchedRole {
		return fmt.Errorf("host role %q is not in target list %v", host.Role, sidecar.Target)
	}
	if artifact.GOOS != host.GOOS {
		return fmt.Errorf("artifact GOOS %q does not equal host %q", artifact.GOOS, host.GOOS)
	}
	if artifact.GOARCH != host.GOARCH {
		return fmt.Errorf("artifact GOARCH %q does not equal host %q", artifact.GOARCH, host.GOARCH)
	}
	if host.BuildSettings == "" {
		return fmt.Errorf("expected host build_settings hash is missing")
	}
	if sidecar.BuildSettings != host.BuildSettings {
		return fmt.Errorf("build_settings hash does not equal host")
	}
	if host.ModuleGraph == "" {
		return fmt.Errorf("expected host module_graph hash is missing")
	}
	if sidecar.ModuleGraph != host.ModuleGraph {
		return fmt.Errorf("module_graph hash does not equal host")
	}
	return nil
}

// realArtifactInspector supports the only CP5 artifact targets: Linux ARM64
// ELF and macOS ARM64 Mach-O. It never executes artifact code.
type realArtifactInspector struct{}

func (realArtifactInspector) Inspect(path string) (ArtifactInfo, error) {
	if file, err := elf.Open(path); err == nil {
		defer file.Close()
		if file.Machine != elf.EM_AARCH64 {
			return ArtifactInfo{}, fmt.Errorf("unsupported ELF machine %s", file.Machine)
		}
		return ArtifactInfo{GOOS: "linux", GOARCH: "arm64"}, nil
	}
	if file, err := macho.Open(path); err == nil {
		defer file.Close()
		if file.Cpu != macho.CpuArm64 {
			return ArtifactInfo{}, fmt.Errorf("unsupported Mach-O CPU %s", file.Cpu)
		}
		return ArtifactInfo{GOOS: "darwin", GOARCH: "arm64"}, nil
	}
	return ArtifactInfo{}, fmt.Errorf("unsupported or unreadable artifact format")
}

type realOpener struct{}

func (realOpener) Open(path string) (NativePlugin, error) {
	loaded, err := plugin.Open(path)
	if err != nil {
		return nil, err
	}
	return realNativePlugin{plugin: loaded}, nil
}

type realNativePlugin struct{ plugin *plugin.Plugin }

func (p realNativePlugin) Lookup(symbol string) (plugin.Symbol, error) {
	return p.plugin.Lookup(symbol)
}

type registryRegistrar struct{}

func (registryRegistrar) Register(ctx context.Context, registry *Registry, id string, loaded v1.Plugin, services HostServices) error {
	return registry.RegisterPlugin(ctx, id, loaded, services.Settings, services.Library, services.Blobs, services.Logger, services.HTTPClient, services.EventBus)
}

func safelyInspect(inspector ArtifactInspector, path string) (info ArtifactInfo, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("artifact inspector panicked: %v", recovered)
		}
	}()
	return inspector.Inspect(path)
}

func safelyOpen(opener PluginOpener, path string) (loaded NativePlugin, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("open panicked: %v", recovered)
		}
	}()
	return opener.Open(path)
}

func safelyLookup(loaded NativePlugin, symbol string) (result plugin.Symbol, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("lookup panicked: %v", recovered)
		}
	}()
	return loaded.Lookup(symbol)
}

func safelyManifest(loaded v1.Plugin) (manifest v1.Manifest, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("embedded manifest panicked: %v", recovered)
		}
	}()
	return loaded.Manifest(), nil
}

func safelyCompare(comparator ManifestComparator, embedded, sidecar v1.Manifest) (equal bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("embedded manifest comparison panicked: %v", recovered)
		}
	}()
	return comparator.Equal(embedded, sidecar), nil
}

func safelyRegister(registrar PluginRegistrar, ctx context.Context, registry *Registry, id string, loaded v1.Plugin, services HostServices) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("registration panicked: %v", recovered)
		}
	}()
	return registrar.Register(ctx, registry, id, loaded, services)
}

// resolveSymbolContract accepts exactly a non-nil Plugin value or a non-nil
// *Plugin containing a non-nil concrete implementation.
func resolveSymbolContract(symbol plugin.Symbol) (v1.Plugin, error) {
	if symbol == nil {
		return nil, fmt.Errorf("DoublanguPlugin symbol is nil")
	}
	if loaded, ok := symbol.(v1.Plugin); ok {
		if isNilValue(loaded) {
			return nil, fmt.Errorf("DoublanguPlugin is a nil Plugin implementation")
		}
		return loaded, nil
	}
	if pointer, ok := symbol.(*v1.Plugin); ok {
		if pointer == nil {
			return nil, fmt.Errorf("DoublanguPlugin is a nil *Plugin pointer")
		}
		if isNilValue(*pointer) {
			return nil, fmt.Errorf("DoublanguPlugin *Plugin contains a nil Plugin implementation")
		}
		return *pointer, nil
	}
	return nil, fmt.Errorf("DoublanguPlugin symbol is %T, not Plugin or *Plugin", symbol)
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

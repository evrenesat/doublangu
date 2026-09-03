package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"doublangu/internal/pipeline"
)

// Provider type identifiers. The registry and transports key on these.
const (
	ProviderTypeCodexAppServer   = "codex_app_server"
	ProviderTypeOpenAICompatible = "openai_compatible"
	ProviderConfigVersion        = 1
	defaultRequestTimeoutSeconds = 600
	providerIDPattern            = `^[a-z0-9][a-z0-9._-]{0,62}$`
	apiKeyEnvPattern             = `^[A-Z_][A-Z0-9_]*$`
	configFingerprintDomain      = "doublangu.provider-config.v1"
)

var (
	providerIDRegex = regexp.MustCompile(providerIDPattern)
	apiKeyEnvRegex  = regexp.MustCompile(apiKeyEnvPattern)
)

// ProviderEntry is one configured provider. It never contains a credential
// value; openai-compatible credentials are referenced through APIKeyEnv.
type ProviderEntry struct {
	ID                    string `json:"id"`
	Label                 string `json:"label"`
	EndpointLabel         string `json:"endpoint_label"`
	Type                  string `json:"type"`
	Enabled               bool   `json:"enabled"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	BaseURL               string `json:"base_url"`
	APIKeyEnv             string `json:"api_key_env"`
}

// ProviderConfigFile is the exact trusted top-level provider configuration.
type ProviderConfigFile struct {
	Version                    int                     `json:"version"`
	AllowInsecureTailscaleHTTP bool                    `json:"allow_insecure_tailscale_http"`
	Providers                  []ProviderEntry         `json:"providers"`
	BootstrapProfile           *BootstrapProfileConfig `json:"bootstrap_profile"`
}

// BootstrapBindingConfig is one bootstrap binding with provider-specific
// options. The map key is the registered stage id.
type BootstrapBindingConfig struct {
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	Options    json.RawMessage `json:"options"`
}

// BootstrapProfileConfig is the optional startup profile. Ids are assigned
// when the profile service seeds it.
type BootstrapProfileConfig struct {
	Name     string                                      `json:"name"`
	Bindings map[pipeline.StageID]BootstrapBindingConfig `json:"bindings"`
}

// DecodeProviderConfig parses and validates the strict configuration file.
func DecodeProviderConfig(data []byte, envLookup func(string) string) (*ProviderConfigFile, error) {
	var file ProviderConfigFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode provider config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("provider config contains trailing JSON")
		}
		return nil, fmt.Errorf("provider config contains malformed trailing JSON: %w", err)
	}
	if file.Version != ProviderConfigVersion {
		return nil, fmt.Errorf("unsupported provider config version %d", file.Version)
	}
	if err := file.Validate(envLookup); err != nil {
		return nil, err
	}
	return &file, nil
}

// LoadProviderConfigFile reads, checks file ownership/mode, and validates one
// trusted provider configuration file.
func LoadProviderConfigFile(path string, envLookup func(string) string) (*ProviderConfigFile, error) {
	if envLookup == nil {
		envLookup = os.Getenv
	}
	if err := CheckProviderConfigFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read provider config %q: %w", path, err)
	}
	return DecodeProviderConfig(data, envLookup)
}

// CheckProviderConfigFile enforces the file rule: a regular non-symlink file
// owned by root or the effective service UID, without group/world-writable or
// world-readable modes.
func CheckProviderConfigFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat provider config %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("provider config %q must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("provider config %q must be a regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return fmt.Errorf("provider config %q ownership cannot be determined", path)
	}
	uid := uint32(os.Getuid())
	if stat.Uid != 0 && stat.Uid != uid {
		return fmt.Errorf("provider config %q must be owned by root or the service user", path)
	}
	perm := info.Mode().Perm()
	if perm&0o022 != 0 {
		return fmt.Errorf("provider config %q must not be group/world-writable", path)
	}
	if perm&0o004 != 0 {
		return fmt.Errorf("provider config %q must not be world-readable", path)
	}
	return nil
}

// Validate checks ids, labels, URLs, secret references, enabled-provider
// secrets, and the bootstrap profile. envLookup resolves environment
// variables; a nil envLookup treats referenced secrets as present.
func (file *ProviderConfigFile) Validate(envLookup func(string) string) error {
	if len(file.Providers) == 0 {
		return errors.New("at least one provider is required")
	}
	byID := make(map[string]ProviderEntry, len(file.Providers))
	allowHTTP := file.AllowInsecureTailscaleHTTP
	for index, entry := range file.Providers {
		if err := validateProviderEntry(entry, allowHTTP); err != nil {
			return fmt.Errorf("providers[%d] %q: %w", index, entry.ID, err)
		}
		if _, exists := byID[entry.ID]; exists {
			return fmt.Errorf("duplicate provider id %q", entry.ID)
		}
		byID[entry.ID] = entry
		if entry.Enabled && entry.Type == ProviderTypeOpenAICompatible && envLookup != nil {
			if strings.TrimSpace(envLookup(entry.APIKeyEnv)) == "" {
				return fmt.Errorf("provider %q references missing or blank secret environment variable %s", entry.ID, entry.APIKeyEnv)
			}
		}
	}
	if file.BootstrapProfile == nil {
		return nil
	}
	if err := pipeline.ValidateProfileName(file.BootstrapProfile.Name); err != nil {
		return fmt.Errorf("bootstrap_profile.name: %w", err)
	}
	seenStages := make(map[pipeline.StageID]bool, len(file.BootstrapProfile.Bindings))
	for _, stage := range pipeline.RegisteredStages() {
		binding, ok := file.BootstrapProfile.Bindings[stage]
		if !ok {
			return fmt.Errorf("bootstrap_profile is missing stage %q", stage)
		}
		seenStages[stage] = true
		provider, ok := byID[binding.ProviderID]
		if !ok {
			return fmt.Errorf("bootstrap_profile stage %q references unknown provider %q", stage, binding.ProviderID)
		}
		if !provider.Enabled {
			return fmt.Errorf("bootstrap_profile stage %q references disabled provider %q", stage, binding.ProviderID)
		}
		if strings.TrimSpace(binding.ModelID) == "" {
			return fmt.Errorf("bootstrap_profile stage %q has no model", stage)
		}
		if _, err := CanonicalizeProviderOptions(provider.Type, binding.Options); err != nil {
			return fmt.Errorf("bootstrap_profile stage %q options: %w", stage, err)
		}
	}
	if len(seenStages) != len(pipeline.RegisteredStages()) {
		return errors.New("bootstrap_profile must not bind unknown stages")
	}
	return nil
}

func validateProviderEntry(entry ProviderEntry, allowInsecureHTTP bool) error {
	if !providerIDRegex.MatchString(entry.ID) {
		return fmt.Errorf("invalid provider id %q", entry.ID)
	}
	if err := validateSafeLabel("label", entry.Label); err != nil {
		return err
	}
	if err := validateSafeLabel("endpoint_label", entry.EndpointLabel); err != nil {
		return err
	}
	switch entry.Type {
	case ProviderTypeCodexAppServer:
		if entry.BaseURL != "" {
			return errors.New("codex_app_server must not set base_url")
		}
		if entry.APIKeyEnv != "" {
			return errors.New("codex_app_server must not set api_key_env")
		}
	case ProviderTypeOpenAICompatible:
		if entry.BaseURL == "" {
			return errors.New("openai_compatible requires base_url")
		}
		if err := validateOpenAIBaseURL(entry.BaseURL, allowInsecureHTTP); err != nil {
			return fmt.Errorf("base_url: %w", err)
		}
		if !apiKeyEnvRegex.MatchString(entry.APIKeyEnv) {
			return fmt.Errorf("invalid api_key_env %q", entry.APIKeyEnv)
		}
	default:
		return fmt.Errorf("unknown provider type %q", entry.Type)
	}
	if entry.RequestTimeoutSeconds < 0 {
		return errors.New("request_timeout_seconds must not be negative")
	}
	return nil
}

func validateSafeLabel(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if !utf8.ValidString(trimmed) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if utf8.RuneCountInString(trimmed) > 80 {
		return fmt.Errorf("%s must be at most 80 characters", name)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}

// validateOpenAIBaseURL requires an absolute URL without user info, query, or
// fragment, with a path of exactly /v1 after trimming one trailing slash.
// HTTPS is always accepted; HTTP only for literal loopback or 100.64.0.0/10
// addresses when explicitly allowed.
func validateOpenAIBaseURL(raw string, allowInsecureHTTP bool) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("scheme must be http or https")
	}
	if parsed.Host == "" {
		return errors.New("host is required")
	}
	if parsed.User != nil {
		return errors.New("must not contain user info")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must not contain a query or fragment")
	}
	pathValue := strings.TrimSuffix(parsed.Path, "/")
	if pathValue != "/v1" {
		return fmt.Errorf("path must be exactly /v1, got %q", parsed.Path)
	}
	host := parsed.Hostname()
	if parsed.Scheme == "http" {
		if !allowInsecureHTTP {
			return errors.New("http is not allowed; use https or enable allow_insecure_tailscale_http")
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return errors.New("http requires a literal loopback or Tailscale/CGNAT IP address, not a DNS name")
		}
		if !ip.IsLoopback() && !isCGNAT(ip) {
			return errors.New("http requires a literal loopback or 100.64.0.0/10 address")
		}
	}
	return nil
}

func isCGNAT(ip net.IP) bool {
	return ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1] >= 64 && ip.To4()[1] <= 127
}

// ProviderConfigFingerprint hashes only the canonical sanitized connection
// identity: type, normalized base URL, secret environment variable name,
// timeout, and enabled state. A secret value never contributes.
func ProviderConfigFingerprint(entry ProviderEntry) string {
	baseURL := ""
	if entry.Type == ProviderTypeOpenAICompatible {
		baseURL = entry.BaseURL
	}
	timeout := entry.RequestTimeoutSeconds
	if timeout == 0 {
		timeout = defaultRequestTimeoutSeconds
	}
	hash := sha256.New()
	hash.Write([]byte(configFingerprintDomain))
	for _, part := range []string{
		entry.Type,
		strings.TrimSuffix(baseURL, "/"),
		entry.APIKeyEnv,
		fmt.Sprintf("%d", timeout),
		fmt.Sprintf("%t", entry.Enabled),
	} {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// ResolveTimeoutSeconds returns the effective request timeout for an entry.
func ResolveTimeoutSeconds(entry ProviderEntry) int {
	if entry.RequestTimeoutSeconds <= 0 {
		return defaultRequestTimeoutSeconds
	}
	return entry.RequestTimeoutSeconds
}

// ---------------------------------------------------------------------------
// Provider-specific options codecs

// CodexOptions are the only codex_app_server options.
type CodexOptions struct {
	ReasoningEffort string `json:"reasoning_effort"`
}

// OpenAICompatibleOptions are the only openai-compatible options.
type OpenAICompatibleOptions struct {
	TemperatureMilli int `json:"temperature_milli"`
	MaxOutputTokens  int `json:"max_output_tokens"`
}

// CanonicalizeProviderOptions strictly decodes provider-specific options,
// rejects unknown or missing fields, validates ranges, and returns canonical
// JSON. Persisted bindings always contain every field explicitly.
func CanonicalizeProviderOptions(providerType string, raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		return nil, errors.New("options must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var canonical json.RawMessage
	switch providerType {
	case ProviderTypeCodexAppServer:
		if len(fields) != 1 {
			return nil, fmt.Errorf("codex options must contain exactly reasoning_effort, got %d fields", len(fields))
		}
		var options CodexOptions
		if err := decodeStrict(raw, &options); err != nil {
			return nil, err
		}
		if strings.TrimSpace(options.ReasoningEffort) == "" {
			return nil, errors.New("codex reasoning_effort must not be empty")
		}
		canonical, _ = json.Marshal(options)
	case ProviderTypeOpenAICompatible:
		for _, required := range []string{"temperature_milli", "max_output_tokens"} {
			if _, ok := fields[required]; !ok {
				return nil, fmt.Errorf("openai-compatible options require %s", required)
			}
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("openai-compatible options must contain exactly temperature_milli and max_output_tokens, got %d fields", len(fields))
		}
		var options OpenAICompatibleOptions
		if err := decodeStrict(raw, &options); err != nil {
			return nil, err
		}
		if options.TemperatureMilli < 0 || options.TemperatureMilli > 2000 {
			return nil, fmt.Errorf("temperature_milli must be 0..2000, got %d", options.TemperatureMilli)
		}
		if options.MaxOutputTokens < 1024 || options.MaxOutputTokens > 65536 {
			return nil, fmt.Errorf("max_output_tokens must be 1024..65536, got %d", options.MaxOutputTokens)
		}
		canonical, _ = json.Marshal(options)
	default:
		return nil, fmt.Errorf("no option codec for provider type %q", providerType)
	}
	return canonical, nil
}

func decodeStrict(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("options contain trailing JSON")
	}
	return nil
}

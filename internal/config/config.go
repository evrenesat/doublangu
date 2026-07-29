// Package config validates and exposes the single-owner server configuration.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Config holds every validated server setting.
type Config struct {
	Listen        string
	PublicURL     string
	Secret        []byte // decoded raw bytes from the base64 env var
	Database      DatabaseConfig
	Session       SessionConfig
	Paths         PathsConfig
	MediaRedirect MediaRedirectConfig
}

// DatabaseConfig holds the SQLite path and WAL mode settings.
type DatabaseConfig struct {
	Path string
}

// SessionConfig controls secure-cookie and token-lifetime properties.
type SessionConfig struct {
	MaxAge   time.Duration
	Secure   bool
	HTTPOnly bool
	SameSite string
}

// PathsConfig names the writable filesystem roots.
type PathsConfig struct {
	Media string
	Data  string
}

// MediaRedirectConfig controls optional X-Accel-Redirect delegation.
// When Enabled is true, the media handler returns X-Accel-Redirect with
// Prefix + digest instead of streaming bytes directly. The prefix is an
// opaque internal URI that the reverse proxy maps to the real blob
// directory — the filesystem path is never exposed to clients.
type MediaRedirectConfig struct {
	Enabled bool
	Prefix  string // e.g. "/_media-internal/"
}

// Load reads configuration from the environment and returns a validated Config
// or a descriptive error. Defaults are suitable for local development.
func Load() (*Config, error) {
	cfg := &Config{}

	cfg.Listen = envOrDefault("DOUBLANGU_LISTEN", ":8080")

	cfg.PublicURL = envOrDefault("DOUBLANGU_PUBLIC_URL", "http://localhost:8080")

	secretEncoded := os.Getenv("DOUBLANGU_SECRET")
	if secretEncoded == "" {
		return nil, errors.New("DOUBLANGU_SECRET is required (32+ byte base64-encoded random value)")
	}
	secret, err := base64.StdEncoding.DecodeString(secretEncoded)
	if err != nil {
		return nil, fmt.Errorf("DOUBLANGU_SECRET is not valid base64: %w", err)
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("DOUBLANGU_SECRET must decode to at least 32 bytes, got %d", len(secret))
	}
	cfg.Secret = secret

	cfg.Database.Path = envOrDefault("DOUBLANGU_DB_PATH", filepath.Join("data", "doublangu.db"))

	cfg.Paths.Media = envOrDefault("DOUBLANGU_MEDIA_PATH", "media")
	cfg.Paths.Data = envOrDefault("DOUBLANGU_DATA_PATH", "data")

	if v := os.Getenv("DOUBLANGU_MEDIA_REDIRECT"); v != "" {
		cfg.MediaRedirect.Enabled = true
		cfg.MediaRedirect.Prefix = v
	}

	cfg.Session.MaxAge = 24 * time.Hour
	if v := os.Getenv("DOUBLANGU_SESSION_MAX_AGE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("DOUBLANGU_SESSION_MAX_AGE: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("DOUBLANGU_SESSION_MAX_AGE must be positive, got %v", d)
		}
		cfg.Session.MaxAge = d
	}

	cfg.Session.HTTPOnly = true
	cfg.Session.SameSite = "lax"
	cfg.Session.Secure = isHTTPS(cfg.PublicURL)

	if v := os.Getenv("DOUBLANGU_SESSION_SECURE"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			cfg.Session.Secure = true
		case "false", "0", "no":
			cfg.Session.Secure = false
		default:
			return nil, fmt.Errorf("DOUBLANGU_SESSION_SECURE must be true/false, got %q", v)
		}
	}

	return cfg, cfg.Validate()
}

// Validate checks every field; Load calls it automatically.
func (cfg *Config) Validate() error {
	if cfg.Listen == "" {
		return errors.New("listen address must not be empty")
	}

	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("public_url %q is not a valid absolute URL with scheme and host", cfg.PublicURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("public_url scheme must be http or https, got %q", parsed.Scheme)
	}

	if len(cfg.Secret) < 32 {
		return fmt.Errorf("secret must be at least 32 bytes, got %d", len(cfg.Secret))
	}

	if cfg.Database.Path == "" {
		return errors.New("database path must not be empty")
	}

	if cfg.Paths.Media == "" {
		return errors.New("media path must not be empty")
	}
	if cfg.Paths.Data == "" {
		return errors.New("data path must not be empty")
	}

	if cfg.Session.MaxAge <= 0 {
		return fmt.Errorf("session max_age must be positive, got %v", cfg.Session.MaxAge)
	}
	if cfg.Session.SameSite != "lax" && cfg.Session.SameSite != "strict" && cfg.Session.SameSite != "none" {
		return fmt.Errorf("session same_site must be lax, strict, or none, got %q", cfg.Session.SameSite)
	}
	if cfg.Session.SameSite == "none" && !cfg.Session.Secure {
		return errors.New("session same_site=none requires secure=true")
	}
	if isHTTPS(cfg.PublicURL) && !cfg.Session.Secure {
		return errors.New("session secure=false is not allowed when public_url uses https")
	}
	if cfg.MediaRedirect.Enabled {
		if err := validateMediaRedirectPrefix(cfg.MediaRedirect.Prefix); err != nil {
			return fmt.Errorf("media redirect prefix: %w", err)
		}
	} else if cfg.MediaRedirect.Prefix != "" {
		return errors.New("media redirect prefix requires redirect to be enabled")
	}

	return nil
}

func validateMediaRedirectPrefix(prefix string) error {
	if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "//") || !strings.HasSuffix(prefix, "/") {
		return errors.New("must be an absolute URI path with one leading and trailing slash")
	}
	if strings.ContainsAny(prefix, "\\%?#") {
		return errors.New("must not contain escapes, query, fragment, or backslash")
	}
	for _, r := range prefix {
		if r < 0x20 || r == 0x7f {
			return errors.New("must not contain control characters")
		}
	}
	u, err := url.ParseRequestURI(prefix)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not contain a scheme or host")
	}
	clean := path.Clean(prefix)
	if clean != strings.TrimSuffix(prefix, "/") {
		return errors.New("must be a clean URI path")
	}
	for _, segment := range strings.Split(strings.Trim(prefix, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("must not contain empty, dot, or dot-dot segments")
		}
	}
	return nil
}

// SecretBase64 returns the secret re-encoded for use in child components.
// It is deliberately not a String method to avoid accidental logging.
func (cfg *Config) SecretBase64() string {
	return base64.StdEncoding.EncodeToString(cfg.Secret)
}

// Redacted returns a copy safe for diagnostics — secrets are replaced.
func (cfg *Config) Redacted() *Config {
	clone := *cfg
	clone.Secret = []byte("[REDACTED]")
	return &clone
}

func envOrDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func isHTTPS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Scheme == "https"
}

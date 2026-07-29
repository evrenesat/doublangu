package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func validSecret(t *testing.T) string {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(secret)
}

func TestLoadDefaultsSucceedWithValidSecret(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", "MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI=") // 32 bytes base64
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.PublicURL != "http://localhost:8080" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if len(cfg.Secret) != 32 {
		t.Errorf("Secret length = %d", len(cfg.Secret))
	}
	if cfg.Database.Path != "data/doublangu.db" {
		t.Errorf("Database.Path = %q", cfg.Database.Path)
	}
	if cfg.Paths.Media != "media" {
		t.Errorf("Paths.Media = %q", cfg.Paths.Media)
	}
	if cfg.Paths.Data != "data" {
		t.Errorf("Paths.Data = %q", cfg.Paths.Data)
	}
	if cfg.Session.MaxAge != 24*time.Hour {
		t.Errorf("Session.MaxAge = %v", cfg.Session.MaxAge)
	}
	if !cfg.Session.HTTPOnly {
		t.Errorf("Session.HTTPOnly = false")
	}
	if cfg.Session.SameSite != "lax" {
		t.Errorf("Session.SameSite = %q", cfg.Session.SameSite)
	}
	if cfg.Session.Secure {
		t.Errorf("Session.Secure should be false for http:// public URL")
	}
}

func TestLoadRequiresSecret(t *testing.T) {
	// Ensure DOUBLANGU_SECRET is not inherited from parent.
	t.Setenv("DOUBLANGU_SECRET", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DOUBLANGU_SECRET is required") {
		t.Fatalf("expected secret-required error, got: %v", err)
	}
}

func TestLoadRejectsShortSecret(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", "c2hvcnQ=") // 4 bytes
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("expected minimum-length error, got: %v", err)
	}
}

func TestLoadRejectsInvalidBase64Secret(t *testing.T) {
	secret := "!!!not-base64!!!"
	t.Setenv("DOUBLANGU_SECRET", secret)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "valid base64") {
		t.Fatalf("expected base64 error, got: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("configuration error leaked secret: %v", err)
	}
}

func TestConfigCustomValues(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t)) // 32 bytes base64 = 44 chars
	t.Setenv("DOUBLANGU_LISTEN", "127.0.0.1:9090")
	t.Setenv("DOUBLANGU_PUBLIC_URL", "https://doublangu.example.com")
	t.Setenv("DOUBLANGU_DB_PATH", "/var/doublangu/db.sqlite")
	t.Setenv("DOUBLANGU_MEDIA_PATH", "/media")
	t.Setenv("DOUBLANGU_DATA_PATH", "/data")
	t.Setenv("DOUBLANGU_SESSION_MAX_AGE", "2h")
	t.Setenv("DOUBLANGU_SESSION_SECURE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9090" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.PublicURL != "https://doublangu.example.com" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if cfg.Database.Path != "/var/doublangu/db.sqlite" {
		t.Errorf("Database.Path = %q", cfg.Database.Path)
	}
	if cfg.Session.MaxAge != 2*time.Hour {
		t.Errorf("Session.MaxAge = %v", cfg.Session.MaxAge)
	}
	if !cfg.Session.Secure {
		t.Errorf("Session.Secure should be true for HTTPS public URLs")
	}
}

func TestLoadRejectsInsecureSessionCookieForHTTPS(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_PUBLIC_URL", "https://doublangu.example.com")
	t.Setenv("DOUBLANGU_SESSION_SECURE", "false")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected HTTPS insecure-cookie error, got: %v", err)
	}
}

func TestSessionSecureDefaultsToTrueForHTTPS(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_PUBLIC_URL", "https://doublangu.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Session.Secure {
		t.Errorf("Session.Secure should default to true for https://")
	}
}

func TestLoadRejectsInvalidPublicURL(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_PUBLIC_URL", "not-a-url")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid public URL")
	}
}

func TestLoadRejectsNonHTTPPublicURLScheme(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_PUBLIC_URL", "ftp://localhost/")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("expected scheme error, got: %v", err)
	}
}

func TestLoadRejectsNegativeSessionMaxAge(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_SESSION_MAX_AGE", "-1h")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for negative max age")
	}
}

func TestLoadRejectsInvalidSessionSecure(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_SESSION_SECURE", "maybe")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid secure value")
	}
}

func TestValidateSameSiteNoneRequiresSecure(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	t.Setenv("DOUBLANGU_PUBLIC_URL", "http://localhost:8080")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Session.SameSite = "none"
	cfg.Session.Secure = false
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "same_site=none requires secure=true") {
		t.Fatalf("expected same_site=none secure error, got: %v", err)
	}
}

func TestValidateRejectsUnknownSameSite(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Session.SameSite = "bogus"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "same_site") {
		t.Fatalf("expected same_site error, got: %v", err)
	}
}

func TestRedactedHidesSecret(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	redacted := cfg.Redacted()
	if string(redacted.Secret) != "[REDACTED]" {
		t.Errorf("Redacted secret = %q", redacted.Secret)
	}
	// Original secret is still intact.
	if len(cfg.Secret) != 32 {
		t.Errorf("original secret length changed to %d", len(cfg.Secret))
	}
}

func TestSecretBase64RoundTrips(t *testing.T) {
	t.Setenv("DOUBLANGU_SECRET", validSecret(t))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	encoded := cfg.SecretBase64()
	if len(encoded) != 44 {
		t.Errorf("SecretBase64 length = %d", len(encoded))
	}
	if encoded == "[REDACTED]" {
		t.Errorf("SecretBase64 should not return the redacted sentinel")
	}
}

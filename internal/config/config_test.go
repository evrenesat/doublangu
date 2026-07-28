package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaultsAreSensible(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Port)
	}
	if cfg.Host != "0.0.0.0" {
		t.Errorf("expected default host 0.0.0.0, got %s", cfg.Host)
	}
	if cfg.ReadTimeout <= 0 {
		t.Errorf("expected positive ReadTimeout, got %v", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout <= 0 {
		t.Errorf("expected positive WriteTimeout, got %v", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout <= 0 {
		t.Errorf("expected positive IdleTimeout, got %v", cfg.IdleTimeout)
	}
	if cfg.ShutdownTimeout <= 0 {
		t.Errorf("expected positive ShutdownTimeout, got %v", cfg.ShutdownTimeout)
	}
}

func TestEnvVarOverrides(t *testing.T) {
	const testPort = "9090"
	os.Setenv("DOUBLANGU_PORT", testPort)
	os.Setenv("DOUBLANGU_HOST", "127.0.0.1")
	os.Setenv("DOUBLANGU_READ_TIMEOUT", "5s")
	defer func() {
		os.Unsetenv("DOUBLANGU_PORT")
		os.Unsetenv("DOUBLANGU_HOST")
		os.Unsetenv("DOUBLANGU_READ_TIMEOUT")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Port)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Host)
	}
	if cfg.ReadTimeout != 5*time.Second {
		t.Errorf("expected ReadTimeout 5s, got %v", cfg.ReadTimeout)
	}
}

func TestInvalidPort(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"port 0", 0, false},
		{"port 65536", 65536, false},
		{"port -1", -1, false},
		{"port 1", 1, true},
		{"port 65535", 65535, true},
		{"port 8080", 8080, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Port: tt.port, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: time.Second}
			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("expected error for port %d, got nil", tt.port)
			}
		})
	}
}

func TestInvalidTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		valid   bool
	}{
		{"zero timeout", 0, false},
		{"negative timeout", -1 * time.Second, false},
		{"positive timeout", 5 * time.Second, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Port: 8080, ReadTimeout: tt.timeout, WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: time.Second}
			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("expected error for timeout %v, got nil", tt.timeout)
			}
		})
	}
}

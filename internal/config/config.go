// Package config provides typed configuration for the Doublangu server,
// loaded from environment variables with sensible defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all server configuration.
type Config struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

// defaults
const (
	defaultHost            = "0.0.0.0"
	defaultPort            = 8080
	defaultReadTimeout     = 10 * time.Second
	defaultWriteTimeout    = 10 * time.Second
	defaultIdleTimeout     = 60 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

// Load reads configuration from environment variables, falling back to
// defaults for any missing or empty values.
func Load() (*Config, error) {
	cfg := &Config{
		Host:            envOrDefault("DOUBLANGU_HOST", defaultHost),
		Port:            envIntOrDefault("DOUBLANGU_PORT", defaultPort),
		ReadTimeout:     envDurationOrDefault("DOUBLANGU_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:    envDurationOrDefault("DOUBLANGU_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:     envDurationOrDefault("DOUBLANGU_IDLE_TIMEOUT", defaultIdleTimeout),
		ShutdownTimeout: envDurationOrDefault("DOUBLANGU_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that the configuration values are acceptable.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range (1–65535)", c.Port)
	}
	if c.ReadTimeout <= 0 {
		return fmt.Errorf("read_timeout must be positive, got %v", c.ReadTimeout)
	}
	if c.WriteTimeout <= 0 {
		return fmt.Errorf("write_timeout must be positive, got %v", c.WriteTimeout)
	}
	if c.IdleTimeout <= 0 {
		return fmt.Errorf("idle_timeout must be positive, got %v", c.IdleTimeout)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown_timeout must be positive, got %v", c.ShutdownTimeout)
	}
	return nil
}

// envOrDefault returns the value of the named env var, or the default if empty.
func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// envIntOrDefault returns an int from the named env var, or the default.
func envIntOrDefault(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envDurationOrDefault parses a time.Duration from the named env var.
// Returns the default on empty or invalid values.
func envDurationOrDefault(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// ErrInvalidPort is returned when port validation fails.
var ErrInvalidPort = errors.New("invalid port")

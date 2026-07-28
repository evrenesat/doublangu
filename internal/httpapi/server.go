// Package httpapi provides the HTTP API server for Doublangu.
package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"doublangu/internal/config"
)

// Version is the current API version.
const Version = "0.1.0"

// Server wraps an *http.Server with Doublangu configuration.
type Server struct {
	http *http.Server
	cfg  *config.Config
}

// New creates a new Server with the given configuration and registers routes.
func New(cfg *config.Config) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", handleHealth)

	srv := &http.Server{
		Addr:         addr(cfg.Host, cfg.Port),
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	return &Server{http: srv, cfg: cfg}
}

// addr formats host and port into a listen address string.
func addr(host string, port int) string {
	return host + ":" + itoa(port)
}

// itoa is a small int-to-string helper to avoid importing strconv just for this.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Start begins listening and serving. It returns http.ErrServerClosed on
// graceful shutdown.
func (s *Server) Start() error {
	log.Printf("starting server on %s (version %s)", s.http.Addr, Version)
	if err := s.http.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server with the configured timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("shutting down server…")
	return s.http.Shutdown(ctx)
}

// healthResponse is the JSON body for the health endpoint.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// handleHealth responds to GET /health/live.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Version: Version,
	})
}

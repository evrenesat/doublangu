package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"doublangu/internal/config"
)

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest("GET", "/health/live", nil)
	w := httptest.NewRecorder()

	handleHealth(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", body.Status)
	}
	if body.Version != Version {
		t.Errorf("expected version %q, got %q", Version, body.Version)
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	// Create a minimal server to test routing.
	cfg := testConfig()
	srv := New(cfg)
	ts := httptest.NewServer(srv.http.Handler)
	defer ts.Close()

	tests := []string{
		"/",
		"/health",
		"/api/unknown",
		"/health/live/extra",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("expected 404 for %s, got %d", path, resp.StatusCode)
			}
		})
	}
}

func TestServerStartsAndResponds(t *testing.T) {
	cfg := testConfig()
	cfg.Port = 0 // let the OS assign a port

	srv := New(cfg)

	// Start on a random port.
	l, err := netListener(0)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	srv.http.Addr = l.Addr().String()

	go func() {
		srv.http.Serve(l)
	}()

	// Give the server a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Hit the health endpoint.
	url := "http://localhost:" + itoa(port) + "/health/live"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.http.Shutdown(ctx)
}

func TestGracefulShutdown(t *testing.T) {
	cfg := testConfig()
	cfg.Port = 0

	srv := New(cfg)

	l, err := netListener(0)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	srv.http.Addr = l.Addr().String()

	// Channel to signal that serve has exited.
	serveDone := make(chan struct{})
	go func() {
		srv.http.Serve(l)
		close(serveDone)
	}()

	time.Sleep(50 * time.Millisecond)

	// Shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.http.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Wait for Serve to return.
	select {
	case <-serveDone:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down in time")
	}

	// After shutdown, the server should no longer accept connections.
	_, err = http.Get("http://localhost:" + itoa(port) + "/health/live")
	if err == nil {
		t.Error("expected connection refused after shutdown, got success")
	}
}

// testConfig returns a minimal valid config for testing.
func testConfig() *config.Config {
	return &config.Config{
		Host:            "127.0.0.1",
		Port:            0, // OS-assigned
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		IdleTimeout:     time.Second,
		ShutdownTimeout: time.Second,
	}
}

// netListener creates a TCP listener on the given port (0 = OS-assigned).
func netListener(port int) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:"+itoa(port))
}

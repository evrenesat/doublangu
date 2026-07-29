// Command doublangu-server starts the zero-plugin core and serves its health
// endpoint. Plugin loading is assembled separately by the application runtime.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"doublangu/internal/httpapi/pluginassets"
	manifest "doublangu/internal/plugins"
)

func main() {
	schema, err := manifest.LoadSchema()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: schema not available: %v\n", err)
	}
	registry := manifest.NewRegistry()
	if err := serve(listenAddress(), registry, schema, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}

func listenAddress() string {
	if address := os.Getenv("DOUBLANGU_LISTEN"); address != "" {
		return address
	}
	return ":8080"
}

func newHandler(registry *manifest.Registry, schema *manifest.ParsedSchema) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(manifest.CollectDiagnostics(registry, schema))
	})
	mux.HandleFunc("GET /api/v1/ui/contributions", func(writer http.ResponseWriter, request *http.Request) {
		snapshot, err := registry.UIContributions()
		if err != nil {
			http.Error(writer, "invalid UI contribution registry", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(snapshot)
	})
	assets, err := pluginassets.New(pluginAssetsRoot(), pluginassets.DefaultPrefix, func(*http.Request) bool {
		// CP8 replaces this explicit zero-plugin policy with the owner session
		// policy. The handler itself never treats a missing callback as allow.
		return true
	})
	if err != nil {
		mux.HandleFunc(pluginassets.DefaultPrefix, func(writer http.ResponseWriter, request *http.Request) {
			http.Error(writer, "plugin assets unavailable", http.StatusInternalServerError)
		})
	} else {
		mux.Handle(pluginassets.DefaultPrefix, assets)
	}
	return mux
}

func pluginAssetsRoot() string {
	if root := os.Getenv("DOUBLANGU_PLUGIN_ASSETS"); root != "" {
		return root
	}
	return "web/static/plugin-assets"
}

func serve(address string, registry *manifest.Registry, schema *manifest.ParsedSchema, output *os.File) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{Handler: newHandler(registry, schema)}
	fmt.Fprint(output, manifest.ZeroPluginBanner(registry, schema))
	fmt.Fprintf(output, "listening on %s\n", listener.Addr())

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stop.Done():
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

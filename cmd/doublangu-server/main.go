// Command doublangu-server starts the Doublangu HTTP API server.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"doublangu/internal/config"
	"doublangu/internal/httpapi"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	srv := httpapi.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("server: %v", err)
		}
	}()

	log.Printf("server listening on %s:%d", cfg.Host, cfg.Port)

	<-ctx.Done()
	log.Println("received shutdown signal")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}

	log.Println("server stopped cleanly")
}

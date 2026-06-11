package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sliverkiss/mimocode2api/internal/config"
	"github.com/Sliverkiss/mimocode2api/internal/handler"
	"github.com/Sliverkiss/mimocode2api/internal/middleware"
	"github.com/Sliverkiss/mimocode2api/internal/proxy"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[mimo2api] ")

	cfg := config.Load()

	fingerprint := cfg.Fingerprint
	if fingerprint == "" {
		fingerprint = proxy.GenerateFingerprint()
	}
	log.Printf("Fingerprint: %s...", fingerprint[:16])

	if _, err := proxy.GetJWT(cfg.BootstrapPath, fingerprint); err != nil {
		log.Fatalf("Bootstrap failed: %v", err)
	}
	log.Printf("JWT obtained, upstream=%s", cfg.UpstreamBase)
	log.Printf("API Key: %s", cfg.APIKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.Health())

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/v1/chat/completions", handler.ChatCompletions(handler.ProxyConfig{
		ChatURL:      cfg.ChatPath,
		BootstrapURL: cfg.BootstrapPath,
		Fingerprint:  fingerprint,
	}))
	apiMux.HandleFunc("/v1/models", handler.Models())

	mux.Handle("/v1/", middleware.Auth(cfg.APIKey)(apiMux))

	addr := fmt.Sprintf("%s:%d", cfg.BindHost, cfg.Port)
	log.Printf("Listening on %s", addr)

	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down gracefully...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
	log.Println("Server stopped")
}
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

	var fingerprints []string
	if cfg.Fingerprint != "" {
		fingerprints = []string{cfg.Fingerprint}
		log.Printf("Fingerprint: %s...", cfg.Fingerprint[:16])
	} else {
		fingerprints = proxy.GenerateFingerprints(cfg.FingerprintCount)
		log.Printf("Generated %d random fingerprint(s)", len(fingerprints))
		for i, fp := range fingerprints {
			log.Printf("  Fingerprint[%d]: %s...", i, fp[:16])
		}
	}
	pool := proxy.NewJWTPool(fingerprints, cfg.ProxyURL, cfg.ProxyEnabled)
	log.Printf("JWT pool ready (%d entries), upstream=%s", len(fingerprints), cfg.UpstreamBase)
	if cfg.ProxyURL != "" {
		log.Printf("Proxy: %s (keep-alives disabled)", cfg.ProxyURL)
	} else if cfg.ProxyEnabled {
		log.Printf("Proxy: enabled via HTTP_PROXY/HTTPS_PROXY (keep-alives disabled)")
	}
	log.Printf("API Key: %s", cfg.APIKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.Health())

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/v1/chat/completions", handler.ChatCompletions(handler.ProxyConfig{
		ChatURL:      cfg.ChatPath,
		BootstrapURL: cfg.BootstrapPath,
		Pool:         pool,
	}))
	apiMux.HandleFunc("/v1/models", handler.Models(cfg))

	mux.Handle("/v1/", middleware.Auth(cfg.APIKey)(apiMux))

	addr := fmt.Sprintf("%s:%d", cfg.BindHost, cfg.Port)
	log.Printf("Listening on %s", addr)

	server := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 60 * time.Second,
		// WriteTimeout set to 0 to support long-lived SSE streaming.
		// The upstream request has its own 300s timeout via context.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

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
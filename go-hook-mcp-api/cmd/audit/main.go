package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-hook-mcp-api/internal/audit"
	"go-hook-mcp-api/internal/otelexport"

	"github.com/go-chi/chi/v5"
)

func main() {
	ctx := context.Background()

	cfg := otelexport.Config{
		ServiceName:  envOrDefault("OTEL_SERVICE_NAME", "claude-audit-service"),
		OTLPEndpoint: envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318"),
		OTLPHeaders:  os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
	}

	providers, cleanup, err := otelexport.Setup(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "otel setup: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	tracer := otelexport.NewTracer(providers.TracerProvider, "audit-service")
	logger := otelexport.NewSlogLogger(providers.LoggerProvider, "audit-service")

	h := &audit.Handler{Tracer: tracer, Logger: logger}
	authToken := os.Getenv("AUDIT_API_TOKEN")

	r := chi.NewRouter()
	r.Get("/healthz", h.Healthz)

	r.Group(func(r chi.Router) {
		r.Use(audit.BearerAuth(authToken))
		r.Post("/hooks/pre-tool-use", h.PreToolUse)
		r.Post("/hooks/post-tool-use", h.PostToolUse)
		r.Post("/hooks/post-tool-use-failure", h.PostToolUseFailure)
		r.Post("/hooks/command", h.Command)
	})

	addr := envOrDefault("LISTEN_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		fmt.Fprintf(os.Stdout, "audit-service listening on %s\n", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Fprintln(os.Stdout, "shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

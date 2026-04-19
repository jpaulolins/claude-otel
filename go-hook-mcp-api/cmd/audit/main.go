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

	// Surface the auth configuration at startup so operators see it.
	// BearerAuth panics when the token is empty and
	// AUDIT_ALLOW_ANONYMOUS != "true" — that's the fail-closed default.
	authToken := os.Getenv("AUDIT_API_TOKEN")
	allowAnon := os.Getenv("AUDIT_ALLOW_ANONYMOUS") == "true"
	switch {
	case authToken != "":
		fmt.Fprintln(os.Stderr, "audit-service: bearer auth ENABLED (AUDIT_API_TOKEN is set)")
	case allowAnon:
		fmt.Fprintln(os.Stderr, "SECURITY WARNING: audit-service starting with anonymous access (AUDIT_API_TOKEN empty, AUDIT_ALLOW_ANONYMOUS=true). Do NOT use in production.")
	default:
		fmt.Fprintln(os.Stderr, "audit-service: AUDIT_API_TOKEN is required; set AUDIT_ALLOW_ANONYMOUS=true to opt out (local dev only)")
		// BearerAuth will panic below — we exit explicitly to produce a
		// cleaner error than a panic stack trace.
		os.Exit(1)
	}

	r := chi.NewRouter()
	r.Use(securityHeadersMiddleware)
	r.Get("/healthz", h.Healthz)

	r.Group(func(r chi.Router) {
		r.Use(audit.BearerAuth(authToken))
		r.Post("/hooks/pre-tool-use", h.PreToolUse)
		r.Post("/hooks/post-tool-use", h.PostToolUse)
		r.Post("/hooks/post-tool-use-failure", h.PostToolUseFailure)
		r.Post("/hooks/command", h.Command)
	})

	addr := envOrDefault("LISTEN_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

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

	fmt.Fprintln(os.Stderr, "shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

// securityHeadersMiddleware adds defense-in-depth response headers to every
// response served by the audit HTTP server.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

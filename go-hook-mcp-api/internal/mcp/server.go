package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer builds an MCP server with all tools registered.
//
// Authentication/authorization is injected per-request into the context:
//   - for stdio, via a receiving middleware added in ServeStdio*
//   - for Streamable HTTP, via an http.Handler middleware in front of the SDK handler
func NewServer(q Querier) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:    "claude-otel-mcp",
		Version: "1.0.0",
	}, nil)
	RegisterTools(s, q)
	adminOnly := RegisterPrompts(s, q)
	registerListPromptsRoleFilter(s, adminOnly)
	return s
}

// ServeStdio serves in stdio mode with a fixed user injected into every
// request context. The token is resolved once at startup.
//
// All log output MUST go to os.Stderr because stdout is the JSON-RPC
// protocol channel for the stdio transport.
func ServeStdio(s *sdk.Server, resolver UserResolver, token string) error {
	user, err := resolver.Resolve(token)
	if err != nil {
		return fmt.Errorf("stdio auth: %w", err)
	}
	fmt.Fprintf(os.Stderr, "mcp-server (stdio) authenticated as %s [%s]\n", user.Email, user.Role)

	injectUserMiddleware(s, user)
	return s.Run(context.Background(), &sdk.StdioTransport{})
}

// ServeStdioUnauthenticated serves stdio mode without token validation.
// Every request is executed as the provided synthetic user. Intended for local
// development only; never enable in production.
//
// All log output MUST go to os.Stderr — stdout is the protocol channel.
func ServeStdioUnauthenticated(s *sdk.Server, user User) error {
	fmt.Fprintf(os.Stderr, "SECURITY WARNING: mcp-server (stdio) running WITHOUT authentication as %s [%s] (MCP_DISABLE_AUTH=true)\n", user.Email, user.Role)

	injectUserMiddleware(s, user)
	return s.Run(context.Background(), &sdk.StdioTransport{})
}

// mcpEndpointPath is the single endpoint exposed by the Streamable HTTP
// transport (MCP spec revision 2025-03-26). It handles POST (client → server
// JSON-RPC), GET (server → client SSE stream) and DELETE (session termination).
const mcpEndpointPath = "/mcp"

// httpServerTimeouts are applied to every *http.Server constructed by this
// package. They provide a conservative baseline against slow-loris / body-hog
// attacks; the reverse proxy in front of us may impose tighter limits.
var httpServerTimeouts = struct {
	ReadHeader, Read, Write, Idle int // seconds
	MaxHeaderBytes                int
}{5, 30, 30, 60, 1 << 20}

// ServeStreamableHTTP serves the MCP server over the Streamable HTTP transport
// on a single endpoint at /mcp. Requests must carry a valid Bearer token that
// the given resolver maps to a user; otherwise the handler responds with 401.
func ServeStreamableHTTP(s *sdk.Server, addr string, resolver UserResolver) error {
	streamable := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()
	mux.Handle(mcpEndpointPath, authMiddleware(resolver, securityHeaders(streamable)))

	fmt.Fprintf(os.Stdout, "mcp-server (Streamable HTTP) listening on %s%s\n", addr, mcpEndpointPath)
	srv := newHTTPServer(addr, mux)
	return srv.ListenAndServe()
}

// ServeStreamableHTTPUnauthenticated serves the Streamable HTTP transport
// without Bearer token validation. Every request is executed as the provided
// synthetic user. Intended for local development only; never enable in
// production.
func ServeStreamableHTTPUnauthenticated(s *sdk.Server, addr string, user User) error {
	streamable := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()
	mux.Handle(mcpEndpointPath, securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		streamable.ServeHTTP(w, r.WithContext(ctx))
	})))

	fmt.Fprintf(os.Stderr, "SECURITY WARNING: mcp-server (Streamable HTTP) listening on %s%s WITHOUT authentication as %s [%s] (MCP_DISABLE_AUTH=true)\n", addr, mcpEndpointPath, user.Email, user.Role)
	srv := newHTTPServer(addr, mux)
	return srv.ListenAndServe()
}

// newHTTPServer constructs an *http.Server pre-wired with conservative
// timeouts and a bounded header size. The MCP and audit servers MUST be
// fronted by a TLS-terminating reverse proxy (see SECURITY.md).
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: secondsDuration(httpServerTimeouts.ReadHeader),
		ReadTimeout:       secondsDuration(httpServerTimeouts.Read),
		WriteTimeout:      secondsDuration(httpServerTimeouts.Write),
		IdleTimeout:       secondsDuration(httpServerTimeouts.Idle),
		MaxHeaderBytes:    httpServerTimeouts.MaxHeaderBytes,
	}
}

func secondsDuration(n int) time.Duration { return time.Duration(n) * time.Second }

// securityHeaders wraps a handler to add defense-in-depth response headers.
// Currently sets X-Content-Type-Options: nosniff. More headers (CSP, etc.)
// are the responsibility of the TLS-terminating reverse proxy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// authMiddleware validates a Bearer token, resolves the associated user and
// injects it into the request context before delegating to next. The SDK's
// Streamable handler reads ctx from the *http.Request, so values set here flow
// into the tool handlers. Rejections are logged (without the token) to
// os.Stderr for audit trails.
func authMiddleware(resolver UserResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		token := extractBearerToken(r)
		if token == "" {
			logAuthFailure(r, "missing_header")
			writeJSONError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}
		user, err := resolver.Resolve(token)
		if err != nil {
			logAuthFailure(r, "invalid_token")
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func logAuthFailure(r *http.Request, reason string) {
	fmt.Fprintf(os.Stderr, "auth_failure remote=%s path=%s reason=%s\n", r.RemoteAddr, r.URL.Path, reason)
}

// injectUserMiddleware adds a receiving-side SDK middleware that stamps the
// given user onto the context of every incoming request. Used by stdio mode
// where all requests share a single authenticated identity.
func injectUserMiddleware(s *sdk.Server, user User) {
	s.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			ctx = context.WithValue(ctx, userContextKey{}, user)
			return next(ctx, method, req)
		}
	})
}

func writeJSONError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"detail":%q}`, detail)
}

type userContextKey struct{}

func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userContextKey{}).(User)
	return u, ok
}

// extractBearerToken extracts the token from an HTTP Authorization header.
// The scheme check is case-insensitive (RFC 7235 § 2.1: auth schemes are
// case-insensitive). Whitespace around the token is trimmed.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

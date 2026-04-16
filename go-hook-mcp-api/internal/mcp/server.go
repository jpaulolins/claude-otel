package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"

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
	return s
}

// ServeStdio serves in stdio mode with a fixed user injected into every
// request context. The token is resolved once at startup.
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
func ServeStdioUnauthenticated(s *sdk.Server, user User) error {
	fmt.Fprintf(os.Stderr, "WARNING: mcp-server (stdio) running WITHOUT authentication as %s [%s] (MCP_DISABLE_AUTH=true)\n", user.Email, user.Role)

	injectUserMiddleware(s, user)
	return s.Run(context.Background(), &sdk.StdioTransport{})
}

// mcpEndpointPath is the single endpoint exposed by the Streamable HTTP
// transport (MCP spec revision 2025-03-26). It handles POST (client → server
// JSON-RPC), GET (server → client SSE stream) and DELETE (session termination).
const mcpEndpointPath = "/mcp"

// ServeStreamableHTTP serves the MCP server over the Streamable HTTP transport
// on a single endpoint at /mcp. Requests must carry a valid Bearer token that
// the given resolver maps to a user; otherwise the handler responds with 401.
func ServeStreamableHTTP(s *sdk.Server, addr string, resolver UserResolver) error {
	streamable := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()
	mux.Handle(mcpEndpointPath, authMiddleware(resolver, streamable))

	fmt.Fprintf(os.Stdout, "mcp-server (Streamable HTTP) listening on %s%s\n", addr, mcpEndpointPath)
	return http.ListenAndServe(addr, mux)
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
	mux.HandleFunc(mcpEndpointPath, func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		streamable.ServeHTTP(w, r.WithContext(ctx))
	})

	fmt.Fprintf(os.Stdout, "WARNING: mcp-server (Streamable HTTP) listening on %s%s WITHOUT authentication as %s [%s] (MCP_DISABLE_AUTH=true)\n", addr, mcpEndpointPath, user.Email, user.Role)
	return http.ListenAndServe(addr, mux)
}

// authMiddleware validates a Bearer token, resolves the associated user and
// injects it into the request context before delegating to next. The SDK's
// Streamable handler reads ctx from the *http.Request, so values set here flow
// into the tool handlers.
func authMiddleware(resolver UserResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}
		user, err := resolver.Resolve(token)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"detail":%q}`, detail)
}

type userContextKey struct{}

func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userContextKey{}).(User)
	return u, ok
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

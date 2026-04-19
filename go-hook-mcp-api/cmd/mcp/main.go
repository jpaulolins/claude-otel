package main

import (
	"fmt"
	"os"
	"strings"

	mcpint "go-hook-mcp-api/internal/mcp"
)

// disableAuthAckEnv is the second opt-in flag required to actually run the
// MCP server without authentication. MCP_DISABLE_AUTH=true alone is not
// enough — the operator must also set this to prove they understand the
// risk.
const disableAuthAckEnv = "MCP_DISABLE_AUTH_I_UNDERSTAND"

func main() {
	dsn := envOrDefault("CLICKHOUSE_DSN", "clickhouse://otel_ingest:CHANGE_ME@localhost:9000/observability")
	transport := envOrDefault("MCP_TRANSPORT", "stdio")
	disableAuth := boolEnv("MCP_DISABLE_AUTH")

	ch, err := mcpint.NewCHClient(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clickhouse: %v\n", err)
		os.Exit(1)
	}
	defer ch.Close()

	s := mcpint.NewServer(ch)

	// When auth is disabled, a synthetic user is injected into every request so
	// tools work without any credential. Intended for local development only —
	// never enable in production. Requires a second explicit opt-in
	// (MCP_DISABLE_AUTH_I_UNDERSTAND=true) to actually start.
	if disableAuth {
		if !boolEnv(disableAuthAckEnv) {
			fmt.Fprintf(os.Stderr, "refusing to start: MCP_DISABLE_AUTH=true also requires %s=true to confirm you understand the risk. This mode must never be used in production.\n", disableAuthAckEnv)
			os.Exit(1)
		}

		fmt.Fprintln(os.Stderr, "SECURITY WARNING: MCP_DISABLE_AUTH=true — MCP server will NOT require any authentication. Do NOT use in production.")
		user := anonymousUser()
		fmt.Fprintf(os.Stderr, "SECURITY WARNING: unauthenticated user synthesized as %s [%s]\n", user.Email, user.Role)

		switch transport {
		case "stdio":
			if err := mcpint.ServeStdioUnauthenticated(s, user); err != nil {
				fmt.Fprintf(os.Stderr, "stdio server: %v\n", err)
				os.Exit(1)
			}
		case "http":
			addr := envOrDefault("MCP_HTTP_ADDR", ":8081")
			if err := mcpint.ServeStreamableHTTPUnauthenticated(s, addr, user); err != nil {
				fmt.Fprintf(os.Stderr, "http server: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown transport: %s (use stdio or http)\n", transport)
			os.Exit(1)
		}
		return
	}

	// Authenticated path: load user resolver (file takes precedence, then env var).
	resolver := loadResolver()

	switch transport {
	case "stdio":
		token := os.Getenv("MCP_USER_TOKEN")
		if token == "" {
			fmt.Fprintln(os.Stderr, "error: MCP_USER_TOKEN is required for stdio mode (or set MCP_DISABLE_AUTH=true and MCP_DISABLE_AUTH_I_UNDERSTAND=true for local testing)")
			os.Exit(1)
		}
		if err := mcpint.ServeStdio(s, resolver, token); err != nil {
			fmt.Fprintf(os.Stderr, "stdio server: %v\n", err)
			os.Exit(1)
		}
	case "http":
		addr := envOrDefault("MCP_HTTP_ADDR", ":8081")
		if err := mcpint.ServeStreamableHTTP(s, addr, resolver); err != nil {
			fmt.Fprintf(os.Stderr, "http server: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown transport: %s (use stdio or http)\n", transport)
		os.Exit(1)
	}
}

// anonymousUser builds the synthetic identity used when MCP_DISABLE_AUTH=true.
// Defaults to the least-privileged viewer role so that admin-only tools remain
// unreachable unless the operator explicitly sets MCP_USER_ROLE=admin. Invalid
// role values also fall back to viewer.
func anonymousUser() mcpint.User {
	email := envOrDefault("MCP_USER_EMAIL", "anonymous@local")
	role := envOrDefault("MCP_USER_ROLE", mcpint.RoleViewer)
	if role != mcpint.RoleAdmin && role != mcpint.RoleViewer {
		role = mcpint.RoleViewer
	}
	return mcpint.User{Token: "", Email: strings.ToLower(strings.TrimSpace(email)), Role: role}
}

// loadResolver tries to load users from MCP_USERS_FILE (JSON), otherwise falls back to MCP_USER_TOKENS env var.
func loadResolver() mcpint.UserResolver {
	// 1. Try JSON file
	if path := os.Getenv("MCP_USERS_FILE"); path != "" {
		r, err := mcpint.NewResolverFromFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to load users file: %v (falling back to env)\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "loaded %d users from %s\n", r.UserCount(), path)
			return r
		}
	}

	// 2. Try env var (multi-user format: "token=email:role,token=email:role")
	if tokens := os.Getenv("MCP_USER_TOKENS"); tokens != "" {
		r := mcpint.NewResolverFromEnv(tokens)
		fmt.Fprintf(os.Stderr, "loaded %d users from MCP_USER_TOKENS\n", r.UserCount())
		return r
	}

	// 3. Single token fallback (stdio mode convenience). Defaults to the
	// least-privileged viewer role — mirrors anonymousUser() so the
	// token-authenticated fallback path does not silently grant admin.
	// Operators must explicitly set MCP_USER_ROLE=admin to escalate.
	token := os.Getenv("MCP_USER_TOKEN")
	email := envOrDefault("MCP_USER_EMAIL", "unknown@local")
	role := envOrDefault("MCP_USER_ROLE", mcpint.RoleViewer)
	if role != mcpint.RoleAdmin && role != mcpint.RoleViewer {
		fmt.Fprintf(os.Stderr, "SECURITY WARNING: invalid MCP_USER_ROLE=%q; defaulting to viewer\n", role)
		role = mcpint.RoleViewer
	}
	return mcpint.NewSingleTokenResolver(token, email, role)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// boolEnv returns true when the env var is set to a common "truthy" value.
// Accepts: 1, true, t, yes, y, on (case-insensitive).
func boolEnv(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "t", "yes", "y", "on":
		return true
	}
	return false
}

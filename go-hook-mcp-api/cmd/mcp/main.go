package main

import (
	"fmt"
	"os"
	"strings"

	mcpint "go-hook-mcp-api/internal/mcp"
)

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
	// both admin- and viewer-scoped tools work without any credential.
	// Intended for local development only — never enable in production.
	if disableAuth {
		fmt.Fprintln(os.Stderr, "WARNING: MCP_DISABLE_AUTH=true — MCP server will NOT require any authentication. Do NOT use in production.")
		user := anonymousUser()

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
			fmt.Fprintln(os.Stderr, "error: MCP_USER_TOKEN is required for stdio mode (or set MCP_DISABLE_AUTH=true for local testing)")
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
// Defaults to an admin role so every registered tool (admin + viewer) is reachable.
// Email and role can be overridden with MCP_USER_EMAIL / MCP_USER_ROLE.
func anonymousUser() mcpint.User {
	email := envOrDefault("MCP_USER_EMAIL", "anonymous@local")
	role := envOrDefault("MCP_USER_ROLE", mcpint.RoleAdmin)
	if role != mcpint.RoleAdmin && role != mcpint.RoleViewer {
		role = mcpint.RoleAdmin
	}
	return mcpint.User{Token: "", Email: email, Role: role}
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

	// 3. Single token fallback (stdio mode convenience)
	token := os.Getenv("MCP_USER_TOKEN")
	email := envOrDefault("MCP_USER_EMAIL", "unknown@local")
	role := envOrDefault("MCP_USER_ROLE", "admin")
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

# Security

This document covers transport and deployment posture for the two HTTP
services shipped by this repository:

- **audit-service** (`cmd/audit`) — ingests Claude Code hook payloads and
  emits OTEL telemetry.
- **mcp-server** (`cmd/mcp`) — serves Model Context Protocol tools and
  prompts to Claude clients.

## Transport Security

**Neither service terminates TLS in the Go process.**

The HTTP listeners opened by `cmd/audit` and `cmd/mcp` (when
`MCP_TRANSPORT=http`) are plain `http.Server` instances with conservative
timeouts (5s read-header, 30s read, 30s write, 60s idle, 1 MiB max header
bytes). Operators **MUST** do one of the following:

1. Front both services with a TLS-terminating reverse proxy (Caddy, Nginx,
   Envoy, AWS ALB, GCP HTTPS LB, Traefik, ...), *or*
2. Bind each service to `127.0.0.1` only and require clients to tunnel
   through SSH / a mesh proxy / etc.

Exposing either listener directly on a public interface without TLS is
unsupported and treated as a misconfiguration.

### Future: in-process TLS

`MCP_TLS_CERT` / `MCP_TLS_KEY` are reserved for a future flag that enables
`ListenAndServeTLS` inside the Go binary. They are **not** honored today —
setting them has no effect. If you need TLS termination in-process, file an
issue; for now, use a proxy.

## Authentication posture

### audit-service

`AUDIT_API_TOKEN` **is required**. An empty token is a startup error unless
you set `AUDIT_ALLOW_ANONYMOUS=true` as an explicit opt-in. The opt-in mode
prints a loud `SECURITY WARNING` to stderr and is intended for local
development only.

Bearer tokens are compared with `crypto/subtle.ConstantTimeCompare` and the
`Bearer` scheme is matched case-insensitively per RFC 7235.

### mcp-server

For HTTP transport, every request must carry a valid `Authorization: Bearer
<token>` header that the configured user resolver maps to a known user.
Unauthenticated mode requires **two** env vars to be truthy:

- `MCP_DISABLE_AUTH=true`, **and**
- `MCP_DISABLE_AUTH_I_UNDERSTAND=true`

Setting only the first refuses to start. Unauthenticated mode defaults the
synthetic user to `viewer` role (least-privileged); override with
`MCP_USER_ROLE=admin` at your own risk.

All failure paths (missing header, invalid token, unauthenticated-mode
gating) log structured lines to stderr; tokens are never logged.

## Secrets in telemetry

`internal/audit/redact.go` redacts well-known secret patterns from
`HookPayload.Command`, `HookPayload.ToolResponse.Stdout`, and
`HookPayload.ToolResponse.Stderr` before they are serialized or emitted as
OTEL attributes. The patterns are conservative (prefer false positives over
false negatives) and cover:

- AWS long-lived keys (`AKIA`) and session keys (`ASIA`)
- GitHub tokens (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`)
- Slack tokens (`xoxb-`, `xoxp-`, `xoxa-`, `xoxr-`, `xoxs-`)
- `Authorization: Bearer ...` headers
- Generic `api_key=`, `secret=`, `password=`, `token=` assignments
- High-entropy base64-ish blobs following `key`/`secret`/`token`

See the godoc on `redactSecrets` for the authoritative list.

## Response headers

Both HTTP services set `X-Content-Type-Options: nosniff` on every response
(auth successes, auth failures, errors, and 413/400 responses). Additional
hardening headers (CSP, HSTS, Referrer-Policy, etc.) are the responsibility
of the TLS-terminating reverse proxy.

# TODO — Cursor HTTP Proxy Interceptor

**Status:** design proposal · not implemented.
**Goal:** capture Cursor chat prompts, completions, and tool calls as OTEL events
in the same ClickHouse-backed observability stack used by Claude Code.

> Cursor does not expose a hook API equivalent to Claude Code's
> `PreToolUse`/`PostToolUse`. The cleanest way to observe its activity is to MITM
> the HTTPS traffic between the IDE and Cursor's backend, decode it, and re-emit
> structured events. This document specifies that interceptor.

---

## 1. Architecture

```
Cursor IDE
   │  (HTTPS — Cursor pins/uses public CA)
   │
   ▼
cursor-proxy :8443                 ← new Go binary, this doc
   │  (TLS terminate w/ local CA, decode, redact, forward)
   │
   ▼
api2.cursor.sh / repo42.cursor.sh  (real upstream)
   │
   ▼
Cursor IDE (response flows back through proxy)


cursor-proxy
   │
   └─ OTLP/HTTP ─► otel-collector :4318 ─► ClickHouse
        (logs + traces, schema-compatible with cotel hook events)
```

The proxy is transparent to the user once configured: Cursor sees its normal
upstream, the user sees normal latency, and every request/response pair is
mirrored as an OTEL event.

---

## 2. Domains to intercept

These already appear in `go-hook-mcp-api/internal/detect/detectors/network.go`
as known Cursor endpoints:

| Domain              | Purpose                                  |
| ------------------- | ---------------------------------------- |
| `api2.cursor.sh`    | Chat / completion / tool-call API         |
| `repo42.cursor.sh`  | Repo-context / embedding queries          |

Both are HTTPS. Other domains may surface; keep the matcher pattern-based
(`*.cursor.sh`) and log unknown hosts as `cursor.unknown_endpoint` for
discoverability.

---

## 3. The TLS challenge

Cursor pins to public CAs but does not appear to do certificate pinning beyond
that, so a locally trusted root is sufficient. Two viable approaches:

### 3a. Local mitmproxy-style CA (recommended)

1. Generate a per-host root CA on first run (`~/.cotel/cursor-proxy-ca.{crt,key}`).
2. Install the CA into the system trust store (macOS Keychain, Linux
   `/usr/local/share/ca-certificates`, Windows root store).
3. The proxy issues per-domain leaf certs on demand from that root.
4. Cursor uses the system trust store ⇒ trusts the leaf, no warnings.

Pros: standard MITM model, well-understood. Cons: requires elevated install
once; the user must explicitly opt in.

### 3b. Shipping a vendored CA

Burn a CA into the binary. Rejected: distributing a private key is unsafe and
makes revocation impossible.

**Decision:** 3a, with `cotel cursor-proxy install-ca` as a privileged
sub-command and `cotel cursor-proxy uninstall-ca` for clean removal.

---

## 4. Routing Cursor's traffic to the proxy

Cursor honors the OS proxy settings on macOS and Windows. Two install modes:

| Mode             | Command                                                           | Tradeoff                  |
| ---------------- | ----------------------------------------------------------------- | ------------------------- |
| System proxy     | `networksetup -setwebproxy "Wi-Fi" 127.0.0.1 8443` (macOS)        | All apps go through it    |
| App-scoped       | `HTTPS_PROXY=http://127.0.0.1:8443 open -a Cursor`                | Only the launched Cursor  |

App-scoped is preferred for development. Document a wrapper script
(`cotel cursor-proxy launch`) that exports the env and `exec`s Cursor.

---

## 5. Payload schema mapping

The interceptor must emit events that the existing MCP queries
(`report_activity_timeline`, `report_token_usage`, etc.) can consume without
schema changes. Reuse the same shape as Claude Code hooks
(`internal/hook/payload.go`):

```go
type HookPayload struct {
    EventType      string         `json:"event_type"`       // "cursor.chat.request" | "cursor.chat.response" | "cursor.tool.call"
    UserID         string         `json:"user_id"`          // best-effort — see §6
    SessionID      string         `json:"session_id"`       // mapped from Cursor's conversation/composer id
    ToolUseID      string         `json:"tool_use_id"`
    ToolName       string         `json:"tool_name"`        // "chat" or specific Cursor tool
    Command        string         `json:"command"`          // user prompt (redacted)
    Cwd            string         `json:"cwd"`
    PermissionMode string         `json:"permission_mode"`
    Success        bool           `json:"success"`
    ToolResponse   *ToolResponse  `json:"tool_response"`    // model output (redacted)
    Repository     string         `json:"repository"`       // derived from Cwd via Normalize()
    OrganizationID string         `json:"organization_id"`
    Timestamp      string         `json:"timestamp"`
}
```

Mapping examples:

| Cursor wire field                    | OTEL/Hook field             |
| ------------------------------------ | --------------------------- |
| `request.messages[-1].content`       | `command` (user prompt)     |
| `response.choices[0].message.content`| `tool_response.stdout`      |
| `request.model`                      | OTEL attr `model`           |
| `request.usage.{input,output}_tokens`| metric `claude_code.token.usage` |
| Cursor's `composerId`/`conversationId`| `session_id`                |
| Workspace folder path                | `cwd` (and derived `repository`) |

Re-use `internal/hook` `Normalize()` for repo derivation and secret redaction —
the same OWASP-style patterns (AWS keys, GitHub tokens, JWTs, PEM blocks)
already implemented for Claude Code apply identically to Cursor traffic.

---

## 6. Identifying the user

Cursor does not send the user email in the request (it authenticates via
session token). Three options, in priority order:

1. **Read `Cursor.app` config** — `~/Library/Application Support/Cursor/User/globalStorage/storage.json`
   often contains `cursorAuth/*` keys with the signed-in account. Best-effort,
   can change between versions.
2. **Env override** — honor `CURSOR_PROXY_USER_EMAIL` so the user can pin it
   explicitly. Useful in CI / shared boxes.
3. **Fallback** — `unknown@local` with `host.name` from `os.Hostname()` so
   events are still groupable.

Emit whichever is non-empty as `user.email` resource attribute (matches
`claude_code.token.usage` schema seen in `metric_attributes`).

---

## 7. Telemetry to emit

Per request/response pair, emit:

1. **Two OTEL spans** under one trace:
   - `cursor.chat.request` (parent) — start at request, end at response received.
   - `cursor.chat.upstream` (child) — duration of the actual upstream call.
2. **One log record** per direction (`request`, `response`) carrying the
   `HookPayload` JSON in `Body`, mirroring how `cotel hook` writes today.
3. **One sum metric** `cursor.token.usage` with attributes
   `{model, type:input|output|cacheRead|cacheCreation, user.email, session.id}`
   so it lines up with `claude_code.token.usage` aggregations.

Service identifier: `OTEL_SERVICE_NAME=cursor-proxy`. (Do not reuse
`claude-code` — the queries already join on it.)

---

## 8. Implementation outline (Go)

Scaffold under `go-hook-mcp-api/cmd/cursor-proxy`:

```
cmd/cursor-proxy/
├── main.go                # flag parsing, sub-commands (run|install-ca|launch)
├── proxy.go               # net/http/httputil.ReverseProxy + tls.Config
├── ca.go                  # local CA generation, on-demand leaf cert signing
├── decode_cursor.go       # parse api2.cursor.sh request/response bodies
└── emit.go                # build HookPayload, emit OTEL via internal/otelexport
```

Reuse:

- `internal/hook` — `HookPayload`, `Normalize()`, `redactSecrets()` (already
  battle-tested on Claude Code traffic).
- `internal/otelexport` — same OTLP/HTTP exporter used by `cotel hook`.
- `internal/detect/detectors/network.go` — domain list, kept as the source of
  truth.

Dependencies to add (likely):

- `golang.org/x/crypto` — already pulled in transitively.
- A small CA helper. `github.com/elazarl/goproxy` is the obvious choice but
  drags in a lot; consider a hand-rolled ~150-line implementation since the
  feature set we need is narrow (single CA, on-demand leaf certs, no chained
  proxies).

Estimate: 2–3 dev-days for a working POC, plus 1 day of redaction/edge cases.

---

## 9. Configuration surface

New env vars (mirror the `cotel hook` style):

| Var                              | Purpose                                              | Default                  |
| -------------------------------- | ---------------------------------------------------- | ------------------------ |
| `CURSOR_PROXY_LISTEN`            | Listen address                                       | `127.0.0.1:8443`         |
| `CURSOR_PROXY_CA_DIR`            | Where to store the local CA                          | `~/.cotel/cursor-proxy/` |
| `CURSOR_PROXY_USER_EMAIL`        | Override for `user.email`                            | empty                    |
| `OTEL_EXPORTER_OTLP_ENDPOINT`    | Same as elsewhere; reused                            | `http://localhost:4318`  |
| `OTEL_EXPORTER_OTLP_HEADERS`     | Same as elsewhere                                    | empty                    |
| `OTEL_SERVICE_NAME`              | Locked by the binary                                 | `cursor-proxy`           |

---

## 10. Security posture

- **Logs at risk:** the proxy sees every chat prompt and completion. Apply
  redaction *before* the bytes leave the proxy process. `internal/hook/redact.go`
  already covers AWS keys, GitHub/Slack tokens, Bearer credentials, PEM blocks,
  JWTs, and database URLs.
- **Cert custody:** the local CA private key is the highest-value artifact.
  `chmod 0600`, store under `~/.cotel/cursor-proxy/`, document removal in the
  uninstall flow.
- **Network exposure:** bind 127.0.0.1 only. A loopback-only proxy is trivially
  hardened; binding any other interface MUST be opt-in and behind auth.
- **Telemetry leakage:** `OTEL_EXPORTER_OTLP_ENDPOINT` should default to
  localhost. Sending to a remote collector with raw prompts is a separate
  decision that requires explicit config.

See `go-hook-mcp-api/SECURITY.md` for the broader posture this proxy should
slot into.

---

## 11. Open questions

1. **Cert pinning.** Verified empirically? If Cursor adds pinning in a future
   release, the proxy breaks silently. Detection: emit `cursor.proxy.pin_failure`
   when the upstream rejects our CA-signed leaf.
2. **gRPC / WebSocket traffic.** Cursor's "agent mode" may use long-lived
   streams. The proxy must support `httputil.ReverseProxy` with WebSocket
   upgrade. Confirm with traffic capture before committing to a plain HTTP/1.1
   proxy design.
3. **Composer ID stability.** Cursor's session/composer identifier may rotate
   per-window. Validate that aggregations like `cost_by_session` group
   meaningfully.
4. **Org attribution.** Without a Cursor SSO integration we cannot recover
   `organization.id`. Acceptable to leave empty? Or require config?
5. **Windows behavior.** macOS/Linux paths and CA trust stores are well
   understood; Windows requires `certutil` and proxy registry edits. Decide
   whether v1 is macOS-only.

---

## 12. Acceptance criteria

A POC is "done" when, with the proxy running and Cursor launched through
`cotel cursor-proxy launch`:

1. `mcp__claude-otel__report_activity_timeline` returns rows with
   `actors[0] == <expected user.email>` and `summary` containing `cursor.chat`.
2. `mcp__claude-otel__report_token_usage` returns non-zero
   `input_tokens`/`output_tokens` for the Cursor session.
3. `mcp__claude-otel__cost_by_model` shows the model identifier Cursor reports.
4. Secrets injected into a chat (e.g. `ghp_<...>`) are redacted in the stored
   `Body` field.
5. Stopping the proxy does not break Cursor (graceful degradation: Cursor
   continues to function with no proxy and we lose telemetry until restart).

---

## 13. Out of scope (for now)

- Capturing chat history retroactively from `state.vscdb`. Tracked separately
  as the SQLite-tailer alternative.
- Sending a managed CA to all developers automatically (enterprise rollout) —
  v1 is per-developer, opt-in.
- Cross-tool correlation (linking a Cursor chat to a Claude Code session that
  ran the same command). Possible later via shared `repository` and timestamp
  windowing.

---

## Appendix A — Reference: existing hook payload schema

For quick reference, the schema this proxy must produce (same one the audit
pipeline consumes today):

```json
{
  "event_type": "cursor.chat.request",
  "user_id": "joao.lins@tempest.com.br",
  "session_id": "composer-8a23eb6d-3d2a-4069-bafe-23f292f8f0e6",
  "tool_use_id": "cursor-req-01HX9...",
  "tool_name": "chat",
  "command": "explain this function",
  "cwd": "/Users/jpcbl/petuti-code/claude-otel",
  "permission_mode": "default",
  "success": true,
  "tool_response": {
    "exit_code": 0,
    "stdout": "...model output...",
    "stderr": ""
  },
  "repository": "claude-otel",
  "organization_id": "",
  "timestamp": "2026-04-27T19:51:12Z"
}
```

See `payload-hook-collector-sample.json` at the repo root for the canonical
example used by `cotel hook`.

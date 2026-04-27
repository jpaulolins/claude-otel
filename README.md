# Claude Code Observability — OTEL + ClickHouse

A self-hosted observability stack for AI coding tools. Collects token usage, costs, tool
call events, and activity timelines from Claude Code (and other MCP-capable clients) via
OpenTelemetry, stores everything in ClickHouse, and surfaces the data back through a
role-aware MCP server so your AI agent can query its own behaviour.

---

## Architecture

```
AI client (Claude Code, OpenCode, Codex CLI, …)
    │
    ├─ OTLP (metrics · logs · traces) ──────────────────────────────────┐
    │                                                                    │
    └─ HTTP hook events (PreToolUse / PostToolUse / Failure)            │
         │                                                              │
         ▼                                                              │
   audit-service :8080                                                  │
   (payload enrichment, secret redaction, OTLP re-emit)                │
         │                                                              │
         └─ OTLP logs + traces ──────────────────────────────────────  │
                                                                        ▼
                                                          otel-collector :4318
                                                          (OTLP → ClickHouse exporter)
                                                                        │
                                                                        ▼
                                                            ClickHouse :9000
                                                          database: observability
                                                                        │
                                                                        ▼
                                                            mcp-server :8081
                                                       11 tools · 7 prompts · role auth
                                                                        │
                                                     MCP (Streamable HTTP, rev 2025-03-26)
                                                                        │
                                                                        ▼
                                                    AI client (Claude Code, Cursor, …)
```

---

## Capabilities

- **Token and cost tracking** — by model, session, developer, and repository.
- **Activity timeline** — per-session event log with tool calls, errors, and timing.
- **Developer ROI analysis** — estimated hours saved, cost, and net benefit; filterable
  by developer, repository, date range, or group.
- **Role-aware MCP server** — 11 tools and 7 prompts; `admin` users see everything,
  `viewer` users are scoped to their own data automatically.
- **AI-native prompts** — 7 server-side MCP prompts (daily standup, weekly digest,
  token/cost reports, ROI snapshot, cross-developer comparison) surface as slash-commands
  in any MCP-capable client.
- **Secret redaction** — AWS keys, GitHub tokens, Slack tokens, Bearer credentials,
  PEM blocks, JWTs, and database URLs are scrubbed from all payloads before storage.
- **Reproducible stack** — every image is pinned; `./start.sh up-mcp` brings up the
  full stack in one command.

---

## Quick Install (self-hosted)

**Prerequisites:** Docker ≥ 24, Go ≥ 1.22 (for local builds only).

```bash
# 1. Clone and enter the repository
git clone <repo-url> && cd claude-otel

# 2. Start the full stack including the MCP server
./start.sh up-mcp

# 3. Verify health
curl -fsS http://localhost:8080/healthz   # audit-service
curl -fsS http://127.0.0.1:13133/        # otel-collector

# 4. Seed synthetic data (optional but recommended for first-run testing)
./seed-data.sh
```

For detailed operations (build, docker-compose options, schema management, ClickHouse
queries, production auth setup) see **[docs/operations.md](docs/operations.md)**.

---

## Connect an AI client

Copy the ready-to-use config from `config-samples/<tool>/` to your project root and
replace `CHANGE_ME` with your token (`admin-token` for local development).

| Client | Config to copy | Full setup |
|--------|----------------|-----------|
| **Claude Code** | `config-samples/claude-code/` | [docs/client-setup.md#claude-code](docs/client-setup.md#claude-code) |
| **Gemini CLI** (≥ v0.26) | `config-samples/gemini/.gemini/settings.json` | [docs/client-setup.md#gemini-cli](docs/client-setup.md#gemini-cli) |
| **OpenCode** | `config-samples/opencode/opencode.json` | [docs/client-setup.md#opencode](docs/client-setup.md#opencode) |
| **Codex CLI** | `config-samples/codex/` | [docs/client-setup.md#codex-cli](docs/client-setup.md#codex-cli) |
| **Cursor** | `config-samples/cursor/.cursor/mcp.json` | [docs/client-setup.md#cursor](docs/client-setup.md#cursor) |

```bash
# Claude Code (full telemetry + hooks):
cp config-samples/claude-code/.mcp.json .
cp config-samples/claude-code/claude-managed-settings.json .

# Gemini CLI (full telemetry + hooks):
mkdir -p .gemini && cp config-samples/gemini/.gemini/settings.json .gemini/settings.json

# Cursor (MCP only — see Known limitations below):
mkdir -p .cursor && cp config-samples/cursor/.cursor/mcp.json .cursor/mcp.json
```

---

## Known limitations

End-to-end telemetry coverage varies per client. Today only **Claude Code** and
**Gemini CLI (≥ v0.26)** ship complete: MCP + native OTEL (metrics/logs/traces)
+ tool-call hooks landing in ClickHouse via `cotel hook`. The remaining clients
have partial coverage:

- **OpenCode** — MCP works, but native OTEL signals depend on a community
  plugin ([`@devtheops/opencode-plugin-otel`](https://github.com/DEVtheOPS/opencode-plugin-otel)).
  Hooks via the plugin system fire for native tools but **not** for MCP tool
  calls (upstream issue [sst/opencode#2319](https://github.com/sst/opencode/issues/2319)).
  No first-party `cotel hook` integration yet.
- **Cursor** — MCP works, but Cursor exposes no hook API and no OTEL
  configuration. The only viable capture path is a local HTTP proxy
  intercepting `api2.cursor.sh` / `repo42.cursor.sh`. Design proposal in
  [`docs/cursor-todo-http-proxy.md`](docs/cursor-todo-http-proxy.md);
  not yet implemented.
- **Codex CLI** — MCP works. Native OTEL is undocumented in the CLI; no hook
  surface. Same proxy-interceptor pattern would apply.

Until those gaps close, MCP tool activity for these clients is captured
server-side via the MCP server's own OTLP instrumentation only — there is no
visibility into prompts, tool inputs, or token usage emitted by the client
itself.

---

## Test the MCP server

Exercise all 11 tools and 7 prompts across admin and viewer roles, for every supported
client: **[docs/e2e-testing.md](docs/e2e-testing.md)**

---

## Security

Transport security posture, auth layers, redaction patterns, and production
hardening checklist: **[go-hook-mcp-api/SECURITY.md](go-hook-mcp-api/SECURITY.md)**

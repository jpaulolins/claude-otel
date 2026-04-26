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
| **OpenCode** | `config-samples/opencode/opencode.json` | [docs/client-setup.md#opencode](docs/client-setup.md#opencode) |
| **Codex CLI** | `config-samples/codex/` | [docs/client-setup.md#codex-cli](docs/client-setup.md#codex-cli) |
| **Cursor** | `config-samples/cursor/.cursor/mcp.json` | [docs/client-setup.md#cursor](docs/client-setup.md#cursor) |
| **Gemini CLI** | `config-samples/gemini/GEMINI.md` | [docs/client-setup.md#gemini-cli](docs/client-setup.md#gemini-cli) |

```bash
# Claude Code (full telemetry + hooks):
cp config-samples/claude-code/.mcp.json .
cp config-samples/claude-code/claude-managed-settings.json .

# Cursor (MCP only):
mkdir -p .cursor && cp config-samples/cursor/.cursor/mcp.json .cursor/mcp.json
```

---

## Test the MCP server

Exercise all 11 tools and 7 prompts across admin and viewer roles, for every supported
client: **[docs/e2e-testing.md](docs/e2e-testing.md)**

---

## Security

Transport security posture, auth layers, redaction patterns, and production
hardening checklist: **[go-hook-mcp-api/SECURITY.md](go-hook-mcp-api/SECURITY.md)**

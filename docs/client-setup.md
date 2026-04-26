# Client Setup Guide

This guide walks through connecting each supported AI client to the MCP server and,
where the client supports it, configuring OTEL telemetry export so that the client's
own metrics/logs/traces flow into the same ClickHouse database.

**Prerequisites for all clients:** the stack must be running before you open the client.

```bash
./start.sh up-mcp   # starts clickhouse + otel-collector + audit-service + mcp-server
```

---

## Telemetry support matrix

| Client | MCP | OTEL telemetry | Hook events |
|--------|-----|----------------|-------------|
| Claude Code | ✓ | ✓ full (env vars + HTTP hooks) | ✓ |
| OpenCode | ✓ | ✓ env vars (best-effort) | — |
| Codex CLI | ✓ | — (not documented) | — |
| Cursor | ✓ | — (TODO) | — |
| Gemini CLI | ✓ | — (TODO) | — |

> **TODO (Cursor & Gemini CLI):** A future release will ship a local interceptor binary
> built from `go-hook-mcp-api/cmd/audit` that acts as an HTTP proxy between these clients
> and their respective AI APIs. The interceptor will emit OTLP events mirroring the hook
> payload schema already used by Claude Code. No code changes to the clients are needed.

---

## Claude Code

Full telemetry: OTLP metrics/logs/traces **and** HTTP hook events (PreToolUse,
PostToolUse, PostToolUseFailure) → audit-service → ClickHouse.

### 1. Copy config files

```bash
cp config-samples/claude-code/.mcp.json .
cp config-samples/claude-code/claude-managed-settings.json .
```

### 2. Replace placeholder tokens

In `claude-managed-settings.json`:
- `CLAUDE_HOOK_TOKEN` — must match `AUDIT_API_TOKEN` in `docker-compose.yml` (default: `CHANGE_ME`).
- `OTEL_EXPORTER_OTLP_HEADERS` — the collector does not require a token by default; keep or remove the `Authorization` header as needed.

In `.mcp.json`:
- The token resolves from `$CLAUDE_OTEL_MCP_TOKEN` at startup; defaults to `admin-token` (matches `docker-compose.yml`).
- Export `CLAUDE_OTEL_MCP_TOKEN=viewer-token` to run as a viewer-scoped session.

### 3. Start Claude Code

```bash
export CLAUDE_HOOK_TOKEN=CHANGE_ME
# To isolate to project settings only:
claude --setting-sources project,local --settings ./claude-managed-settings.json
```

Or simply open Claude Code from the repository root — it auto-loads `.mcp.json`.

### What gets captured

| Signal | Source | Destination |
|--------|--------|-------------|
| Metrics (tokens, cost, sessions) | Claude Code OTEL SDK | otel-collector → ClickHouse |
| Logs + traces | Claude Code OTEL SDK | otel-collector → ClickHouse |
| Hook events (tool calls) | PreToolUse / PostToolUse HTTP hooks | audit-service → otel-collector → ClickHouse |

---

## OpenCode

MCP connection + best-effort OTEL telemetry via standard environment variables.

### 1. Copy config file

```bash
cp config-samples/opencode/opencode.json .
```

### 2. Replace the token

In `opencode.json`, set `Authorization` → `Bearer <your-token>`.
Default local development token: `admin-token`.

### 3. Export OTEL environment variables

OpenCode may pick up standard `OTEL_*` env vars from the shell environment. Export these
before launching `opencode`:

```bash
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_SERVICE_NAME=opencode
```

> If OpenCode does not emit OTEL signals, tool call activity is still captured
> server-side through the MCP server's own OTLP instrumentation.

### 4. Start OpenCode

```bash
opencode
```

---

## Codex CLI

MCP connection via `config.toml`. OTEL telemetry is not currently documented for Codex CLI.

### 1. Copy config files

```bash
# Project-level MCP config:
mkdir -p .codex
cp config-samples/codex/config.toml .codex/config.toml

# Context file read by Codex at startup:
cp config-samples/codex/AGENTS.md AGENTS.md
```

### 2. Replace the token

In `.codex/config.toml`, set `Authorization = "Bearer <your-token>"`.
Default local development token: `admin-token`.

### 3. Start Codex CLI

```bash
codex
```

> **Telemetry:** Codex CLI does not have documented OTEL export support. Tool call
> activity is captured server-side via the MCP server's OTLP instrumentation.
>
> **TODO:** See the future interceptor plan in the [telemetry support matrix](#telemetry-support-matrix).

---

## Cursor

MCP connection only. OTEL telemetry is not available from Cursor.

### 1. Copy config file

```bash
mkdir -p .cursor
cp config-samples/cursor/.cursor/mcp.json .cursor/mcp.json
```

### 2. Replace the token

In `.cursor/mcp.json`, set `Authorization` → `Bearer <your-token>`.
Default local development token: `admin-token`.

### 3. Open Cursor

Cursor reads `.cursor/mcp.json` automatically from the project root. Restart the IDE
after adding or modifying the file.

> **Telemetry:** Cursor does not expose user-configurable OTEL hooks. Tool call activity
> is captured server-side via the MCP server's OTLP instrumentation.
>
> **TODO:** See the future interceptor plan in the [telemetry support matrix](#telemetry-support-matrix).

---

## Gemini CLI

MCP connection only. OTEL telemetry is not available from Gemini CLI.

### 1. Copy context file

```bash
cp config-samples/gemini/GEMINI.md GEMINI.md
```

### 2. Configure MCP in Gemini CLI settings

Add the server to your Gemini CLI MCP configuration. Refer to the
[Gemini CLI documentation](https://github.com/google-gemini/gemini-cli) for the current
config file location and format, then point it at:

```
url: http://localhost:8081/mcp
Authorization: Bearer CHANGE_ME
```

Default local development token: `admin-token`.

### 3. Start Gemini CLI

```bash
gemini
```

> **Telemetry:** Gemini CLI does not expose user-configurable OTEL hooks. Tool call
> activity is captured server-side via the MCP server's OTLP instrumentation.
>
> **TODO:** See the future interceptor plan in the [telemetry support matrix](#telemetry-support-matrix).

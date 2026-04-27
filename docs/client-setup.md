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
| Claude Code | ✓ | ✓ full (env vars) | ✓ (`PreToolUse` / `PostToolUse`) |
| Gemini CLI (≥ v0.26) | ✓ | ✓ full (settings.json) | ✓ (`BeforeTool` / `AfterTool`) |
| OpenCode | ✓ | ✓ via [`@devtheops/opencode-plugin-otel`](https://github.com/DEVtheOPS/opencode-plugin-otel) | ✓ via plugin (gap on MCP tool calls — issue [#2319](https://github.com/sst/opencode/issues/2319)) |
| Codex CLI | ✓ | — (not documented) | — |
| Cursor | ✓ | — | — (see `docs/cursor-todo-http-proxy.md`) |

> **`cotel hook` accepts both Claude Code and Gemini CLI payload schemas
> transparently.** The same binary wired into either client's hook config emits
> identical OTEL spans, so dashboards and MCP queries work uniformly across
> both clients with no wrapper script.

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

Full coverage as of Gemini CLI v0.26+: MCP, OTEL telemetry (metrics/logs/traces),
and lifecycle hooks (`BeforeTool` / `AfterTool`).

### 1. Copy config files

```bash
# Project-level settings (MCP + telemetry + hooks):
mkdir -p .gemini
cp config-samples/gemini/.gemini/settings.json .gemini/settings.json

# Optional context file Gemini reads at startup:
cp config-samples/gemini/GEMINI.md GEMINI.md
```

### 2. Replace the token

In `.gemini/settings.json`, set `Authorization` → `Bearer <your-token>`.
Default local development token: `admin-token`.

### 3. Verify `cotel` is on PATH

The hooks shell out to `cotel hook`. Install the binary if not already:

```bash
cd go-hook-mcp-api && make build
sudo install bin/cotel /usr/local/bin/cotel
which cotel   # should print /usr/local/bin/cotel
```

The `cotel` binary auto-detects both Claude Code and Gemini CLI hook payload
schemas, so the same binary works for both clients without any wrapper.

### 4. Start Gemini CLI

```bash
gemini
```

### What gets captured

| Signal | Source | Destination |
|--------|--------|-------------|
| Metrics (`gemini_cli.token.usage`, `tool.call.*`, `api.request.*`) | Gemini CLI native OTEL | otel-collector → ClickHouse |
| Logs (`gemini_cli.user_prompt`, `tool_call`, `api_response`, …) | Gemini CLI native OTEL | otel-collector → ClickHouse |
| Traces (`tool_call`, `llm_call`, `user_prompt`) | Gemini CLI native OTEL (when `traces: true`) | otel-collector → ClickHouse |
| Hook events (`BeforeTool` / `AfterTool`) | Gemini hook → `cotel hook` | otel-collector → ClickHouse |

> **Note:** Gemini CLI uses `GEMINI_TELEMETRY_*` env vars (not the standard
> `OTEL_*` names). The `settings.json` shipped here keeps the equivalent config
> in the file's `telemetry` block so no shell exports are needed.

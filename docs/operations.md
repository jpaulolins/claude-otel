# Operations Guide

This guide covers building, running, and operating the full observability stack locally.

## Components

- **`audit-service`** (Go): receives AI client hook events and sends OTLP logs/traces to the collector.
- **`mcp-server`** (Go): MCP server (Streamable HTTP transport, MCP spec rev 2025-03-26) exposing 11 ClickHouse query tools for auditing tokens, costs, hooks, and traces.
- **`otel-collector`**: OTLP HTTP on `4318`, exports to ClickHouse (`create_schema: true`). Image pinned to `otel/opentelemetry-collector-contrib:0.149.0` for reproducible config.
- **`clickhouse`**: database `observability`, user `otel_ingest` (see `clickhouse/init/01-init.sql`).

## Build (Go)

```bash
cd go-hook-mcp-api
make build        # builds bin/audit-service and bin/mcp-server
make test         # run all tests
make test-v       # run all tests (verbose)
```

## Run end-to-end

1. Start the stack (build + detached):

   ```bash
   ./start.sh up
   ```

   Or: `docker compose up --build -d`

   To also start the MCP server (Streamable HTTP transport, port 8081):

   ```bash
   ./start.sh up-mcp
   ```

2. Health checks:

   ```bash
   curl -fsS http://localhost:8080/healthz
   # Collector health_check extension: HTTP GET / -> 200
   curl -fsS http://127.0.0.1:13133/
   ```

3. Send a sample hook payload:

   ```bash
   curl -X POST http://localhost:8080/hooks/post-tool-use \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer CHANGE_ME" \
     --data-binary @payload-hook-collector-sample.json
   ```

4. Query ClickHouse (table exists after init; rows appear after you send telemetry):

   ```bash
   docker compose exec clickhouse clickhouse-client \
     --query "SELECT Timestamp, Body, SeverityText FROM observability.otel_logs ORDER BY Timestamp DESC LIMIT 5"
   ```

5. Stop and remove containers, volumes, and local compose images:

   ```bash
   ./start.sh down
   ```

Other `./start.sh` commands: `up-mcp`, `restart`, `status`, `logs`. Use `./start.sh --help` for usage.

## Seed data

Populate ClickHouse with realistic synthetic data for testing:

```bash
./seed-data.sh
```

The script emits hook events and OTLP metrics across multiple developers, repositories,
and sessions. A validation block at the end queries ClickHouse and prints row counts and
distinct `repository` / `organization_id` values. Use `log_warn` output to spot missing
attributes without aborting.

## MCP Server authentication

### Development flag (`MCP_DISABLE_AUTH`)

> **NOTE — local development only.** When `MCP_DISABLE_AUTH=true`, no token is required.
> Every request runs as a synthetic admin user (configurable via `MCP_USER_EMAIL` /
> `MCP_USER_ROLE`). `docker-compose.yml` ships with this flag enabled by default.

### Production setup

**Do NOT run with `MCP_DISABLE_AUTH=true` in production.** Anyone who can reach the MCP
endpoint can read all audited data.

To enable authentication:

1. Set `MCP_DISABLE_AUTH: "false"` (or remove the variable) in `docker-compose.yml`.

2. Configure the user/token mapping:

   - **Inline via `MCP_USER_TOKENS`** (simplest):

     ```yaml
     MCP_USER_TOKENS: "REAL_ADMIN_TOKEN=admin@company.com:admin,REAL_VIEWER_TOKEN=user@company.com:viewer"
     ```

   - **Or via a JSON file** (`MCP_USERS_FILE`; see `go-hook-mcp-api/mcp-users.json.example`):

     ```yaml
     volumes:
       - ./mcp-users.json:/etc/mcp/users.json:ro
     environment:
       MCP_USERS_FILE: /etc/mcp/users.json
     ```

3. Rotate the default tokens. Replace `admin-token` / `viewer-token` with long, random
   secrets (`openssl rand -hex 32`) and keep them out of version control.

4. Restrict network exposure. Bind port `8081` to `127.0.0.1` or put the service behind
   a reverse proxy with TLS.

5. Update the `Authorization` header in your client config (e.g. export
   `CLAUDE_OTEL_MCP_TOKEN=<REAL_ADMIN_TOKEN>` before launching Claude Code).

## MCP transport modes

| Var | Default | Description |
|-----|---------|-------------|
| `MCP_TRANSPORT` | `http` | `http` for Streamable HTTP, `stdio` for subprocess mode |
| `MCP_HTTP_ADDR` | `:8081` | Listen address for the Streamable HTTP transport |

### Stdio mode (optional, local process)

Replace the `claude-otel` entry in `.mcp.json` with:

```json
{
  "mcpServers": {
    "claude-otel": {
      "command": "go",
      "args": ["run", "./go-hook-mcp-api/cmd/mcp"],
      "env": {
        "CLICKHOUSE_DSN": "clickhouse://otel_ingest:CHANGE_ME@localhost:9000/observability",
        "MCP_TRANSPORT": "stdio",
        "MCP_USER_TOKEN": "CHANGE_ME",
        "MCP_USER_EMAIL": "admin@example.com",
        "MCP_USER_ROLE": "admin"
      }
    }
  }
}
```

Or run the compiled binary:

```bash
cd go-hook-mcp-api && make build
# use "command": "./go-hook-mcp-api/bin/mcp-server" in .mcp.json
```

## Claude Code settings

`config-samples/claude-code/claude-managed-settings.json` wires Claude Code to this stack:

| Item | Value |
|------|-------|
| Hook URLs | `http://localhost:8080/hooks/...` (audit-service) |
| `CLAUDE_HOOK_TOKEN` | Must match `AUDIT_API_TOKEN` in `docker-compose.yml` (default `CHANGE_ME`) |
| OTLP from Claude Code | `http://localhost:4318` (collector published on the host) |

Run Claude Code isolated to these settings:

```bash
export CLAUDE_HOOK_TOKEN=CHANGE_ME
claude --setting-sources project,local --settings ./claude-managed-settings.json
```

## Project structure

```
claude-otel/
├── config-samples/                    # Per-client MCP + OTEL config samples
│   ├── claude-code/
│   ├── cursor/
│   ├── opencode/
│   ├── codex/
│   └── gemini/
├── clickhouse/init/                   # ClickHouse init SQL scripts
├── docker-compose.yml                 # Full stack (+ mcp profile)
├── otel-collector-config.yaml         # OTEL Collector config
├── payload-hook-collector-sample.json # Sample hook payload
├── querys.md                          # ClickHouse query reference
├── seed-data.sh                       # Synthetic data seeder
├── start.sh                           # Stack management (up, up-mcp, down, …)
│
└── go-hook-mcp-api/                   # Go project (self-contained)
    ├── go.mod / go.sum
    ├── Makefile
    ├── Dockerfile.audit-service
    ├── Dockerfile.mcp-server
    ├── cmd/
    │   ├── audit/main.go
    │   └── mcp/main.go
    └── internal/
        ├── audit/                     # handler, middleware, payload, redact
        ├── otelexport/                # OTLP provider setup
        └── mcp/                       # server, tools, prompts, auth, ClickHouse client
```

## ClickHouse schema

### `clickhouse/init/01-init.sql`

Reasserts database **`observability`**. User `otel_ingest` credentials come only from
`docker-compose.yml`.

### `clickhouse/init/02-otel-schema.sql`

Creates `otel_logs`, `otel_traces`, `otel_traces_trace_id_ts`, and the materialized view
`otel_traces_trace_id_ts_mv`. Metrics tables are created by the collector with
`create_schema: true`.

### If you see `UNKNOWN_TABLE ... otel_logs`

ClickHouse only runs `/docker-entrypoint-initdb.d` on first data directory init. Either:

- Recreate the volume: `./start.sh down` then `./start.sh up`, or
- Apply the schema manually:

  ```bash
  docker compose exec -T clickhouse clickhouse-client --multiquery < clickhouse/init/02-otel-schema.sql
  ```

## Interactive ClickHouse session

```bash
docker compose exec clickhouse clickhouse-client \
  --user otel_ingest --password 'CHANGE_ME' --database observability
```

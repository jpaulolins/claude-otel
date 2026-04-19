# Claude Code audit with OpenTelemetry and ClickHouse

## Components

- **`audit-service`** (Go): receives Claude hook events and sends OTLP logs/traces to the collector.
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

## MCP Server

The MCP server exposes 11 tools for querying ClickHouse audit data (8 admin + 3 role-aware report tools). See `querys.md` for the full query reference.

### MCP authentication feature flag (`MCP_DISABLE_AUTH`)

> **NOTE — local development only.** The MCP server ships with an
> authentication bypass controlled by the `MCP_DISABLE_AUTH` environment
> variable. When it is set to a truthy value (`true`, `1`, `yes`, `on`), **no
> token is required for any MCP request** — neither for admin tools nor for
> viewer tools. Every incoming request is executed as a synthetic user
> (defaults to `anonymous@local` with the `admin` role, configurable via
> `MCP_USER_EMAIL` / `MCP_USER_ROLE`).
>
> This flag exists to make local testing easier: you can connect to
> `http://localhost:8081/mcp` with any `Authorization` header (or none at all)
> and all 11 tools become reachable immediately.
>
> **`docker-compose.yml` has this flag enabled by default** (`MCP_DISABLE_AUTH:
> "true"`) precisely so that `./start.sh up-mcp` gives you a frictionless dev
> environment.

#### Production recommendation

**Do NOT run the MCP server with `MCP_DISABLE_AUTH=true` in production.**
Disabling auth means anyone who can reach the MCP endpoint can read every
audited token, cost, session, hook event, and trace across every user in your
ClickHouse database. In production, always require Bearer tokens.

To enable authentication (the default when the flag is absent or `false`):

1. **Set the flag to `false`** (or remove the variable) in
   `docker-compose.yml`:

   ```yaml
   mcp-server:
     environment:
       MCP_DISABLE_AUTH: "false"
   ```

2. **Configure the user/token mapping** using either of these two mechanisms:

   - **Inline via `MCP_USER_TOKENS`** (simplest):

     ```yaml
     MCP_USER_TOKENS: "REAL_ADMIN_TOKEN=admin@company.com:admin,REAL_VIEWER_TOKEN=user@company.com:viewer"
     ```

   - **Or via a JSON file** mounted into the container, pointed at by
     `MCP_USERS_FILE` (see `go-hook-mcp-api/mcp-users.json.example` for the
     schema):

     ```yaml
     volumes:
       - ./mcp-users.json:/etc/mcp/users.json:ro
     environment:
       MCP_USERS_FILE: /etc/mcp/users.json
     ```

3. **Rotate the default tokens.** The `admin-token` / `viewer-token` values in
   `docker-compose.yml` are placeholders. Replace them with long,
   unpredictable secrets (e.g. `openssl rand -hex 32`) and keep them out of
   version control (use Docker secrets, a `.env` file listed in
   `.gitignore`, or your platform's secret manager).

4. **Restrict network exposure.** Publish port `8081` only on a trusted
   interface (e.g. bind to `127.0.0.1:8081` or put the service behind a
   reverse proxy with TLS and its own auth layer).

5. **Point Claude Code at the real token.** Update the `Authorization` header
   in `.mcp.json` — for example by exporting
   `CLAUDE_OTEL_MCP_TOKEN=<REAL_ADMIN_TOKEN>` before launching Claude Code.

With the flag disabled and `MCP_USER_TOKENS` (or `MCP_USERS_FILE`) populated,
the Streamable HTTP handler rejects any request to `/mcp` without a valid
`Authorization: Bearer <token>` header with HTTP `401`, and admin-scoped tools
additionally reject tokens whose role is not `admin`.

### Tools available

| Tool | Scope | Description |
|------|-------|-------------|
| `recent_logs` | admin | Recent logs with body preview |
| `log_counts` | admin | Log volume by service/severity |
| `cost_by_model` | admin | Cost in USD by model |
| `cost_by_session` | admin | Cost in USD by session |
| `available_metrics` | admin | List available sum metrics |
| `trace_spans` | admin | Trace spans by service/name |
| `hook_trace_duration` | admin | Hook spans with duration |
| `metric_attributes` | admin | Discover metric attributes |
| `report_activity_timeline` | role-aware | Sessions/activity grouped by entity |
| `report_token_usage` | role-aware | Token + cost usage by developer/model |
| `report_developer_roi` | role-aware | ROI analysis (cost, trend, by model/repo) |

### MCP Prompts

In addition to tools, the MCP server exposes 7 server-side prompts (the MCP
`prompt` primitive). Prompts surface in MCP-aware clients as
ready-to-run slash-commands or canned actions and emit English instructions
that guide the agent to call the right `report_*` / `cost_by_*` tool and shape
the final report. Prompts do not execute queries themselves.

| Prompt | Scope | Underlying tool(s) |
|--------|-------|--------------------|
| `daily_agent_standup` | authenticated | `report_activity_timeline` (24h, agent) |
| `weekly_activity_digest` | authenticated | `report_activity_timeline` (7d) |
| `token_and_cost_week` | authenticated | `report_token_usage` (week) |
| `token_and_cost_month` | authenticated | `report_token_usage` (month or custom range) |
| `cost_drilldown_repository` | authenticated | `report_token_usage` + `cost_by_session` (admin only) |
| `roi_executive_snapshot` | authenticated | `report_developer_roi` |
| `compare_developers_cost` | admin | `report_developer_roi` (no developer filter) |

Role rules:

- Viewers see 6 prompts; `compare_developers_cost` is admin-only and hidden
  from `prompts/list` for non-admins (and rejected if called directly).
- For role-aware prompts, the rendered text forces `developer="<viewer email>"`
  for viewers so the agent cannot fan out across other developers.
- Unauthenticated callers receive an empty prompt list and an `unauthorized`
  error on `prompts/get`.

### Claude Code: Streamable HTTP (default in `.mcp.json`)

The MCP server uses the **Streamable HTTP** transport from MCP spec revision
`2025-03-26`: a single endpoint at `/mcp` that handles `POST` (client → server
JSON-RPC), `GET` (server → client SSE stream for notifications and
long-running responses) and `DELETE` (session termination). This replaces the
deprecated HTTP+SSE two-endpoint transport.

Use the MCP container (no local `go` on PATH required). Start the stack with the MCP profile, then open Claude Code from the repo root:

```bash
./start.sh up-mcp
```

Project `.mcp.json` connects to `http://localhost:8081/mcp` with:

- **`Authorization`:** `Bearer ${CLAUDE_OTEL_MCP_TOKEN:-admin-token}` (expand env at startup; default matches `MCP_USER_TOKENS` in `docker-compose.yml`).

Set `CLAUDE_OTEL_MCP_TOKEN` to `viewer-token` (or another token from `MCP_USER_TOKENS`) when you want viewer-scoped tools only.

### Stdio mode (optional, local process)

If you prefer a subprocess instead of Docker MCP, replace the `claude-otel` entry in `.mcp.json` with:

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
# then in .mcp.json use "command": "./go-hook-mcp-api/bin/mcp-server" instead of "go"
```

### Streamable HTTP server details (multi-user)

The MCP server listens on port `8081` at path `/mcp` with Bearer token auth.
Configure users via `MCP_USER_TOKENS` env var in `docker-compose.yml`:

```
MCP_USER_TOKENS="token1=alice@example.com:admin,token2=bob@example.com:viewer"
```

Transport settings (docker-compose defaults):

| Var | Default | Description |
|-----|---------|-------------|
| `MCP_TRANSPORT` | `http` | `http` for Streamable HTTP, `stdio` for subprocess mode |
| `MCP_HTTP_ADDR` | `:8081` | Listen address for the Streamable HTTP transport |

### Authentication

- **Stdio**: single-user, token set via `MCP_USER_TOKEN` env var
- **Streamable HTTP**: multi-user, tokens mapped to `email:role` via `MCP_USER_TOKENS` env var (`token=email:role` triples, comma-separated) or a JSON file via `MCP_USERS_FILE`
- **Audit service hooks**: Bearer token via `AUDIT_API_TOKEN` / `CLAUDE_HOOK_TOKEN`

## Claude Code: `claude-managed-settings.json` (local tests)

The file `claude-managed-settings.json` is aligned with this repo:

| Item | Value |
|------|--------|
| Hook URLs | `http://localhost:8080/hooks/...` (audit-service) |
| `CLAUDE_HOOK_TOKEN` | Must match `AUDIT_API_TOKEN` in `docker-compose.yml` (default `CHANGE_ME`) |
| OTLP from Claude Code | `http://localhost:4318` (collector published on the host) |

### Run Claude Code with this file only (avoid loading user/global settings)

From this repository root, with the stack running:

```bash
export CLAUDE_HOOK_TOKEN=CHANGE_ME
claude --setting-sources project,local --settings ./claude-managed-settings.json
```

## Project structure

```
claude-otel/
├── .mcp.json                          # MCP for Claude Code (Streamable HTTP → localhost:8081/mcp)
├── claude-managed-settings.json       # Claude Code hooks + OTEL env
├── clickhouse/init/                   # ClickHouse init SQL scripts
├── docker-compose.yml                 # Full stack (+ mcp profile)
├── otel-collector-config.yaml         # OTEL Collector config
├── payload-hook-collector-sample.json # Sample hook payload
├── querys.md                          # 15 ClickHouse query reference
├── README.md
├── start.sh                           # Stack management (up, up-mcp, down, restart, status, logs)
│
└── go-hook-mcp-api/                   # Go project (self-contained)
    ├── go.mod / go.sum
    ├── Makefile                       # build, test, lint, clean
    ├── Dockerfile.audit-service       # Multi-stage Go build
    ├── Dockerfile.mcp-server          # Multi-stage Go build
    ├── cmd/
    │   ├── audit/main.go              # Audit service entrypoint
    │   └── mcp/main.go                # MCP server entrypoint
    └── internal/
        ├── audit/                     # HTTP handlers, auth middleware, payload parsing
        │   ├── handler.go / handler_test.go
        │   ├── middleware.go / middleware_test.go
        │   └── payload.go / payload_test.go
        ├── otelexport/                # OTLP trace + log provider setup
        │   └── exporter.go / exporter_test.go
        └── mcp/                       # MCP server, tools, auth, ClickHouse client
            ├── server.go / server_test.go
            ├── tools.go / tools_test.go
            ├── auth.go / auth_test.go
            └── clickhouse.go
```

## ClickHouse init and OTEL tables

### `clickhouse/init/01-init.sql`

- Reasserts database **`observability`** (usually already created by `CLICKHOUSE_DB`).
- User **`otel_ingest`** and password come only from **`docker-compose.yml`** / image env.

### `clickhouse/init/02-otel-schema.sql`

Creates **`observability.otel_logs`**, **`observability.otel_traces`**, the trace lookup table **`otel_traces_trace_id_ts`**, and the materialized view **`otel_traces_trace_id_ts_mv`**.

**Metrics** tables (`otel_metrics_gauge`, `otel_metrics_sum`, etc.) are created by the collector with `create_schema: true`.

### If you see `UNKNOWN_TABLE ... otel_logs`

ClickHouse only runs `/docker-entrypoint-initdb.d` on **first** data directory initialization. Either:

- Recreate the volume: `./start.sh down` then `./start.sh up`, **or**
- Apply the schema manually:

  ```bash
  docker compose exec -T clickhouse clickhouse-client --multiquery < clickhouse/init/02-otel-schema.sql
  ```

## Interactive ClickHouse queries

To open an interactive ClickHouse session inside the Docker container:

```bash
docker compose exec clickhouse clickhouse-client --user otel_ingest --password 'CHANGE_ME' --database observability
```

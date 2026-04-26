# Agent Instructions — claude-otel MCP Server

This project provides an MCP server that exposes observability data (token usage, costs,
activity timelines, and developer ROI) from a ClickHouse database populated by Claude Code
and other AI coding tools.

## MCP Server

The server runs at `http://localhost:8081/mcp` (Streamable HTTP transport, MCP spec
rev 2025-03-26). Authentication uses Bearer tokens; the default local development token
is `admin-token`.

Configure the server in your Gemini CLI MCP settings and start the stack before running:

```bash
./start.sh up-mcp
```

## Available Tools (11)

### Admin tools (8) — require `admin` role token

| Tool | Description |
|------|-------------|
| `recent_logs` | Last N logs with body preview |
| `log_counts` | Log volume grouped by service and severity |
| `cost_by_model` | Total cost in USD grouped by model |
| `cost_by_session` | Cost in USD grouped by session |
| `available_metrics` | List available sum metrics and datapoint counts |
| `trace_spans` | Trace spans grouped by service and span name |
| `hook_trace_duration` | Audit-service spans with duration in ms |
| `metric_attributes` | Attribute keys present on a given metric |

### Role-aware tools (3) — accessible by all authenticated users

| Tool | Description |
|------|-------------|
| `report_activity_timeline` | Session activity timeline filtered by developer/repository/group |
| `report_token_usage` | Token consumption and cost by developer/model/repository |
| `report_developer_roi` | ROI analysis: hours saved, cost, net benefit |

## Available Prompts (7)

| Prompt | Scope | Description |
|--------|-------|-------------|
| `daily_agent_standup` | authenticated | 24-hour activity standup |
| `weekly_activity_digest` | authenticated | 7-day activity digest |
| `token_and_cost_week` | authenticated | Token and cost for the current week |
| `token_and_cost_month` | authenticated | Token and cost for the current month |
| `cost_drilldown_repository` | authenticated | Cost breakdown by repository |
| `roi_executive_snapshot` | authenticated | Executive ROI snapshot |
| `compare_developers_cost` | admin only | Cross-developer cost comparison |

## Telemetry

> **TODO:** Native OTEL telemetry export from Gemini CLI is not currently supported.
> A future release of this project will ship a local interceptor binary (built from
> `go-hook-mcp-api/cmd/audit`) that acts as an HTTP proxy between the Gemini CLI
> process and the Gemini API, emitting OTLP events that mirror the hook payload schema
> used by Claude Code. See `docs/client-setup.md` for the roadmap.

For now, tool call activity is captured server-side via the MCP server's own OTLP
instrumentation and is visible in the `report_activity_timeline` tool.

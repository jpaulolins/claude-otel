# MCP Server End-to-End Test Guide

This guide covers how to exercise every tool and prompt exposed by this MCP server from
each supported AI client. Replace example values (`alice@example.com`, `api-gateway`,
`org_example_001`) with values that actually exist in your seeded data.

## Prerequisites

1. Stack running with MCP profile:

   ```bash
   ./start.sh up-mcp
   ```

2. Data seeded:

   ```bash
   ./seed-data.sh
   ```

3. Client configured per [docs/client-setup.md](client-setup.md).

---

## Role matrix

| Role | Tools visible | Tools callable | Prompts visible |
|------|---------------|----------------|-----------------|
| admin | 11 | 11 | 7 |
| viewer | 11 | 3 (role-aware) — admin-only tools return `forbidden` | 6 (no `compare_developers_cost`) |

---

## Tools (11)

### Admin-only tools (8)

| # | Tool | Natural-language query |
|---|------|------------------------|
| 1 | `recent_logs` | "Show the last 25 logs from observability" |
| 2 | `log_counts` | "How many logs per service and severity?" |
| 3 | `cost_by_model` | "What is the total cost in USD grouped by model?" |
| 4 | `cost_by_session` | "Cost per session — filter by `alice@example.com`" |
| 5 | `available_metrics` | "List the available sum metrics and their datapoint counts" |
| 6 | `trace_spans` | "Group trace spans by service and span name" |
| 7 | `hook_trace_duration` | "Show the last 20 audit-service spans with duration in ms" |
| 8 | `metric_attributes` | "What attributes exist on the metric `claude_code.token.usage`?" |

### Role-aware tools (3)

| # | Tool | Natural-language query |
|---|------|------------------------|
| 9 | `report_activity_timeline` | "What happened in the last 24 hours?" • "Timeline for the last 7 days in repository `api-gateway`" • "Agent activity filtered by `group_id=org_example_001`" |
| 10 | `report_token_usage` | "Token consumption and cost for this week" • "Token usage this month for repository `frontend-app`" • "How many tokens has `alice@example.com` used today?" (admin) |
| 11 | `report_developer_roi` | "Developer ROI for the month" • "ROI for `alice@example.com` between 2026-04-01 and 2026-04-19" • "What is the net benefit of the team on repository `data-pipeline`?" |

---

## Prompts (7)

| Prompt | Scope | Arguments | Example |
|--------|-------|-----------|---------|
| `daily_agent_standup` | authenticated | `repository?` | see per-client sections below |
| `weekly_activity_digest` | authenticated | `repository?`, `group_id?` | |
| `token_and_cost_week` | authenticated | `repository?` | |
| `token_and_cost_month` | authenticated | `repository?`, `date_start?`, `date_end?` | |
| `cost_drilldown_repository` | authenticated | `repository` (required), `developer?` | |
| `roi_executive_snapshot` | authenticated | `developer?`, `repository?`, `period?`, `date_start?`, `date_end?` | |
| `compare_developers_cost` | **admin** | `period?`, `date_start?`, `date_end?`, `repository?` | |

---

## Claude Code

**Server label in picker:** the label you set in `.mcp.json` (default: `claude-otel`).

### Invoking tools

Type your query in natural language in the Claude Code prompt. The agent selects the
appropriate tool automatically based on the tool descriptions.

```
What happened in the last 24 hours?
→ calls report_activity_timeline

Show me token usage and cost for this week broken down by model.
→ calls report_token_usage
```

### Invoking prompts

Prompts appear in the slash-command picker as `/mcp__claude-otel__<prompt_name>`.
Open the picker with `/` and start typing the prompt name.

```
/mcp__claude-otel__daily_agent_standup
/mcp__claude-otel__daily_agent_standup repository=api-gateway

/mcp__claude-otel__weekly_activity_digest repository=frontend-app group_id=org_example_001

/mcp__claude-otel__token_and_cost_week
/mcp__claude-otel__token_and_cost_month date_start=2026-04-01 date_end=2026-04-30

/mcp__claude-otel__cost_drilldown_repository repository=data-pipeline
/mcp__claude-otel__roi_executive_snapshot period=month
/mcp__claude-otel__compare_developers_cost period=month   # admin only
```

### Testing the role boundary

Switch to a viewer token and confirm admin tools return `forbidden`:

```bash
export CLAUDE_OTEL_MCP_TOKEN=viewer-token
# Restart Claude Code, then:
# "What is the total cost in USD grouped by model?" → should return forbidden
# "What happened in the last 24 hours?"            → should return only your own data
```

---

## OpenCode

**Server label in picker:** the key name you set in `opencode.json` (default: `claude-otel`).

### Invoking tools

Type your query in natural language in the OpenCode prompt. Tools are invoked
automatically.

```
What happened in the last 24 hours?
Show me token usage and cost for this week.
Developer ROI for the current month.
```

### Invoking prompts

In OpenCode, prompts appear in the slash-command picker. The exact invocation syntax
depends on your OpenCode version; check the OpenCode docs for the prompt picker shortcut.
The prompt names follow the same convention:

```
daily_agent_standup
weekly_activity_digest repository=api-gateway
token_and_cost_month date_start=2026-04-01 date_end=2026-04-30
roi_executive_snapshot period=month
compare_developers_cost period=month   # admin only
```

### Testing the role boundary

Edit `opencode.json` and change the `Authorization` header to `Bearer viewer-token`,
restart OpenCode, then verify:

- Admin-only tools (`cost_by_model`, `recent_logs`, etc.) return `forbidden`.
- `report_activity_timeline` returns data scoped to the viewer's email only.
- `compare_developers_cost` does not appear in the prompt list.

---

## Codex CLI

**MCP config:** `.codex/config.toml` — server registered as `claude-otel`.

### Invoking tools

Type your query in natural language in the Codex CLI prompt.

```
What happened in the last 24 hours?
Show me token usage and cost for this week.
```

### Invoking prompts

Codex CLI prompt invocation depends on how the CLI surfaces MCP prompts in your version.
Refer to the Codex CLI documentation for the current prompt picker shortcut. Use the
same prompt names listed in the table above.

> **Note:** Because Codex does not emit OTEL telemetry natively, activity from your
> Codex session will not appear in the `report_activity_timeline` or `report_token_usage`
> tools. Tool call events are still logged server-side.

### Testing the role boundary

Change `Authorization = "Bearer viewer-token"` in `.codex/config.toml`, restart the CLI,
then verify admin tools return `forbidden`.

---

## Cursor

**MCP config:** `.cursor/mcp.json` — server registered as `claude-otel`.

### Invoking tools

In Cursor's Composer (Agent mode), type your query in natural language. Cursor surfaces
MCP tools automatically when they are enabled in the MCP settings panel.

```
What happened in the last 24 hours?
Show me token usage and cost for this week.
Developer ROI for the current month.
```

### Invoking prompts

Cursor surfaces MCP prompts via the `/` slash-command picker in Composer. Start typing
the prompt name after the slash:

```
/daily_agent_standup
/weekly_activity_digest repository=api-gateway
/token_and_cost_month date_start=2026-04-01 date_end=2026-04-30
/roi_executive_snapshot period=month
```

> **Note:** Cursor does not emit OTEL telemetry to external collectors. Tool call
> activity is still captured server-side via the MCP server.

### Testing the role boundary

Change the `Authorization` value to `Bearer viewer-token` in `.cursor/mcp.json`, restart
Cursor, then verify admin tools return `forbidden`.

---

## Gemini CLI

**MCP config:** configured in Gemini CLI settings (see [docs/client-setup.md](client-setup.md)).

### Invoking tools

Type your query in natural language in the Gemini CLI prompt. Tools are invoked
automatically when the MCP server is connected.

```
What happened in the last 24 hours?
Show me token usage and cost for this week.
```

### Invoking prompts

Gemini CLI prompt invocation depends on your version's MCP prompt support. Refer to the
[Gemini CLI documentation](https://github.com/google-gemini/gemini-cli) for the current
prompt picker interface. Prompt names follow the same convention as the other clients.

> **Note:** Gemini CLI does not emit OTEL telemetry to external collectors. Tool call
> activity is still captured server-side via the MCP server.

### Testing the role boundary

Change the token in your Gemini CLI MCP config to `viewer-token`, restart the CLI, then
verify admin tools return `forbidden`.

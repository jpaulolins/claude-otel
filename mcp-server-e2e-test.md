# MCP Server End-to-End Test Guide

This document lists natural-language queries and slash-command invocations you can use from any MCP-capable client (Claude Code, MCP Inspector, etc.) to exercise every tool and prompt exposed by this observability MCP server.

Replace `<server>` in the examples with the label you assigned to the server in your client configuration (e.g. `claude-otel`). Replace example emails (`alice@example.com`, `bob@example.com`) and repository names (`api-gateway`, `frontend-app`, `data-pipeline`) with values that actually exist in your seeded data.

## How it works

- **Tools** are invoked implicitly. You ask a question in natural language and the agent picks the right tool based on the tool descriptions.
- **Prompts** are invoked explicitly via the slash-command picker as `/mcp__<server>__<prompt_name>`. Arguments are collected by the picker (or can be appended `key=value` after the name).

## Role matrix

| Role   | Tools visible | Tools callable | Prompts visible |
|--------|---------------|----------------|-----------------|
| admin  | 11            | 11             | 7               |
| viewer | 11            | 3 (role-aware) — admin-only tools return `forbidden` | 6 (no `compare_developers_cost`) |

---

## Tools (11)

### Admin-only tools (8)

| # | Tool                  | Natural-language query                                                  |
|---|-----------------------|-------------------------------------------------------------------------|
| 1 | `recent_logs`         | "Show the last 25 logs from observability"                              |
| 2 | `log_counts`          | "How many logs per service and severity?"                               |
| 3 | `cost_by_model`       | "What is the total cost in USD grouped by model?"                       |
| 4 | `cost_by_session`     | "Cost per session — filter by `alice@example.com`"                      |
| 5 | `available_metrics`   | "List the available sum metrics and their datapoint counts"             |
| 6 | `trace_spans`         | "Group trace spans by service and span name"                            |
| 7 | `hook_trace_duration` | "Show the last 20 audit-service spans with duration in ms"              |
| 8 | `metric_attributes`   | "What attributes exist on the metric `claude_code.token.usage`?"        |

### Role-aware tools (3)

| #  | Tool                       | Natural-language query                                                                        |
|----|----------------------------|-----------------------------------------------------------------------------------------------|
| 9  | `report_activity_timeline` | "What happened in the last 24 hours?" • "Timeline for the last 7 days in repository `api-gateway`" • "Agent activity filtered by `group_id=org_example_001`" |
| 10 | `report_token_usage`       | "Token consumption and cost for this week" • "Token usage this month between 2026-04-01 and 2026-04-19 for repository `frontend-app`" • "How many tokens has `alice@example.com` used today?" (admin) |
| 11 | `report_developer_roi`     | "Developer ROI for the month" • "ROI for `alice@example.com` between 2026-04-01 and 2026-04-19" • "What is the net benefit of the team on repository `data-pipeline`?" |

---

## Prompts (7)

### Invocation mechanics

In Claude Code (and similar MCP clients) prompts appear in the slash-command picker as:

```
/mcp__<server>__<prompt_name>
```

Open the picker (usually `/` or a dedicated shortcut like `Ctrl-K` / `Cmd-K`) and start typing the prompt name. Optional arguments can be provided interactively or appended inline as `key=value` pairs.

### Prompt reference

| Prompt                      | Scope     | Arguments                                              | Example invocation                                                                                                   |
|-----------------------------|-----------|--------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------|
| `daily_agent_standup`       | auth      | `repository?`                                          | `/mcp__<server>__daily_agent_standup` &nbsp;•&nbsp; `/mcp__<server>__daily_agent_standup repository=api-gateway`     |
| `weekly_activity_digest`    | auth      | `repository?`, `group_id?`                             | `/mcp__<server>__weekly_activity_digest repository=frontend-app group_id=org_example_001`                            |
| `token_and_cost_week`       | auth      | `repository?`                                          | `/mcp__<server>__token_and_cost_week`                                                                                |
| `token_and_cost_month`      | auth      | `repository?`, `date_start?`, `date_end?`              | `/mcp__<server>__token_and_cost_month date_start=2026-04-01 date_end=2026-04-30`                                     |
| `cost_drilldown_repository` | auth      | `repository` (required), `developer?`                  | `/mcp__<server>__cost_drilldown_repository repository=data-pipeline`                                                 |
| `roi_executive_snapshot`    | auth      | `developer?`, `repository?`, `period?`, `date_start?`, `date_end?` | `/mcp__<server>__roi_executive_snapshot period=month` &nbsp;•&nbsp; `/mcp__<server>__roi_executive_snapshot developer=alice@example.com date_start=2026-03-01 date_end=2026-03-31` |
| `compare_developers_cost`   | **admin** | `period?`, `date_start?`, `date_end?`, `repository?`   | `/mcp__<server>__compare_developers_cost period=month`                                                               |

### What to expect when a prompt runs

Each prompt returns a single `user`-role message with English instructions. The agent then:

1. Calls the underlying tool(s) with the arguments rendered by the prompt.
2. Formats the response in the structure requested (sections, tables, or narrative synthesis depending on the prompt).

Example — `daily_agent_standup` handed to the agent:

> Call `report_activity_timeline(since="24h", source="agent", developer="<your email>")` and produce an English standup with three sections: **Done**, **In Progress**, **Attention**. For each entity include session id, repository, tools used, and event count. Flag any session with errors in the *Attention* section.

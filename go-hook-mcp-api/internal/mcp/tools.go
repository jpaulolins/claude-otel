package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var allowedMetricNames = map[string]bool{
	"claude_code.token.usage":       true,
	"claude_code.cost.usage":        true,
	"claude_code.active_time.total": true,
	"claude_code.session.count":     true,
}

// Args is the generic JSON-decoded tool input. We use map[string]any across
// every tool to keep the registration boilerplate minimal; per-tool schemas
// are declared explicitly via InputSchema.
type Args = map[string]any

// emptySchema is reused for tools that take no arguments.
func emptySchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
}

func intSchema(prop, desc string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			prop: {Type: "number", Description: desc},
		},
	}
}

func stringSchema(prop, desc string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			prop: {Type: "string", Description: desc},
		},
	}
}

// RegisterTools registers all MCP tools with role-based access control.
// Admin tools: full access to all data and queries.
// Viewer tools: only access to the authenticated user's own data.
func RegisterTools(s *sdk.Server, q Querier) {
	registerAdminTools(s, q)
	registerViewerTools(s, q)
}

// --- RBAC helpers ---

func requireAdmin(ctx context.Context) error {
	u, ok := UserFromContext(ctx)
	if !ok {
		return ErrUnauthorized
	}
	if !u.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

func requireAuth(ctx context.Context) (User, error) {
	u, ok := UserFromContext(ctx)
	if !ok {
		return User{}, ErrUnauthorized
	}
	return u, nil
}

// adminToolFn wraps a handler with the admin role check.
func adminToolFn(fn func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error)) func(context.Context, *sdk.CallToolRequest, Args) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, _ *sdk.CallToolRequest, args Args) (*sdk.CallToolResult, any, error) {
		if err := requireAdmin(ctx); err != nil {
			return errorResult(err.Error()), nil, nil
		}
		return fn(ctx, args)
	}
}

// authToolFn wraps a handler that requires any authenticated user; the User is
// passed to the inner handler.
func authToolFn(fn func(ctx context.Context, user User, args Args) (*sdk.CallToolResult, any, error)) func(context.Context, *sdk.CallToolRequest, Args) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, _ *sdk.CallToolRequest, args Args) (*sdk.CallToolResult, any, error) {
		u, err := requireAuth(ctx)
		if err != nil {
			return errorResult(err.Error()), nil, nil
		}
		return fn(ctx, u, args)
	}
}

// runQueryStatic is the simplest admin handler: run a fixed SQL query.
func runQueryStatic(q Querier, query string) func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, _ Args) (*sdk.CallToolResult, any, error) {
		return execQuery(ctx, q, query)
	}
}

// --- Admin tools (15 original tools, admin-only) ---

func registerAdminTools(s *sdk.Server, q Querier) {
	// 1. list_tables
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "list_tables",
			Description: "[admin] List all OTEL tables in the observability database",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, "SHOW TABLES FROM observability")),
	)

	// 2. recent_logs
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "recent_logs",
			Description: "[admin] Get recent logs with timestamp, severity, service and body preview",
			InputSchema: intSchema("limit", "Number of logs to return (default 10)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			limit := clampInt(getInt(args, "limit", 10), 1, 10000)
			query := fmt.Sprintf(`SELECT Timestamp, SeverityText, ServiceName, substring(Body, 1, 120) AS body FROM observability.otel_logs ORDER BY Timestamp DESC LIMIT %d`, limit)
			return execQuery(ctx, q, query)
		}),
	)

	// 3. log_counts
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "log_counts",
			Description: "[admin] Count logs grouped by service and severity",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT ServiceName, SeverityText, count() AS n FROM observability.otel_logs GROUP BY ServiceName, SeverityText ORDER BY n DESC`)),
	)

	// 4. claude_events_by_type
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "claude_events_by_type",
			Description: "[admin] Claude Code events grouped by type",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT Body, count() AS n FROM observability.otel_logs WHERE ServiceName = 'claude-code' GROUP BY Body ORDER BY n DESC`)),
	)

	// 5. hook_events
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "hook_events",
			Description: "[admin] Hook events by hook type and tool name",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT JSONExtractString(Body, 'hook_event_name') AS hook, JSONExtractString(Body, 'tool_name') AS tool, count() AS n FROM observability.otel_logs WHERE ServiceName = 'claude-audit-service' GROUP BY hook, tool ORDER BY n DESC`)),
	)

	// 6. token_usage_detailed
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "token_usage_detailed",
			Description: "[admin] Token consumption by user, model and token type (all users)",
			InputSchema: stringSchema("user_email", "Filter by user email (optional)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			query := `SELECT Attributes['user.email'] AS user_email, Attributes['user.id'] AS user_id, Attributes['organization.id'] AS org_id, Attributes['model'] AS model, Attributes['type'] AS token_type, sum(Value) AS total_tokens FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.token.usage'`
			query += optionalEmailFilter(args)
			query += ` GROUP BY user_email, user_id, org_id, model, token_type ORDER BY user_email, total_tokens DESC`
			return execQuery(ctx, q, query)
		}),
	)

	// 7. token_usage_summary
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "token_usage_summary",
			Description: "[admin] Pivoted token consumption summary (all users)",
			InputSchema: stringSchema("user_email", "Filter by user email (optional)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			query := `SELECT Attributes['user.email'] AS user_email, Attributes['model'] AS model, sum(Value) AS total_tokens, round(sumIf(Value, Attributes['type'] = 'input')) AS input, round(sumIf(Value, Attributes['type'] = 'output')) AS output, round(sumIf(Value, Attributes['type'] = 'cacheRead')) AS cache_read, round(sumIf(Value, Attributes['type'] = 'cacheCreation')) AS cache_creation FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.token.usage'`
			query += optionalEmailFilter(args)
			query += ` GROUP BY user_email, model ORDER BY total_tokens DESC`
			return execQuery(ctx, q, query)
		}),
	)

	// 8. cost_by_model
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "cost_by_model",
			Description: "[admin] Cost in USD grouped by model",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT Attributes['model'] AS model, round(sum(Value), 6) AS total_cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage' GROUP BY model`)),
	)

	// 9. cost_by_user
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "cost_by_user",
			Description: "[admin] Cost in USD grouped by user email",
			InputSchema: stringSchema("user_email", "Filter by user email (optional)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			query := `SELECT Attributes['user.email'] AS user_email, Attributes['model'] AS model, round(sum(Value), 4) AS cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage'`
			query += optionalEmailFilter(args)
			query += ` GROUP BY user_email, model ORDER BY cost_usd DESC`
			return execQuery(ctx, q, query)
		}),
	)

	// 10. cost_by_session
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "cost_by_session",
			Description: "[admin] Cost in USD grouped by session ID",
			InputSchema: stringSchema("user_email", "Filter by user email (optional)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			query := `SELECT Attributes['session.id'] AS session, Attributes['user.email'] AS user_email, round(sum(Value), 4) AS cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage'`
			query += optionalEmailFilter(args)
			query += ` GROUP BY session, user_email ORDER BY cost_usd DESC`
			return execQuery(ctx, q, query)
		}),
	)

	// 11. recent_token_usage
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "recent_token_usage",
			Description: "[admin] Token consumption in the last N minutes (all users)",
			InputSchema: intSchema("minutes", "Time window in minutes (default 10)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			mins := clampInt(getInt(args, "minutes", 10), 1, 10000)
			query := fmt.Sprintf(`SELECT Attributes['user.email'] AS user_email, Attributes['model'] AS model, Attributes['type'] AS token_type, sum(Value) AS tokens FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.token.usage' AND TimeUnix > now() - INTERVAL %d MINUTE GROUP BY user_email, model, token_type ORDER BY tokens DESC`, mins)
			return execQuery(ctx, q, query)
		}),
	)

	// 12. available_metrics
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "available_metrics",
			Description: "[admin] List all available sum metrics with datapoint counts",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT MetricName, count() AS n FROM observability.otel_metrics_sum GROUP BY MetricName ORDER BY n DESC`)),
	)

	// 13. trace_spans
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "trace_spans",
			Description: "[admin] Trace spans grouped by service and span name",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT ServiceName, SpanName, count() AS n FROM observability.otel_traces GROUP BY ServiceName, SpanName ORDER BY n DESC`)),
	)

	// 14. hook_trace_duration
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "hook_trace_duration",
			Description: "[admin] Recent audit-service hook spans with duration in milliseconds",
			InputSchema: intSchema("limit", "Number of spans to return (default 10)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			limit := clampInt(getInt(args, "limit", 10), 1, 10000)
			query := fmt.Sprintf(`SELECT Timestamp, SpanName, round(Duration / 1e6, 2) AS duration_ms, StatusCode FROM observability.otel_traces WHERE ServiceName = 'claude-audit-service' ORDER BY Timestamp DESC LIMIT %d`, limit)
			return execQuery(ctx, q, query)
		}),
	)

	// 15. metric_attributes
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "metric_attributes",
			Description: "[admin] Discover available attributes for a given metric name",
			InputSchema: stringSchema("metric_name", "Metric name (default: claude_code.token.usage)"),
		},
		adminToolFn(func(ctx context.Context, args Args) (*sdk.CallToolResult, any, error) {
			metricName := getString(args, "metric_name", "claude_code.token.usage")
			if !allowedMetricNames[metricName] {
				return errorResult(fmt.Sprintf("metric %q not in allowlist: %v", metricName, allowedMetricNamesList())), nil, nil
			}
			safe := strings.ReplaceAll(metricName, "'", "''")
			q1 := fmt.Sprintf(`SELECT DISTINCT arrayJoin(mapKeys(Attributes)) AS k FROM observability.otel_metrics_sum WHERE MetricName = '%s'`, safe)
			q2 := fmt.Sprintf(`SELECT DISTINCT arrayJoin(mapKeys(ResourceAttributes)) AS k FROM observability.otel_metrics_sum WHERE MetricName = '%s'`, safe)

			r1, err := q.Query(ctx, q1)
			if err != nil {
				return errorResult(err.Error()), nil, nil
			}
			r2, err := q.Query(ctx, q2)
			if err != nil {
				return errorResult(err.Error()), nil, nil
			}
			result := map[string]any{
				"metric":              metricName,
				"attributes":          extractColumn(r1, "k"),
				"resource_attributes": extractColumn(r2, "k"),
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			return textResult(string(b)), nil, nil
		}),
	)
}

// --- Viewer tools (personal data only, filtered by authenticated user email) ---

func registerViewerTools(s *sdk.Server, q Querier) {
	// my_token_usage
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "my_token_usage",
			Description: "Your personal token consumption summary by model",
			InputSchema: emptySchema(),
		},
		authToolFn(func(ctx context.Context, u User, _ Args) (*sdk.CallToolResult, any, error) {
			email := escapeSQL(u.Email)
			query := fmt.Sprintf(`SELECT Attributes['model'] AS model, sum(Value) AS total_tokens, round(sumIf(Value, Attributes['type'] = 'input')) AS input, round(sumIf(Value, Attributes['type'] = 'output')) AS output, round(sumIf(Value, Attributes['type'] = 'cacheRead')) AS cache_read, round(sumIf(Value, Attributes['type'] = 'cacheCreation')) AS cache_creation FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.token.usage' AND Attributes['user.email'] = '%s' GROUP BY model ORDER BY total_tokens DESC`, email)
			return execQuery(ctx, q, query)
		}),
	)

	// my_tokens_daily
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "my_tokens_daily",
			Description: "Your daily token consumption (last 30 days)",
			InputSchema: intSchema("days", "Number of days to look back (default 30)"),
		},
		authToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			days := clampInt(getInt(args, "days", 30), 1, 365)
			email := escapeSQL(u.Email)
			query := fmt.Sprintf(`SELECT toDate(TimeUnix) AS day, Attributes['model'] AS model, sum(Value) AS tokens, round(sumIf(Value, Attributes['type'] = 'input')) AS input, round(sumIf(Value, Attributes['type'] = 'output')) AS output, round(sumIf(Value, Attributes['type'] = 'cacheRead')) AS cache_read, round(sumIf(Value, Attributes['type'] = 'cacheCreation')) AS cache_creation FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.token.usage' AND Attributes['user.email'] = '%s' AND TimeUnix > now() - INTERVAL %d DAY GROUP BY day, model ORDER BY day DESC, tokens DESC`, email, days)
			return execQuery(ctx, q, query)
		}),
	)

	// my_cost
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "my_cost",
			Description: "Your personal cost in USD by model",
			InputSchema: emptySchema(),
		},
		authToolFn(func(ctx context.Context, u User, _ Args) (*sdk.CallToolResult, any, error) {
			email := escapeSQL(u.Email)
			query := fmt.Sprintf(`SELECT Attributes['model'] AS model, round(sum(Value), 4) AS cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage' AND Attributes['user.email'] = '%s' GROUP BY model ORDER BY cost_usd DESC`, email)
			return execQuery(ctx, q, query)
		}),
	)

	// my_sessions
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "my_sessions",
			Description: "Your sessions with cost and token totals",
			InputSchema: intSchema("limit", "Number of sessions to return (default 20)"),
		},
		authToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			limit := clampInt(getInt(args, "limit", 20), 1, 1000)
			email := escapeSQL(u.Email)
			query := fmt.Sprintf(`SELECT Attributes['session.id'] AS session, round(sum(Value), 4) AS cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage' AND Attributes['user.email'] = '%s' GROUP BY session ORDER BY cost_usd DESC LIMIT %d`, email, limit)
			return execQuery(ctx, q, query)
		}),
	)

	// my_commands_daily
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "my_commands_daily",
			Description: "Your daily command count from hook events (last 30 days)",
			InputSchema: intSchema("days", "Number of days to look back (default 30)"),
		},
		authToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			days := clampInt(getInt(args, "days", 30), 1, 365)
			email := escapeSQL(u.Email)
			query := fmt.Sprintf(`SELECT toDate(Timestamp) AS day, JSONExtractString(Body, 'hook_event_name') AS hook, JSONExtractString(Body, 'tool_name') AS tool, count() AS n FROM observability.otel_logs WHERE ServiceName = 'claude-audit-service' AND JSONExtractString(Body, 'user_id') = '%s' AND Timestamp > now() - INTERVAL %d DAY GROUP BY day, hook, tool ORDER BY day DESC, n DESC`, email, days)
			return execQuery(ctx, q, query)
		}),
	)

	// my_report
	sdk.AddTool(s,
		&sdk.Tool{
			Name:        "my_report",
			Description: "Comprehensive personal report: tokens, cost, sessions, and commands for today, this week, or this month",
			InputSchema: stringSchema("period", "Period: 'day', 'week', or 'month' (default 'day')"),
		},
		authToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			period := getString(args, "period", "day")
			interval := periodToInterval(period)
			email := escapeSQL(u.Email)

			qTokens := fmt.Sprintf(`SELECT Attributes['model'] AS model, sum(Value) AS total_tokens, round(sumIf(Value, Attributes['type'] = 'input')) AS input, round(sumIf(Value, Attributes['type'] = 'output')) AS output, round(sumIf(Value, Attributes['type'] = 'cacheRead')) AS cache_read, round(sumIf(Value, Attributes['type'] = 'cacheCreation')) AS cache_creation FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.token.usage' AND Attributes['user.email'] = '%s' AND TimeUnix > %s GROUP BY model ORDER BY total_tokens DESC`, email, interval)
			qCost := fmt.Sprintf(`SELECT Attributes['model'] AS model, round(sum(Value), 4) AS cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage' AND Attributes['user.email'] = '%s' AND TimeUnix > %s GROUP BY model ORDER BY cost_usd DESC`, email, interval)
			qSessions := fmt.Sprintf(`SELECT Attributes['session.id'] AS session, round(sum(Value), 4) AS cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage' AND Attributes['user.email'] = '%s' AND TimeUnix > %s GROUP BY session ORDER BY cost_usd DESC LIMIT 10`, email, interval)
			qCmds := fmt.Sprintf(`SELECT JSONExtractString(Body, 'hook_event_name') AS hook, JSONExtractString(Body, 'tool_name') AS tool, count() AS n FROM observability.otel_logs WHERE ServiceName = 'claude-audit-service' AND JSONExtractString(Body, 'user_id') = '%s' AND Timestamp > %s GROUP BY hook, tool ORDER BY n DESC`, email, interval)

			tokens, _ := q.Query(ctx, qTokens)
			cost, _ := q.Query(ctx, qCost)
			sessions, _ := q.Query(ctx, qSessions)
			cmds, _ := q.Query(ctx, qCmds)

			report := map[string]any{
				"user":     u.Email,
				"period":   period,
				"tokens":   tokens,
				"cost":     cost,
				"sessions": sessions,
				"commands": cmds,
			}
			b, _ := json.MarshalIndent(report, "", "  ")
			return textResult(string(b)), nil, nil
		}),
	)
}

// --- helpers ---

func execQuery(ctx context.Context, q Querier, query string) (*sdk.CallToolResult, any, error) {
	rows, err := q.Query(ctx, query)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return textResult(string(b)), nil, nil
}

func textResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: text}},
	}
}

func errorResult(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: msg}},
	}
}

func optionalEmailFilter(args Args) string {
	email := getString(args, "user_email", "")
	if email == "" {
		return ""
	}
	return fmt.Sprintf(` AND Attributes['user.email'] = '%s'`, escapeSQL(email))
}

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func periodToInterval(period string) string {
	switch period {
	case "week":
		return "toStartOfWeek(now())"
	case "month":
		return "toStartOfMonth(now())"
	default:
		return "toStartOfDay(now())"
	}
}

// getInt extracts an int from args. JSON unmarshals numbers as float64, so we
// also accept that and ints (in case the args were synthesized from Go).
func getInt(args Args, key string, def int) int {
	if args == nil {
		return def
	}
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

func getString(args Args, key, def string) string {
	if args == nil {
		return def
	}
	v, ok := args[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func allowedMetricNamesList() []string {
	names := make([]string, 0, len(allowedMetricNames))
	for k := range allowedMetricNames {
		names = append(names, k)
	}
	return names
}

func extractColumn(rows []map[string]any, col string) []string {
	var vals []string
	for _, r := range rows {
		if v, ok := r[col]; ok {
			vals = append(vals, fmt.Sprint(v))
		}
	}
	return vals
}

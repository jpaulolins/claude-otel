package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// roiDefaults — defaults for ROI analysis constants.
const (
	roiDefaultProductivityFactor = 3.5
	roiDefaultHourlyRateUSD      = 50.0
)

// roiConstants returns the productivity factor and hourly rate used by the
// report_developer_roi tool. Values are read from the environment on every
// invocation so tests can override them via t.Setenv. Invalid values fall back
// to the defaults and emit a warning to stderr.
//
// ROI formula (period_days basis: "elapsed_days_inclusive" — e.g. Apr 1 → Apr 1
// is 1 day, Apr 1 → Apr 30 is 30 days):
//
//	hours_active        = sum(claude_code.active_time.total) / 3600
//	hours_equivalent    = hours_active * ROI_PRODUCTIVITY_FACTOR
//	value_delivered_usd = hours_equivalent * ROI_HOURLY_RATE_USD
//	total_cost_usd      = sum(claude_code.cost.usage)
//	net_benefit_usd     = value_delivered_usd - total_cost_usd
//	roi_ratio           = value_delivered_usd / total_cost_usd   (null if cost==0)
//	daily_average_usd   = total_cost_usd / period_days           (elapsed-days basis)
func roiConstants() (productivityFactor, hourlyRateUSD float64) {
	productivityFactor = parseEnvFloat("ROI_PRODUCTIVITY_FACTOR", roiDefaultProductivityFactor)
	hourlyRateUSD = parseEnvFloat("ROI_HOURLY_RATE_USD", roiDefaultHourlyRateUSD)
	return
}

func parseEnvFloat(name string, def float64) float64 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid %s=%q, using default %v: %v\n", name, raw, def, err)
		return def
	}
	return v
}

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

// intSchema builds a single-number-property schema.
//
// Deprecated: prefer multiFieldSchema even for single-field tools for
// uniformity. Kept as a thin convenience wrapper.
func intSchema(prop, desc string) *jsonschema.Schema {
	return multiFieldSchema(field{Name: prop, Type: "number", Description: desc})
}

// stringSchema builds a single-string-property schema.
//
// Deprecated: prefer multiFieldSchema even for single-field tools for
// uniformity. Kept as a thin convenience wrapper.
func stringSchema(prop, desc string) *jsonschema.Schema {
	return multiFieldSchema(field{Name: prop, Type: "string", Description: desc})
}

// field describes one property of a multi-field input schema.
type field struct {
	Name        string
	Type        string // "string" | "number"
	Description string
}

// multiFieldSchema builds an object schema with multiple typed properties.
func multiFieldSchema(fields ...field) *jsonschema.Schema {
	props := make(map[string]*jsonschema.Schema, len(fields))
	for _, f := range fields {
		props[f.Name] = &jsonschema.Schema{Type: f.Type, Description: f.Description}
	}
	return &jsonschema.Schema{Type: "object", Properties: props}
}

// toolSchemas exposes the registered InputSchema per tool name. Populated by
// RegisterTools; consumed by prompts_test.go to cross-check that every prompt
// renders tool calls that only use arguments the tool actually accepts.
var toolSchemas = map[string]*jsonschema.Schema{}

// RegisterTools registers all MCP tools with role-based access control.
func RegisterTools(s *sdk.Server, q Querier) {
	// Reset the schema registry so test re-registration does not pile up.
	toolSchemas = map[string]*jsonschema.Schema{}
	registerAdminTools(s, q)
	registerReportTools(s, q)
}

// addTool is a thin AddTool wrapper that also records the InputSchema in
// toolSchemas for cross-check tests.
func addTool(s *sdk.Server, t *sdk.Tool, h func(context.Context, *sdk.CallToolRequest, Args) (*sdk.CallToolResult, any, error)) {
	if sc, ok := t.InputSchema.(*jsonschema.Schema); ok {
		toolSchemas[t.Name] = sc
	}
	sdk.AddTool(s, t, h)
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

// roleAwareToolFn wraps a handler that requires any authenticated user; the
// User is passed to the handler so it can scope results appropriately. Both
// admin and viewer may call the tool; the handler decides what they see.
func roleAwareToolFn(fn func(ctx context.Context, user User, args Args) (*sdk.CallToolResult, any, error)) func(context.Context, *sdk.CallToolRequest, Args) (*sdk.CallToolResult, any, error) {
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

// --- Admin tools (8, admin-only) ---

func registerAdminTools(s *sdk.Server, q Querier) {
	// 1. recent_logs
	addTool(s,
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

	// 2. log_counts
	addTool(s,
		&sdk.Tool{
			Name:        "log_counts",
			Description: "[admin] Count logs grouped by service and severity",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT ServiceName, SeverityText, count() AS n FROM observability.otel_logs GROUP BY ServiceName, SeverityText ORDER BY n DESC`)),
	)

	// 3. cost_by_model
	addTool(s,
		&sdk.Tool{
			Name:        "cost_by_model",
			Description: "[admin] Cost in USD grouped by model",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT Attributes['model'] AS model, round(sum(Value), 6) AS total_cost_usd FROM observability.otel_metrics_sum WHERE MetricName = 'claude_code.cost.usage' GROUP BY model`)),
	)

	// 4. cost_by_session
	addTool(s,
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

	// 5. available_metrics
	addTool(s,
		&sdk.Tool{
			Name:        "available_metrics",
			Description: "[admin] List all available sum metrics with datapoint counts",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT MetricName, count() AS n FROM observability.otel_metrics_sum GROUP BY MetricName ORDER BY n DESC`)),
	)

	// 6. trace_spans
	addTool(s,
		&sdk.Tool{
			Name:        "trace_spans",
			Description: "[admin] Trace spans grouped by service and span name",
			InputSchema: emptySchema(),
		},
		adminToolFn(runQueryStatic(q, `SELECT ServiceName, SpanName, count() AS n FROM observability.otel_traces GROUP BY ServiceName, SpanName ORDER BY n DESC`)),
	)

	// 7. hook_trace_duration
	addTool(s,
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

	// 8. metric_attributes
	addTool(s,
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
			safe := escapeSQL(metricName)
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

// --- Report tools (3, role-aware) ---

func registerReportTools(s *sdk.Server, q Querier) {
	// report_activity_timeline — period basis: relative window via `since`; UTC.
	addTool(s,
		&sdk.Tool{
			Name:        "report_activity_timeline",
			Description: "Activity timeline grouped by session. Role-aware: viewer sees only own sessions. Timezone: UTC.",
			InputSchema: multiFieldSchema(
				field{Name: "since", Type: "string", Description: "ISO-8601 or relative (Nh|Nd|Nw). Default 24h"},
				field{Name: "source", Type: "string", Description: "agent|github|linear|all. Default all"},
				field{Name: "repository", Type: "string", Description: "Filter by repository basename. Default all"},
				field{Name: "group_id", Type: "string", Description: "Filter by organization.id. Default all"},
				field{Name: "developer", Type: "string", Description: "Filter by user.email (admin only; viewer is always scoped to self). Default all"},
				field{Name: "max_items", Type: "number", Description: "Clamp 1..10000. Default 100"},
			),
		},
		roleAwareToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			source := getString(args, "source", "all")
			switch source {
			case "github", "linear":
				return errorResult("source not yet ingested"), nil, nil
			case "agent", "all":
				// ok
			default:
				return errorResult(fmt.Sprintf("invalid source %q", source)), nil, nil
			}

			sinceStr := getString(args, "since", "24h")
			sinceExpr, err := parseSince(sinceStr)
			if err != nil {
				return errorResult(err.Error()), nil, nil
			}

			// CQ-L1: single branch. If caller supplied max_items, coerce +
			// clamp it into [1, 10000]. If absent, default to 100.
			var maxItems int
			if raw, ok := args["max_items"]; ok {
				switch n := raw.(type) {
				case float64:
					maxItems = int(n)
				case int:
					maxItems = n
				case int64:
					maxItems = int(n)
				default:
					maxItems = 1
				}
				maxItems = clampInt(maxItems, 1, 10000)
			} else {
				maxItems = 100
			}
			repository := getString(args, "repository", "all")
			groupID := getString(args, "group_id", "")
			developerArg := roleScopedEmail(ctx, args, "developer")
			// forcedFilter is true for viewers (ctx email always wins).
			forcedFilter := !u.IsAdmin()

			where := []string{
				"ServiceName = 'claude-audit-service'",
				fmt.Sprintf("Timestamp > %s", sinceExpr),
			}
			if repository != "" && repository != "all" {
				where = append(where, fmt.Sprintf("JSONExtractString(Body, 'repository') = '%s'", escapeSQL(repository)))
			}
			if groupID != "" && groupID != "all" {
				where = append(where, fmt.Sprintf("JSONExtractString(Body, 'organization_id') = '%s'", escapeSQL(groupID)))
			}
			// BL-H4: match on both Body.user_id and Attributes['user.email']
			// (lower-cased) for defense-in-depth.
			if developerArg != "" && developerArg != "all" {
				lowered := strings.ToLower(developerArg)
				where = append(where, fmt.Sprintf(
					"(lower(JSONExtractString(Body, 'user_id')) = '%s' OR lower(Attributes['user.email']) = '%s')",
					escapeSQL(lowered), escapeSQL(lowered),
				))
			}

			query := fmt.Sprintf(
				`SELECT 'session' AS type, JSONExtractString(Body, 'session_id') AS id, JSONExtractString(Body, 'repository') AS repository, min(Timestamp) AS first_seen, max(Timestamp) AS last_seen, groupUniqArray(JSONExtractString(Body, 'user_id')) AS actors, count() AS event_count, concat('tools: ', arrayStringConcat(groupUniqArray(JSONExtractString(Body, 'tool_name')), ',')) AS summary FROM observability.otel_logs WHERE %s GROUP BY id, repository ORDER BY last_seen DESC LIMIT %d`,
				strings.Join(where, " AND "), maxItems,
			)
			rows, err := q.Query(ctx, query)
			if err != nil {
				return errorResult(err.Error()), nil, nil
			}
			if rows == nil {
				rows = []map[string]any{}
			}

			envelope := map[string]any{
				"timezone": "UTC",
				"filter": map[string]any{
					"developer": developerArgOrAll(developerArg),
					"forced":    forcedFilter,
				},
				"rows": rows,
			}
			b, _ := json.MarshalIndent(envelope, "", "  ")
			return textResult(string(b)), nil, nil
		}),
	)

	// report_token_usage
	addTool(s,
		&sdk.Tool{
			Name:        "report_token_usage",
			Description: "Token and cost usage by developer/model. Role-aware: viewer sees only own data. Timezone: UTC.",
			InputSchema: multiFieldSchema(
				field{Name: "period", Type: "string", Description: "today|week|month. Default week. Ignored when date_start+date_end set"},
				field{Name: "developer", Type: "string", Description: "Filter by user.email. Default all (admin). Viewer is forced to self"},
				field{Name: "date_start", Type: "string", Description: "YYYY-MM-DD (inclusive). Requires date_end"},
				field{Name: "date_end", Type: "string", Description: "YYYY-MM-DD (inclusive). Requires date_start"},
				field{Name: "repository", Type: "string", Description: "Filter by repository basename. Default all"},
			),
		},
		roleAwareToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			period := getString(args, "period", "week")
			startExpr, endExpr, err := parseDateRange(args, period)
			if err != nil {
				return errorResult(err.Error()), nil, nil
			}

			developer := roleScopedEmail(ctx, args, "developer")
			repository := getString(args, "repository", "all")
			forcedFilter := !u.IsAdmin()

			where := []string{
				"MetricName IN ('claude_code.token.usage','claude_code.cost.usage')",
				fmt.Sprintf("TimeUnix >= %s AND TimeUnix < %s", startExpr, endExpr),
			}
			if developer != "" && developer != "all" {
				// BL-NEW-2 / Sec-NEW-1: case-insensitive match on user.email
				// — normalize both sides with lower() so mixed-case admin
				// input maps to the same row.
				where = append(where, fmt.Sprintf(
					"lower(Attributes['user.email']) = '%s'",
					escapeSQL(strings.ToLower(developer)),
				))
			}
			if repository != "" && repository != "all" {
				where = append(where, fmt.Sprintf("Attributes['repository'] = '%s'", escapeSQL(repository)))
			}

			query := fmt.Sprintf(
				`SELECT Attributes['user.email'] AS developer, Attributes['model'] AS model, round(sumIf(Value, MetricName='claude_code.token.usage' AND Attributes['type']='input'), 0) AS input_tokens, round(sumIf(Value, MetricName='claude_code.token.usage' AND Attributes['type']='output'), 0) AS output_tokens, round(sumIf(Value, MetricName='claude_code.token.usage' AND Attributes['type']='cacheRead'), 0) AS cache_read, round(sumIf(Value, MetricName='claude_code.token.usage' AND Attributes['type']='cacheCreation'), 0) AS cache_creation, round(sumIf(Value, MetricName='claude_code.cost.usage'), 6) AS cost_usd FROM observability.otel_metrics_sum WHERE %s GROUP BY developer, model ORDER BY cost_usd DESC`,
				strings.Join(where, " AND "),
			)
			rows, err := q.Query(ctx, query)
			if err != nil {
				return errorResult(err.Error()), nil, nil
			}
			if rows == nil {
				rows = []map[string]any{}
			}

			envelope := map[string]any{
				"timezone": "UTC",
				"filter": map[string]any{
					"developer": developerArgOrAll(developer),
					"forced":    forcedFilter,
				},
				"rows": rows,
			}
			b, _ := json.MarshalIndent(envelope, "", "  ")
			return textResult(string(b)), nil, nil
		}),
	)

	// report_developer_roi
	addTool(s,
		&sdk.Tool{
			Name:        "report_developer_roi",
			Description: "ROI analysis per developer: total cost, daily average (basis: elapsed_days_inclusive), trend, and breakdowns by model and repository. Timezone: UTC.",
			InputSchema: multiFieldSchema(
				field{Name: "developer", Type: "string", Description: "Filter by user.email. Default all (admin). Viewer is forced to self"},
				field{Name: "period", Type: "string", Description: "today|week|month. Default month. Ignored when date_start+date_end set"},
				field{Name: "date_start", Type: "string", Description: "YYYY-MM-DD (inclusive). Requires date_end"},
				field{Name: "date_end", Type: "string", Description: "YYYY-MM-DD (inclusive). Requires date_start"},
				field{Name: "repository", Type: "string", Description: "Filter by repository basename. Default all"},
			),
		},
		roleAwareToolFn(func(ctx context.Context, u User, args Args) (*sdk.CallToolResult, any, error) {
			return runDeveloperROI(ctx, q, u, args)
		}),
	)
}

// developerArgOrAll normalizes a developer filter for the response envelope.
// Empty or explicit "all" are returned as "all"; everything else is echoed.
func developerArgOrAll(s string) string {
	if s == "" || s == "all" {
		return "all"
	}
	return s
}

// runDeveloperROI executes the ROI report. It runs up to four sub-queries per
// matched developer: cost+tokens aggregate from otel_metrics_sum, active time
// from otel_metrics_gauge, daily trend, by-model and by-repository breakdowns.
// Errors from individual sub-queries are surfaced per developer without
// aborting the whole response, and also collected at top level via `warnings`.
func runDeveloperROI(ctx context.Context, q Querier, u User, args Args) (*sdk.CallToolResult, any, error) {
	period := getString(args, "period", "month")
	startExpr, endExpr, err := parseDateRange(args, period)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}

	developer := roleScopedEmail(ctx, args, "developer")
	repository := getString(args, "repository", "all")
	forcedFilter := !u.IsAdmin()

	productivityFactor, hourlyRateUSD := roiConstants()

	// Base WHERE clauses applied to both sum + gauge queries. TimeUnix is the
	// canonical timestamp column on both tables. The window is half-open:
	// start <= TimeUnix < end_exclusive (BL-M4).
	//
	// CQ-L2: baseWhereNoDev carries every filter EXCEPT the per-developer
	// one, so buildROIEntry can re-apply a single developer filter without
	// duplicating the clause. baseWhere is the fully-scoped form used by the
	// top-level aggregate queries.
	baseWhereNoDev := []string{
		fmt.Sprintf("TimeUnix >= %s AND TimeUnix < %s", startExpr, endExpr),
	}
	if repository != "" && repository != "all" {
		baseWhereNoDev = append(baseWhereNoDev, fmt.Sprintf("Attributes['repository'] = '%s'", escapeSQL(repository)))
	}
	baseWhere := append([]string{}, baseWhereNoDev...)
	if developer != "" && developer != "all" {
		// BL-NEW-2 / Sec-NEW-1: case-insensitive equality on user.email.
		baseWhere = append(baseWhere, fmt.Sprintf(
			"lower(Attributes['user.email']) = '%s'",
			escapeSQL(strings.ToLower(developer)),
		))
	}

	warnings := []string{}

	// 1. Resolve the period window in absolute dates for the response payload
	//    and for daily_average_usd computation. period_days is elapsed-inclusive:
	//    Apr 1 → Apr 1 == 1, Apr 1 → Apr 30 == 30.
	windowRows, err := q.Query(ctx, fmt.Sprintf(
		`SELECT toString(toDate(toTimeZone(%s, 'UTC'))) AS start, toString(toDate(toTimeZone(%s - toIntervalSecond(1), 'UTC'))) AS end, dateDiff('day', toDate(toTimeZone(%s, 'UTC')), toDate(toTimeZone(%s - toIntervalSecond(1), 'UTC'))) + 1 AS period_days`,
		startExpr, endExpr, startExpr, endExpr,
	))
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	periodStart, periodEnd, periodDays := extractWindow(windowRows)

	// 2. Aggregate cost per developer.
	sumWhere := append([]string{"MetricName = 'claude_code.cost.usage'"}, baseWhere...)
	costRows, err := q.Query(ctx, fmt.Sprintf(
		`SELECT Attributes['user.email'] AS developer, round(sum(Value), 6) AS total_cost_usd FROM observability.otel_metrics_sum WHERE %s GROUP BY developer ORDER BY total_cost_usd DESC`,
		strings.Join(sumWhere, " AND "),
	))
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}

	// 3. Aggregate active time per developer from the gauge table.
	//    C2: gauge datapoints can be re-emitted per (session, repository,
	//    model, TimeUnix) — collapse duplicates first, then sum per developer.
	//    BL-NEW-1: coalesce missing session.id with a synthetic per-timestamp
	//    discriminator so rows without session.id remain distinct per
	//    TimeUnix instead of collapsing into one bucket and under-counting.
	gaugeWhere := append([]string{"MetricName = 'claude_code.active_time.total'"}, baseWhere...)
	activeRows, err := q.Query(ctx, fmt.Sprintf(
		`SELECT developer, sum(v) AS active_seconds FROM (
			SELECT Attributes['user.email'] AS developer, max(Value) AS v
			FROM observability.otel_metrics_gauge
			WHERE %s
			GROUP BY
				developer,
				coalesce(nullIf(Attributes['session.id'], ''), concat('ts:', toString(TimeUnix))),
				coalesce(nullIf(Attributes['repository'], ''), ''),
				coalesce(nullIf(Attributes['model'], ''), ''),
				TimeUnix
		) GROUP BY developer`,
		strings.Join(gaugeWhere, " AND "),
	))
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	activeByDev := map[string]float64{}
	for _, r := range activeRows {
		dev := toString(r["developer"])
		activeByDev[dev] = toFloat(r["active_seconds"])
	}

	// Collect the full set of developers: union of cost + active-time rows.
	devs := make([]string, 0, len(costRows)+len(activeByDev))
	seen := map[string]bool{}
	addDev := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		devs = append(devs, name)
	}
	for _, r := range costRows {
		addDev(toString(r["developer"]))
	}
	for d := range activeByDev {
		addDev(d)
	}

	costByDev := map[string]float64{}
	for _, r := range costRows {
		costByDev[toString(r["developer"])] = toFloat(r["total_cost_usd"])
	}

	// BL-H3: viewer with no rows → inject self entry so the viewer sees a
	// zeroed-out ROI card rather than an empty list.
	if forcedFilter && len(devs) == 0 && u.Email != "" {
		addDev(u.Email)
	}

	// 4. Per-developer breakdowns.
	//
	// CQ-L2: pass baseWhereNoDev so buildROIEntry applies the per-developer
	// filter exactly once. Otherwise the developer clause would appear twice
	// in each emitted query.
	developers := make([]map[string]any, 0, len(devs))
	for _, dev := range devs {
		entry := buildROIEntry(ctx, q, dev, baseWhereNoDev, costByDev[dev], activeByDev[dev],
			productivityFactor, hourlyRateUSD, periodDays, &warnings)
		developers = append(developers, entry)
	}

	result := map[string]any{
		"timezone": "UTC",
		"period": map[string]any{
			"start": periodStart,
			"end":   periodEnd,
		},
		"period_days":       periodDays,
		"period_days_basis": "elapsed_days_inclusive",
		"constants": map[string]any{
			"productivity_factor": productivityFactor,
			"hourly_rate_usd":     hourlyRateUSD,
		},
		"filter": map[string]any{
			"developer": developerArgOrAll(developer),
			"forced":    forcedFilter,
		},
		"developers": developers,
		"warnings":   warnings,
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return textResult(string(b)), nil, nil
}

// buildROIEntry assembles the per-developer payload — ratios + daily trend +
// by_model + by_repository. Scope is narrowed by adding a developer filter on
// top of baseWhere so each dev's breakdowns are independent.
//
// daily_average_usd basis = elapsed_days_inclusive. e.g. on April 1 of a
// month-to-date run, period_days == 1 and daily_average_usd == total_cost_usd;
// by April 30 it is total_cost_usd / 30. That is intentional but callers
// should inspect `period_days` / `period_days_basis` to interpret it.
func buildROIEntry(
	ctx context.Context,
	q Querier,
	developer string,
	baseWhere []string,
	totalCostUSD float64,
	activeSeconds float64,
	productivityFactor float64,
	hourlyRateUSD float64,
	periodDays int,
	warnings *[]string,
) map[string]any {
	hoursActive := activeSeconds / 3600.0
	hoursEquivalent := hoursActive * productivityFactor
	valueDelivered := hoursEquivalent * hourlyRateUSD
	netBenefit := valueDelivered - totalCostUSD
	var dailyAvg float64
	if periodDays > 0 {
		dailyAvg = totalCostUSD / float64(periodDays)
	}

	entry := map[string]any{
		"developer":           developer,
		"total_cost_usd":      round6(totalCostUSD),
		"hours_active":        round6(hoursActive),
		"value_delivered_usd": round6(valueDelivered),
		"net_benefit_usd":     round6(netBenefit),
		"daily_average_usd":   round6(dailyAvg),
		"period_days":         periodDays,
	}
	// roi_ratio is nil (JSON null) when cost is zero to avoid division-by-zero.
	if totalCostUSD > 0 {
		entry["roi_ratio"] = round6(valueDelivered / totalCostUSD)
	} else {
		entry["roi_ratio"] = nil
	}

	// Per-developer scope: extend baseWhere with explicit developer filter.
	// BL-NEW-2 / Sec-NEW-1: case-insensitive equality via lower() on both
	// sides. CQ-L2: caller passes baseWhereNoDev so the developer clause is
	// applied exactly once.
	devWhere := append([]string{}, baseWhere...)
	devWhere = append(devWhere, fmt.Sprintf(
		"lower(Attributes['user.email']) = '%s'",
		escapeSQL(strings.ToLower(developer)),
	))

	// daily_trend — cost per day, ASC.
	trendWhere := append([]string{"MetricName = 'claude_code.cost.usage'"}, devWhere...)
	trendRows, err := q.Query(ctx, fmt.Sprintf(
		`SELECT toString(toDate(toTimeZone(TimeUnix, 'UTC'))) AS day, round(sum(Value), 6) AS cost_usd FROM observability.otel_metrics_sum WHERE %s GROUP BY day ORDER BY day ASC`,
		strings.Join(trendWhere, " AND "),
	))
	if err != nil {
		entry["daily_trend_error"] = err.Error()
		entry["daily_trend"] = []any{}
		if warnings != nil {
			*warnings = append(*warnings, fmt.Sprintf("daily_trend for %s: %s", developer, err.Error()))
		}
	} else {
		entry["daily_trend"] = rowsToList(trendRows, "day", "cost_usd")
	}

	// by_model — tokens + cost, DESC by cost.
	modelRows, err := q.Query(ctx, fmt.Sprintf(
		`SELECT Attributes['model'] AS model, round(sumIf(Value, MetricName='claude_code.cost.usage'), 6) AS cost_usd, round(sumIf(Value, MetricName='claude_code.token.usage'), 0) AS tokens FROM observability.otel_metrics_sum WHERE MetricName IN ('claude_code.cost.usage','claude_code.token.usage') AND %s GROUP BY model ORDER BY cost_usd DESC`,
		strings.Join(devWhere, " AND "),
	))
	if err != nil {
		entry["by_model_error"] = err.Error()
		entry["by_model"] = []any{}
		if warnings != nil {
			*warnings = append(*warnings, fmt.Sprintf("by_model for %s: %s", developer, err.Error()))
		}
	} else {
		entry["by_model"] = rowsToList(modelRows, "model", "cost_usd", "tokens")
	}

	// by_repository — DESC by cost.
	repoWhere := append([]string{"MetricName = 'claude_code.cost.usage'"}, devWhere...)
	repoRows, err := q.Query(ctx, fmt.Sprintf(
		`SELECT Attributes['repository'] AS repository, round(sum(Value), 6) AS cost_usd FROM observability.otel_metrics_sum WHERE %s GROUP BY repository ORDER BY cost_usd DESC`,
		strings.Join(repoWhere, " AND "),
	))
	if err != nil {
		entry["by_repository_error"] = err.Error()
		entry["by_repository"] = []any{}
		if warnings != nil {
			*warnings = append(*warnings, fmt.Sprintf("by_repository for %s: %s", developer, err.Error()))
		}
	} else {
		entry["by_repository"] = rowsToList(repoRows, "repository", "cost_usd")
	}

	return entry
}

// rowsToList projects the given columns from each row into an ordered map,
// preserving source order. Missing columns are omitted per-row.
func rowsToList(rows []map[string]any, cols ...string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		entry := make(map[string]any, len(cols))
		for _, c := range cols {
			if v, ok := r[c]; ok {
				entry[c] = v
			}
		}
		out = append(out, entry)
	}
	return out
}

// extractWindow pulls (start, end, period_days) from the single-row window
// query. All mock/real result shapes funnel through toString/toFloat so we
// tolerate both typed scanning and json.Number / float64 paths.
func extractWindow(rows []map[string]any) (string, string, int) {
	if len(rows) == 0 {
		return "", "", 1
	}
	r := rows[0]
	days := int(toFloat(r["period_days"]))
	days = max(days, 1)
	return toString(r["start"]), toString(r["end"]), days
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func toFloat(v any) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f
		}
	}
	return 0
}

// round6 rounds to 6 decimals so JSON output is stable and auditable.
func round6(v float64) float64 {
	// Using strconv round-trip is robust and doesn't require math.Round with
	// scaling tricks that drift at the ulp level.
	s := strconv.FormatFloat(v, 'f', 6, 64)
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// --- helpers ---

var (
	reRelativeSince = regexp.MustCompile(`^(\d+)([hdw])$`)
	reISODate       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reISODateTime   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
)

// parseSince converts a since string to a ClickHouse expression in UTC.
// Accepted forms:
//   - ""                     → default "24h" → toTimeZone(now(),'UTC') - INTERVAL 24 HOUR
//   - "Nh"/"Nd"/"Nw"        → toTimeZone(now(),'UTC') - INTERVAL N HOUR|DAY|WEEK  (N>0)
//   - "YYYY-MM-DDTHH:MM:SSZ" → parseDateTimeBestEffort('...', 'UTC')
//
// Rejects N=0 (BL-L2) because "since 0 hours" is an obviously nonsensical
// relative window.
func parseSince(v string) (string, error) {
	if v == "" {
		v = "24h"
	}
	if m := reRelativeSince.FindStringSubmatch(v); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			return "", fmt.Errorf("since must be > 0, got %q", v)
		}
		unit := map[string]string{"h": "HOUR", "d": "DAY", "w": "WEEK"}[m[2]]
		return fmt.Sprintf("toTimeZone(now(), 'UTC') - INTERVAL %d %s", n, unit), nil
	}
	if reISODateTime.MatchString(v) {
		return fmt.Sprintf("parseDateTimeBestEffort('%s', 'UTC')", escapeSQL(v)), nil
	}
	return "", fmt.Errorf("invalid since %q: expected Nh|Nd|Nw or YYYY-MM-DDTHH:MM:SSZ", v)
}

// parseDateRange resolves (startExpr, endExpr_exclusive) for the report tools.
// Priority: date_start+date_end (both required together) > period.
//
// BL-M4: endExpr is exclusive (= date_end + 1 day, 00:00:00 UTC) so callers
// compare `TimeUnix >= start AND TimeUnix < end_exclusive`. This avoids the
// 23:59:59 precision loss around seconds/sub-second boundaries.
//
// CQ-M2 / BL-M5: returns an error for unknown period values. Valid set:
// today, week, month, "" (empty → handled as caller's own default when the
// string is passed).
func parseDateRange(args Args, period string) (string, string, error) {
	ds := getString(args, "date_start", "")
	de := getString(args, "date_end", "")

	if ds != "" || de != "" {
		if ds == "" || de == "" {
			return "", "", fmt.Errorf("date_start and date_end must be provided together")
		}
		if !reISODate.MatchString(ds) {
			return "", "", fmt.Errorf("invalid date_start %q: expected YYYY-MM-DD", ds)
		}
		if !reISODate.MatchString(de) {
			return "", "", fmt.Errorf("invalid date_end %q: expected YYYY-MM-DD", de)
		}
		if de < ds {
			return "", "", fmt.Errorf("date_end %q must be >= date_start %q", de, ds)
		}
		startExpr := fmt.Sprintf("parseDateTimeBestEffort('%s 00:00:00', 'UTC')", escapeSQL(ds))
		// Exclusive end = de + 1 day, 00:00:00 UTC.
		endExpr := fmt.Sprintf("(parseDateTimeBestEffort('%s 00:00:00', 'UTC') + toIntervalDay(1))", escapeSQL(de))
		return startExpr, endExpr, nil
	}
	startExpr, err := periodToInterval(period)
	if err != nil {
		return "", "", err
	}
	// Relative periods end at "now" (UTC). Callers that want a fully-bounded
	// window should pass explicit dates.
	return startExpr, "toTimeZone(now(), 'UTC')", nil
}

// roleScopedEmail returns the email used to scope a query to a user.
// Viewer: always returns ctx.User.Email (ignores arg).
// Admin: honors arg (empty = no filter).
func roleScopedEmail(ctx context.Context, args Args, key string) string {
	u, ok := UserFromContext(ctx)
	if !ok {
		return ""
	}
	if !u.IsAdmin() {
		return u.Email
	}
	return getString(args, key, "")
}

func execQuery(ctx context.Context, q Querier, query string) (*sdk.CallToolResult, any, error) {
	rows, err := q.Query(ctx, query)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if rows == nil {
		rows = []map[string]any{}
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

// escapeSQL doubles single quotes for inclusion inside ClickHouse SQL string
// literals wrapped in '...'. It does NOT handle backslash, identifier quoting,
// LIKE wildcards, or other contexts. Callers must always single-quote the
// result.
//
// TODO: migrate to parameterized queries via Querier.Query args so call sites
// can stop worrying about lexical escaping entirely.
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

// periodToInterval maps a period keyword to a UTC-anchored ClickHouse
// expression. Unknown values return an error so the surface cannot silently
// fall back to "today" (CQ-M2).
func periodToInterval(period string) (string, error) {
	switch period {
	case "today", "day":
		return "toStartOfDay(toTimeZone(now(), 'UTC'))", nil
	case "week":
		return "toStartOfWeek(toTimeZone(now(), 'UTC'))", nil
	case "month":
		return "toStartOfMonth(toTimeZone(now(), 'UTC'))", nil
	case "":
		return "", fmt.Errorf("period is required when date_start/date_end are not provided")
	default:
		return "", fmt.Errorf("invalid period %q: expected today|week|month", period)
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
	vals := []string{}
	for _, r := range rows {
		if v, ok := r[col]; ok {
			vals = append(vals, fmt.Sprint(v))
		}
	}
	return vals
}

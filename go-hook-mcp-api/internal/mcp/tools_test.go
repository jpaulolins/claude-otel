package mcp

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockQuerier returns canned results and records the SQL executed.
//
//   - `results` and `err` apply to every call (backward-compat with existing tests).
//   - `sqlHistory` records every executed SQL statement in order.
//   - `resultsByIndex` optionally overrides `results` for the Nth call (0-based)
//     when a test runs multiple queries and needs per-call control.
//   - `matchResults` optionally maps a substring → results; the FIRST matching
//     entry wins (iteration order is deterministic via matchOrder). Useful when
//     the tool runs queries that aren't ordered stably across refactors.
//   - `resultsByPurpose` (Test-H3) takes precedence over matchResults and
//     `results`. Keys are one of: "window", "cost_agg", "active_agg", "trend",
//     "by_model", "by_repository", "timeline", "token_pivot", "other".
//   - `strictPurpose` + `t` (Test-NEW-HIGH-2): when resultsByPurpose is in use,
//     opting into strict mode via withStrictPurpose causes a `t.Fatalf` if the
//     classifier falls through to "other". Prevents tests from silently
//     passing on an unclassified SQL shape.
type mockQuerier struct {
	results          []map[string]any
	err              error
	lastSQL          string
	sqlHistory       []string
	resultsByIndex   map[int][]map[string]any
	matchResults     map[string][]map[string]any
	matchOrder       []string
	resultsByPurpose map[string][]map[string]any
	strictPurpose    bool
	t                *testing.T
}

// withStrictPurpose turns on strict-mode purpose routing for this mock, so
// any SQL the classifier cannot tag as a known purpose triggers a t.Fatalf.
// Callers pair this with resultsByPurpose: the assertion is that every SQL
// the tool emits lands in a known bucket.
func (m *mockQuerier) withStrictPurpose(t *testing.T) *mockQuerier {
	m.t = t
	m.strictPurpose = true
	return m
}

// Purpose classifies an executed SQL statement into one of a small set of
// labels so tests can route mock results by semantic intent rather than
// matching incidental SQL substrings. Keep the heuristics cheap — the goal is
// "good enough to fan-out mock routing", not full SQL parsing.
func (m *mockQuerier) Purpose(sql string) string {
	s := sql
	switch {
	case strings.Contains(s, "period_days"):
		return "window"
	case strings.Contains(s, "toDate") && strings.Contains(s, "GROUP BY day"):
		return "trend"
	case strings.Contains(s, "otel_metrics_gauge") && strings.Contains(s, "active_time.total"):
		return "active_agg"
	case strings.Contains(s, "'claude_code.cost.usage','claude_code.token.usage'") ||
		strings.Contains(s, "GROUP BY model"):
		return "by_model"
	case strings.Contains(s, "GROUP BY repository") && strings.Contains(s, "claude_code.cost.usage"):
		return "by_repository"
	case strings.Contains(s, "MetricName = 'claude_code.cost.usage'") &&
		strings.Contains(s, "GROUP BY developer"):
		return "cost_agg"
	case strings.Contains(s, "otel_logs") && strings.Contains(s, "groupUniqArray"):
		return "timeline"
	case strings.Contains(s, "sumIf(Value, MetricName='claude_code.token.usage'"):
		return "token_pivot"
	}
	return "other"
}

func (m *mockQuerier) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	idx := len(m.sqlHistory)
	m.lastSQL = query
	m.sqlHistory = append(m.sqlHistory, query)
	if m.err != nil {
		return nil, m.err
	}
	// Purpose-based routing takes precedence.
	if m.resultsByPurpose != nil {
		p := m.Purpose(query)
		// Test-NEW-HIGH-2: in strict mode, any SQL that does not classify
		// into a known purpose is a test bug — surface it loudly rather
		// than fall through to legacy routing with an empty result.
		if p == "other" && m.strictPurpose && m.t != nil {
			m.t.Fatalf("mockQuerier: SQL did not classify into any known purpose:\n%s", query)
		}
		if r, ok := m.resultsByPurpose[p]; ok {
			return r, nil
		}
	}
	if r, ok := m.resultsByIndex[idx]; ok {
		return r, nil
	}
	for _, key := range m.matchOrder {
		if strings.Contains(query, key) {
			return m.matchResults[key], nil
		}
	}
	return m.results, nil
}

// testServer wires a server + in-memory client and injects the given user
// (or nothing, when user is the zero value) into every tool call context.
func testServer(t *testing.T, q Querier, user *User) (*sdk.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()

	srv := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0.0.1"}, nil)
	if user != nil {
		u := *user
		srv.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
			return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
				ctx = context.WithValue(ctx, userContextKey{}, u)
				return next(ctx, method, req)
			}
		})
	}
	RegisterTools(srv, q)

	clientT, serverT := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	c := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := c.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	cleanup := func() {
		cs.Close()
		ss.Close()
	}
	return cs, cleanup
}

func adminUser() *User              { return &User{Email: "admin@test.com", Role: RoleAdmin} }
func viewerUser(email string) *User { return &User{Email: email, Role: RoleViewer} }

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func toolText(res *sdk.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*sdk.TextContent); ok {
		return tc.Text
	}
	return ""
}

// unsetEnv saves a value and restores it via t.Cleanup. Anti-Pattern-8: the
// previous idiom of `t.Setenv("","")` + `os.Unsetenv` was redundant and
// buggy. This helper does the right thing once.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, wasSet := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

// 8 admin + 3 role-aware report = 11 tools
func TestRegisterTools_Count(t *testing.T) {
	cs, cleanup := testServer(t, &mockQuerier{}, adminUser())
	defer cleanup()

	res, err := cs.ListTools(context.Background(), &sdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 11 {
		t.Errorf("tool count = %d; want 11", len(res.Tools))
		for _, tool := range res.Tools {
			t.Logf("  tool: %s", tool.Name)
		}
	}
}

// Admin tool blocked for viewer
func TestAdminTool_Forbidden_ForViewer(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("user@test.com"))
	defer cleanup()

	res := callTool(t, cs, "recent_logs", nil)
	if !res.IsError {
		t.Error("expected error for viewer calling admin tool")
	}
	if !strings.Contains(toolText(res), "forbidden") {
		t.Errorf("expected forbidden message; got %q", toolText(res))
	}
}

// Admin tool blocked without auth (no user injected)
func TestAdminTool_Unauthorized_NoAuth(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, nil)
	defer cleanup()

	res := callTool(t, cs, "recent_logs", nil)
	if !res.IsError {
		t.Error("expected error without auth")
	}
	if !strings.Contains(toolText(res), "unauthorized") {
		t.Errorf("expected unauthorized message; got %q", toolText(res))
	}
}

// recent_logs default limit
func TestAdminTool_RecentLogs_DefaultLimit(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "recent_logs", nil)
	if !strings.Contains(mq.lastSQL, "LIMIT 10") {
		t.Errorf("expected LIMIT 10 in SQL; got %q", mq.lastSQL)
	}
}

// recent_logs custom limit
func TestAdminTool_RecentLogs_CustomLimit(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "recent_logs", map[string]any{"limit": 25})
	if !strings.Contains(mq.lastSQL, "LIMIT 25") {
		t.Errorf("expected LIMIT 25 in SQL; got %q", mq.lastSQL)
	}
}

// metric_attributes rejects unknown metric (allowlist)
func TestAdminTool_MetricAttributes_InvalidMetric(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "metric_attributes", map[string]any{"metric_name": "evil.metric; DROP TABLE"})
	if !res.IsError {
		t.Error("expected error for invalid metric name")
	}
}

// --- report_activity_timeline ---

func TestTimeline_DefaultSince24h(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", nil)
	if !strings.Contains(mq.lastSQL, "INTERVAL 24 HOUR") {
		t.Errorf("expected INTERVAL 24 HOUR; got %q", mq.lastSQL)
	}
}

func TestTimeline_Relative_7d(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"since": "7d"})
	if !strings.Contains(mq.lastSQL, "INTERVAL 7 DAY") {
		t.Errorf("expected INTERVAL 7 DAY; got %q", mq.lastSQL)
	}
}

func TestTimeline_Relative_2w(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"since": "2w"})
	if !strings.Contains(mq.lastSQL, "INTERVAL 2 WEEK") {
		t.Errorf("expected INTERVAL 2 WEEK; got %q", mq.lastSQL)
	}
}

func TestTimeline_ISO8601(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"since": "2026-04-01T00:00:00Z"})
	if !strings.Contains(mq.lastSQL, "parseDateTimeBestEffort('2026-04-01T00:00:00Z', 'UTC')") {
		t.Errorf("expected parseDateTimeBestEffort; got %q", mq.lastSQL)
	}
}

func TestTimeline_InvalidSince_Rejected(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", map[string]any{"since": "abc"})
	if !res.IsError {
		t.Error("expected error for invalid since")
	}
}

func TestTimeline_SourceAgent(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"source": "agent"})
	if !strings.Contains(mq.lastSQL, "ServiceName = 'claude-audit-service'") {
		t.Errorf("expected ServiceName filter; got %q", mq.lastSQL)
	}
}

func TestTimeline_SourceGithub_NotImplemented(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", map[string]any{"source": "github"})
	if !res.IsError {
		t.Error("expected error for source=github")
	}
	if !strings.Contains(toolText(res), "source not yet ingested") {
		t.Errorf("expected 'source not yet ingested'; got %q", toolText(res))
	}
}

func TestTimeline_SourceLinear_NotImplemented(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", map[string]any{"source": "linear"})
	if !res.IsError {
		t.Error("expected error for source=linear")
	}
	if !strings.Contains(toolText(res), "source not yet ingested") {
		t.Errorf("expected 'source not yet ingested'; got %q", toolText(res))
	}
}

func TestTimeline_SourceInvalid_Rejected(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", map[string]any{"source": "jenkins"})
	if !res.IsError {
		t.Error("expected error for unknown source")
	}
	if !strings.Contains(toolText(res), "invalid source") {
		t.Errorf("expected 'invalid source' message; got %q", toolText(res))
	}
}

func TestTimeline_RepositoryFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"repository": "api"})
	if !strings.Contains(mq.lastSQL, "'repository'") {
		t.Errorf("expected repository key in SQL; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "= 'api'") {
		t.Errorf("expected = 'api' in SQL; got %q", mq.lastSQL)
	}
}

func TestTimeline_RepositoryAll_NoFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"repository": "all"})
	// The SELECT projects `JSONExtractString(Body, 'repository')` so we check
	// the WHERE clause doesn't equal-filter on it.
	if strings.Contains(mq.lastSQL, "JSONExtractString(Body, 'repository') = ") {
		t.Errorf("expected no repository filter; got %q", mq.lastSQL)
	}
}

func TestTimeline_GroupIdFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"group_id": "org_X"})
	if !strings.Contains(mq.lastSQL, "organization_id") {
		t.Errorf("expected organization_id filter; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "= 'org_X'") {
		t.Errorf("expected group_id value; got %q", mq.lastSQL)
	}
}

func TestTimeline_GroupIdAll_NoFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"group_id": "all"})
	if strings.Contains(mq.lastSQL, "organization_id") {
		t.Errorf("expected no organization_id filter for 'all'; got %q", mq.lastSQL)
	}
}

func TestTimeline_MaxItemsClamped(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"max_items": 99999})
	if !strings.Contains(mq.lastSQL, "LIMIT 10000") {
		t.Errorf("expected LIMIT 10000; got %q", mq.lastSQL)
	}
}

func TestTimeline_MaxItemsDefault(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", nil)
	if !strings.Contains(mq.lastSQL, "LIMIT 100") {
		t.Errorf("expected LIMIT 100; got %q", mq.lastSQL)
	}
}

func TestTimeline_MaxItemsLowerClamp(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"max_items": 0})
	if !strings.Contains(mq.lastSQL, "LIMIT 1") {
		t.Errorf("expected LIMIT 1 (lower clamp); got %q", mq.lastSQL)
	}
}

func TestTimeline_MaxItemsExact10000(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"max_items": 10000})
	if !strings.Contains(mq.lastSQL, "LIMIT 10000") {
		t.Errorf("expected LIMIT 10000; got %q", mq.lastSQL)
	}
}

func TestTimeline_ViewerScoped_ForcesActorFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("viewer@test.com"))
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", nil)
	// BL-H4: the viewer filter is now a lowercased OR on Body.user_id and
	// Attributes['user.email'].
	if !strings.Contains(mq.lastSQL, "'viewer@test.com'") {
		t.Errorf("expected viewer email in SQL; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "lower(JSONExtractString(Body, 'user_id'))") {
		t.Errorf("expected lowercased Body.user_id filter; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "lower(Attributes['user.email'])") {
		t.Errorf("expected lowercased Attributes[user.email] filter; got %q", mq.lastSQL)
	}
}

func TestTimeline_AdminCrossUser(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", nil)
	if strings.Contains(mq.lastSQL, "'user_id') = ") {
		t.Errorf("admin should not have user_id filter; got %q", mq.lastSQL)
	}
	if strings.Contains(mq.lastSQL, "lower(JSONExtractString(Body, 'user_id'))") {
		t.Errorf("admin should not have a lowercased user_id filter either; got %q", mq.lastSQL)
	}
}

func TestTimeline_AdminDeveloperArg_AppliesFilter(t *testing.T) {
	// CQ-H1: report_activity_timeline now accepts `developer` for admin.
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"developer": "alice@x.com"})
	if !strings.Contains(mq.lastSQL, "lower(JSONExtractString(Body, 'user_id'))") {
		t.Errorf("expected lowercased Body.user_id filter; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "'alice@x.com'") {
		t.Errorf("expected alice email literal; got %q", mq.lastSQL)
	}
}

func TestTimeline_Unauthorized(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, nil)
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", nil)
	if !res.IsError {
		t.Error("expected error without auth")
	}
	if !strings.Contains(toolText(res), "unauthorized") {
		t.Errorf("expected unauthorized; got %q", toolText(res))
	}
}

func TestTimeline_SQLInjection_Repository(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"repository": "'; DROP--"})
	// Expect doubled single-quote: ''; DROP--
	if !strings.Contains(mq.lastSQL, "''; DROP--") {
		t.Errorf("expected escaped single quote (''); got %q", mq.lastSQL)
	}
}

func TestTimeline_SQLInjection_GroupId(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_activity_timeline", map[string]any{"group_id": "o'; DROP--"})
	if !strings.Contains(mq.lastSQL, "o''; DROP--") {
		t.Errorf("expected escaped single quote (''); got %q", mq.lastSQL)
	}
}

func TestTimeline_ViewerEmpty_ReturnsEmpty(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("viewer@test.com"))
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	// Contrast with ROI self-entry behavior — timeline returns an empty list.
	body := toolText(res)
	if !strings.Contains(body, `"rows": []`) {
		t.Errorf("expected rows:[] for viewer with no data; got %s", body)
	}
}

func TestTimeline_ResponseEnvelope_Timezone(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	body := toolText(res)
	if !strings.Contains(body, `"timezone": "UTC"`) {
		t.Errorf("expected timezone UTC in envelope; got %s", body)
	}
	if !strings.Contains(body, `"filter":`) {
		t.Errorf("expected filter envelope; got %s", body)
	}
}

func TestTimeline_BehavioralFilter_Semantics(t *testing.T) {
	// Test-NEW-HIGH-1: the paired assertion to TestTimeline_RepositoryFilter.
	// That test proves the filter SQL is emitted; this test proves that the
	// filtered shape reaches the envelope. Because the mock cannot actually
	// run a WHERE clause, we seed ONLY the expected post-filter rows and then
	// decode the envelope to confirm cardinality + identity.
	rows := []map[string]any{
		{"id": "s1", "repository": "petuti", "event_count": float64(5)},
		{"id": "s3", "repository": "petuti", "event_count": float64(2)},
	}
	mq := &mockQuerier{results: rows}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_activity_timeline", map[string]any{"repository": "petuti"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	if !strings.Contains(mq.lastSQL, "= 'petuti'") {
		t.Errorf("expected SQL filter on 'petuti'; got %q", mq.lastSQL)
	}

	var env struct {
		Timezone string           `json:"timezone"`
		Filter   map[string]any   `json:"filter"`
		Rows     []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(toolText(res)), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody=%s", err, toolText(res))
	}

	// Cardinality: exactly the two seeded rows.
	if len(env.Rows) != 2 {
		t.Fatalf("rows cardinality = %d; want 2", len(env.Rows))
	}
	// Identity: every expected id/repository is present; "other" repo is absent.
	gotIDs := map[string]bool{}
	gotRepos := map[string]bool{}
	for _, r := range env.Rows {
		gotIDs[toString(r["id"])] = true
		gotRepos[toString(r["repository"])] = true
	}
	for _, want := range []string{"s1", "s3"} {
		if !gotIDs[want] {
			t.Errorf("envelope missing id %q; got %v", want, gotIDs)
		}
	}
	if gotIDs["s2"] {
		t.Errorf("unexpected filtered-out id s2 leaked; got %v", gotIDs)
	}
	if !gotRepos["petuti"] {
		t.Errorf("expected repository petuti in envelope; got %v", gotRepos)
	}
	if gotRepos["other"] {
		t.Errorf("wrong repository leaked; got %v", gotRepos)
	}
}

// --- report_token_usage ---

func TestTokenUsage_DefaultPeriod_Week(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", nil)
	if !strings.Contains(mq.lastSQL, "toStartOfWeek(toTimeZone(now(), 'UTC'))") {
		t.Errorf("expected toStartOfWeek(UTC); got %q", mq.lastSQL)
	}
}

func TestTokenUsage_Today(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"period": "today"})
	if !strings.Contains(mq.lastSQL, "toStartOfDay(toTimeZone(now(), 'UTC'))") {
		t.Errorf("expected toStartOfDay(UTC); got %q", mq.lastSQL)
	}
}

func TestTokenUsage_Month(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"period": "month"})
	if !strings.Contains(mq.lastSQL, "toStartOfMonth(toTimeZone(now(), 'UTC'))") {
		t.Errorf("expected toStartOfMonth(UTC); got %q", mq.lastSQL)
	}
}

func TestTokenUsage_DateRangeOverridesPeriod(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{
		"period":     "week",
		"date_start": "2026-04-01",
		"date_end":   "2026-04-07",
	})
	if !strings.Contains(mq.lastSQL, "parseDateTimeBestEffort('2026-04-01 00:00:00', 'UTC')") {
		t.Errorf("expected UTC start; got %q", mq.lastSQL)
	}
	// BL-M4: end is exclusive, day after date_end.
	if !strings.Contains(mq.lastSQL, "parseDateTimeBestEffort('2026-04-07 00:00:00', 'UTC') + toIntervalDay(1)") {
		t.Errorf("expected exclusive end (date_end+1 day); got %q", mq.lastSQL)
	}
	if strings.Contains(mq.lastSQL, "23:59:59") {
		t.Errorf("expected no 23:59:59 inclusive end; got %q", mq.lastSQL)
	}
	if strings.Contains(mq.lastSQL, "toStartOfWeek") {
		t.Errorf("period should be ignored when dates set; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_InvalidDate_Rejected(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{
		"date_start": "not-a-date",
		"date_end":   "2026-04-07",
	})
	if !res.IsError {
		t.Error("expected error for invalid date_start")
	}
}

func TestTokenUsage_DateStartOnly_Rejected(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{
		"date_start": "2026-04-01",
	})
	if !res.IsError {
		t.Error("expected error for date_start without date_end")
	}
}

func TestTokenUsage_DateEndOnly_Rejected(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{
		"date_end": "2026-04-01",
	})
	if !res.IsError {
		t.Error("expected error for date_end without date_start")
	}
}

func TestTokenUsage_DateRangeReversed(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{
		"date_start": "2026-04-10",
		"date_end":   "2026-04-01",
	})
	if !res.IsError {
		t.Error("expected error when date_end < date_start")
	}
}

func TestTokenUsage_ViewerDateRangeCombined(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("viewer@test.com"))
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{
		"date_start": "2026-04-01",
		"date_end":   "2026-04-07",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	if !strings.Contains(mq.lastSQL, "= 'viewer@test.com'") {
		t.Errorf("expected viewer email in SQL; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "parseDateTimeBestEffort('2026-04-01 00:00:00', 'UTC')") {
		t.Errorf("expected UTC start; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_DeveloperFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"developer": "alice@x"})
	if !strings.Contains(mq.lastSQL, "= 'alice@x'") {
		t.Errorf("expected alice@x filter; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_ViewerForcesDeveloperSelf(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("viewer@test.com"))
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"developer": "other@x"})
	if !strings.Contains(mq.lastSQL, "= 'viewer@test.com'") {
		t.Errorf("expected viewer email in SQL; got %q", mq.lastSQL)
	}
	if strings.Contains(mq.lastSQL, "other@x") {
		t.Errorf("should not contain overridden developer; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_AdminRespectsDeveloperArg(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"developer": "alice"})
	if !strings.Contains(mq.lastSQL, "'alice'") {
		t.Errorf("expected alice in SQL; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_AdminDeveloperAll_NoFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"developer": "all"})
	// BL-NEW-2 / Sec-NEW-1: defend against both the old raw form and the
	// new lower()'d form.
	if strings.Contains(mq.lastSQL, "Attributes['user.email'] = '") {
		t.Errorf("expected no developer filter; got %q", mq.lastSQL)
	}
	if strings.Contains(mq.lastSQL, "lower(Attributes['user.email']) = '") {
		t.Errorf("expected no (lower-cased) developer filter; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_RepositoryFilter(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"repository": "api"})
	if !strings.Contains(mq.lastSQL, "Attributes['repository'] = 'api'") {
		t.Errorf("expected repository filter; got %q", mq.lastSQL)
	}
}

func TestTokenUsage_NoData_ReturnsEmptyRows(t *testing.T) {
	mq := &mockQuerier{results: nil}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", nil)
	if res.IsError {
		t.Errorf("expected non-error on empty results; got %q", toolText(res))
	}
	body := toolText(res)
	if !strings.Contains(body, `"rows": []`) {
		t.Errorf("expected rows:[] on empty results; got %s", body)
	}
}

func TestTokenUsage_SQLInjection_Developer(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"developer": "a'; DROP--"})
	// BL-NEW-2 / Sec-NEW-1: the filter now lower()s both sides, so the
	// literal is lowered to `a''; drop--`. The injection defense (doubled
	// single quote) is unchanged.
	if !strings.Contains(mq.lastSQL, "a''; drop--") {
		t.Errorf("expected escaped single quote (lower-cased); got %q", mq.lastSQL)
	}
}

func TestTokenUsage_PivotShape(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", nil)
	for _, col := range []string{"input_tokens", "output_tokens", "cache_read", "cache_creation", "cost_usd"} {
		if !strings.Contains(mq.lastSQL, col) {
			t.Errorf("expected column %s in SELECT; got %q", col, mq.lastSQL)
		}
	}
}

// BL-NEW-2 / Sec-NEW-1: mixed-case developer emails must lower() on both
// sides of the comparison so the filter actually matches the stored rows
// (which can be lower-cased at ingestion time).
func TestTokenUsage_MixedCaseDeveloper_MatchesLower(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_token_usage", map[string]any{"developer": "Alice@Example.Com"})
	if !strings.Contains(mq.lastSQL, "lower(Attributes['user.email'])") {
		t.Errorf("expected lowercased Attributes[user.email] filter; got %q", mq.lastSQL)
	}
	if !strings.Contains(mq.lastSQL, "'alice@example.com'") {
		t.Errorf("expected lower-cased literal 'alice@example.com'; got %q", mq.lastSQL)
	}
	if strings.Contains(mq.lastSQL, "'Alice@Example.Com'") {
		t.Errorf("raw mixed-case email leaked into SQL; got %q", mq.lastSQL)
	}
}

// Test-NEW-LOW-3: mirror of TestTimeline_Unauthorized / TestROI_Unauthorized_NoAuth
// — any authenticated tool must reject unauthenticated callers with an error
// envelope that mentions "unauthorized".
func TestTokenUsage_Unauthorized_NoAuth(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, nil)
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", nil)
	if !res.IsError {
		t.Error("expected error without auth")
	}
	if !strings.Contains(toolText(res), "unauthorized") {
		t.Errorf("expected unauthorized; got %q", toolText(res))
	}
}

func TestTokenUsage_InvalidPeriod_Rejected(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{"period": "quarter"})
	if !res.IsError {
		t.Error("expected error for invalid period")
	}
	if !strings.Contains(toolText(res), "invalid period") {
		t.Errorf("expected 'invalid period' in message; got %q", toolText(res))
	}
}

func TestTokenUsage_BehavioralFilter_Semantics(t *testing.T) {
	// Test-NEW-HIGH-1: paired with TestTokenUsage_DeveloperFilter. The mock
	// cannot run a real WHERE, so we seed only the expected post-filter row
	// and assert cardinality + identity on the decoded envelope.
	rows := []map[string]any{
		{"developer": "alice@x.com", "model": "opus", "cost_usd": float64(1.5)},
	}
	mq := &mockQuerier{results: rows}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_token_usage", map[string]any{"developer": "alice@x.com"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	// BL-NEW-2 / Sec-NEW-1: case-insensitive SQL uses lower() on both sides.
	if !strings.Contains(mq.lastSQL, "lower(Attributes['user.email']) = 'alice@x.com'") {
		t.Errorf("expected case-insensitive alice filter in SQL; got %q", mq.lastSQL)
	}

	var env struct {
		Timezone string           `json:"timezone"`
		Filter   map[string]any   `json:"filter"`
		Rows     []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(toolText(res)), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody=%s", err, toolText(res))
	}
	if env.Timezone != "UTC" {
		t.Errorf("timezone = %q; want UTC", env.Timezone)
	}
	if forced, _ := env.Filter["forced"].(bool); forced {
		t.Errorf("expected forced:false for admin; got %v", env.Filter["forced"])
	}
	// Cardinality: exactly the seeded row.
	if len(env.Rows) != 1 {
		t.Fatalf("rows cardinality = %d; want 1", len(env.Rows))
	}
	// Identity: alice present; bob absent.
	gotDevs := map[string]bool{}
	for _, r := range env.Rows {
		gotDevs[toString(r["developer"])] = true
	}
	if !gotDevs["alice@x.com"] {
		t.Errorf("envelope missing alice@x.com; got %v", gotDevs)
	}
	if gotDevs["bob@x.com"] {
		t.Errorf("unexpected filtered-out bob@x.com leaked; got %v", gotDevs)
	}
}

// --- helper tests ---

func TestParseSince(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "toTimeZone(now(), 'UTC') - INTERVAL 24 HOUR", false},
		{"24h", "toTimeZone(now(), 'UTC') - INTERVAL 24 HOUR", false},
		{"7d", "toTimeZone(now(), 'UTC') - INTERVAL 7 DAY", false},
		{"2w", "toTimeZone(now(), 'UTC') - INTERVAL 2 WEEK", false},
		{"2026-04-01T00:00:00Z", "parseDateTimeBestEffort('2026-04-01T00:00:00Z', 'UTC')", false},
		{"abc", "", true},
		{"12", "", true},
		{"24hour", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSince(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseSince(%q) expected error; got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseSince(%q) unexpected error: %v", tc.in, err)
				return
			}
			if got != tc.want {
				t.Errorf("parseSince(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// BL-L2: zero-valued relative since ("0h" etc.) must be rejected.
func TestParseSince_ZeroRejected(t *testing.T) {
	for _, s := range []string{"0h", "0d", "0w"} {
		if _, err := parseSince(s); err == nil {
			t.Errorf("parseSince(%q) expected error; got nil", s)
		}
	}
}

func TestParseDateRange(t *testing.T) {
	t.Run("period only", func(t *testing.T) {
		start, end, err := parseDateRange(Args{}, "week")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if start != "toStartOfWeek(toTimeZone(now(), 'UTC'))" || end != "toTimeZone(now(), 'UTC')" {
			t.Errorf("got (%q,%q)", start, end)
		}
	})
	t.Run("date_start only", func(t *testing.T) {
		_, _, err := parseDateRange(Args{"date_start": "2026-04-01"}, "week")
		if err == nil {
			t.Error("expected error for date_start without date_end")
		}
	})
	t.Run("full date range", func(t *testing.T) {
		start, end, err := parseDateRange(Args{
			"date_start": "2026-04-01",
			"date_end":   "2026-04-07",
		}, "week")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !strings.Contains(start, "2026-04-01 00:00:00") {
			t.Errorf("start = %q", start)
		}
		if !strings.Contains(end, "toIntervalDay(1)") {
			t.Errorf("end should be exclusive with toIntervalDay(1); got %q", end)
		}
	})
	t.Run("dates win over period", func(t *testing.T) {
		start, _, err := parseDateRange(Args{
			"date_start": "2026-04-01",
			"date_end":   "2026-04-07",
		}, "month")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if strings.Contains(start, "toStartOfMonth") {
			t.Errorf("date range should override period; got start=%q", start)
		}
	})
	t.Run("invalid date format", func(t *testing.T) {
		_, _, err := parseDateRange(Args{
			"date_start": "04-01-2026",
			"date_end":   "04-07-2026",
		}, "week")
		if err == nil {
			t.Error("expected error for invalid date format")
		}
	})
	t.Run("date_end before date_start", func(t *testing.T) {
		_, _, err := parseDateRange(Args{
			"date_start": "2026-04-10",
			"date_end":   "2026-04-01",
		}, "week")
		if err == nil {
			t.Error("expected error for reversed date range")
		}
	})
}

// BL-M5 / CQ-M2: invalid period must error.
func TestParseDateRange_InvalidPeriod_Rejected(t *testing.T) {
	_, _, err := parseDateRange(Args{}, "quarter")
	if err == nil {
		t.Error("expected error for invalid period")
	}
}

func TestRoleScopedEmail_Admin(t *testing.T) {
	ctx := context.WithValue(context.Background(), userContextKey{}, User{Email: "admin@test.com", Role: RoleAdmin})
	// honors arg
	if got := roleScopedEmail(ctx, Args{"developer": "alice"}, "developer"); got != "alice" {
		t.Errorf("admin honors arg: got %q; want alice", got)
	}
	// empty arg = empty return (no filter)
	if got := roleScopedEmail(ctx, Args{}, "developer"); got != "" {
		t.Errorf("admin empty arg: got %q; want empty", got)
	}
}

func TestRoleScopedEmail_Viewer(t *testing.T) {
	ctx := context.WithValue(context.Background(), userContextKey{}, User{Email: "viewer@test.com", Role: RoleViewer})
	// ignores arg, returns ctx email
	if got := roleScopedEmail(ctx, Args{"developer": "other"}, "developer"); got != "viewer@test.com" {
		t.Errorf("viewer forced self: got %q; want viewer@test.com", got)
	}
	if got := roleScopedEmail(ctx, Args{}, "developer"); got != "viewer@test.com" {
		t.Errorf("viewer no arg: got %q; want viewer@test.com", got)
	}
}

// --- existing utility tests ---

func TestOptionalEmailFilter_SQLInjection(t *testing.T) {
	args := Args{"user_email": "test'; DROP TABLE--"}
	filter := optionalEmailFilter(args)
	if !strings.Contains(filter, "test''") {
		t.Errorf("single quotes should be escaped: %q", filter)
	}
	if strings.Contains(filter, "test'; DROP") {
		t.Errorf("raw single quote not escaped: %q", filter)
	}
}

func TestEscapeSQL(t *testing.T) {
	if escapeSQL("o'malley") != "o''malley" {
		t.Error("escapeSQL failed")
	}
}

func TestPeriodToInterval(t *testing.T) {
	tests := []struct {
		period  string
		want    string
		wantErr bool
	}{
		{"day", "toStartOfDay(toTimeZone(now(), 'UTC'))", false},
		{"today", "toStartOfDay(toTimeZone(now(), 'UTC'))", false},
		{"week", "toStartOfWeek(toTimeZone(now(), 'UTC'))", false},
		{"month", "toStartOfMonth(toTimeZone(now(), 'UTC'))", false},
		{"invalid", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		got, err := periodToInterval(tc.period)
		if tc.wantErr {
			if err == nil {
				t.Errorf("periodToInterval(%q) expected error", tc.period)
			}
			continue
		}
		if err != nil {
			t.Errorf("periodToInterval(%q) unexpected error: %v", tc.period, err)
			continue
		}
		if got != tc.want {
			t.Errorf("periodToInterval(%q) = %q; want %q", tc.period, got, tc.want)
		}
	}
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name string
		args Args
		want int
	}{
		{"missing", Args{}, 99},
		{"float64", Args{"k": float64(42)}, 42},
		{"int", Args{"k": 42}, 42},
		{"wrong type", Args{"k": "str"}, 99},
		{"nil args", nil, 99},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := getInt(tc.args, "k", 99); got != tc.want {
				t.Errorf("getInt = %d; want %d", got, tc.want)
			}
		})
	}
}

func TestGetString(t *testing.T) {
	tests := []struct {
		name string
		args Args
		want string
	}{
		{"missing", Args{}, "default"},
		{"present", Args{"k": "hello"}, "hello"},
		{"empty string", Args{"k": ""}, "default"},
		{"wrong type", Args{"k": 42}, "default"},
		{"nil args", nil, "default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := getString(tc.args, "k", "default"); got != tc.want {
				t.Errorf("getString = %q; want %q", got, tc.want)
			}
		})
	}
}

// --- report_developer_roi ---

// roiMock returns a mockQuerier wired with a permissive default: every query
// returns a single-row window for the window query, empty results otherwise.
// Individual tests override this via resultsByIndex, matchResults, or
// resultsByPurpose (the Test-H3 API).
//
// Test-NEW-HIGH-2: strict-purpose mode is enabled here so any SQL whose
// classifier falls through to "other" fails the test instead of quietly
// returning zero rows.
func roiMock(t *testing.T) *mockQuerier {
	m := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window":        {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg":      {},
			"active_agg":    {},
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	return m.withStrictPurpose(t)
}

// roiSQLContains searches every SQL statement executed by the mock, not only
// the last one (the ROI tool issues several queries per call).
func roiSQLContains(m *mockQuerier, want string) bool {
	for _, sql := range m.sqlHistory {
		if strings.Contains(sql, want) {
			return true
		}
	}
	return false
}

func TestROI_DefaultPeriod_Month(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", nil)
	if !roiSQLContains(mq, "toStartOfMonth(toTimeZone(now(), 'UTC'))") {
		t.Errorf("expected toStartOfMonth UTC; got history=%v", mq.sqlHistory)
	}
}

func TestROI_Today(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"period": "today"})
	if !roiSQLContains(mq, "toStartOfDay(toTimeZone(now(), 'UTC'))") {
		t.Errorf("expected toStartOfDay UTC; got %v", mq.sqlHistory)
	}
}

func TestROI_Week(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"period": "week"})
	if !roiSQLContains(mq, "toStartOfWeek(toTimeZone(now(), 'UTC'))") {
		t.Errorf("expected toStartOfWeek UTC; got %v", mq.sqlHistory)
	}
}

func TestROI_DateRangeOverridesPeriod(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{
		"period":     "month",
		"date_start": "2026-04-01",
		"date_end":   "2026-04-07",
	})
	if !roiSQLContains(mq, "parseDateTimeBestEffort('2026-04-01 00:00:00', 'UTC')") {
		t.Errorf("expected start date UTC; got %v", mq.sqlHistory)
	}
	if !roiSQLContains(mq, "toIntervalDay(1)") {
		t.Errorf("expected exclusive end (date_end + 1 day); got %v", mq.sqlHistory)
	}
	if roiSQLContains(mq, "toStartOfMonth") {
		t.Errorf("period should be ignored when dates set; got %v", mq.sqlHistory)
	}
}

func TestROI_InvalidDate_Rejected(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", map[string]any{
		"date_start": "not-a-date",
		"date_end":   "2026-04-07",
	})
	if !res.IsError {
		t.Error("expected error for invalid date_start")
	}
}

func TestROI_InvalidPeriod_Rejected(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", map[string]any{"period": "quarter"})
	if !res.IsError {
		t.Error("expected error for invalid period")
	}
}

func TestROI_DeveloperFilter(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"developer": "alice@x"})
	// BL-NEW-2 / Sec-NEW-1: filter lower()s both sides.
	if !roiSQLContains(mq, "lower(Attributes['user.email']) = 'alice@x'") {
		t.Errorf("expected case-insensitive alice@x filter; got %v", mq.sqlHistory)
	}
}

func TestROI_ViewerForcesDeveloperSelf(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, viewerUser("viewer@test.com"))
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"developer": "other@x"})
	// BL-NEW-2 / Sec-NEW-1: filter lower()s both sides.
	if !roiSQLContains(mq, "lower(Attributes['user.email']) = 'viewer@test.com'") {
		t.Errorf("expected viewer email in case-insensitive SQL; got %v", mq.sqlHistory)
	}
	for _, sql := range mq.sqlHistory {
		if strings.Contains(sql, "other@x") {
			t.Errorf("should not contain overridden developer; got %q", sql)
		}
	}
}

func TestROI_RepositoryFilter(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"repository": "api"})
	if !roiSQLContains(mq, "Attributes['repository'] = 'api'") {
		t.Errorf("expected repository filter; got %v", mq.sqlHistory)
	}
}

// roiEnvelope mirrors enough of the ROI response shape for structural tests.
type roiEnvelope struct {
	Timezone        string           `json:"timezone"`
	Period          map[string]any   `json:"period"`
	PeriodDays      int              `json:"period_days"`
	PeriodDaysBasis string           `json:"period_days_basis"`
	Constants       map[string]any   `json:"constants"`
	Filter          map[string]any   `json:"filter"`
	Developers      []map[string]any `json:"developers"`
	Warnings        []string         `json:"warnings"`
}

// roiUnmarshal decodes a ROI response body into roiEnvelope, failing the test
// on decode error. Test-H2: prefer typed decoding over substring peeking.
func roiUnmarshal(t *testing.T, body string) roiEnvelope {
	t.Helper()
	var env roiEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode ROI envelope: %v\nbody=%s", err, body)
	}
	return env
}

func TestROI_ConstantsInResponse(t *testing.T) {
	// Ensure a clean env so defaults apply deterministically regardless of host.
	unsetEnv(t, "ROI_PRODUCTIVITY_FACTOR")
	unsetEnv(t, "ROI_HOURLY_RATE_USD")

	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if pf, _ := env.Constants["productivity_factor"].(float64); pf != 3.5 {
		t.Errorf("productivity_factor = %v; want 3.5", env.Constants["productivity_factor"])
	}
	if hr, _ := env.Constants["hourly_rate_usd"].(float64); hr != 50.0 {
		t.Errorf("hourly_rate_usd = %v; want 50", env.Constants["hourly_rate_usd"])
	}
	if env.Timezone != "UTC" {
		t.Errorf("timezone = %q; want UTC", env.Timezone)
	}
	if env.PeriodDaysBasis != "elapsed_days_inclusive" {
		t.Errorf("period_days_basis = %q; want elapsed_days_inclusive", env.PeriodDaysBasis)
	}
	if env.PeriodDays < 1 {
		t.Errorf("period_days = %d; want >= 1", env.PeriodDays)
	}
	if env.Filter == nil {
		t.Errorf("filter envelope missing")
	}
	if env.Warnings == nil {
		t.Errorf("warnings field should be present (even if empty)")
	}
}

func TestROI_EnvVarOverride(t *testing.T) {
	t.Setenv("ROI_PRODUCTIVITY_FACTOR", "4.0")
	t.Setenv("ROI_HOURLY_RATE_USD", "75.0")

	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if pf, _ := env.Constants["productivity_factor"].(float64); pf != 4.0 {
		t.Errorf("productivity_factor = %v; want 4.0", env.Constants["productivity_factor"])
	}
	if hr, _ := env.Constants["hourly_rate_usd"].(float64); hr != 75.0 {
		t.Errorf("hourly_rate_usd = %v; want 75.0", env.Constants["hourly_rate_usd"])
	}
}

func TestROI_InvalidEnvVar_UsesDefault(t *testing.T) {
	t.Setenv("ROI_HOURLY_RATE_USD", "not-a-number")

	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	// Test-NEW-LOW-1: typed decode rather than substring search. JSON
	// renders 50.0 as `"hourly_rate_usd": 50` and 50.5 as `: 50.5`, so the
	// substring form would silently match "50something" too. Decode the
	// envelope and compare the typed value.
	env := roiUnmarshal(t, toolText(res))
	if hr, _ := env.Constants["hourly_rate_usd"].(float64); hr != 50.0 {
		t.Errorf("hourly_rate_usd = %v; want default 50.0", env.Constants["hourly_rate_usd"])
	}
}

func TestROI_ZeroCost_NoDivisionByZero(t *testing.T) {
	mq := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window": {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg": {
				{"developer": "alice@x.com", "total_cost_usd": float64(0)},
			},
			"active_agg": {
				{"developer": "alice@x.com", "active_seconds": float64(3600)},
			},
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if len(env.Developers) != 1 {
		t.Fatalf("expected 1 developer; got %d", len(env.Developers))
	}
	entry := env.Developers[0]
	if entry["roi_ratio"] != nil {
		t.Errorf("expected roi_ratio null when cost is zero; got %v", entry["roi_ratio"])
	}
}

func TestROI_ZeroActive_NonzeroCost(t *testing.T) {
	mq := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window": {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg": {
				{"developer": "alice@x.com", "total_cost_usd": float64(10.0)},
			},
			"active_agg":    {}, // zero active time
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if len(env.Developers) != 1 {
		t.Fatalf("expected 1 developer; got %d", len(env.Developers))
	}
	e := env.Developers[0]
	if ratio, _ := e["roi_ratio"].(float64); ratio != 0 {
		t.Errorf("roi_ratio = %v; want 0 (no active time)", e["roi_ratio"])
	}
	if net, _ := e["net_benefit_usd"].(float64); net >= 0 {
		t.Errorf("net_benefit_usd = %v; want < 0 (cost with no value)", e["net_benefit_usd"])
	}
}

func TestROI_MultiDeveloper_Ordering(t *testing.T) {
	mq := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window": {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg": {
				// The cost_agg query ORDERs BY total_cost_usd DESC — so the
				// first-seen developer in our iteration must be the bigger
				// spender.
				{"developer": "alice@x.com", "total_cost_usd": float64(100.0)},
				{"developer": "bob@x.com", "total_cost_usd": float64(10.0)},
			},
			"active_agg":    {},
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if len(env.Developers) != 2 {
		t.Fatalf("expected 2 developers; got %d", len(env.Developers))
	}
	if env.Developers[0]["developer"] != "alice@x.com" {
		t.Errorf("expected alice first (higher cost); got %v", env.Developers[0]["developer"])
	}
	if env.Developers[1]["developer"] != "bob@x.com" {
		t.Errorf("expected bob second; got %v", env.Developers[1]["developer"])
	}
}

// C2 — ROI gauge aggregation double-count regression
//
// If the gauge table has two datapoints at the same TimeUnix but with
// different session.id and the same Value=3600, after the collapse-then-sum
// pattern the effective active_seconds should be 3600 + 3600 = 7200 under
// different sessions (they are distinct sessions), which is 2 hours.
//
// The stronger collapse case is: two rows with SAME (session, repository,
// model, TimeUnix) and same Value=3600. The inner SELECT's max(Value) per
// group collapses them to a single 3600, and the outer sum yields 3600 →
// hours_active == 1.
//
// We test the latter: mock emits a pre-collapsed row (the SQL the tool
// emits uses the inner SELECT + max + sum); we stub that layer by returning
// a single active_seconds=3600 row for one dev. The *guarantee* being
// tested at the tool level is that the emitted SQL uses the collapse
// pattern — we assert the SQL shape and trust the DB to execute it.
func TestROI_GaugeDedupe_SameTimestamp(t *testing.T) {
	mq := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window": {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg": {
				{"developer": "alice@x.com", "total_cost_usd": float64(1.0)},
			},
			// Returned from the outer (collapsed + summed) query → 1h.
			"active_agg": {
				{"developer": "alice@x.com", "active_seconds": float64(3600)},
			},
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if len(env.Developers) != 1 {
		t.Fatalf("expected 1 developer; got %d", len(env.Developers))
	}
	if hrs, _ := env.Developers[0]["hours_active"].(float64); hrs != 1.0 {
		t.Errorf("hours_active = %v; want 1.0 (collapsed gauge)", env.Developers[0]["hours_active"])
	}
	// Assert the SQL structure is collapse-then-sum: inner GROUP BY by
	// (developer, session.id, repository, model, TimeUnix) with max(Value),
	// outer sum grouped by developer.
	foundCollapse := false
	for _, sql := range mq.sqlHistory {
		if strings.Contains(sql, "max(Value) AS v") &&
			strings.Contains(sql, "Attributes['session.id']") &&
			strings.Contains(sql, "TimeUnix") &&
			strings.Contains(sql, "sum(v) AS active_seconds") {
			foundCollapse = true
			break
		}
	}
	if !foundCollapse {
		t.Errorf("expected collapse-then-sum SQL shape; got %v", mq.sqlHistory)
	}
}

// BL-NEW-1 — Gauge de-dup robustness when session.id is missing.
//
// The gauge sub-query groups on session.id among other keys. When session.id
// is missing (”) the pre-fix SQL collapsed all such rows into a single
// bucket per timestamp/repository/model, under-counting active time. The
// fix wraps the group key in coalesce(nullIf(...,”), concat('ts:',TimeUnix))
// so rows missing session.id remain distinct per TimeUnix.
//
// The mock does not actually execute the SQL — we assert two things:
//  1. The emitted SQL contains the coalesce/nullIf pattern on session.id.
//  2. Given a post-collapse active_agg that reflects the correct DB
//     behavior (two rows with distinct TimeUnix survive → 2*3600 = 7200s),
//     the envelope surfaces 2.0 hours_active rather than 1.0.
func TestROI_GaugeDedupe_MissingSessionId(t *testing.T) {
	mq := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window": {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg": {
				{"developer": "alice@x.com", "total_cost_usd": float64(1.0)},
			},
			// Two distinct TimeUnix datapoints, both missing session.id, both
			// Value=3600. After the coalesce-on-TimeUnix synthetic key, they
			// remain distinct in the inner GROUP BY. The outer sum yields
			// 7200 seconds = 2 hours.
			"active_agg": {
				{"developer": "alice@x.com", "active_seconds": float64(7200)},
			},
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	cs, cleanup := testServer(t, mq.withStrictPurpose(t), adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}

	// SQL-shape: coalesce(nullIf(...,''), concat('ts:',toString(TimeUnix)))
	// on session.id so empty session.id rows don't collapse per-timestamp.
	foundCoalesce := false
	for _, sql := range mq.sqlHistory {
		if strings.Contains(sql, "otel_metrics_gauge") &&
			strings.Contains(sql, "coalesce(nullIf(Attributes['session.id'], '')") &&
			strings.Contains(sql, "concat('ts:', toString(TimeUnix))") {
			foundCoalesce = true
			break
		}
	}
	if !foundCoalesce {
		t.Errorf("expected coalesce/nullIf pattern on session.id in gauge SQL; got %v", mq.sqlHistory)
	}

	env := roiUnmarshal(t, toolText(res))
	if len(env.Developers) != 1 {
		t.Fatalf("expected 1 developer; got %d", len(env.Developers))
	}
	if hrs, _ := env.Developers[0]["hours_active"].(float64); hrs != 2.0 {
		t.Errorf("hours_active = %v; want 2.0 (two distinct TimeUnix preserved)", env.Developers[0]["hours_active"])
	}
}

// C1 — period_days_basis in envelope
func TestROI_PeriodDaysBasis_ElapsedInclusive(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if env.PeriodDaysBasis != "elapsed_days_inclusive" {
		t.Errorf("period_days_basis = %q; want elapsed_days_inclusive", env.PeriodDaysBasis)
	}
	if env.PeriodDays < 1 {
		t.Errorf("period_days = %d; want >= 1", env.PeriodDays)
	}
}

// BL-H3 — viewer with no data still gets a self-entry zeroed out.
func TestROI_ViewerZeroData_InjectsSelfEntry(t *testing.T) {
	mq := &mockQuerier{
		resultsByPurpose: map[string][]map[string]any{
			"window":        {{"start": "2026-04-01", "end": "2026-04-19", "period_days": float64(19)}},
			"cost_agg":      {},
			"active_agg":    {},
			"trend":         {},
			"by_model":      {},
			"by_repository": {},
		},
	}
	cs, cleanup := testServer(t, mq, viewerUser("viewer@test.com"))
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	env := roiUnmarshal(t, toolText(res))
	if len(env.Developers) != 1 {
		t.Fatalf("expected 1 injected self entry; got %d", len(env.Developers))
	}
	entry := env.Developers[0]
	if entry["developer"] != "viewer@test.com" {
		t.Errorf("expected viewer email as developer; got %v", entry["developer"])
	}
	if cost, _ := entry["total_cost_usd"].(float64); cost != 0 {
		t.Errorf("expected zero total_cost_usd; got %v", entry["total_cost_usd"])
	}
	if entry["roi_ratio"] != nil {
		t.Errorf("expected roi_ratio null; got %v", entry["roi_ratio"])
	}
}

func TestROI_SQLInjection_Developer(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"developer": "a'; DROP--"})
	// BL-NEW-2 / Sec-NEW-1: the filter lower()s the literal, so the
	// doubled-single-quote escape survives while the alpha chars drop to
	// lower case. Injection is still defanged.
	if !roiSQLContains(mq, "a''; drop--") {
		t.Errorf("expected escaped single quote (lower-cased); got %v", mq.sqlHistory)
	}
}

func TestROI_Unauthorized_NoAuth(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, nil)
	defer cleanup()

	res := callTool(t, cs, "report_developer_roi", nil)
	if !res.IsError {
		t.Error("expected error without auth")
	}
	if !strings.Contains(toolText(res), "unauthorized") {
		t.Errorf("expected unauthorized; got %q", toolText(res))
	}
}

func TestROI_RepositoryAll_NoFilter(t *testing.T) {
	mq := roiMock(t)
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "report_developer_roi", map[string]any{"repository": "all"})
	for _, sql := range mq.sqlHistory {
		if strings.Contains(sql, "Attributes['repository'] = ") {
			t.Errorf("expected no repository filter for 'all'; got %q", sql)
		}
	}
}

// --- cross-check ----------------------------------------------------------

// TestExtractColumn_EmptyIsEmptySlice ensures extractColumn returns a non-nil
// empty slice so JSON marshals to `[]` rather than `null`.
func TestExtractColumn_EmptyIsEmptySlice(t *testing.T) {
	out := extractColumn(nil, "k")
	if out == nil {
		t.Fatal("expected non-nil slice")
	}
	b, _ := json.Marshal(out)
	if string(b) != "[]" {
		t.Errorf("expected `[]`; got %q", string(b))
	}
}

// TestExecQuery_EmptyRows_MarshalsAsList ensures execQuery wraps a nil rows
// slice as `[]` in the JSON output (Nil-safety Obs-2).
func TestExecQuery_EmptyRows_MarshalsAsList(t *testing.T) {
	mq := &mockQuerier{results: nil}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "recent_logs", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", toolText(res))
	}
	body := toolText(res)
	if strings.TrimSpace(body) == "null" {
		t.Errorf("expected JSON list, not null; got %q", body)
	}
}

// --- schema + prompt cross-check regex -----------------------------------

// renderedArgsFromPromptText finds every "tool(arg=..., arg2=...)"-like
// fragment inside text and returns the keys. It is used by the prompt/tool
// schema cross-check test (CQ-H1 meta-test).
var reToolCall = regexp.MustCompile(`(?m)(\w+)\(([^)]*)\)`)
var reKwarg = regexp.MustCompile(`(\w+)=`)

// renderedToolArgs extracts every `ident(kw=..., kw2=...)` fragment from the
// text. It returns entries for ALL matches (regardless of whether the tool
// name is in the registry) — callers decide what to do with unknown tools.
// Historically this filtered unknowns silently; the meta-test (CQ-L3 /
// Test-NEW-MED-3) now opts in to loudly rejecting them.
//
// Prose like `developer(s)` matches the generic `(\w+)\(...)` pattern too —
// such matches have no `kw=` inside so Args ends up empty. Callers that
// want to ignore prose should check `len(Args) > 0 || toolSchemas[Tool]`
// before acting on an entry.
func renderedToolArgs(text string) []struct {
	Tool string
	Args []string
} {
	var out []struct {
		Tool string
		Args []string
	}
	for _, m := range reToolCall.FindAllStringSubmatch(text, -1) {
		tool := m[1]
		args := []string{}
		for _, km := range reKwarg.FindAllStringSubmatch(m[2], -1) {
			args = append(args, km[1])
		}
		sort.Strings(args)
		out = append(out, struct {
			Tool string
			Args []string
		}{tool, args})
	}
	return out
}

package mcp

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockQuerier returns canned results and records the last SQL executed.
type mockQuerier struct {
	results []map[string]any
	err     error
	lastSQL string
}

func (m *mockQuerier) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	m.lastSQL = query
	return m.results, m.err
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

func adminUser() *User  { return &User{Email: "admin@test.com", Role: RoleAdmin} }
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

// 15 admin + 6 viewer = 21 tools
func TestRegisterTools_Count(t *testing.T) {
	cs, cleanup := testServer(t, &mockQuerier{}, adminUser())
	defer cleanup()

	res, err := cs.ListTools(context.Background(), &sdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 21 {
		t.Errorf("tool count = %d; want 21", len(res.Tools))
		for _, tool := range res.Tools {
			t.Logf("  tool: %s", tool.Name)
		}
	}
}

// Admin tool succeeds with admin context
func TestAdminTool_ListTables_Success(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{{"name": "otel_logs"}}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	res := callTool(t, cs, "list_tables", nil)
	if res.IsError {
		t.Errorf("expected success; got error: %s", toolText(res))
	}
	if mq.lastSQL != "SHOW TABLES FROM observability" {
		t.Errorf("SQL = %q; want SHOW TABLES FROM observability", mq.lastSQL)
	}
}

// Admin tool blocked for viewer
func TestAdminTool_Forbidden_ForViewer(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("user@test.com"))
	defer cleanup()

	res := callTool(t, cs, "list_tables", nil)
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

	res := callTool(t, cs, "list_tables", nil)
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

// token_usage_detailed with optional email filter
func TestAdminTool_TokenUsageDetailed_WithEmail(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "token_usage_detailed", map[string]any{"user_email": "alice@test.com"})
	if !strings.Contains(mq.lastSQL, "alice@test.com") {
		t.Errorf("expected email filter in SQL; got %q", mq.lastSQL)
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

// Viewer tool filters by viewer email
func TestViewerTool_MyTokenUsage(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("joao@test.com"))
	defer cleanup()

	callTool(t, cs, "my_token_usage", nil)
	if !strings.Contains(mq.lastSQL, "joao@test.com") {
		t.Errorf("expected viewer email in SQL; got %q", mq.lastSQL)
	}
}

// my_report includes user email and runs all 4 sub-queries
func TestViewerTool_MyReport(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, viewerUser("maria@test.com"))
	defer cleanup()

	res := callTool(t, cs, "my_report", map[string]any{"period": "week"})
	text := toolText(res)
	if text == "" {
		t.Fatal("expected content in my_report response")
	}
	if !strings.Contains(text, "maria@test.com") {
		t.Errorf("expected viewer email in report; got %q", text)
	}
}

// Admin can also call viewer tools (filtered by admin's own email)
func TestViewerTool_AccessibleByAdmin(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, adminUser())
	defer cleanup()

	callTool(t, cs, "my_token_usage", nil)
	if !strings.Contains(mq.lastSQL, "admin@test.com") {
		t.Errorf("expected admin email in SQL; got %q", mq.lastSQL)
	}
}

// Viewer tool blocked without auth
func TestViewerTool_Unauthorized_NoAuth(t *testing.T) {
	mq := &mockQuerier{results: []map[string]any{}}
	cs, cleanup := testServer(t, mq, nil)
	defer cleanup()

	res := callTool(t, cs, "my_token_usage", nil)
	if !res.IsError {
		t.Error("expected error without auth")
	}
}

// SQL injection on user_email is escaped
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
		period string
		want   string
	}{
		{"day", "toStartOfDay(now())"},
		{"week", "toStartOfWeek(now())"},
		{"month", "toStartOfMonth(now())"},
		{"invalid", "toStartOfDay(now())"},
	}
	for _, tc := range tests {
		if got := periodToInterval(tc.period); got != tc.want {
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

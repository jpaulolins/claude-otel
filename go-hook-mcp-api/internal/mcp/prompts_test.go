package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptsTestServer wires a server that has BOTH the tools and the prompts
// registered (so the list-prompts middleware kicks in). Mirrors the shape of
// testServer (used by tools_test.go) but specific to prompt tests, because
// testServer only registers tools.
func promptsTestServer(t *testing.T, user *User) (*sdk.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()

	srv := sdk.NewServer(&sdk.Implementation{Name: "prompts-test", Version: "0.0.1"}, nil)
	mq := &mockQuerier{}
	// Register tools so the schema registry (toolSchemas) is populated; the
	// prompt cross-check test depends on it.
	RegisterTools(srv, mq)
	adminOnly := RegisterPrompts(srv, mq)
	// Register the list-prompts role filter BEFORE the user-injection
	// middleware, because the last middleware registered is the outermost
	// (wraps all previously registered ones). In the production server the
	// user-injection middleware is added in ServeStdio* AFTER NewServer, so
	// this mirrors that ordering.
	registerListPromptsRoleFilter(srv, adminOnly)
	if user != nil {
		u := *user
		srv.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
			return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
				ctx = context.WithValue(ctx, userContextKey{}, u)
				return next(ctx, method, req)
			}
		})
	}

	clientT, serverT := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "prompts-test-client", Version: "0.0.1"}, nil)
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

// getPromptText calls cs.GetPrompt and returns the text of the first message.
// Fails the test on transport error.
func getPromptText(t *testing.T, cs *sdk.ClientSession, name string, args map[string]string) string {
	t.Helper()
	res, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("GetPrompt(%s): %v", name, err)
	}
	if res == nil || len(res.Messages) == 0 || res.Messages[0] == nil {
		t.Fatalf("GetPrompt(%s): empty messages", name)
		return ""
	}
	tc, ok := res.Messages[0].Content.(*sdk.TextContent)
	if !ok {
		t.Fatalf("GetPrompt(%s): first message content is not TextContent (got %T)", name, res.Messages[0].Content)
	}
	return tc.Text
}

// listPrompts returns every prompt name visible to the given session.
func listPromptNames(t *testing.T, cs *sdk.ClientSession) []string {
	t.Helper()
	res, err := cs.ListPrompts(context.Background(), &sdk.ListPromptsParams{})
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	names := make([]string, 0, len(res.Prompts))
	for _, p := range res.Prompts {
		names = append(names, p.Name)
	}
	return names
}

// The full set of prompt names we expect the server to register.
var allPromptNames = []string{
	"daily_agent_standup",
	"weekly_activity_digest",
	"token_and_cost_week",
	"token_and_cost_month",
	"cost_drilldown_repository",
	"roi_executive_snapshot",
	"compare_developers_cost",
}

// --- ListPrompts ----------------------------------------------------------

func TestListPrompts_Admin_Sees7(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	names := listPromptNames(t, cs)
	if len(names) != 7 {
		t.Errorf("admin should see 7 prompts; got %d: %v", len(names), names)
	}
	set := toSet(names)
	for _, want := range allPromptNames {
		if !set[want] {
			t.Errorf("admin list missing %q; got %v", want, names)
		}
	}
}

func TestListPrompts_Viewer_Sees6(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("v@test.com"))
	defer cleanup()

	names := listPromptNames(t, cs)
	if len(names) != 6 {
		t.Errorf("viewer should see 6 prompts; got %d: %v", len(names), names)
	}
	set := toSet(names)
	if set["compare_developers_cost"] {
		t.Errorf("viewer must not see compare_developers_cost; got %v", names)
	}
}

func TestListPrompts_Unauthenticated_Empty(t *testing.T) {
	cs, cleanup := promptsTestServer(t, nil)
	defer cleanup()

	res, err := cs.ListPrompts(context.Background(), &sdk.ListPromptsParams{})
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(res.Prompts) != 0 {
		t.Errorf("unauthenticated should see 0 prompts; got %d", len(res.Prompts))
	}
}

// --- Per-prompt: default args + repository + viewer/admin scoping ---------

// assertViewerScopedOnce runs the Anti-Pattern-6 guard: exactly one
// `developer="..."` fragment is present, it is the viewer's email, and there
// is no stray `developer="all"` or other email leaked into the text.
//
// Test-NEW-MED-1: an empty text silently satisfies every `strings.Contains`
// check below with `got=0` which masks a short-circuited renderSafe. Fail
// fast when text is blank so we never rubber-stamp empty output.
func assertViewerScopedOnce(t *testing.T, text, viewerEmail string) {
	t.Helper()
	if strings.TrimSpace(text) == "" {
		t.Fatal("rendered text is empty — renderSafe short-circuited")
	}
	count := strings.Count(text, `developer="`)
	if count != 1 {
		t.Errorf("expected exactly one developer=\" fragment; got %d in %q", count, text)
	}
	if !strings.Contains(text, `developer="`+viewerEmail+`"`) {
		t.Errorf("expected developer=%q; got %q", viewerEmail, text)
	}
	if strings.Contains(text, `developer="all"`) {
		t.Errorf("viewer must not see developer=\"all\"; got %q", text)
	}
}

func TestPrompt_DailyAgentStandup_DefaultArgs(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "daily_agent_standup", nil)
	if !strings.Contains(text, "report_activity_timeline") {
		t.Errorf("expected report_activity_timeline in text; got %q", text)
	}
	if !strings.Contains(text, `since="24h"`) {
		t.Errorf("expected since=24h; got %q", text)
	}
	if !strings.Contains(text, `source="agent"`) {
		t.Errorf("expected source=agent; got %q", text)
	}
	if !strings.Contains(text, `developer="all"`) {
		t.Errorf("admin default should render developer=\"all\"; got %q", text)
	}
}

func TestPrompt_DailyAgentStandup_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "daily_agent_standup", map[string]string{"repository": "api"})
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
}

func TestPrompt_DailyAgentStandup_ViewerScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	text := getPromptText(t, cs, "daily_agent_standup", nil)
	assertViewerScopedOnce(t, text, "viewer@test.com")
	if !strings.Contains(text, "scope: self") {
		t.Errorf("expected scope clarification; got %q", text)
	}
}

func TestPrompt_DailyAgentStandup_AdminScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "daily_agent_standup", map[string]string{"developer": "alice@x.com"})
	if !strings.Contains(text, `developer="alice@x.com"`) {
		t.Errorf("expected admin-supplied developer; got %q", text)
	}
}

func TestPrompt_WeeklyActivityDigest_DefaultArgs(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "weekly_activity_digest", nil)
	if !strings.Contains(text, "report_activity_timeline") {
		t.Errorf("expected tool name; got %q", text)
	}
	if !strings.Contains(text, `since="7d"`) {
		t.Errorf("expected since=7d; got %q", text)
	}
	if !strings.Contains(text, `developer="all"`) {
		t.Errorf("admin default all; got %q", text)
	}
}

func TestPrompt_WeeklyActivityDigest_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "weekly_activity_digest", map[string]string{"repository": "api"})
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
}

func TestPrompt_WeeklyActivityDigest_ViewerScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	text := getPromptText(t, cs, "weekly_activity_digest", nil)
	assertViewerScopedOnce(t, text, "viewer@test.com")
}

func TestPrompt_WeeklyActivityDigest_AdminScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "weekly_activity_digest", map[string]string{"developer": "bob@x.com"})
	if !strings.Contains(text, `developer="bob@x.com"`) {
		t.Errorf("expected admin-supplied developer; got %q", text)
	}
}

func TestPrompt_WeeklyActivityDigest_GroupId(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "weekly_activity_digest", map[string]string{"group_id": "org_42"})
	if !strings.Contains(text, `group_id="org_42"`) {
		t.Errorf("expected group_id=org_42 in text; got %q", text)
	}
}

// BL-H5 mirror: "all" sentinel means no filter — prompt should not render it.
func TestPrompt_WeeklyActivityDigest_GroupIdAll_Treated_AsNoFilter(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "weekly_activity_digest", map[string]string{"group_id": "all"})
	if strings.Contains(text, `group_id="all"`) {
		t.Errorf("expected no group_id=\"all\" fragment; got %q", text)
	}
	if strings.Contains(text, `group_id="`) {
		t.Errorf("expected no group_id fragment at all; got %q", text)
	}
}

func TestPrompt_TokenAndCostWeek_DefaultArgs(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_week", nil)
	if !strings.Contains(text, "report_token_usage") {
		t.Errorf("expected tool name; got %q", text)
	}
	if !strings.Contains(text, `period="week"`) {
		t.Errorf("expected period=week; got %q", text)
	}
	if !strings.Contains(text, `developer="all"`) {
		t.Errorf("admin default all; got %q", text)
	}
	// BL-H1: the token-usage wording should not instruct the agent to naively
	// sum all four token columns.
	if strings.Contains(text, "sum of tokens") {
		t.Errorf("prompt still uses legacy 'sum of tokens' wording; got %q", text)
	}
	if !strings.Contains(text, "billable_tokens = input_tokens + output_tokens") {
		t.Errorf("expected billable_tokens guidance; got %q", text)
	}
	if !strings.Contains(text, "cache_read represents reused context") {
		t.Errorf("expected cache_read guidance; got %q", text)
	}
}

func TestPrompt_TokenAndCostWeek_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_week", map[string]string{"repository": "api"})
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
}

func TestPrompt_TokenAndCostWeek_ViewerScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_week", nil)
	assertViewerScopedOnce(t, text, "viewer@test.com")
}

func TestPrompt_TokenAndCostWeek_AdminScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_week", map[string]string{"developer": "carol@x.com"})
	if !strings.Contains(text, `developer="carol@x.com"`) {
		t.Errorf("expected admin-supplied developer; got %q", text)
	}
}

func TestPrompt_TokenAndCostMonth_DefaultArgs(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_month", nil)
	if !strings.Contains(text, "report_token_usage") {
		t.Errorf("expected tool name; got %q", text)
	}
	if !strings.Contains(text, `period="month"`) {
		t.Errorf("expected period=month; got %q", text)
	}
	// BL-H1: the token-usage wording should not instruct the agent to naively
	// sum all four token columns.
	if strings.Contains(text, "sum of tokens") {
		t.Errorf("prompt still uses legacy 'sum of tokens' wording; got %q", text)
	}
	if !strings.Contains(text, "billable_tokens = input_tokens + output_tokens") {
		t.Errorf("expected billable_tokens guidance; got %q", text)
	}
}

func TestPrompt_TokenAndCostMonth_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_month", map[string]string{"repository": "api"})
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
}

func TestPrompt_TokenAndCostMonth_ViewerScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_month", nil)
	assertViewerScopedOnce(t, text, "viewer@test.com")
}

func TestPrompt_TokenAndCostMonth_AdminScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_month", map[string]string{"developer": "dave@x.com"})
	if !strings.Contains(text, `developer="dave@x.com"`) {
		t.Errorf("expected admin-supplied developer; got %q", text)
	}
}

func TestPrompt_TokenAndCostMonth_CustomRange(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "token_and_cost_month", map[string]string{
		"date_start": "2026-04-01",
		"date_end":   "2026-04-07",
	})
	if !strings.Contains(text, `date_start="2026-04-01"`) {
		t.Errorf("expected date_start; got %q", text)
	}
	if !strings.Contains(text, `date_end="2026-04-07"`) {
		t.Errorf("expected date_end; got %q", text)
	}
	if strings.Contains(text, `period="month"`) {
		t.Errorf("period should be ignored when date range given; got %q", text)
	}
}

// Renamed per spec: this validates that the admin variant of
// cost_drilldown_repository instructs the agent to additionally correlate
// session-level cost.
func TestPrompt_CostDrilldownRepository_AdminCorrelatesSession(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "cost_drilldown_repository", map[string]string{"repository": "api"})
	if !strings.Contains(text, "report_token_usage") {
		t.Errorf("expected report_token_usage; got %q", text)
	}
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
	if !strings.Contains(text, "cost_by_session") {
		t.Errorf("admin should also be instructed to call cost_by_session; got %q", text)
	}
}

func TestPrompt_CostDrilldownRepository_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "cost_drilldown_repository", map[string]string{"repository": "backend"})
	if !strings.Contains(text, `repository="backend"`) {
		t.Errorf("expected repository=backend; got %q", text)
	}
}

func TestPrompt_CostDrilldownRepository_ViewerScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	text := getPromptText(t, cs, "cost_drilldown_repository", map[string]string{"repository": "api"})
	assertViewerScopedOnce(t, text, "viewer@test.com")
	// Viewer must NOT be asked to call cost_by_session (admin-only tool).
	if strings.Contains(text, "cost_by_session") {
		t.Errorf("viewer must not be instructed to call cost_by_session; got %q", text)
	}
}

func TestPrompt_CostDrilldownRepository_AdminScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "cost_drilldown_repository", map[string]string{
		"repository": "api",
		"developer":  "eve@x.com",
	})
	if !strings.Contains(text, `developer="eve@x.com"`) {
		t.Errorf("expected admin-supplied developer; got %q", text)
	}
}

func TestPrompt_CostDrilldownRepository_RepositoryRequired(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	_, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name:      "cost_drilldown_repository",
		Arguments: map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error when repository is missing")
	}
	if !strings.Contains(err.Error(), "repository") {
		t.Errorf("expected 'repository' in error; got %v", err)
	}
}

// Additional regression: a whitespace-only repository should be treated as
// missing, not passed through.
func TestPrompt_CostDrilldownRepository_WhitespaceRepository_Rejected(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	_, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name:      "cost_drilldown_repository",
		Arguments: map[string]string{"repository": "   "},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only repository")
	}
}

func TestPrompt_RoiExecutiveSnapshot_DefaultArgs(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "roi_executive_snapshot", nil)
	if !strings.Contains(text, "report_developer_roi") {
		t.Errorf("expected report_developer_roi; got %q", text)
	}
	if !strings.Contains(text, `period="month"`) {
		t.Errorf("expected default period=month; got %q", text)
	}
}

func TestPrompt_RoiExecutiveSnapshot_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "roi_executive_snapshot", map[string]string{"repository": "api"})
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
}

func TestPrompt_RoiExecutiveSnapshot_ViewerScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	text := getPromptText(t, cs, "roi_executive_snapshot", nil)
	assertViewerScopedOnce(t, text, "viewer@test.com")
}

func TestPrompt_RoiExecutiveSnapshot_AdminScoped(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "roi_executive_snapshot", map[string]string{"developer": "frank@x.com"})
	if !strings.Contains(text, `developer="frank@x.com"`) {
		t.Errorf("expected admin-supplied developer; got %q", text)
	}
}

func TestPrompt_RoiExecutiveSnapshot_DateRangeOverridesPeriod(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "roi_executive_snapshot", map[string]string{
		"period":     "week",
		"date_start": "2026-04-01",
		"date_end":   "2026-04-07",
	})
	if !strings.Contains(text, `date_start="2026-04-01"`) {
		t.Errorf("expected date_start; got %q", text)
	}
	if !strings.Contains(text, `date_end="2026-04-07"`) {
		t.Errorf("expected date_end; got %q", text)
	}
	if strings.Contains(text, `period="week"`) {
		t.Errorf("period should be ignored when date range given; got %q", text)
	}
	if !strings.Contains(text, "IGNORE period") {
		t.Errorf("expected IGNORE period instruction; got %q", text)
	}
}

func TestPrompt_CompareDevelopersCost_DefaultArgs(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "compare_developers_cost", nil)
	if !strings.Contains(text, "report_developer_roi") {
		t.Errorf("expected report_developer_roi; got %q", text)
	}
	if !strings.Contains(text, `period="month"`) {
		t.Errorf("expected default period=month; got %q", text)
	}
	// No developer arg should be passed in the rendered call.
	if strings.Contains(text, "developer=\"") {
		t.Errorf("compare prompt must not pass a developer filter; got %q", text)
	}
}

func TestPrompt_CompareDevelopersCost_WithRepository(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "compare_developers_cost", map[string]string{"repository": "api"})
	if !strings.Contains(text, `repository="api"`) {
		t.Errorf("expected repository=api; got %q", text)
	}
}

func TestPrompt_CompareDevelopersCost_AdminScoped(t *testing.T) {
	// compare_developers_cost is admin-only; "admin scoped" means admin can
	// successfully fetch it. Viewer coverage is in _Forbidden_Viewer below.
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	text := getPromptText(t, cs, "compare_developers_cost", map[string]string{
		"period": "week",
	})
	if !strings.Contains(text, `period="week"`) {
		t.Errorf("expected period=week; got %q", text)
	}
}

func TestPrompt_CompareDevelopersCost_Forbidden_Viewer(t *testing.T) {
	cs, cleanup := promptsTestServer(t, viewerUser("viewer@test.com"))
	defer cleanup()

	_, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name: "compare_developers_cost",
	})
	if err == nil {
		t.Fatal("expected forbidden error for viewer")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		t.Errorf("expected 'forbidden' in error; got %v", err)
	}
}

// --- Unauthorized: table-driven across all 7 prompts ----------------------

func TestPrompt_Unauthorized_NoAuth(t *testing.T) {
	cs, cleanup := promptsTestServer(t, nil)
	defer cleanup()

	// Build minimal valid args so we don't hit argument-required checks before
	// the auth check (cost_drilldown_repository needs repository).
	argsByPrompt := map[string]map[string]string{
		"daily_agent_standup":       nil,
		"weekly_activity_digest":    nil,
		"token_and_cost_week":       nil,
		"token_and_cost_month":      nil,
		"cost_drilldown_repository": {"repository": "api"},
		"roi_executive_snapshot":    nil,
		"compare_developers_cost":   nil,
	}

	for _, name := range allPromptNames {
		t.Run(name, func(t *testing.T) {
			_, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{
				Name:      name,
				Arguments: argsByPrompt[name],
			})
			if err == nil {
				t.Fatalf("expected error for unauthenticated call to %s", name)
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "unauthorized") && !strings.Contains(msg, "forbidden") {
				t.Errorf("expected unauthorized/forbidden in error; got %v", err)
			}
		})
	}
}

// --- Prompt/tool contract cross-check (CQ-H1 meta-test) ------------------

// TestPrompts_AllRenderedArgs_ExistInToolSchema iterates every prompt, renders
// it (with admin context so "developer" and other admin-facing args appear),
// captures every rendered `tool(kw=...)` fragment, and asserts every kw
// appears in the matching tool's InputSchema properties.
//
// Test-NEW-MED-3: we run TWO passes per prompt — once with the minimum
// required args (default path) and once with a populated args set
// (with-args path). Both paths must resolve to known tool names and only
// use kwargs present in the schema.
//
// CQ-L3: rather than silently skipping unknown tool names, we treat any
// `word(kw=..., kw2=...)` fragment (i.e. something that clearly looks
// like a tool call because it has kwargs) with an unknown word as a
// typo / registry-gap. Fragments without kwargs are prose (e.g.
// `developer(s)` narrative parentheses).
func TestPrompts_AllRenderedArgs_ExistInToolSchema(t *testing.T) {
	cs, cleanup := promptsTestServer(t, adminUser())
	defer cleanup()

	// Two scenarios per prompt: "default" and "with-args". cost_drilldown_repository
	// needs `repository` even in the default path, per its Required=true schema.
	type scenario struct {
		label string
		args  map[string]string
	}
	perPrompt := []struct {
		name      string
		scenarios []scenario
	}{
		{
			name: "daily_agent_standup",
			scenarios: []scenario{
				{"default", map[string]string{}},
				{"with-args", map[string]string{"developer": "alice@x.com", "repository": "api"}},
			},
		},
		{
			name: "weekly_activity_digest",
			scenarios: []scenario{
				{"default", map[string]string{}},
				{"with-args", map[string]string{"developer": "alice@x.com", "repository": "api", "group_id": "org_1"}},
			},
		},
		{
			name: "token_and_cost_week",
			scenarios: []scenario{
				{"default", map[string]string{}},
				{"with-args", map[string]string{"developer": "alice@x.com", "repository": "api"}},
			},
		},
		{
			name: "token_and_cost_month",
			scenarios: []scenario{
				{"default", map[string]string{}},
				{"with-args", map[string]string{"developer": "alice@x.com", "repository": "api", "date_start": "2026-04-01", "date_end": "2026-04-07"}},
			},
		},
		{
			name: "cost_drilldown_repository",
			scenarios: []scenario{
				// Required arg must be present even on the "default" path.
				{"default", map[string]string{"repository": "api"}},
				{"with-args", map[string]string{"developer": "alice@x.com", "repository": "api"}},
			},
		},
		{
			name: "roi_executive_snapshot",
			scenarios: []scenario{
				{"default", map[string]string{}},
				{"with-args", map[string]string{"developer": "alice@x.com", "repository": "api", "date_start": "2026-04-01", "date_end": "2026-04-07"}},
			},
		},
		{
			name: "compare_developers_cost",
			scenarios: []scenario{
				{"default", map[string]string{}},
				{"with-args", map[string]string{"repository": "api", "date_start": "2026-04-01", "date_end": "2026-04-07"}},
			},
		},
	}

	for _, p := range perPrompt {
		for _, sc := range p.scenarios {
			t.Run(p.name+"/"+sc.label, func(t *testing.T) {
				text := getPromptText(t, cs, p.name, sc.args)
				for _, tc := range renderedToolArgs(text) {
					schema, ok := toolSchemas[tc.Tool]
					if !ok {
						// CQ-L3: if this fragment has kwargs it looks like a
						// tool call but points at something not in the
						// registry — fail the test so typos are caught.
						// Fragments without kwargs are prose (e.g. narrative
						// `developer(s)` parentheses) and are ignored.
						if len(tc.Args) > 0 {
							t.Errorf("prompt %q renders unknown tool %q (args=%v)", p.name, tc.Tool, tc.Args)
						}
						continue
					}
					props := map[string]*jsonschema.Schema{}
					if schema != nil {
						props = schema.Properties
					}
					for _, a := range tc.Args {
						if _, hasArg := props[a]; !hasArg {
							t.Errorf("tool %q rendered in prompt %q uses arg %q not in its schema (have: %v)",
								tc.Tool, p.name, a, propNames(props))
						}
					}
				}
			})
		}
	}
}

func propNames(m map[string]*jsonschema.Schema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- helpers --------------------------------------------------------------

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

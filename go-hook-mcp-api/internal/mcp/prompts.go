package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptScope is the access scope for a prompt.
type promptScope int

const (
	// scopeAuthenticated requires any authenticated user (admin or viewer).
	// Viewer callers have their data automatically narrowed to self.
	scopeAuthenticated promptScope = iota
	// scopeAdmin requires an admin user. Viewer callers are rejected.
	scopeAdmin
)

// promptSpec defines metadata and rendering logic for a single MCP prompt.
// Handlers do not execute queries — prompts only emit text instructions the
// agent will later use to call the appropriate report_* tool.
type promptSpec struct {
	name        string
	title       string
	description string
	scope       promptScope
	args        []*sdk.PromptArgument
	render      func(u User, args map[string]string) string
}

// handler returns the SDK PromptHandler bound to this spec. It enforces the
// declared scope, auto-scopes the `developer` argument for viewers, and emits
// a single user-role PromptMessage with the rendered text.
func (p *promptSpec) handler() sdk.PromptHandler {
	return func(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
		var u User
		var err error
		if p.scope == scopeAdmin {
			if err = requireAdmin(ctx); err != nil {
				return nil, err
			}
			u, _ = UserFromContext(ctx)
		} else {
			u, err = requireAuth(ctx)
			if err != nil {
				return nil, err
			}
		}

		args := map[string]string{}
		if req != nil && req.Params != nil && req.Params.Arguments != nil {
			maps.Copy(args, req.Params.Arguments)
		}

		// Viewer scoping: force developer=self so the rendered text never
		// leaks other developers' identities.
		if !u.IsAdmin() {
			args["developer"] = u.Email
		}

		// Per-prompt validation & text rendering.
		text, err := p.renderSafe(u, args)
		if err != nil {
			return nil, err
		}

		return &sdk.GetPromptResult{
			Description: p.description,
			Messages: []*sdk.PromptMessage{
				{
					Role:    "user",
					Content: &sdk.TextContent{Text: text},
				},
			},
		}, nil
	}
}

// renderSafe is a thin wrapper that lets specific prompts return an error from
// their render function by embedding a sentinel prefix. For simplicity the 7
// prompts call helpers that either return text or short-circuit through a
// per-prompt wrapper (see cost_drilldown_repository below).
func (p *promptSpec) renderSafe(u User, args map[string]string) (string, error) {
	// Repository-required guard for cost_drilldown_repository.
	if p.name == "cost_drilldown_repository" {
		if strings.TrimSpace(args["repository"]) == "" {
			return "", errors.New("repository argument is required")
		}
	}
	return p.render(u, args), nil
}

// RegisterPrompts registers every prompt exposed by the server and returns
// the set of admin-only prompt names. Callers pair the returned set with
// registerListPromptsRoleFilter so the list-prompts middleware can hide
// admin-only entries from non-admin callers.
//
// q is unused today (prompts don't execute queries) and kept as a parameter
// for symmetry with RegisterTools and for future prompts that may need data
// access.
func RegisterPrompts(s *sdk.Server, _ Querier) map[string]bool {
	adminSet := map[string]bool{}

	specs := []*promptSpec{
		dailyAgentStandupSpec(),
		weeklyActivityDigestSpec(),
		tokenAndCostWeekSpec(),
		tokenAndCostMonthSpec(),
		costDrilldownRepositorySpec(),
		roiExecutiveSnapshotSpec(),
		compareDevelopersCostSpec(),
	}

	for _, spec := range specs {
		if spec.scope == scopeAdmin {
			adminSet[spec.name] = true
		}
		s.AddPrompt(&sdk.Prompt{
			Name:        spec.name,
			Title:       spec.title,
			Description: spec.description,
			Arguments:   spec.args,
		}, spec.handler())
	}
	return adminSet
}

// registerListPromptsRoleFilter installs a receiving middleware that filters
// the prompts/list response based on the caller's role:
//   - unauthenticated → empty list (prompts are not advertised)
//   - viewer          → prompts in `adminOnly` stripped
//   - admin           → full list
//
// Defense-in-depth: each prompt handler also re-checks the role, so hiding a
// prompt from listing is only the first layer — direct GetPrompt calls on
// admin-only prompts still fail for viewers.
func registerListPromptsRoleFilter(s *sdk.Server, adminOnly map[string]bool) {
	s.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil || method != "prompts/list" {
				return res, err
			}
			lpr, ok := res.(*sdk.ListPromptsResult)
			if !ok || lpr == nil {
				return res, nil
			}
			u, authed := UserFromContext(ctx)
			if !authed {
				return &sdk.ListPromptsResult{Prompts: []*sdk.Prompt{}}, nil
			}
			if u.IsAdmin() {
				return lpr, nil
			}
			filtered := make([]*sdk.Prompt, 0, len(lpr.Prompts))
			for _, p := range lpr.Prompts {
				if p == nil {
					continue
				}
				if adminOnly[p.Name] {
					continue
				}
				filtered = append(filtered, p)
			}
			return &sdk.ListPromptsResult{
				NextCursor: lpr.NextCursor,
				Prompts:    filtered,
			}, nil
		}
	})
}

// --- Per-prompt specs ------------------------------------------------------

func dailyAgentStandupSpec() *promptSpec {
	return &promptSpec{
		name:        "daily_agent_standup",
		title:       "Daily Agent Standup (last 24h)",
		description: "Summarize the last 24 hours of Claude Code agent activity as a standup with Done / In Progress / Attention sections.",
		scope:       scopeAuthenticated,
		args: []*sdk.PromptArgument{
			{Name: "developer", Description: "Optional developer email (admin only; viewer is always scoped to self).", Required: false},
			{Name: "repository", Description: "Optional repository basename to narrow the timeline.", Required: false},
		},
		render: func(u User, args map[string]string) string {
			developerLine, scopeNote := developerCallLine(u, args)
			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString("Produce a daily standup from Claude Code agent hook activity over the last 24 hours.\n\n")
			b.WriteString("Instructions:\n")
			b.WriteString("Call the following tool exactly once:\n")
			b.WriteString(fmt.Sprintf("  report_activity_timeline(since=\"24h\", source=\"agent\", %s%s)\n",
				developerLine, repositoryArg(args)))
			if scopeNote != "" {
				b.WriteString(scopeNote + "\n")
			}
			b.WriteString("\nOutput format:\n")
			b.WriteString("Render three English sections:\n")
			b.WriteString("  - **Done**: sessions that completed without errors.\n")
			b.WriteString("  - **In Progress**: sessions still active or recently updated.\n")
			b.WriteString("  - **Attention**: sessions with errors, timeouts, or anomalies.\n")
			b.WriteString("For each entry list: session id, repository, tools used, event count, and first/last seen timestamps.\n")
			return b.String()
		},
	}
}

func weeklyActivityDigestSpec() *promptSpec {
	return &promptSpec{
		name:        "weekly_activity_digest",
		title:       "Weekly Activity Digest (last 7 days)",
		description: "Summarize the last 7 days of Claude Code activity: per-session timeline, activity peaks, and top repositories.",
		scope:       scopeAuthenticated,
		args: []*sdk.PromptArgument{
			{Name: "developer", Description: "Optional developer email (admin only; viewer is always scoped to self).", Required: false},
			{Name: "repository", Description: "Optional repository basename filter.", Required: false},
			{Name: "group_id", Description: "Optional organization.id filter (sentinel \"all\" treated as no filter).", Required: false},
		},
		render: func(u User, args map[string]string) string {
			developerLine, scopeNote := developerCallLine(u, args)
			call := fmt.Sprintf("report_activity_timeline(since=\"7d\", source=\"agent\", %s%s",
				developerLine, repositoryArg(args))
			if g := strings.TrimSpace(args["group_id"]); g != "" && g != "all" {
				call += fmt.Sprintf(", group_id=\"%s\"", g)
			}
			call += ", max_items=1000)"

			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString("Produce a weekly digest of Claude Code activity for the last 7 days.\n\n")
			b.WriteString("Instructions:\n")
			b.WriteString("Call the following tool exactly once:\n")
			b.WriteString("  " + call + "\n")
			if scopeNote != "" {
				b.WriteString(scopeNote + "\n")
			}
			b.WriteString("\nOutput format:\n")
			b.WriteString("Render the digest in English with these parts:\n")
			b.WriteString("  - **Timeline per session**: one bullet per session (id, repository, tools, event count, first/last seen).\n")
			b.WriteString("  - **Activity peaks**: highlight the 2-3 busiest time windows (day or hour buckets).\n")
			b.WriteString("  - **Top repositories**: list the 3-5 most active repositories with event counts.\n")
			return b.String()
		},
	}
}

func tokenAndCostWeekSpec() *promptSpec {
	return &promptSpec{
		name:        "token_and_cost_week",
		title:       "Token & Cost (this week)",
		description: "Token usage and USD cost for the current week, broken down by developer and model.",
		scope:       scopeAuthenticated,
		args: []*sdk.PromptArgument{
			{Name: "developer", Description: "Optional developer email (admin only; viewer is always scoped to self).", Required: false},
			{Name: "repository", Description: "Optional repository basename filter.", Required: false},
		},
		render: func(u User, args map[string]string) string {
			developerLine, scopeNote := developerCallLine(u, args)
			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString("Report token usage and USD cost for the current week.\n\n")
			b.WriteString("Instructions:\n")
			b.WriteString("Call the following tool exactly once:\n")
			b.WriteString(fmt.Sprintf("  report_token_usage(period=\"week\", %s%s)\n",
				developerLine, repositoryArg(args)))
			if scopeNote != "" {
				b.WriteString(scopeNote + "\n")
			}
			b.WriteString("\nOutput format:\n")
			b.WriteString("Render a Markdown table with one row per developer x model, columns: developer, model, input_tokens, output_tokens, cache_read, cache_creation, cost_usd.\n")
			b.WriteString("Report `billable_tokens = input_tokens + output_tokens` and show `cache_read` and `cache_creation` as separate metrics (cache_read represents reused context, not fresh work). Include cost_usd and 2-3 short highlights (most expensive model, biggest spender, any outliers).\n")
			return b.String()
		},
	}
}

func tokenAndCostMonthSpec() *promptSpec {
	return &promptSpec{
		name:        "token_and_cost_month",
		title:       "Token & Cost (this month or custom range)",
		description: "Token usage and USD cost for the current month (or a custom date range), broken down by developer and model.",
		scope:       scopeAuthenticated,
		args: []*sdk.PromptArgument{
			{Name: "developer", Description: "Optional developer email (admin only; viewer is always scoped to self).", Required: false},
			{Name: "repository", Description: "Optional repository basename filter.", Required: false},
			{Name: "date_start", Description: "Optional inclusive start date (YYYY-MM-DD). Must be paired with date_end.", Required: false},
			{Name: "date_end", Description: "Optional inclusive end date (YYYY-MM-DD). Must be paired with date_start.", Required: false},
		},
		render: func(u User, args map[string]string) string {
			developerLine, scopeNote := developerCallLine(u, args)

			ds := strings.TrimSpace(args["date_start"])
			de := strings.TrimSpace(args["date_end"])

			var call string
			if ds != "" && de != "" {
				call = fmt.Sprintf("report_token_usage(date_start=\"%s\", date_end=\"%s\", %s%s)",
					ds, de, developerLine, repositoryArg(args))
			} else {
				call = fmt.Sprintf("report_token_usage(period=\"month\", %s%s)",
					developerLine, repositoryArg(args))
			}

			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString("Report token usage and USD cost for the current month, or for a custom date range when provided.\n\n")
			b.WriteString("Instructions:\n")
			if ds != "" && de != "" {
				b.WriteString(fmt.Sprintf("A custom range is set (date_start=\"%s\", date_end=\"%s\"); use it and IGNORE period.\n", ds, de))
			} else {
				b.WriteString("No custom range provided; use period=\"month\".\n")
			}
			b.WriteString("Call the following tool exactly once:\n")
			b.WriteString("  " + call + "\n")
			if scopeNote != "" {
				b.WriteString(scopeNote + "\n")
			}
			b.WriteString("\nOutput format:\n")
			b.WriteString("Render a Markdown table with one row per developer x model, columns: developer, model, input_tokens, output_tokens, cache_read, cache_creation, cost_usd.\n")
			b.WriteString("Report `billable_tokens = input_tokens + output_tokens` and show `cache_read` and `cache_creation` as separate metrics (cache_read represents reused context, not fresh work). Include cost_usd and 2-3 highlights (heaviest week, most expensive model, any month-over-month shift you can infer).\n")
			return b.String()
		},
	}
}

func costDrilldownRepositorySpec() *promptSpec {
	return &promptSpec{
		name:        "cost_drilldown_repository",
		title:       "Cost Drilldown by Repository",
		description: "Drill down cost and tokens for a given repository; admins also correlate with per-session cost.",
		scope:       scopeAuthenticated,
		args: []*sdk.PromptArgument{
			{Name: "repository", Description: "Repository basename to drill into (required).", Required: true},
			{Name: "developer", Description: "Optional developer email (admin only; viewer is always scoped to self).", Required: false},
		},
		render: func(u User, args map[string]string) string {
			developerLine, scopeNote := developerCallLine(u, args)
			repo := strings.TrimSpace(args["repository"])

			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString(fmt.Sprintf("Drill down cost and tokens for repository \"%s\".\n\n", repo))
			b.WriteString("Instructions:\n")
			b.WriteString("Call the following tool:\n")
			b.WriteString(fmt.Sprintf("  report_token_usage(period=\"month\", repository=\"%s\", %s)\n", repo, developerLine))
			if u.IsAdmin() {
				b.WriteString("Then ALSO call:\n")
				b.WriteString("  cost_by_session()\n")
				b.WriteString("and correlate the most expensive sessions with the rows from report_token_usage.\n")
			}
			if scopeNote != "" {
				b.WriteString(scopeNote + "\n")
			}
			b.WriteString("\nOutput format:\n")
			b.WriteString(fmt.Sprintf("Render a Markdown table showing cost_usd and tokens per model for repository \"%s\".\n", repo))
			if u.IsAdmin() {
				b.WriteString("Append a **Session-level cost** section listing the top 5 sessions by USD with session id, user_email and cost_usd.\n")
			}
			b.WriteString("Close with a short synthesis (2-3 bullets) on where cost concentrates.\n")
			return b.String()
		},
	}
}

func roiExecutiveSnapshotSpec() *promptSpec {
	return &promptSpec{
		name:        "roi_executive_snapshot",
		title:       "ROI Executive Snapshot",
		description: "Executive ROI snapshot per developer: total cost, active hours, equivalent value, net benefit and ratio.",
		scope:       scopeAuthenticated,
		args: []*sdk.PromptArgument{
			{Name: "developer", Description: "Developer email (admin only; viewer is always scoped to self).", Required: false},
			{Name: "repository", Description: "Optional repository basename filter.", Required: false},
			{Name: "period", Description: "today|week|month. Default month. Ignored when date_start+date_end are set.", Required: false},
			{Name: "date_start", Description: "Optional inclusive start date (YYYY-MM-DD). Must be paired with date_end.", Required: false},
			{Name: "date_end", Description: "Optional inclusive end date (YYYY-MM-DD). Must be paired with date_start.", Required: false},
		},
		render: func(u User, args map[string]string) string {
			developerLine, scopeNote := developerCallLine(u, args)

			ds := strings.TrimSpace(args["date_start"])
			de := strings.TrimSpace(args["date_end"])
			period := strings.TrimSpace(args["period"])
			if period == "" {
				period = "month"
			}

			var call string
			if ds != "" && de != "" {
				call = fmt.Sprintf("report_developer_roi(date_start=\"%s\", date_end=\"%s\", %s%s)",
					ds, de, developerLine, repositoryArg(args))
			} else {
				call = fmt.Sprintf("report_developer_roi(period=\"%s\", %s%s)",
					period, developerLine, repositoryArg(args))
			}

			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString("Produce an executive ROI snapshot for the selected developer(s) over the chosen window.\n\n")
			b.WriteString("Instructions:\n")
			if ds != "" && de != "" {
				b.WriteString(fmt.Sprintf("A custom range is set (date_start=\"%s\", date_end=\"%s\"); use it and IGNORE period.\n", ds, de))
			} else {
				b.WriteString(fmt.Sprintf("No custom range provided; use period=\"%s\".\n", period))
			}
			b.WriteString("Call the following tool exactly once:\n")
			b.WriteString("  " + call + "\n")
			if scopeNote != "" {
				b.WriteString(scopeNote + "\n")
			}
			b.WriteString("\nOutput format:\n")
			b.WriteString("For each developer in the response, write a short narrative paragraph covering:\n")
			b.WriteString("  - total_cost_usd\n  - hours_active\n  - value_delivered_usd (equivalent value)\n  - net_benefit_usd\n  - roi_ratio\n  - daily_average_usd\n")
			b.WriteString("Close with a 1-2 sentence executive takeaway on whether ROI is healthy, borderline or negative.\n")
			return b.String()
		},
	}
}

func compareDevelopersCostSpec() *promptSpec {
	return &promptSpec{
		name:        "compare_developers_cost",
		title:       "Compare Developers by Cost (admin)",
		description: "Rank developers by USD cost and tokens over a window, with heaviest repositories and a synthesis paragraph.",
		scope:       scopeAdmin,
		args: []*sdk.PromptArgument{
			{Name: "period", Description: "today|week|month. Default month. Ignored when date_start+date_end are set.", Required: false},
			{Name: "date_start", Description: "Optional inclusive start date (YYYY-MM-DD). Must be paired with date_end.", Required: false},
			{Name: "date_end", Description: "Optional inclusive end date (YYYY-MM-DD). Must be paired with date_start.", Required: false},
			{Name: "repository", Description: "Optional repository basename filter.", Required: false},
		},
		render: func(_ User, args map[string]string) string {
			ds := strings.TrimSpace(args["date_start"])
			de := strings.TrimSpace(args["date_end"])
			period := strings.TrimSpace(args["period"])
			if period == "" {
				period = "month"
			}

			var call string
			if ds != "" && de != "" {
				call = fmt.Sprintf("report_developer_roi(date_start=\"%s\", date_end=\"%s\"%s)",
					ds, de, repositoryArg(args))
			} else {
				call = fmt.Sprintf("report_developer_roi(period=\"%s\"%s)",
					period, repositoryArg(args))
			}

			var b strings.Builder
			b.WriteString("Goal:\n")
			b.WriteString("Compare developers by Claude Code cost over the selected window and surface concentration risks.\n\n")
			b.WriteString("Instructions:\n")
			if ds != "" && de != "" {
				b.WriteString(fmt.Sprintf("A custom range is set (date_start=\"%s\", date_end=\"%s\"); use it and IGNORE period.\n", ds, de))
			} else {
				b.WriteString(fmt.Sprintf("No custom range provided; use period=\"%s\".\n", period))
			}
			b.WriteString("Call the following tool exactly once, WITHOUT a developer filter (admin scope sees all developers):\n")
			b.WriteString("  " + call + "\n")
			b.WriteString("\nOutput format:\n")
			b.WriteString("Produce three sections:\n")
			b.WriteString("  - **Ranking by USD cost**: developers sorted by total_cost_usd desc.\n")
			b.WriteString("  - **Ranking by tokens**: developers sorted by total tokens desc (sum input+output+cache).\n")
			b.WriteString("  - **Heaviest repositories**: the top repositories across all developers with aggregate cost_usd.\n")
			b.WriteString("Close with a one-paragraph synthesis on cost concentration, outliers and fairness of distribution.\n")
			return b.String()
		},
	}
}

// --- render helpers --------------------------------------------------------

// developerCallLine returns the `developer="..."` fragment embedded in the
// rendered tool call, together with an optional short note clarifying viewer
// scoping. For viewers the email from the context is always used; for admins
// the supplied argument (or "all") is used.
func developerCallLine(u User, args map[string]string) (string, string) {
	if !u.IsAdmin() {
		return fmt.Sprintf("developer=\"%s\"", u.Email), "(scope: self; cannot be overridden)"
	}
	dev := strings.TrimSpace(args["developer"])
	if dev == "" {
		dev = "all"
	}
	return fmt.Sprintf("developer=\"%s\"", dev), ""
}

// repositoryArg returns an optional `, repository="..."` fragment when the
// argument is present (and not the sentinel "all"), or an empty string.
func repositoryArg(args map[string]string) string {
	repo := strings.TrimSpace(args["repository"])
	if repo == "" || repo == "all" {
		return ""
	}
	return fmt.Sprintf(", repository=\"%s\"", repo)
}

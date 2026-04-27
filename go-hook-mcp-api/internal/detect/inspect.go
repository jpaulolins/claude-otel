package detect

import "context"

type InspectInput struct {
	ToolName  string
	Command   string
	Cwd       string
	SessionID string
}

type InspectResult struct {
	Allow    bool
	Severity string // "safe" | "suspicious" | "dangerous"
	Reason   string
}

type CommandInspector interface {
	Inspect(ctx context.Context, cmd InspectInput) InspectResult
}

// NoopInspector always permits execution.
// TODO: replace with rule/ML-based analysis when command inspection is implemented.
type NoopInspector struct{}

func (NoopInspector) Inspect(_ context.Context, _ InspectInput) InspectResult {
	return InspectResult{Allow: true, Severity: "safe"}
}

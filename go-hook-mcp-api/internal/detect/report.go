package detect

import (
	"os"
	"runtime"
	"sort"
	"time"
)

type Report struct {
	SchemaVersion string    `json:"schema_version"`
	ScannedAt     time.Time `json:"scanned_at"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	ExitCode      int       `json:"exit_code"`
	Findings      []Finding `json:"findings"`
	Summary       *Summary  `json:"summary"`
}

// ExitCode returns the process exit code for a set of findings.
// Exit 1 if any finding is medium or high (or any severity in strict mode).
func ExitCode(findings []Finding, strict bool) int {
	for _, f := range findings {
		if strict || f.Severity == SeverityMedium || f.Severity == SeverityHigh {
			return 1
		}
	}
	return 0
}

// BuildReport constructs a Report for the given findings.
func BuildReport(findings []Finding, modulesRan []string, durationMS int64) *Report {
	hostname, _ := os.Hostname()
	toolSet := map[string]struct{}{}
	for _, f := range findings {
		if f.Tool != "" {
			toolSet[f.Tool] = struct{}{}
		}
	}
	tools := make([]string, 0, len(toolSet))
	for t := range toolSet {
		tools = append(tools, t)
	}
	sort.Strings(tools)

	if findings == nil {
		findings = []Finding{}
	}

	return &Report{
		SchemaVersion: "1.0",
		ScannedAt:     time.Now().UTC(),
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Findings:      findings,
		Summary: &Summary{
			FindingsCount: len(findings),
			ToolsDetected: tools,
		},
	}
}

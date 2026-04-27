package detect_test

import (
	"encoding/json"
	"testing"

	"go-hook-mcp-api/internal/detect"
)

func TestExitCode_NoFindings(t *testing.T) {
	if detect.ExitCode(nil, false) != 0 {
		t.Error("exit code should be 0 for empty findings")
	}
}

func TestExitCode_InfoOnlyIsClean(t *testing.T) {
	findings := []detect.Finding{{Severity: detect.SeverityInfo}}
	if detect.ExitCode(findings, false) != 0 {
		t.Error("info-only findings should not trigger exit 1")
	}
}

func TestExitCode_MediumTriggers(t *testing.T) {
	findings := []detect.Finding{{Severity: detect.SeverityMedium}}
	if detect.ExitCode(findings, false) != 1 {
		t.Error("medium severity should produce exit 1")
	}
}

func TestExitCode_StrictModeInfoTriggers(t *testing.T) {
	findings := []detect.Finding{{Severity: detect.SeverityInfo}}
	if detect.ExitCode(findings, true) != 1 {
		t.Error("strict mode: info severity should produce exit 1")
	}
}

func TestBuildReport_ValidJSON(t *testing.T) {
	findings := []detect.Finding{
		{Tool: "cursor", Module: "filesystem", Signal: "dir found", Severity: detect.SeverityInfo},
	}
	r := detect.BuildReport(findings, []string{"filesystem"}, 0)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Error("empty JSON output")
	}
	var check map[string]any
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if check["schema_version"] != "1.0" {
		t.Errorf("schema_version = %v; want 1.0", check["schema_version"])
	}
}

func TestToolsDetected_Deduplication(t *testing.T) {
	findings := []detect.Finding{
		{Tool: "cursor", Severity: detect.SeverityInfo},
		{Tool: "cursor", Severity: detect.SeverityMedium},
		{Tool: "codex", Severity: detect.SeverityHigh},
	}
	r := detect.BuildReport(findings, nil, 1)
	tools := r.Summary.ToolsDetected
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool] {
			t.Fatalf("duplicate tool: %s", tool)
		}
		seen[tool] = true
	}
}

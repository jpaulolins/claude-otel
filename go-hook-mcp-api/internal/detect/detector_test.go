package detect_test

import (
	"context"
	"testing"

	"go-hook-mcp-api/internal/detect"
)

type stubDetector struct {
	name     string
	findings []detect.Finding
}

func (s *stubDetector) Name() string { return s.name }
func (s *stubDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	return s.findings, nil
}

func TestFindingSeverityValues(t *testing.T) {
	for _, s := range []detect.Severity{
		detect.SeverityInfo, detect.SeverityMedium, detect.SeverityHigh,
	} {
		if string(s) == "" {
			t.Errorf("severity value is empty string")
		}
	}
}

func TestFindingStruct(t *testing.T) {
	f := detect.Finding{
		Tool:     "cursor",
		Module:   "filesystem",
		Signal:   "config dir found",
		Severity: detect.SeverityInfo,
	}
	if f.Tool != "cursor" {
		t.Errorf("Tool = %q; want cursor", f.Tool)
	}
}

func TestNoopInspector_AlwaysAllows(t *testing.T) {
	insp := detect.NoopInspector{}
	result := insp.Inspect(context.Background(), detect.InspectInput{
		ToolName: "Bash",
		Command:  "rm -rf /",
	})
	if !result.Allow {
		t.Error("NoopInspector must always return Allow=true")
	}
	if result.Severity != "safe" {
		t.Errorf("Severity = %q; want safe", result.Severity)
	}
}

package detect_test

import (
	"context"
	"errors"
	"testing"

	"go-hook-mcp-api/internal/detect"
)

type fixedDetector struct {
	name     string
	findings []detect.Finding
	err      error
}

func (f *fixedDetector) Name() string { return f.name }
func (f *fixedDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	return f.findings, f.err
}

func TestRun_CollectsAllFindings(t *testing.T) {
	d1 := &fixedDetector{name: "a", findings: []detect.Finding{{Tool: "claude", Severity: detect.SeverityInfo}}}
	d2 := &fixedDetector{name: "b", findings: []detect.Finding{{Tool: "cursor", Severity: detect.SeverityMedium}}}
	findings, errs := detect.Run(context.Background(), []detect.Detector{d1, d2})
	if len(findings) != 2 {
		t.Errorf("findings count = %d; want 2", len(findings))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestRun_CollectsErrors(t *testing.T) {
	d := &fixedDetector{name: "bad", err: errors.New("detector failed")}
	_, errs := detect.Run(context.Background(), []detect.Detector{d})
	if len(errs) != 1 {
		t.Errorf("errors count = %d; want 1", len(errs))
	}
}

func TestRun_EmptyDetectors(t *testing.T) {
	findings, errs := detect.Run(context.Background(), nil)
	if len(findings) != 0 || len(errs) != 0 {
		t.Errorf("expected empty results; got findings=%d errs=%d", len(findings), len(errs))
	}
}

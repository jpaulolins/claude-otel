package detectors_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestOpenCode_PluginFound_HighSeverity(t *testing.T) {
	d := detectors.NewOpenCodeDetector(
		func(p string) error {
			if strings.HasSuffix(p, "opencode-plugin-otel") {
				return nil
			}
			return errors.New("not found")
		},
		func() ([]string, error) { return nil, nil },
	)
	findings, _ := d.Detect(context.Background())
	var found bool
	for _, f := range findings {
		if f.Module == "otel-harvest" && f.Severity == detect.SeverityHigh {
			found = true
		}
	}
	if !found {
		t.Error("expected high otel-harvest finding for plugin")
	}
}

func TestOpenCode_NothingFound(t *testing.T) {
	d := detectors.NewOpenCodeDetector(
		func(string) error { return errors.New("not found") },
		func() ([]string, error) { return nil, nil },
	)
	findings, err := d.Detect(context.Background())
	if err != nil || len(findings) != 0 {
		t.Errorf("expected 0 findings; got %d err=%v", len(findings), err)
	}
}

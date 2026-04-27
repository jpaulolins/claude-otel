package detectors_test

import (
	"context"
	"testing"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestPackages_PythonAIFound(t *testing.T) {
	d := detectors.NewPackagesDetector(
		func() ([]string, error) { return []string{"openai", "boto3"}, nil },
		func() ([]string, error) { return nil, nil },
	)
	findings, _ := d.Detect(context.Background())
	if len(findings) == 0 {
		t.Error("expected finding for openai package")
	}
	for _, f := range findings {
		if f.Severity != detect.SeverityInfo {
			t.Errorf("packages severity should be info; got %q", f.Severity)
		}
	}
}

func TestPackages_NoAIPackages(t *testing.T) {
	d := detectors.NewPackagesDetector(
		func() ([]string, error) { return []string{"boto3", "requests"}, nil },
		func() ([]string, error) { return []string{"lodash", "express"}, nil },
	)
	findings, _ := d.Detect(context.Background())
	if len(findings) != 0 {
		t.Errorf("expected 0 findings; got %d", len(findings))
	}
}

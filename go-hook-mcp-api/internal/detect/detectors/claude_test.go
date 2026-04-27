package detectors_test

import (
	"context"
	"errors"
	"testing"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestClaude_NothingFound(t *testing.T) {
	d := detectors.NewClaudeDetector(
		func(string) error { return errors.New("not found") },
		func() ([]string, error) { return nil, nil },
	)
	findings, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings; got %d", len(findings))
	}
}

func TestClaude_DirExists_InfoSeverity(t *testing.T) {
	d := detectors.NewClaudeDetector(
		func(string) error { return nil },
		func() ([]string, error) { return nil, nil },
	)
	findings, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	for _, f := range findings {
		if f.Severity != detect.SeverityInfo {
			t.Errorf("dir-only finding should be info; got %q", f.Severity)
		}
		if f.Tool != "claude" {
			t.Errorf("Tool = %q; want claude", f.Tool)
		}
	}
}

func TestClaude_ProcessRunning_MediumSeverity(t *testing.T) {
	d := detectors.NewClaudeDetector(
		func(string) error { return errors.New("not found") },
		func() ([]string, error) { return []string{"claude", "bash"}, nil },
	)
	findings, _ := d.Detect(context.Background())
	var found bool
	for _, f := range findings {
		if f.Module == "process" && f.Severity == detect.SeverityMedium {
			found = true
		}
	}
	if !found {
		t.Error("expected medium process finding for running claude process")
	}
}

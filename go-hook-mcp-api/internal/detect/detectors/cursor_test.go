package detectors_test

import (
	"context"
	"errors"
	"testing"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestCursor_NothingFound(t *testing.T) {
	d := detectors.NewCursorDetector(
		func(string) error { return errors.New("not found") },
		func() ([]string, error) { return nil, nil },
	)
	findings, err := d.Detect(context.Background())
	if err != nil || len(findings) != 0 {
		t.Errorf("expected 0 findings, no error; got %d, %v", len(findings), err)
	}
}

func TestCursor_ProcessMedium(t *testing.T) {
	d := detectors.NewCursorDetector(
		func(string) error { return errors.New("not found") },
		func() ([]string, error) { return []string{"Cursor"}, nil },
	)
	findings, _ := d.Detect(context.Background())
	if len(findings) == 0 || findings[0].Severity != detect.SeverityMedium {
		t.Error("expected medium finding for Cursor process")
	}
}

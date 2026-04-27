package detectors_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestCodex_OTELHarvest_Configured(t *testing.T) {
	toml := []byte("[otel]\nexporter = \"otlp-http\"\nendpoint = \"https://collector.internal\"\n")
	d := detectors.NewCodexDetector(
		func(string) error { return nil },
		func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "config.toml") {
				return toml, nil
			}
			return nil, errors.New("not found")
		},
		func() ([]string, error) { return nil, nil },
	)
	findings, _ := d.Detect(context.Background())
	var high *detect.Finding
	for i := range findings {
		if findings[i].Module == "otel-harvest" {
			high = &findings[i]
		}
	}
	if high == nil {
		t.Fatal("expected otel-harvest finding")
	}
	if high.Severity != detect.SeverityHigh {
		t.Errorf("severity = %q; want high", high.Severity)
	}
	if high.Metadata["exporter"] != "otlp-http" {
		t.Errorf("exporter = %q; want otlp-http", high.Metadata["exporter"])
	}
}

func TestCodex_OTELHarvest_NoneExporter(t *testing.T) {
	toml := []byte("[otel]\nexporter = \"none\"\n")
	d := detectors.NewCodexDetector(
		func(string) error { return errors.New("not found") },
		func(string) ([]byte, error) { return toml, nil },
		func() ([]string, error) { return nil, nil },
	)
	findings, _ := d.Detect(context.Background())
	for _, f := range findings {
		if f.Module == "otel-harvest" {
			t.Error("exporter=none should not produce an otel-harvest finding")
		}
	}
}

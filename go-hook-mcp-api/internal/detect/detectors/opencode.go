package detectors

import (
	"context"
	"os"
	"path/filepath"

	"go-hook-mcp-api/internal/detect"
)

type OpenCodeDetector struct {
	statFn    func(string) error
	processFn func() ([]string, error)
}

func NewOpenCodeDetector(statFn func(string) error, processFn func() ([]string, error)) *OpenCodeDetector {
	return &OpenCodeDetector{statFn: statFn, processFn: processFn}
}

func NewOpenCode() *OpenCodeDetector {
	return NewOpenCodeDetector(
		func(p string) error { _, err := os.Stat(p); return err },
		listProcessNames,
	)
}

func (d *OpenCodeDetector) Name() string { return "opencode" }

func (d *OpenCodeDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "opencode")

	if d.statFn(configDir) == nil {
		findings = append(findings, detect.Finding{
			Tool: "opencode", Module: "filesystem",
			Signal: "config directory found", Path: configDir,
			Severity: detect.SeverityInfo,
		})
	}

	pluginPath := filepath.Join(home, ".cache", "opencode", "node_modules", "opencode-plugin-otel")
	if d.statFn(pluginPath) == nil {
		findings = append(findings, detect.Finding{
			Tool: "opencode", Module: "otel-harvest",
			Signal:   "opencode-plugin-otel installed",
			Path:     pluginPath,
			Severity: detect.SeverityHigh,
		})
	}

	procs, _ := d.processFn()
	for _, p := range procs {
		if p == "opencode" {
			findings = append(findings, detect.Finding{
				Tool: "opencode", Module: "process",
				Signal: "process running", Path: p,
				Severity: detect.SeverityMedium,
			})
			break
		}
	}
	return findings, nil
}

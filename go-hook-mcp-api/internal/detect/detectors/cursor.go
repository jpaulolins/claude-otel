package detectors

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"go-hook-mcp-api/internal/detect"
)

type CursorDetector struct {
	statFn    func(string) error
	processFn func() ([]string, error)
}

func NewCursorDetector(statFn func(string) error, processFn func() ([]string, error)) *CursorDetector {
	return &CursorDetector{statFn: statFn, processFn: processFn}
}

func NewCursor() *CursorDetector {
	return NewCursorDetector(
		func(p string) error { _, err := os.Stat(p); return err },
		listProcessNames,
	)
}

func (d *CursorDetector) Name() string { return "cursor" }

func (d *CursorDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding
	for _, dir := range cursorDirs() {
		if d.statFn(dir) == nil {
			findings = append(findings, detect.Finding{
				Tool: "cursor", Module: "filesystem",
				Signal: "config directory found", Path: dir,
				Severity: detect.SeverityInfo,
			})
		}
	}
	procs, _ := d.processFn()
	for _, p := range procs {
		if p == "Cursor" || p == "cursor" || p == "cursor-tunnel" {
			findings = append(findings, detect.Finding{
				Tool: "cursor", Module: "process",
				Signal: "process running", Path: p,
				Severity: detect.SeverityMedium,
			})
			break
		}
	}
	return findings, nil
}

func cursorDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, ".cursor"),
			filepath.Join(home, "Library", "Application Support", "Cursor"),
		}
	case "windows":
		return []string{
			filepath.Join(os.Getenv("APPDATA"), "Cursor"),
			filepath.Join(home, ".cursor"),
		}
	default:
		return []string{
			filepath.Join(home, ".cursor"),
			filepath.Join(home, ".config", "Cursor"),
		}
	}
}

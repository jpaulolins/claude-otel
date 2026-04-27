package detectors

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"go-hook-mcp-api/internal/detect"
)

type ClaudeDetector struct {
	statFn    func(string) error
	processFn func() ([]string, error)
}

func NewClaudeDetector(statFn func(string) error, processFn func() ([]string, error)) *ClaudeDetector {
	return &ClaudeDetector{statFn: statFn, processFn: processFn}
}

// NewClaude returns a production ClaudeDetector using real OS calls.
func NewClaude() *ClaudeDetector {
	return NewClaudeDetector(
		func(p string) error { _, err := os.Stat(p); return err },
		listProcessNames,
	)
}

func (d *ClaudeDetector) Name() string { return "claude" }

func (d *ClaudeDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding
	for _, dir := range claudeDirs() {
		if d.statFn(dir) == nil {
			findings = append(findings, detect.Finding{
				Tool:     "claude",
				Module:   "filesystem",
				Signal:   "config directory found",
				Path:     dir,
				Severity: detect.SeverityInfo,
			})
		}
	}
	procs, _ := d.processFn()
	for _, p := range procs {
		if p == "claude" {
			findings = append(findings, detect.Finding{
				Tool:     "claude",
				Module:   "process",
				Signal:   "process running",
				Path:     p,
				Severity: detect.SeverityMedium,
			})
			break
		}
	}
	return findings, nil
}

func claudeDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return []string{
			filepath.Join(os.Getenv("APPDATA"), "Claude"),
			filepath.Join(home, ".claude"),
		}
	default:
		return []string{filepath.Join(home, ".claude")}
	}
}

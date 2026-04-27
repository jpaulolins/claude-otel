package detectors

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	"go-hook-mcp-api/internal/detect"
)

type CodexDetector struct {
	statFn     func(string) error
	readFileFn func(string) ([]byte, error)
	processFn  func() ([]string, error)
}

func NewCodexDetector(statFn func(string) error, readFileFn func(string) ([]byte, error), processFn func() ([]string, error)) *CodexDetector {
	return &CodexDetector{statFn: statFn, readFileFn: readFileFn, processFn: processFn}
}

func NewCodex() *CodexDetector {
	return NewCodexDetector(
		func(p string) error { _, err := os.Stat(p); return err },
		os.ReadFile,
		listProcessNames,
	)
}

func (d *CodexDetector) Name() string { return "codex" }

func (d *CodexDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding
	home, _ := os.UserHomeDir()
	codexDir := filepath.Join(home, ".codex")

	if d.statFn(codexDir) == nil {
		findings = append(findings, detect.Finding{
			Tool: "codex", Module: "filesystem",
			Signal: "config directory found", Path: codexDir,
			Severity: detect.SeverityInfo,
		})
	}

	for _, cfgPath := range []string{
		filepath.Join(codexDir, "config.toml"),
		filepath.Join(".", ".codex", "config.toml"),
	} {
		if f := d.harvestOTEL(cfgPath); f != nil {
			findings = append(findings, *f)
		}
	}

	procs, _ := d.processFn()
	for _, p := range procs {
		if p == "codex" {
			findings = append(findings, detect.Finding{
				Tool: "codex", Module: "process",
				Signal: "process running", Path: p,
				Severity: detect.SeverityMedium,
			})
			break
		}
	}
	return findings, nil
}

func (d *CodexDetector) harvestOTEL(path string) *detect.Finding {
	data, err := d.readFileFn(path)
	if err != nil {
		return nil
	}
	otelSection := false
	meta := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[otel]" {
			otelSection = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			otelSection = false
		}
		if !otelSection {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			k = strings.TrimSpace(strings.Trim(k, `"`))
			v = strings.TrimSpace(strings.Trim(v, `" `))
			meta[k] = v
		}
	}
	exporter, ok := meta["exporter"]
	if !ok || exporter == "none" || exporter == "" {
		return nil
	}
	return &detect.Finding{
		Tool: "codex", Module: "otel-harvest",
		Signal:   "otel exporter configured",
		Path:     path,
		Severity: detect.SeverityHigh,
		Metadata: map[string]string{"exporter": exporter, "endpoint": meta["endpoint"]},
	}
}

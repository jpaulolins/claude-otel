package detectors

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"go-hook-mcp-api/internal/detect"
)

var pythonTargets = []string{"openai", "anthropic", "langchain", "litellm", "together", "huggingface_hub"}
var nodeTargets = []string{"@anthropic-ai/sdk", "openai", "langchain", "@google/generative-ai"}

type PackagesDetector struct {
	pythonPkgsFn func() ([]string, error)
	nodePkgsFn   func() ([]string, error)
}

func NewPackagesDetector(pythonFn, nodeFn func() ([]string, error)) *PackagesDetector {
	return &PackagesDetector{pythonPkgsFn: pythonFn, nodePkgsFn: nodeFn}
}

func NewPackages() *PackagesDetector {
	return NewPackagesDetector(listPythonPackages, listNodePackages)
}

func (d *PackagesDetector) Name() string { return "packages" }

func (d *PackagesDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding
	if pkgs, err := d.pythonPkgsFn(); err == nil {
		for _, pkg := range pkgs {
			for _, target := range pythonTargets {
				if strings.Contains(pkg, target) {
					findings = append(findings, detect.Finding{
						Tool: pkg, Module: "packages",
						Signal:   "python ai package installed",
						Path:     pkg,
						Severity: detect.SeverityInfo,
					})
					break
				}
			}
		}
	}
	if pkgs, err := d.nodePkgsFn(); err == nil {
		for _, pkg := range pkgs {
			for _, target := range nodeTargets {
				if strings.Contains(pkg, target) {
					findings = append(findings, detect.Finding{
						Tool: pkg, Module: "packages",
						Signal:   "node ai package installed",
						Path:     pkg,
						Severity: detect.SeverityInfo,
					})
					break
				}
			}
		}
	}
	return findings, nil
}

func listPythonPackages() ([]string, error) {
	out, err := exec.Command("python3", "-m", "pip", "list", "--format=freeze").Output()
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(string(out), "\n") {
		if pkg, _, ok := strings.Cut(line, "=="); ok {
			pkgs = append(pkgs, strings.ToLower(strings.TrimSpace(pkg)))
		}
	}
	return pkgs, nil
}

func listNodePackages() ([]string, error) {
	out, err := exec.Command("npm", "list", "-g", "--depth=0", "--json").Output()
	if err != nil {
		return nil, err
	}
	var result struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	pkgs := make([]string, 0, len(result.Dependencies))
	for k := range result.Dependencies {
		pkgs = append(pkgs, k)
	}
	return pkgs, nil
}

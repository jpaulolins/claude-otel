# cotel Unified Binary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the HTTP-based `audit-service` with a unified `cotel` CLI binary that handles Claude Code hooks via stdin and adds cross-platform shadow AI detection (`cotel scan`).

**Architecture:** Single binary at `cmd/detect/` with two subcommands. `cotel hook` reads hook JSON from stdin, redacts secrets, calls a no-op inspector, emits one OTEL span, then exits. `cotel scan` runs parallel detectors (Claude/Cursor/Codex/OpenCode/packages/network), caches daily results in `~/.cotel/settings.json`, and outputs JSON to stdout with optional OTEL emit. Existing `internal/audit` is renamed to `internal/hook` (HTTP handler code dropped); a new `internal/detect` package holds scan logic; `otelexport` gets a `SetupSync` variant.

**Tech Stack:** Go 1.25, existing OTEL SDK, `runtime.GOOS` for cross-platform paths, `sync.WaitGroup` for parallelism, `flag` for CLI, no new external dependencies.

---

## File Map

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `internal/hook/payload.go` | HookPayload struct + normalize (moved from audit) |
| Create | `internal/hook/redact.go` | Secret redaction (moved from audit) |
| Create | `internal/hook/payload_test.go` | Tests (moved from audit) |
| Create | `internal/hook/redact_test.go` | Tests (moved from audit) |
| Delete | `internal/audit/` | Entire directory |
| Modify | `internal/otelexport/exporter.go` | Add SetupSync() with simple processors |
| Modify | `internal/otelexport/exporter_test.go` | Add SetupSync test |
| Create | `internal/detect/detector.go` | Detector interface + Finding/Severity/Report types |
| Create | `internal/detect/inspect.go` | CommandInspector interface + NoopInspector |
| Create | `internal/detect/cache.go` | Settings read/write for `~/.cotel/settings.json` |
| Create | `internal/detect/runner.go` | Parallel WaitGroup runner |
| Create | `internal/detect/report.go` | JSON output + exit code logic + OTEL emit |
| Create | `internal/detect/detector_test.go` | Type sanity tests |
| Create | `internal/detect/cache_test.go` | Cache logic tests |
| Create | `internal/detect/runner_test.go` | Runner parallel tests |
| Create | `internal/detect/report_test.go` | Report JSON + exit code tests |
| Create | `internal/detect/detectors/claude.go` | Claude Code detector |
| Create | `internal/detect/detectors/cursor.go` | Cursor detector |
| Create | `internal/detect/detectors/codex.go` | Codex CLI + OTEL harvest |
| Create | `internal/detect/detectors/opencode.go` | OpenCode + OTEL harvest |
| Create | `internal/detect/detectors/packages.go` | Python/Node package scan |
| Create | `internal/detect/detectors/network.go` | MCP port probe + connection scan |
| Create | `internal/detect/detectors/*_test.go` | One test file per detector |
| Create | `cmd/detect/main.go` | CLI entry point: hook + scan subcommands |
| Delete | `cmd/audit/` | Entire directory |
| Modify | `Makefile` | Update build targets |
| Modify | `docker-compose.yml` | Remove audit-service service |
| Modify | `../../claude-managed-settings.json` | URL hooks → command hooks |

All paths below are relative to `go-hook-mcp-api/` unless noted.

---

## Task 1: Create internal/hook (rename from internal/audit)

**Files:**
- Create: `internal/hook/payload.go`
- Create: `internal/hook/redact.go`
- Create: `internal/hook/payload_test.go`
- Create: `internal/hook/redact_test.go`
- Delete: `internal/audit/` (all files)

- [ ] **Step 1: Copy payload.go with package rename**

```go
// internal/hook/payload.go  — package declaration changed from "audit" to "hook"
package hook
```

Run:
```bash
cp internal/audit/payload.go internal/hook/payload.go
sed -i '' 's/^package audit$/package hook/' internal/hook/payload.go
```

- [ ] **Step 2: Copy redact.go with package rename**

```bash
cp internal/audit/redact.go internal/hook/redact.go
sed -i '' 's/^package audit$/package hook/' internal/hook/redact.go
```

- [ ] **Step 3: Copy tests with package rename**

```bash
cp internal/audit/payload_test.go internal/hook/payload_test.go
cp internal/audit/redact_test.go  internal/hook/redact_test.go
sed -i '' 's/^package audit$/package hook/' internal/hook/payload_test.go
sed -i '' 's/^package audit$/package hook/' internal/hook/redact_test.go
```

- [ ] **Step 4: Run tests to verify package compiles**

```bash
go test ./internal/hook/...
```
Expected: PASS (all tests from audit package now pass under hook)

- [ ] **Step 5: Delete internal/audit**

```bash
rm -rf internal/audit/
```

- [ ] **Step 6: Verify no broken imports (only cmd/audit used internal/audit, which will be deleted)**

```bash
grep -r 'internal/audit' . --include='*.go'
```
Expected: no output

- [ ] **Step 7: Run all tests**

```bash
go test ./...
```
Expected: PASS (cmd/audit is still present and imports internal/audit — it will fail. That's OK; cmd/audit is deleted in Task 10. For now, scope test to packages that exist: `go test ./internal/... ./cmd/mcp/...`)

```bash
go test ./internal/hook/... ./internal/mcp/... ./internal/otelexport/...
```
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/hook/ internal/audit/
git commit -m "refactor: rename internal/audit → internal/hook, drop HTTP handler code"
```

---

## Task 2: Add SetupSync to otelexport

The existing `Setup()` uses batch processors (good for long-running servers like mcp-server). The `cotel` CLI binary lives for milliseconds — it needs synchronous processors that flush before exit.

**Files:**
- Modify: `internal/otelexport/exporter.go`
- Modify: `internal/otelexport/exporter_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/otelexport/exporter_test.go`:

```go
func TestSetupSync_ReturnsProviders(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		ServiceName:  "test-sync",
		OTLPEndpoint: "http://localhost:14318", // nothing listening; export will fail but Setup must succeed
	}
	providers, cleanup, err := SetupSync(ctx, cfg)
	if err != nil {
		t.Fatalf("SetupSync: %v", err)
	}
	defer cleanup()
	if providers == nil {
		t.Fatal("providers is nil")
	}
	if providers.TracerProvider == nil {
		t.Fatal("TracerProvider is nil")
	}
	if providers.LoggerProvider == nil {
		t.Fatal("LoggerProvider is nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/otelexport/... -run TestSetupSync
```
Expected: FAIL — `SetupSync` undefined

- [ ] **Step 3: Add SetupSync to exporter.go**

Append to `internal/otelexport/exporter.go` after the existing `Setup` function:

```go
// SetupSync creates providers with synchronous span/log processors.
// Use this for short-lived CLI processes where ForceFlush via Shutdown()
// must complete before os.Exit — batch processors may drop spans on fast exit.
func SetupSync(ctx context.Context, cfg Config) (*Providers, func(), error) {
	headers := ParseHeaders(cfg.OTLPHeaders)

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, nil, err
	}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint + "/v1/traces"),
		otlptracehttp.WithHeaders(headers),
	}
	traceExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSyncer(traceExp),
	)
	otel.SetTracerProvider(tp)

	logOpts := []otlploghttp.Option{
		otlploghttp.WithEndpointURL(cfg.OTLPEndpoint + "/v1/logs"),
		otlploghttp.WithHeaders(headers),
	}
	logExp, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExp)),
	)

	cleanup := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutCtx)
		_ = lp.Shutdown(shutCtx)
	}

	return &Providers{TracerProvider: tp, LoggerProvider: lp}, cleanup, nil
}
```

Add `"time"` to the import block in exporter.go.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/otelexport/...
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/otelexport/
git commit -m "feat(otelexport): add SetupSync with simple processors for short-lived CLIs"
```

---

## Task 3: Create detect core types

**Files:**
- Create: `internal/detect/detector.go`
- Create: `internal/detect/inspect.go`
- Create: `internal/detect/detector_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/detect/detector_test.go`:

```go
package detect_test

import (
	"context"
	"testing"

	"go-hook-mcp-api/internal/detect"
)

type stubDetector struct {
	name     string
	findings []detect.Finding
}

func (s *stubDetector) Name() string { return s.name }
func (s *stubDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	return s.findings, nil
}

func TestFindingSeverityValues(t *testing.T) {
	for _, s := range []detect.Severity{
		detect.SeverityInfo, detect.SeverityMedium, detect.SeverityHigh,
	} {
		if string(s) == "" {
			t.Errorf("severity value is empty string")
		}
	}
}

func TestFindingStruct(t *testing.T) {
	f := detect.Finding{
		Tool:     "cursor",
		Module:   "filesystem",
		Signal:   "config dir found",
		Severity: detect.SeverityInfo,
	}
	if f.Tool != "cursor" {
		t.Errorf("Tool = %q; want cursor", f.Tool)
	}
}

func TestNoopInspector_AlwaysAllows(t *testing.T) {
	insp := detect.NoopInspector{}
	result := insp.Inspect(context.Background(), detect.InspectInput{
		ToolName: "Bash",
		Command:  "rm -rf /",
	})
	if !result.Allow {
		t.Error("NoopInspector must always return Allow=true")
	}
	if result.Severity != "safe" {
		t.Errorf("Severity = %q; want safe", result.Severity)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./internal/detect/... 2>&1 | head -5
```
Expected: FAIL — package not found

- [ ] **Step 3: Create detector.go**

```go
// internal/detect/detector.go
package detect

import "context"

type Severity string

const (
	SeverityInfo   Severity = "info"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

type Finding struct {
	Tool     string            `json:"tool"`
	Module   string            `json:"module"`
	Signal   string            `json:"signal"`
	Path     string            `json:"path,omitempty"`
	Severity Severity          `json:"severity"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Detector interface {
	Name() string
	Detect(ctx context.Context) ([]Finding, error)
}
```

- [ ] **Step 4: Create inspect.go**

```go
// internal/detect/inspect.go
package detect

import "context"

type InspectInput struct {
	ToolName  string
	Command   string
	Cwd       string
	SessionID string
}

type InspectResult struct {
	Allow    bool
	Severity string // "safe" | "suspicious" | "dangerous"
	Reason   string
}

type CommandInspector interface {
	Inspect(ctx context.Context, cmd InspectInput) InspectResult
}

// NoopInspector always permits execution.
// TODO: replace with rule/ML-based analysis when command inspection is implemented.
type NoopInspector struct{}

func (NoopInspector) Inspect(_ context.Context, _ InspectInput) InspectResult {
	return InspectResult{Allow: true, Severity: "safe"}
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/detect/...
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/detect/
git commit -m "feat(detect): add core Detector interface, Finding types, NoopInspector"
```

---

## Task 4: Create cache.go

**Files:**
- Create: `internal/detect/cache.go`
- Create: `internal/detect/cache_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/detect/cache_test.go`:

```go
package detect_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-hook-mcp-api/internal/detect"
)

func TestLoadSettings_DefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	s, path := detect.LoadSettings(filepath.Join(dir, "settings.json"))
	if s.Version != 1 {
		t.Errorf("Version = %d; want 1", s.Version)
	}
	if s.ScanTimeoutSecs != 30 {
		t.Errorf("ScanTimeoutSecs = %d; want 30", s.ScanTimeoutSecs)
	}
	if path != filepath.Join(dir, "settings.json") {
		t.Errorf("path = %q; unexpected", path)
	}
}

func TestShouldSkip_FreshRun(t *testing.T) {
	s := &detect.Settings{LastRunAt: time.Now().Add(-10 * time.Minute)}
	if !detect.ShouldSkip(s, false) {
		t.Error("ShouldSkip should be true when last run < 24h ago")
	}
}

func TestShouldSkip_StaleRun(t *testing.T) {
	s := &detect.Settings{LastRunAt: time.Now().Add(-25 * time.Hour)}
	if detect.ShouldSkip(s, false) {
		t.Error("ShouldSkip should be false when last run > 24h ago")
	}
}

func TestShouldSkip_ZeroTime(t *testing.T) {
	s := &detect.Settings{}
	if detect.ShouldSkip(s, false) {
		t.Error("ShouldSkip should be false when LastRunAt is zero")
	}
}

func TestShouldSkip_ForceFlagOverrides(t *testing.T) {
	s := &detect.Settings{LastRunAt: time.Now()}
	if detect.ShouldSkip(s, true) {
		t.Error("ShouldSkip must be false when force=true")
	}
}

func TestSaveSettings_Roundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := &detect.Settings{
		Version:       1,
		OTLPEndpoint:  "http://localhost:4318",
		LastExitCode:  1,
		LastRunAt:     time.Now().UTC().Truncate(time.Second),
		LastSummary:   &detect.Summary{FindingsCount: 2, ToolsDetected: []string{"cursor"}},
	}
	if err := detect.SaveSettings(path, s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	s2, _ := detect.LoadSettings(path)
	if s2.OTLPEndpoint != s.OTLPEndpoint {
		t.Errorf("OTLPEndpoint = %q; want %q", s2.OTLPEndpoint, s.OTLPEndpoint)
	}
	if s2.LastExitCode != 1 {
		t.Errorf("LastExitCode = %d; want 1", s2.LastExitCode)
	}
	if s2.LastSummary == nil || s2.LastSummary.FindingsCount != 2 {
		t.Errorf("LastSummary not preserved")
	}
}

func TestResolveSettingsPath_LocalFirst(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "settings.json")
	os.WriteFile(local, []byte(`{"version":1}`), 0600)
	got := detect.ResolveSettingsPath(dir)
	if got != local {
		t.Errorf("ResolveSettingsPath = %q; want local %q", got, local)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./internal/detect/... -run TestLoad 2>&1 | head -5
```
Expected: FAIL

- [ ] **Step 3: Create cache.go**

```go
// internal/detect/cache.go
package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Settings struct {
	Version         int       `json:"version"`
	OTLPEndpoint    string    `json:"otel_endpoint,omitempty"`
	OTLPToken       string    `json:"otel_token,omitempty"`
	ScanTimeoutSecs int       `json:"scan_timeout_seconds,omitempty"`
	LastRunAt       time.Time `json:"last_run_at,omitempty"`
	LastExitCode    int       `json:"last_exit_code,omitempty"`
	LastSummary     *Summary  `json:"last_summary,omitempty"`
}

type Summary struct {
	FindingsCount int      `json:"findings_count"`
	ToolsDetected []string `json:"tools_detected"`
}

// ResolveSettingsPath returns the effective settings file path.
// Checks ./settings.json first, then ~/.cotel/settings.json.
// If explicitDir is non-empty, only that directory is checked.
func ResolveSettingsPath(explicitDir string) string {
	if explicitDir != "" {
		return filepath.Join(explicitDir, "settings.json")
	}
	if _, err := os.Stat("settings.json"); err == nil {
		abs, _ := filepath.Abs("settings.json")
		return abs
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cotel", "settings.json")
}

// LoadSettings reads the settings file at path. Returns defaults if the file
// does not exist or cannot be parsed.
func LoadSettings(path string) (*Settings, string) {
	s := &Settings{Version: 1, ScanTimeoutSecs: 30}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, s)
		if s.ScanTimeoutSecs == 0 {
			s.ScanTimeoutSecs = 30
		}
	}
	return s, path
}

// SaveSettings writes s to path, creating parent directories as needed.
func SaveSettings(path string, s *Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ShouldSkip returns true when the last run was less than 24h ago and force is false.
func ShouldSkip(s *Settings, force bool) bool {
	if force || s.LastRunAt.IsZero() {
		return false
	}
	return time.Since(s.LastRunAt) < 24*time.Hour
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/detect/... -run TestLoad,TestShouldSkip,TestSave,TestResolve
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/detect/cache.go internal/detect/cache_test.go
git commit -m "feat(detect): add Settings cache with daily skip logic"
```

---

## Task 5: Create runner.go and report.go

**Files:**
- Create: `internal/detect/runner.go`
- Create: `internal/detect/report.go`
- Create: `internal/detect/runner_test.go`
- Create: `internal/detect/report_test.go`

- [ ] **Step 1: Write runner tests**

Create `internal/detect/runner_test.go`:

```go
package detect_test

import (
	"context"
	"errors"
	"testing"

	"go-hook-mcp-api/internal/detect"
)

type fixedDetector struct {
	name     string
	findings []detect.Finding
	err      error
}

func (f *fixedDetector) Name() string { return f.name }
func (f *fixedDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	return f.findings, f.err
}

func TestRun_CollectsAllFindings(t *testing.T) {
	d1 := &fixedDetector{name: "a", findings: []detect.Finding{{Tool: "claude", Severity: detect.SeverityInfo}}}
	d2 := &fixedDetector{name: "b", findings: []detect.Finding{{Tool: "cursor", Severity: detect.SeverityMedium}}}
	findings, errs := detect.Run(context.Background(), []detect.Detector{d1, d2})
	if len(findings) != 2 {
		t.Errorf("findings count = %d; want 2", len(findings))
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestRun_CollectsErrors(t *testing.T) {
	d := &fixedDetector{name: "bad", err: errors.New("detector failed")}
	_, errs := detect.Run(context.Background(), []detect.Detector{d})
	if len(errs) != 1 {
		t.Errorf("errors count = %d; want 1", len(errs))
	}
}

func TestRun_EmptyDetectors(t *testing.T) {
	findings, errs := detect.Run(context.Background(), nil)
	if len(findings) != 0 || len(errs) != 0 {
		t.Errorf("expected empty results; got findings=%d errs=%d", len(findings), len(errs))
	}
}
```

- [ ] **Step 2: Write report tests**

Create `internal/detect/report_test.go`:

```go
package detect_test

import (
	"encoding/json"
	"testing"

	"go-hook-mcp-api/internal/detect"
)

func TestExitCode_NoFindings(t *testing.T) {
	if detect.ExitCode(nil, false) != 0 {
		t.Error("exit code should be 0 for empty findings")
	}
}

func TestExitCode_InfoOnlyIsClean(t *testing.T) {
	findings := []detect.Finding{{Severity: detect.SeverityInfo}}
	if detect.ExitCode(findings, false) != 0 {
		t.Error("info-only findings should not trigger exit 1")
	}
}

func TestExitCode_MediumTriggers(t *testing.T) {
	findings := []detect.Finding{{Severity: detect.SeverityMedium}}
	if detect.ExitCode(findings, false) != 1 {
		t.Error("medium severity should produce exit 1")
	}
}

func TestExitCode_StrictModeInfoTriggers(t *testing.T) {
	findings := []detect.Finding{{Severity: detect.SeverityInfo}}
	if detect.ExitCode(findings, true) != 1 {
		t.Error("strict mode: info severity should produce exit 1")
	}
}

func TestBuildReport_ValidJSON(t *testing.T) {
	findings := []detect.Finding{
		{Tool: "cursor", Module: "filesystem", Signal: "dir found", Severity: detect.SeverityInfo},
	}
	r := detect.BuildReport(findings, []string{"filesystem"}, 0)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Error("empty JSON output")
	}
	var check map[string]any
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if check["schema_version"] != "1.0" {
		t.Errorf("schema_version = %v; want 1.0", check["schema_version"])
	}
}

func TestToolsDetected_Deduplication(t *testing.T) {
	findings := []detect.Finding{
		{Tool: "cursor", Severity: detect.SeverityInfo},
		{Tool: "cursor", Severity: detect.SeverityMedium},
		{Tool: "codex", Severity: detect.SeverityHigh},
	}
	r := detect.BuildReport(findings, nil, 1)
	tools := r.Summary.ToolsDetected
	seen := map[string]bool{}
	for _, t := range tools {
		if seen[t] {
			panic("duplicate tool: " + t)
		}
		seen[t] = true
	}
}
```

- [ ] **Step 3: Run to verify fail**

```bash
go test ./internal/detect/... -run TestRun,TestExitCode,TestBuildReport,TestToolsDetected 2>&1 | head -5
```
Expected: FAIL

- [ ] **Step 4: Create runner.go**

```go
// internal/detect/runner.go
package detect

import (
	"context"
	"sync"
)

// Run executes all detectors in parallel and returns aggregated findings and errors.
func Run(ctx context.Context, detectors []Detector) ([]Finding, []error) {
	if len(detectors) == 0 {
		return nil, nil
	}
	type result struct {
		findings []Finding
		err      error
	}
	results := make([]result, len(detectors))
	var wg sync.WaitGroup
	for i, d := range detectors {
		wg.Add(1)
		go func(i int, d Detector) {
			defer wg.Done()
			f, err := d.Detect(ctx)
			results[i] = result{f, err}
		}(i, d)
	}
	wg.Wait()

	var allFindings []Finding
	var allErrors []error
	for _, r := range results {
		allFindings = append(allFindings, r.findings...)
		if r.err != nil {
			allErrors = append(allErrors, r.err)
		}
	}
	return allFindings, allErrors
}
```

- [ ] **Step 5: Create report.go**

```go
// internal/detect/report.go
package detect

import (
	"os"
	"runtime"
	"sort"
	"time"
)

type Report struct {
	SchemaVersion string    `json:"schema_version"`
	ScannedAt     time.Time `json:"scanned_at"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	ExitCode      int       `json:"exit_code"`
	Findings      []Finding `json:"findings"`
	Summary       *Summary  `json:"summary"`
}

// ExitCode returns the process exit code for a set of findings.
// Exit 1 if any finding is medium or high (or any severity in strict mode).
// Exit 0 otherwise.
func ExitCode(findings []Finding, strict bool) int {
	for _, f := range findings {
		if strict || f.Severity == SeverityMedium || f.Severity == SeverityHigh {
			return 1
		}
	}
	return 0
}

// BuildReport constructs a Report for the given findings.
func BuildReport(findings []Finding, modulesRan []string, durationMS int64) *Report {
	hostname, _ := os.Hostname()
	toolSet := map[string]struct{}{}
	for _, f := range findings {
		if f.Tool != "" {
			toolSet[f.Tool] = struct{}{}
		}
	}
	tools := make([]string, 0, len(toolSet))
	for t := range toolSet {
		tools = append(tools, t)
	}
	sort.Strings(tools)

	if findings == nil {
		findings = []Finding{}
	}

	return &Report{
		SchemaVersion: "1.0",
		ScannedAt:     time.Now().UTC(),
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Findings:      findings,
		Summary: &Summary{
			FindingsCount: len(findings),
			ToolsDetected: tools,
		},
	}
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/detect/...
```
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/detect/runner.go internal/detect/report.go internal/detect/runner_test.go internal/detect/report_test.go
git commit -m "feat(detect): add parallel runner and report builder"
```

---

## Task 6: Create detector infrastructure and claude.go

**Files:**
- Create: `internal/detect/detectors/testhelper_test.go`
- Create: `internal/detect/detectors/claude.go`
- Create: `internal/detect/detectors/claude_test.go`

The detectors use function injection for filesystem and process operations so tests can mock them without hitting the real OS.

- [ ] **Step 1: Write claude detector tests**

Create `internal/detect/detectors/claude_test.go`:

```go
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
		func(string) error { return errors.New("not found") }, // statFn
		func() ([]string, error) { return nil, nil },          // processFn
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
		func(string) error { return nil }, // statFn: all paths exist
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
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./internal/detect/detectors/... 2>&1 | head -5
```
Expected: FAIL

- [ ] **Step 3: Create claude.go**

```go
// internal/detect/detectors/claude.go
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

func (d *ClaudeDetector) Detect(ctx context.Context) ([]detect.Finding, error) {
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
```

- [ ] **Step 4: Create listProcessNames helper (shared by all detectors)**

Create `internal/detect/detectors/process.go`:

```go
// internal/detect/detectors/process.go
package detectors

import (
	"os/exec"
	"runtime"
	"strings"
)

// listProcessNames returns a list of running process names on the current OS.
func listProcessNames() ([]string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("tasklist", "/fo", "csv", "/nh")
	default:
		cmd = exec.Command("ps", "-eo", "comm")
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Windows tasklist CSV: "process.exe","PID",...  — extract first field.
		if runtime.GOOS == "windows" {
			line = strings.Trim(strings.SplitN(line, ",", 2)[0], `"`)
			line = strings.TrimSuffix(line, ".exe")
		}
		names = append(names, line)
	}
	return names, nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/detect/detectors/... -run TestClaude
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/detect/detectors/
git commit -m "feat(detect): add claude detector + process lister"
```

---

## Task 7: cursor.go, codex.go, opencode.go detectors

**Files:**
- Create: `internal/detect/detectors/cursor.go`
- Create: `internal/detect/detectors/cursor_test.go`
- Create: `internal/detect/detectors/codex.go`
- Create: `internal/detect/detectors/codex_test.go`
- Create: `internal/detect/detectors/opencode.go`
- Create: `internal/detect/detectors/opencode_test.go`

- [ ] **Step 1: Create cursor.go**

```go
// internal/detect/detectors/cursor.go
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
```

- [ ] **Step 2: Create cursor_test.go**

```go
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
```

- [ ] **Step 3: Create codex.go (with OTEL harvest)**

```go
// internal/detect/detectors/codex.go
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

// harvestOTEL reads a Codex config.toml and extracts [otel] section fields.
// Returns a high-severity finding if an active exporter is configured.
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
	endpoint := meta["endpoint"]
	return &detect.Finding{
		Tool: "codex", Module: "otel-harvest",
		Signal:   "otel exporter configured",
		Path:     path,
		Severity: detect.SeverityHigh,
		Metadata: map[string]string{"exporter": exporter, "endpoint": endpoint},
	}
}
```

- [ ] **Step 4: Create codex_test.go**

```go
package detectors_test

import (
	"context"
	"errors"
	"testing"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestCodex_OTELHarvest_Configured(t *testing.T) {
	toml := []byte("[otel]\nexporter = \"otlp-http\"\nendpoint = \"https://collector.internal\"\n")
	d := detectors.NewCodexDetector(
		func(string) error { return nil },
		func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "config.toml") { return toml, nil }
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
```

Add `"strings"` import to codex_test.go.

- [ ] **Step 5: Create opencode.go**

```go
// internal/detect/detectors/opencode.go
package detectors

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

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
	for _, dir := range openCodeDirs() {
		if d.statFn(dir) == nil {
			findings = append(findings, detect.Finding{
				Tool: "opencode", Module: "filesystem",
				Signal: "config directory found", Path: dir,
				Severity: detect.SeverityInfo,
			})
		}
	}
	pluginPath := openCodePluginPath()
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

func openCodeDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return []string{filepath.Join(home, ".config", "opencode")}
	default:
		return []string{filepath.Join(home, ".config", "opencode")}
	}
}

func openCodePluginPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "opencode", "node_modules", "opencode-plugin-otel")
}
```

- [ ] **Step 6: Create opencode_test.go**

```go
package detectors_test

import (
	"context"
	"errors"
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
```

Add `"strings"` import.

- [ ] **Step 7: Run all detector tests**

```bash
go test ./internal/detect/...
```
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/detect/detectors/
git commit -m "feat(detect): add cursor, codex (otel-harvest), opencode detectors"
```

---

## Task 8: packages.go and network.go detectors

**Files:**
- Create: `internal/detect/detectors/packages.go`
- Create: `internal/detect/detectors/packages_test.go`
- Create: `internal/detect/detectors/network.go`
- Create: `internal/detect/detectors/network_test.go`

- [ ] **Step 1: Create packages.go**

```go
// internal/detect/detectors/packages.go
package detectors

import (
	"context"
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
```

Add `"encoding/json"` import.

- [ ] **Step 2: Create packages_test.go**

```go
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
```

- [ ] **Step 3: Create network.go**

```go
// internal/detect/detectors/network.go
package detectors

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"time"

	"go-hook-mcp-api/internal/detect"
)

var mcpProbePorts = []string{"3000", "5000", "8000", "8080", "11434"}

var aiDomains = []string{
	"api.anthropic.com",
	"api.openai.com",
	"api2.cursor.sh",
	"repo42.cursor.sh",
	"generativelanguage.googleapis.com",
	"huggingface.co",
	"ollama.ai",
}

type NetworkDetector struct {
	dialFn      func(network, addr string, timeout time.Duration) (net.Conn, error)
	connScanFn  func() ([]string, error) // returns list of remote addresses
}

func NewNetworkDetector(dialFn func(string, string, time.Duration) (net.Conn, error), connFn func() ([]string, error)) *NetworkDetector {
	return &NetworkDetector{dialFn: dialFn, connScanFn: connFn}
}

func NewNetwork() *NetworkDetector {
	return NewNetworkDetector(
		func(n, a string, t time.Duration) (net.Conn, error) { return net.DialTimeout(n, a, t) },
		scanActiveConnections,
	)
}

func (d *NetworkDetector) Name() string { return "network" }

func (d *NetworkDetector) Detect(ctx context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding

	// MCP port probe
	for _, port := range mcpProbePorts {
		addr := "127.0.0.1:" + port
		conn, err := d.dialFn("tcp", addr, 2*time.Second)
		if err != nil {
			continue
		}
		probe := `{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}`
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		conn.Write([]byte(probe))
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		conn.Close()
		if n > 0 && json.Valid(buf[:n]) {
			findings = append(findings, detect.Finding{
				Tool: "unknown", Module: "network",
				Signal:   "mcp server responding",
				Path:     addr,
				Severity: detect.SeverityMedium,
				Metadata: map[string]string{"port": port},
			})
		}
	}

	// Active connections to AI domains
	remotes, _ := d.connScanFn()
	for _, remote := range remotes {
		for _, domain := range aiDomains {
			if strings.Contains(remote, domain) {
				findings = append(findings, detect.Finding{
					Tool: domainToTool(domain), Module: "network",
					Signal:   "active connection to ai api",
					Path:     remote,
					Severity: detect.SeverityHigh,
				})
				break
			}
		}
	}
	return findings, nil
}

func domainToTool(domain string) string {
	switch {
	case strings.Contains(domain, "anthropic"):
		return "claude"
	case strings.Contains(domain, "openai"):
		return "codex"
	case strings.Contains(domain, "cursor"):
		return "cursor"
	default:
		return "unknown"
	}
}

func scanActiveConnections() ([]string, error) {
	// Stub: real implementation would parse /proc/net/tcp (Linux),
	// run netstat -an (macOS/Windows). Returns empty for safety on unsupported platforms.
	return nil, nil
}
```

- [ ] **Step 4: Create network_test.go**

```go
package detectors_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestNetwork_MCPProbe_NoResponse(t *testing.T) {
	d := detectors.NewNetworkDetector(
		func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("refused") },
		func() ([]string, error) { return nil, nil },
	)
	findings, err := d.Detect(context.Background())
	if err != nil || len(findings) != 0 {
		t.Errorf("expected 0 findings; got %d err=%v", len(findings), err)
	}
}

func TestNetwork_ActiveConnection_HighSeverity(t *testing.T) {
	d := detectors.NewNetworkDetector(
		func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("refused") },
		func() ([]string, error) { return []string{"api.anthropic.com:443"}, nil },
	)
	findings, _ := d.Detect(context.Background())
	if len(findings) == 0 || findings[0].Severity != detect.SeverityHigh {
		t.Error("expected high finding for active anthropic connection")
	}
}
```

- [ ] **Step 5: Run all tests**

```bash
go test ./internal/detect/...
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/detect/detectors/
git commit -m "feat(detect): add packages and network detectors"
```

---

## Task 9: Create cmd/detect/main.go

**Files:**
- Create: `cmd/detect/main.go`

- [ ] **Step 1: Create main.go with hook and scan subcommands**

```go
// cmd/detect/main.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
	"go-hook-mcp-api/internal/hook"
	"go-hook-mcp-api/internal/otelexport"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cotel <hook|scan|version>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		os.Exit(runHook())
	case "scan":
		os.Exit(runScan(os.Args[2:]))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

// runHook reads a hook payload from stdin, redacts secrets, calls the inspector,
// emits an OTEL span, and returns the exit code.
// exit 0 = allow, exit 2 = blocked by inspector.
func runHook() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var payload hook.HookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "cotel hook: decode payload: %v\n", err)
		return 2
	}
	payload.Normalize()

	inspector := detect.NoopInspector{}
	result := inspector.Inspect(ctx, detect.InspectInput{
		ToolName:  payload.ToolName,
		Command:   payload.Command,
		Cwd:       payload.Cwd,
		SessionID: payload.SessionID,
	})

	endpoint := envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if endpoint != "" {
		providers, cleanup, err := otelexport.SetupSync(ctx, otelexport.Config{
			ServiceName:  envOrDefault("OTEL_SERVICE_NAME", "cotel"),
			OTLPEndpoint: endpoint,
			OTLPHeaders:  os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		})
		if err == nil {
			defer cleanup()
			tracer := otelexport.NewTracer(providers.TracerProvider, "cotel")
			logger := otelexport.NewSlogLogger(providers.LoggerProvider, "cotel")

			_, span := tracer.Start(ctx, "cotel.hook.inspect")
			span.End()

			_, span2 := tracer.Start(ctx, "cotel.hook.ingest")
			attrs := hook.BuildAttributes(payload.EventType, payload)
			logArgs := make([]any, 0, len(attrs))
			for _, kv := range attrs {
				logArgs = append(logArgs, slog.String(string(kv.Key), kv.Value.Emit()))
			}
			logger.InfoContext(ctx, hook.PayloadJSON(payload), logArgs...)
			span2.End()
		}
	}

	if !result.Allow {
		fmt.Fprintf(os.Stderr, "cotel: command blocked by inspector: %s\n", result.Reason)
		return 2
	}
	return 0
}

// runScan runs all detectors and outputs results.
func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	force := fs.Bool("force", false, "force re-scan ignoring daily cache")
	output := fs.String("output", "json", "output format: json|text")
	strict := fs.Bool("strict", false, "exit 1 for any finding including info")
	configPath := fs.String("config", "", "explicit path to settings.json directory")
	_ = fs.Parse(args)

	settingsPath := detect.ResolveSettingsPath(*configPath)
	settings, _ := detect.LoadSettings(settingsPath)

	if detect.ShouldSkip(settings, *force) {
		if settings.LastSummary != nil {
			fmt.Fprintf(os.Stderr, "cotel scan: cached (last run %s). Use --force to re-run.\n",
				settings.LastRunAt.Format(time.RFC3339))
		}
		return settings.LastExitCode
	}

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(settings.ScanTimeoutSecs)*time.Second)
	defer cancel()

	start := time.Now()
	allDetectors := []detect.Detector{
		detectors.NewClaude(),
		detectors.NewCursor(),
		detectors.NewCodex(),
		detectors.NewOpenCode(),
		detectors.NewPackages(),
		detectors.NewNetwork(),
	}

	findings, _ := detect.Run(ctx, allDetectors)
	exitCode := detect.ExitCode(findings, *strict)
	report := detect.BuildReport(findings, nil, time.Since(start).Milliseconds())
	report.ExitCode = exitCode

	switch *output {
	case "text":
		printText(report)
	default:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report)
	}

	settings.LastRunAt = time.Now().UTC()
	settings.LastExitCode = exitCode
	settings.LastSummary = report.Summary
	_ = detect.SaveSettings(settingsPath, settings)

	return exitCode
}

func printText(r *detect.Report) {
	fmt.Printf("cotel scan — %s %s/%s\n", r.ScannedAt.Format(time.RFC3339), r.OS, r.Arch)
	fmt.Printf("findings: %d  tools: %v\n\n", r.Summary.FindingsCount, r.Summary.ToolsDetected)
	for _, f := range r.Findings {
		fmt.Printf("[%s] %s / %s — %s (%s)\n", f.Severity, f.Tool, f.Module, f.Signal, f.Path)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 2: Build to verify compilation**

```bash
go build ./cmd/detect/
```
Expected: success, produces `detect` binary in current dir. Clean up: `rm -f detect`

- [ ] **Step 3: Smoke test hook subcommand**

```bash
echo '{"event_type":"post_tool_use","tool_name":"Bash","command":"echo hi"}' | go run ./cmd/detect hook
echo "exit: $?"
```
Expected: exit 0 (OTEL endpoint not configured, skips telemetry, allows command)

- [ ] **Step 4: Smoke test scan subcommand**

```bash
go run ./cmd/detect scan --output text 2>/dev/null
echo "exit: $?"
```
Expected: exit 0 or 1 (depends on what's installed on the machine), no panic

- [ ] **Step 5: Commit**

```bash
git add cmd/detect/
git commit -m "feat(detect): add cotel CLI with hook and scan subcommands"
```

---

## Task 10: Remove cmd/audit, update Makefile, docker-compose, settings

**Files:**
- Delete: `cmd/audit/`
- Modify: `Makefile`
- Modify: `docker-compose.yml` (project root)
- Modify: `claude-managed-settings.json` (project root)

- [ ] **Step 1: Delete cmd/audit**

```bash
rm -rf cmd/audit/
```

- [ ] **Step 2: Run full test suite to confirm nothing broken**

```bash
go test ./...
```
Expected: PASS (no references to deleted cmd/audit remain)

- [ ] **Step 3: Update Makefile**

Replace the entire Makefile content:

```makefile
.PHONY: build build-all test test-v lint clean

build:
	go build -o bin/cotel      ./cmd/detect
	go build -o bin/mcp-server ./cmd/mcp

build-all:
	GOOS=linux   GOARCH=amd64  go build -o bin/cotel-linux-amd64       ./cmd/detect
	GOOS=darwin  GOARCH=arm64  go build -o bin/cotel-darwin-arm64      ./cmd/detect
	GOOS=windows GOARCH=amd64  go build -o bin/cotel-windows-amd64.exe ./cmd/detect

test:
	go test ./...

test-v:
	go test -v ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
```

- [ ] **Step 4: Update docker-compose.yml — remove audit-service service**

In `docker-compose.yml` at the project root, find and remove the entire `audit-service` block (the service definition including its `build`, `env_file`, `depends_on`, `ports`, and `restart` entries). Keep `clickhouse`, `otel-collector`, and `mcp-server` services unchanged.

- [ ] **Step 5: Update claude-managed-settings.json — switch to command-based hooks**

Replace the content of `claude-managed-settings.json` (project root) with:

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_TRACES_EXPORTER": "otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4318",
    "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer CHANGE_ME",
    "OTEL_SERVICE_NAME": "claude-code"
  },
  "forceRemoteSettingsRefresh": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": ".*",
        "hooks": [
          {
            "type": "command",
            "command": "cotel hook",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": ".*",
        "hooks": [
          {
            "type": "command",
            "command": "cotel hook",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUseFailure": [
      {
        "matcher": ".*",
        "hooks": [
          {
            "type": "command",
            "command": "cotel hook",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

> **Note:** Verify the exact `"type": "command"` format against Claude Code docs before deploying — the project has only used `"type": "http"` before. Test with one hook first.

- [ ] **Step 6: Build final binaries**

```bash
make build
```
Expected: `bin/cotel` and `bin/mcp-server` produced, no errors

- [ ] **Step 7: Run full test suite**

```bash
go test ./...
```
Expected: all PASS

- [ ] **Step 8: Final commit**

```bash
git add Makefile cmd/audit/ ../../claude-managed-settings.json ../../docker-compose.yml
git commit -m "feat(cotel): remove audit-service, wire command-based hooks, update Makefile"
```

---

## Self-Review

**Spec coverage check:**
- ✅ Unified binary `cotel` with `hook` and `scan` subcommands — Task 9
- ✅ Replace audit-service HTTP → command-based — Task 10
- ✅ `internal/audit` → `internal/hook` — Task 1
- ✅ `SetupSync` for short-lived processes — Task 2
- ✅ NoopInspector + CommandInspector interface — Task 3
- ✅ `~/.cotel/settings.json` cache (daily skip + `--force`) — Task 4
- ✅ Parallel runner — Task 5
- ✅ JSON report + exit codes + `--strict` — Task 5
- ✅ Claude/Cursor/Codex/OpenCode/packages/network detectors — Tasks 6-8
- ✅ OTEL harvest for Codex and OpenCode — Tasks 7
- ✅ Cross-platform paths via `runtime.GOOS` switch — Tasks 6-8
- ✅ `--force`, `--output`, `--strict`, `--config` flags — Task 9
- ✅ Makefile `build-all` for 3 platforms — Task 10
- ✅ docker-compose cleanup — Task 10
- ✅ `claude-managed-settings.json` updated — Task 10

**Placeholder scan:** No TBDs. The network `scanActiveConnections` stub is intentional and documented inline.

**Type consistency:** `hook.BuildAttributes`, `hook.PayloadJSON`, `hook.HookPayload` used in Task 9 match definitions in Task 1. `detect.Finding`, `detect.Severity`, `detect.NoopInspector`, `detect.Run`, `detect.BuildReport`, `detect.ExitCode` all defined before use.

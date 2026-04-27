package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
	"go-hook-mcp-api/internal/hook"
	"go-hook-mcp-api/internal/otelexport"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "hook":
		os.Exit(runHook(os.Args[2:]))
	case "scan":
		os.Exit(runScan(os.Args[2:]))
	case "version":
		fmt.Printf("cotel-detect %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: detect <subcommand> [flags]\n\nSubcommands:\n  hook     Process a Claude Code hook event from stdin\n  scan     Run all AI-tool detectors\n  version  Print version\n")
}

// runHook reads a HookPayload from stdin, normalises it, runs the NoopInspector,
// optionally emits OTEL spans, and returns 0 (allow) or 2 (block).
func runHook(args []string) int {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	_ = fs.Parse(args)

	ctx := context.Background()

	// Decode payload from stdin.
	var payload hook.HookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "detect hook: decode stdin: %v\n", err)
		return 2
	}

	// Normalize derives Repository, redacts secrets, etc.
	payload.Normalize()

	// Run the inspector — always NoopInspector for now.
	inspector := detect.NoopInspector{}
	input := detect.InspectInput{
		ToolName:  payload.ToolName,
		Command:   payload.Command,
		Cwd:       payload.Cwd,
		SessionID: payload.SessionID,
	}
	result := inspector.Inspect(ctx, input)

	// Emit OTEL spans only when an endpoint can be resolved. Resolution order:
	//   1. OTEL_EXPORTER_OTLP_ENDPOINT — set by Claude Code in the inherited env.
	//   2. .gemini/settings.json telemetry.otlpEndpoint — Gemini CLI configures
	//      its own SDK from this file but does NOT propagate the endpoint as an
	//      env var to hook subprocesses. Discovered via $GEMINI_PROJECT_DIR.
	// If neither is set, the hook still runs and returns 0/2 — telemetry is
	// just skipped.
	endpoint := envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", discoverGeminiOTLPEndpoint())
	if endpoint != "" {
		cfg := otelexport.Config{
			ServiceName:  envOrDefault("OTEL_SERVICE_NAME", "cotel-detect"),
			OTLPEndpoint: endpoint,
			OTLPHeaders:  os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		}
		providers, cleanup, err := otelexport.SetupSync(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "detect hook: otel setup: %v\n", err)
			// Non-fatal — continue without telemetry.
		} else {
			defer cleanup()
			emitHookSpans(ctx, providers, payload, result)
		}
	}

	if !result.Allow {
		return 2
	}
	return 0
}

func emitHookSpans(ctx context.Context, providers *otelexport.Providers, payload hook.HookPayload, result detect.InspectResult) {
	// SetupSync already called otel.SetTracerProvider; use the global.
	_ = providers // TracerProvider is registered globally by SetupSync
	tracer := otel.Tracer("cotel-detect")

	attrs := hook.BuildAttributes("hook", payload)
	attrs = append(attrs,
		attribute.String("inspect.severity", result.Severity),
		attribute.Bool("inspect.allow", result.Allow),
	)
	if result.Reason != "" {
		attrs = append(attrs, attribute.String("inspect.reason", result.Reason))
	}

	// Span: inspect
	_, inspectSpan := tracer.Start(ctx, "cotel.hook.inspect")
	inspectSpan.SetAttributes(attrs...)
	if !result.Allow {
		inspectSpan.SetStatus(codes.Error, "blocked by inspector")
	}
	inspectSpan.End()

	// Span: ingest (always emitted to record the event was received)
	_, ingestSpan := tracer.Start(ctx, "cotel.hook.ingest")
	ingestSpan.SetAttributes(
		attribute.String("audit.event_type", payload.EventType),
		attribute.String("audit.tool_name", payload.ToolName),
		attribute.String("audit.session_id", payload.SessionID),
		attribute.String("audit.repository", payload.Repository),
	)
	ingestSpan.End()
}

// runScan loads settings, runs all detectors, prints a report, saves settings,
// and returns the appropriate exit code.
func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	force := fs.Bool("force", false, "ignore 24h cooldown and run even if recently scanned")
	output := fs.String("output", "text", "output format: json or text")
	strict := fs.Bool("strict", false, "exit 1 on any finding regardless of severity")
	configDir := fs.String("config", "", "directory containing settings.json (default: auto-detect)")
	_ = fs.Parse(args)

	settingsPath := detect.ResolveSettingsPath(*configDir)
	settings, _ := detect.LoadSettings(settingsPath)

	if detect.ShouldSkip(settings, *force) {
		fmt.Fprintf(os.Stderr, "scan: skipping (last run %s ago, use --force to override)\n",
			time.Since(settings.LastRunAt).Round(time.Second))
		return settings.LastExitCode
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.ScanTimeoutSecs)*time.Second)
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

	findings, errs := detect.Run(ctx, allDetectors)
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "scan: detector error: %v\n", err)
	}

	durationMS := time.Since(start).Milliseconds()
	moduleNames := make([]string, len(allDetectors))
	for i, d := range allDetectors {
		moduleNames[i] = d.Name()
	}

	report := detect.BuildReport(findings, moduleNames, durationMS)
	exitCode := detect.ExitCode(findings, *strict)
	report.ExitCode = exitCode

	switch *output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "scan: encode report: %v\n", err)
			return 1
		}
	default:
		printTextReport(report)
	}

	// Persist run metadata for next cooldown check.
	settings.LastRunAt = time.Now().UTC()
	settings.LastExitCode = exitCode
	settings.LastSummary = report.Summary
	if err := detect.SaveSettings(settingsPath, settings); err != nil {
		fmt.Fprintf(os.Stderr, "scan: save settings: %v\n", err)
		// Non-fatal.
	}

	return exitCode
}

func printTextReport(r *detect.Report) {
	fmt.Printf("cotel-detect scan  %s\n", r.ScannedAt.Format(time.RFC3339))
	fmt.Printf("host: %s  os: %s/%s\n\n", r.Hostname, r.OS, r.Arch)

	if len(r.Findings) == 0 {
		fmt.Println("No findings.")
	} else {
		fmt.Printf("Findings (%d):\n", len(r.Findings))
		for _, f := range r.Findings {
			path := ""
			if f.Path != "" {
				path = "  path=" + f.Path
			}
			fmt.Printf("  [%s] %s / %s: %s%s\n", f.Severity, f.Tool, f.Module, f.Signal, path)
		}
	}

	fmt.Printf("\nExit code: %d\n", r.ExitCode)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// discoverGeminiOTLPEndpoint walks the Gemini settings search path and returns
// the first telemetry.otlpEndpoint it finds with telemetry.enabled=true.
// Returns "" when no usable settings are found.
//
// This exists because Gemini CLI configures its OTEL SDK from settings.json
// internally and does not export OTEL_* / GEMINI_TELEMETRY_* env vars to hook
// subprocesses, so cotel cannot otherwise discover where to send spans.
func discoverGeminiOTLPEndpoint() string {
	for _, root := range geminiSearchRoots() {
		if ep := readGeminiOTLPEndpoint(root); ep != "" {
			return ep
		}
	}
	return ""
}

// geminiSearchRoots returns directories to scan for .gemini/settings.json in
// priority order:
//
//  1. $GEMINI_PROJECT_DIR — Gemini CLI sets this on hook subprocesses.
//  2. Current working directory — project-scoped settings.
//  3. User home directory — Gemini's user-level config (~/.gemini/settings.json).
//
// Cross-platform note: os.UserHomeDir() returns $HOME on Unix and %USERPROFILE%
// on Windows, and filepath.Join uses the platform's path separator, so this
// works identically on macOS, Linux, and Windows without OS-specific branches.
func geminiSearchRoots() []string {
	var roots []string
	if v := os.Getenv("GEMINI_PROJECT_DIR"); v != "" {
		roots = append(roots, v)
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	return roots
}

// readGeminiOTLPEndpoint reads <root>/.gemini/settings.json and returns
// telemetry.otlpEndpoint when telemetry.enabled is true. Returns "" on any
// error (missing file, malformed JSON, telemetry disabled, empty endpoint).
func readGeminiOTLPEndpoint(root string) string {
	if root == "" {
		return ""
	}
	path := filepath.Join(root, ".gemini", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s struct {
		Telemetry struct {
			Enabled      bool   `json:"enabled"`
			OTLPEndpoint string `json:"otlpEndpoint"`
		} `json:"telemetry"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return ""
	}
	if !s.Telemetry.Enabled || s.Telemetry.OTLPEndpoint == "" {
		return ""
	}
	return s.Telemetry.OTLPEndpoint
}

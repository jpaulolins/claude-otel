package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGeminiSettings creates <root>/.gemini/settings.json with the given body.
func writeGeminiSettings(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".gemini")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

func TestReadGeminiOTLPEndpoint_Found(t *testing.T) {
	root := t.TempDir()
	writeGeminiSettings(t, root, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://example:4318"}}`)
	if got := readGeminiOTLPEndpoint(root); got != "http://example:4318" {
		t.Errorf("got %q; want http://example:4318", got)
	}
}

func TestReadGeminiOTLPEndpoint_TelemetryDisabled(t *testing.T) {
	root := t.TempDir()
	writeGeminiSettings(t, root, `{"telemetry":{"enabled":false,"otlpEndpoint":"http://example:4318"}}`)
	if got := readGeminiOTLPEndpoint(root); got != "" {
		t.Errorf("got %q; want empty (telemetry disabled)", got)
	}
}

func TestReadGeminiOTLPEndpoint_EmptyEndpoint(t *testing.T) {
	root := t.TempDir()
	writeGeminiSettings(t, root, `{"telemetry":{"enabled":true,"otlpEndpoint":""}}`)
	if got := readGeminiOTLPEndpoint(root); got != "" {
		t.Errorf("got %q; want empty (endpoint empty)", got)
	}
}

func TestReadGeminiOTLPEndpoint_NoFile(t *testing.T) {
	if got := readGeminiOTLPEndpoint(t.TempDir()); got != "" {
		t.Errorf("got %q; want empty (no settings.json)", got)
	}
}

func TestReadGeminiOTLPEndpoint_BadJSON(t *testing.T) {
	root := t.TempDir()
	writeGeminiSettings(t, root, `{not json`)
	if got := readGeminiOTLPEndpoint(root); got != "" {
		t.Errorf("got %q; want empty (malformed JSON)", got)
	}
}

func TestReadGeminiOTLPEndpoint_EmptyRoot(t *testing.T) {
	if got := readGeminiOTLPEndpoint(""); got != "" {
		t.Errorf("got %q; want empty (empty root)", got)
	}
}

// TestDiscover_HomeFallback exercises the user-home fallback path: no
// GEMINI_PROJECT_DIR set, cwd has no .gemini/settings.json, but
// $HOME/.gemini/settings.json exists. This is the path that should hit
// regardless of OS — on Windows os.UserHomeDir() reads %USERPROFILE%, which
// we set alongside HOME in the test.
func TestDiscover_HomeFallback(t *testing.T) {
	homeRoot := t.TempDir()
	writeGeminiSettings(t, homeRoot, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://home:4318"}}`)

	// cwd that does NOT contain .gemini, so the cwd lookup misses.
	cwd := t.TempDir()
	t.Chdir(cwd)

	t.Setenv("GEMINI_PROJECT_DIR", "")
	t.Setenv("HOME", homeRoot)         // Unix
	t.Setenv("USERPROFILE", homeRoot)  // Windows

	if got := discoverGeminiOTLPEndpoint(); got != "http://home:4318" {
		t.Errorf("got %q; want http://home:4318 (home fallback)", got)
	}
}

// TestDiscover_PrecedenceProjectDirOverHome ensures that when both
// $GEMINI_PROJECT_DIR/.gemini/settings.json and $HOME/.gemini/settings.json
// have endpoints, the project-dir wins.
func TestDiscover_PrecedenceProjectDirOverHome(t *testing.T) {
	projectRoot := t.TempDir()
	writeGeminiSettings(t, projectRoot, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://project:4318"}}`)

	homeRoot := t.TempDir()
	writeGeminiSettings(t, homeRoot, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://home:4318"}}`)

	cwd := t.TempDir()
	t.Chdir(cwd)

	t.Setenv("GEMINI_PROJECT_DIR", projectRoot)
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	if got := discoverGeminiOTLPEndpoint(); got != "http://project:4318" {
		t.Errorf("got %q; want http://project:4318 (GEMINI_PROJECT_DIR should win over home)", got)
	}
}

// TestDiscover_PrecedenceCwdOverHome ensures that when GEMINI_PROJECT_DIR is
// unset but the current working directory has settings, cwd wins over home.
func TestDiscover_PrecedenceCwdOverHome(t *testing.T) {
	cwd := t.TempDir()
	writeGeminiSettings(t, cwd, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://cwd:4318"}}`)
	t.Chdir(cwd)

	homeRoot := t.TempDir()
	writeGeminiSettings(t, homeRoot, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://home:4318"}}`)

	t.Setenv("GEMINI_PROJECT_DIR", "")
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	if got := discoverGeminiOTLPEndpoint(); got != "http://cwd:4318" {
		t.Errorf("got %q; want http://cwd:4318 (cwd should win over home)", got)
	}
}

// TestDiscover_AllMissing returns empty when no settings.json is found
// anywhere in the search path.
func TestDiscover_AllMissing(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	homeRoot := t.TempDir()
	t.Setenv("GEMINI_PROJECT_DIR", "")
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	if got := discoverGeminiOTLPEndpoint(); got != "" {
		t.Errorf("got %q; want empty (no settings anywhere)", got)
	}
}

// TestDiscover_SkipsDisabledFallsThrough verifies that a settings.json with
// telemetry.enabled=false at the project level does NOT block the home
// fallback from being consulted.
func TestDiscover_SkipsDisabledFallsThrough(t *testing.T) {
	projectRoot := t.TempDir()
	writeGeminiSettings(t, projectRoot, `{"telemetry":{"enabled":false,"otlpEndpoint":"http://project:4318"}}`)

	homeRoot := t.TempDir()
	writeGeminiSettings(t, homeRoot, `{"telemetry":{"enabled":true,"otlpEndpoint":"http://home:4318"}}`)

	cwd := t.TempDir()
	t.Chdir(cwd)

	t.Setenv("GEMINI_PROJECT_DIR", projectRoot)
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	if got := discoverGeminiOTLPEndpoint(); got != "http://home:4318" {
		t.Errorf("got %q; want http://home:4318 (project disabled, home enabled)", got)
	}
}

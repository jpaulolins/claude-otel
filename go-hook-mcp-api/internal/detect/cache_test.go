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
		Version:         1,
		OTLPEndpoint:    "http://localhost:4318",
		LastExitCode:    1,
		LastRunAt:       time.Now().UTC().Truncate(time.Second),
		LastSummary:     &detect.Summary{FindingsCount: 2, ToolsDetected: []string{"cursor"}},
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

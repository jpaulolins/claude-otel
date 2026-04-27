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
// If explicitDir is non-empty, uses that directory. Otherwise checks
// ./settings.json first, then ~/.cotel/settings.json.
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

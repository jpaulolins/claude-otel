package audit

import (
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

func TestBuildAttributes_FullPayload(t *testing.T) {
	p := HookPayload{
		EventType:      "claude_code.bash.post_tool_use",
		UserID:         "jon@empresa.com",
		SessionID:      "abc123",
		ToolUseID:      "toolu_01ABC123",
		ToolName:       "Bash",
		Command:        "npm test",
		Cwd:            "/repo/app",
		PermissionMode: "default",
		Success:        true,
		ToolResponse:   &ToolResponse{ExitCode: 0, Stdout: "ok", Stderr: ""},
	}

	attrs := BuildAttributes("post_tool_use", p)
	m := attrMap(attrs)

	cases := []struct {
		key  string
		want string
	}{
		{"audit.event_kind", "post_tool_use"},
		{"audit.event_type", "claude_code.bash.post_tool_use"},
		{"audit.user_id", "jon@empresa.com"},
		{"audit.session_id", "abc123"},
		{"audit.tool_use_id", "toolu_01ABC123"},
		{"audit.tool_name", "Bash"},
		{"audit.command", "npm test"},
		{"audit.cwd", "/repo/app"},
		{"audit.permission_mode", "default"},
	}
	for _, tc := range cases {
		if got := m[tc.key]; got != tc.want {
			t.Errorf("%s = %q; want %q", tc.key, got, tc.want)
		}
	}

	// check bool
	for _, kv := range attrs {
		if string(kv.Key) == "audit.success" {
			if !kv.Value.AsBool() {
				t.Error("audit.success should be true")
			}
		}
		if string(kv.Key) == "audit.exit_code" {
			if kv.Value.AsInt64() != 0 {
				t.Errorf("audit.exit_code = %d; want 0", kv.Value.AsInt64())
			}
		}
	}
}

func TestBuildAttributes_NilToolResponse(t *testing.T) {
	p := HookPayload{EventType: "test"}
	attrs := BuildAttributes("pre_tool_use", p)

	for _, kv := range attrs {
		if string(kv.Key) == "audit.exit_code" {
			if kv.Value.AsInt64() != -1 {
				t.Errorf("audit.exit_code = %d; want -1 (nil tool_response)", kv.Value.AsInt64())
			}
		}
	}
}

func TestBuildAttributes_EmptyFields(t *testing.T) {
	p := HookPayload{}
	attrs := BuildAttributes("command", p)
	m := attrMap(attrs)

	if m["audit.event_kind"] != "command" {
		t.Errorf("event_kind = %q; want %q", m["audit.event_kind"], "command")
	}
	if m["audit.event_type"] != "" {
		t.Errorf("event_type = %q; want empty", m["audit.event_type"])
	}
}

func TestEventTimestamp_WithTimestamp(t *testing.T) {
	p := HookPayload{Timestamp: "2026-04-12T21:05:00Z"}
	ts := EventTimestamp(p)
	want := time.Date(2026, 4, 12, 21, 5, 0, 0, time.UTC)
	if !ts.Equal(want) {
		t.Errorf("EventTimestamp = %v; want %v", ts, want)
	}
}

func TestEventTimestamp_Empty(t *testing.T) {
	p := HookPayload{}
	ts := EventTimestamp(p)
	if time.Since(ts) > 2*time.Second {
		t.Errorf("EventTimestamp should be ~now; got %v", ts)
	}
}

func TestEventTimestamp_Invalid(t *testing.T) {
	p := HookPayload{Timestamp: "not-a-date"}
	ts := EventTimestamp(p)
	if time.Since(ts) > 2*time.Second {
		t.Errorf("EventTimestamp should fallback to now; got %v", ts)
	}
}

func TestPayloadJSON(t *testing.T) {
	p := HookPayload{EventType: "test", UserID: "u1"}
	j := PayloadJSON(p)
	if j == "" || j[0] != '{' {
		t.Errorf("PayloadJSON should return JSON object; got %q", j)
	}
}

func TestNormalize_RepositoryExplicitPreserved(t *testing.T) {
	p := HookPayload{Repository: "explicit-repo", Cwd: "/repo/other-dir"}
	p.Normalize()
	if p.Repository != "explicit-repo" {
		t.Errorf("Repository = %q; want %q (explicit value must win over cwd)", p.Repository, "explicit-repo")
	}
}

func TestNormalize_RepositoryDerivedFromCwd(t *testing.T) {
	p := HookPayload{Cwd: "/repo/projeto-x"}
	p.Normalize()
	if p.Repository != "projeto-x" {
		t.Errorf("Repository = %q; want %q (should derive from filepath.Base(cwd))", p.Repository, "projeto-x")
	}
}

func TestNormalize_EmptyRepositoryAndEmptyCwd(t *testing.T) {
	p := HookPayload{}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty when Cwd is empty", p.Repository)
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	p := HookPayload{Cwd: "/repo/projeto-x"}
	p.Normalize()
	p.Normalize()
	if p.Repository != "projeto-x" {
		t.Errorf("Repository = %q; want %q after repeated normalize", p.Repository, "projeto-x")
	}
}

func TestNormalize_NilReceiver(t *testing.T) {
	var p *HookPayload
	// Must not panic on nil receiver.
	p.Normalize()
}

func TestNormalize_OrganizationIDNotSynthesized(t *testing.T) {
	p := HookPayload{Cwd: "/repo/projeto-x"}
	p.Normalize()
	if p.OrganizationID != "" {
		t.Errorf("OrganizationID = %q; want empty (must not be synthesized)", p.OrganizationID)
	}
}

func TestBuildAttributes_RepositoryAndOrg(t *testing.T) {
	p := HookPayload{
		Cwd:            "/repo/projeto-x",
		Repository:     "", // will be derived
		OrganizationID: "org_123",
	}
	// Callers are now responsible for invoking Normalize() before
	// BuildAttributes / PayloadJSON (CQ-M1).
	p.Normalize()
	m := attrMap(BuildAttributes("pre_tool_use", p))
	if m["audit.repository"] != "projeto-x" {
		t.Errorf("audit.repository = %q; want %q", m["audit.repository"], "projeto-x")
	}
	if m["audit.organization_id"] != "org_123" {
		t.Errorf("audit.organization_id = %q; want %q", m["audit.organization_id"], "org_123")
	}
}

func TestBuildAttributes_RepositoryExplicitWins(t *testing.T) {
	p := HookPayload{
		Cwd:        "/repo/other",
		Repository: "explicit",
	}
	p.Normalize()
	m := attrMap(BuildAttributes("pre_tool_use", p))
	if m["audit.repository"] != "explicit" {
		t.Errorf("audit.repository = %q; want %q", m["audit.repository"], "explicit")
	}
}

func TestPayloadJSON_IncludesRepositoryAndOrg(t *testing.T) {
	p := HookPayload{
		EventType:      "test",
		Cwd:            "/repo/projeto-x",
		OrganizationID: "org_123",
	}
	p.Normalize()
	j := PayloadJSON(p)
	// Body must surface repository/organization_id so ClickHouse
	// JSONExtractString(Body, 'repository'|'organization_id') works.
	if !strings.Contains(j, `"repository":"projeto-x"`) {
		t.Errorf("PayloadJSON missing derived repository; got %q", j)
	}
	if !strings.Contains(j, `"organization_id":"org_123"`) {
		t.Errorf("PayloadJSON missing organization_id; got %q", j)
	}
}

func TestPayloadJSON_OmitEmptyRepositoryAndOrg(t *testing.T) {
	// Backward compatibility: payloads without these fields must not include
	// them (omitempty) so no downstream consumer breaks on unexpected keys.
	p := HookPayload{EventType: "test"}
	j := PayloadJSON(p)
	if strings.Contains(j, `"repository"`) {
		t.Errorf("PayloadJSON should omit empty repository; got %q", j)
	}
	if strings.Contains(j, `"organization_id"`) {
		t.Errorf("PayloadJSON should omit empty organization_id; got %q", j)
	}
}

func attrMap(attrs []attribute.KeyValue) map[string]string {
	m := make(map[string]string)
	for _, kv := range attrs {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

// --- Normalize edge cases (Sec-M4) -------------------------------------------

func TestNormalize_Cwd_Root(t *testing.T) {
	p := HookPayload{Cwd: "/"}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty for Cwd=\"/\"", p.Repository)
	}
}

func TestNormalize_Cwd_Dot(t *testing.T) {
	p := HookPayload{Cwd: "."}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty for Cwd=\".\"", p.Repository)
	}
}

func TestNormalize_Cwd_TrailingSlash(t *testing.T) {
	p := HookPayload{Cwd: "/repo/projeto-x/"}
	p.Normalize()
	if p.Repository != "projeto-x" {
		t.Errorf("Repository = %q; want %q for trailing-slash Cwd", p.Repository, "projeto-x")
	}
}

func TestNormalize_Cwd_Windows(t *testing.T) {
	p := HookPayload{Cwd: `C:\Users\joao\repo`}
	p.Normalize()
	if p.Repository != "repo" {
		t.Errorf("Repository = %q; want %q for Windows Cwd", p.Repository, "repo")
	}
}

func TestNormalize_Cwd_Whitespace(t *testing.T) {
	p := HookPayload{Cwd: "   "}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty for whitespace Cwd", p.Repository)
	}
}

func TestNormalize_Cwd_InvalidCharset(t *testing.T) {
	// Newline embedded in basename — must be rejected by charset guard.
	p := HookPayload{Cwd: "/repo/bad\nname"}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty for invalid charset", p.Repository)
	}
}

func TestNormalize_Cwd_ControlChars(t *testing.T) {
	p := HookPayload{Cwd: "/repo/ok\x00name"}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty for control-char basename", p.Repository)
	}
}

func TestNormalize_Cwd_InvalidCharsetOnExplicit(t *testing.T) {
	// Explicit Repository must also be validated. A newline in an explicit
	// value is treated the same way (cleared to "").
	p := HookPayload{Repository: "oops\nno", Cwd: "/repo/projeto-x"}
	p.Normalize()
	if p.Repository != "" {
		t.Errorf("Repository = %q; want empty for invalid-charset explicit value", p.Repository)
	}
}

func TestNormalize_ConcurrentSafe(t *testing.T) {
	// 20 goroutines all call Normalize on the SAME pointer. Because
	// Normalize is idempotent and each iteration writes the same bytes, the
	// result must be deterministic. This is a regression guard — we are not
	// claiming true concurrent-mutation safety for the Go memory model, but
	// we do want `go test -race` to stay clean when multiple handlers
	// happen to reuse a pointer (they don't today, but future refactors
	// might).
	p := &HookPayload{Cwd: "/repo/projeto-x"}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			p.Normalize()
		})
	}
	wg.Wait()
	if p.Repository != "projeto-x" {
		t.Errorf("Repository = %q; want %q after concurrent Normalize", p.Repository, "projeto-x")
	}
}

// --- Redaction integration (Sec-M3) ------------------------------------------

func TestNormalize_RedactsCommand(t *testing.T) {
	p := HookPayload{Command: "export AWS=AKIAABCDEFGHIJKLMNOP && run"}
	p.Normalize()
	if strings.Contains(p.Command, "AKIAABCDEFGHIJKLMNOP") {
		t.Errorf("command not redacted: %q", p.Command)
	}
}

func TestNormalize_RedactsToolResponseStreams(t *testing.T) {
	p := HookPayload{
		ToolResponse: &ToolResponse{
			Stdout: "ghp_1234567890abcdefghijABCDEFGHIJklmno",
			Stderr: "Authorization: Bearer abcdefghijklmnopqrstuvwxyz123",
		},
	}
	p.Normalize()
	if strings.Contains(p.ToolResponse.Stdout, "ghp_") {
		t.Errorf("stdout not redacted: %q", p.ToolResponse.Stdout)
	}
	if strings.Contains(p.ToolResponse.Stderr, "abcdefghijklmnopqrstuvwxyz123") {
		t.Errorf("stderr not redacted: %q", p.ToolResponse.Stderr)
	}
}

package audit

import (
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

func attrMap(attrs []attribute.KeyValue) map[string]string {
	m := make(map[string]string)
	for _, kv := range attrs {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

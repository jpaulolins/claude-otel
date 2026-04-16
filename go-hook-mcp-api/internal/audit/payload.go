package audit

import (
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type ToolResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type HookPayload struct {
	EventType      string        `json:"event_type"`
	UserID         string        `json:"user_id"`
	SessionID      string        `json:"session_id"`
	ToolUseID      string        `json:"tool_use_id"`
	ToolName       string        `json:"tool_name"`
	Command        string        `json:"command"`
	Cwd            string        `json:"cwd"`
	PermissionMode string        `json:"permission_mode"`
	Success        bool          `json:"success"`
	ToolResponse   *ToolResponse `json:"tool_response,omitempty"`
	TranscriptPath string        `json:"transcript_path"`
	Timestamp      string        `json:"timestamp"`
}

func BuildAttributes(eventKind string, p HookPayload) []attribute.KeyValue {
	exitCode := -1
	if p.ToolResponse != nil {
		exitCode = p.ToolResponse.ExitCode
	}

	return []attribute.KeyValue{
		attribute.String("audit.event_kind", eventKind),
		attribute.String("audit.event_type", p.EventType),
		attribute.String("audit.user_id", p.UserID),
		attribute.String("audit.session_id", p.SessionID),
		attribute.String("audit.tool_use_id", p.ToolUseID),
		attribute.String("audit.tool_name", p.ToolName),
		attribute.String("audit.command", p.Command),
		attribute.String("audit.cwd", p.Cwd),
		attribute.String("audit.permission_mode", p.PermissionMode),
		attribute.Bool("audit.success", p.Success),
		attribute.Int("audit.exit_code", exitCode),
	}
}

func EventTimestamp(p HookPayload) time.Time {
	if p.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, p.Timestamp); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func PayloadJSON(p HookPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

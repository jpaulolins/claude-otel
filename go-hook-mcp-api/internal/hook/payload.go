package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type ToolResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// UnmarshalJSON accepts two payload shapes Claude Code emits for tool_response:
//
//   - Bash-style object: {"exit_code":0,"stdout":"...","stderr":"..."}
//   - MCP-style array of content blocks: [{"type":"text","text":"..."}]
//
// For the MCP shape, text blocks are concatenated into Stdout. Non-text blocks
// (images, resource links) are ignored. ExitCode defaults to 0.
func (tr *ToolResponse) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch trimmed[0] {
	case '{':
		type alias ToolResponse
		var obj alias
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		*tr = ToolResponse(obj)
		return nil
	case '[':
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &blocks); err != nil {
			return err
		}
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		tr.Stdout = sb.String()
		return nil
	default:
		return fmt.Errorf("tool_response: unsupported JSON shape: %s", trimmed[:1])
	}
}

// HookPayload is the JSON body received from the Claude Code hook. It carries
// both the raw tool invocation fields and a handful of derived fields
// populated by Normalize().
//
// Callers must invoke Normalize() once after decoding a HookPayload from JSON
// before accessing derived fields (Repository) or emitting telemetry. The
// derivation is idempotent but downstream helpers (BuildAttributes,
// PayloadJSON) no longer re-invoke it — they trust the caller.
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
	// Repository is the short name of the repo the event originated from.
	// Optional. When empty and Cwd is set, Normalize() derives it from
	// path.Base(filepath.ToSlash(Cwd)), after trimming whitespace and
	// trailing slashes, and after charset validation (see below). Surfaced
	// in the serialized Body so ClickHouse queries can use
	// JSONExtractString(Body, 'repository').
	Repository string `json:"repository,omitempty"`
	// OrganizationID is the tenant/org identifier (maps to the
	// organization.id attribute emitted on metrics). Optional; propagated
	// verbatim so queries can use JSONExtractString(Body, 'organization_id').
	OrganizationID string `json:"organization_id,omitempty"`
}

// UnmarshalJSON accepts the Claude Code hook payload schema (canonical) and
// transparently remaps the Gemini CLI hook schema onto it:
//
//   - Claude:  {"event_type":"PreToolUse"|"PostToolUse", "command":"...", ...}
//   - Gemini:  {"hook_event_name":"BeforeTool"|"AfterTool", "tool_input":{"command":"..."}, ...}
//
// Mapping rules (Gemini → canonical), applied only when the Claude field is
// empty so explicit Claude payloads always win:
//
//   - hook_event_name "BeforeTool"  → event_type "PreToolUse"
//   - hook_event_name "AfterTool"   → event_type "PostToolUse"
//   - any other hook_event_name     → event_type passes through verbatim
//   - tool_input.command            → command
//
// Fields the two schemas already share (session_id, tool_name, cwd,
// transcript_path, tool_response) decode normally without any remapping.
func (p *HookPayload) UnmarshalJSON(data []byte) error {
	type alias HookPayload
	var canonical alias
	if err := json.Unmarshal(data, &canonical); err != nil {
		return err
	}
	*p = HookPayload(canonical)

	var gemini struct {
		HookEventName string          `json:"hook_event_name"`
		ToolInput     json.RawMessage `json:"tool_input"`
	}
	_ = json.Unmarshal(data, &gemini)

	if p.EventType == "" && gemini.HookEventName != "" {
		switch gemini.HookEventName {
		case "BeforeTool":
			p.EventType = "PreToolUse"
		case "AfterTool":
			p.EventType = "PostToolUse"
		default:
			p.EventType = gemini.HookEventName
		}
	}
	if p.Command == "" && len(gemini.ToolInput) > 0 {
		var ti struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(gemini.ToolInput, &ti); err == nil {
			p.Command = ti.Command
		}
	}
	return nil
}

// repoNameRe constrains what we allow as a Repository value. If the derived
// basename does not match, Normalize() clears Repository rather than
// propagating garbage (including newlines or control characters).
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,128}$`)

// normalizeMu serializes concurrent Normalize() calls. In practice handlers
// decode a fresh payload per request so contention is near-zero, but we
// guarantee safety against `go test -race` and future code paths that might
// share a pointer between goroutines.
var normalizeMu sync.Mutex

// Normalize applies backward-compatible derivations on the payload so that
// downstream consumers (Body JSON, OTEL attributes) see a consistent view.
// It is idempotent: calling it multiple times yields the same result.
//
// Currently it:
//   - derives Repository from path.Base(filepath.ToSlash(Cwd)) when
//     Repository is empty and Cwd is non-empty, handling trailing slashes,
//     Windows paths, "/", "." and whitespace-only Cwd correctly. Values that
//     do not match [A-Za-z0-9._-]{1,128} are dropped (treated as unknown).
//   - redacts secrets in Command / ToolResponse.Stdout / ToolResponse.Stderr
//     via redactSecrets — see redact.go for the supported patterns.
//
// OrganizationID is never synthesized — if the caller did not provide it, it
// stays empty.
func (p *HookPayload) Normalize() {
	if p == nil {
		return
	}
	normalizeMu.Lock()
	defer normalizeMu.Unlock()

	if p.Repository == "" {
		p.Repository = deriveRepository(p.Cwd)
	} else if !repoNameRe.MatchString(p.Repository) {
		// Reject explicit-but-invalid Repository values too — safer than
		// letting a bogus name flow into ClickHouse JSONExtract or OTEL
		// attribute values.
		p.Repository = ""
	}

	p.Command = redactSecrets(p.Command)
	if p.ToolResponse != nil {
		p.ToolResponse.Stdout = redactSecrets(p.ToolResponse.Stdout)
		p.ToolResponse.Stderr = redactSecrets(p.ToolResponse.Stderr)
	}
}

// deriveRepository computes a Repository basename from Cwd. See Normalize()
// for the full contract. Returns "" when the derivation yields nothing
// useful or the result fails charset validation.
func deriveRepository(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	// Normalize Windows backslashes so `path.Base` behaves identically on
	// both POSIX and Windows inputs. filepath.ToSlash is a no-op on POSIX
	// platforms (it uses the host separator), so we additionally replace
	// raw backslashes to cover Windows-style inputs received on Linux/macOS
	// hosts.
	slashed := strings.ReplaceAll(cwd, `\`, `/`)
	// Strip trailing slashes so "/repo/foo/" → "foo" and not "".
	slashed = strings.TrimRight(slashed, "/")
	if slashed == "" {
		// Was "/" (or all slashes) — no useful basename.
		return ""
	}
	base := path.Base(slashed)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	// Additionally drop a Windows drive letter like "C:" if path.Base
	// happened to return one (e.g. Cwd="C:" alone).
	if len(base) == 2 && base[1] == ':' {
		return ""
	}
	if !repoNameRe.MatchString(base) {
		return ""
	}
	return base
}

// BuildAttributes returns the OTEL attributes for a payload. The caller MUST
// have invoked payload.Normalize() beforehand; this function no longer
// re-normalizes (CQ-M1).
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
		attribute.String("audit.repository", p.Repository),
		attribute.String("audit.organization_id", p.OrganizationID),
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

// PayloadJSON serializes a (presumably already Normalize()-d) payload. The
// caller MUST have invoked Normalize() beforehand; this function no longer
// re-normalizes (CQ-M1).
func PayloadJSON(p HookPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

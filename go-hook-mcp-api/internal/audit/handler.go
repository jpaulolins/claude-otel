package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// defaultMaxHookBodyBytes caps the hook payload size when AUDIT_MAX_BODY_BYTES
// is unset or invalid. Hook payloads are expected to be a few KB (Claude Code
// command + tool_response). 1 MiB is a generous upper bound — anything larger
// is either a misconfigured tool or abuse.
const defaultMaxHookBodyBytes int64 = 1 << 20

// maxHookBodyBytes is initialised from AUDIT_MAX_BODY_BYTES at package-load
// time. Kept as a package-level var (not a field on Handler) so existing
// callers that construct `&Handler{}` directly continue to work. Parse
// failures fall back to the default and log a stderr warning.
var maxHookBodyBytes = resolveMaxHookBodyBytes()

// resolveMaxHookBodyBytes reads AUDIT_MAX_BODY_BYTES. Values must be a
// positive integer byte count. Invalid/zero/negative values log a warning
// and use the default.
func resolveMaxHookBodyBytes() int64 {
	raw := os.Getenv("AUDIT_MAX_BODY_BYTES")
	if raw == "" {
		return defaultMaxHookBodyBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		slog.Warn("invalid AUDIT_MAX_BODY_BYTES; using default",
			slog.String("raw", raw),
			slog.Int64("default_bytes", defaultMaxHookBodyBytes),
		)
		return defaultMaxHookBodyBytes
	}
	return n
}

type Handler struct {
	Tracer trace.Tracer
	Logger *slog.Logger
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) PreToolUse(w http.ResponseWriter, r *http.Request) {
	h.handleHook("pre_tool_use", w, r)
}

func (h *Handler) PostToolUse(w http.ResponseWriter, r *http.Request) {
	h.handleHook("post_tool_use", w, r)
}

func (h *Handler) PostToolUseFailure(w http.ResponseWriter, r *http.Request) {
	h.handleHook("post_tool_use_failure", w, r)
}

func (h *Handler) Command(w http.ResponseWriter, r *http.Request) {
	h.handleHook("command", w, r)
}

func (h *Handler) handleHook(eventKind string, w http.ResponseWriter, r *http.Request) {
	// Wrap the request body with MaxBytesReader so that the JSON decoder
	// stops (and the caller gets a 413) once the configured cap is hit,
	// instead of happily buffering arbitrary payloads in memory.
	limit := maxHookBodyBytes
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	var payload HookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			// Log the enforcement event without leaking the body — the whole
			// point of the cap is to avoid buffering this payload.
			h.Logger.Warn("body_too_large",
				slog.String("remote", r.RemoteAddr),
				slog.Int64("size_limit", limit),
			)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"detail": fmt.Sprintf("request body exceeds %d bytes", limit),
			})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid JSON"})
		return
	}
	// Derive fields like Repository from Cwd and redact secrets once, up
	// front, so that the serialized Body and the OTEL attributes share a
	// consistent view. Per payload.go's contract, downstream helpers do NOT
	// re-invoke Normalize (CQ-M1).
	payload.Normalize()

	eventTS := EventTimestamp(payload)
	attrs := BuildAttributes(eventKind, payload)
	payloadJSON := PayloadJSON(payload)

	ctx, span := h.Tracer.Start(r.Context(), "claude.hook.ingest",
		trace.WithAttributes(
			attribute.String("audit.event_kind", eventKind),
			attribute.String("audit.event_type", payload.EventType),
			attribute.String("audit.session_id", payload.SessionID),
		),
	)
	defer span.End()

	logAttrs := make([]slog.Attr, 0, len(attrs))
	for _, kv := range attrs {
		switch kv.Value.Type() {
		case attribute.BOOL:
			logAttrs = append(logAttrs, slog.Bool(string(kv.Key), kv.Value.AsBool()))
		case attribute.INT64:
			logAttrs = append(logAttrs, slog.Int64(string(kv.Key), kv.Value.AsInt64()))
		default:
			logAttrs = append(logAttrs, slog.String(string(kv.Key), kv.Value.Emit()))
		}
	}
	args := make([]any, len(logAttrs))
	for i, a := range logAttrs {
		args[i] = a
	}
	h.Logger.InfoContext(ctx, payloadJSON, args...)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":     "ok",
		"event_kind": eventKind,
		"timestamp":  eventTS.Format("2006-01-02T15:04:05Z"),
	})
}

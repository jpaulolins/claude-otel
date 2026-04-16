package audit

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Handler struct {
	Tracer trace.Tracer
	Logger *slog.Logger
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
	var payload HookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"detail": "invalid JSON"})
		return
	}

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
	json.NewEncoder(w).Encode(map[string]string{
		"status":     "ok",
		"event_kind": eventKind,
		"timestamp":  eventTS.Format("2006-01-02T15:04:05Z"),
	})
}

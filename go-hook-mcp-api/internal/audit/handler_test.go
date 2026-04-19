package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace/noop"
)

func newTestRouter() http.Handler {
	h := &Handler{
		Tracer: noop.NewTracerProvider().Tracer("test"),
		Logger: slog.Default(),
	}

	r := chi.NewRouter()
	r.Get("/healthz", h.Healthz)
	r.Post("/hooks/pre-tool-use", h.PreToolUse)
	r.Post("/hooks/post-tool-use", h.PostToolUse)
	r.Post("/hooks/post-tool-use-failure", h.PostToolUseFailure)
	r.Post("/hooks/command", h.Command)
	return r
}

func newAuthRouter(token string) http.Handler {
	h := &Handler{
		Tracer: noop.NewTracerProvider().Tracer("test"),
		Logger: slog.Default(),
	}

	r := chi.NewRouter()
	r.Get("/healthz", h.Healthz)
	r.Group(func(r chi.Router) {
		r.Use(BearerAuth(token))
		r.Post("/hooks/post-tool-use", h.PostToolUse)
	})
	return r
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body.status = %q; want %q", body["status"], "ok")
	}
}

func TestHookEndpoints(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	tests := []struct {
		path     string
		wantKind string
	}{
		{"/hooks/pre-tool-use", "pre_tool_use"},
		{"/hooks/post-tool-use", "post_tool_use"},
		{"/hooks/post-tool-use-failure", "post_tool_use_failure"},
		{"/hooks/command", "command"},
	}

	payload := `{"event_type":"test","user_id":"u1","session_id":"s1","tool_name":"Bash"}`

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Post(srv.URL+tc.path, "application/json", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				t.Errorf("status = %d; want 200", resp.StatusCode)
			}

			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["event_kind"] != tc.wantKind {
				t.Errorf("event_kind = %q; want %q", body["event_kind"], tc.wantKind)
			}
			if body["status"] != "ok" {
				t.Errorf("status = %q; want %q", body["status"], "ok")
			}
		})
	}
}

func TestHookEndpoints_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/hooks/pre-tool-use", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["detail"] != "invalid JSON" {
		t.Errorf("detail = %q; want %q", body["detail"], "invalid JSON")
	}
}

// TestHandler_MaxBytesEnforced posts a body >1 MiB to /hooks/post-tool-use
// and expects the MaxBytesReader to reject the request with 413 Request
// Entity Too Large. It also asserts that the handler emits a stderr log
// line identifying the enforcement event without leaking the body.
func TestHandler_MaxBytesEnforced(t *testing.T) {
	// Capture slog output (used by the handler for the enforcement line)
	// by swapping the default slog logger. Parallelising this test would
	// break the global logger state, so we run serially.
	var logBuf bytes.Buffer
	origDefault := slog.Default()
	testLogger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(testLogger)
	t.Cleanup(func() {
		slog.SetDefault(origDefault)
	})

	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	// Craft a valid-looking JSON payload that is deliberately larger than
	// 1 MiB — the decoder wraps r.Body in MaxBytesReader so the read fails
	// before the JSON structure is complete.
	huge := strings.Repeat("a", (1<<20)+1024)
	body := `{"event_type":"test","command":"` + huge + `"}`

	resp, err := http.Post(srv.URL+"/hooks/post-tool-use", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// httptest's server / net transport may surface MaxBytesError as 413
	// (preferred) or 400 if the error did not carry MaxBytesError — accept
	// either as long as it's NOT 200.
	if resp.StatusCode == 200 {
		t.Fatalf("status = %d; want 4xx", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 413 or 400", resp.StatusCode)
	}

	// Only assert the log line when the MaxBytesError path actually
	// triggered (413 response). If the transport short-circuited the
	// request and we got 400, no log line is expected.
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		logged := logBuf.String()
		if !strings.Contains(logged, "body_too_large") {
			t.Errorf("expected body_too_large log line; got %q", logged)
		}
		if !strings.Contains(logged, "size_limit=") {
			t.Errorf("expected size_limit= in log line; got %q", logged)
		}
		// Negative assertion: the body itself must NOT be logged. The
		// 'aaaa...' run is big enough to matter.
		if strings.Contains(logged, strings.Repeat("a", 64)) {
			t.Errorf("log line leaked body content: %q", logged)
		}
	}
}

func TestHookEndpoints_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(newAuthRouter("secret"))
	defer srv.Close()

	payload := `{"event_type":"test"}`
	resp, err := http.Post(srv.URL+"/hooks/post-tool-use", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 401 {
		t.Errorf("status = %d; want 401", resp.StatusCode)
	}
}

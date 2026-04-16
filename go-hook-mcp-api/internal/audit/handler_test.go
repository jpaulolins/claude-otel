package audit

import (
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
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("body.status = %q; want %q", body["status"], "ok")
	}
}

func TestHookEndpoints(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	tests := []struct {
		path      string
		wantKind  string
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
			json.NewDecoder(resp.Body).Decode(&body)
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

	resp, err := http.Post(srv.URL+"/hooks/post-tool-use", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("status = %d; want 400", resp.StatusCode)
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

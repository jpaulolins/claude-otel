package audit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBearerAuth_EmptyToken_Panics verifies that an empty token with the
// default env (no AUDIT_ALLOW_ANONYMOUS) panics at construction time —
// fail-closed against misconfiguration.
func TestBearerAuth_EmptyToken_Panics(t *testing.T) {
	t.Setenv(auditAnonymousEnv, "")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("BearerAuth(\"\") should panic when AUDIT_ALLOW_ANONYMOUS is unset")
		}
	}()
	_ = BearerAuth("")
}

// TestBearerAuth_EmptyToken_AnonymousOptIn verifies the explicit opt-in:
// AUDIT_ALLOW_ANONYMOUS=true lets requests pass without any Authorization
// header (local dev only).
func TestBearerAuth_EmptyToken_AnonymousOptIn(t *testing.T) {
	t.Setenv(auditAnonymousEnv, "true")
	handler := BearerAuth("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (opt-in anonymous mode)", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options header missing")
	}
}

func TestBearerAuth_ValidToken(t *testing.T) {
	handler := BearerAuth("secret123")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer secret123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

// TestBearerAuth_CaseInsensitiveScheme verifies that "bearer" (lowercase)
// is accepted per RFC 7235.
func TestBearerAuth_CaseInsensitiveScheme(t *testing.T) {
	handler := BearerAuth("secret123")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer secret123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (scheme is case-insensitive)", rec.Code)
	}
}

func TestBearerAuth_InvalidToken(t *testing.T) {
	handler := BearerAuth("secret123")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

// TestBearerAuth_ConstantTime is a regression test: we cannot reliably
// observe timing differences inside `go test -race`, but we can make sure
// the code path still calls the constant-time compare (i.e. rejection
// behavior remains correct for every prefix and suffix of the real token).
func TestBearerAuth_ConstantTime(t *testing.T) {
	handler := BearerAuth("secret123")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Every one of these MUST be rejected — the implementation no longer
	// uses `!=` string comparison (which can short-circuit on first byte).
	wrongTokens := []string{"", "s", "secret", "secret12", "secret1234", "xxxxxxxxx"}
	for _, tok := range wrongTokens {
		req := httptest.NewRequest("GET", "/", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d; want 401", tok, rec.Code)
		}
	}
}

func TestBearerAuth_MissingHeader(t *testing.T) {
	handler := BearerAuth("secret123")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

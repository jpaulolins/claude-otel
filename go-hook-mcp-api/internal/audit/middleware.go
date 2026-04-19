package audit

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// auditAnonymousEnv enables running BearerAuth with an empty token (no auth).
// Intended strictly for local development and CI. In every other case, an
// empty token must cause BearerAuth to panic at construction time so that
// misconfigured production deployments fail closed instead of silently
// serving traffic without authentication.
const auditAnonymousEnv = "AUDIT_ALLOW_ANONYMOUS"

// BearerAuth returns a middleware that requires an Authorization: Bearer
// header matching token. The comparison is constant-time
// (crypto/subtle.ConstantTimeCompare) and the scheme is case-insensitive.
//
// If token is empty the middleware is FAIL-CLOSED by default: BearerAuth
// panics unless AUDIT_ALLOW_ANONYMOUS=true was set to explicitly opt into
// unauthenticated operation (local dev only). When opting in, a loud
// SECURITY WARNING is printed to os.Stderr.
func BearerAuth(token string) func(http.Handler) http.Handler {
	if token == "" {
		if os.Getenv(auditAnonymousEnv) != "true" {
			panic("AUDIT_API_TOKEN required; set AUDIT_ALLOW_ANONYMOUS=true to opt out explicitly (local dev only)")
		}
		fmt.Fprintln(os.Stderr, "SECURITY WARNING: audit-service running WITHOUT authentication (AUDIT_API_TOKEN is empty, AUDIT_ALLOW_ANONYMOUS=true). Do NOT use in production.")
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Content-Type-Options", "nosniff")
				next.ServeHTTP(w, r)
			})
		}
	}

	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			got := extractBearer(r.Header.Get("Authorization"))
			if got == "" {
				logAuthFailure(r, "missing_header")
				writeAuthError(w)
				return
			}
			if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
				logAuthFailure(r, "invalid_token")
				writeAuthError(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// extractBearer returns the token from an Authorization header value.
// The scheme is case-insensitive; surrounding whitespace is trimmed.
func extractBearer(header string) string {
	scheme, tok, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

func logAuthFailure(r *http.Request, reason string) {
	fmt.Fprintf(os.Stderr, "audit auth_failure remote=%s path=%s reason=%s\n", r.RemoteAddr, r.URL.Path, reason)
}

func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Unauthorized"})
}

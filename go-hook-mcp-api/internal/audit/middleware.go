package audit

import (
	"encoding/json"
	"net/http"
)

func BearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			expected := "Bearer " + token
			if r.Header.Get("Authorization") != expected {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"detail": "Unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

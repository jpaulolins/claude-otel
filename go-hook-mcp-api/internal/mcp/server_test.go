package mcp

import (
	"testing"
)

func TestNewServer(t *testing.T) {
	mq := &mockQuerier{}
	s := NewServer(mq)
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer mytoken", "mytoken"},
		{"Bearer ", ""},
		{"", ""},
		{"Basic abc123", ""},
		{"Bearernotoken", ""},
	}
	for _, tc := range tests {
		t.Run(tc.header, func(t *testing.T) {
			got := extractBearerFromHeader(tc.header)
			if got != tc.want {
				t.Errorf("extractBearer(%q) = %q; want %q", tc.header, got, tc.want)
			}
		})
	}
}

func extractBearerFromHeader(auth string) string {
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

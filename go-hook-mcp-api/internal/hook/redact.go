package hook

import "regexp"

// redactSecrets returns s with well-known secret patterns replaced by a
// redaction marker. Most patterns collapse to the literal string
// "[REDACTED]"; a few (JWT, PEM, DB URLs) use more specific markers so that
// the audit reader can tell at a glance what class of secret was masked.
//
// The patterns below are intentionally conservative: false positives
// (masking a non-secret) are preferred over false negatives (leaking a
// real secret into telemetry).
//
// Current patterns:
//   - AWS access keys:     AKIA[0-9A-Z]{16}, ASIA[0-9A-Z]{16}
//   - GitHub tokens:       ghp_/gho_/ghu_/ghs_/ghr_... (ghX_ + 30+ chars)
//   - Slack tokens:        xoxb-/xoxp-/xoxa-/xoxr-/xoxs- + 10+ chars
//   - Bearer headers:      (?i)bearer <token-20+>
//   - Generic key=value:   api_key / secret / password / token followed by
//     = or : and an 8+ char value (case-insensitive)
//   - High-entropy:        key|secret|token followed by a 32+ char
//     base64-ish run
//   - JWT:                 three base64url segments separated by "."; the
//     first must start with "eyJ" (the b64 of `{"`). Marker:
//     "[REDACTED-JWT]".
//   - DB URL with creds:   postgres/postgresql/mysql/mongodb/redis URLs of
//     the form scheme://user:pass@... — the scheme + credentials prefix up
//     to (and including) the "@" is replaced with "[REDACTED-DB-URL]",
//     preserving the host/path so ops context is not lost.
//   - PEM private key:     multiline `-----BEGIN ... PRIVATE KEY-----` /
//     `-----END ... PRIVATE KEY-----` block. Marker: "[REDACTED-PEM-KEY]".
//     Evaluated first so the embedded base64 body cannot trip the generic
//     high-entropy pattern.
//
// Callers: Normalize() applies this to HookPayload.Command and
// HookPayload.ToolResponse.{Stdout,Stderr} before the payload is serialized
// into Body or emitted as OTEL attributes. Keep the pattern set small and
// document every addition here.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	for _, r := range secretReplacers {
		s = r.re.ReplaceAllString(s, r.replacement)
	}
	return s
}

type secretReplacer struct {
	re          *regexp.Regexp
	replacement string
}

// secretReplacers are evaluated in order. Longer / more specific patterns
// come first so they mask before the generic patterns see them. PEM comes
// before everything because its body is exactly the kind of high-entropy
// blob the generic patterns were written to catch.
var secretReplacers = []secretReplacer{
	// PEM private-key block (multiline). Dotall flag so `.` matches `\n`.
	{
		re:          regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+PRIVATE KEY-----.*?-----END [A-Z ]+PRIVATE KEY-----`),
		replacement: "[REDACTED-PEM-KEY]",
	},

	// DB URL with inline credentials. Matches `scheme://user:pass@` (plus an
	// optional `+driver` like `postgresql+psycopg`) and replaces only that
	// prefix so the host/path remains visible for ops context.
	{
		re:          regexp.MustCompile(`(?i)(postgres(?:ql)?|mysql|mongodb|redis)(\+\w+)?://[^:/@\s]+:[^@\s]+@`),
		replacement: "[REDACTED-DB-URL]",
	},

	// JWT: three base64url segments (header.payload.signature); the header
	// conventionally starts with "eyJ" (b64 of `{"`). The minimum length is
	// loose but above noise — short doted tokens will not false-positive.
	{
		re:          regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
		replacement: "[REDACTED-JWT]",
	},

	// AWS access keys (long-lived and session)
	{re: regexp.MustCompile(`AKIA[0-9A-Z]{16}`), replacement: "[REDACTED]"},
	{re: regexp.MustCompile(`ASIA[0-9A-Z]{16}`), replacement: "[REDACTED]"},

	// GitHub personal access / OAuth / app / refresh tokens
	{re: regexp.MustCompile(`gh[a-z]_[A-Za-z0-9_]{30,}`), replacement: "[REDACTED]"},

	// Slack bot / user / admin / refresh / config tokens
	{re: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), replacement: "[REDACTED]"},

	// "Authorization: Bearer <token>" style headers embedded in output.
	{re: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/=]{20,}`), replacement: "[REDACTED]"},

	// Generic "api_key = 'xyz'" / "secret: xyz" / "password=xyz" / "token=xyz".
	// Mask the whole assignment (key+value) so an observer cannot learn which
	// secret leaked. The value character class is broad (alphanumerics plus
	// common password symbols) to catch real-world secrets — remember: the
	// redactor is intentionally conservative.
	{
		re:          regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token)\s*[=:]\s*["']?[A-Za-z0-9\-._~+/=@!#\$%\^&\*\(\)]{8,}["']?`),
		replacement: "[REDACTED]",
	},

	// High-entropy base64-ish blobs preceded by key|secret|token. Conservative
	// 32+ char threshold.
	{
		re:          regexp.MustCompile(`(?i)(key|secret|token)[^A-Za-z0-9]+[A-Za-z0-9+/=]{32,}`),
		replacement: "[REDACTED]",
	},
}

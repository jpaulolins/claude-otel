package hook

import (
	"strings"
	"testing"
)

func TestRedactSecrets_AWSKey(t *testing.T) {
	in := "aws_access_key_id = AKIAABCDEFGHIJKLMNOP\n"
	out := redactSecrets(in)
	if strings.Contains(out, "AKIAABCDEFGHIJKLMNOP") {
		t.Errorf("AWS key not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("missing redaction marker: %q", out)
	}
}

func TestRedactSecrets_AWSSessionKey(t *testing.T) {
	in := "ASIAABCDEFGHIJKLMNOP"
	out := redactSecrets(in)
	if strings.Contains(out, "ASIAABCDEFGHIJKLMNOP") {
		t.Errorf("session key not redacted: %q", out)
	}
}

func TestRedactSecrets_GitHubToken(t *testing.T) {
	// gh_ + 30+ char body
	in := "Token: ghp_1234567890abcdefghijABCDEFGHIJklmno"
	out := redactSecrets(in)
	if strings.Contains(out, "ghp_1234567890abcdefghijABCDEFGHIJklmno") {
		t.Errorf("github token not redacted: %q", out)
	}
}

func TestRedactSecrets_SlackToken(t *testing.T) {
	in := "SLACK=xoxb-12345-ABCDEFGHIJKLMN"
	out := redactSecrets(in)
	if strings.Contains(out, "xoxb-12345-ABCDEFGHIJKLMN") {
		t.Errorf("slack token not redacted: %q", out)
	}
}

func TestRedactSecrets_BearerHeader(t *testing.T) {
	in := "curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz123' https://api"
	out := redactSecrets(in)
	if strings.Contains(out, "abcdefghijklmnopqrstuvwxyz123") {
		t.Errorf("bearer token not redacted: %q", out)
	}
}

func TestRedactSecrets_GenericAPIKey(t *testing.T) {
	cases := []string{
		"api_key=abcdef1234567890",
		`API_KEY: "abcdef1234567890"`,
		"password=s3cretP@ssword",
		"token = 'abcdefghij12345'",
		"SECRET=topsecret1234",
	}
	for _, in := range cases {
		out := redactSecrets(in)
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("generic key not redacted: in=%q out=%q", in, out)
		}
	}
}

func TestRedactSecrets_MultilineCommand(t *testing.T) {
	in := "export AWS_KEY=AKIAABCDEFGHIJKLMNOP\nexport TOKEN=ghp_1234567890abcdefghijABCDEFGHIJklmno\necho ok"
	out := redactSecrets(in)
	if strings.Contains(out, "AKIAABCDEFGHIJKLMNOP") || strings.Contains(out, "ghp_1234567890abcdefghijABCDEFGHIJklmno") {
		t.Errorf("multiline secrets leaked: %q", out)
	}
	if !strings.Contains(out, "echo ok") {
		t.Errorf("non-secret line dropped: %q", out)
	}
}

func TestRedactSecrets_PassthroughNoSecret(t *testing.T) {
	cases := []string{
		"",
		"echo hello world",
		"ls -la /tmp",
		"grep -r TODO .",
	}
	for _, in := range cases {
		out := redactSecrets(in)
		if out != in {
			t.Errorf("redactSecrets mutated clean text: in=%q out=%q", in, out)
		}
	}
}

func TestRedactSecrets_HighEntropyAfterKeyword(t *testing.T) {
	// key followed by 32+ base64-ish characters — must be flagged.
	in := "key:AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AB=="
	out := redactSecrets(in)
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("high-entropy blob not redacted: %q", out)
	}
}

func TestRedactSecrets_JWT(t *testing.T) {
	// Realistic-looking (but non-real) JWT: three base64url segments
	// separated by dots, header starts with "eyJ".
	in := "Authorization=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	out := redactSecrets(in)
	if strings.Contains(out, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Errorf("JWT header leaked: %q", out)
	}
	if strings.Contains(out, "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c") {
		t.Errorf("JWT signature leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED-JWT]") {
		t.Errorf("expected [REDACTED-JWT] marker: %q", out)
	}
}

func TestRedactSecrets_PostgresURL(t *testing.T) {
	cases := []string{
		"postgres://user:pass@db.local:5432/mydb",
		"postgresql://alice:s3cret@example.com/prod",
		"mysql://root:hunter2@10.0.0.5:3306/app",
		"mongodb://admin:letmein@mongo.internal/auth",
		"redis://default:passw0rd@cache:6379",
		"postgresql+psycopg://u:p@host/db",
	}
	for _, in := range cases {
		out := redactSecrets(in)
		if strings.Contains(out, "pass@") || strings.Contains(out, "s3cret") ||
			strings.Contains(out, "hunter2") || strings.Contains(out, "letmein") ||
			strings.Contains(out, "passw0rd") || strings.Contains(out, "u:p@") {
			t.Errorf("DB credentials leaked: in=%q out=%q", in, out)
		}
		if !strings.Contains(out, "[REDACTED-DB-URL]") {
			t.Errorf("expected [REDACTED-DB-URL] marker: in=%q out=%q", in, out)
		}
	}
}

func TestRedactSecrets_PEMKey(t *testing.T) {
	in := `some log line
-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAvT7eZrFAKeTJ1v5WmF3sXN2K0iM9wQJW3X+8ExampleLine1
Z0wT0uDq8oHgT8X9X1Ue8NoVXyZ6kqexampleLine2exampleLine2exampleLin
cExampleLine3ExampleLine3ExampleLine3ExampleLine3ExampleLine3E=
-----END RSA PRIVATE KEY-----
tail line`
	out := redactSecrets(in)
	if strings.Contains(out, "BEGIN RSA PRIVATE KEY") || strings.Contains(out, "MIIEpAIBAAKCAQEA") {
		t.Errorf("PEM block leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED-PEM-KEY]") {
		t.Errorf("expected [REDACTED-PEM-KEY] marker: %q", out)
	}
	if !strings.Contains(out, "some log line") || !strings.Contains(out, "tail line") {
		t.Errorf("surrounding lines dropped: %q", out)
	}
}

func TestRedactSecrets_NoFalsePositives(t *testing.T) {
	cases := []string{
		"The quick brown fox jumps over the lazy dog. The quick brown fox jumps over the lazy dog.",
		"Running migration: ALTER TABLE users ADD COLUMN last_seen timestamptz NOT NULL DEFAULT now();",
		"user opened https://example.com/docs/getting-started?ref=home and read for three minutes",
		"package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() { fmt.Println(\"hello\") }",
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt.",
	}
	for _, in := range cases {
		out := redactSecrets(in)
		if out != in {
			t.Errorf("false-positive redaction: in=%q out=%q", in, out)
		}
	}
}

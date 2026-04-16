package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverFromEnv_WithRoles(t *testing.T) {
	r := NewResolverFromEnv("tok1=alice@co.com:admin,tok2=bob@co.com:viewer,tok3=carol@co.com")

	tests := []struct {
		token   string
		email   string
		role    string
		wantErr bool
	}{
		{"tok1", "alice@co.com", RoleAdmin, false},
		{"tok2", "bob@co.com", RoleViewer, false},
		{"tok3", "carol@co.com", RoleViewer, false},
		{"bad", "", "", true},
	}
	for _, tc := range tests {
		u, err := r.Resolve(tc.token)
		if tc.wantErr {
			if err == nil {
				t.Errorf("token %q: expected error", tc.token)
			}
			continue
		}
		if err != nil {
			t.Fatalf("token %q: %v", tc.token, err)
		}
		if u.Email != tc.email {
			t.Errorf("token %q: email = %q; want %q", tc.token, u.Email, tc.email)
		}
		if u.Role != tc.role {
			t.Errorf("token %q: role = %q; want %q", tc.token, u.Role, tc.role)
		}
	}
}

func TestResolverFromEnv_Empty(t *testing.T) {
	r := NewResolverFromEnv("")
	if r.UserCount() != 0 {
		t.Errorf("UserCount = %d; want 0", r.UserCount())
	}
}

func TestSingleTokenResolver(t *testing.T) {
	r := NewSingleTokenResolver("mytoken", "admin@co.com", "admin")
	u, err := r.Resolve("mytoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Email != "admin@co.com" || u.Role != RoleAdmin {
		t.Errorf("got %+v", u)
	}
	_, err = r.Resolve("other")
	if err == nil {
		t.Error("expected error for wrong token")
	}
}

func TestSingleTokenResolver_InvalidRole(t *testing.T) {
	r := NewSingleTokenResolver("tok", "u@co.com", "invalid")
	u, _ := r.Resolve("tok")
	if u.Role != RoleViewer {
		t.Errorf("role = %q; want viewer (default)", u.Role)
	}
}

func TestResolverFromFile(t *testing.T) {
	content := `{
		"users": [
			{"token": "t1", "email": "admin@test.com", "role": "admin"},
			{"token": "t2", "email": "user@test.com", "role": "viewer"},
			{"token": "t3", "email": "norole@test.com"}
		]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	os.WriteFile(path, []byte(content), 0644)

	r, err := NewResolverFromFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.UserCount() != 3 {
		t.Errorf("UserCount = %d; want 3", r.UserCount())
	}

	u, _ := r.Resolve("t1")
	if u.Role != RoleAdmin {
		t.Errorf("t1 role = %q; want admin", u.Role)
	}
	u, _ = r.Resolve("t3")
	if u.Role != RoleViewer {
		t.Errorf("t3 role = %q; want viewer (default)", u.Role)
	}
}

func TestResolverFromFile_MissingFile(t *testing.T) {
	_, err := NewResolverFromFile("/no/such/file.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestResolverFromFile_EmptyUsers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	os.WriteFile(path, []byte(`{"users": []}`), 0644)

	_, err := NewResolverFromFile(path)
	if err == nil {
		t.Error("expected error for empty users")
	}
}

func TestUser_Roles(t *testing.T) {
	admin := User{Role: RoleAdmin}
	viewer := User{Role: RoleViewer}

	if !admin.IsAdmin() || admin.IsViewer() {
		t.Error("admin role checks failed")
	}
	if viewer.IsAdmin() || !viewer.IsViewer() {
		t.Error("viewer role checks failed")
	}
}

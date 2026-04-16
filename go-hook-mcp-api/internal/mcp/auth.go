package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

var ErrUnauthorized = errors.New("unauthorized: invalid or missing token")
var ErrForbidden = errors.New("forbidden: insufficient permissions")

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

type User struct {
	Token string `json:"token"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (u User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u User) IsViewer() bool {
	return u.Role == RoleViewer
}

type UserResolver interface {
	Resolve(token string) (User, error)
}

// StaticResolver maps tokens to users.
type StaticResolver struct {
	users map[string]User
}

func (s *StaticResolver) Resolve(token string) (User, error) {
	u, ok := s.users[token]
	if !ok {
		return User{}, ErrUnauthorized
	}
	return u, nil
}

func (s *StaticResolver) UserCount() int {
	return len(s.users)
}

// --- Constructors ---

// NewResolverFromFile loads users from a JSON file.
// File format: { "users": [ { "token": "...", "email": "...", "role": "admin|viewer" } ] }
func NewResolverFromFile(path string) (*StaticResolver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users file %s: %w", path, err)
	}

	var cfg struct {
		Users []User `json:"users"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse users file %s: %w", path, err)
	}

	users := make(map[string]User, len(cfg.Users))
	for _, u := range cfg.Users {
		if u.Token == "" || u.Email == "" {
			continue
		}
		if u.Role != RoleAdmin && u.Role != RoleViewer {
			u.Role = RoleViewer // default to viewer
		}
		users[u.Token] = u
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("users file %s has no valid users", path)
	}

	return &StaticResolver{users: users}, nil
}

// NewResolverFromEnv creates a resolver from MCP_USER_TOKENS env var.
// Format: "token1=email1:admin,token2=email2:viewer,token3=email3"
// If role is omitted, defaults to viewer.
func NewResolverFromEnv(config string) *StaticResolver {
	users := make(map[string]User)
	if config == "" {
		return &StaticResolver{users: users}
	}
	for _, entry := range strings.Split(config, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		token := strings.TrimSpace(parts[0])
		emailRole := strings.TrimSpace(parts[1])

		email := emailRole
		role := RoleViewer
		if idx := strings.LastIndex(emailRole, ":"); idx > 0 {
			email = emailRole[:idx]
			r := emailRole[idx+1:]
			if r == RoleAdmin || r == RoleViewer {
				role = r
			}
		}

		users[token] = User{Token: token, Email: email, Role: role}
	}
	return &StaticResolver{users: users}
}

// NewSingleTokenResolver creates a resolver for stdio mode with a single token.
func NewSingleTokenResolver(token, email, role string) *StaticResolver {
	if role != RoleAdmin && role != RoleViewer {
		role = RoleViewer
	}
	users := map[string]User{
		token: {Token: token, Email: email, Role: role},
	}
	return &StaticResolver{users: users}
}

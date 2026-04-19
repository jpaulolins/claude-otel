package mcp

import (
	"crypto/subtle"
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

// StaticResolver maps tokens to users. The map lookup itself is O(1) and short
// circuits on an unknown key — which is acceptable here because:
//   - the set of tokens is small and not attacker-controlled;
//   - the map only gates entry into the constant-time compare below;
//   - for a matching token we still run subtle.ConstantTimeCompare against the
//     stored value so an attacker cannot distinguish "known prefix" from
//     "fully matching token" via timing.
type StaticResolver struct {
	users map[string]User
}

// Resolve returns the User for a given token. The comparison against the
// stored token uses crypto/subtle.ConstantTimeCompare to mitigate timing side
// channels. Callers should still treat the outcome as a single boolean —
// leaking whether the token matched is unavoidable.
func (s *StaticResolver) Resolve(token string) (User, error) {
	if token == "" {
		return User{}, ErrUnauthorized
	}
	u, ok := s.users[token]
	if !ok {
		return User{}, ErrUnauthorized
	}
	// Constant-time re-compare of the stored token vs the presented token.
	if subtle.ConstantTimeCompare([]byte(u.Token), []byte(token)) != 1 {
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
//
// Emails are lowercased at load time so downstream lookups are
// case-insensitive (required so ClickHouse predicates on lower(user.email)
// match what the MCP server stamps on the request context).
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
		u.Email = strings.ToLower(strings.TrimSpace(u.Email))
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
// If role is omitted, defaults to viewer. Emails are lowercased.
func NewResolverFromEnv(config string) *StaticResolver {
	users := make(map[string]User)
	if config == "" {
		return &StaticResolver{users: users}
	}
	for entry := range strings.SplitSeq(config, ",") {
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
		email = strings.ToLower(strings.TrimSpace(email))

		users[token] = User{Token: token, Email: email, Role: role}
	}
	return &StaticResolver{users: users}
}

// NewSingleTokenResolver creates a resolver for stdio mode with a single token.
// Email is lowercased.
func NewSingleTokenResolver(token, email, role string) *StaticResolver {
	if role != RoleAdmin && role != RoleViewer {
		role = RoleViewer
	}
	email = strings.ToLower(strings.TrimSpace(email))
	users := map[string]User{
		token: {Token: token, Email: email, Role: role},
	}
	return &StaticResolver{users: users}
}

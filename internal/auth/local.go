// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"log/slog"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// userFinder is the slice of the store the local strategy needs (interface
// segregation keeps it trivially testable).
type userFinder interface {
	FindUser(ctx context.Context, username string) (*db.User, error)
}

// LocalStrategy authenticates against bcrypt password hashes in the store — the
// Go port of src/service/passport/local.ts.
type LocalStrategy struct {
	users userFinder
}

// NewLocalStrategy builds a local strategy over the store.
func NewLocalStrategy(store db.Store) *LocalStrategy { return &LocalStrategy{users: store} }

// Type returns the strategy type name.
func (s *LocalStrategy) Type() string { return "local" }

// Authenticate verifies the username/password against the stored hash. It
// returns (nil, nil) when the user is unknown, has no password (OIDC user) or
// the password is wrong — matching Passport's done(null, false).
func (s *LocalStrategy) Authenticate(ctx context.Context, username, password string) (*db.User, error) {
	user, err := s.users.FindUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if user == nil || user.Password == nil {
		return nil, nil
	}
	if !CheckPassword(*user.Password, password) {
		return nil, nil
	}
	return user, nil
}

// defaultUsers mirrors createDefaultAdmin in the Node local strategy.
var defaultUsers = []struct {
	username, password, email string
	admin                     bool
}{
	{"admin", "admin", "admin@place.com", true},
	{"user", "user", "user@place.com", false},
}

// CreateDefaultAdmin seeds the default admin/user accounts if they are missing,
// mirroring createDefaultAdmin in src/service/passport/local.ts. Passwords are
// bcrypt-hashed (gitAccount "none").
func CreateDefaultAdmin(ctx context.Context, store db.Store) error {
	for _, d := range defaultUsers {
		existing, err := store.FindUser(ctx, d.username)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		hash, err := HashPassword(d.password)
		if err != nil {
			return err
		}
		if err := store.CreateUser(ctx, &db.User{
			Username:   d.username,
			Password:   &hash,
			Email:      d.email,
			GitAccount: "none",
			Admin:      d.admin,
		}); err != nil {
			return err
		}
		slog.Info("created default user", "username", d.username, "admin", d.admin)
	}
	return nil
}

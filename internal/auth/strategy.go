// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"strings"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// LoginStrategy authenticates a username/password login (the local and — when
// added — AD strategies). It is the Go equivalent of a Passport username/
// password strategy. Authenticate returns (nil, nil) for invalid credentials
// and a non-nil error only for unexpected failures (e.g. the store).
type LoginStrategy interface {
	Type() string
	Authenticate(ctx context.Context, username, password string) (*db.User, error)
}

// Registry is the Passport-equivalent multiplex: it holds the enabled auth
// methods and the username/password strategy that /login should use.
type Registry struct {
	enabledTypes []string
	login        LoginStrategy
}

// BuildRegistry constructs the registry from the enabled authentication methods
// in the configuration. Only the local strategy is implemented today; other
// enabled types are still recorded (so /api/auth/config reports them) but have
// no login handler. The first enabled username/password method becomes the
// /login strategy, mirroring getLoginStrategy in the Node auth routes. (AD,
// also a username/password method, is deferred — see issue #33.)
func BuildRegistry(cfg *config.Config, store db.Store) *Registry {
	r := &Registry{}
	for _, m := range cfg.Authentication {
		if !m.Enabled {
			continue
		}
		t := strings.ToLower(string(m.Type))
		r.enabledTypes = append(r.enabledTypes, t)
		if r.login == nil && t == "local" {
			r.login = NewLocalStrategy(store)
		}
	}
	return r
}

// LoginStrategy returns the configured username/password strategy, or nil when
// none is enabled (in which case /login responds 403, as in Node).
func (r *Registry) LoginStrategy() LoginStrategy { return r.login }

// LoginType returns the type name of the login strategy, or "" if none.
func (r *Registry) LoginType() string {
	if r.login == nil {
		return ""
	}
	return r.login.Type()
}

// EnabledTypes returns every enabled auth method type (lower-cased).
func (r *Registry) EnabledTypes() []string { return r.enabledTypes }

// OtherMethods returns the enabled types except the username/password login
// type, for /api/auth/config.
func (r *Registry) OtherMethods() []string {
	login := r.LoginType()
	others := make([]string, 0, len(r.enabledTypes))
	for _, t := range r.enabledTypes {
		if t != login {
			others = append(others, t)
		}
	}
	return others
}

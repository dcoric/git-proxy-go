// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !CheckPassword(hash, "hunter2") {
		t.Error("CheckPassword rejected the correct password")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("CheckPassword accepted the wrong password")
	}
}

// fakeFinder implements the local strategy's userFinder.
type fakeFinder struct{ users map[string]*db.User }

func (f fakeFinder) FindUser(_ context.Context, username string) (*db.User, error) {
	return f.users[username], nil
}

func TestLocalStrategyAuthenticate(t *testing.T) {
	hash, _ := HashPassword("s3cret")
	finder := fakeFinder{users: map[string]*db.User{
		"alice": {Username: "alice", Password: &hash},
		"oidc":  {Username: "oidc", Password: nil}, // OIDC user, no password
	}}
	s := &LocalStrategy{users: finder}
	ctx := context.Background()

	got, err := s.Authenticate(ctx, "alice", "s3cret")
	if err != nil || got == nil || got.Username != "alice" {
		t.Fatalf("Authenticate(correct) = %v, %v", got, err)
	}
	if u, _ := s.Authenticate(ctx, "alice", "wrong"); u != nil {
		t.Error("Authenticate accepted a wrong password")
	}
	if u, _ := s.Authenticate(ctx, "ghost", "x"); u != nil {
		t.Error("Authenticate accepted an unknown user")
	}
	if u, _ := s.Authenticate(ctx, "oidc", ""); u != nil {
		t.Error("Authenticate accepted a passwordless (OIDC) user")
	}
	if s.Type() != "local" {
		t.Errorf("Type() = %q, want local", s.Type())
	}
}

func TestBuildRegistry(t *testing.T) {
	cfgWith := func(types ...generated.AuthenticationElementType) *config.Config {
		c := &config.Config{}
		for _, ty := range types {
			c.Authentication = append(c.Authentication, generated.AuthenticationElement{Type: ty, Enabled: true})
		}
		// a disabled method must be ignored
		c.Authentication = append(c.Authentication, generated.AuthenticationElement{Type: generated.Jwt, Enabled: false})
		return c
	}

	reg := BuildRegistry(cfgWith(generated.Local, generated.Openidconnect), nil)
	if reg.LoginType() != "local" {
		t.Errorf("LoginType() = %q, want local", reg.LoginType())
	}
	if got := reg.OtherMethods(); len(got) != 1 || got[0] != "openidconnect" {
		t.Errorf("OtherMethods() = %v, want [openidconnect]", got)
	}

	// No username/password method enabled -> no login strategy (login 403s).
	regNoLogin := BuildRegistry(cfgWith(generated.Openidconnect), nil)
	if regNoLogin.LoginStrategy() != nil || regNoLogin.LoginType() != "" {
		t.Errorf("expected no login strategy, got %q", regNoLogin.LoginType())
	}
}

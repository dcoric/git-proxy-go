// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// oidcType is the registry/config type name for the OIDC strategy.
const oidcType = "openidconnect"

// oidcUserStore is the slice of the store the OIDC strategy needs to provision
// users (interface segregation, as in local.go).
type oidcUserStore interface {
	FindUserByOIDC(ctx context.Context, oidcID string) (*db.User, error)
	CreateUser(ctx context.Context, user *db.User) error
}

// OIDCStrategy authenticates users via OpenID Connect — the Go port of
// src/service/passport/oidc.ts. NewOIDCStrategy performs provider discovery, so
// configuration errors surface at startup; AuthCodeURL/Exchange drive the
// authorization-code flow from the HTTP routes.
type OIDCStrategy struct {
	oauth2   oauth2.Config
	verifier *oidc.IDTokenVerifier
	provider *oidc.Provider
	store    oidcUserStore
}

// EnabledOIDCConfig returns the oidcConfig of the first enabled openidconnect
// authentication method, or nil when none is enabled. Keeping the lookup here
// lets the entrypoint construct the strategy without importing the generated
// config package.
func EnabledOIDCConfig(cfg *config.Config) *generated.OidcConfig {
	for i := range cfg.Authentication {
		m := &cfg.Authentication[i]
		if m.Enabled && strings.EqualFold(string(m.Type), oidcType) {
			return m.OidcConfig
		}
	}
	return nil
}

// NewOIDCStrategy builds the OIDC strategy: it discovers the provider's
// endpoints (oidc.NewProvider), constructs the oauth2 config (redirect URL +
// scopes incl. openid) and the ID-token verifier. Discovery failures are
// returned so the caller can fail fast at startup, mirroring the Node
// discovery() error handling.
func NewOIDCStrategy(ctx context.Context, oidcConfig *generated.OidcConfig, store oidcUserStore) (*OIDCStrategy, error) {
	if oidcConfig == nil || oidcConfig.Issuer == "" {
		return nil, fmt.Errorf("oidc: missing issuer in configuration")
	}

	provider, err := oidc.NewProvider(ctx, oidcConfig.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc setup error (discovery): %w", err)
	}

	return &OIDCStrategy{
		oauth2: oauth2.Config{
			ClientID:     oidcConfig.ClientID,
			ClientSecret: oidcConfig.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  oidcConfig.CallbackURL,
			Scopes:       parseScopes(oidcConfig.Scope),
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: oidcConfig.ClientID}),
		provider: provider,
		store:    store,
	}, nil
}

// Type returns the strategy type name.
func (s *OIDCStrategy) Type() string { return oidcType }

// AuthCodeURL returns the provider's authorization URL for the given state,
// which the start route redirects the browser to.
func (s *OIDCStrategy) AuthCodeURL(state string) string {
	return s.oauth2.AuthCodeURL(state)
}

// Exchange completes the authorization-code flow: it swaps the code for tokens,
// verifies the ID token, fetches the userinfo, and provisions/looks up the
// local user. It is the Go equivalent of the Node verify callback +
// handleUserAuthentication.
func (s *OIDCStrategy) Exchange(ctx context.Context, code string) (*db.User, error) {
	token, err := s.oauth2.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc: token exchange failed: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, fmt.Errorf("oidc: no id_token in token response")
	}
	if _, err := s.verifier.Verify(ctx, rawIDToken); err != nil {
		return nil, fmt.Errorf("oidc: id_token verification failed: %w", err)
	}

	rawInfo, err := s.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return nil, fmt.Errorf("oidc: userinfo request failed: %w", err)
	}
	var info userInfo
	if err := rawInfo.Claims(&info); err != nil {
		return nil, fmt.Errorf("oidc: decoding userinfo claims: %w", err)
	}
	if info.Sub == "" {
		info.Sub = rawInfo.Subject
	}

	return s.provision(ctx, info)
}

// userInfo is the subset of the OIDC userinfo response we consume. Providers
// disagree on the email field, hence the alternate emails array (see
// safelyExtractEmail).
type userInfo struct {
	Sub    string `json:"sub"`
	Email  string `json:"email"`
	Emails []struct {
		Value string `json:"value"`
	} `json:"emails"`
}

// provision looks up the user by OIDC subject and, on first login, creates a
// local account derived from the profile email. Port of handleUserAuthentication.
func (s *OIDCStrategy) provision(ctx context.Context, info userInfo) (*db.User, error) {
	user, err := s.store.FindUserByOIDC(ctx, info.Sub)
	if err != nil {
		return nil, err
	}
	if user != nil {
		return user, nil
	}

	email := safelyExtractEmail(info)
	if email == "" {
		return nil, fmt.Errorf("oidc: no email found in OIDC profile")
	}

	sub := info.Sub
	newUser := &db.User{
		Username:   getUsername(email),
		Email:      email,
		OIDCID:     &sub,
		GitAccount: "Edit me",
		Admin:      false,
	}
	if err := s.store.CreateUser(ctx, newUser); err != nil {
		return nil, err
	}
	return newUser, nil
}

// safelyExtractEmail pulls the email from a userinfo response, falling back to
// the first entry of an emails array (providers differ). Port of
// safelyExtractEmail.
func safelyExtractEmail(info userInfo) string {
	if info.Email != "" {
		return info.Email
	}
	if len(info.Emails) > 0 {
		return info.Emails[0].Value
	}
	return ""
}

// getUsername derives a username from the local part of an email. Port of
// getUsername; note this is incompatible with multiple providers (see the Node
// comment) but matches the existing behaviour.
func getUsername(email string) string {
	if email == "" {
		return ""
	}
	return strings.SplitN(email, "@", 2)[0]
}

// parseScopes splits the space-separated scope string and guarantees the openid
// scope is present (required for OIDC).
func parseScopes(scope string) []string {
	scopes := strings.Fields(scope)
	for _, s := range scopes {
		if s == oidc.ScopeOpenID {
			return scopes
		}
	}
	return append([]string{oidc.ScopeOpenID}, scopes...)
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
)

// ctxKey is the private context key type for the JWT-authenticated principal.
type ctxKey int

const principalKey ctxKey = iota

// Principal is the JWT-authenticated API caller stored on the request context.
type Principal struct {
	Claims jwt.MapClaims
	Roles  map[string]bool
}

// PrincipalFromContext returns the JWT principal set by the API middleware, if any.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey).(*Principal)
	return p, ok
}

// JWTMiddlewareOptions configures the API JWT middleware.
type JWTMiddlewareOptions struct {
	// AlreadyAuthenticated lets an existing session bypass JWT (mirrors the Node
	// req.isAuthenticated() short-circuit). Optional.
	AlreadyAuthenticated func(*http.Request) bool
	// Fetch overrides JWKS retrieval (for tests). Optional.
	Fetch JWKSFetcher
}

// JWTMiddleware returns API-auth middleware mirroring jwtAuthHandler.ts: when a
// JWT method is enabled in apiAuthentication, requests must carry a valid Bearer
// token (verified against the authority's JWKS); otherwise the middleware is a
// pass-through. The verified principal (claims + mapped roles) is attached to
// the request context.
//
// It is not wired into the router yet — the API route groups it guards land in
// P4; this delivers and tests the middleware itself (P3-7).
func JWTMiddleware(cfg *config.Config, opts JWTMiddlewareOptions) func(http.Handler) http.Handler {
	jwtMethod := findEnabledJWT(cfg)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if jwtMethod == nil {
				next.ServeHTTP(w, r)
				return
			}
			if opts.AlreadyAuthenticated != nil && opts.AlreadyAuthenticated(r) {
				next.ServeHTTP(w, r)
				return
			}

			authz := r.Header.Get("Authorization")
			if authz == "" {
				http.Error(w, "No token provided", http.StatusUnauthorized)
				return
			}

			jc := jwtMethod.JwtConfig
			if jc == nil || jc.AuthorityURL == "" || jc.ClientID == "" {
				http.Error(w, "JWT configuration is incomplete", http.StatusInternalServerError)
				return
			}
			audience := jc.ClientID
			if jc.ExpectedAudience != nil && *jc.ExpectedAudience != "" {
				audience = *jc.ExpectedAudience
			}

			token := authz
			if parts := strings.Fields(authz); len(parts) == 2 {
				token = parts[1]
			}

			claims, err := ValidateJWT(r.Context(), token, jc.AuthorityURL, audience, jc.ClientID, opts.Fetch)
			if err != nil {
				http.Error(w, "JWT validation failed", http.StatusUnauthorized)
				return
			}

			p := &Principal{Claims: claims, Roles: AssignRoles(jc.RoleMapping, claims)}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
		})
	}
}

// findEnabledJWT returns the first enabled jwt method in apiAuthentication.
func findEnabledJWT(cfg *config.Config) *generated.AuthenticationElement {
	for i := range cfg.APIAuthentication {
		m := &cfg.APIAuthentication[i]
		if m.Enabled && strings.EqualFold(string(m.Type), "jwt") {
			return m
		}
	}
	return nil
}

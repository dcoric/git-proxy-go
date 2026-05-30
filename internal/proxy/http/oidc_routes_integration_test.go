// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

//go:build integration

// OIDC route integration test (P3-8 gate): drives the openidconnect UI-login
// path end-to-end — GET /openidconnect -> mock IdP -> /callback -> session ->
// /profile — against a real Postgres-backed session store (dockertest, shared
// with auth_integration_test.go) and a mock OIDC issuer.
package proxyhttp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/session"
)

const idpKid = "idp-key-1"

// mockIdP is a minimal OIDC issuer: discovery, JWKS, token and userinfo. The
// authorization endpoint is never hit (the test captures the start redirect
// instead of following it).
type mockIdP struct {
	*httptest.Server
	key      *rsa.PrivateKey
	clientID string
	sub      string
	email    string
}

func newMockIdP(t *testing.T, clientID, sub, email string) *mockIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	idp := &mockIdP{key: key, clientID: clientID, sub: sub, email: email}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	idp.Server = srv

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		idp.encode(t, w, map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"userinfo_endpoint":                     srv.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		eBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(eBytes, uint64(key.E))
		i := 0
		for i < len(eBytes)-1 && eBytes[i] == 0 {
			i++
		}
		idp.encode(t, w, map[string]any{
			"keys": []map[string]string{{
				"kid": idpKid, "kty": "RSA", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(eBytes[i:]),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": srv.URL,
			"aud": clientID,
			"sub": idp.sub,
			"exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(),
		})
		tok.Header["kid"] = idpKid
		idToken, err := tok.SignedString(key)
		if err != nil {
			t.Errorf("sign id_token: %v", err)
		}
		idp.encode(t, w, map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{"sub": idp.sub}
		if idp.email != "" {
			resp["email"] = idp.email
		}
		idp.encode(t, w, resp)
	})

	t.Cleanup(srv.Close)
	return idp
}

func (idp *mockIdP) encode(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

// oidcRouter builds a router with the OIDC strategy enabled against idp, sharing
// the Postgres-backed session store and user store from TestMain.
func oidcRouter(t *testing.T, idp *mockIdP, redirectURL string) http.Handler {
	t.Helper()
	strategy, err := auth.NewOIDCStrategy(context.Background(), &generated.OidcConfig{
		Issuer:       idp.URL,
		ClientID:     idp.clientID,
		ClientSecret: "test-secret",
		CallbackURL:  "http://localhost:8080/api/auth/openidconnect/callback",
		Scope:        "openid email profile",
	}, testStore)
	if err != nil {
		t.Fatalf("NewOIDCStrategy: %v", err)
	}

	cfg := &config.Config{}
	cfg.Authentication = []generated.AuthenticationElement{{Type: generated.Openidconnect, Enabled: true}}
	registry := auth.BuildRegistry(cfg, testStore)
	registry.EnableOIDC(strategy)

	sessions := session.New(stdlib.OpenDBFromPool(testStore.Pool()), time.Hour, false)
	return NewRouter(Options{
		Sessions:        sessions,
		Store:           testStore,
		Auth:            registry,
		CSRFProtection:  true,
		OIDCRedirectURL: redirectURL,
	})
}

// noRedirectClient returns a cookie-jar client that does not auto-follow
// redirects (so the test can inspect each 302).
func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestOIDCLoginFlow(t *testing.T) {
	const clientID = "git-proxy-ui"
	idp := newMockIdP(t, clientID, "oidc-route-sub-1", "oidcuser@example.com")
	const redirectURL = "http://localhost:8080/dashboard/profile"

	srv := httptest.NewServer(oidcRouter(t, idp, redirectURL))
	defer srv.Close()
	c := noRedirectClient(t)

	// 1. Start: 302 to the IdP authorization endpoint with a state we can read.
	start, err := c.Get(srv.URL + "/api/auth/openidconnect")
	if err != nil {
		t.Fatalf("GET /openidconnect: %v", err)
	}
	_ = start.Body.Close()
	if start.StatusCode != http.StatusFound {
		t.Fatalf("start status = %d, want 302", start.StatusCode)
	}
	authURL, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	state := authURL.Query().Get("state")
	if state == "" {
		t.Fatal("start redirect carried no state")
	}

	// 2. Callback with the matching state + an authorization code.
	cb, err := c.Get(srv.URL + "/api/auth/openidconnect/callback?state=" + url.QueryEscape(state) + "&code=test-code")
	if err != nil {
		t.Fatalf("GET /callback: %v", err)
	}
	_ = cb.Body.Close()
	if cb.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", cb.StatusCode)
	}
	if loc := cb.Header.Get("Location"); loc != redirectURL {
		t.Errorf("callback redirect = %q, want %q", loc, redirectURL)
	}

	// 3. Profile is served from the established session; the user was provisioned.
	pr, err := c.Get(srv.URL + "/api/auth/profile")
	if err != nil {
		t.Fatalf("GET /profile: %v", err)
	}
	defer func() { _ = pr.Body.Close() }()
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("profile status = %d, want 200", pr.StatusCode)
	}
	var profile struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	if err := json.NewDecoder(pr.Body).Decode(&profile); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if profile.Username != "oidcuser" {
		t.Errorf("profile username = %q, want oidcuser", profile.Username)
	}
}

func TestOIDCCallbackRejectsBadState(t *testing.T) {
	const clientID = "git-proxy-ui"
	idp := newMockIdP(t, clientID, "oidc-route-sub-2", "other@example.com")
	srv := httptest.NewServer(oidcRouter(t, idp, "http://localhost:8080/dashboard/profile"))
	defer srv.Close()

	// No prior /openidconnect call, so the session holds no state -> 400.
	c := noRedirectClient(t)
	resp, err := c.Get(srv.URL + "/api/auth/openidconnect/callback?state=bogus&code=test-code")
	if err != nil {
		t.Fatalf("GET /callback: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback with bad state = %d, want 400", resp.StatusCode)
	}
}

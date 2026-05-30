// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
)

func TestSafelyExtractEmail(t *testing.T) {
	tests := []struct {
		name string
		info userInfo
		want string
	}{
		{"direct email", userInfo{Email: "a@example.com"}, "a@example.com"},
		{"emails array fallback", userInfo{Emails: []struct {
			Value string `json:"value"`
		}{{Value: "b@example.com"}}}, "b@example.com"},
		{"email preferred over array", userInfo{Email: "a@example.com", Emails: []struct {
			Value string `json:"value"`
		}{{Value: "b@example.com"}}}, "a@example.com"},
		{"none", userInfo{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := safelyExtractEmail(tc.info); got != tc.want {
				t.Errorf("safelyExtractEmail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetUsername(t *testing.T) {
	tests := map[string]string{
		"alice@example.com": "alice",
		"bob@corp.internal": "bob",
		"weird@a@b.com":     "weird",
		"":                  "",
		"nolocalpart":       "nolocalpart",
	}
	for email, want := range tests {
		if got := getUsername(email); got != want {
			t.Errorf("getUsername(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestParseScopes(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"openid email profile", []string{"openid", "email", "profile"}},
		{"email profile", []string{"openid", "email", "profile"}},
		{"", []string{"openid"}},
		{"openid", []string{"openid"}},
	}
	for _, tc := range tests {
		got := parseScopes(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("parseScopes(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseScopes(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// fakeOIDCStore implements oidcUserStore for provisioning tests.
type fakeOIDCStore struct {
	byOIDC  map[string]*db.User
	created []*db.User
	findErr error
}

func (f *fakeOIDCStore) FindUserByOIDC(_ context.Context, id string) (*db.User, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.byOIDC[id], nil
}

func (f *fakeOIDCStore) CreateUser(_ context.Context, u *db.User) error {
	f.created = append(f.created, u)
	if f.byOIDC == nil {
		f.byOIDC = map[string]*db.User{}
	}
	if u.OIDCID != nil {
		f.byOIDC[*u.OIDCID] = u
	}
	return nil
}

// mockOIDCProvider is a test OIDC issuer serving discovery, JWKS, token and
// userinfo endpoints — enough to drive the authorization-code flow. It reuses
// the RSA-key / JWKS pattern from jwt_test.go (signToken/testKid).
type mockOIDCProvider struct {
	*httptest.Server
	key   *rsa.PrivateKey
	sub   string
	email string // omitted from /userinfo when empty
}

func newMockOIDCProvider(t *testing.T, clientID, sub, email string) *mockOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	m := &mockOIDCProvider{key: key, sub: sub, email: email}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	m.Server = srv

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
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
		writeJSON(t, w, map[string]any{
			"keys": []map[string]string{{
				"kid": testKid, "kty": "RSA", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(eBytes[i:]),
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		idToken := signToken(t, key, jwt.MapClaims{
			"iss": srv.URL,
			"aud": clientID,
			"sub": m.sub,
			"exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(),
		})
		writeJSON(t, w, map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{"sub": m.sub}
		if m.email != "" {
			resp["email"] = m.email
		}
		writeJSON(t, w, resp)
	})

	t.Cleanup(srv.Close)
	return m
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func newTestStrategy(t *testing.T, m *mockOIDCProvider, clientID string, store oidcUserStore) *OIDCStrategy {
	t.Helper()
	s, err := NewOIDCStrategy(context.Background(), &generated.OidcConfig{
		Issuer:       m.URL,
		ClientID:     clientID,
		ClientSecret: "test-secret",
		CallbackURL:  "http://localhost:8080/api/auth/openidconnect/callback",
		Scope:        "openid email profile",
	}, store)
	if err != nil {
		t.Fatalf("NewOIDCStrategy: %v", err)
	}
	return s
}

func TestOIDCExchangeProvisionsNewUser(t *testing.T) {
	const clientID = "git-proxy"
	m := newMockOIDCProvider(t, clientID, "oidc-sub-1", "alice@example.com")
	store := &fakeOIDCStore{}
	s := newTestStrategy(t, m, clientID, store)

	user, err := s.Exchange(context.Background(), "test-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if user.Username != "alice" {
		t.Errorf("username = %q, want alice", user.Username)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d users, want 1", len(store.created))
	}
	got := store.created[0]
	if got.Email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", got.Email)
	}
	if got.OIDCID == nil || *got.OIDCID != "oidc-sub-1" {
		t.Errorf("oidcId = %v, want oidc-sub-1", got.OIDCID)
	}
	if got.Password != nil {
		t.Errorf("password = %v, want nil for OIDC user", got.Password)
	}
	if got.GitAccount != "Edit me" {
		t.Errorf("gitAccount = %q, want \"Edit me\"", got.GitAccount)
	}
	if got.Admin {
		t.Error("admin = true, want false")
	}
}

func TestOIDCExchangeExistingUser(t *testing.T) {
	const clientID = "git-proxy"
	m := newMockOIDCProvider(t, clientID, "oidc-sub-1", "alice@example.com")
	existing := &db.User{Username: "existing", Email: "alice@example.com"}
	store := &fakeOIDCStore{byOIDC: map[string]*db.User{"oidc-sub-1": existing}}
	s := newTestStrategy(t, m, clientID, store)

	user, err := s.Exchange(context.Background(), "test-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if user.Username != "existing" {
		t.Errorf("username = %q, want existing", user.Username)
	}
	if len(store.created) != 0 {
		t.Errorf("created %d users, want 0 (existing user)", len(store.created))
	}
}

func TestOIDCExchangeNoEmail(t *testing.T) {
	const clientID = "git-proxy"
	m := newMockOIDCProvider(t, clientID, "oidc-sub-1", "") // userinfo returns no email
	store := &fakeOIDCStore{}
	s := newTestStrategy(t, m, clientID, store)

	if _, err := s.Exchange(context.Background(), "test-code"); err == nil {
		t.Error("expected error when OIDC profile has no email")
	}
	if len(store.created) != 0 {
		t.Errorf("created %d users, want 0", len(store.created))
	}
}

func TestNewOIDCStrategyMissingIssuer(t *testing.T) {
	if _, err := NewOIDCStrategy(context.Background(), &generated.OidcConfig{}, &fakeOIDCStore{}); err == nil {
		t.Error("expected error for missing issuer")
	}
	if _, err := NewOIDCStrategy(context.Background(), nil, &fakeOIDCStore{}); err == nil {
		t.Error("expected error for nil config")
	}
}

func TestAuthCodeURL(t *testing.T) {
	const clientID = "git-proxy"
	m := newMockOIDCProvider(t, clientID, "oidc-sub-1", "alice@example.com")
	s := newTestStrategy(t, m, clientID, &fakeOIDCStore{})

	authURL := s.AuthCodeURL("xyz-state")
	if authURL == "" {
		t.Fatal("AuthCodeURL returned empty string")
	}
	for _, want := range []string{"state=xyz-state", "client_id=" + clientID, "scope=openid", "/authorize"} {
		if !strings.Contains(authURL, want) {
			t.Errorf("AuthCodeURL = %q, missing %q", authURL, want)
		}
	}
}

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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
)

const testKid = "test-key-1"

// mockIssuer is a test OIDC authority serving discovery + JWKS for key.
func mockIssuer(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		eBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(eBytes, uint64(key.E))
		// trim leading zero bytes of the exponent
		i := 0
		for i < len(eBytes)-1 && eBytes[i] == 0 {
			i++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kid": testKid, "kty": "RSA", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(eBytes[i:]),
			}},
		})
	})
	t.Cleanup(srv.Close)
	return srv
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestValidateJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	iss := mockIssuer(t, key)
	ctx := context.Background()
	const clientID = "git-proxy"

	base := func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss":  iss.URL,
			"aud":  clientID,
			"azp":  clientID,
			"sub":  "user-1",
			"name": "John Doe",
			"exp":  time.Now().Add(time.Hour).Unix(),
		}
	}

	// Valid token.
	claims, err := ValidateJWT(ctx, signToken(t, key, base()), iss.URL, clientID, clientID, nil)
	if err != nil {
		t.Fatalf("ValidateJWT(valid): %v", err)
	}
	if claims["name"] != "John Doe" {
		t.Errorf("claims[name] = %v, want John Doe", claims["name"])
	}

	// Wrong audience.
	if _, err := ValidateJWT(ctx, signToken(t, key, base()), iss.URL, "other-aud", clientID, nil); err == nil {
		t.Error("expected error for wrong audience")
	}

	// azp mismatch.
	bad := base()
	bad["azp"] = "someone-else"
	if _, err := ValidateJWT(ctx, signToken(t, key, bad), iss.URL, clientID, clientID, nil); err == nil {
		t.Error("expected error for azp mismatch")
	}

	// Expired.
	exp := base()
	exp["exp"] = time.Now().Add(-time.Hour).Unix()
	if _, err := ValidateJWT(ctx, signToken(t, key, exp), iss.URL, clientID, clientID, nil); err == nil {
		t.Error("expected error for expired token")
	}

	// Token signed by a different key is rejected.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := ValidateJWT(ctx, signToken(t, other, base()), iss.URL, clientID, clientID, nil); err == nil {
		t.Error("expected error for wrong signing key")
	}
}

func TestAssignRoles(t *testing.T) {
	rm := &generated.RoleMapping{Admin: map[string]any{"name": "John Doe"}}

	roles := AssignRoles(rm, jwt.MapClaims{"name": "John Doe"})
	if !roles["admin"] {
		t.Error("admin role not assigned for matching claim")
	}
	roles = AssignRoles(rm, jwt.MapClaims{"name": "Someone Else"})
	if roles["admin"] {
		t.Error("admin role assigned for non-matching claim")
	}
	if got := AssignRoles(nil, jwt.MapClaims{"name": "x"}); len(got) != 0 {
		t.Errorf("nil roleMapping should assign no roles, got %v", got)
	}
}

// staticFetch returns a JWKSFetcher serving the given public key under testKid.
func staticFetch(key *rsa.PublicKey) JWKSFetcher {
	return func(context.Context, string) (map[string]*rsa.PublicKey, error) {
		return map[string]*rsa.PublicKey{testKid: key}, nil
	}
}

func jwtConfig(authorityURL, clientID string) *config.Config {
	c := &config.Config{}
	c.APIAuthentication = []generated.AuthenticationElement{{
		Type:      generated.Jwt,
		Enabled:   true,
		JwtConfig: &generated.JwtConfig{AuthorityURL: authorityURL, ClientID: clientID},
	}}
	return c
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestJWTMiddleware(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	const iss = "https://issuer.example.com"
	const clientID = "git-proxy"
	cfg := jwtConfig(iss, clientID)
	opts := JWTMiddlewareOptions{Fetch: staticFetch(&key.PublicKey)}

	mw := JWTMiddleware(cfg, opts)
	token := signToken(t, key, jwt.MapClaims{
		"iss": iss, "aud": clientID, "azp": clientID, "sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	// Valid Bearer token -> 200, principal on context.
	var gotPrincipal bool
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPrincipal = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !gotPrincipal {
		t.Errorf("valid token: status=%d principal=%v, want 200/true", rr.Code, gotPrincipal)
	}

	// No token -> 401.
	rr = httptest.NewRecorder()
	mw(okHandler()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repo", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no token: status=%d, want 401", rr.Code)
	}

	// Garbage token -> 401.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/repo", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	mw(okHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("garbage token: status=%d, want 401", rr.Code)
	}

	// JWT not enabled -> pass-through (no token needed).
	rr = httptest.NewRecorder()
	JWTMiddleware(&config.Config{}, opts)(okHandler()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repo", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("jwt disabled: status=%d, want 200 (pass-through)", rr.Code)
	}

	// Existing session bypasses JWT.
	rr = httptest.NewRecorder()
	bypass := JWTMiddlewareOptions{Fetch: opts.Fetch, AlreadyAuthenticated: func(*http.Request) bool { return true }}
	JWTMiddleware(cfg, bypass)(okHandler()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repo", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("session bypass: status=%d, want 200", rr.Code)
	}
}

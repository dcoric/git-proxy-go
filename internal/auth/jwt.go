// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dcoric/git-proxy-go/internal/config/generated"
)

// JWKSFetcher resolves an OIDC authority URL to its signing keys, keyed by kid.
// It is injectable so tests can avoid real network calls (mirrors the Node
// getJwksInject parameter).
type JWKSFetcher func(ctx context.Context, authorityURL string) (map[string]*rsa.PublicKey, error)

// ValidateJWT verifies an RS256 access token against the OIDC authority's JWKS,
// checking the issuer and audience and the azp==clientID claim. It is the Go
// port of validateJwt in src/service/passport/jwtUtils.ts. On success it
// returns the verified claims.
func ValidateJWT(ctx context.Context, token, authorityURL, audience, clientID string, fetch JWKSFetcher) (jwt.MapClaims, error) {
	if fetch == nil {
		fetch = fetchJWKS
	}
	keys, err := fetch(ctx, authorityURL)
	if err != nil {
		return nil, err
	}

	keyfunc := func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("invalid JWT: missing key ID (kid)")
		}
		key, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("no matching key found in JWKS")
		}
		return key, nil
	}

	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, keyfunc,
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(authorityURL),
		jwt.WithAudience(audience),
	); err != nil {
		return nil, fmt.Errorf("JWT validation failed: %w", err)
	}

	// The authorized party, when present, must match the client ID.
	if azp, ok := claims["azp"].(string); ok && azp != "" && azp != clientID {
		return nil, fmt.Errorf("JWT client ID does not match")
	}
	return claims, nil
}

// AssignRoles sets role flags from the claims per the role mapping: a role is
// granted when the mapped claim equals the configured value. Mirrors
// assignRoles in jwtUtils.ts (e.g. {"admin": {"name": "John Doe"}}).
func AssignRoles(roleMapping *generated.RoleMapping, claims jwt.MapClaims) map[string]bool {
	roles := map[string]bool{}
	if roleMapping == nil {
		return roles
	}
	// generated.RoleMapping only models the "admin" role today.
	for claim, want := range roleMapping.Admin {
		if got, ok := claims[claim]; ok && fmt.Sprint(got) == fmt.Sprint(want) {
			roles["admin"] = true
		}
	}
	return roles
}

// openIDConfig / jwksDoc are the minimal discovery + JWKS shapes we parse.
type openIDConfig struct {
	JWKSURI string `json:"jwks_uri"`
}

type jwksDoc struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

var jwksHTTPClient = &http.Client{Timeout: 10 * time.Second}

// fetchJWKS performs OIDC discovery against authorityURL and returns the RSA
// signing keys by kid (mirrors getJwks in jwtUtils.ts).
func fetchJWKS(ctx context.Context, authorityURL string) (map[string]*rsa.PublicKey, error) {
	discoveryURL := strings.TrimRight(authorityURL, "/") + "/.well-known/openid-configuration"
	var cfg openIDConfig
	if err := getJSON(ctx, discoveryURL, &cfg); err != nil {
		return nil, fmt.Errorf("fetching openid-configuration: %w", err)
	}
	if cfg.JWKSURI == "" {
		return nil, fmt.Errorf("openid-configuration missing jwks_uri")
	}

	var doc jwksDoc
	if err := getJSON(ctx, cfg.JWKSURI, &doc); err != nil {
		return nil, fmt.Errorf("fetching JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, err := rsaPublicKeyFromJWK(k.N, k.E)
		if err != nil {
			return nil, fmt.Errorf("parsing JWK %q: %w", k.Kid, err)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no RSA keys in JWKS")
	}
	return keys, nil
}

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := jwksHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// rsaPublicKeyFromJWK builds an RSA public key from the base64url modulus (n)
// and exponent (e) of a JWK.
func rsaPublicKeyFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	// Left-pad the exponent to 8 bytes for a uint64.
	padded := make([]byte, 8)
	copy(padded[8-len(eBytes):], eBytes)
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(binary.BigEndian.Uint64(padded)),
	}, nil
}

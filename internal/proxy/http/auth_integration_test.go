// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

//go:build integration

// HTTP auth integration tests (P3): exercise the /api/auth routes end-to-end —
// session persistence and the csrf double-submit — against a real Postgres via
// dockertest. Run with: go test -tags=integration ./...
package proxyhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db/migrations"
	"github.com/dcoric/git-proxy-go/internal/db/postgres"
	"github.com/dcoric/git-proxy-go/internal/session"
)

var (
	testRouter http.Handler
	// testStore is exposed for sibling integration tests (e.g. the OIDC route
	// flow) that build their own router against the same Postgres container.
	testStore *postgres.Store
)

func TestMain(m *testing.M) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		log.Fatalf("dockertest: %v", err)
	}
	if err := pool.Client.Ping(); err != nil {
		log.Fatalf("docker not reachable: %v", err)
	}
	pool.MaxWait = 120 * time.Second
	ctx := context.Background()

	pg, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres", Tag: "16-alpine",
		Env: []string{"POSTGRES_USER=gitproxy", "POSTGRES_PASSWORD=secret", "POSTGRES_DB=gitproxy"},
	}, func(c *docker.HostConfig) {
		c.AutoRemove = true
		c.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		log.Fatalf("start postgres: %v", err)
	}

	dsn := fmt.Sprintf("postgres://gitproxy:secret@%s/gitproxy?sslmode=disable", pg.GetHostPort("5432/tcp"))
	if err := pool.Retry(func() error {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer p.Close()
		return p.Ping(ctx)
	}); err != nil {
		_ = pool.Purge(pg)
		log.Fatalf("postgres never ready: %v", err)
	}
	if err := migrations.Up(ctx, dsn); err != nil {
		_ = pool.Purge(pg)
		log.Fatalf("migrations: %v", err)
	}

	store, err := postgres.Connect(ctx, dsn)
	if err != nil {
		_ = pool.Purge(pg)
		log.Fatalf("connect store: %v", err)
	}
	testStore = store
	if err := auth.CreateDefaultAdmin(ctx, store); err != nil {
		_ = pool.Purge(pg)
		log.Fatalf("seed users: %v", err)
	}

	cfg := &config.Config{}
	cfg.Authentication = []generated.AuthenticationElement{{Type: generated.Local, Enabled: true}}
	registry := auth.BuildRegistry(cfg, store)
	sessions := session.New(stdlib.OpenDBFromPool(store.Pool()), time.Hour, false)
	testRouter = NewRouter(Options{
		Sessions:       sessions,
		Store:          store,
		Auth:           registry,
		CSRFProtection: true,
	})

	code := m.Run()

	store.Close()
	_ = pool.Purge(pg)
	os.Exit(code)
}

// newClient returns an HTTP client with a cookie jar (so session + csrf cookies
// persist across requests like a browser).
func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// csrfToken reads the csrf cookie the server set for base.
func csrfToken(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	u, _ := url.Parse(base)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == CSRFCookieName {
			return ck.Value
		}
	}
	t.Fatal("no csrf cookie issued")
	return ""
}

func TestAuthLoginProfileLogoutFlow(t *testing.T) {
	srv := httptest.NewServer(testRouter)
	defer srv.Close()
	c := newClient(t)

	// 1. A safe GET mints the csrf cookie (as the UI's first load would).
	if _, err := c.Get(srv.URL + "/api/v1/healthcheck"); err != nil {
		t.Fatalf("GET healthcheck: %v", err)
	}
	token := csrfToken(t, c, srv.URL)

	// 2. Login with the csrf token echoed in the header.
	resp := postJSON(t, c, srv.URL+"/api/auth/login", token, map[string]string{"username": "admin", "password": "admin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	var loginBody struct {
		Message string `json:"message"`
		User    struct {
			Username string `json:"username"`
			Admin    bool   `json:"admin"`
		} `json:"user"`
	}
	decode(t, resp, &loginBody)
	if loginBody.Message != "success" || loginBody.User.Username != "admin" || !loginBody.User.Admin {
		t.Errorf("login body = %+v", loginBody)
	}

	// 3. Profile uses the session cookie (GET, no csrf needed).
	pr, err := c.Get(srv.URL + "/api/auth/profile")
	if err != nil {
		t.Fatalf("GET profile: %v", err)
	}
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("profile status = %d, want 200", pr.StatusCode)
	}
	var profile struct {
		Username string `json:"username"`
	}
	decode(t, pr, &profile)
	if profile.Username != "admin" {
		t.Errorf("profile username = %q, want admin", profile.Username)
	}

	// 4. Logout, then profile is unauthorised again.
	lo := postJSON(t, c, srv.URL+"/api/auth/logout", token, nil)
	if lo.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", lo.StatusCode)
	}
	_ = lo.Body.Close()
	pr2, _ := c.Get(srv.URL + "/api/auth/profile")
	if pr2.StatusCode != http.StatusUnauthorized {
		t.Errorf("profile after logout = %d, want 401", pr2.StatusCode)
	}
	_ = pr2.Body.Close()
}

func TestAuthWrongPassword(t *testing.T) {
	srv := httptest.NewServer(testRouter)
	defer srv.Close()
	c := newClient(t)
	_, _ = c.Get(srv.URL + "/api/v1/healthcheck")
	token := csrfToken(t, c, srv.URL)

	resp := postJSON(t, c, srv.URL+"/api/auth/login", token, map[string]string{"username": "admin", "password": "nope"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-password login = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAuthLoginRejectedWithoutCSRF(t *testing.T) {
	srv := httptest.NewServer(testRouter)
	defer srv.Close()
	c := newClient(t)
	_, _ = c.Get(srv.URL + "/api/v1/healthcheck") // sets cookie, but we omit the header

	resp := postJSON(t, c, srv.URL+"/api/auth/login", "", map[string]string{"username": "admin", "password": "admin"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("login without csrf header = %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAuthConfig(t *testing.T) {
	srv := httptest.NewServer(testRouter)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/auth/config")
	if err != nil {
		t.Fatalf("GET config: %v", err)
	}
	var body struct {
		UsernamePasswordMethod string   `json:"usernamePasswordMethod"`
		OtherMethods           []string `json:"otherMethods"`
	}
	decode(t, resp, &body)
	if body.UsernamePasswordMethod != "local" {
		t.Errorf("usernamePasswordMethod = %q, want local", body.UsernamePasswordMethod)
	}
}

func postJSON(t *testing.T, c *http.Client, urlStr, csrf string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, urlStr, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set(CSRFHeaderName, csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", urlStr, err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

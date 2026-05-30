// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

//go:build integration

// API route-group integration tests (P4-4): exercise /api, /api/v1/{push,repo,
// user,config} end-to-end with session auth + CSRF against a real Postgres
// (dockertest, shared with auth_integration_test.go).
package proxyhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
	"github.com/dcoric/git-proxy-go/internal/session"
)

// apiRouter builds a router with the API route groups wired (Config with one
// attestation question), over the shared store.
func apiRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{}
	cfg.Authentication = []generated.AuthenticationElement{{Type: generated.Local, Enabled: true}}
	cfg.AttestationConfig = &generated.AttestationConfig{Questions: []generated.Question{{Label: "Q1"}}}
	cfg.URLShortener = strPtr("https://short.example")
	sessions := session.New(stdlib.OpenDBFromPool(testStore.Pool()), time.Hour, false)
	return NewRouter(Options{
		Sessions:       sessions,
		Store:          testStore,
		Auth:           auth.BuildRegistry(cfg, testStore),
		CSRFProtection: true,
		Config:         cfg,
		UIPort:         "8080",
		GitProxyPort:   "8000",
	})
}

// loginAs logs the client in and returns the csrf token to echo on mutations.
func loginAs(t *testing.T, c *http.Client, base, username, password string) string {
	t.Helper()
	if _, err := c.Get(base + "/api/v1/healthcheck"); err != nil {
		t.Fatalf("GET healthcheck: %v", err)
	}
	token := csrfToken(t, c, base)
	resp := postJSON(t, c, base+"/api/auth/login", token, map[string]string{"username": username, "password": password})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s = %d, want 200", username, resp.StatusCode)
	}
	_ = resp.Body.Close()
	return token
}

func TestAPIHome(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api")
	if err != nil {
		t.Fatalf("GET /api: %v", err)
	}
	var body map[string]string
	decode(t, resp, &body)
	if body["push"] != "/api/v1/push" || body["auth"] != "/api/auth" {
		t.Errorf("home = %v", body)
	}
}

func TestAPIUsers(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/user/admin")
	if err != nil {
		t.Fatalf("GET user: %v", err)
	}
	var u publicUser
	decode(t, resp, &u)
	if u.Username != "admin" || !u.Admin {
		t.Errorf("user = %+v, want admin/true", u)
	}
	if u.GitAccount == "" {
		t.Error("expected gitAccount to be populated")
	}

	miss, _ := http.Get(srv.URL + "/api/v1/user/nobody")
	if miss.StatusCode != http.StatusNotFound {
		t.Errorf("missing user = %d, want 404", miss.StatusCode)
	}
	_ = miss.Body.Close()
}

func TestAPIConfig(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/config/attestation")
	if err != nil {
		t.Fatalf("GET attestation: %v", err)
	}
	var att struct {
		Questions []struct {
			Label string `json:"label"`
		} `json:"questions"`
	}
	decode(t, resp, &att)
	if len(att.Questions) != 1 || att.Questions[0].Label != "Q1" {
		t.Errorf("attestation = %+v", att)
	}

	sh, _ := http.Get(srv.URL + "/api/v1/config/urlShortener")
	buf := make([]byte, 64)
	n, _ := sh.Body.Read(buf)
	_ = sh.Body.Close()
	if got := string(buf[:n]); got != "https://short.example" {
		t.Errorf("urlShortener = %q", got)
	}
}

func TestAPIRepoCreateRequiresAdmin(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	c := newClient(t)
	token := loginAs(t, c, srv.URL, "user", "user") // non-admin

	resp := postJSON(t, c, srv.URL+"/api/v1/repo", token, map[string]string{"url": "https://github.com/x/y.git"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("non-admin create = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAPIRepoCreateAndList(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	c := newClient(t)
	token := loginAs(t, c, srv.URL, "admin", "admin")

	url := "https://github.com/p4test/create.git"
	resp := postJSON(t, c, srv.URL+"/api/v1/repo", token, map[string]any{
		"url": url, "name": "create.git", "project": "p4test",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create repo = %d, want 200", resp.StatusCode)
	}
	var created map[string]any
	decode(t, resp, &created)
	if created["message"] != "created" || created["proxyURL"] == "" {
		t.Errorf("create body = %v", created)
	}

	// Missing url -> 400.
	bad := postJSON(t, c, srv.URL+"/api/v1/repo", token, map[string]any{"name": "x"})
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("create without url = %d, want 400", bad.StatusCode)
	}
	_ = bad.Body.Close()

	// Duplicate -> 409.
	dup := postJSON(t, c, srv.URL+"/api/v1/repo", token, map[string]any{"url": url})
	if dup.StatusCode != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", dup.StatusCode)
	}
	_ = dup.Body.Close()

	// List includes the proxyURL augmentation.
	list, _ := c.Get(srv.URL + "/api/v1/repo")
	var repos []map[string]any
	decode(t, list, &repos)
	if len(repos) == 0 {
		t.Fatal("repo list empty")
	}
	if _, ok := repos[0]["proxyURL"]; !ok {
		t.Error("repo list entries missing proxyURL")
	}
}

func TestAPIPushListAndGet(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	ctx := context.Background()

	id := "p4-push-list-1"
	if err := testStore.WriteAudit(ctx, &db.Push{ID: id, Type: "push", URL: "https://github.com/p4test/push.git", UserEmail: "user@place.com"}); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	c := newClient(t)
	loginAs(t, c, srv.URL, "admin", "admin")

	got, _ := c.Get(srv.URL + "/api/v1/push/" + id)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get push = %d, want 200", got.StatusCode)
	}
	var push struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	decode(t, got, &push)
	if push.ID != id || push.Type != "push" {
		t.Errorf("push = %+v", push)
	}

	miss, _ := c.Get(srv.URL + "/api/v1/push/does-not-exist")
	if miss.StatusCode != http.StatusNotFound {
		t.Errorf("missing push = %d, want 404", miss.StatusCode)
	}
	_ = miss.Body.Close()
}

func TestAPIPushAuthorise(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	ctx := context.Background()

	// Repo with admin on the authorise list, and a push committed by 'user'.
	url := "https://github.com/p4test/authorise.git"
	repo, err := testStore.CreateRepo(ctx, &db.Repo{Name: "authorise.git", Project: "p4test", URL: url})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if err := testStore.AddUserCanAuthorise(ctx, repo.ID, "admin"); err != nil {
		t.Fatalf("add authorise: %v", err)
	}
	id := "p4-push-authorise-1"
	if err := testStore.WriteAudit(ctx, &db.Push{ID: id, Type: "push", URL: url, UserEmail: "user@place.com"}); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	c := newClient(t)
	token := loginAs(t, c, srv.URL, "admin", "admin")

	resp := postJSON(t, c, srv.URL+"/api/v1/push/"+id+"/authorise", token, map[string]any{
		"params": map[string]any{
			"attestation": []map[string]any{{"label": "Q1", "checked": true}},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorise = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	decode(t, resp, &body)
	if body["message"] != "authorised "+id {
		t.Errorf("authorise message = %q", body["message"])
	}

	// Incomplete attestation -> 400.
	bad := postJSON(t, c, srv.URL+"/api/v1/push/"+id+"/authorise", token, map[string]any{
		"params": map[string]any{"attestation": []map[string]any{}},
	})
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("incomplete attestation = %d, want 400", bad.StatusCode)
	}
	_ = bad.Body.Close()
}

func TestAPIPushRejectReasonRequired(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	ctx := context.Background()

	id := "p4-push-reject-1"
	if err := testStore.WriteAudit(ctx, &db.Push{ID: id, Type: "push", URL: "https://github.com/p4test/reject.git", UserEmail: "user@place.com"}); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	c := newClient(t)
	token := loginAs(t, c, srv.URL, "admin", "admin")

	resp := postJSON(t, c, srv.URL+"/api/v1/push/"+id+"/reject", token, map[string]any{"reason": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("reject without reason = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAPIPushUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(apiRouter(t))
	defer srv.Close()
	c := newClient(t)
	// Mint a csrf token but do not log in.
	_, _ = c.Get(srv.URL + "/api/v1/healthcheck")
	token := csrfToken(t, c, srv.URL)

	resp := postJSON(t, c, srv.URL+"/api/v1/push/whatever/cancel", token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("cancel while logged out = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

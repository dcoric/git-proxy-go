// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
)

func TestCheckIfWaitingAuth(t *testing.T) {
	t.Run("authorised push is adopted and allowed", func(t *testing.T) {
		existing := &db.Push{ID: "p1", URL: "https://github.com/x/y.git", Authorised: true}
		fs := &fakeStore{pushByID: map[string]*db.Push{"p1": existing}}
		a := &Action{Push: db.Push{ID: "p1"}}
		a, _ = (&Engine{store: fs}).checkIfWaitingAuth(context.Background(), nil, a)
		if !a.AllowPush {
			t.Error("an authorised push should be allowed")
		}
		if a.URL != existing.URL {
			t.Errorf("action should adopt the stored push; url = %q", a.URL)
		}
	})

	t.Run("not authorised -> no allow", func(t *testing.T) {
		fs := &fakeStore{pushByID: map[string]*db.Push{"p1": {ID: "p1", Authorised: false}}}
		a := &Action{Push: db.Push{ID: "p1"}}
		a, _ = (&Engine{store: fs}).checkIfWaitingAuth(context.Background(), nil, a)
		if a.AllowPush {
			t.Error("an unauthorised push must not be allowed")
		}
	})

	t.Run("not found -> no allow, continues", func(t *testing.T) {
		a := &Action{Push: db.Push{ID: "missing"}}
		a, _ = (&Engine{store: &fakeStore{}}).checkIfWaitingAuth(context.Background(), nil, a)
		if a.AllowPush || !a.Continue() {
			t.Errorf("missing push: allow=%v continue=%v", a.AllowPush, a.Continue())
		}
	})
}

func makeHook(t *testing.T, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre-receive.sh")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPreReceive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hooks are not supported on Windows")
	}
	repoParent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoParent, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkAction := func() *Action {
		return &Action{Push: db.Push{ProxyGitPath: repoParent, RepoName: "repo", CommitFrom: "a", CommitTo: "b", Branch: "refs/heads/main"}}
	}

	tests := []struct {
		name                       string
		exit                       int
		wantApproved, wantRejected bool
		wantError                  bool
	}{
		{"approve", 0, true, false, false},
		{"reject", 1, false, true, false},
		{"manual", 2, false, false, false},
		{"error", 3, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{preReceiveHook: makeHook(t, tc.exit)}
			a, _ := e.preReceive(context.Background(), nil, mkAction())
			if a.AutoApproved != tc.wantApproved || a.AutoRejected != tc.wantRejected || a.Error != tc.wantError {
				t.Errorf("exit %d: approved=%v rejected=%v error=%v", tc.exit, a.AutoApproved, a.AutoRejected, a.Error)
			}
		})
	}

	t.Run("missing hook is skipped", func(t *testing.T) {
		e := &Engine{preReceiveHook: filepath.Join(t.TempDir(), "does-not-exist.sh")}
		a, _ := e.preReceive(context.Background(), nil, mkAction())
		if a.Error || a.AutoApproved || a.AutoRejected {
			t.Error("a missing hook should be skipped without effect")
		}
		if len(a.Steps) != 1 {
			t.Errorf("expected a step to be recorded; got %d", len(a.Steps))
		}
	})
}

func TestGitleaks(t *testing.T) {
	t.Run("disabled is skipped", func(t *testing.T) {
		a := &Action{Push: db.Push{ProxyGitPath: "/x", RepoName: "repo"}}
		a, _ = (&Engine{}).gitleaks(context.Background(), nil, a)
		if a.Error {
			t.Errorf("disabled gitleaks should skip; got %v", a.ErrorMessage)
		}
	})

	t.Run("enabled but repo missing -> error", func(t *testing.T) {
		enabled := true
		cfg := &config.Config{}
		cfg.API = &generated.API{Gitleaks: &generated.Gitleaks{Enabled: &enabled}}
		a := &Action{Push: db.Push{ProxyGitPath: filepath.Join(t.TempDir(), "nope"), RepoName: "repo", CommitFrom: "a", CommitTo: "b"}}
		a, _ = (&Engine{cfg: cfg}).gitleaks(context.Background(), nil, a)
		if !a.Error {
			t.Error("enabled gitleaks against a missing repo should error")
		}
	})

	t.Run("unreadable config path -> error", func(t *testing.T) {
		enabled := true
		bad := "/nonexistent/gitleaks.toml"
		cfg := &config.Config{}
		cfg.API = &generated.API{Gitleaks: &generated.Gitleaks{Enabled: &enabled, ConfigPath: &bad}}
		a := &Action{Push: db.Push{ProxyGitPath: "/x", RepoName: "repo"}}
		a, _ = (&Engine{cfg: cfg}).gitleaks(context.Background(), nil, a)
		if !a.Error || !strings.Contains(*a.ErrorMessage, "config path") {
			t.Errorf("expected a config-path error; got %v", a.ErrorMessage)
		}
	})
}

func TestBlockForAuth(t *testing.T) {
	a := &Action{Push: db.Push{ID: "push-42"}}
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	a, _ = (&Engine{}).blockForAuth(context.Background(), r, a)

	if !a.Blocked {
		t.Fatal("blockForAuth should block the push")
	}
	if a.BlockedMessage == nil || !strings.Contains(*a.BlockedMessage, "/dashboard/push/push-42") {
		t.Errorf("block message should contain the dashboard link; got %v", a.BlockedMessage)
	}
}

func TestServiceUIURL(t *testing.T) {
	// Port swap: request on the proxy port -> link on the UI port.
	e := &Engine{uiPort: "8080", proxyPort: "8000"}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8000/x", nil)
	r.Host = "localhost:8000"
	if got := e.serviceUIURL(r); got != "http://localhost:8080" {
		t.Errorf("serviceUIURL = %q, want http://localhost:8080", got)
	}

	// domains.service override wins.
	svc := "https://proxy.example.com"
	cfg := &config.Config{}
	cfg.Domains = &generated.Domains{Service: &svc}
	e = &Engine{cfg: cfg}
	if got := e.serviceUIURL(r); got != svc {
		t.Errorf("serviceUIURL = %q, want %q", got, svc)
	}
}

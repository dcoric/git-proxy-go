// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// stubClones returns an Engine whose clone implementations are recorded rather
// than executed, so pullRemote's transport dispatch can be tested without a
// network or a git binary.
func stubClones(t *testing.T) (*Engine, *cloneCalls) {
	t.Helper()
	calls := &cloneCalls{}
	e := &Engine{
		remoteDir: t.TempDir(),
		cloneHTTPS: func(_ context.Context, url, _, user, pass string, _ int) error {
			calls.https = true
			calls.url, calls.user, calls.pass = url, user, pass
			return nil
		},
		cloneSSH: func(_ context.Context, url, _, user string, _ func() ([]ssh.Signer, error), _ ssh.HostKeyCallback, _ int) error {
			calls.ssh = true
			calls.url, calls.user = url, user
			return nil
		},
	}
	return e, calls
}

type cloneCalls struct {
	https, ssh      bool
	url, user, pass string
}

func pushAction() *Action {
	return NewAction("id1", "push", "POST", 0, "https://github.com/org/repo.git")
}

func TestPullRemoteClonesOverSSHWithForwardedAgent(t *testing.T) {
	e, calls := stubClones(t)
	a := pushAction()
	ctx := WithSSHCloneAuth(context.Background(), &SSHCloneAuth{
		User:    "git",
		Signers: func() ([]ssh.Signer, error) { return nil, nil },
	})

	// The request carries no basic auth — SSH must be chosen regardless.
	if _, err := e.pullRemote(ctx, httptest.NewRequest(http.MethodPost, "/", nil), a); err != nil {
		t.Fatalf("pullRemote: %v", err)
	}
	if !calls.ssh || calls.https {
		t.Fatalf("expected SSH clone, got ssh=%v https=%v", calls.ssh, calls.https)
	}
	if calls.url != "git@github.com:org/repo.git" {
		t.Errorf("ssh clone url = %q, want git@github.com:org/repo.git", calls.url)
	}
	if calls.user != "git" {
		t.Errorf("ssh clone user = %q, want git", calls.user)
	}
	if !a.cleanupClone {
		t.Error("cleanupClone not set after a successful SSH clone")
	}
	if a.Error {
		t.Errorf("unexpected action error: %v", a.ErrorMessage)
	}
}

func TestPullRemoteClonesOverHTTPSWithBasicAuth(t *testing.T) {
	e, calls := stubClones(t)
	a := pushAction()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.SetBasicAuth("alice", "secret")

	if _, err := e.pullRemote(context.Background(), r, a); err != nil {
		t.Fatalf("pullRemote: %v", err)
	}
	if !calls.https || calls.ssh {
		t.Fatalf("expected HTTPS clone, got ssh=%v https=%v", calls.ssh, calls.https)
	}
	if calls.url != a.URL || calls.user != "alice" || calls.pass != "secret" {
		t.Errorf("https clone = url %q user %q pass %q", calls.url, calls.user, calls.pass)
	}
	if !a.cleanupClone {
		t.Error("cleanupClone not set after a successful HTTPS clone")
	}
}

func TestPullRemoteHTTPSRequiresBasicAuth(t *testing.T) {
	e, calls := stubClones(t)
	a := pushAction()

	if _, err := e.pullRemote(context.Background(), httptest.NewRequest(http.MethodPost, "/", nil), a); err != nil {
		t.Fatalf("pullRemote: %v", err)
	}
	if calls.https || calls.ssh {
		t.Error("clone attempted without credentials")
	}
	if !a.Error || a.ErrorMessage == nil || !strings.Contains(*a.ErrorMessage, "authorization header is required") {
		t.Errorf("expected authorization error, got error=%v msg=%v", a.Error, a.ErrorMessage)
	}
}

func TestConvertToSSHURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/org/repo.git", "git@github.com:org/repo.git"},
		{"https://gitlab.com/group/sub/repo.git", "git@gitlab.com:group/sub/repo.git"},
		{"https://git.example.com:8443/o/r.git", "git@git.example.com:o/r.git"},
	}
	for _, tc := range tests {
		if got := convertToSSHURL(tc.in); got != tc.want {
			t.Errorf("convertToSSHURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

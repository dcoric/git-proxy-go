// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/dcoric/git-proxy-go/internal/db"
)

func TestParseGitCommand(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		wantOp   string
		wantRepo string
		wantErr  bool
	}{
		{"receive-pack quoted", "git-receive-pack 'github.com/org/repo.git'", "receive-pack", "github.com/org/repo.git", false},
		{"upload-pack quoted", "git-upload-pack 'github.com/org/repo.git'", "upload-pack", "github.com/org/repo.git", false},
		{"leading slash stripped", "git-upload-pack '/github.com/org/repo.git'", "upload-pack", "github.com/org/repo.git", false},
		{"unquoted", "git-upload-pack github.com/org/repo.git", "upload-pack", "github.com/org/repo.git", false},
		{"not a git command", "ls -la", "", "", true},
		{"path traversal", "git-upload-pack 'github.com/../etc/passwd.git'", "", "", true},
		{"double slash", "git-upload-pack 'github.com//repo.git'", "", "", true},
		{"missing .git", "git-upload-pack 'github.com/org/repo'", "", "", true},
		{"too few segments", "git-upload-pack 'repo.git'", "", "", true},
		{"bad hostname", "git-upload-pack 'not_a_host/org/repo.git'", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op, repo, err := parseGitCommand(tc.command)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tc.command)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitCommand(%q): %v", tc.command, err)
			}
			if op != tc.wantOp || repo != tc.wantRepo {
				t.Errorf("parseGitCommand(%q) = %q, %q; want %q, %q", tc.command, op, repo, tc.wantOp, tc.wantRepo)
			}
		})
	}
}

// genSigner returns a fresh Ed25519 ssh.Signer.
func genSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// fakeFinder maps authorized_keys strings to users.
type fakeFinder struct{ users map[string]*db.User }

func (f fakeFinder) FindUserBySSHKey(_ context.Context, key string) (*db.User, error) {
	return f.users[key], nil
}

type recordedGit struct {
	op, repo, user string
}

type fakeGitHandler struct{ got chan recordedGit }

func (h *fakeGitHandler) Handle(_ context.Context, req GitRequest) (uint32, error) {
	h.got <- recordedGit{op: req.Op, repo: req.RepoPath, user: req.Username}
	return 0, nil
}

// startTestServer binds the server on a random local port and returns its addr.
func startTestServer(t *testing.T, finder UserKeyFinder, handler GitHandler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ln.Addr().String(), genSigner(t), finder, handler)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.serve(ctx, ln) }()
	return ln.Addr().String()
}

func TestServerAuthAndExec(t *testing.T) {
	clientSigner := genSigner(t)
	keyString := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(clientSigner.PublicKey())))
	finder := fakeFinder{users: map[string]*db.User{keyString: {Username: "alice"}}}
	handler := &fakeGitHandler{got: make(chan recordedGit, 1)}
	addr := startTestServer(t, finder, handler)

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Run("git-receive-pack 'github.com/org/repo.git'"); err != nil {
		t.Fatalf("run: %v", err)
	}

	select {
	case got := <-handler.got:
		if got.op != "receive-pack" || got.repo != "github.com/org/repo.git" || got.user != "alice" {
			t.Errorf("handler got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("git handler was not invoked")
	}
}

func TestServerRejectsUnknownKey(t *testing.T) {
	finder := fakeFinder{users: map[string]*db.User{}} // no keys registered
	addr := startTestServer(t, finder, &fakeGitHandler{got: make(chan recordedGit, 1)})

	_, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(genSigner(t))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected authentication to fail for an unregistered key")
	}
}

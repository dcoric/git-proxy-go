// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// gitHelper runs the git binary and fails the test on error, returning stdout.
type gitHelper struct {
	t   *testing.T
	bin string
}

func newGitHelper(t *testing.T) gitHelper {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not found; skipping git-engine test")
	}
	return gitHelper{t: t, bin: bin}
}

func (g gitHelper) run(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command(g.bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Keep commits deterministic and independent of the host git config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com")
	out, err := cmd.Output()
	if err != nil {
		g.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// seedRepo creates a non-bare repo at dir with one commit on the default branch.
func (g gitHelper) seedRepo(dir string) {
	g.t.Helper()
	g.run("", "init", "-q", "-b", "main", dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "add", "a.txt")
	g.run(dir, "commit", "-q", "-m", "first commit")
}

func authedRequest(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.SetBasicAuth("user", "pass") // ignored for a local clone, but pullRemote requires it
	return r
}

func TestPullRemote(t *testing.T) {
	g := newGitHelper(t)
	src := filepath.Join(t.TempDir(), "src")
	g.seedRepo(src)

	e := &Engine{remoteDir: t.TempDir()}
	a := &Action{Push: db.Push{ID: "push-1", URL: src, RepoName: "myrepo"}}
	a, _ = e.pullRemote(context.Background(), authedRequest(t), a)

	if a.Error {
		t.Fatalf("pullRemote errored: %v", a.ErrorMessage)
	}
	if !a.cleanupClone {
		t.Error("cleanupClone should be set after a successful clone")
	}
	if a.ProxyGitPath != filepath.Join(e.remoteDir, "push-1") {
		t.Errorf("proxyGitPath = %q", a.ProxyGitPath)
	}
	if _, err := os.Stat(filepath.Join(a.ProxyGitPath, "myrepo", ".git")); err != nil {
		t.Errorf("clone not found: %v", err)
	}
}

func TestPullRemoteFolderExists(t *testing.T) {
	g := newGitHelper(t)
	src := filepath.Join(t.TempDir(), "src")
	g.seedRepo(src)

	remoteDir := t.TempDir()
	existing := filepath.Join(remoteDir, "push-2")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}

	e := &Engine{remoteDir: remoteDir}
	a := &Action{Push: db.Push{ID: "push-2", URL: src, RepoName: "myrepo"}}
	a, _ = e.pullRemote(context.Background(), authedRequest(t), a)

	if !a.Error || a.ErrorMessage == nil || !strings.Contains(*a.ErrorMessage, "already exists") {
		t.Errorf("expected an 'already exists' error, got %v", a.ErrorMessage)
	}
	if a.cleanupClone {
		t.Error("cleanupClone must not be set when the folder already existed")
	}
	if _, err := os.Stat(existing); err != nil {
		t.Error("the pre-existing folder must not be deleted")
	}
}

func TestPullRemoteNoAuth(t *testing.T) {
	g := newGitHelper(t)
	src := filepath.Join(t.TempDir(), "src")
	g.seedRepo(src)

	e := &Engine{remoteDir: t.TempDir()}
	a := &Action{Push: db.Push{ID: "push-3", URL: src, RepoName: "myrepo"}}
	r := httptest.NewRequest(http.MethodPost, "/x", nil) // no auth header
	a, _ = e.pullRemote(context.Background(), r, a)

	if !a.Error || a.ErrorMessage == nil || !strings.Contains(*a.ErrorMessage, "authorization header is required") {
		t.Errorf("expected an auth-required error, got %v", a.ErrorMessage)
	}
	if _, err := os.Stat(a.ProxyGitPath); !os.IsNotExist(err) {
		t.Error("the partial checkout folder should have been cleaned up")
	}
}

func TestWritePack(t *testing.T) {
	g := newGitHelper(t)
	dir := t.TempDir()

	// Bare remote seeded with one commit, via a scratch working clone.
	remote := filepath.Join(dir, "remote.git")
	g.run("", "init", "-q", "--bare", "-b", "main", remote)
	work := filepath.Join(dir, "work")
	g.run("", "clone", "-q", remote, work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run(work, "add", "a.txt")
	g.run(work, "commit", "-q", "-m", "first commit")
	g.run(work, "push", "-q", "origin", "main")
	commit1 := strings.TrimSpace(g.run(work, "rev-parse", "HEAD"))

	// The clone writePack operates on (as pullRemote would have produced).
	proxyGitPath := filepath.Join(dir, "proxy")
	g.run("", "clone", "-q", remote, filepath.Join(proxyGitPath, "repo"))

	// A second commit, and the receive-pack body that pushes commit1->commit2.
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run(work, "commit", "-q", "-am", "second commit")
	commit2 := strings.TrimSpace(g.run(work, "rev-parse", "HEAD"))

	revs := g.run(work, "rev-list", "--objects", commit2)
	packCmd := exec.Command(g.bin, "pack-objects", "--stdout")
	packCmd.Dir = work
	packCmd.Stdin = strings.NewReader(revs)
	pack, err := packCmd.Output()
	if err != nil {
		t.Fatalf("pack-objects: %v", err)
	}

	var body bytes.Buffer
	body.WriteString(pktLine(commit1 + " " + commit2 + " refs/heads/main\x00report-status"))
	body.WriteString("0000")
	body.Write(pack)

	e := &Engine{}
	a := &Action{Push: db.Push{ProxyGitPath: proxyGitPath, RepoName: "repo"}}
	a, _ = e.writePack(rawCtx(body.Bytes()), nil, a)

	if a.Error {
		t.Fatalf("writePack errored: %v", a.ErrorMessage)
	}
	if len(a.NewIdxFiles) == 0 {
		t.Error("expected writePack to record at least one new .idx file")
	}
}

func TestWritePackMissingPath(t *testing.T) {
	a := &Action{Push: db.Push{ProxyGitPath: "", RepoName: ""}}
	a, _ = (&Engine{}).writePack(rawCtx(nil), nil, a)
	if !a.Error {
		t.Error("expected an error when proxyGitPath/repoName are unset")
	}
}

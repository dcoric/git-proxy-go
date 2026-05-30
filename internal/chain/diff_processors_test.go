// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
)

func TestGetDiff(t *testing.T) {
	g := newGitHelper(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	g.run("", "init", "-q", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run(repo, "add", "a.txt")
	g.run(repo, "commit", "-q", "-m", "first")
	commit1 := strings.TrimSpace(g.run(repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run(repo, "commit", "-q", "-am", "second")
	commit2 := strings.TrimSpace(g.run(repo, "rev-parse", "HEAD"))

	a := &Action{Push: db.Push{
		ProxyGitPath: dir, RepoName: "repo",
		CommitFrom: commit1, CommitTo: commit2,
		CommitData: []db.CommitData{{Parent: commit1}},
	}}
	a, _ = (&Engine{}).getDiff(context.Background(), nil, a)

	if a.Error {
		t.Fatalf("getDiff errored: %v", a.ErrorMessage)
	}
	diff := diffStepContent(a)
	if !strings.Contains(diff, "+world") {
		t.Errorf("diff missing the added line; got:\n%s", diff)
	}
}

func TestGetDiffNoCommitData(t *testing.T) {
	a := &Action{Push: db.Push{ProxyGitPath: "/x", RepoName: "repo"}}
	a, _ = (&Engine{}).getDiff(context.Background(), nil, a)
	if !a.Error {
		t.Error("getDiff should error when there is no commit data")
	}
}

func diffCfg(literals []string, patterns []string, providers map[string]string) *generated.CommitConfig {
	block := &generated.DiffBlock{Literals: literals, Providers: providers}
	for _, p := range patterns {
		block.Patterns = append(block.Patterns, p)
	}
	return &generated.CommitConfig{Diff: &generated.Diff{Block: block}}
}

// sampleDiff is a unified diff adding two lines to a.txt.
const sampleDiff = `diff --git a/a.txt b/a.txt
index 1234567..89abcde 100644
--- a/a.txt
+++ b/a.txt
@@ -1,1 +1,3 @@
 hello
+my password is hunter2
+all good here
`

func TestScanDiff(t *testing.T) {
	withDiff := func(diff string) *Action {
		a := NewAction("id", "push", "POST", 0, "https://github.com/org/repo.git")
		s := NewStep("diff")
		s.SetContent(diff)
		a.AddStep(s)
		return a
	}

	t.Run("blocked literal", func(t *testing.T) {
		e := &Engine{cfg: commitCfg(t, diffCfg([]string{"password"}, nil, nil))}
		a, _ := e.scanDiff(context.Background(), nil, withDiff(sampleDiff))
		if !a.Error {
			t.Fatal("expected the diff to be blocked")
		}
		if !strings.Contains(*a.ErrorMessage, "password") {
			t.Errorf("error message should name the literal; got %q", *a.ErrorMessage)
		}
	})

	t.Run("blocked pattern", func(t *testing.T) {
		e := &Engine{cfg: commitCfg(t, diffCfg(nil, []string{`hunter\d+`}, nil))}
		a, _ := e.scanDiff(context.Background(), nil, withDiff(sampleDiff))
		if !a.Error {
			t.Error("expected the pattern to block the diff")
		}
	})

	t.Run("clean diff passes", func(t *testing.T) {
		e := &Engine{cfg: commitCfg(t, diffCfg([]string{"password"}, nil, nil))}
		clean := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,2 @@\n hello\n+all good here\n"
		a, _ := e.scanDiff(context.Background(), nil, withDiff(clean))
		if a.Error {
			t.Errorf("clean diff should pass; got %v", a.ErrorMessage)
		}
	})

	t.Run("provider skipped for private org", func(t *testing.T) {
		cfg := commitCfg(t, diffCfg(nil, nil, map[string]string{"GitHub Token": `password`}))
		cfg.PrivateOrganizations = []interface{}{"org"}
		e := &Engine{cfg: cfg}
		// action.Project is "org" (from the URL), which is private -> provider skipped.
		a, _ := e.scanDiff(context.Background(), nil, withDiff(sampleDiff))
		if a.Error {
			t.Errorf("provider rule should be skipped for a private org; got %v", a.ErrorMessage)
		}
	})

	t.Run("deleted lines are ignored", func(t *testing.T) {
		e := &Engine{cfg: commitCfg(t, diffCfg([]string{"password"}, nil, nil))}
		delDiff := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,1 @@\n hello\n-my password is hunter2\n"
		a, _ := e.scanDiff(context.Background(), nil, withDiff(delDiff))
		if a.Error {
			t.Error("a removed secret should not block (only added lines are scanned)")
		}
	})
}

func TestParseAddedChanges(t *testing.T) {
	changes := parseAddedChanges(sampleDiff)
	if len(changes) != 2 {
		t.Fatalf("added changes = %d, want 2", len(changes))
	}
	if changes[0].file != "a.txt" || changes[0].line != 2 {
		t.Errorf("first change = %+v, want a.txt line 2", changes[0])
	}
	if changes[1].line != 3 {
		t.Errorf("second change line = %d, want 3", changes[1].line)
	}
}

func TestCheckHiddenCommits(t *testing.T) {
	g := newGitHelper(t)
	dir := t.TempDir()

	remote := filepath.Join(dir, "remote.git")
	g.run("", "init", "-q", "--bare", "-b", "main", remote)
	work := filepath.Join(dir, "work")
	g.run("", "clone", "-q", remote, work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run(work, "add", "a.txt")
	g.run(work, "commit", "-q", "-m", "first")
	g.run(work, "push", "-q", "origin", "main")
	commit1 := strings.TrimSpace(g.run(work, "rev-parse", "HEAD"))

	proxyGitPath := filepath.Join(dir, "proxy")
	g.run("", "clone", "-q", remote, filepath.Join(proxyGitPath, "repo"))

	// Second commit + a thin pack of just its new objects (as a real push sends).
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run(work, "commit", "-q", "-am", "second")
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

	// A new-branch push (commitFrom empty): rev-list commit2 covers the whole
	// history, so every commit in the pushed pack is referenced -> clean.
	e := &Engine{}
	a := &Action{Push: db.Push{ProxyGitPath: proxyGitPath, RepoName: "repo", CommitFrom: emptyCommitHash, CommitTo: commit2}}
	a, _ = e.writePack(rawCtx(body.Bytes()), nil, a)
	if a.Error {
		t.Fatalf("writePack errored: %v", a.ErrorMessage)
	}

	a, _ = e.checkHiddenCommits(context.Background(), nil, a)
	if a.Error {
		t.Fatalf("checkHiddenCommits flagged a clean push: %v", a.ErrorMessage)
	}
}

func TestCheckHiddenCommitsMissingRange(t *testing.T) {
	a := &Action{Push: db.Push{ProxyGitPath: "/x", RepoName: "repo"}}
	a, _ = (&Engine{}).checkHiddenCommits(context.Background(), nil, a)
	if !a.Error {
		t.Error("expected an error when commitFrom/commitTo are unset")
	}
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestGitEngineSpike exercises the plan §3.9 hybrid git engine end-to-end on a
// real (hermetic, local) repo, proving the three mechanisms interoperate:
//
//  1. go-git performs the clone (the part we keep go-git for).
//  2. The git binary performs receive-pack/unpack of a pushed pack (the part we
//     keep the binary for — `writePack`, task P4-12).
//  3. The git binary produces the diff (task P4-16).
//
// It informs the engine decision recorded in docs/decisions/0001-git-engine.md
// (#8 -> #9). Skipped when the git binary is unavailable.
func TestGitEngineSpike(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not found; skipping engine spike")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "source")

	// --- fixture: a source repo with two commits ---
	gitStr(t, gitBin, "", "init", "-q", src)
	gitStr(t, gitBin, src, "config", "user.email", "spike@example.com")
	gitStr(t, gitBin, src, "config", "user.name", "Spike")
	writeFile(t, filepath.Join(src, "a.txt"), "hello\n")
	gitStr(t, gitBin, src, "add", "a.txt")
	gitStr(t, gitBin, src, "commit", "-q", "-m", "first")
	writeFile(t, filepath.Join(src, "a.txt"), "hello\nworld\n")
	gitStr(t, gitBin, src, "commit", "-q", "-am", "second")
	headSHA := strings.TrimSpace(gitStr(t, gitBin, src, "rev-parse", "HEAD"))

	// === Step 1: go-git clone ===
	clone := filepath.Join(dir, "goclone")
	repo, err := gogit.PlainClone(clone, false, &gogit.CloneOptions{URL: src})
	if err != nil {
		t.Fatalf("go-git clone failed: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("go-git Head: %v", err)
	}
	if got := head.Hash().String(); got != headSHA {
		t.Fatalf("go-git clone HEAD = %s, want %s", got, headSHA)
	}
	t.Logf("step 1 OK: go-git cloned HEAD %s", headSHA[:8])

	// === Step 2: git binary receive-pack / unpack ===
	// Produce a pack of all objects, then unpack it into a fresh bare repo with
	// the git binary, mirroring writePack (`git unpack-objects`, unpackLimit 0).
	bare := filepath.Join(dir, "remote.git")
	gitStr(t, gitBin, "", "init", "-q", "--bare", bare)
	revs := gitStr(t, gitBin, src, "rev-list", "--objects", "--all")
	pack := gitRaw(t, gitBin, src, []byte(revs), "pack-objects", "--stdout")
	gitStr(t, gitBin, bare, "config", "receive.unpackLimit", "0")
	gitRaw(t, gitBin, bare, pack, "unpack-objects", "-q")
	if typ := strings.TrimSpace(gitStr(t, gitBin, bare, "cat-file", "-t", headSHA)); typ != "commit" {
		t.Fatalf("after unpack, object %s type = %q, want commit", headSHA[:8], typ)
	}
	t.Logf("step 2 OK: git unpack-objects landed %d-byte pack; commit %s present in bare repo",
		len(pack), headSHA[:8])

	// === Step 3: git binary diff ===
	diff := gitStr(t, gitBin, src, "diff", "HEAD~1", "HEAD")
	if !strings.Contains(diff, "+world") {
		t.Fatalf("git diff missing expected change; got:\n%s", diff)
	}
	t.Logf("step 3 OK: git diff produced %d bytes containing the change", len(diff))

	// sanity: go-git resolves the same commit object (object-store fidelity)
	if _, err := repo.CommitObject(plumbing.NewHash(headSHA)); err != nil {
		t.Fatalf("go-git CommitObject(%s): %v", headSHA[:8], err)
	}
}

func gitRaw(t *testing.T, bin, dir string, stdin []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.Bytes()
}

func gitStr(t *testing.T, bin, dir string, args ...string) string {
	t.Helper()
	return string(gitRaw(t, bin, dir, nil, args...))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

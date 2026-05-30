// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestParsePushRealGitPack drives parsePush against a packfile produced by the
// real git binary, validating the parser against git's actual pack output
// (real object headers, zlib streams and any deltas). Skipped when git is
// unavailable, mirroring the engine spike test.
func TestParsePushRealGitPack(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not found; skipping real-git parsePush test")
	}

	dir := t.TempDir()
	repo := filepath.Join(dir, "src")
	git := func(stdin []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		if !strings.HasPrefix(args[0], "init") {
			cmd.Dir = repo
		}
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return out
	}

	git(nil, "init", "-q", repo)
	git(nil, "config", "user.email", "spike@example.com")
	git(nil, "config", "user.name", "Spike")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(nil, "add", "a.txt")
	git(nil, "commit", "-q", "-m", "first commit")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(nil, "commit", "-q", "-am", "second commit")
	head := strings.TrimSpace(string(git(nil, "rev-parse", "HEAD")))

	// Build a packfile of everything reachable from HEAD (a new-branch push).
	revs := git(nil, "rev-list", "--objects", "HEAD")
	pack := git(revs, "pack-objects", "--stdout")

	// Assemble the receive-pack body: ref-update pkt-line + flush + packfile.
	var body bytes.Buffer
	body.WriteString(pktLine(emptyCommitHash + " " + head + " refs/heads/main\x00report-status"))
	body.WriteString("0000")
	body.Write(pack)

	a := NewAction("id", "push", "POST", 0, "https://github.com/x/y.git")
	if _, err := (&Engine{}).parsePushInto(rawCtx(body.Bytes()), a); err != nil {
		t.Fatalf("parsePushInto on real pack: %v", err)
	}

	if len(a.CommitData) != 2 {
		t.Fatalf("commitData = %d, want 2", len(a.CommitData))
	}
	messages := map[string]bool{}
	for _, c := range a.CommitData {
		messages[c.Message] = true
		if c.CommitterEmail != "spike@example.com" || c.Committer != "Spike" {
			t.Errorf("committer = %q <%q>, want Spike/spike@example.com", c.Committer, c.CommitterEmail)
		}
	}
	if !messages["first commit"] || !messages["second commit"] {
		t.Errorf("messages = %v, want both commits", messages)
	}
	if a.UserEmail != "spike@example.com" {
		t.Errorf("userEmail = %q, want spike@example.com", a.UserEmail)
	}
}

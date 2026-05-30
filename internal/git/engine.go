// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	gogit "github.com/go-git/go-git/v5"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Clone performs a clone of url into dir using go-git (the network-clone half of
// the hybrid engine, per docs/decisions/0001-git-engine.md). When username or
// password is set, HTTP basic auth is used; depth > 0 makes the clone shallow.
func Clone(ctx context.Context, url, dir, username, password string, depth int) error {
	var auth *githttp.BasicAuth
	if username != "" || password != "" {
		auth = &githttp.BasicAuth{Username: username, Password: password}
	}
	if _, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:   url,
		Auth:  auth,
		Depth: depth,
	}); err != nil {
		return fmt.Errorf("git clone %s: %w", url, err)
	}
	return nil
}

// Run executes the git binary in dir (the receive-pack/unpack/diff half of the
// hybrid engine), optionally feeding stdin, and returns the combined output. A
// non-zero exit is returned as an error with the captured output for context.
func Run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return out, nil
}

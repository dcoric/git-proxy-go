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
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
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

// CloneSSH performs a clone of url (an scp-like "git@host:path" or "ssh://"
// address) into dir over SSH, authenticating with the client's forwarded agent
// (signers) and verifying the upstream host key (hostKey). It is the SSH
// counterpart of Clone for SSH-originated pushes (issue #105 / PR #1332's
// PullRemoteSSH), keeping the network clone on go-git per the hybrid-engine
// decision. depth > 0 makes the clone shallow.
func CloneSSH(ctx context.Context, url, dir, user string, signers func() ([]ssh.Signer, error), hostKey ssh.HostKeyCallback, depth int) error {
	if user == "" {
		user = "git"
	}
	auth := &gitssh.PublicKeysCallback{User: user, Callback: signers}
	auth.HostKeyCallback = hostKey
	if _, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:   url,
		Auth:  auth,
		Depth: depth,
	}); err != nil {
		return fmt.Errorf("git clone (ssh) %s: %w", url, err)
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

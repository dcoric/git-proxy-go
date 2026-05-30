// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"

	"golang.org/x/crypto/ssh"
)

// SSHCloneAuth carries the credentials pullRemote needs to clone over SSH for an
// SSH-originated push: the upstream user (always "git"), the client's forwarded
// agent signers, and the host-key verification callback. It is placed on the
// request context by the SSH proxy handler (the HTTP proxy never sets it, so
// HTTP-originated pushes keep cloning over HTTPS). Mirrors PR #1332 routing a
// push to PullRemoteSSH when action.protocol === 'ssh'.
type SSHCloneAuth struct {
	User    string
	Signers func() ([]ssh.Signer, error)
	HostKey ssh.HostKeyCallback
}

type sshCloneAuthKey struct{}

// WithSSHCloneAuth returns ctx carrying the SSH clone credentials.
func WithSSHCloneAuth(ctx context.Context, auth *SSHCloneAuth) context.Context {
	return context.WithValue(ctx, sshCloneAuthKey{}, auth)
}

// SSHCloneAuthFromContext reports the SSH clone credentials on ctx, if any. The
// SSH proxy handler uses it to confirm the credentials reach the chain.
func SSHCloneAuthFromContext(ctx context.Context) (*SSHCloneAuth, bool) {
	auth, ok := ctx.Value(sshCloneAuthKey{}).(*SSHCloneAuth)
	return auth, ok && auth != nil
}

// cloneHTTPSFunc clones over HTTPS with basic auth (gitengine.Clone).
type cloneHTTPSFunc func(ctx context.Context, url, dir, username, password string, depth int) error

// cloneSSHFunc clones over SSH using the forwarded agent (gitengine.CloneSSH).
type cloneSSHFunc func(ctx context.Context, url, dir, user string, signers func() ([]ssh.Signer, error), hostKey ssh.HostKeyCallback, depth int) error

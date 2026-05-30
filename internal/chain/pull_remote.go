// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	gitengine "github.com/dcoric/git-proxy-go/internal/git"
)

// httpsCloner / sshCloner return the configured clone implementation, defaulting
// to the git engine when unset (Engines built directly in tests leave them nil).
func (e *Engine) httpsCloner() cloneHTTPSFunc {
	if e.cloneHTTPS != nil {
		return e.cloneHTTPS
	}
	return gitengine.Clone
}

func (e *Engine) sshCloner() cloneSSHFunc {
	if e.cloneSSH != nil {
		return e.cloneSSH
	}
	return gitengine.CloneSSH
}

// pullRemote clones the upstream repo into a per-push working directory so the
// downstream processors can inspect it. Port of
// src/proxy/processors/push-action/pullRemote.ts: it uses go-git for the network
// clone (the hybrid engine), requires HTTP basic credentials (anonymous pulls
// are unsupported), and fails fast if the checkout folder already exists (a
// concurrent request for the same push).
func (e *Engine) pullRemote(ctx context.Context, r *http.Request, a *Action) (*Action, error) {
	step := NewStep("pullRemote")
	a.ProxyGitPath = filepath.Join(e.remoteDir, a.ID)

	if _, err := os.Stat(a.ProxyGitPath); err == nil {
		// Do not delete the folder: another request may be completing.
		step.SetError("The checkout folder already exists - we may be processing a concurrent request for this push. If this issue persists the proxy may need to be restarted.")
		a.AddStep(step)
		return a, nil
	}

	if err := e.clone(ctx, r, a, step); err != nil {
		step.SetError(err.Error())
		// Clean up the partial checkout so it doesn't block subsequent attempts.
		_ = os.RemoveAll(a.ProxyGitPath)
		step.Log(".remote is deleted!")
	}
	a.AddStep(step)
	return a, nil
}

// clone creates the checkout folder and performs the authenticated clone,
// dispatching to SSH (when the request carries forwarded-agent credentials) or
// HTTPS basic auth, and marking the action for post-chain cleanup on success.
// Port of createPullRemote's protocol switch.
func (e *Engine) clone(ctx context.Context, r *http.Request, a *Action, step *Step) error {
	step.Log("Creating folder " + a.ProxyGitPath)
	if err := os.MkdirAll(a.ProxyGitPath, 0o755); err != nil {
		return fmt.Errorf("creating checkout folder: %w", err)
	}
	dest := filepath.Join(a.ProxyGitPath, a.RepoName)

	if auth, ok := SSHCloneAuthFromContext(ctx); ok {
		return e.cloneSSHInto(ctx, a, step, dest, auth)
	}
	return e.cloneHTTPSInto(ctx, r, a, step, dest)
}

// cloneHTTPSInto clones over HTTPS using the inbound request's basic credentials
// (anonymous pulls are unsupported).
func (e *Engine) cloneHTTPSInto(ctx context.Context, r *http.Request, a *Action, step *Step, dest string) error {
	username, password, ok := r.BasicAuth()
	if !ok {
		return fmt.Errorf("authorization header is required for pullRemote. Make sure to provide valid credentials as anonymous pulls are not currently supported")
	}

	cmd := "git clone " + a.URL
	step.Log("Executing " + cmd)
	if err := e.httpsCloner()(ctx, a.URL, dest, username, password, 1); err != nil {
		return err
	}
	step.Log("Completed " + cmd)
	step.SetContent("Completed " + cmd)
	a.cleanupClone = true
	return nil
}

// cloneSSHInto clones over SSH using the client's forwarded agent and host-key
// verification (PullRemoteSSH).
func (e *Engine) cloneSSHInto(ctx context.Context, a *Action, step *Step, dest string, auth *SSHCloneAuth) error {
	sshURL := convertToSSHURL(a.URL)
	cmd := "git clone " + sshURL
	step.Log("Cloning over SSH using agent forwarding: " + sshURL)
	if err := e.sshCloner()(ctx, sshURL, dest, auth.User, auth.Signers, auth.HostKey, 1); err != nil {
		return err
	}
	step.Log("Completed " + cmd)
	step.SetContent("Completed " + cmd)
	a.cleanupClone = true
	return nil
}

// convertToSSHURL turns an "https://host/path.git" repo URL into the scp-like
// "git@host:path.git" address go-git's SSH transport expects. Port of
// convertToSSHUrl; on a non-URL input it returns the input unchanged.
func convertToSSHURL(httpsURL string) string {
	u, err := url.Parse(httpsURL)
	if err != nil || u.Hostname() == "" {
		return httpsURL
	}
	// Drop any HTTPS port; SSH connects on 22.
	return "git@" + u.Hostname() + ":" + strings.TrimPrefix(u.Path, "/")
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	gitengine "github.com/dcoric/git-proxy-go/internal/git"
)

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
// marking the action for post-chain cleanup on success.
func (e *Engine) clone(ctx context.Context, r *http.Request, a *Action, step *Step) error {
	step.Log("Creating folder " + a.ProxyGitPath)
	if err := os.MkdirAll(a.ProxyGitPath, 0o755); err != nil {
		return fmt.Errorf("creating checkout folder: %w", err)
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return fmt.Errorf("authorization header is required for pullRemote. Make sure to provide valid credentials as anonymous pulls are not currently supported")
	}

	cmd := "git clone " + a.URL
	step.Log("Executing " + cmd)
	if err := gitengine.Clone(ctx, a.URL, filepath.Join(a.ProxyGitPath, a.RepoName), username, password, 1); err != nil {
		return err
	}
	step.Log("Completed " + cmd)
	step.SetContent("Completed " + cmd)
	a.cleanupClone = true
	return nil
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"net/http"
)

// checkEmptyBranch blocks a push that carries no commit data. Port of
// src/proxy/processors/push-action/checkEmptyBranch.ts: when the push has
// commits it passes without recording a step; otherwise it errors.
//
// The Node empty-branch detection inspects the cloned repo with `git cat-file`,
// but this processor runs before pullRemote (#46), so no clone exists yet and
// the check resolves to false — exactly as in Node. The git-backed check will
// activate once pullRemote sets proxyGitPath.
func (e *Engine) checkEmptyBranch(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
	if len(a.CommitData) > 0 {
		return a, nil
	}

	step := NewStep("checkEmptyBranch")
	if isEmptyBranch(a) {
		step.SetError("Push blocked: Empty branch. Please make a commit before pushing a new branch.")
	} else {
		step.SetError("Push blocked: Commit data not found. Please contact an administrator for support.")
	}
	a.AddStep(step)
	return a, nil
}

// isEmptyBranch reports whether the push is a legitimately empty new branch.
// This requires a clone to inspect (proxyGitPath), which does not exist until
// pullRemote, so it currently always returns false.
func isEmptyBranch(a *Action) bool {
	if a.CommitFrom != emptyCommitHash || a.ProxyGitPath == "" {
		return false
	}
	// TODO(#46): once pullRemote provides the clone, verify commitTo exists via
	// `git cat-file -t` in proxyGitPath/repoName.
	return false
}

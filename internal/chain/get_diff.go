// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"net/http"
	"path/filepath"

	gitengine "github.com/dcoric/git-proxy-go/internal/git"
)

// emptyTreeHash is git's well-known empty-tree object, used as the diff base
// for an initial commit (see getDiff.ts).
const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// getDiff computes the push diff with the git binary and stores it on the
// "diff" step for scanDiff to consume. Port of
// src/proxy/processors/push-action/getDiff.ts.
func (e *Engine) getDiff(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("diff")

	if len(a.CommitData) == 0 {
		step.Log("No commitData found")
		step.SetError("Your push has been blocked because no commit data was found.")
		a.AddStep(step)
		return a, nil
	}

	commitFrom := emptyTreeHash
	if a.CommitFrom == emptyCommitHash {
		if a.CommitData[0].Parent != emptyCommitHash {
			commitFrom = a.CommitData[len(a.CommitData)-1].Parent
		}
	} else {
		commitFrom = a.CommitFrom
	}

	path := filepath.Join(a.ProxyGitPath, a.RepoName)
	revisionRange := commitFrom + ".." + a.CommitTo
	step.Log("Executing \"git diff " + commitFrom + " " + a.CommitTo + "\" in " + path)

	out, err := gitengine.Run(ctx, path, nil, "diff", revisionRange)
	if err != nil {
		step.SetError(err.Error())
		a.AddStep(step)
		return a, nil
	}
	diff := string(out)
	step.Log(diff)
	step.SetContent(diff)
	a.AddStep(step)
	return a, nil
}

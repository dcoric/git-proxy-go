// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	gitengine "github.com/dcoric/git-proxy-go/internal/git"
)

// checkHiddenCommits blocks a push whose pack contains commits not reachable in
// the introduced commit range — commits made from an unapproved base. Port of
// src/proxy/processors/push-action/checkHiddenCommits.ts.
func (e *Engine) checkHiddenCommits(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("checkHiddenCommits")
	if err := e.runHiddenCommits(ctx, a, step); err != nil {
		step.SetError(err.Error())
	}
	a.AddStep(step)
	return a, nil
}

func (e *Engine) runHiddenCommits(ctx context.Context, a *Action, step *Step) error {
	if a.CommitFrom == "" || a.CommitTo == "" {
		return fmt.Errorf("both action.commitFrom and action.commitTo must be defined")
	}
	repoPath := filepath.Join(a.ProxyGitPath, a.RepoName)

	// Commits introduced by this push.
	revRange := a.CommitTo
	if a.CommitFrom != emptyCommitHash {
		revRange = a.CommitFrom + ".." + a.CommitTo
	}
	out, err := gitengine.Run(ctx, repoPath, nil, "rev-list", revRange)
	if err != nil {
		return err
	}
	introduced := lineSet(string(out))
	step.Log(fmt.Sprintf("Total introduced commits: %d", len(introduced)))

	// Commits actually present in the pushed pack(s).
	pack := map[string]bool{}
	for _, idx := range a.NewIdxFiles {
		idxPath := filepath.Join(".git", "objects", "pack", idx)
		vp, err := gitengine.Run(ctx, repoPath, nil, "verify-pack", "-v", idxPath)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(vp)), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "commit" {
				pack[fields[0]] = true
			}
		}
	}
	step.Log(fmt.Sprintf("Total commits in the pack: %d", len(pack)))

	// Every pack commit must be within the introduced range.
	var referenced, unreferenced []string
	for sha := range pack {
		if introduced[sha] {
			referenced = append(referenced, sha)
		} else {
			unreferenced = append(unreferenced, sha)
		}
	}
	if len(unreferenced) > 0 {
		sort.Strings(unreferenced)
		step.Log(fmt.Sprintf("Referenced commits: %d", len(referenced)))
		step.Log(fmt.Sprintf("Unreferenced commits: %d", len(unreferenced)))
		step.SetError(fmt.Sprintf(
			"Unreferenced commits in pack (%d): %s.\n"+
				"This usually happens when a branch was made from a commit that hasn't been approved and pushed to the remote.\n"+
				"Please get approval on the commits, push them and try again.",
			len(unreferenced), strings.Join(unreferenced, ", ")))
		step.SetContent(fmt.Sprintf("Referenced: %d, Unreferenced: %d", len(referenced), len(unreferenced)))
		return nil
	}

	step.Log("All pack commits are referenced in the introduced range.")
	step.SetContent(fmt.Sprintf("All %d pack commits are within introduced commits.", len(pack)))
	return nil
}

// lineSet splits output into a set of non-empty trimmed lines.
func lineSet(out string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set
}

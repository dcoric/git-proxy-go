// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gitengine "github.com/dcoric/git-proxy-go/internal/git"
)

// writePack stores the pushed objects into the local clone with the git binary
// so the downstream processors can inspect them. Port of
// src/proxy/processors/push-action/writePack.ts: it sets receive.unpackLimit to
// 0 (always keep a pack), feeds the receive-pack request body to
// `git receive-pack`, and records the .idx files the push added.
func (e *Engine) writePack(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("writePack")
	if err := e.runWritePack(ctx, a, step); err != nil {
		step.SetError(err.Error())
	}
	a.AddStep(step)
	return a, nil
}

func (e *Engine) runWritePack(ctx context.Context, a *Action, step *Step) error {
	if a.ProxyGitPath == "" || a.RepoName == "" {
		return fmt.Errorf("proxyGitPath and repoName must be defined")
	}
	repoPath := filepath.Join(a.ProxyGitPath, a.RepoName)
	packDir := filepath.Join(repoPath, ".git", "objects", "pack")

	if _, err := gitengine.Run(ctx, repoPath, nil, "config", "receive.unpackLimit", "0"); err != nil {
		return err
	}

	before, err := idxFiles(packDir)
	if err != nil {
		return err
	}

	// Feed the push body to receive-pack. Its exit status is ignored (as in the
	// Node spawnSync): a rejected ref update still leaves the objects written,
	// and the new .idx files below are the real signal.
	body, _ := RawBody(ctx)
	if _, err := gitengine.Run(ctx, a.ProxyGitPath, body, "receive-pack", a.RepoName); err != nil {
		slog.Warn("git receive-pack reported an error (continuing)", "err", err)
	}

	after, err := idxFiles(packDir)
	if err != nil {
		return err
	}

	var newIdx []string
	for f := range after {
		if !before[f] {
			newIdx = append(newIdx, f)
		}
	}
	sort.Strings(newIdx)
	a.NewIdxFiles = newIdx
	step.Log(fmt.Sprintf("new idx files: %s", strings.Join(newIdx, ",")))
	return nil
}

// idxFiles returns the set of *.idx file names in dir (empty when dir is absent).
func idxFiles(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".idx") {
			set[e.Name()] = true
		}
	}
	return set, nil
}

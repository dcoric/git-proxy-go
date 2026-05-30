// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gitengine "github.com/dcoric/git-proxy-go/internal/git"
)

// gitleaksFindingsExitCode is the exit code gitleaks is told to use when it
// finds secrets (matches EXIT_CODE in gitleaks.ts).
const gitleaksFindingsExitCode = 99

// gitleaks scans the pushed commit range for secrets using the gitleaks binary.
// It is disabled by default (api.gitleaks.enabled). Port of
// src/proxy/processors/push-action/gitleaks.ts.
func (e *Engine) gitleaks(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("gitleaks")

	enabled, ignoreAllow, noColor, configPath := e.gitleaksConfig()
	if !enabled {
		step.Log("Gitleaks is disabled, skipping.")
		a.AddStep(step)
		return a, nil
	}
	if configPath != "" && !fileReadable(configPath) {
		step.SetError("Failed to get gitleaks config: unable to read file at the provided config path: " + configPath)
		a.AddStep(step)
		return a, nil
	}

	workingDir := filepath.Join(a.ProxyGitPath, a.RepoName)
	step.Log(fmt.Sprintf("Scanning range with gitleaks: %s:%s in %s", a.CommitFrom, a.CommitTo, workingDir))

	rootOut, err := gitengine.Run(ctx, workingDir, nil, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		step.SetError("Failed to spawn gitleaks: " + err.Error())
		a.AddStep(step)
		return a, nil
	}
	rootCommit := strings.TrimSpace(string(rootOut))

	from := a.CommitFrom + "^"
	if rootCommit == a.CommitFrom {
		from = rootCommit
	}
	args := []string{fmt.Sprintf("--exit-code=%d", gitleaksFindingsExitCode), "--platform=none"}
	if configPath != "" {
		args = append(args, "--config="+configPath)
	}
	if ignoreAllow {
		args = append(args, "--ignore-gitleaks-allow")
	}
	args = append(args, "--no-banner")
	if noColor {
		args = append(args, "--no-color")
	}
	args = append(args, "--redact", "--verbose", "git",
		fmt.Sprintf("--log-opts=--first-parent %s..%s", from, a.CommitTo))

	code, stdout, stderr, runErr := runGitleaks(ctx, workingDir, args)
	switch {
	case runErr != nil:
		step.SetError("Failed to spawn gitleaks: " + runErr.Error())
	case code == 0:
		step.Log("Succeeded.")
		step.Log("Gitleaks output: " + stderr)
	case code == gitleaksFindingsExitCode:
		step.SetError("\n" + stdout + stderr)
	default:
		step.SetError("Failed to run gitleaks, please contact an administrator.")
	}

	a.AddStep(step)
	return a, nil
}

// gitleaksConfig returns the effective gitleaks settings, applying the Node
// defaults (disabled; ignoreGitleaksAllow true; noColor false).
func (e *Engine) gitleaksConfig() (enabled, ignoreAllow, noColor bool, configPath string) {
	ignoreAllow = true // default
	if e.cfg == nil || e.cfg.API == nil || e.cfg.API.Gitleaks == nil {
		return false, ignoreAllow, false, ""
	}
	g := e.cfg.API.Gitleaks
	if g.Enabled != nil {
		enabled = *g.Enabled
	}
	if g.IgnoreGitleaksAllow != nil {
		ignoreAllow = *g.IgnoreGitleaksAllow
	}
	if g.NoColor != nil {
		noColor = *g.NoColor
	}
	if g.ConfigPath != nil {
		configPath = strings.TrimSpace(*g.ConfigPath)
	}
	return enabled, ignoreAllow, noColor, configPath
}

// runGitleaks executes the gitleaks binary, returning its exit code and output.
// A non-nil error indicates gitleaks could not be spawned (e.g. not installed),
// as distinct from a non-zero exit.
func runGitleaks(ctx context.Context, dir string, args []string) (code int, stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "gitleaks", args...)
	cmd.Dir = dir
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	runErr := cmd.Run()
	if runErr == nil {
		return 0, so.String(), se.String(), nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return ee.ExitCode(), so.String(), se.String(), nil
	}
	return 0, so.String(), se.String(), runErr
}

// fileReadable reports whether path is a readable regular file.
func fileReadable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

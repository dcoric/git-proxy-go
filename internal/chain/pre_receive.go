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
	"runtime"
	"strings"
)

// preReceiveHookPath is the external pre-receive hook script (Node default).
const preReceiveHookPath = "./hooks/pre-receive.sh"

// preReceive runs the external pre-receive hook, if present, and applies its
// verdict: exit 0 auto-approves, 1 auto-rejects, 2 requires manual approval,
// anything else is an error. Port of
// src/proxy/processors/push-action/preReceive.ts. The hook is a Unix shell
// script, so execution is skipped on Windows and when the hook is absent.
func (e *Engine) preReceive(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("executeExternalPreReceiveHook")

	if runtime.GOOS == "windows" {
		step.Log("Warning: Pre-receive hooks are not supported on Windows, skipping execution.")
		a.AddStep(step)
		return a, nil
	}

	hookPath := e.preReceiveHook
	if hookPath == "" {
		hookPath = preReceiveHookPath
	}
	resolved, err := filepath.Abs(hookPath)
	if err != nil {
		step.SetError(fmt.Sprintf("Hook execution error: %s", err))
		a.AddStep(step)
		return a, nil
	}
	if _, err := os.Stat(resolved); err != nil {
		step.Log("Pre-receive hook not found, skipping execution.")
		a.AddStep(step)
		return a, nil
	}

	repoPath := filepath.Join(a.ProxyGitPath, a.RepoName)
	step.Log("Executing pre-receive hook from: " + resolved)

	cmd := exec.CommandContext(ctx, resolved)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s %s %s \n", a.CommitFrom, a.CommitTo, a.Branch))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	status, ran := exitStatus(runErr)
	step.Log(fmt.Sprintf("Hook exited with status %d", status))

	switch {
	case ran && status == 0:
		step.Log("Push automatically approved by pre-receive hook.")
		a.AddStep(step)
		a.SetAutoApproval()
	case ran && status == 1:
		step.Log("Push automatically rejected by pre-receive hook.")
		a.AddStep(step)
		a.SetAutoRejection()
	case ran && status == 2:
		step.Log("Push requires manual approval.")
		a.AddStep(step)
	default:
		step.Log(fmt.Sprintf("Unexpected hook status: %d", status))
		msg := strings.TrimSpace(stdout.String())
		if msg == "" {
			msg = "Unknown pre-receive hook error."
		}
		step.SetError(msg)
		a.AddStep(step)
	}
	return a, nil
}

// exitStatus returns the process exit code and whether it exited normally (a
// non-zero code) versus failing to run at all (e.g. not executable).
func exitStatus(err error) (code int, ran bool) {
	if err == nil {
		return 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return -1, false
}

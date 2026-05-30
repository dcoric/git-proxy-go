// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"crypto/rand"
	"fmt"
	"log/slog"

	"github.com/dcoric/git-proxy-go/internal/db"
	"github.com/dcoric/git-proxy-go/internal/giturl"
)

// Action is the runtime push/pull being processed by the chain — the Go port of
// the Action class (src/proxy/actions/Action.ts). It embeds db.Push (the stored
// shape) and adds the behaviour the processors rely on; auditing persists the
// embedded Push unchanged.
type Action struct {
	db.Push

	// cleanupClone marks that pullRemote successfully created a bare clone that
	// the post-chain clearBareClone must remove. It is runtime-only (not
	// persisted), mirroring the Node checkoutCleanUpRequired flag — set only on a
	// successful clone, so the concurrent-request ("folder exists") and
	// clone-failure paths do not trigger cleanup.
	cleanupClone bool
}

// NewAction builds an Action for the given inbound request details, deriving the
// repo/project/repoName from the repo URL (mirrors the Action constructor).
func NewAction(id, actionType, method string, timestamp int64, url string) *Action {
	a := &Action{Push: db.Push{
		ID:        id,
		Type:      actionType,
		Method:    method,
		Timestamp: timestamp,
		URL:       url,
		Steps:     []db.Step{},
	}}
	if b := giturl.ProcessURLPath(url); b != nil {
		a.Repo = b.RepoPath
		if nb := giturl.ProcessGitURLForNameAndOrg(b.RepoPath); nb != nil {
			a.Project = nb.Project
			a.RepoName = nb.RepoName
		}
	} else {
		a.Repo = "NOT-FOUND"
		a.Project = "UNKNOWN"
		a.RepoName = "UNKNOWN"
	}
	return a
}

// AddStep appends a step and propagates its blocked/error state to the action
// (mirrors Action.addStep).
func (a *Action) AddStep(s *Step) {
	a.Steps = append(a.Steps, s.Step)
	last := s.Step
	a.LastStep = &last

	if s.Blocked {
		a.Blocked = true
		a.BlockedMessage = s.BlockedMessage
	}
	if s.Error {
		a.Error = true
		a.ErrorMessage = s.ErrorMessage
	}
}

// GetLastStep returns the most recently added step, or nil.
func (a *Action) GetLastStep() *db.Step { return a.LastStep }

// SetCommit records the commit range and rebases the action id on it (mirrors
// Action.setCommit).
func (a *Action) SetCommit(commitFrom, commitTo string) {
	a.CommitFrom = commitFrom
	a.CommitTo = commitTo
	a.ID = fmt.Sprintf("%s__%s", commitFrom, commitTo)
}

// SetBranch records the branch.
func (a *Action) SetBranch(branch string) { a.Branch = branch }

// SetMessage records a message.
func (a *Action) SetMessage(message string) { a.Message = message }

// SetAllowPush marks the action as explicitly allowed, clearing any block.
func (a *Action) SetAllowPush() {
	a.AllowPush = true
	a.Blocked = false
}

// SetAutoApproval marks the action for auto-approval.
func (a *Action) SetAutoApproval() { a.AutoApproved = true }

// SetAutoRejection marks the action for auto-rejection.
func (a *Action) SetAutoRejection() { a.AutoRejected = true }

// Continue reports whether the chain may proceed (no error, not blocked).
func (a *Action) Continue() bool { return !a.Error && !a.Blocked }

// Step is one chain step result — the Go port of the Step class
// (src/proxy/actions/Step.ts). It embeds db.Step and adds the mutators the
// processors use.
type Step struct {
	db.Step
}

// NewStep creates a named step with a fresh id.
func NewStep(name string) *Step {
	return &Step{Step: db.Step{ID: newUUID(), StepName: name, Logs: []string{}}}
}

// SetError marks the step errored, records the message and logs it.
func (s *Step) SetError(message string) {
	s.Error = true
	s.ErrorMessage = &message
	s.Log(message)
}

// SetContent attaches arbitrary content to the step.
func (s *Step) SetContent(content any) { s.Content = content }

// SetAsyncBlock marks the step blocked pending asynchronous review.
func (s *Step) SetAsyncBlock(message string) {
	s.Blocked = true
	s.BlockedMessage = &message
}

// Log appends a "<stepName> - <message>" line to the step log.
func (s *Step) Log(message string) {
	m := fmt.Sprintf("%s - %s", s.StepName, message)
	s.Logs = append(s.Logs, m)
	slog.Info(m)
}

// newUUID returns a random RFC 4122 v4 UUID (replacing the Node uuid dep).
func newUUID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

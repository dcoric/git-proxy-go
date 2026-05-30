// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/dcoric/git-proxy-go/internal/db"
	"github.com/dcoric/git-proxy-go/internal/giturl"
)

// Store is the slice of the persistence layer the chain needs: repo lookup for
// parseAction, audit writes, and the authorise/reject calls behind auto
// actions. db.Store satisfies it.
type Store interface {
	GetRepoByURL(ctx context.Context, url string) (*db.Repo, error)
	WriteAudit(ctx context.Context, p *db.Push) error
	Authorise(ctx context.Context, id string, attestation *db.Attestation) (string, error)
	Reject(ctx context.Context, id string, rejection db.Rejection) (string, error)
}

// Processor is one chain step — the Go equivalent of the Node
// (req, action) => Promise<Action> functions. It mutates and returns the action;
// a non-nil error is treated like a thrown exception (the action is marked
// errored and the chain stops).
type Processor func(ctx context.Context, r *http.Request, a *Action) (*Action, error)

// Engine runs the processor chains. The push chain is filled out as the
// individual processors are ported (#40–#54); the pull and default chains hold
// only checkRepoInAuthorisedList (P4-3).
type Engine struct {
	store Store

	pushChain    []Processor
	pullChain    []Processor
	defaultChain []Processor
}

// NewEngine builds the production engine over store and wires the processor
// chains. Today only checkRepoInAuthorisedList is ported; in the Node push
// chain it sits after parsePush/checkEmptyBranch, which prepend it as they land.
func NewEngine(store Store) *Engine {
	e := &Engine{store: store}
	e.pushChain = []Processor{e.checkRepoInAuthorisedList}
	e.pullChain = []Processor{e.checkRepoInAuthorisedList}
	e.defaultChain = []Processor{e.checkRepoInAuthorisedList}
	return e
}

// Execute runs the full chain for a request and returns the resulting action.
// It is the Go port of executeChain: parseAction, then the type-specific chain
// (stopping when the action cannot continue or has been allowed), then — in all
// cases — bare-clone cleanup, audit, and any auto approve/reject.
func (e *Engine) Execute(ctx context.Context, r *http.Request) *Action {
	action := &Action{}

	parsed, err := e.parseAction(ctx, r)
	if err == nil {
		action = parsed
		for _, fn := range e.getChain(action) {
			action, err = fn(ctx, r, action)
			if err != nil {
				break
			}
			if !action.Continue() || action.AllowPush {
				break
			}
		}
	}
	if err != nil {
		msg := err.Error()
		slog.Error("chain execution error", "err", msg)
		action.Error = true
		action.ErrorMessage = &msg
	}

	// Clean up a bare clone if one was created. Node tracks this with a
	// checkoutCleanUpRequired flag set when pullRemote ran; keying off
	// proxyGitPath (which pullRemote sets) is behaviourally equivalent and
	// avoids comparing function identities.
	if action.ProxyGitPath != "" {
		action = e.clearBareClone(action)
	}

	e.audit(ctx, action)

	switch {
	case action.AutoApproved:
		e.attemptAutoApproval(ctx, action)
	case action.AutoRejected:
		e.attemptAutoRejection(ctx, action)
	}

	return action
}

// getChain selects the processor chain for the action type (mirrors getChain).
func (e *Engine) getChain(a *Action) []Processor {
	switch a.Type {
	case "pull":
		return e.pullChain
	case "push":
		return e.pushChain
	default:
		return e.defaultChain
	}
}

// parseAction is the pre-processor: it classifies the request as push/pull/
// default from the content-type and resolves the upstream repo URL, falling
// back to github.com for legacy host-less proxy paths. Port of parseAction.exec.
func (e *Engine) parseAction(ctx context.Context, r *http.Request) (*Action, error) {
	id := time.Now().UnixMilli()
	timestamp := id

	actionType := "default"
	switch r.Header.Get("Content-Type") {
	case "application/x-git-upload-pack-request":
		actionType = "pull"
	case "application/x-git-receive-pack-request":
		actionType = "push"
	}

	repoPath := "NOT-FOUND"
	if b := giturl.ProcessURLPath(r.RequestURI); b != nil {
		repoPath = b.RepoPath
	}
	url := "https:/" + repoPath

	repo, err := e.store.GetRepoByURL(ctx, url)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		// Legacy proxy paths omitted the host; assume github.com.
		url = "https://github.com" + repoPath
	}

	return NewAction(strconv.FormatInt(id, 10), actionType, r.Method, timestamp, url), nil
}

// clearBareClone removes the bare clone directory created by pullRemote. Port of
// clearBareClone.exec.
func (e *Engine) clearBareClone(a *Action) *Action {
	step := NewStep("clearBareClone")
	if a.ProxyGitPath != "" {
		if err := os.RemoveAll(a.ProxyGitPath); err != nil {
			step.SetError(err.Error())
		} else {
			step.Log(".remote is deleted!")
		}
	} else {
		step.SetError("action.proxyGitPath was not set and cannot be removed")
	}
	a.AddStep(step)
	return a
}

// audit writes the audit record for non-pull actions (mirrors audit.exec; the
// pushes table doubles as the audit log). Failures are logged, not fatal — the
// git operation's outcome is already decided.
func (e *Engine) audit(ctx context.Context, a *Action) {
	if a.Type == "pull" {
		return
	}
	if err := e.store.WriteAudit(ctx, &a.Push); err != nil {
		slog.Error("audit write failed", "id", a.ID, "err", err)
	}
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// checkUserPushPermission blocks the push unless the committer (resolved from
// the commit e-mail) is permitted to push to the repo. Port of
// src/proxy/processors/push-action/checkUserPushPermission.ts.
func (e *Engine) checkUserPushPermission(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("checkUserPushPermission")

	if a.UserEmail == "" {
		step.SetError("Push blocked: User not found. Please contact an administrator for support.")
		a.AddStep(step)
		return a, nil
	}

	users, err := e.store.GetUsers(ctx, db.UserQuery{Email: &a.UserEmail})
	if err != nil {
		return a, err
	}

	allowed := false
	switch {
	case len(users) > 1:
		step.Log(fmt.Sprintf("Multiple Users have email <%s> so we cannot uniquely identify the user, ending", a.UserEmail))
		step.SetError(fmt.Sprintf("Your push has been blocked (there are multiple users with email %s)", a.UserEmail))
		a.AddStep(step)
		return a, nil
	case len(users) == 0:
		step.Log(fmt.Sprintf("No user with email address %s found", a.UserEmail))
	default:
		allowed, err = e.isUserPushAllowed(ctx, a.URL, users[0].Username)
		if err != nil {
			return a, err
		}
	}

	step.Log(fmt.Sprintf("User %s permission on Repo %s: %t", a.UserEmail, a.URL, allowed))
	if !allowed {
		step.Log(fmt.Sprintf("User %s is not allowed to push on repo %s, ending", a.UserEmail, a.URL))
		step.SetError(fmt.Sprintf("Your push has been blocked (%s is not allowed to push on repo %s)", a.UserEmail, a.URL))
		a.AddStep(step)
		return a, nil
	}

	step.Log(fmt.Sprintf("User %s is allowed to push on repo %s", a.UserEmail, a.URL))
	a.AddStep(step)
	return a, nil
}

// isUserPushAllowed reports whether username is on the repo's canPush or
// canAuthorise list. Port of isUserPushAllowed (username is lower-cased).
func (e *Engine) isUserPushAllowed(ctx context.Context, url, username string) (bool, error) {
	user := strings.ToLower(username)
	repo, err := e.store.GetRepoByURL(ctx, url)
	if err != nil || repo == nil {
		return false, err
	}
	return slices.Contains(repo.Users.CanPush, user) || slices.Contains(repo.Users.CanAuthorise, user), nil
}

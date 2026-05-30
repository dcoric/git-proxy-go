// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
)

// checkRepoInAuthorisedList rejects the action unless the target repo URL is in
// the store's authorised list. It is the Go port of
// src/proxy/processors/push-action/checkRepoInAuthorisedList.ts and is the only
// processor in the pull and default chains (P4-3), as well as a member of the
// push chain.
func (e *Engine) checkRepoInAuthorisedList(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("checkRepoInAuthorisedList")

	repo, err := e.store.GetRepoByURL(ctx, a.URL)
	if err != nil {
		// An unexpected store error aborts the chain (the Node getRepoByUrl
		// throw is caught by executeChain and marks the action errored).
		return a, err
	}
	if repo != nil {
		step.Log(fmt.Sprintf("repo %s is in the authorisedList", a.URL))
	} else {
		step.Log(fmt.Sprintf("repo %s is not in the authorised whitelist, ending", a.URL))
		step.SetError(fmt.Sprintf("Rejecting repo %s not in the authorised whitelist", a.URL))
	}

	a.AddStep(step)
	return a, nil
}

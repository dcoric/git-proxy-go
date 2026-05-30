// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"net/http"
)

// checkIfWaitingAuth lets a previously-submitted push through once it has been
// authorised: if a stored push with this id is already authorised, it adopts
// that record and allows the push. Port of
// src/proxy/processors/push-action/checkIfWaitingAuth.ts.
func (e *Engine) checkIfWaitingAuth(ctx context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("checkIfWaitingAuth")

	existing, err := e.store.GetPush(ctx, a.ID)
	if err != nil {
		step.SetError(err.Error())
		a.AddStep(step)
		return a, nil
	}
	if existing != nil && !a.Error && existing.Authorised {
		a.Push = *existing
		a.SetAllowPush()
	}
	a.AddStep(step)
	return a, nil
}

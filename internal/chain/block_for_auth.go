// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"net/http"
)

// blockForAuth is the terminal gate: a push that reaches it (passed every check
// and was not already authorised) is blocked pending manual approval, with a
// shareable link to the review dashboard. Port of
// src/proxy/processors/push-action/blockForAuth.ts.
func (e *Engine) blockForAuth(_ context.Context, r *http.Request, a *Action) (*Action, error) {
	step := NewStep("authBlock")
	url := e.serviceUIURL(r)

	message := "\n\n\n" +
		"\x1b[32mGitProxy has received your push ✅\x1b[0m\n\n" +
		"\U0001f517 Shareable Link\n\n" +
		"\x1b[34m" + url + "/dashboard/push/" + a.ID + "\x1b[0m" +
		"\n\n\n"
	step.SetAsyncBlock(message)

	a.AddStep(step)
	return a, nil
}

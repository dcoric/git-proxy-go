// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"log/slog"
	"time"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// systemReviewer is the synthetic reviewer recorded for automated decisions.
var systemReviewer = db.Reviewer{Username: "system", Email: "system@git-proxy.com"}

// attemptAutoApproval records a system attestation against the push. Port of
// attemptAutoApproval; errors are logged, not propagated.
func (e *Engine) attemptAutoApproval(ctx context.Context, a *Action) {
	attestation := &db.Attestation{
		Reviewer:  systemReviewer,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Automated: true,
		Answers:   []db.AttestationAnswer{},
	}
	if _, err := e.store.Authorise(ctx, a.ID, attestation); err != nil {
		slog.Error("auto-approval failed", "id", a.ID, "err", err)
		return
	}
	slog.Info("push automatically approved by system", "id", a.ID)
}

// attemptAutoRejection records a system rejection against the push. Port of
// attemptAutoRejection; errors are logged, not propagated.
func (e *Engine) attemptAutoRejection(ctx context.Context, a *Action) {
	rejection := db.Rejection{
		Reviewer:  systemReviewer,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Automated: true,
		Reason:    "Auto-rejected by system",
	}
	if _, err := e.store.Reject(ctx, a.ID, rejection); err != nil {
		slog.Error("auto-rejection failed", "id", a.ID, "err", err)
		return
	}
	slog.Info("push automatically rejected by system", "id", a.ID)
}

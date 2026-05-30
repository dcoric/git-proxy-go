// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package chain is the git-proxy processor chain (P4-2): the Go port of
// src/proxy/chain.ts plus the Action/Step runtime types (src/proxy/actions) and
// the pre/post processors (parseAction, audit, clearBareClone) and auto
// approve/reject actions.
//
// The Engine runs, per request, a pre-processor (parseAction) to build the
// Action, then the type-specific processor chain (push/pull/default), stopping
// as soon as the action can no longer continue or has been explicitly allowed;
// finally it cleans up any bare clone, writes the audit record and applies any
// auto approve/reject. The push processors themselves (parsePush, gitleaks, …,
// #40–#54) are added to the chains one per PR; this package delivers the engine
// and the chains start empty.
package chain

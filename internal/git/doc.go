// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package git contains the git-engine helpers for git-proxy-go.
//
// Per plan §3.9 the engine is a hybrid: go-git performs network clone/fetch,
// while the git binary performs receive-pack/unpack and diff (the operations
// where byte-fidelity to canonical git matters). The choice is validated by the
// engine spike (engine_spike_test.go) and recorded in
// docs/decisions/0001-git-engine.md.
package git

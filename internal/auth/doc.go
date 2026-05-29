// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package auth is the root of the authentication subsystem (P3): the strategy
// registry/multiplex and the local/OIDC/AD/JWT strategies will live under here.
//
// For now it carries the P0-5 day-1 interop check (bcrypt_interop_test.go),
// which proves Go's golang.org/x/crypto/bcrypt and Node's bcryptjs produce and
// verify mutually compatible hashes — a prerequisite for the local strategy
// (P3-4) reusing the existing user-table password hashes.
package auth

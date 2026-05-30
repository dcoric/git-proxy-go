// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package ssh is the git-over-SSH transport (P5): a golang.org/x/crypto/ssh
// server whose behaviour is ported from finos/git-proxy PR #1332 (pinned in
// docs/ssh-source-pin.md). Clients authenticate with a public key that the
// proxy maps to a user (db.FindUserBySSHKey); a git-upload-pack/git-receive-pack
// exec request is parsed and handed to a GitHandler.
//
// P5-1 (this package's first cut) is the server itself — host key, public-key
// auth, and command parsing. Routing the parsed command through the processor
// chain and forwarding upstream is P5-2 (the GitHandler seam); agent forwarding
// is P5-4.
package ssh

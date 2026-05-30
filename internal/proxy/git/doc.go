// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package proxygit is the git transport reverse proxy (P4-1): the Go port of
// src/proxy (index.ts + routes/index.ts). It serves git smart-HTTP, runs each
// request through the processor chain (internal/chain) and, when the chain
// allows it, forwards the request upstream to the embedded git host via
// net/http/httputil.ReverseProxy. Blocked or errored actions are answered with a
// git protocol error packet instead of being forwarded.
//
// It listens on its own ports (GIT_PROXY_SERVER_PORT / GIT_PROXY_HTTPS_SERVER_PORT),
// separate from the management/UI server in internal/proxy/http.
package proxygit

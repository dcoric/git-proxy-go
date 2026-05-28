// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Command git-proxy is the git-proxy-go entrypoint: an HTTP/SSH git proxy that
// enforces push policies through a processor chain.
//
// This is a skeleton — see GO-REWRITE-TASKS.md (milestone P1). It currently
// serves the management HTTP router (healthcheck + middleware); routes, the git
// transport and SSH land in later phases.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/dcoric/git-proxy-go/internal/config"
	proxyhttp "github.com/dcoric/git-proxy-go/internal/proxy/http"
)

// version is overridden at build time via -ldflags (see goreleaser, task X-1).
var version = "0.0.0-dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := config.Load(); err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	const addr = ":8080"
	slog.Info("git-proxy-go starting (skeleton)", "version", version, "addr", addr)

	srv := &http.Server{Addr: addr, Handler: proxyhttp.NewRouter(proxyhttp.Options{})}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

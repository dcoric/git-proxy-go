// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Command git-proxy is the git-proxy-go entrypoint: an HTTP/SSH git proxy that
// enforces push policies through a processor chain.
//
// As of P1 it loads proxy.config.json, serves the management HTTP router
// (healthcheck + middleware) and, when tls.enabled is set, an HTTPS listener
// too, with graceful shutdown. The full route set, git transport and SSH land
// in later phases (see GO-REWRITE-TASKS.md).
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dcoric/git-proxy-go/internal/config"
	proxyhttp "github.com/dcoric/git-proxy-go/internal/proxy/http"
)

// version is overridden at build time via -ldflags (see goreleaser, task X-1).
var version = "0.0.0-dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("git-proxy-go exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	env := config.LoadServerEnv()

	router := proxyhttp.NewRouter(proxyhttp.Options{
		AllowedOrigins: parseAllowedOrigins(os.Getenv("ALLOWED_ORIGINS")),
		RateLimitRPS:   cfg.RateLimitRPS(),
		RateLimitBurst: cfg.RateLimitBurst(),
	})

	listeners := proxyhttp.Listeners{HTTPAddr: net.JoinHostPort("", env.UIPort)}
	if cfg.TLSEnabled() {
		listeners.HTTPSAddr = net.JoinHostPort("", env.HTTPSUIPort)
		listeners.CertFile = cfg.TLSCertPath()
		listeners.KeyFile = cfg.TLSKeyPath()
	}

	servers, err := proxyhttp.NewServers(listeners, router)
	if err != nil {
		return err
	}

	slog.Info("git-proxy-go starting",
		"version", version,
		"config", configSource(cfg),
		"tls", cfg.TLSEnabled(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return servers.Serve(ctx)
}

func configSource(cfg *config.Config) string {
	if cfg.Source == "" {
		return "embedded defaults"
	}
	return cfg.Source
}

// parseAllowedOrigins mirrors getAllowedOrigins in src/service/index.ts: "*"
// reflects any origin, a comma-separated list pins those origins, and an empty
// value leaves the router on its default.
func parseAllowedOrigins(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if v == "*" {
		return []string{"*"}
	}
	var origins []string
	for _, o := range strings.Split(v, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

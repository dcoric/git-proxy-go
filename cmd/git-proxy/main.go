// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Command git-proxy is the git-proxy-go entrypoint: an HTTP/SSH git proxy that
// enforces push policies through a processor chain.
//
// As of P3 it loads proxy.config.json and serves the management HTTP router
// (healthcheck, middleware, and — when GIT_PROXY_DB_DSN is set — the Postgres-
// backed session + /api/auth routes), with HTTPS and graceful shutdown. The git
// transport, the remaining route groups and SSH land in later phases.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/db/migrations"
	"github.com/dcoric/git-proxy-go/internal/db/postgres"
	proxyhttp "github.com/dcoric/git-proxy-go/internal/proxy/http"
	"github.com/dcoric/git-proxy-go/internal/session"
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
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	env := config.LoadServerEnv()

	opts := proxyhttp.Options{
		AllowedOrigins:  parseAllowedOrigins(os.Getenv("ALLOWED_ORIGINS")),
		RateLimitRPS:    cfg.RateLimitRPS(),
		RateLimitBurst:  cfg.RateLimitBurst(),
		CSRFProtection:  cfg.CSRFProtection != nil && *cfg.CSRFProtection,
		OIDCRedirectURL: fmt.Sprintf("%s:%s/dashboard/profile", env.UIHost, env.UIPort),
	}

	// Wire the Postgres-backed session + auth routes when a DSN is configured.
	// Without it the binary still boots and serves the healthcheck (P1 behaviour).
	if dsn := os.Getenv("GIT_PROXY_DB_DSN"); dsn != "" {
		store, err := setupAuth(ctx, cfg, dsn, &opts)
		if err != nil {
			return err
		}
		defer store.Close()
	}

	router := proxyhttp.NewRouter(opts)

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
		"auth", opts.Store != nil,
	)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return servers.Serve(sigCtx)
}

// setupAuth connects the store, applies migrations, and populates the session +
// auth wiring on opts. It returns the store so the caller can close it.
func setupAuth(ctx context.Context, cfg *config.Config, dsn string, opts *proxyhttp.Options) (*postgres.Store, error) {
	if err := migrations.Up(ctx, dsn); err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	store, err := postgres.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}

	registry := auth.BuildRegistry(cfg, store)
	if slices.Contains(registry.EnabledTypes(), "local") {
		if err := auth.CreateDefaultAdmin(ctx, store); err != nil {
			store.Close()
			return nil, fmt.Errorf("seed default users: %w", err)
		}
	}

	// Construct the OIDC strategy here (not in BuildRegistry) so provider
	// discovery runs at startup and configuration errors fail fast.
	if oidcConfig := auth.EnabledOIDCConfig(cfg); oidcConfig != nil {
		strategy, err := auth.NewOIDCStrategy(ctx, oidcConfig, store)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("configure OIDC: %w", err)
		}
		registry.EnableOIDC(strategy)
	}

	sqlDB := stdlib.OpenDBFromPool(store.Pool())
	sessions := session.New(sqlDB, sessionLifetime(cfg), cfg.TLSEnabled())

	opts.Store = store
	opts.Sessions = sessions
	opts.Auth = registry
	return store, nil
}

// sessionLifetime converts sessionMaxAgeHours into a duration (0 lets scs use
// its default).
func sessionLifetime(cfg *config.Config) time.Duration {
	if cfg.SessionMaxAgeHours == nil || *cfg.SessionMaxAgeHours <= 0 {
		return 0
	}
	return time.Duration(*cfg.SessionMaxAgeHours * float64(time.Hour))
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

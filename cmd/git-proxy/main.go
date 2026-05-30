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
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/chain"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/db"
	"github.com/dcoric/git-proxy-go/internal/db/migrations"
	"github.com/dcoric/git-proxy-go/internal/db/postgres"
	"github.com/dcoric/git-proxy-go/internal/giturl"
	proxygit "github.com/dcoric/git-proxy-go/internal/proxy/git"
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
		Config:          cfg,
		UIPort:          env.UIPort,
		GitProxyPort:    env.GitServerPort,
	}

	// Wire the Postgres-backed session + auth routes, and the git transport
	// proxy, when a DSN is configured. Without it the binary still boots and
	// serves the management healthcheck (P1 behaviour); the git proxy needs the
	// store (for the chain's repo lookups and audit) so it is gated likewise.
	var gitHandler http.Handler
	if dsn := os.Getenv("GIT_PROXY_DB_DSN"); dsn != "" {
		store, err := setupAuth(ctx, cfg, dsn, &opts)
		if err != nil {
			return err
		}
		defer store.Close()

		gitHandler, err = setupGitProxy(ctx, cfg, store)
		if err != nil {
			return err
		}
	}

	router := proxyhttp.NewRouter(opts)

	mgmt := proxyhttp.Listeners{HTTPAddr: net.JoinHostPort("", env.UIPort)}
	if cfg.TLSEnabled() {
		mgmt.HTTPSAddr = net.JoinHostPort("", env.HTTPSUIPort)
		mgmt.CertFile = cfg.TLSCertPath()
		mgmt.KeyFile = cfg.TLSKeyPath()
	}
	mgmtServers, err := proxyhttp.NewServers(mgmt, router)
	if err != nil {
		return err
	}
	servers := []*proxyhttp.Servers{mgmtServers}

	if gitHandler != nil {
		git := proxyhttp.Listeners{HTTPAddr: net.JoinHostPort("", env.GitServerPort)}
		if cfg.TLSEnabled() {
			git.HTTPSAddr = net.JoinHostPort("", env.HTTPSGitServerPort)
			git.CertFile = cfg.TLSCertPath()
			git.KeyFile = cfg.TLSKeyPath()
		}
		gitServers, err := proxyhttp.NewServers(git, gitHandler)
		if err != nil {
			return err
		}
		servers = append(servers, gitServers)
	}

	slog.Info("git-proxy-go starting",
		"version", version,
		"config", configSource(cfg),
		"tls", cfg.TLSEnabled(),
		"auth", opts.Store != nil,
		"git_proxy", gitHandler != nil,
	)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveAll(sigCtx, servers...)
}

// serveAll runs every server until ctx is cancelled (or one fails), returning
// the first non-shutdown error. Each Servers.Serve shuts itself down on ctx
// cancellation.
func serveAll(ctx context.Context, servers ...*proxyhttp.Servers) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(servers))
	for _, s := range servers {
		wg.Add(1)
		go func(s *proxyhttp.Servers) {
			defer wg.Done()
			errCh <- s.Serve(ctx)
		}(s)
	}
	wg.Wait()
	close(errCh)

	var first error
	for err := range errCh {
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// setupGitProxy seeds the authorised repos and builds the git transport proxy
// handler over the chain engine. Mirrors the Node proxyPreparations + getRouter.
func setupGitProxy(ctx context.Context, cfg *config.Config, store *postgres.Store) (http.Handler, error) {
	if err := seedAuthorisedRepos(ctx, cfg, store); err != nil {
		return nil, fmt.Errorf("seed authorised repos: %w", err)
	}
	origins, err := proxiedHosts(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("list proxied hosts: %w", err)
	}
	return proxygit.NewHandler(chain.NewEngine(store, cfg), origins), nil
}

// seedAuthorisedRepos ensures every repo in the config's authorisedList exists
// in the store, granting admin push/authorise rights on newly created ones
// (mirrors the default-repo seeding in proxyPreparations).
func seedAuthorisedRepos(ctx context.Context, cfg *config.Config, store *postgres.Store) error {
	repos, err := store.GetRepos(ctx, db.RepoQuery{})
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(repos))
	for _, r := range repos {
		existing[r.URL] = true
	}
	for _, ar := range cfg.AuthorisedList {
		if existing[ar.URL] {
			continue
		}
		created, err := store.CreateRepo(ctx, &db.Repo{Project: ar.Project, Name: ar.Name, URL: ar.URL})
		if err != nil {
			return err
		}
		if err := store.AddUserCanPush(ctx, created.ID, "admin"); err != nil {
			return err
		}
		if err := store.AddUserCanAuthorise(ctx, created.ID, "admin"); err != nil {
			return err
		}
	}
	return nil
}

// proxiedHosts returns the distinct upstream hosts of the store's repos — the
// origins the git proxy recognises (Go port of getAllProxiedHosts).
func proxiedHosts(ctx context.Context, store *postgres.Store) ([]string, error) {
	repos, err := store.GetRepos(ctx, db.RepoQuery{})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(repos))
	hosts := make([]string, 0, len(repos))
	for _, r := range repos {
		if b := giturl.ProcessGitURL(r.URL); b != nil && !seen[b.Host] {
			seen[b.Host] = true
			hosts = append(hosts, b.Host)
		}
	}
	return hosts, nil
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

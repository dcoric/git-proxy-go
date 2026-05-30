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
	"github.com/dcoric/git-proxy-go/internal/db/sqlite"
	"github.com/dcoric/git-proxy-go/internal/giturl"
	proxygit "github.com/dcoric/git-proxy-go/internal/proxy/git"
	proxyhttp "github.com/dcoric/git-proxy-go/internal/proxy/http"
	sshproxy "github.com/dcoric/git-proxy-go/internal/proxy/ssh"
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
	var sshServer *sshproxy.Server
	if dsn := os.Getenv("GIT_PROXY_DB_DSN"); dsn != "" {
		store, err := setupAuth(ctx, cfg, dsn, &opts)
		if err != nil {
			return err
		}
		defer store.Close()

		var engine *chain.Engine
		gitHandler, engine, err = setupGitProxy(ctx, cfg, env, store)
		if err != nil {
			return err
		}

		sshServer, err = setupSSHServer(env, store, engine)
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
	servers := []listenServer{mgmtServers}

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
	if sshServer != nil {
		servers = append(servers, sshServer)
	}

	slog.Info("git-proxy-go starting",
		"version", version,
		"config", configSource(cfg),
		"tls", cfg.TLSEnabled(),
		"auth", opts.Store != nil,
		"git_proxy", gitHandler != nil,
		"ssh", sshServer != nil,
	)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveAll(sigCtx, servers...)
}

// listenServer is a long-running listener that serves until ctx is cancelled
// (the management/git HTTP servers and the SSH server).
type listenServer interface {
	Serve(ctx context.Context) error
}

// serveAll runs every server until ctx is cancelled (or one fails), returning
// the first non-shutdown error. Each server shuts itself down on ctx cancellation.
func serveAll(ctx context.Context, servers ...listenServer) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(servers))
	for _, s := range servers {
		wg.Add(1)
		go func(s listenServer) {
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

// setupSSHServer builds the git-over-SSH server when enabled, loading/generating
// the host key. Returns nil when SSH is disabled.
func setupSSHServer(env config.ServerEnv, store db.Store, engine *chain.Engine) (*sshproxy.Server, error) {
	if !env.SSHEnabled {
		return nil, nil
	}
	hostKey, err := sshproxy.EnsureHostKey(env.SSHHostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh host key: %w", err)
	}
	// Route parsed git commands through the same chain as the HTTP proxy (P5-2),
	// forwarding approved traffic upstream over SSH with host-key verification
	// against the known hosts (P5-3).
	knownHosts := sshproxy.NewKnownHosts(env.SSHKnownHosts)
	upstream := sshproxy.NewSSHUpstream(knownHosts.Callback())
	handler := sshproxy.NewProxyHandler(engine, upstream, knownHosts.Callback())
	return sshproxy.NewServer(net.JoinHostPort("", env.SSHPort), hostKey, store, handler), nil
}

// setupGitProxy seeds the authorised repos and builds the git transport proxy
// handler over the chain engine. Mirrors the Node proxyPreparations + getRouter.
// It returns the engine too so the SSH server can route into the same chain.
func setupGitProxy(ctx context.Context, cfg *config.Config, env config.ServerEnv, store db.Store) (http.Handler, *chain.Engine, error) {
	if err := seedAuthorisedRepos(ctx, cfg, store); err != nil {
		return nil, nil, fmt.Errorf("seed authorised repos: %w", err)
	}
	origins, err := proxiedHosts(ctx, store)
	if err != nil {
		return nil, nil, fmt.Errorf("list proxied hosts: %w", err)
	}
	engine := chain.NewEngine(store, cfg, env.UIPort, env.GitServerPort)
	return proxygit.NewHandler(engine, origins), engine, nil
}

// seedAuthorisedRepos ensures every repo in the config's authorisedList exists
// in the store, granting admin push/authorise rights on newly created ones
// (mirrors the default-repo seeding in proxyPreparations).
func seedAuthorisedRepos(ctx context.Context, cfg *config.Config, store db.Store) error {
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
func proxiedHosts(ctx context.Context, store db.Store) ([]string, error) {
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

// setupAuth opens the store (Postgres or SQLite, by DSN scheme), wires the
// session manager, and populates the auth wiring on opts. It returns the store
// so the caller can close it.
func setupAuth(ctx context.Context, cfg *config.Config, dsn string, opts *proxyhttp.Options) (db.Store, error) {
	store, err := openStore(ctx, cfg, dsn, opts)
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

	opts.Store = store
	opts.Auth = registry
	return store, nil
}

// openStore opens the configured backend and sets opts.Sessions to a matching
// session store. Postgres is selected by a postgres:// DSN (and runs the goose
// migrations); SQLite by a sqlite: DSN (the dev backend, schema applied on
// connect).
func openStore(ctx context.Context, cfg *config.Config, dsn string, opts *proxyhttp.Options) (db.Store, error) {
	switch {
	case isPostgresDSN(dsn):
		if err := migrations.Up(ctx, dsn); err != nil {
			return nil, fmt.Errorf("apply migrations: %w", err)
		}
		store, err := postgres.Connect(ctx, dsn)
		if err != nil {
			return nil, err
		}
		opts.Sessions = session.New(stdlib.OpenDBFromPool(store.Pool()), sessionLifetime(cfg), cfg.TLSEnabled())
		return store, nil
	case isSQLiteDSN(dsn):
		store, err := sqlite.Connect(ctx, strings.TrimPrefix(dsn, "sqlite:"))
		if err != nil {
			return nil, err
		}
		opts.Sessions = session.NewSQLite(store.DB(), sessionLifetime(cfg), cfg.TLSEnabled())
		return store, nil
	default:
		return nil, fmt.Errorf("unsupported GIT_PROXY_DB_DSN: use a postgres:// or sqlite: DSN")
	}
}

func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

func isSQLiteDSN(dsn string) bool { return strings.HasPrefix(dsn, "sqlite:") }

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

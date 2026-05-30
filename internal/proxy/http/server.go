// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package proxyhttp builds the git-proxy-go management HTTP server: the chi
// router, the middleware chain (request logging, CORS, security headers, rate
// limiting) and the management routes the React UI consumes.
//
// Tasks: P1-1 (router), P1-4 (healthcheck), P1-5 (slog), P1-6 (CORS),
// P1-7 (security headers), P1-8 (rate limit). Config wiring is P1-3; the full
// route set + git transport land in P4. CSRF + sessions are P3.
package proxyhttp

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/cors"
	"github.com/unrolled/secure"
	"golang.org/x/time/rate"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// Options configures the server. Values are defaulted to mirror the Node
// middleware setup; they are sourced from config in the entrypoint.
type Options struct {
	// AllowedOrigins for CORS. The sentinel ["*"] reflects the request origin
	// (so credentials still work), mirroring the Node dev default ALLOWED_ORIGINS=*.
	AllowedOrigins []string
	// RateLimitRPS / RateLimitBurst configure a global token-bucket limiter.
	// TODO(parity): the Node express-rate-limit is per-IP; make this per-client.
	RateLimitRPS   float64
	RateLimitBurst int

	// Sessions, Store and Auth wire the management auth routes (/api/auth, P3).
	// When all three are set, NewRouter adds the session middleware and mounts
	// the auth routes; otherwise the router serves only the healthcheck +
	// middleware (the P1 behaviour).
	Sessions *scs.SessionManager
	Store    db.Store
	Auth     *auth.Registry
	// CSRFProtection enables the csrf-cookie + X-CSRF-TOKEN double-submit
	// middleware, gated on the csrfProtection config flag.
	CSRFProtection bool
	// OIDCRedirectURL is where a successful OIDC login redirects the browser
	// (the UI profile page, ${GIT_PROXY_UI_HOST}:${GIT_PROXY_UI_PORT}/dashboard/profile).
	// Only used when the registry has an OIDC strategy enabled.
	OIDCRedirectURL string

	// Config backs the /api/v1/config routes and the API JWT middleware (P4-4).
	Config *config.Config
	// UIPort / GitProxyPort are the management and git-transport listener ports,
	// used to build each repo's proxyURL in the /api/v1/repo responses.
	UIPort       string
	GitProxyPort string
}

// authReady reports whether the auth dependencies are all wired.
func (o Options) authReady() bool {
	return o.Sessions != nil && o.Store != nil && o.Auth != nil
}

func (o Options) withDefaults() Options {
	if len(o.AllowedOrigins) == 0 {
		o.AllowedOrigins = []string{"*"}
	}
	if o.RateLimitRPS == 0 {
		o.RateLimitRPS = 50
	}
	if o.RateLimitBurst == 0 {
		o.RateLimitBurst = 300
	}
	return o
}

// NewRouter builds the management HTTP handler with the full middleware chain.
func NewRouter(opts Options) http.Handler {
	opts = opts.withDefaults()

	r := chi.NewRouter()
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(opts))
	r.Use(rateLimit(opts))
	r.Use(securityHeaders())

	// Session then CSRF, mirroring the Node order (session → lusca → routes).
	if opts.Sessions != nil {
		r.Use(opts.Sessions.LoadAndSave)
	}
	if opts.CSRFProtection {
		r.Use(CSRFProtection())
	}

	// P1-4: liveness, matching the Node contract (GET → {"message":"ok"}).
	r.Get("/api/v1/healthcheck", healthcheck)

	// P3: management auth routes, mounted once the store/session/registry are wired.
	if opts.authReady() {
		(&authHandler{
			store:           opts.Store,
			sessions:        opts.Sessions,
			registry:        opts.Auth,
			oidcRedirectURL: opts.OIDCRedirectURL,
		}).mount(r)

		// P4-4: management API route groups (push/repo/users/config/home).
		apiCfg := opts.Config
		if apiCfg == nil {
			apiCfg = &config.Config{}
		}
		(&apiHandler{
			store:        opts.Store,
			sessions:     opts.Sessions,
			cfg:          apiCfg,
			uiPort:       opts.UIPort,
			gitProxyPort: opts.GitProxyPort,
		}).mount(r)
	}

	return r
}

func healthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"ok"}`))
}

// requestLogger logs one structured line per request (P1-5).
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

// corsMiddleware mirrors the Node CORS config: credentials on, X-CSRF-TOKEN
// allowed, Set-Cookie exposed (P1-6).
func corsMiddleware(opts Options) func(http.Handler) http.Handler {
	c := cors.Options{
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Requested-With", "X-CSRF-TOKEN"},
		ExposedHeaders:   []string{"Set-Cookie"},
		AllowCredentials: true,
		MaxAge:           86400,
	}
	if len(opts.AllowedOrigins) == 1 && opts.AllowedOrigins[0] == "*" {
		// Reflect the origin so credentials remain valid (wildcard + credentials
		// is rejected by browsers).
		c.AllowOriginFunc = func(string) bool { return true }
	} else {
		c.AllowedOrigins = opts.AllowedOrigins
	}
	return cors.New(c).Handler
}

// securityHeaders mirrors the Node lusca headers: HSTS, nosniff, referrer-policy
// and X-Frame-Options SAMEORIGIN (P1-7).
func securityHeaders() func(http.Handler) http.Handler {
	s := secure.New(secure.Options{
		STSSeconds:              31536000,
		STSIncludeSubdomains:    true,
		STSPreload:              true,
		ContentTypeNosniff:      true,
		ReferrerPolicy:          "same-origin",
		CustomFrameOptionsValue: "SAMEORIGIN",
	})
	return s.Handler
}

// rateLimit applies a global token-bucket limiter (P1-8).
func rateLimit(opts Options) func(http.Handler) http.Handler {
	lim := rate.NewLimiter(rate.Limit(opts.RateLimitRPS), opts.RateLimitBurst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !lim.Allow() {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

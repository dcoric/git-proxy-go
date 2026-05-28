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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/cors"
	"github.com/unrolled/secure"
	"golang.org/x/time/rate"
)

// Options configures the server. Values are defaulted to mirror the Node
// middleware setup; they will be sourced from config once P1-3 lands.
type Options struct {
	// AllowedOrigins for CORS. The sentinel ["*"] reflects the request origin
	// (so credentials still work), mirroring the Node dev default ALLOWED_ORIGINS=*.
	AllowedOrigins []string
	// RateLimitRPS / RateLimitBurst configure a global token-bucket limiter.
	// TODO(parity): the Node express-rate-limit is per-IP; make this per-client.
	RateLimitRPS   float64
	RateLimitBurst int
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

	// P1-4: liveness, matching the Node contract (GET → {"message":"ok"}).
	r.Get("/api/v1/healthcheck", healthcheck)

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

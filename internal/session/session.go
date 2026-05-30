// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package session configures the HTTP session manager (P3-1): alexedwards/scs
// backed by the Postgres `sessions` table. It replaces express-session +
// connect-mongo. Session cookies are NOT byte-identical to the Node ones (scs
// uses random tokens, not signed ids) — only the behaviour matches: httpOnly,
// a configurable Secure flag, and a lifetime from sessionMaxAgeHours.
package session

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/alexedwards/scs/postgresstore"
	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
)

// New builds a session manager storing sessions in Postgres via sqlDB. The
// store schema is created by the goose migrations (the `sessions` table).
// secureCookie should be true when served over HTTPS/behind a TLS-terminating
// proxy (the equivalent of express-session's secure:"auto" + trust proxy).
func New(sqlDB *sql.DB, lifetime time.Duration, secureCookie bool) *scs.SessionManager {
	m := newManager(lifetime, secureCookie)
	m.Store = postgresstore.New(sqlDB)
	return m
}

// NewSQLite builds a session manager storing sessions in SQLite via sqlDB (the
// dev backend). The `sessions` table is created by the SQLite store's schema.
func NewSQLite(sqlDB *sql.DB, lifetime time.Duration, secureCookie bool) *scs.SessionManager {
	m := newManager(lifetime, secureCookie)
	m.Store = sqlite3store.New(sqlDB)
	return m
}

func newManager(lifetime time.Duration, secureCookie bool) *scs.SessionManager {
	m := scs.New()
	if lifetime > 0 {
		m.Lifetime = lifetime
	}
	m.Cookie.HttpOnly = true
	m.Cookie.SameSite = http.SameSiteLaxMode
	m.Cookie.Secure = secureCookie
	m.Cookie.Path = "/"
	return m
}

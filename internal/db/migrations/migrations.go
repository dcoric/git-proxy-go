// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package migrations embeds the goose SQL migrations and applies them. The .sql
// files are the single schema source: they are run at startup/test setup here
// and parsed by sqlc to generate the typed queries (see sqlc.yaml).
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Up applies all pending migrations against the database at dsn. It opens its
// own short-lived database/sql connection (goose needs a *sql.DB) and closes it
// before returning, leaving the caller's pgx pool untouched.
func Up(ctx context.Context, dsn string) error {
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

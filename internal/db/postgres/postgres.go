// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package postgres holds the pgx-backed implementation of the db.Store
// interface.
//
// Skeleton stub:
//   - TODO(P2-1): define the db.Store interface (users, repos, pushes, sessions).
//   - TODO(P2-2): goose migrations.
//   - TODO(P2-3): sqlc-generated queries over this pool.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open returns a pgx connection pool for the given DSN. Skeleton (P2-3).
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}

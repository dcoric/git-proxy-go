// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package migrate

import (
	"context"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// Source reads the three collections (users, repos, pushes) from a legacy
// git-proxy datastore — a neDB directory (NeDBSource) or a MongoDB database
// (MongoSource).
type Source interface {
	// Kind identifies the source ("nedb" or "mongo") for logging.
	Kind() string
	Users(ctx context.Context) ([]*db.User, error)
	Repos(ctx context.Context) ([]*db.Repo, error)
	Pushes(ctx context.Context) ([]*db.Push, error)
	Close(ctx context.Context) error
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package db

import "context"

// PushQuery filters getPushes. Each non-nil field adds an equality constraint;
// nil means "don't filter on this field" (Partial<PushQuery> in the Node API).
// The Node default query is {error:false, blocked:true, allowPush:false,
// authorised:false, type:"push"}; callers build that explicitly via
// DefaultPushQuery.
type PushQuery struct {
	Type       *string
	Error      *bool
	Blocked    *bool
	AllowPush  *bool
	Authorised *bool
	Canceled   *bool
	Rejected   *bool
}

// DefaultPushQuery is the default getPushes filter used by the UI's review
// list, mirroring defaultPushQuery in src/db/mongo/pushes.ts.
func DefaultPushQuery() PushQuery {
	f, tt, push := false, true, "push"
	return PushQuery{Type: &push, Error: &f, Blocked: &tt, AllowPush: &f, Authorised: &f}
}

// RepoQuery filters getRepos. Non-nil fields add equality constraints.
type RepoQuery struct {
	Name    *string
	URL     *string
	Project *string
}

// UserQuery filters getUsers. Non-nil fields add equality constraints
// (username/email are lower-cased to match the Node behaviour).
type UserQuery struct {
	Username *string
	Email    *string
}

// Store is the persistence contract for git-proxy-go: the Go port of the Sink
// interface (src/db/types.ts). Find/Get lookups return (nil, nil) when the
// record is absent, mirroring the Node "return null" convention; Authorise,
// Reject and Cancel return an error when the push does not exist.
//
// Session storage is intentionally absent: the Node getSessionStore returns an
// express-session/connect-mongo store, whereas Go uses alexedwards/scs over the
// `sessions` table (created by the migrations) — wired up in P3-1.
type Store interface {
	// Pushes / audit. WriteAudit upserts a push by id; the pushes table is the
	// audit trail (mirrors writeAudit in the Node sink).
	GetPushes(ctx context.Context, q PushQuery) ([]*Push, error)
	WriteAudit(ctx context.Context, p *Push) error
	GetPush(ctx context.Context, id string) (*Push, error)
	DeletePush(ctx context.Context, id string) error
	Authorise(ctx context.Context, id string, attestation *Attestation) (string, error)
	Cancel(ctx context.Context, id string) (string, error)
	Reject(ctx context.Context, id string, rejection Rejection) (string, error)

	// Repos.
	GetRepos(ctx context.Context, q RepoQuery) ([]*Repo, error)
	GetRepo(ctx context.Context, name string) (*Repo, error)
	GetRepoByURL(ctx context.Context, url string) (*Repo, error)
	GetRepoByID(ctx context.Context, id string) (*Repo, error)
	CreateRepo(ctx context.Context, repo *Repo) (*Repo, error)
	AddUserCanPush(ctx context.Context, id, user string) error
	AddUserCanAuthorise(ctx context.Context, id, user string) error
	RemoveUserCanPush(ctx context.Context, id, user string) error
	RemoveUserCanAuthorise(ctx context.Context, id, user string) error
	DeleteRepo(ctx context.Context, id string) error

	// Users.
	FindUser(ctx context.Context, username string) (*User, error)
	FindUserByEmail(ctx context.Context, email string) (*User, error)
	FindUserByOIDC(ctx context.Context, oidcID string) (*User, error)
	FindUserBySSHKey(ctx context.Context, key string) (*User, error)
	GetUsers(ctx context.Context, q UserQuery) ([]*User, error)
	CreateUser(ctx context.Context, user *User) error
	DeleteUser(ctx context.Context, username string) error
	UpdateUser(ctx context.Context, user *User) error

	// Close releases the underlying connection pool.
	Close()
}

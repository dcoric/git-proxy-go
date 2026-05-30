// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dcoric/git-proxy-go/internal/db"
	"github.com/dcoric/git-proxy-go/internal/db/postgres/sqlc"
)

// ---- Pushes / audit -------------------------------------------------------

// WriteAudit upserts the push by id, mirroring the Node writeAudit. The full
// Push is stored as JSONB; the scalar columns are promoted copies for querying.
func (s *Store) WriteAudit(ctx context.Context, p *db.Push) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal push: %w", err)
	}
	if p.ID == "" {
		return errors.New("invalid id")
	}
	return s.q.WriteAudit(ctx, sqlc.WriteAuditParams{
		ID:           p.ID,
		Type:         p.Type,
		Method:       p.Method,
		Timestamp:    p.Timestamp,
		Project:      p.Project,
		RepoName:     p.RepoName,
		Url:          p.URL,
		Repo:         p.Repo,
		Branch:       p.Branch,
		Error:        p.Error,
		Blocked:      p.Blocked,
		AllowPush:    p.AllowPush,
		Authorised:   p.Authorised,
		Canceled:     p.Canceled,
		Rejected:     p.Rejected,
		AutoApproved: p.AutoApproved,
		AutoRejected: p.AutoRejected,
		CommitFrom:   p.CommitFrom,
		CommitTo:     p.CommitTo,
		PushUser:     p.User,
		Data:         data,
	})
}

// GetPush returns the push by id, or (nil, nil) if absent (Node returns null).
func (s *Store) GetPush(ctx context.Context, id string) (*db.Push, error) {
	data, err := s.q.GetPush(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return unmarshalPush(data)
}

// GetPushes returns pushes matching the query, newest first.
func (s *Store) GetPushes(ctx context.Context, q db.PushQuery) ([]*db.Push, error) {
	rows, err := s.q.GetPushes(ctx, sqlc.GetPushesParams{
		Type:       q.Type,
		Error:      q.Error,
		Blocked:    q.Blocked,
		AllowPush:  q.AllowPush,
		Authorised: q.Authorised,
		Canceled:   q.Canceled,
		Rejected:   q.Rejected,
	})
	if err != nil {
		return nil, err
	}
	pushes := make([]*db.Push, 0, len(rows))
	for _, data := range rows {
		p, err := unmarshalPush(data)
		if err != nil {
			return nil, err
		}
		pushes = append(pushes, p)
	}
	return pushes, nil
}

// DeletePush removes a push by id.
func (s *Store) DeletePush(ctx context.Context, id string) error {
	return s.q.DeletePush(ctx, id)
}

// Authorise marks a push authorised, mirroring the Node authorise mutation.
func (s *Store) Authorise(ctx context.Context, id string, attestation *db.Attestation) (string, error) {
	p, err := s.requirePush(ctx, id)
	if err != nil {
		return "", err
	}
	p.Authorised = true
	p.Canceled = false
	p.Rejected = false
	p.Attestation = attestation
	if err := s.WriteAudit(ctx, p); err != nil {
		return "", err
	}
	return fmt.Sprintf("authorised %s", id), nil
}

// Reject marks a push rejected.
func (s *Store) Reject(ctx context.Context, id string, rejection db.Rejection) (string, error) {
	p, err := s.requirePush(ctx, id)
	if err != nil {
		return "", err
	}
	p.Authorised = false
	p.Canceled = false
	p.Rejected = true
	r := rejection
	p.Rejection = &r
	if err := s.WriteAudit(ctx, p); err != nil {
		return "", err
	}
	return fmt.Sprintf("reject %s", id), nil
}

// Cancel marks a push canceled.
func (s *Store) Cancel(ctx context.Context, id string) (string, error) {
	p, err := s.requirePush(ctx, id)
	if err != nil {
		return "", err
	}
	p.Authorised = false
	p.Canceled = true
	p.Rejected = false
	if err := s.WriteAudit(ctx, p); err != nil {
		return "", err
	}
	return fmt.Sprintf("canceled %s", id), nil
}

func (s *Store) requirePush(ctx context.Context, id string) (*db.Push, error) {
	p, err := s.GetPush(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("push %s not found", id)
	}
	return p, nil
}

func unmarshalPush(data []byte) (*db.Push, error) {
	var p db.Push
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("unmarshal push: %w", err)
	}
	return &p, nil
}

// ---- Repos ----------------------------------------------------------------

// CreateRepo inserts a repo and returns it with its assigned id.
func (s *Store) CreateRepo(ctx context.Context, repo *db.Repo) (*db.Repo, error) {
	// The canPush/canAuthorise columns are NOT NULL; a new repo starts with
	// empty access lists (matching the Node default of empty arrays).
	row, err := s.q.CreateRepo(ctx, sqlc.CreateRepoParams{
		Project:      repo.Project,
		Name:         repo.Name,
		Url:          repo.URL,
		CanPush:      orEmpty(repo.Users.CanPush),
		CanAuthorise: orEmpty(repo.Users.CanAuthorise),
	})
	if err != nil {
		return nil, err
	}
	return repoFromRow(row), nil
}

// GetRepos returns repos matching the query, ordered by name.
func (s *Store) GetRepos(ctx context.Context, q db.RepoQuery) ([]*db.Repo, error) {
	rows, err := s.q.GetRepos(ctx, sqlc.GetReposParams{
		Name:    q.Name,
		Url:     q.URL,
		Project: q.Project,
	})
	if err != nil {
		return nil, err
	}
	repos := make([]*db.Repo, 0, len(rows))
	for i := range rows {
		repos = append(repos, repoFromRow(rows[i]))
	}
	return repos, nil
}

// GetRepo looks a repo up by (lower-cased) name.
func (s *Store) GetRepo(ctx context.Context, name string) (*db.Repo, error) {
	return s.oneRepo(s.q.GetRepo(ctx, name))
}

// GetRepoByURL looks a repo up by URL.
func (s *Store) GetRepoByURL(ctx context.Context, url string) (*db.Repo, error) {
	return s.oneRepo(s.q.GetRepoByUrl(ctx, url))
}

// GetRepoByID looks a repo up by id.
func (s *Store) GetRepoByID(ctx context.Context, id string) (*db.Repo, error) {
	return s.oneRepo(s.q.GetRepoById(ctx, id))
}

func (s *Store) oneRepo(row sqlc.Repo, err error) (*db.Repo, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return repoFromRow(row), nil
}

// AddUserCanPush adds a user to a repo's push list (idempotent).
func (s *Store) AddUserCanPush(ctx context.Context, id, user string) error {
	_, err := s.q.AddUserCanPush(ctx, sqlc.AddUserCanPushParams{ID: id, Member: user})
	return err
}

// AddUserCanAuthorise adds a user to a repo's authorise list (idempotent).
func (s *Store) AddUserCanAuthorise(ctx context.Context, id, user string) error {
	_, err := s.q.AddUserCanAuthorise(ctx, sqlc.AddUserCanAuthoriseParams{ID: id, Member: user})
	return err
}

// RemoveUserCanPush removes a user from a repo's push list.
func (s *Store) RemoveUserCanPush(ctx context.Context, id, user string) error {
	return s.q.RemoveUserCanPush(ctx, sqlc.RemoveUserCanPushParams{ID: id, Member: user})
}

// RemoveUserCanAuthorise removes a user from a repo's authorise list.
func (s *Store) RemoveUserCanAuthorise(ctx context.Context, id, user string) error {
	return s.q.RemoveUserCanAuthorise(ctx, sqlc.RemoveUserCanAuthoriseParams{ID: id, Member: user})
}

// DeleteRepo removes a repo by id.
func (s *Store) DeleteRepo(ctx context.Context, id string) error {
	return s.q.DeleteRepo(ctx, id)
}

func repoFromRow(r sqlc.Repo) *db.Repo {
	return &db.Repo{
		ID:      r.ID,
		Project: r.Project,
		Name:    r.Name,
		URL:     r.Url,
		Users:   db.RepoUsers{CanPush: r.CanPush, CanAuthorise: r.CanAuthorise},
	}
}

// ---- Users ----------------------------------------------------------------

// CreateUser inserts a user (username/email are lower-cased by the query).
func (s *Store) CreateUser(ctx context.Context, u *db.User) error {
	return s.q.CreateUser(ctx, sqlc.CreateUserParams{
		Username:    u.Username,
		Password:    u.Password,
		GitAccount:  u.GitAccount,
		Email:       u.Email,
		Admin:       u.Admin,
		OidcID:      u.OIDCID,
		DisplayName: u.DisplayName,
		Title:       u.Title,
	})
}

// FindUser looks a user up by (lower-cased) username.
func (s *Store) FindUser(ctx context.Context, username string) (*db.User, error) {
	return s.oneUser(s.q.FindUser(ctx, username))
}

// FindUserByEmail looks a user up by (lower-cased) email.
func (s *Store) FindUserByEmail(ctx context.Context, email string) (*db.User, error) {
	return s.oneUser(s.q.FindUserByEmail(ctx, email))
}

// FindUserByOIDC looks a user up by OIDC subject id.
func (s *Store) FindUserByOIDC(ctx context.Context, oidcID string) (*db.User, error) {
	return s.oneUser(s.q.FindUserByOIDC(ctx, &oidcID))
}

// GetUsers returns users matching the query. Passwords are stripped from the
// result, mirroring the Node getUsers projection.
func (s *Store) GetUsers(ctx context.Context, q db.UserQuery) ([]*db.User, error) {
	rows, err := s.q.GetUsers(ctx, sqlc.GetUsersParams{Username: q.Username, Email: q.Email})
	if err != nil {
		return nil, err
	}
	users := make([]*db.User, 0, len(rows))
	for i := range rows {
		u := userFromRow(rows[i])
		u.Password = nil
		users = append(users, u)
	}
	return users, nil
}

// DeleteUser removes a user by (lower-cased) username.
func (s *Store) DeleteUser(ctx context.Context, username string) error {
	return s.q.DeleteUser(ctx, username)
}

// UpdateUser writes the user's value fields and preserves nil pointer fields
// (password/oidcId/displayName/title), returning an error if the user does not
// exist. Unlike the Node updateUser this does not upsert; the OIDC first-login
// create path uses CreateUser and is wired in P3.
func (s *Store) UpdateUser(ctx context.Context, u *db.User) error {
	rows, err := s.q.UpdateUser(ctx, sqlc.UpdateUserParams{
		Username:    u.Username,
		Password:    u.Password,
		GitAccount:  &u.GitAccount,
		Email:       &u.Email,
		Admin:       &u.Admin,
		OidcID:      u.OIDCID,
		DisplayName: u.DisplayName,
		Title:       u.Title,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("user %s not found", u.Username)
	}
	return nil
}

func (s *Store) oneUser(row sqlc.User, err error) (*db.User, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return userFromRow(row), nil
}

func userFromRow(u sqlc.User) *db.User {
	return &db.User{
		Username:    u.Username,
		Password:    u.Password,
		GitAccount:  u.GitAccount,
		Email:       u.Email,
		Admin:       u.Admin,
		OIDCID:      u.OidcID,
		DisplayName: u.DisplayName,
		Title:       u.Title,
	}
}

// orEmpty returns s, or an empty (non-nil) slice when s is nil, so NOT NULL
// array columns are never sent a NULL.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

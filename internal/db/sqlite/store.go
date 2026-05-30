// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package sqlite is a SQLite-backed db.Store for local development and tests
// (D-4): it lets the proxy run without Postgres. It uses the pure-Go
// modernc.org/sqlite driver (no CGO), applies its schema on connect, and mirrors
// the Postgres store's semantics (lowercased usernames/emails, the push stored
// as a JSON blob with promoted scalar columns for filtering).
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/dcoric/git-proxy-go/internal/db"
)

//go:embed schema.sql
var schema string

// Store is the SQLite implementation of db.Store.
type Store struct {
	db *sql.DB
}

var _ db.Store = (*Store)(nil)

// Connect opens the SQLite database at dsn (a file path or a "file:" DSN),
// applies the schema, and returns the store. The connection pool is capped at
// one connection: SQLite is single-writer, and an in-memory ":memory:" DSN must
// reuse the same connection to retain its data.
func Connect(ctx context.Context, dsn string) (*Store, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dsn, err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, schema); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("apply sqlite schema: %w", err)
	}
	return &Store{db: sqlDB}, nil
}

// DB exposes the underlying *sql.DB for the scs session store.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() { _ = s.db.Close() }

// ---- Pushes / audit -------------------------------------------------------

// WriteAudit upserts the push by id; the full Push is stored as JSON in `data`
// and the scalar columns are promoted copies for querying.
func (s *Store) WriteAudit(ctx context.Context, p *db.Push) error {
	if p.ID == "" {
		return errors.New("invalid id")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal push: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO pushes (
  id, type, method, timestamp, project, repo_name, url, repo, branch,
  error, blocked, allow_push, authorised, canceled, rejected,
  auto_approved, auto_rejected, commit_from, commit_to, push_user, data
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  type=excluded.type, method=excluded.method, timestamp=excluded.timestamp,
  project=excluded.project, repo_name=excluded.repo_name, url=excluded.url,
  repo=excluded.repo, branch=excluded.branch, error=excluded.error,
  blocked=excluded.blocked, allow_push=excluded.allow_push,
  authorised=excluded.authorised, canceled=excluded.canceled,
  rejected=excluded.rejected, auto_approved=excluded.auto_approved,
  auto_rejected=excluded.auto_rejected, commit_from=excluded.commit_from,
  commit_to=excluded.commit_to, push_user=excluded.push_user, data=excluded.data`,
		p.ID, p.Type, p.Method, p.Timestamp, p.Project, p.RepoName, p.URL, p.Repo, p.Branch,
		boolToInt(p.Error), boolToInt(p.Blocked), boolToInt(p.AllowPush), boolToInt(p.Authorised),
		boolToInt(p.Canceled), boolToInt(p.Rejected), boolToInt(p.AutoApproved), boolToInt(p.AutoRejected),
		p.CommitFrom, p.CommitTo, p.User, string(data))
	return err
}

// GetPush returns the push by id, or (nil, nil) when absent.
func (s *Store) GetPush(ctx context.Context, id string) (*db.Push, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM pushes WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return unmarshalPush(data)
}

// GetPushes returns pushes matching the query, newest first. Each non-nil filter
// adds an equality constraint.
func (s *Store) GetPushes(ctx context.Context, q db.PushQuery) ([]*db.Push, error) {
	var conds []string
	var args []any
	if q.Type != nil {
		conds, args = append(conds, "type = ?"), append(args, *q.Type)
	}
	addBool := func(col string, v *bool) {
		if v != nil {
			conds, args = append(conds, col+" = ?"), append(args, boolToInt(*v))
		}
	}
	addBool("error", q.Error)
	addBool("blocked", q.Blocked)
	addBool("allow_push", q.AllowPush)
	addBool("authorised", q.Authorised)
	addBool("canceled", q.Canceled)
	addBool("rejected", q.Rejected)

	query := "SELECT data FROM pushes"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY timestamp DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	pushes := make([]*db.Push, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		p, err := unmarshalPush(data)
		if err != nil {
			return nil, err
		}
		pushes = append(pushes, p)
	}
	return pushes, rows.Err()
}

// DeletePush removes a push by id.
func (s *Store) DeletePush(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM pushes WHERE id = ?", id)
	return err
}

// Authorise marks a push authorised.
func (s *Store) Authorise(ctx context.Context, id string, attestation *db.Attestation) (string, error) {
	p, err := s.requirePush(ctx, id)
	if err != nil {
		return "", err
	}
	p.Authorised, p.Canceled, p.Rejected = true, false, false
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
	p.Authorised, p.Canceled, p.Rejected = false, false, true
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
	p.Authorised, p.Canceled, p.Rejected = false, true, false
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

func unmarshalPush(data string) (*db.Push, error) {
	var p db.Push
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, fmt.Errorf("unmarshal push: %w", err)
	}
	return &p, nil
}

// ---- Repos ----------------------------------------------------------------

// CreateRepo inserts a repo with a generated id and empty access lists.
func (s *Store) CreateRepo(ctx context.Context, repo *db.Repo) (*db.Repo, error) {
	id := newUUID()
	name := strings.ToLower(repo.Name)
	canPush := orEmpty(repo.Users.CanPush)
	canAuthorise := orEmpty(repo.Users.CanAuthorise)
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO repos (id, project, name, url, can_push, can_authorise) VALUES (?,?,?,?,?,?)",
		id, repo.Project, name, repo.URL, encodeStrList(canPush), encodeStrList(canAuthorise)); err != nil {
		return nil, err
	}
	return &db.Repo{ID: id, Project: repo.Project, Name: name, URL: repo.URL,
		Users: db.RepoUsers{CanPush: canPush, CanAuthorise: canAuthorise}}, nil
}

const repoColumns = "id, project, name, url, can_push, can_authorise"

// GetRepos returns repos matching the query, ordered by name.
func (s *Store) GetRepos(ctx context.Context, q db.RepoQuery) ([]*db.Repo, error) {
	var conds []string
	var args []any
	if q.Name != nil {
		conds, args = append(conds, "name = ?"), append(args, strings.ToLower(*q.Name))
	}
	if q.URL != nil {
		conds, args = append(conds, "url = ?"), append(args, *q.URL)
	}
	if q.Project != nil {
		conds, args = append(conds, "project = ?"), append(args, *q.Project)
	}
	query := "SELECT " + repoColumns + " FROM repos"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY name"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	repos := make([]*db.Repo, 0)
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// GetRepo looks a repo up by (lower-cased) name.
func (s *Store) GetRepo(ctx context.Context, name string) (*db.Repo, error) {
	return s.oneRepo(ctx, "name = ?", strings.ToLower(name))
}

// GetRepoByURL looks a repo up by URL.
func (s *Store) GetRepoByURL(ctx context.Context, url string) (*db.Repo, error) {
	return s.oneRepo(ctx, "url = ?", url)
}

// GetRepoByID looks a repo up by id.
func (s *Store) GetRepoByID(ctx context.Context, id string) (*db.Repo, error) {
	return s.oneRepo(ctx, "id = ?", id)
}

func (s *Store) oneRepo(ctx context.Context, where string, arg any) (*db.Repo, error) {
	r, err := scanRepo(s.db.QueryRowContext(ctx, "SELECT "+repoColumns+" FROM repos WHERE "+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// AddUserCanPush adds a user to a repo's push list (idempotent).
func (s *Store) AddUserCanPush(ctx context.Context, id, user string) error {
	return s.addToList(ctx, id, user, "can_push")
}

// AddUserCanAuthorise adds a user to a repo's authorise list (idempotent).
func (s *Store) AddUserCanAuthorise(ctx context.Context, id, user string) error {
	return s.addToList(ctx, id, user, "can_authorise")
}

// RemoveUserCanPush removes a user from a repo's push list.
func (s *Store) RemoveUserCanPush(ctx context.Context, id, user string) error {
	return s.removeFromList(ctx, id, user, "can_push")
}

// RemoveUserCanAuthorise removes a user from a repo's authorise list.
func (s *Store) RemoveUserCanAuthorise(ctx context.Context, id, user string) error {
	return s.removeFromList(ctx, id, user, "can_authorise")
}

// addToList appends a lower-cased member to a repo's access-list column if not
// already present (column is an internal constant, never user input).
func (s *Store) addToList(ctx context.Context, id, member, column string) error {
	member = strings.ToLower(member)
	return s.mutateList(ctx, id, column, func(list []string) ([]string, bool) {
		if contains(list, member) {
			return list, false
		}
		return append(list, member), true
	})
}

func (s *Store) removeFromList(ctx context.Context, id, member, column string) error {
	member = strings.ToLower(member)
	return s.mutateList(ctx, id, column, func(list []string) ([]string, bool) {
		out := list[:0]
		changed := false
		for _, m := range list {
			if m == member {
				changed = true
				continue
			}
			out = append(out, m)
		}
		return out, changed
	})
}

func (s *Store) mutateList(ctx context.Context, id, column string, fn func([]string) ([]string, bool)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var raw string
	err = tx.QueryRowContext(ctx, "SELECT "+column+" FROM repos WHERE id = ?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no matching repo: a no-op, mirroring the Postgres UPDATE
	}
	if err != nil {
		return err
	}
	list, changed := fn(decodeStrList(raw))
	if !changed {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE repos SET "+column+" = ? WHERE id = ?", encodeStrList(list), id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRepo removes a repo by id.
func (s *Store) DeleteRepo(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM repos WHERE id = ?", id)
	return err
}

func scanRepo(sc scanner) (*db.Repo, error) {
	var id, project, name, url, canPush, canAuthorise string
	if err := sc.Scan(&id, &project, &name, &url, &canPush, &canAuthorise); err != nil {
		return nil, err
	}
	return &db.Repo{
		ID: id, Project: project, Name: name, URL: url,
		Users: db.RepoUsers{CanPush: decodeStrList(canPush), CanAuthorise: decodeStrList(canAuthorise)},
	}, nil
}

// ---- Users ----------------------------------------------------------------

const userColumns = "username, password, git_account, email, admin, oidc_id, display_name, title, public_keys"

// CreateUser inserts a user (username/email lower-cased).
func (s *Store) CreateUser(ctx context.Context, u *db.User) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO users ("+userColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
		strings.ToLower(u.Username), ns(u.Password), u.GitAccount, strings.ToLower(u.Email),
		boolToInt(u.Admin), ns(u.OIDCID), ns(u.DisplayName), ns(u.Title), string(marshalPublicKeys(u.PublicKeys)))
	return err
}

// FindUserBySSHKey looks a user up by a registered SSH public key (the
// authorized_keys form), matching against the public_keys JSON array.
func (s *Store) FindUserBySSHKey(ctx context.Context, key string) (*db.User, error) {
	return s.oneUser(s.db.QueryRowContext(ctx,
		"SELECT "+userColumns+" FROM users WHERE EXISTS "+
			"(SELECT 1 FROM json_each(users.public_keys) WHERE json_extract(value, '$.key') = ?) LIMIT 1", key))
}

// FindUser looks a user up by (lower-cased) username.
func (s *Store) FindUser(ctx context.Context, username string) (*db.User, error) {
	return s.oneUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE username = ?", strings.ToLower(username)))
}

// FindUserByEmail looks a user up by (lower-cased) email.
func (s *Store) FindUserByEmail(ctx context.Context, email string) (*db.User, error) {
	return s.oneUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE email = ?", strings.ToLower(email)))
}

// FindUserByOIDC looks a user up by OIDC subject id.
func (s *Store) FindUserByOIDC(ctx context.Context, oidcID string) (*db.User, error) {
	return s.oneUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE oidc_id = ?", oidcID))
}

// GetUsers returns users matching the query (passwords stripped).
func (s *Store) GetUsers(ctx context.Context, q db.UserQuery) ([]*db.User, error) {
	var conds []string
	var args []any
	if q.Username != nil {
		conds, args = append(conds, "username = ?"), append(args, strings.ToLower(*q.Username))
	}
	if q.Email != nil {
		conds, args = append(conds, "email = ?"), append(args, strings.ToLower(*q.Email))
	}
	query := "SELECT " + userColumns + " FROM users"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY username"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	users := make([]*db.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		u.Password = nil
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteUser removes a user by (lower-cased) username.
func (s *Store) DeleteUser(ctx context.Context, username string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE username = ?", strings.ToLower(username))
	return err
}

// UpdateUser writes the user's value fields and preserves nil pointer fields
// (password/oidcId/displayName/title), erroring if the user does not exist.
func (s *Store) UpdateUser(ctx context.Context, u *db.User) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE users SET
  password     = COALESCE(?, password),
  git_account  = ?,
  email        = ?,
  admin        = ?,
  oidc_id      = COALESCE(?, oidc_id),
  display_name = COALESCE(?, display_name),
  title        = COALESCE(?, title)
WHERE username = ?`,
		ns(u.Password), u.GitAccount, strings.ToLower(u.Email), boolToInt(u.Admin),
		ns(u.OIDCID), ns(u.DisplayName), ns(u.Title), strings.ToLower(u.Username))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("user %s not found", u.Username)
	}
	return nil
}

func (s *Store) oneUser(row *sql.Row) (*db.User, error) {
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func scanUser(sc scanner) (*db.User, error) {
	var username, gitAccount, email, publicKeys string
	var password, oidcID, displayName, title sql.NullString
	var admin int64
	if err := sc.Scan(&username, &password, &gitAccount, &email, &admin, &oidcID, &displayName, &title, &publicKeys); err != nil {
		return nil, err
	}
	return &db.User{
		Username:    username,
		Password:    nsPtr(password),
		GitAccount:  gitAccount,
		Email:       email,
		Admin:       admin != 0,
		OIDCID:      nsPtr(oidcID),
		DisplayName: nsPtr(displayName),
		Title:       nsPtr(title),
		PublicKeys:  unmarshalPublicKeys([]byte(publicKeys)),
	}, nil
}

// ---- helpers --------------------------------------------------------------

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ns converts a *string to an arg that is NULL when nil.
func ns(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nsPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func contains(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}

func decodeStrList(s string) []string {
	if s == "" {
		return []string{}
	}
	var list []string
	if json.Unmarshal([]byte(s), &list) != nil || list == nil {
		return []string{}
	}
	return list
}

func encodeStrList(list []string) string {
	b, err := json.Marshal(orEmpty(list))
	if err != nil {
		return "[]"
	}
	return string(b)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func marshalPublicKeys(keys []db.PublicKey) []byte {
	if len(keys) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func unmarshalPublicKeys(b []byte) []db.PublicKey {
	if len(b) == 0 {
		return nil
	}
	var keys []db.PublicKey
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil
	}
	return keys
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

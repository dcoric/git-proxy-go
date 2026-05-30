-- sqlc query definitions for git-proxy-go. Generated into ./sqlc by `sqlc
-- generate` (see sqlc.yaml). The schema source is internal/db/migrations.

-- Users ---------------------------------------------------------------------

-- name: CreateUser :exec
INSERT INTO users (username, password, git_account, email, admin, oidc_id, display_name, title, public_keys)
VALUES (LOWER(@username), @password, @git_account, LOWER(@email), @admin, @oidc_id, @display_name, @title, @public_keys);

-- name: FindUser :one
SELECT * FROM users WHERE username = LOWER(@username);

-- name: FindUserByEmail :one
SELECT * FROM users WHERE email = LOWER(@email);

-- name: FindUserByOIDC :one
SELECT * FROM users WHERE oidc_id = @oidc_id;

-- name: FindUserBySSHKey :one
SELECT * FROM users
WHERE public_keys @> jsonb_build_array(jsonb_build_object('key', @key::text))
LIMIT 1;

-- name: GetUsers :many
SELECT * FROM users
WHERE (sqlc.narg('username')::text IS NULL OR username = LOWER(sqlc.narg('username')))
  AND (sqlc.narg('email')::text IS NULL OR email = LOWER(sqlc.narg('email')))
ORDER BY username;

-- name: DeleteUser :exec
DELETE FROM users WHERE username = LOWER(@username);

-- name: UpdateUser :execrows
UPDATE users SET
  password     = COALESCE(sqlc.narg('password'), password),
  git_account  = COALESCE(sqlc.narg('git_account'), git_account),
  email        = COALESCE(LOWER(sqlc.narg('email')), email),
  admin        = COALESCE(sqlc.narg('admin'), admin),
  oidc_id      = COALESCE(sqlc.narg('oidc_id'), oidc_id),
  display_name = COALESCE(sqlc.narg('display_name'), display_name),
  title        = COALESCE(sqlc.narg('title'), title)
WHERE username = LOWER(@username);

-- Repos ---------------------------------------------------------------------

-- name: CreateRepo :one
INSERT INTO repos (project, name, url, can_push, can_authorise)
VALUES (@project, LOWER(@name), @url, @can_push, @can_authorise)
RETURNING *;

-- name: GetRepos :many
SELECT * FROM repos
WHERE (sqlc.narg('name')::text IS NULL OR name = LOWER(sqlc.narg('name')))
  AND (sqlc.narg('url')::text IS NULL OR url = sqlc.narg('url'))
  AND (sqlc.narg('project')::text IS NULL OR project = sqlc.narg('project'))
ORDER BY name;

-- name: GetRepo :one
SELECT * FROM repos WHERE name = LOWER(@name);

-- name: GetRepoByUrl :one
SELECT * FROM repos WHERE url = @url;

-- name: GetRepoById :one
SELECT * FROM repos WHERE id = @id;

-- name: AddUserCanPush :execrows
UPDATE repos SET can_push = array_append(can_push, LOWER(@member))
WHERE id = @id AND NOT (LOWER(@member) = ANY(can_push));

-- name: AddUserCanAuthorise :execrows
UPDATE repos SET can_authorise = array_append(can_authorise, LOWER(@member))
WHERE id = @id AND NOT (LOWER(@member) = ANY(can_authorise));

-- name: RemoveUserCanPush :exec
UPDATE repos SET can_push = array_remove(can_push, LOWER(@member)) WHERE id = @id;

-- name: RemoveUserCanAuthorise :exec
UPDATE repos SET can_authorise = array_remove(can_authorise, LOWER(@member)) WHERE id = @id;

-- name: DeleteRepo :exec
DELETE FROM repos WHERE id = @id;

-- Pushes (also the audit trail) ---------------------------------------------

-- name: WriteAudit :exec
INSERT INTO pushes (
  id, type, method, timestamp, project, repo_name, url, repo, branch,
  error, blocked, allow_push, authorised, canceled, rejected,
  auto_approved, auto_rejected, commit_from, commit_to, push_user, data
) VALUES (
  @id, @type, @method, @timestamp, @project, @repo_name, @url, @repo, @branch,
  @error, @blocked, @allow_push, @authorised, @canceled, @rejected,
  @auto_approved, @auto_rejected, @commit_from, @commit_to, @push_user, @data
)
ON CONFLICT (id) DO UPDATE SET
  type = EXCLUDED.type, method = EXCLUDED.method, timestamp = EXCLUDED.timestamp,
  project = EXCLUDED.project, repo_name = EXCLUDED.repo_name, url = EXCLUDED.url,
  repo = EXCLUDED.repo, branch = EXCLUDED.branch, error = EXCLUDED.error,
  blocked = EXCLUDED.blocked, allow_push = EXCLUDED.allow_push,
  authorised = EXCLUDED.authorised, canceled = EXCLUDED.canceled,
  rejected = EXCLUDED.rejected, auto_approved = EXCLUDED.auto_approved,
  auto_rejected = EXCLUDED.auto_rejected, commit_from = EXCLUDED.commit_from,
  commit_to = EXCLUDED.commit_to, push_user = EXCLUDED.push_user, data = EXCLUDED.data;

-- name: GetPush :one
SELECT data FROM pushes WHERE id = @id;

-- name: GetPushes :many
SELECT data FROM pushes
WHERE (sqlc.narg('type')::text IS NULL OR type = sqlc.narg('type'))
  AND (sqlc.narg('error')::boolean IS NULL OR error = sqlc.narg('error'))
  AND (sqlc.narg('blocked')::boolean IS NULL OR blocked = sqlc.narg('blocked'))
  AND (sqlc.narg('allow_push')::boolean IS NULL OR allow_push = sqlc.narg('allow_push'))
  AND (sqlc.narg('authorised')::boolean IS NULL OR authorised = sqlc.narg('authorised'))
  AND (sqlc.narg('canceled')::boolean IS NULL OR canceled = sqlc.narg('canceled'))
  AND (sqlc.narg('rejected')::boolean IS NULL OR rejected = sqlc.narg('rejected'))
ORDER BY timestamp DESC;

-- name: DeletePush :exec
DELETE FROM pushes WHERE id = @id;

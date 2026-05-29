-- +goose Up
-- Schema for git-proxy-go (P2-2). Ported from the Node Mongo/neDB collections
-- (src/db). This file is also the schema source for sqlc (see sqlc.yaml).

CREATE TABLE users (
    username     TEXT PRIMARY KEY,
    password     TEXT,
    git_account  TEXT NOT NULL DEFAULT '',
    email        TEXT NOT NULL,
    admin        BOOLEAN NOT NULL DEFAULT FALSE,
    oidc_id      TEXT,
    display_name TEXT,
    title        TEXT
);
CREATE UNIQUE INDEX users_email_key ON users (email);
CREATE UNIQUE INDEX users_oidc_id_key ON users (oidc_id) WHERE oidc_id IS NOT NULL AND oidc_id <> '';

CREATE TABLE repos (
    id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    project       TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL,
    url           TEXT NOT NULL,
    can_push      TEXT[] NOT NULL DEFAULT '{}',
    can_authorise TEXT[] NOT NULL DEFAULT '{}'
);
CREATE INDEX repos_name_idx ON repos (name);
CREATE UNIQUE INDEX repos_url_key ON repos (url);

-- pushes doubles as the audit trail (writeAudit upserts here, as in Node). The
-- full Action document is stored in `data`; the scalar columns are promoted
-- copies used only for filtering/sorting.
CREATE TABLE pushes (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL DEFAULT '',
    method        TEXT NOT NULL DEFAULT '',
    timestamp     BIGINT NOT NULL DEFAULT 0,
    project       TEXT NOT NULL DEFAULT '',
    repo_name     TEXT NOT NULL DEFAULT '',
    url           TEXT NOT NULL DEFAULT '',
    repo          TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL DEFAULT '',
    error         BOOLEAN NOT NULL DEFAULT FALSE,
    blocked       BOOLEAN NOT NULL DEFAULT FALSE,
    allow_push    BOOLEAN NOT NULL DEFAULT FALSE,
    authorised    BOOLEAN NOT NULL DEFAULT FALSE,
    canceled      BOOLEAN NOT NULL DEFAULT FALSE,
    rejected      BOOLEAN NOT NULL DEFAULT FALSE,
    auto_approved BOOLEAN NOT NULL DEFAULT FALSE,
    auto_rejected BOOLEAN NOT NULL DEFAULT FALSE,
    commit_from   TEXT NOT NULL DEFAULT '',
    commit_to     TEXT NOT NULL DEFAULT '',
    push_user     TEXT NOT NULL DEFAULT '',
    data          JSONB NOT NULL
);
CREATE INDEX pushes_timestamp_idx ON pushes (timestamp DESC);
CREATE INDEX pushes_query_idx ON pushes (type, error, blocked, allow_push, authorised);

-- Session store table for alexedwards/scs/v2 postgresstore (wired up in P3-1).
CREATE TABLE sessions (
    token  TEXT PRIMARY KEY,
    data   BYTEA NOT NULL,
    expiry TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

-- +goose Down
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS pushes;
DROP TABLE IF EXISTS repos;
DROP TABLE IF EXISTS users;

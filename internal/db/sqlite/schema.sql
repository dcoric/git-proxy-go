-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 GitProxy Contributors
--
-- SQLite schema for the dev backend (D-4). It mirrors the Postgres schema with
-- SQLite-native types: booleans as INTEGER, arrays/JSONB as TEXT (JSON). The
-- sessions table matches what scs/sqlite3store expects (token/data/expiry).

CREATE TABLE IF NOT EXISTS users (
    username     TEXT PRIMARY KEY,
    password     TEXT,
    git_account  TEXT NOT NULL DEFAULT '',
    email        TEXT NOT NULL,
    admin        INTEGER NOT NULL DEFAULT 0,
    oidc_id      TEXT,
    display_name TEXT,
    title        TEXT,
    public_keys  TEXT NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX IF NOT EXISTS users_email_key ON users (email);
CREATE UNIQUE INDEX IF NOT EXISTS users_oidc_id_key ON users (oidc_id) WHERE oidc_id IS NOT NULL AND oidc_id <> '';

CREATE TABLE IF NOT EXISTS repos (
    id            TEXT PRIMARY KEY,
    project       TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL,
    url           TEXT NOT NULL,
    can_push      TEXT NOT NULL DEFAULT '[]',
    can_authorise TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS repos_name_idx ON repos (name);
CREATE UNIQUE INDEX IF NOT EXISTS repos_url_key ON repos (url);

CREATE TABLE IF NOT EXISTS pushes (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL DEFAULT '',
    method        TEXT NOT NULL DEFAULT '',
    timestamp     INTEGER NOT NULL DEFAULT 0,
    project       TEXT NOT NULL DEFAULT '',
    repo_name     TEXT NOT NULL DEFAULT '',
    url           TEXT NOT NULL DEFAULT '',
    repo          TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL DEFAULT '',
    error         INTEGER NOT NULL DEFAULT 0,
    blocked       INTEGER NOT NULL DEFAULT 0,
    allow_push    INTEGER NOT NULL DEFAULT 0,
    authorised    INTEGER NOT NULL DEFAULT 0,
    canceled      INTEGER NOT NULL DEFAULT 0,
    rejected      INTEGER NOT NULL DEFAULT 0,
    auto_approved INTEGER NOT NULL DEFAULT 0,
    auto_rejected INTEGER NOT NULL DEFAULT 0,
    commit_from   TEXT NOT NULL DEFAULT '',
    commit_to     TEXT NOT NULL DEFAULT '',
    push_user     TEXT NOT NULL DEFAULT '',
    data          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS pushes_timestamp_idx ON pushes (timestamp DESC);
CREATE INDEX IF NOT EXISTS pushes_query_idx ON pushes (type, error, blocked, allow_push, authorised);

CREATE TABLE IF NOT EXISTS sessions (
    token  TEXT PRIMARY KEY,
    data   BLOB NOT NULL,
    expiry REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions (expiry);

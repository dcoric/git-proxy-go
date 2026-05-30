-- +goose Up
-- SSH public keys for users (P5-1, ported from finos/git-proxy PR #1332). Each
-- user may register one or more SSH public keys; the SSH server maps a presented
-- key to its owner (db.FindUserBySSHKey). Stored as a JSONB array of
-- {key, fingerprint} records, mirroring the Node user.publicKeys field.
ALTER TABLE users ADD COLUMN public_keys JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE users DROP COLUMN public_keys;

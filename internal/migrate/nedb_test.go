// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package migrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeNeDB(t *testing.T, dir, file, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}

// TestNeDBReplay exercises the append-only log reduction: index lines are
// ignored, the latest version of a document wins, and tombstones delete.
func TestNeDBUsersReplay(t *testing.T) {
	dir := t.TempDir()
	writeNeDB(t, dir, neDBUsersFile, `{"$$indexCreated":{"fieldName":"username","unique":true}}
{"username":"alice","password":"$2b$10$x","gitAccount":"a","email":"alice@example.com","admin":true,"_id":"id1"}
{"username":"bob","password":"$2b$10$y","gitAccount":"b","email":"bob@example.com","admin":false,"_id":"id2"}
{"username":"alice","password":"$2b$10$z","gitAccount":"a2","email":"alice@example.com","admin":false,"_id":"id1"}
{"$$deleted":true,"_id":"id2"}
`)

	users, err := NewNeDBSource(dir).Users(context.Background())
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("got %d users, want 1 (id2 deleted)", len(users))
	}
	u := users[0]
	if u.Username != "alice" || u.GitAccount != "a2" || u.Admin {
		t.Errorf("latest version not applied: %+v", u)
	}
	if u.Password == nil || *u.Password != "$2b$10$z" {
		t.Errorf("password = %v, want the updated hash", u.Password)
	}
}

func TestNeDBReposAndPushes(t *testing.T) {
	dir := t.TempDir()
	writeNeDB(t, dir, neDBReposFile,
		`{"project":"finos","name":"git-proxy","url":"https://github.com/finos/git-proxy.git","users":{"canPush":["alice"],"canAuthorise":["bob"]},"_id":"r1"}
`)
	writeNeDB(t, dir, neDBPushesFile,
		`{"id":"a__b","type":"push","blocked":true,"timestamp":100,"steps":[],"_id":"p1"}
`)

	src := NewNeDBSource(dir)
	ctx := context.Background()

	repos, err := src.Repos(ctx)
	if err != nil || len(repos) != 1 {
		t.Fatalf("Repos: %d, %v", len(repos), err)
	}
	if repos[0].Name != "git-proxy" || len(repos[0].Users.CanPush) != 1 || repos[0].Users.CanPush[0] != "alice" {
		t.Errorf("repo not decoded correctly: %+v", repos[0])
	}

	pushes, err := src.Pushes(ctx)
	if err != nil || len(pushes) != 1 {
		t.Fatalf("Pushes: %d, %v", len(pushes), err)
	}
	if pushes[0].ID != "a__b" || !pushes[0].Blocked || pushes[0].Timestamp != 100 {
		t.Errorf("push not decoded correctly: %+v", pushes[0])
	}
}

// TestNeDBMissingFile: an absent collection file is an empty collection.
func TestNeDBMissingFile(t *testing.T) {
	users, err := NewNeDBSource(t.TempDir()).Users(context.Background())
	if err != nil {
		t.Fatalf("Users(missing file): %v", err)
	}
	if len(users) != 0 {
		t.Errorf("got %d users, want 0", len(users))
	}
}

func TestNeDBKind(t *testing.T) {
	if k := NewNeDBSource("x").Kind(); k != "nedb" {
		t.Errorf("Kind() = %q, want nedb", k)
	}
}

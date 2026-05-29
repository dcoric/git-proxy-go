// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

//go:build integration

// End-to-end migrate-data tests (P2-4..P2-6). They spin up real Postgres and
// MongoDB containers via dockertest: the Mongo reader is exercised against a
// live MongoDB, and the neDB->Postgres ETL against the real store.
// Run with: go test -tags=integration ./...
package migrate

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dcoric/git-proxy-go/internal/db/migrations"
	"github.com/dcoric/git-proxy-go/internal/db/postgres"
)

var (
	testStore    *postgres.Store
	testMongoURI string
)

func TestMain(m *testing.M) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		log.Fatalf("dockertest: %v", err)
	}
	if err := pool.Client.Ping(); err != nil {
		log.Fatalf("docker not reachable: %v", err)
	}
	pool.MaxWait = 120 * time.Second
	ctx := context.Background()

	pg, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres", Tag: "16-alpine",
		Env: []string{"POSTGRES_USER=gitproxy", "POSTGRES_PASSWORD=secret", "POSTGRES_DB=gitproxy"},
	}, autoRemove)
	if err != nil {
		log.Fatalf("start postgres: %v", err)
	}
	mg, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "mongo", Tag: "7",
	}, autoRemove)
	if err != nil {
		_ = pool.Purge(pg)
		log.Fatalf("start mongo: %v", err)
	}
	purgeAll := func() { _ = pool.Purge(mg); _ = pool.Purge(pg) }

	dsn := fmt.Sprintf("postgres://gitproxy:secret@%s/gitproxy?sslmode=disable", pg.GetHostPort("5432/tcp"))
	testMongoURI = fmt.Sprintf("mongodb://%s", mg.GetHostPort("27017/tcp"))

	if err := pool.Retry(func() error {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer p.Close()
		return p.Ping(ctx)
	}); err != nil {
		purgeAll()
		log.Fatalf("postgres never ready: %v", err)
	}
	if err := pool.Retry(func() error {
		c, err := mongo.Connect(options.Client().ApplyURI(testMongoURI))
		if err != nil {
			return err
		}
		defer func() { _ = c.Disconnect(ctx) }()
		return c.Ping(ctx, nil)
	}); err != nil {
		purgeAll()
		log.Fatalf("mongo never ready: %v", err)
	}

	if err := migrations.Up(ctx, dsn); err != nil {
		purgeAll()
		log.Fatalf("migrations: %v", err)
	}
	store, err := postgres.Connect(ctx, dsn)
	if err != nil {
		purgeAll()
		log.Fatalf("connect store: %v", err)
	}
	testStore = store

	code := m.Run()

	store.Close()
	purgeAll()
	os.Exit(code)
}

func autoRemove(c *docker.HostConfig) {
	c.AutoRemove = true
	c.RestartPolicy = docker.RestartPolicy{Name: "no"}
}

func truncatePG(t *testing.T) {
	t.Helper()
	if _, err := testStore.Pool().Exec(context.Background(), "TRUNCATE users, repos, pushes"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// TestMongoSourceRoundTrip inserts documents (with ObjectID _ids) into a live
// MongoDB and reads them back through MongoSource.
func TestMongoSourceRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbName := "gitproxy_test"

	client, err := mongo.Connect(options.Client().ApplyURI(testMongoURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	mdb := client.Database(dbName)
	_ = mdb.Drop(ctx)

	if _, err := mdb.Collection("users").InsertOne(ctx, bson.M{
		"_id": bson.NewObjectID(), "username": "alice", "password": "$2b$10$h",
		"gitAccount": "a", "email": "alice@example.com", "admin": true,
	}); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := mdb.Collection("repos").InsertOne(ctx, bson.M{
		"_id": bson.NewObjectID(), "project": "finos", "name": "git-proxy",
		"url":   "https://github.com/finos/git-proxy.git",
		"users": bson.M{"canPush": bson.A{"alice"}, "canAuthorise": bson.A{}},
	}); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if _, err := mdb.Collection("pushes").InsertOne(ctx, bson.M{
		"_id": bson.NewObjectID(), "id": "a__b", "type": "push", "blocked": true, "timestamp": int64(123),
	}); err != nil {
		t.Fatalf("insert push: %v", err)
	}

	src, err := NewMongoSource(ctx, testMongoURI, dbName)
	if err != nil {
		t.Fatalf("NewMongoSource: %v", err)
	}
	defer func() { _ = src.Close(ctx) }()

	users, err := src.Users(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "alice" || !users[0].Admin {
		t.Fatalf("Users = %+v, err %v", users, err)
	}
	repos, err := src.Repos(ctx)
	if err != nil || len(repos) != 1 || repos[0].Name != "git-proxy" {
		t.Fatalf("Repos = %+v, err %v", repos, err)
	}
	if len(repos[0].ID) != 24 { // ObjectID hex string
		t.Errorf("repo ID = %q, want 24-char hex", repos[0].ID)
	}
	pushes, err := src.Pushes(ctx)
	if err != nil || len(pushes) != 1 || pushes[0].ID != "a__b" || pushes[0].Timestamp != 123 {
		t.Fatalf("Pushes = %+v, err %v", pushes, err)
	}
}

// TestETLNeDBToPostgres runs the full pipeline: sample neDB files -> ETL -> the
// real Postgres store, asserting dry-run writes nothing and apply persists.
func TestETLNeDBToPostgres(t *testing.T) {
	truncatePG(t)
	ctx := context.Background()

	dir := t.TempDir()
	writeNeDB(t, dir, neDBUsersFile,
		`{"username":"alice","password":"$2b$10$h","gitAccount":"a","email":"alice@example.com","admin":true,"_id":"u1"}
`)
	writeNeDB(t, dir, neDBReposFile,
		`{"project":"finos","name":"git-proxy","url":"https://github.com/finos/git-proxy.git","users":{"canPush":["alice"],"canAuthorise":[]},"_id":"r1"}
`)
	writeNeDB(t, dir, neDBPushesFile,
		`{"id":"a__b","type":"push","blocked":true,"timestamp":100,"steps":[],"_id":"p1"}
`)
	src := NewNeDBSource(dir)

	// Dry-run writes nothing.
	rep, err := Run(ctx, src, testStore, Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if rep.Summary[ActionWouldCreate] != 3 {
		t.Errorf("dry-run would-create = %d, want 3", rep.Summary[ActionWouldCreate])
	}
	if u, _ := testStore.FindUser(ctx, "alice"); u != nil {
		t.Fatal("dry-run created a user")
	}

	// Apply persists everything.
	rep, err = Run(ctx, src, testStore, Options{DryRun: false})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.HasErrors() || rep.Summary[ActionCreated] != 3 {
		t.Fatalf("apply report = %+v", rep.Summary)
	}

	u, _ := testStore.FindUser(ctx, "alice")
	if u == nil || u.GitAccount != "a" || !u.Admin {
		t.Errorf("user not migrated: %+v", u)
	}
	r, _ := testStore.GetRepoByURL(ctx, "https://github.com/finos/git-proxy.git")
	if r == nil || len(r.Users.CanPush) != 1 || r.Users.CanPush[0] != "alice" {
		t.Errorf("repo not migrated: %+v", r)
	}
	p, _ := testStore.GetPush(ctx, "a__b")
	if p == nil || !p.Blocked {
		t.Errorf("push not migrated: %+v", p)
	}

	// Re-running apply skips users/repos and updates the push (idempotent).
	rep, err = Run(ctx, src, testStore, Options{DryRun: false})
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if rep.Summary[ActionSkipped] != 2 || rep.Summary[ActionUpdated] != 1 {
		t.Errorf("re-apply summary = %+v, want 2 skipped + 1 updated", rep.Summary)
	}
}

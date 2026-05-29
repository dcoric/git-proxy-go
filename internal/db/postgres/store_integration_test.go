// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

//go:build integration

// Round-trip tests for the Postgres store (P2-7). They spin up a real Postgres
// in Docker via dockertest, apply the goose migrations and exercise the store
// against it. Run with: go test -tags=integration ./...
package postgres

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"

	"github.com/dcoric/git-proxy-go/internal/db"
	"github.com/dcoric/git-proxy-go/internal/db/migrations"
)

var testStore *Store

func TestMain(m *testing.M) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		log.Fatalf("dockertest: could not connect to docker: %v", err)
	}
	if err := pool.Client.Ping(); err != nil {
		log.Fatalf("dockertest: docker not reachable: %v", err)
	}

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "16-alpine",
		Env: []string{
			"POSTGRES_USER=gitproxy",
			"POSTGRES_PASSWORD=secret",
			"POSTGRES_DB=gitproxy",
		},
	}, func(c *docker.HostConfig) {
		c.AutoRemove = true
		c.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		log.Fatalf("dockertest: could not start postgres: %v", err)
	}
	_ = resource.Expire(180) // self-destruct safety net

	dsn := fmt.Sprintf("postgres://gitproxy:secret@%s/gitproxy?sslmode=disable", resource.GetHostPort("5432/tcp"))
	ctx := context.Background()

	pool.MaxWait = 90 * time.Second
	if err := pool.Retry(func() error {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer p.Close()
		return p.Ping(ctx)
	}); err != nil {
		_ = pool.Purge(resource)
		log.Fatalf("dockertest: postgres never became ready: %v", err)
	}

	if err := migrations.Up(ctx, dsn); err != nil {
		_ = pool.Purge(resource)
		log.Fatalf("migrations: %v", err)
	}

	store, err := Connect(ctx, dsn)
	if err != nil {
		_ = pool.Purge(resource)
		log.Fatalf("connect store: %v", err)
	}
	testStore = store

	code := m.Run()

	store.Close()
	_ = pool.Purge(resource)
	os.Exit(code)
}

// truncate resets all tables so each test runs against a clean schema.
func truncate(t *testing.T) {
	t.Helper()
	_, err := testStore.pool.Exec(context.Background(), "TRUNCATE users, repos, pushes")
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func strptr(s string) *string { return &s }

func TestUsersRoundTrip(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	if err := testStore.CreateUser(ctx, &db.User{
		Username: "Alice", Password: strptr("$2b$10$hash"), GitAccount: "alice-gh",
		Email: "Alice@Example.com", Admin: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Lookups lower-case their input (parity with the Node sink).
	got, err := testStore.FindUser(ctx, "alice")
	if err != nil || got == nil {
		t.Fatalf("FindUser: %v / %v", got, err)
	}
	if got.Username != "alice" || got.Email != "alice@example.com" || !got.Admin {
		t.Errorf("FindUser returned %+v", got)
	}
	if byEmail, _ := testStore.FindUserByEmail(ctx, "ALICE@example.com"); byEmail == nil {
		t.Error("FindUserByEmail did not find the user (case-insensitive)")
	}

	// Absent user -> (nil, nil), mirroring the Node "return null".
	if missing, err := testStore.FindUser(ctx, "nobody"); err != nil || missing != nil {
		t.Errorf("FindUser(missing) = %v, %v; want nil, nil", missing, err)
	}

	// GetUsers strips passwords.
	users, err := testStore.GetUsers(ctx, db.UserQuery{})
	if err != nil || len(users) != 1 {
		t.Fatalf("GetUsers: %v users, err %v", len(users), err)
	}
	if users[0].Password != nil {
		t.Errorf("GetUsers leaked a password: %v", *users[0].Password)
	}

	// UpdateUser preserves nil pointer fields, writes value fields.
	if err := testStore.UpdateUser(ctx, &db.User{
		Username: "alice", GitAccount: "alice-2", Email: "alice@example.com", Admin: false,
		DisplayName: strptr("Alice A"),
	}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	got, _ = testStore.FindUser(ctx, "alice")
	if got.GitAccount != "alice-2" || got.Admin {
		t.Errorf("UpdateUser did not apply value fields: %+v", got)
	}
	if got.DisplayName == nil || *got.DisplayName != "Alice A" {
		t.Errorf("UpdateUser displayName = %v", got.DisplayName)
	}
	if got.Password == nil { // nil pointer field must be preserved, not nulled
		t.Error("UpdateUser nulled the password despite passing nil")
	}

	if err := testStore.UpdateUser(ctx, &db.User{Username: "ghost"}); err == nil {
		t.Error("UpdateUser(missing) should error")
	}

	if err := testStore.DeleteUser(ctx, "alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if got, _ := testStore.FindUser(ctx, "alice"); got != nil {
		t.Error("user still present after DeleteUser")
	}
}

func TestUserOIDCLookup(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	if err := testStore.CreateUser(ctx, &db.User{
		Username: "oidcuser", GitAccount: "g", Email: "o@example.com", OIDCID: strptr("sub-123"),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := testStore.FindUserByOIDC(ctx, "sub-123")
	if err != nil || got == nil || got.Username != "oidcuser" {
		t.Fatalf("FindUserByOIDC = %+v, %v", got, err)
	}
	if got.Password != nil {
		t.Errorf("OIDC user should have nil password, got %v", *got.Password)
	}
}

func TestReposRoundTrip(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	created, err := testStore.CreateRepo(ctx, &db.Repo{
		Project: "finos", Name: "Git-Proxy", URL: "https://github.com/finos/git-proxy.git",
		Users: db.RepoUsers{CanPush: []string{}, CanAuthorise: []string{}},
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateRepo did not return an id")
	}
	if created.Name != "git-proxy" {
		t.Errorf("repo name not lower-cased: %q", created.Name)
	}

	// Lookup by name (lower-cased), url and id.
	if r, _ := testStore.GetRepo(ctx, "GIT-PROXY"); r == nil || r.ID != created.ID {
		t.Error("GetRepo(name) failed")
	}
	if r, _ := testStore.GetRepoByURL(ctx, "https://github.com/finos/git-proxy.git"); r == nil {
		t.Error("GetRepoByURL failed")
	}
	if r, _ := testStore.GetRepoByID(ctx, created.ID); r == nil {
		t.Error("GetRepoByID failed")
	}

	// Access lists: add (idempotent), then remove.
	for i := 0; i < 2; i++ { // twice to prove idempotency
		if err := testStore.AddUserCanPush(ctx, created.ID, "Bob"); err != nil {
			t.Fatalf("AddUserCanPush: %v", err)
		}
	}
	if err := testStore.AddUserCanAuthorise(ctx, created.ID, "carol"); err != nil {
		t.Fatalf("AddUserCanAuthorise: %v", err)
	}
	r, _ := testStore.GetRepoByID(ctx, created.ID)
	if !reflect.DeepEqual(r.Users.CanPush, []string{"bob"}) {
		t.Errorf("canPush = %v, want [bob] (lower-cased, deduped)", r.Users.CanPush)
	}
	if !reflect.DeepEqual(r.Users.CanAuthorise, []string{"carol"}) {
		t.Errorf("canAuthorise = %v, want [carol]", r.Users.CanAuthorise)
	}

	if err := testStore.RemoveUserCanPush(ctx, created.ID, "bob"); err != nil {
		t.Fatalf("RemoveUserCanPush: %v", err)
	}
	r, _ = testStore.GetRepoByID(ctx, created.ID)
	if len(r.Users.CanPush) != 0 {
		t.Errorf("canPush after remove = %v, want empty", r.Users.CanPush)
	}

	if repos, _ := testStore.GetRepos(ctx, db.RepoQuery{Project: strptr("finos")}); len(repos) != 1 {
		t.Errorf("GetRepos(project=finos) = %d, want 1", len(repos))
	}

	if err := testStore.DeleteRepo(ctx, created.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if r, _ := testStore.GetRepoByID(ctx, created.ID); r != nil {
		t.Error("repo still present after DeleteRepo")
	}
}

func TestPushWriteReadDiff(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	msg := "boom"
	want := &db.Push{
		ID: "from__to", Type: "push", Method: "POST", Timestamp: 1717000000000,
		Project: "finos", RepoName: "git-proxy", URL: "https://github.com/finos/git-proxy.git",
		Repo: "finos/git-proxy.git",
		Steps: []db.Step{{
			ID: "s1", StepName: "checkCommitMessages", Content: map[string]any{"detail": "value"},
			Error: true, ErrorMessage: &msg, Logs: []string{"checkCommitMessages - boom"},
		}},
		Error: true, Blocked: true,
		CommitData: []db.CommitData{{Tree: "t", Author: "a", AuthorEmail: "a@b.c", Message: "m"}},
		CommitFrom: "from", CommitTo: "to", Branch: "refs/heads/main",
		User: "alice", UserEmail: "alice@example.com",
		Attestation: &db.Attestation{
			Reviewer:  db.Reviewer{Username: "bob", Email: "bob@example.com"},
			Timestamp: "2026-05-29T00:00:00Z", Answers: []db.AttestationAnswer{{Label: "ok?", Checked: true}},
		},
	}

	if err := testStore.WriteAudit(ctx, want); err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}
	got, err := testStore.GetPush(ctx, "from__to")
	if err != nil || got == nil {
		t.Fatalf("GetPush: %v / %v", got, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("push round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}

	// Upsert: a second WriteAudit with the same id updates in place.
	want.Blocked = false
	want.AllowPush = true
	if err := testStore.WriteAudit(ctx, want); err != nil {
		t.Fatalf("WriteAudit (upsert): %v", err)
	}
	got, _ = testStore.GetPush(ctx, "from__to")
	if got.Blocked || !got.AllowPush {
		t.Errorf("upsert did not update scalar fields: blocked=%v allowPush=%v", got.Blocked, got.AllowPush)
	}

	if err := testStore.DeletePush(ctx, "from__to"); err != nil {
		t.Fatalf("DeletePush: %v", err)
	}
	if got, _ := testStore.GetPush(ctx, "from__to"); got != nil {
		t.Error("push still present after DeletePush")
	}
}

func TestGetPushesFilterAndOrder(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	// Two pushes match the default review query (push/blocked/not-error/etc),
	// one does not (already authorised).
	mustWrite(t, &db.Push{ID: "p1", Type: "push", Blocked: true, Timestamp: 100})
	mustWrite(t, &db.Push{ID: "p2", Type: "push", Blocked: true, Timestamp: 300})
	mustWrite(t, &db.Push{ID: "p3", Type: "push", Blocked: true, Authorised: true, Timestamp: 200})

	got, err := testStore.GetPushes(ctx, db.DefaultPushQuery())
	if err != nil {
		t.Fatalf("GetPushes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetPushes returned %d, want 2", len(got))
	}
	// Newest first (timestamp desc): p2 (300) before p1 (100).
	if got[0].ID != "p2" || got[1].ID != "p1" {
		t.Errorf("order = [%s, %s], want [p2, p1]", got[0].ID, got[1].ID)
	}
}

func TestAuthoriseRejectCancel(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	mustWrite(t, &db.Push{ID: "px", Type: "push", Blocked: true, Timestamp: 1})

	att := &db.Attestation{Reviewer: db.Reviewer{Username: "bob"}, Timestamp: "t", Answers: []db.AttestationAnswer{{Label: "q", Checked: true}}}
	if msg, err := testStore.Authorise(ctx, "px", att); err != nil || msg != "authorised px" {
		t.Fatalf("Authorise = %q, %v", msg, err)
	}
	p, _ := testStore.GetPush(ctx, "px")
	if !p.Authorised || p.Canceled || p.Rejected || p.Attestation == nil {
		t.Errorf("after Authorise: %+v", p)
	}

	if msg, err := testStore.Reject(ctx, "px", db.Rejection{Reason: "nope", Timestamp: "t"}); err != nil || msg != "reject px" {
		t.Fatalf("Reject = %q, %v", msg, err)
	}
	p, _ = testStore.GetPush(ctx, "px")
	if p.Authorised || !p.Rejected || p.Rejection == nil || p.Rejection.Reason != "nope" {
		t.Errorf("after Reject: %+v", p)
	}

	if msg, err := testStore.Cancel(ctx, "px"); err != nil || msg != "canceled px" {
		t.Fatalf("Cancel = %q, %v", msg, err)
	}
	p, _ = testStore.GetPush(ctx, "px")
	if !p.Canceled || p.Authorised || p.Rejected {
		t.Errorf("after Cancel: %+v", p)
	}

	if _, err := testStore.Authorise(ctx, "missing", nil); err == nil {
		t.Error("Authorise(missing) should error")
	}
}

func mustWrite(t *testing.T, p *db.Push) {
	t.Helper()
	if err := testStore.WriteAudit(context.Background(), p); err != nil {
		t.Fatalf("WriteAudit(%s): %v", p.ID, err)
	}
}

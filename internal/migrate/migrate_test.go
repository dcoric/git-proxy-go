// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package migrate

import (
	"context"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// fakeSource returns fixed slices.
type fakeSource struct {
	users  []*db.User
	repos  []*db.Repo
	pushes []*db.Push
}

func (f *fakeSource) Kind() string                               { return "fake" }
func (f *fakeSource) Close(context.Context) error                { return nil }
func (f *fakeSource) Users(context.Context) ([]*db.User, error)  { return f.users, nil }
func (f *fakeSource) Repos(context.Context) ([]*db.Repo, error)  { return f.repos, nil }
func (f *fakeSource) Pushes(context.Context) ([]*db.Push, error) { return f.pushes, nil }

// fakeStore is an in-memory db.Store; only the methods the ETL uses do real
// work, the rest satisfy the interface.
type fakeStore struct {
	users           map[string]*db.User
	reposByURL      map[string]*db.Repo
	pushes          map[string]*db.Push
	createUserCalls int
	createRepoCalls int
	writeAuditCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:      map[string]*db.User{},
		reposByURL: map[string]*db.Repo{},
		pushes:     map[string]*db.Push{},
	}
}

func (f *fakeStore) FindUser(_ context.Context, username string) (*db.User, error) {
	return f.users[username], nil
}
func (f *fakeStore) CreateUser(_ context.Context, u *db.User) error {
	f.users[u.Username] = u
	f.createUserCalls++
	return nil
}
func (f *fakeStore) GetRepoByURL(_ context.Context, url string) (*db.Repo, error) {
	return f.reposByURL[url], nil
}
func (f *fakeStore) CreateRepo(_ context.Context, r *db.Repo) (*db.Repo, error) {
	f.reposByURL[r.URL] = r
	f.createRepoCalls++
	return r, nil
}
func (f *fakeStore) GetPush(_ context.Context, id string) (*db.Push, error) {
	return f.pushes[id], nil
}
func (f *fakeStore) WriteAudit(_ context.Context, p *db.Push) error {
	f.pushes[p.ID] = p
	f.writeAuditCalls++
	return nil
}

// Unused-by-ETL methods (no-op stubs to satisfy db.Store).
func (f *fakeStore) GetPushes(context.Context, db.PushQuery) ([]*db.Push, error) { return nil, nil }
func (f *fakeStore) DeletePush(context.Context, string) error                    { return nil }
func (f *fakeStore) Authorise(context.Context, string, *db.Attestation) (string, error) {
	return "", nil
}
func (f *fakeStore) Cancel(context.Context, string) (string, error)               { return "", nil }
func (f *fakeStore) Reject(context.Context, string, db.Rejection) (string, error) { return "", nil }
func (f *fakeStore) GetRepos(context.Context, db.RepoQuery) ([]*db.Repo, error)   { return nil, nil }
func (f *fakeStore) GetRepo(context.Context, string) (*db.Repo, error)            { return nil, nil }
func (f *fakeStore) GetRepoByID(context.Context, string) (*db.Repo, error)        { return nil, nil }
func (f *fakeStore) AddUserCanPush(context.Context, string, string) error         { return nil }
func (f *fakeStore) AddUserCanAuthorise(context.Context, string, string) error    { return nil }
func (f *fakeStore) RemoveUserCanPush(context.Context, string, string) error      { return nil }
func (f *fakeStore) RemoveUserCanAuthorise(context.Context, string, string) error { return nil }
func (f *fakeStore) DeleteRepo(context.Context, string) error                     { return nil }
func (f *fakeStore) FindUserByEmail(context.Context, string) (*db.User, error)    { return nil, nil }
func (f *fakeStore) FindUserByOIDC(context.Context, string) (*db.User, error)     { return nil, nil }
func (f *fakeStore) GetUsers(context.Context, db.UserQuery) ([]*db.User, error)   { return nil, nil }
func (f *fakeStore) DeleteUser(context.Context, string) error                     { return nil }
func (f *fakeStore) UpdateUser(context.Context, *db.User) error                   { return nil }
func (f *fakeStore) Close()                                                       {}

func sampleSource() *fakeSource {
	pw := "$2b$10$hash"
	return &fakeSource{
		users:  []*db.User{{Username: "alice", Password: &pw, Email: "a@x.com", GitAccount: "a"}},
		repos:  []*db.Repo{{Name: "git-proxy", URL: "https://x/y.git", Users: db.RepoUsers{CanPush: []string{"alice"}}}},
		pushes: []*db.Push{{ID: "a__b", Type: "push", Blocked: true, Timestamp: 1}},
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	store := newFakeStore()
	rep, err := Run(context.Background(), sampleSource(), store, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.createUserCalls != 0 || store.createRepoCalls != 0 || store.writeAuditCalls != 0 {
		t.Errorf("dry-run wrote to the store: %+v", store)
	}
	if !rep.DryRun {
		t.Error("report.DryRun = false")
	}
	if rep.Summary[ActionWouldCreate] != 3 {
		t.Errorf("would-create = %d, want 3", rep.Summary[ActionWouldCreate])
	}
	if rep.HasErrors() {
		t.Error("dry-run reported errors")
	}
}

func TestRunApplyWritesAll(t *testing.T) {
	store := newFakeStore()
	rep, err := Run(context.Background(), sampleSource(), store, Options{DryRun: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.createUserCalls != 1 || store.createRepoCalls != 1 || store.writeAuditCalls != 1 {
		t.Errorf("apply did not write everything: %+v", store)
	}
	if rep.Summary[ActionCreated] != 3 {
		t.Errorf("created = %d, want 3", rep.Summary[ActionCreated])
	}
}

func TestRunSkipsExisting(t *testing.T) {
	store := newFakeStore()
	store.users["alice"] = &db.User{Username: "alice"}        // already present
	store.reposByURL["https://x/y.git"] = &db.Repo{Name: "x"} // already present

	rep, err := Run(context.Background(), sampleSource(), store, Options{DryRun: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.createUserCalls != 0 {
		t.Error("existing user was re-created")
	}
	if store.createRepoCalls != 0 {
		t.Error("existing repo was re-created")
	}
	if rep.Summary[ActionSkipped] != 2 {
		t.Errorf("skipped = %d, want 2 (user + repo)", rep.Summary[ActionSkipped])
	}
	if rep.Summary[ActionCreated] != 1 { // the push is still upserted
		t.Errorf("created = %d, want 1 (push)", rep.Summary[ActionCreated])
	}
}

func TestRunPushUpsertReportsUpdate(t *testing.T) {
	store := newFakeStore()
	store.pushes["a__b"] = &db.Push{ID: "a__b"} // already present -> update

	rep, err := Run(context.Background(), sampleSource(), store, Options{DryRun: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Summary[ActionUpdated] != 1 {
		t.Errorf("updated = %d, want 1", rep.Summary[ActionUpdated])
	}
}

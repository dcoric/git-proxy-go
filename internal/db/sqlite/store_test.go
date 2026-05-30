// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Connect(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }

func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, &db.User{
		Username:   "Alice", // mixed case — must be lowered
		Email:      "Alice@Example.com",
		GitAccount: "alice-gh",
		Admin:      true,
		Password:   strptr("hashed"),
		PublicKeys: []db.PublicKey{{Key: "ssh-ed25519 AAAAKEY", Fingerprint: "SHA256:fp"}},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	u, err := s.FindUser(ctx, "alice")
	if err != nil || u == nil {
		t.Fatalf("FindUser: %v %v", u, err)
	}
	if u.Username != "alice" || u.Email != "alice@example.com" || !u.Admin {
		t.Errorf("user lowering/fields wrong: %+v", u)
	}
	if u.Password == nil || *u.Password != "hashed" {
		t.Errorf("password = %v", u.Password)
	}

	// Lookup by email (lowercased input) and by SSH key.
	if got, _ := s.FindUserByEmail(ctx, "ALICE@example.com"); got == nil || got.Username != "alice" {
		t.Errorf("FindUserByEmail = %v", got)
	}
	if got, _ := s.FindUserBySSHKey(ctx, "ssh-ed25519 AAAAKEY"); got == nil || got.Username != "alice" {
		t.Errorf("FindUserBySSHKey = %v", got)
	}
	if got, _ := s.FindUserBySSHKey(ctx, "ssh-ed25519 NOPE"); got != nil {
		t.Errorf("FindUserBySSHKey unknown key returned %v", got)
	}

	// GetUsers strips passwords.
	users, err := s.GetUsers(ctx, db.UserQuery{Username: strptr("alice")})
	if err != nil || len(users) != 1 || users[0].Password != nil {
		t.Errorf("GetUsers = %v %v", users, err)
	}

	// UpdateUser preserves nil pointer fields (password) and updates values.
	if err := s.UpdateUser(ctx, &db.User{Username: "alice", Email: "alice@example.com", GitAccount: "new-gh", Admin: false}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	u, _ = s.FindUser(ctx, "alice")
	if u.GitAccount != "new-gh" || u.Admin {
		t.Errorf("update not applied: %+v", u)
	}
	if u.Password == nil || *u.Password != "hashed" {
		t.Errorf("nil password should have been preserved, got %v", u.Password)
	}

	// Missing-user update errors.
	if err := s.UpdateUser(ctx, &db.User{Username: "ghost"}); err == nil {
		t.Error("UpdateUser on missing user should error")
	}

	// Absent lookups return (nil, nil).
	if got, err := s.FindUser(ctx, "nobody"); got != nil || err != nil {
		t.Errorf("FindUser absent = %v %v", got, err)
	}

	if err := s.DeleteUser(ctx, "alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if got, _ := s.FindUser(ctx, "alice"); got != nil {
		t.Error("user not deleted")
	}
}

func TestRepoCRUDAndAccessLists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	repo, err := s.CreateRepo(ctx, &db.Repo{Project: "proj", Name: "MyRepo", URL: "https://github.com/proj/myrepo.git"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if repo.ID == "" {
		t.Fatal("CreateRepo did not assign an id")
	}
	if repo.Name != "myrepo" {
		t.Errorf("repo name not lowered: %q", repo.Name)
	}
	if repo.Users.CanPush == nil || len(repo.Users.CanPush) != 0 {
		t.Errorf("new repo CanPush should be empty non-nil, got %v", repo.Users.CanPush)
	}

	if got, _ := s.GetRepoByURL(ctx, "https://github.com/proj/myrepo.git"); got == nil || got.ID != repo.ID {
		t.Errorf("GetRepoByURL = %v", got)
	}
	if got, _ := s.GetRepo(ctx, "MYREPO"); got == nil {
		t.Errorf("GetRepo (lowered) = %v", got)
	}

	// AddUserCanPush is idempotent and lower-cases the member.
	if err := s.AddUserCanPush(ctx, repo.ID, "Bob"); err != nil {
		t.Fatalf("AddUserCanPush: %v", err)
	}
	if err := s.AddUserCanPush(ctx, repo.ID, "bob"); err != nil {
		t.Fatalf("AddUserCanPush (dup): %v", err)
	}
	got, _ := s.GetRepoByID(ctx, repo.ID)
	if len(got.Users.CanPush) != 1 || got.Users.CanPush[0] != "bob" {
		t.Errorf("CanPush after idempotent add = %v", got.Users.CanPush)
	}

	if err := s.AddUserCanAuthorise(ctx, repo.ID, "carol"); err != nil {
		t.Fatalf("AddUserCanAuthorise: %v", err)
	}
	if err := s.RemoveUserCanPush(ctx, repo.ID, "bob"); err != nil {
		t.Fatalf("RemoveUserCanPush: %v", err)
	}
	got, _ = s.GetRepoByID(ctx, repo.ID)
	if len(got.Users.CanPush) != 0 || len(got.Users.CanAuthorise) != 1 {
		t.Errorf("after remove: push=%v auth=%v", got.Users.CanPush, got.Users.CanAuthorise)
	}

	// GetRepos with a project filter.
	repos, err := s.GetRepos(ctx, db.RepoQuery{Project: strptr("proj")})
	if err != nil || len(repos) != 1 {
		t.Errorf("GetRepos = %v %v", repos, err)
	}

	if err := s.DeleteRepo(ctx, repo.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if got, _ := s.GetRepoByID(ctx, repo.ID); got != nil {
		t.Error("repo not deleted")
	}
}

func TestPushAuditQueryAndActions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	push := &db.Push{ID: "c1__c2", Type: "push", Repo: "github.com/o/r.git", URL: "https://github.com/o/r.git",
		Blocked: true, Timestamp: 100, Branch: "refs/heads/main"}
	if err := s.WriteAudit(ctx, push); err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}
	// A second, non-blocked push to exercise filtering + ordering.
	if err := s.WriteAudit(ctx, &db.Push{ID: "c3__c4", Type: "push", Blocked: false, Authorised: true, Timestamp: 200}); err != nil {
		t.Fatalf("WriteAudit 2: %v", err)
	}

	got, err := s.GetPush(ctx, "c1__c2")
	if err != nil || got == nil || got.Branch != "refs/heads/main" {
		t.Fatalf("GetPush round-trip = %v %v", got, err)
	}

	// Filter: blocked only.
	blocked, err := s.GetPushes(ctx, db.PushQuery{Type: strptr("push"), Blocked: boolptr(true)})
	if err != nil || len(blocked) != 1 || blocked[0].ID != "c1__c2" {
		t.Fatalf("GetPushes blocked = %v %v", blocked, err)
	}
	// No filter: both, newest first (timestamp DESC).
	all, _ := s.GetPushes(ctx, db.PushQuery{Type: strptr("push")})
	if len(all) != 2 || all[0].ID != "c3__c4" {
		t.Errorf("GetPushes order = %v", all)
	}

	// Authorise flips the flags and persists the attestation.
	if _, err := s.Authorise(ctx, "c1__c2", &db.Attestation{Timestamp: "now"}); err != nil {
		t.Fatalf("Authorise: %v", err)
	}
	got, _ = s.GetPush(ctx, "c1__c2")
	if !got.Authorised || got.Blocked && got.Rejected {
		t.Errorf("after authorise: %+v", got)
	}
	if got.Attestation == nil {
		t.Error("attestation not persisted")
	}

	// Reject then Cancel transitions.
	if _, err := s.Reject(ctx, "c1__c2", db.Rejection{Reason: "no"}); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	got, _ = s.GetPush(ctx, "c1__c2")
	if !got.Rejected || got.Authorised {
		t.Errorf("after reject: %+v", got)
	}
	if _, err := s.Cancel(ctx, "c1__c2"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ = s.GetPush(ctx, "c1__c2")
	if !got.Canceled || got.Rejected {
		t.Errorf("after cancel: %+v", got)
	}

	// Acting on a missing push errors.
	if _, err := s.Authorise(ctx, "missing", nil); err == nil {
		t.Error("Authorise on missing push should error")
	}

	if err := s.DeletePush(ctx, "c1__c2"); err != nil {
		t.Fatalf("DeletePush: %v", err)
	}
	if got, _ := s.GetPush(ctx, "c1__c2"); got != nil {
		t.Error("push not deleted")
	}
}

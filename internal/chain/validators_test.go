// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"net/http"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/config/generated"
	"github.com/dcoric/git-proxy-go/internal/db"
)

func actionWithCommits(commits ...db.CommitData) *Action {
	a := NewAction("id", "push", http.MethodPost, 0, "https://github.com/x/y.git")
	a.CommitData = commits
	return a
}

func TestCheckEmptyBranch(t *testing.T) {
	e := &Engine{}

	// Has commit data -> passes, no step recorded.
	a := actionWithCommits(db.CommitData{Message: "x"})
	a, _ = e.checkEmptyBranch(context.Background(), nil, a)
	if !a.Continue() || len(a.Steps) != 0 {
		t.Errorf("push with commits should pass without a step; continue=%v steps=%d", a.Continue(), len(a.Steps))
	}

	// No commit data -> blocked with an error step.
	a = actionWithCommits()
	a, _ = e.checkEmptyBranch(context.Background(), nil, a)
	if a.Continue() {
		t.Error("empty push should be errored")
	}
	if a.ErrorMessage == nil || *a.ErrorMessage == "" {
		t.Error("expected an error message for the empty push")
	}
}

func commitCfg(t *testing.T, cc *generated.CommitConfig) *config.Config {
	t.Helper()
	c := &config.Config{}
	c.CommitConfig = cc
	return c
}

func TestCheckCommitMessages(t *testing.T) {
	block := &generated.CommitConfig{Message: &generated.Message{Block: &generated.MessageBlock{
		Literals: []string{"secret"},
		Patterns: []string{`api[_-]?key`},
	}}}

	tests := []struct {
		name      string
		cfg       *config.Config
		message   string
		wantError bool
	}{
		{"clean message, no config", nil, "a normal commit", false},
		{"blocked literal (case-insensitive)", commitCfg(t, block), "added SECRET token", true},
		{"blocked pattern", commitCfg(t, block), "set api_key here", true},
		{"allowed message with config", commitCfg(t, block), "ordinary change", false},
		{"empty message is illegal", commitCfg(t, block), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{cfg: tc.cfg}
			a := actionWithCommits(db.CommitData{Message: tc.message})
			a, _ = e.checkCommitMessages(context.Background(), nil, a)
			if a.Error != tc.wantError {
				t.Errorf("error = %v, want %v", a.Error, tc.wantError)
			}
		})
	}
}

func TestCheckAuthorEmails(t *testing.T) {
	rules := &generated.CommitConfig{Author: &generated.Author{Email: &generated.Email{
		Domain: &generated.Domain{Allow: strPtrV("example\\.com$")},
		Local:  &generated.LocalClass{Block: strPtrV("^blocked")},
	}}}

	tests := []struct {
		name      string
		cfg       *config.Config
		email     string
		wantError bool
	}{
		{"valid email, no rules", nil, "alice@anywhere.io", false},
		{"invalid email", nil, "not-an-email", true},
		{"allowed domain", commitCfg(t, rules), "alice@example.com", false},
		{"disallowed domain", commitCfg(t, rules), "alice@evil.org", true},
		{"blocked local part", commitCfg(t, rules), "blocked.user@example.com", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{cfg: tc.cfg}
			a := actionWithCommits(db.CommitData{AuthorEmail: tc.email})
			a, err := e.checkAuthorEmails(context.Background(), nil, a)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if a.Error != tc.wantError {
				t.Errorf("error = %v, want %v", a.Error, tc.wantError)
			}
		})
	}
}

func TestCheckUserPushPermission(t *testing.T) {
	const url = "https://github.com/x/y.git"
	pushAction := func() *Action {
		a := NewAction("id", "push", http.MethodPost, 0, url)
		a.UserEmail = "bob@example.com"
		return a
	}

	t.Run("allowed when on canPush", func(t *testing.T) {
		fs := &fakeStore{
			repoByURL:    map[string]*db.Repo{url: {Users: db.RepoUsers{CanPush: []string{"bob"}}}},
			usersByEmail: map[string][]*db.User{"bob@example.com": {{Username: "bob"}}},
		}
		a, _ := (&Engine{store: fs}).checkUserPushPermission(context.Background(), nil, pushAction())
		if a.Error {
			t.Errorf("expected push allowed, got error %v", a.ErrorMessage)
		}
	})

	t.Run("blocked when not on any list", func(t *testing.T) {
		fs := &fakeStore{
			repoByURL:    map[string]*db.Repo{url: {Users: db.RepoUsers{CanPush: []string{"someone-else"}}}},
			usersByEmail: map[string][]*db.User{"bob@example.com": {{Username: "bob"}}},
		}
		a, _ := (&Engine{store: fs}).checkUserPushPermission(context.Background(), nil, pushAction())
		if !a.Error {
			t.Error("expected push blocked for a user not on the access lists")
		}
	})

	t.Run("blocked when no user matches the email", func(t *testing.T) {
		fs := &fakeStore{repoByURL: map[string]*db.Repo{url: {}}}
		a, _ := (&Engine{store: fs}).checkUserPushPermission(context.Background(), nil, pushAction())
		if !a.Error {
			t.Error("expected push blocked when no user has the committer email")
		}
	})

	t.Run("blocked when multiple users share the email", func(t *testing.T) {
		fs := &fakeStore{usersByEmail: map[string][]*db.User{"bob@example.com": {{Username: "bob"}, {Username: "bob2"}}}}
		a, _ := (&Engine{store: fs}).checkUserPushPermission(context.Background(), nil, pushAction())
		if !a.Error {
			t.Error("expected push blocked when the email is ambiguous")
		}
	})

	t.Run("blocked when no committer email", func(t *testing.T) {
		a := NewAction("id", "push", http.MethodPost, 0, url)
		a, _ = (&Engine{store: &fakeStore{}}).checkUserPushPermission(context.Background(), nil, a)
		if !a.Error {
			t.Error("expected push blocked when there is no committer email")
		}
	})
}

func strPtrV(s string) *string { return &s }

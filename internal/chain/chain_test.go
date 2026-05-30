// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// fakeStore records the chain's store interactions for assertions.
type fakeStore struct {
	repoByURL    map[string]*db.Repo
	usersByEmail map[string][]*db.User
	pushByID     map[string]*db.Push
	getErr       error
	audited      []*db.Push
	authorised   []string
	rejected     []string
}

func (f *fakeStore) GetPush(_ context.Context, id string) (*db.Push, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.pushByID[id], nil
}

func (f *fakeStore) GetRepoByURL(_ context.Context, url string) (*db.Repo, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.repoByURL[url], nil
}
func (f *fakeStore) GetUsers(_ context.Context, q db.UserQuery) ([]*db.User, error) {
	if q.Email != nil {
		return f.usersByEmail[*q.Email], nil
	}
	return nil, nil
}
func (f *fakeStore) WriteAudit(_ context.Context, p *db.Push) error {
	f.audited = append(f.audited, p)
	return nil
}
func (f *fakeStore) Authorise(_ context.Context, id string, _ *db.Attestation) (string, error) {
	f.authorised = append(f.authorised, id)
	return id, nil
}
func (f *fakeStore) Reject(_ context.Context, id string, _ db.Rejection) (string, error) {
	f.rejected = append(f.rejected, id)
	return id, nil
}

func pushRequest(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/github.com/finos/git-proxy.git/git-receive-pack", nil)
	r.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	return r
}

func TestExecutePushAuditsAndForwards(t *testing.T) {
	fs := &fakeStore{}
	e := &Engine{store: fs}

	action := e.Execute(context.Background(), pushRequest(t))
	if action.Type != "push" {
		t.Errorf("type = %q, want push", action.Type)
	}
	if !action.Continue() {
		t.Error("empty push chain should leave the action continuable")
	}
	if action.AllowPush {
		t.Error("allowPush should be false")
	}
	if len(fs.audited) != 1 {
		t.Errorf("audited %d, want 1", len(fs.audited))
	}
}

func TestExecutePullNotAudited(t *testing.T) {
	fs := &fakeStore{}
	e := &Engine{store: fs}

	r := httptest.NewRequest(http.MethodPost, "/github.com/finos/git-proxy.git/git-upload-pack", nil)
	r.Header.Set("Content-Type", "application/x-git-upload-pack-request")

	action := e.Execute(context.Background(), r)
	if action.Type != "pull" {
		t.Errorf("type = %q, want pull", action.Type)
	}
	if len(fs.audited) != 0 {
		t.Errorf("audited %d, want 0 for a pull", len(fs.audited))
	}
}

func TestExecuteStopsOnBlock(t *testing.T) {
	fs := &fakeStore{}
	ran := false
	block := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		s := NewStep("block")
		s.SetAsyncBlock("needs review")
		a.AddStep(s)
		return a, nil
	}
	record := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		ran = true
		return a, nil
	}
	e := &Engine{store: fs, pushChain: []Processor{block, record}}

	action := e.Execute(context.Background(), pushRequest(t))
	if !action.Blocked {
		t.Error("action should be blocked")
	}
	if ran {
		t.Error("processor after a block must not run")
	}
	if len(fs.audited) != 1 {
		t.Errorf("audited %d, want 1", len(fs.audited))
	}
}

func TestExecuteStopsOnError(t *testing.T) {
	fs := &fakeStore{}
	ran := false
	boom := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		return a, errors.New("boom")
	}
	record := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		ran = true
		return a, nil
	}
	e := &Engine{store: fs, pushChain: []Processor{boom, record}}

	action := e.Execute(context.Background(), pushRequest(t))
	if !action.Error || action.ErrorMessage == nil || *action.ErrorMessage != "boom" {
		t.Errorf("error not recorded: error=%v msg=%v", action.Error, action.ErrorMessage)
	}
	if ran {
		t.Error("processor after an error must not run")
	}
	if len(fs.audited) != 1 {
		t.Errorf("audited %d, want 1 (audit runs even on error)", len(fs.audited))
	}
}

func TestExecuteStopsOnAllowPush(t *testing.T) {
	fs := &fakeStore{}
	ran := false
	allow := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		a.SetAllowPush()
		return a, nil
	}
	record := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		ran = true
		return a, nil
	}
	e := &Engine{store: fs, pushChain: []Processor{allow, record}}

	action := e.Execute(context.Background(), pushRequest(t))
	if !action.AllowPush {
		t.Error("allowPush should be set")
	}
	if ran {
		t.Error("processor after allowPush must not run")
	}
}

func TestExecuteRunsAllPassingProcessors(t *testing.T) {
	fs := &fakeStore{}
	var order []string
	mk := func(name string) Processor {
		return func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
			order = append(order, name)
			return a, nil
		}
	}
	e := &Engine{store: fs, pushChain: []Processor{mk("a"), mk("b"), mk("c")}}

	e.Execute(context.Background(), pushRequest(t))
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("processor order = %v, want [a b c]", order)
	}
}

func TestExecuteAutoApproval(t *testing.T) {
	fs := &fakeStore{}
	approve := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		a.SetAutoApproval()
		return a, nil
	}
	e := &Engine{store: fs, pushChain: []Processor{approve}}

	action := e.Execute(context.Background(), pushRequest(t))
	if len(fs.authorised) != 1 || fs.authorised[0] != action.ID {
		t.Errorf("authorised = %v, want [%s]", fs.authorised, action.ID)
	}
	if len(fs.rejected) != 0 {
		t.Errorf("rejected = %v, want none", fs.rejected)
	}
}

func TestExecuteAutoRejection(t *testing.T) {
	fs := &fakeStore{}
	reject := func(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
		a.SetAutoRejection()
		return a, nil
	}
	e := &Engine{store: fs, pushChain: []Processor{reject}}

	action := e.Execute(context.Background(), pushRequest(t))
	if len(fs.rejected) != 1 || fs.rejected[0] != action.ID {
		t.Errorf("rejected = %v, want [%s]", fs.rejected, action.ID)
	}
	if len(fs.authorised) != 0 {
		t.Errorf("authorised = %v, want none", fs.authorised)
	}
}

func TestParseActionTypeAndURL(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		repoSeed    map[string]*db.Repo
		wantType    string
		wantURL     string
	}{
		{
			name:     "default type for refs GET",
			path:     "/github.com/finos/git-proxy.git/info/refs?service=git-upload-pack",
			repoSeed: map[string]*db.Repo{"https://github.com/finos/git-proxy.git": {}},
			wantType: "default",
			wantURL:  "https://github.com/finos/git-proxy.git",
		},
		{
			name:        "push type from receive-pack content-type",
			path:        "/github.com/finos/git-proxy.git/git-receive-pack",
			contentType: "application/x-git-receive-pack-request",
			repoSeed:    map[string]*db.Repo{"https://github.com/finos/git-proxy.git": {}},
			wantType:    "push",
			wantURL:     "https://github.com/finos/git-proxy.git",
		},
		{
			name:        "pull type from upload-pack content-type",
			path:        "/github.com/finos/git-proxy.git/git-upload-pack",
			contentType: "application/x-git-upload-pack-request",
			repoSeed:    map[string]*db.Repo{"https://github.com/finos/git-proxy.git": {}},
			wantType:    "pull",
			wantURL:     "https://github.com/finos/git-proxy.git",
		},
		{
			name:     "legacy host-less path falls back to github.com",
			path:     "/finos/git-proxy.git/info/refs?service=git-upload-pack",
			repoSeed: nil, // store has no repo at https://finos/... so we fall back
			wantType: "default",
			wantURL:  "https://github.com/finos/git-proxy.git",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{repoByURL: tc.repoSeed}
			e := &Engine{store: fs}
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			a, err := e.parseAction(context.Background(), r)
			if err != nil {
				t.Fatalf("parseAction: %v", err)
			}
			if a.Type != tc.wantType {
				t.Errorf("type = %q, want %q", a.Type, tc.wantType)
			}
			if a.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", a.URL, tc.wantURL)
			}
		})
	}
}

func TestCheckRepoInAuthorisedList(t *testing.T) {
	const url = "https://github.com/finos/git-proxy.git"

	// In the authorised list -> step logged, action still continuable.
	fs := &fakeStore{repoByURL: map[string]*db.Repo{url: {}}}
	e := &Engine{store: fs}
	a := NewAction("id", "push", http.MethodPost, 0, url)
	a, err := e.checkRepoInAuthorisedList(context.Background(), nil, a)
	if err != nil {
		t.Fatalf("checkRepoInAuthorisedList: %v", err)
	}
	if !a.Continue() {
		t.Error("authorised repo should leave the action continuable")
	}
	if len(a.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(a.Steps))
	}

	// Not in the list -> action errored.
	fs = &fakeStore{}
	e = &Engine{store: fs}
	a = NewAction("id", "push", http.MethodPost, 0, url)
	a, _ = e.checkRepoInAuthorisedList(context.Background(), nil, a)
	if a.Continue() {
		t.Error("unauthorised repo should stop the action")
	}
	if a.ErrorMessage == nil || !strings.Contains(*a.ErrorMessage, "not in the authorised whitelist") {
		t.Errorf("errorMessage = %v, want whitelist rejection", a.ErrorMessage)
	}

	// Store error -> propagated (the chain marks the action errored).
	fs = &fakeStore{getErr: errors.New("db down")}
	e = &Engine{store: fs}
	a = NewAction("id", "push", http.MethodPost, 0, url)
	if _, err := e.checkRepoInAuthorisedList(context.Background(), nil, a); err == nil {
		t.Error("expected store error to propagate")
	}
}

func TestNewEngineRunsCheckRepo(t *testing.T) {
	const url = "https://github.com/finos/git-proxy.git"

	// A valid push (committer Bob on canPush) passes every validator; the chain
	// then reaches pullRemote, which errors because pushRequest carries no auth
	// header — confirming the full pre-clone chain ran and was wired in order.
	body := buildReceivePack(t, "2222222222222222222222222222222222222222",
		"1111111111111111111111111111111111111111", "refs/heads/main", sampleCommit("abc123"))
	fs := &fakeStore{
		repoByURL:    map[string]*db.Repo{url: {Users: db.RepoUsers{CanPush: []string{"bob"}}}},
		usersByEmail: map[string][]*db.User{"bob@example.com": {{Username: "bob"}}},
	}
	e := NewEngine(fs, nil, "", "")
	e.remoteDir = t.TempDir() // avoid touching ./.remote
	action := e.Execute(rawCtx(body), pushRequest(t))
	if action.LastStep == nil || action.LastStep.StepName != "pullRemote" {
		t.Fatalf("expected the chain to reach pullRemote; last step = %+v", action.LastStep)
	}
	if !action.Error {
		t.Error("expected pullRemote to error without an auth header")
	}
	if len(fs.audited) != 1 {
		t.Errorf("audited %d, want 1", len(fs.audited))
	}

	// Unauthorised pull: errored via the pull chain, and not audited.
	fs = &fakeStore{}
	r := httptest.NewRequest(http.MethodPost, "/github.com/finos/git-proxy.git/git-upload-pack", nil)
	r.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	action = NewEngine(fs, nil, "", "").Execute(context.Background(), r)
	if !action.Error {
		t.Error("unauthorised pull should be errored by checkRepoInAuthorisedList")
	}
	if len(fs.audited) != 0 {
		t.Errorf("audited %d, want 0 for a pull", len(fs.audited))
	}
}

func TestExecuteParseActionStoreError(t *testing.T) {
	fs := &fakeStore{getErr: errors.New("db down")}
	e := &Engine{store: fs}

	action := e.Execute(context.Background(), pushRequest(t))
	if !action.Error {
		t.Error("store failure in parseAction should mark the action errored")
	}
	if len(fs.audited) != 1 {
		t.Errorf("audited %d, want 1 (audit runs even when parseAction fails)", len(fs.audited))
	}
}

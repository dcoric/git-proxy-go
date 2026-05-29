// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package db

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestPushJSONRoundTrip proves a fully-populated Push survives a JSONB
// round-trip unchanged — the property the pushes.data column relies on.
func TestPushJSONRoundTrip(t *testing.T) {
	msg := "boom"
	p := &Push{
		ID:        "abc__def",
		Type:      "push",
		Method:    "POST",
		Timestamp: 1717000000000,
		Project:   "finos",
		RepoName:  "git-proxy",
		URL:       "https://github.com/finos/git-proxy.git",
		Repo:      "finos/git-proxy.git",
		Steps: []Step{{
			ID:           "step-1",
			StepName:     "checkCommitMessages",
			Content:      map[string]any{"detail": "value"},
			Error:        true,
			ErrorMessage: &msg,
			Logs:         []string{"checkCommitMessages - boom"},
		}},
		Error:      true,
		Blocked:    true,
		CommitData: []CommitData{{Tree: "t", Author: "a", AuthorEmail: "a@b.c", Message: "m"}},
		CommitFrom: "abc",
		CommitTo:   "def",
		Branch:     "refs/heads/main",
		User:       "alice",
		UserEmail:  "alice@example.com",
		Attestation: &Attestation{
			Reviewer:  Reviewer{Username: "bob", Email: "bob@example.com"},
			Timestamp: "2026-05-29T00:00:00Z",
			Answers:   []AttestationAnswer{{Label: "ok?", Checked: true}},
		},
	}

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Push
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(*p, got) {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, *p)
	}
}

// TestDefaultPushQuery matches defaultPushQuery in src/db/mongo/pushes.ts.
func TestDefaultPushQuery(t *testing.T) {
	q := DefaultPushQuery()
	if q.Type == nil || *q.Type != "push" {
		t.Errorf("Type = %v, want push", q.Type)
	}
	for name, got := range map[string]*bool{"error": q.Error, "allowPush": q.AllowPush, "authorised": q.Authorised} {
		if got == nil || *got != false {
			t.Errorf("%s = %v, want false", name, got)
		}
	}
	if q.Blocked == nil || *q.Blocked != true {
		t.Errorf("Blocked = %v, want true", q.Blocked)
	}
	if q.Canceled != nil || q.Rejected != nil {
		t.Errorf("Canceled/Rejected should be unset (nil)")
	}
}

// TestRepoUsersJSONShape pins the nested users object shape the UI consumes.
func TestRepoUsersJSONShape(t *testing.T) {
	r := &Repo{
		ID: "id1", Project: "finos", Name: "git-proxy", URL: "https://x/y.git",
		Users: RepoUsers{CanPush: []string{"alice"}, CanAuthorise: []string{}},
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	users, ok := m["users"].(map[string]any)
	if !ok {
		t.Fatalf("users not an object: %v", m["users"])
	}
	if _, ok := users["canPush"]; !ok {
		t.Error("users.canPush missing")
	}
	if _, ok := m["_id"]; !ok {
		t.Error("_id missing")
	}
}

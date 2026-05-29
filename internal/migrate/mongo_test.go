// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package migrate

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// TestDecodeMongoRepo proves the §7 landmine is handled: a repo document's
// ObjectID _id decodes to its hex string and the nested users object maps
// correctly.
func TestDecodeMongoRepo(t *testing.T) {
	oid := bson.NewObjectID()
	doc := bson.M{
		"_id":     oid,
		"project": "finos",
		"name":    "git-proxy",
		"url":     "https://github.com/finos/git-proxy.git",
		"users":   bson.M{"canPush": bson.A{"alice"}, "canAuthorise": bson.A{}},
	}

	r, err := decodeMongoDoc[db.Repo](doc)
	if err != nil {
		t.Fatalf("decodeMongoDoc: %v", err)
	}
	if r.ID != oid.Hex() {
		t.Errorf("ID = %q, want hex string %q", r.ID, oid.Hex())
	}
	if r.Project != "finos" || r.Name != "git-proxy" {
		t.Errorf("repo fields wrong: %+v", r)
	}
	if !reflect.DeepEqual(r.Users.CanPush, []string{"alice"}) {
		t.Errorf("canPush = %v, want [alice]", r.Users.CanPush)
	}
}

// TestDecodeMongoPush covers a push doc with an ObjectID _id (dropped), an
// integer timestamp and nested arrays.
func TestDecodeMongoPush(t *testing.T) {
	doc := bson.M{
		"_id":       bson.NewObjectID(),
		"id":        "from__to",
		"type":      "push",
		"blocked":   true,
		"timestamp": int64(1717000000000),
		"steps": bson.A{
			bson.M{"id": "s1", "stepName": "parsePush", "logs": bson.A{"parsePush - ok"}},
		},
		"commitData": bson.A{
			bson.M{"author": "alice", "authorEmail": "a@b.c", "message": "m"},
		},
	}

	p, err := decodeMongoDoc[db.Push](doc)
	if err != nil {
		t.Fatalf("decodeMongoDoc: %v", err)
	}
	if p.ID != "from__to" || !p.Blocked || p.Timestamp != 1717000000000 {
		t.Errorf("push scalar fields wrong: %+v", p)
	}
	if len(p.Steps) != 1 || p.Steps[0].StepName != "parsePush" {
		t.Errorf("steps not decoded: %+v", p.Steps)
	}
	if len(p.CommitData) != 1 || p.CommitData[0].AuthorEmail != "a@b.c" {
		t.Errorf("commitData not decoded: %+v", p.CommitData)
	}
}

// TestNormalizeBSONNested checks ObjectIDs are converted even when nested in
// arrays and sub-documents.
func TestNormalizeBSONNested(t *testing.T) {
	oid := bson.NewObjectID()
	in := bson.M{
		"list":   bson.A{oid, "plain"},
		"nested": bson.M{"ref": oid},
	}
	out, ok := normalizeBSON(in).(map[string]any)
	if !ok {
		t.Fatalf("normalizeBSON did not return a map: %T", normalizeBSON(in))
	}
	list := out["list"].([]any)
	if list[0] != oid.Hex() {
		t.Errorf("nested-array ObjectID not converted: %v", list[0])
	}
	if out["nested"].(map[string]any)["ref"] != oid.Hex() {
		t.Errorf("nested-doc ObjectID not converted: %v", out["nested"])
	}
}

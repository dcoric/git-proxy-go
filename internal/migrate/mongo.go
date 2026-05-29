// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// MongoDB collection names (the Node collections in src/db/mongo).
const (
	mongoUsersColl  = "users"
	mongoReposColl  = "repos"
	mongoPushesColl = "pushes"
)

// MongoSource reads users/repos/pushes from a MongoDB database. Documents are
// normalised to JSON before decoding into the domain types; in particular
// bson.ObjectID values are converted to their hex string (the §7 landmine:
// ObjectIDs round-trip as strings in the Node world).
type MongoSource struct {
	client *mongo.Client
	db     *mongo.Database
}

// NewMongoSource connects to uri and selects database dbName.
func NewMongoSource(ctx context.Context, uri, dbName string) (*MongoSource, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect mongo: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ping mongo: %w", err)
	}
	return &MongoSource{client: client, db: client.Database(dbName)}, nil
}

// Kind identifies the source.
func (s *MongoSource) Kind() string { return "mongo" }

// Close disconnects the client.
func (s *MongoSource) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

// Users reads the users collection.
func (s *MongoSource) Users(ctx context.Context) ([]*db.User, error) {
	return readMongo[db.User](ctx, s.db, mongoUsersColl)
}

// Repos reads the repos collection.
func (s *MongoSource) Repos(ctx context.Context) ([]*db.Repo, error) {
	return readMongo[db.Repo](ctx, s.db, mongoReposColl)
}

// Pushes reads the pushes collection.
func (s *MongoSource) Pushes(ctx context.Context) ([]*db.Push, error) {
	return readMongo[db.Push](ctx, s.db, mongoPushesColl)
}

// readMongo reads every document from a collection, normalises BSON-specific
// values to JSON-friendly ones and decodes into T.
func readMongo[T any](ctx context.Context, d *mongo.Database, coll string) ([]*T, error) {
	cur, err := d.Collection(coll).Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", coll, err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var out []*T
	for cur.Next(ctx) {
		var raw bson.M
		if err := cur.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode %s document: %w", coll, err)
		}
		v, err := decodeMongoDoc[T](raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", coll, err)
		}
		out = append(out, v)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("cursor %s: %w", coll, err)
	}
	return out, nil
}

// decodeMongoDoc normalises a raw BSON document and decodes it into T. Split out
// from readMongo so it can be unit-tested without a live MongoDB.
func decodeMongoDoc[T any](raw bson.M) (*T, error) {
	jsonBytes, err := json.Marshal(normalizeBSON(raw))
	if err != nil {
		return nil, fmt.Errorf("marshal normalised doc: %w", err)
	}
	var v T
	if err := json.Unmarshal(jsonBytes, &v); err != nil {
		return nil, fmt.Errorf("decode doc: %w", err)
	}
	return &v, nil
}

// normalizeBSON recursively converts BSON-specific values into plain JSON
// values: ObjectIDs become hex strings, DateTimes become RFC3339, and the
// container types are walked. Everything else is left for encoding/json.
func normalizeBSON(v any) any {
	switch val := v.(type) {
	case bson.ObjectID:
		return val.Hex()
	case bson.M:
		return normalizeMap(val)
	case map[string]any:
		return normalizeMap(val)
	case bson.D:
		m := make(map[string]any, len(val))
		for _, e := range val {
			m[e.Key] = normalizeBSON(e.Value)
		}
		return m
	case bson.A:
		return normalizeSlice(val)
	case []any:
		return normalizeSlice(val)
	case bson.DateTime:
		return val.Time().UTC().Format(time.RFC3339Nano)
	default:
		return val
	}
}

func normalizeMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = normalizeBSON(v)
	}
	return out
}

func normalizeSlice(a []any) []any {
	out := make([]any, len(a))
	for i, v := range a {
		out[i] = normalizeBSON(v)
	}
	return out
}

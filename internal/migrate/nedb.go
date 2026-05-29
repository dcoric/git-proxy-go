// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package migrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// neDB collection file names, relative to the data directory (Node ./.data/db).
const (
	neDBUsersFile  = "users.db"
	neDBReposFile  = "repos.db"
	neDBPushesFile = "pushes.db"
)

// NeDBSource reads users/repos/pushes from a neDB data directory. neDB persists
// each collection as an append-only NDJSON log: data lines, `{$$deleted:true,
// _id}` tombstones and `{$$indexCreated|$$indexRemoved:...}` index lines. This
// reader replays the log to the final document set (applying tombstones,
// ignoring index lines), mirroring neDB's own treatRawData.
type NeDBSource struct {
	dir string
}

// NewNeDBSource returns a reader over the neDB directory dir (e.g. ./.data/db).
func NewNeDBSource(dir string) *NeDBSource { return &NeDBSource{dir: dir} }

// Kind identifies the source.
func (s *NeDBSource) Kind() string { return "nedb" }

// Close is a no-op (no open handles are retained).
func (s *NeDBSource) Close(context.Context) error { return nil }

// Users reads users.db.
func (s *NeDBSource) Users(context.Context) ([]*db.User, error) {
	return readNeDB[db.User](filepath.Join(s.dir, neDBUsersFile))
}

// Repos reads repos.db.
func (s *NeDBSource) Repos(context.Context) ([]*db.Repo, error) {
	return readNeDB[db.Repo](filepath.Join(s.dir, neDBReposFile))
}

// Pushes reads pushes.db.
func (s *NeDBSource) Pushes(context.Context) ([]*db.Push, error) {
	return readNeDB[db.Push](filepath.Join(s.dir, neDBPushesFile))
}

// readNeDB replays a neDB collection file and decodes the live documents into T.
// A missing file is treated as an empty collection (not an error).
func readNeDB[T any](path string) ([]*T, error) {
	f, err := os.Open(path) //nolint:gosec // path is an operator-supplied data dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	live, order, err := replayNeDB(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	out := make([]*T, 0, len(order))
	for _, id := range order {
		var v T
		if err := json.Unmarshal(live[id], &v); err != nil {
			return nil, fmt.Errorf("%s: decode doc %q: %w", path, id, err)
		}
		out = append(out, &v)
	}
	return out, nil
}

// neDBLine captures the meta fields needed to interpret a persistence log line.
type neDBLine struct {
	ID           string          `json:"_id"`
	Deleted      bool            `json:"$$deleted"`
	IndexCreated json.RawMessage `json:"$$indexCreated"`
	IndexRemoved json.RawMessage `json:"$$indexRemoved"`
}

// replayNeDB reduces the append-only log to the live documents, preserving
// first-seen order for deterministic output.
func replayNeDB(r io.Reader) (map[string]json.RawMessage, []string, error) {
	live := map[string]json.RawMessage{}
	var order []string

	br := bufio.NewReader(r)
	for {
		raw, readErr := br.ReadBytes('\n')
		line := bytes.TrimSpace(raw)
		if len(line) > 0 {
			var meta neDBLine
			if err := json.Unmarshal(line, &meta); err != nil {
				return nil, nil, fmt.Errorf("parse line: %w", err)
			}
			switch {
			case meta.IndexCreated != nil || meta.IndexRemoved != nil:
				// index definitions, not data
			case meta.ID == "":
				// every neDB document has an _id; ignore anything else defensively
			case meta.Deleted:
				if _, ok := live[meta.ID]; ok {
					delete(live, meta.ID)
					order = removeString(order, meta.ID)
				}
			default:
				if _, ok := live[meta.ID]; !ok {
					order = append(order, meta.ID)
				}
				live[meta.ID] = bytes.Clone(line)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	return live, order, nil
}

func removeString(s []string, target string) []string {
	for i, v := range s {
		if v == target {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

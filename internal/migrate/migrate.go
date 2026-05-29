// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package migrate is the one-shot ETL that moves existing git-proxy data
// (neDB files or a MongoDB database) into Postgres (P2-4..P2-6). A Source reads
// users/repos/pushes from the old store; Run loads them into a db.Store. It is
// dry-run by default and produces a typed audit Report of every record.
package migrate

import (
	"context"
	"fmt"

	"github.com/dcoric/git-proxy-go/internal/db"
)

// Action is the outcome recorded for a single migrated record.
type Action string

const (
	// ActionWouldCreate / ActionWouldUpdate are emitted in dry-run mode.
	ActionWouldCreate Action = "would-create"
	ActionWouldUpdate Action = "would-update"
	// ActionCreated / ActionUpdated are emitted when changes are applied.
	ActionCreated Action = "created"
	ActionUpdated Action = "updated"
	// ActionSkipped means the record already exists and was left untouched.
	ActionSkipped Action = "skipped-exists"
	// ActionError means the record could not be migrated.
	ActionError Action = "error"
)

// Record is one line of the typed audit log.
type Record struct {
	Collection string `json:"collection"` // users | repos | pushes
	Key        string `json:"key"`        // username | repo url | push id
	Action     Action `json:"action"`
	Error      string `json:"error,omitempty"`
}

// Report is the full audit of a migration run.
type Report struct {
	Source  string         `json:"source"`
	DryRun  bool           `json:"dryRun"`
	Records []Record       `json:"records"`
	Summary map[Action]int `json:"summary"`
}

// HasErrors reports whether any record failed.
func (r *Report) HasErrors() bool { return r.Summary[ActionError] > 0 }

func (r *Report) add(collection, key string, action Action, err error) {
	rec := Record{Collection: collection, Key: key, Action: action}
	if err != nil {
		rec.Action = ActionError
		rec.Error = err.Error()
	}
	r.Records = append(r.Records, rec)
	r.Summary[rec.Action]++
}

// Options controls a migration run.
type Options struct {
	// DryRun reports what would change without writing. Defaults to true at the
	// CLI; the zero value here is also dry-run-safe only if the caller sets it.
	DryRun bool
}

// Run reads every collection from src and loads it into store, returning the
// audit Report. Existing users/repos are skipped (never clobbered); pushes are
// upserted (writeAudit semantics). In dry-run mode nothing is written.
func Run(ctx context.Context, src Source, store db.Store, opts Options) (*Report, error) {
	rep := &Report{Source: src.Kind(), DryRun: opts.DryRun, Summary: map[Action]int{}}

	if err := migrateUsers(ctx, src, store, opts, rep); err != nil {
		return rep, err
	}
	if err := migrateRepos(ctx, src, store, opts, rep); err != nil {
		return rep, err
	}
	if err := migratePushes(ctx, src, store, opts, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

func migrateUsers(ctx context.Context, src Source, store db.Store, opts Options, rep *Report) error {
	users, err := src.Users(ctx)
	if err != nil {
		return fmt.Errorf("read users: %w", err)
	}
	for _, u := range users {
		existing, err := store.FindUser(ctx, u.Username)
		if err != nil {
			rep.add("users", u.Username, ActionError, err)
			continue
		}
		if existing != nil {
			rep.add("users", u.Username, ActionSkipped, nil)
			continue
		}
		if opts.DryRun {
			rep.add("users", u.Username, ActionWouldCreate, nil)
			continue
		}
		rep.add("users", u.Username, ActionCreated, store.CreateUser(ctx, u))
	}
	return nil
}

func migrateRepos(ctx context.Context, src Source, store db.Store, opts Options, rep *Report) error {
	repos, err := src.Repos(ctx)
	if err != nil {
		return fmt.Errorf("read repos: %w", err)
	}
	for _, r := range repos {
		existing, err := store.GetRepoByURL(ctx, r.URL)
		if err != nil {
			rep.add("repos", r.URL, ActionError, err)
			continue
		}
		if existing != nil {
			rep.add("repos", r.URL, ActionSkipped, nil)
			continue
		}
		if opts.DryRun {
			rep.add("repos", r.URL, ActionWouldCreate, nil)
			continue
		}
		_, err = store.CreateRepo(ctx, r)
		rep.add("repos", r.URL, ActionCreated, err)
	}
	return nil
}

func migratePushes(ctx context.Context, src Source, store db.Store, opts Options, rep *Report) error {
	pushes, err := src.Pushes(ctx)
	if err != nil {
		return fmt.Errorf("read pushes: %w", err)
	}
	for _, p := range pushes {
		existing, err := store.GetPush(ctx, p.ID)
		if err != nil {
			rep.add("pushes", p.ID, ActionError, err)
			continue
		}
		if opts.DryRun {
			if existing != nil {
				rep.add("pushes", p.ID, ActionWouldUpdate, nil)
			} else {
				rep.add("pushes", p.ID, ActionWouldCreate, nil)
			}
			continue
		}
		writeErr := store.WriteAudit(ctx, p)
		action := ActionCreated
		if existing != nil {
			action = ActionUpdated
		}
		rep.add("pushes", p.ID, action, writeErr)
	}
	return nil
}

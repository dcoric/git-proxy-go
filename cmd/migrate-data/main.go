// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Command migrate-data is a one-shot ETL that migrates existing neDB or MongoDB
// git-proxy data into Postgres (P2-4..P2-6).
//
// It is DRY-RUN BY DEFAULT: it reports what it would write and changes nothing.
// Pass --apply to actually write. The typed audit log is emitted as JSON.
//
//	migrate-data --source nedb  --nedb-dir ./.data/db        --postgres "postgres://…" [--apply]
//	migrate-data --source mongo --mongo-uri mongodb://… --mongo-db gitproxy --postgres "postgres://…" [--apply]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/dcoric/git-proxy-go/internal/db/migrations"
	"github.com/dcoric/git-proxy-go/internal/db/postgres"
	"github.com/dcoric/git-proxy-go/internal/migrate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate-data:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("migrate-data", flag.ContinueOnError)
	var (
		source      = fs.String("source", "", "data source: nedb | mongo (required)")
		nedbDir     = fs.String("nedb-dir", "./.data/db", "neDB data directory (source=nedb)")
		mongoURI    = fs.String("mongo-uri", "", "MongoDB connection string (source=mongo)")
		mongoDB     = fs.String("mongo-db", "gitproxy", "MongoDB database name (source=mongo)")
		postgresDSN = fs.String("postgres", "", "Postgres DSN to migrate into (required)")
		migrateUp   = fs.Bool("migrate", true, "apply Postgres schema migrations before loading (only with --apply)")
		apply       = fs.Bool("apply", false, "WRITE changes; without this the run is a dry-run")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *postgresDSN == "" {
		return errors.New("--postgres is required")
	}

	ctx := context.Background()

	src, err := openSource(ctx, *source, *nedbDir, *mongoURI, *mongoDB)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close(ctx) }()

	if *apply && *migrateUp {
		if err := migrations.Up(ctx, *postgresDSN); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
	}

	store, err := postgres.Connect(ctx, *postgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	report, runErr := migrate.Run(ctx, src, store, migrate.Options{DryRun: !*apply})
	if emitErr := emitReport(report); emitErr != nil {
		return emitErr
	}
	if runErr != nil {
		return runErr
	}
	if report.HasErrors() {
		return fmt.Errorf("%d record(s) failed to migrate", report.Summary[migrate.ActionError])
	}
	return nil
}

func openSource(ctx context.Context, source, nedbDir, mongoURI, mongoDB string) (migrate.Source, error) {
	switch source {
	case "nedb":
		return migrate.NewNeDBSource(nedbDir), nil
	case "mongo":
		if mongoURI == "" {
			return nil, errors.New("--mongo-uri is required for --source mongo")
		}
		return migrate.NewMongoSource(ctx, mongoURI, mongoDB)
	case "":
		return nil, errors.New("--source is required (nedb | mongo)")
	default:
		return nil, fmt.Errorf("unknown --source %q (want nedb | mongo)", source)
	}
}

// emitReport writes the typed audit log as indented JSON to stdout.
func emitReport(report *migrate.Report) error {
	if report == nil {
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

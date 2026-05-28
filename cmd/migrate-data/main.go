// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Command migrate-data is a one-shot ETL that migrates existing neDB and
// MongoDB data into Postgres. It is dry-run by default.
//
// Skeleton — see GO-REWRITE-TASKS.md tasks P2-4 (neDB reader), P2-5 (MongoDB
// reader) and P2-6 (ETL + typed audit log).
package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("migrate-data: not yet implemented", "tasks", "P2-4..P2-6")
}

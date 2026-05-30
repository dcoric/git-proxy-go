# Parity harness (P6)

Mirrors the same request to the **Node** and **Go** proxies and reports
divergences — the foundation for the parity gates:

- **P6-1** (this package): the traffic mirror + differs.
- **P6-2**: JSON API byte-diff (`jsondiff.go` — normalises volatile fields, then
  structurally diffs).
- **P6-3**: git ops semantic-diff (`gitrefs.go` — parses ref advertisements into
  ref→SHA maps; ignores pkt-line framing, capability ordering, and pack
  compression, which legitimately differ).
- **P6-5**: the soak driver loops these scenarios and tracks the divergence rate.

## What runs where

- **Unit tests** (`go test ./test/parity/`) exercise the differs and the mirror
  against in-process fakes — these run in CI.
- **Live comparison** (`TestParityAgainstBackends`) runs only when the backend
  URLs are set, so it executes in **staging** where both proxies exist.

## Running against live backends

```sh
export PARITY_NODE_API_URL=http://node-proxy:8080
export PARITY_GO_API_URL=http://go-proxy:8080
export PARITY_NODE_GIT_URL=http://node-proxy:8000
export PARITY_GO_GIT_URL=http://go-proxy:8000
export PARITY_GIT_REPO=github.com/org/repo.git
go test ./test/parity/ -run TestParityAgainstBackends -v
```

Each reported divergence names the scenario, the field (a JSON path, a ref name,
or `status`), and the two values. The API and git scenario sets are independent
— set only the API URLs, only the git URLs, or both.

## Extending

Add scenarios in `scenarios.go` (`DefaultAPIScenarios` / `DefaultGitScenarios`).
Pick the `Kind`: `KindJSON` (normalised structural diff), `KindGitRefs` (semantic
ref diff), or `KindRaw` (exact bytes). Volatile JSON keys to ignore are listed in
`jsondiff.go`.

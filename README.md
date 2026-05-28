# git-proxy-go

A Go port of [`git-proxy`](https://github.com/finos/git-proxy) — an HTTP/SSH git
proxy that enforces push protections and policies through a sequential
processor chain. Ships as a single static binary with no Node runtime.

> **Status: scaffold.** This repo is at the start of the port. The design and
> the full task breakdown live in [`GO-REWRITE-PLAN.md`](./GO-REWRITE-PLAN.md)
> and [`GO-REWRITE-TASKS.md`](./GO-REWRITE-TASKS.md). Progress is tracked in
> [issues](https://github.com/dcoric/git-proxy-go/issues), grouped by the
> milestones **PF, P0–P7**.

## Quick start (dev)

```sh
go build ./...
go test -race ./...
go run ./cmd/git-proxy   # skeleton; serves GET /healthz on :8080
```

## Layout

```
cmd/
  git-proxy/       main entrypoint
  migrate-data/    neDB + MongoDB -> Postgres ETL (one-shot, dry-run default)
internal/
  proxy/http/      HTTP proxy + routes
  proxy/ssh/       SSH server (ported from finos PR #1332) + agent/ forwarding
  chain/           push processor chain + processors/ (15 steps)
  git/             go-git wrappers (clone/fetch) + git-binary exec (receive-pack/diff)
  git/pktline/     pkt-line parser (fuzz target)
  auth/            local / oidc / ad / jwt strategies
  db/              Store interface + postgres/ (sqlc) + migrations/ (goose)
  config/          quicktype-generated structs + koanf loader
  chainext/        placeholder interface for future plugins
api/v1/            OpenAPI spec (P0 deliverable)
docs/              architecture + porting notes (incl. ssh-source-pin.md)
test/golden/       captured request/response fixtures
test/parity/       traffic-mirror harness
```

## Engineering rules

- **Git engine (§3.9):** go-git for network clone/fetch; the `git` binary for
  `receive-pack`/unpack and `diff` (mirrors the Node design, like the gitleaks
  binary). Gated by the P0 go-git spike.
- **Contract-first port:** human writes the Go test from a golden fixture; the
  implementation references the TS source for behaviour, not line-by-line.
- **Fuzz** pkt-line / pack parsing and the SSH agent protocol.
- CI gates: `go build`, `go test -race`, `golangci-lint`, `govulncheck`.

## License

[Apache-2.0](./LICENSE). See [`NOTICE`](./NOTICE).

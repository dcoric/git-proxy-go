# Architecture

git-proxy-go is a Go port of [FINOS git-proxy](https://github.com/finos/git-proxy):
a reverse proxy that sits in front of upstream git hosts (GitHub/GitLab/…) and
runs every push through a policy chain, holding it for review before it reaches
the upstream. It speaks both **smart-HTTP** and **SSH** git transports.

This document is the map of the codebase and the notable deviations from the
Node original. Design rationale lives in [`GO-REWRITE-PLAN.md`](GO-REWRITE-PLAN.md)
and [`docs/decisions/`](docs/decisions/).

## Package layout

```
cmd/
  git-proxy/         entrypoint: wires config, store, auth, the chain, and the
                     HTTP + SSH listeners under one shutdown context
  migrate-data/      one-shot import of legacy NeDB/Mongo data into Postgres
internal/
  config/            embedded JSON schema + defaults + env (ServerEnv)
  db/                domain types, the Store interface, goose migrations
    postgres/        sqlc-generated queries + the Store implementation (pgx)
  auth/              strategies: local (bcrypt), JWT, OIDC; session helpers
  session/           scs session store over the Postgres `sessions` table
  giturl/            git URL/path parsing (shared by chain + proxy)
  chain/             the policy engine: Action/Step + the 15 push processors
  git/               the hybrid git engine (go-git clone + git-binary ops)
  migrate/           legacy-source readers for migrate-data
  proxy/
    http/            management UI/API server + the auth/api routes + CSRF
    git/             the smart-HTTP git reverse proxy
    ssh/             the git-over-SSH server, agent forwarding, host keys
```

## Request flow

Two transports converge on the **same chain**:

- **Smart-HTTP** (`internal/proxy/git`): a `httputil.ReverseProxy`. It buffers
  the pack POST body, runs the chain, and either returns a git error packet
  (blocked) or forwards the request to the host named in the path.
- **SSH** (`internal/proxy/ssh`): an `x/crypto/ssh` server. It authenticates by
  public key, synthesises an `*http.Request` mirroring the HTTP proxy path, runs
  the chain, and forwards approved traffic upstream over SSH using the client's
  forwarded agent.

```
git client ──HTTP──▶ proxy/git ─┐
                                 ├─▶ chain.Engine.Execute(ctx, *http.Request) ─▶ upstream
git client ──SSH───▶ proxy/ssh ─┘        (parseAction → push/pull/default chain)
```

`parseAction` classifies the request from its `Content-Type`
(`application/x-git-{receive,upload}-pack-request`) and resolves the upstream
repo URL from the path, so an SSH-synthesised request takes the same code path
as a real HTTP one.

## The chain (`internal/chain`)

`Engine.Execute` runs an ordered list of `Processor`s for the action type. A
`Processor` is `func(ctx, *http.Request, *Action) (*Action, error)`; processors
are methods on `*Engine`. An `Action` embeds `db.Push` (the audit record) plus
runtime fields; each processor appends a `Step`. The chain stops on the first
step that blocks, errors, or sets `allowPush`.

The full **push** chain (15 processors), in order:

```
parsePush → checkEmptyBranch → checkRepoInAuthorisedList → checkCommitMessages →
checkAuthorEmails → checkUserPushPermission → pullRemote → writePack →
checkHiddenCommits → checkIfWaitingAuth → preReceive → getDiff → gitleaks →
scanDiff → blockForAuth
```

`pull` and `default` chains run only `checkRepoInAuthorisedList`. A normal push
ends **blocked** at `blockForAuth` (held for manual approval); an authorised
retry is let through by `checkIfWaitingAuth`. The pushed pack reaches `parsePush`
via `chain.RawBody(ctx)`, which both transports populate.

## The hybrid git engine (`internal/git`)

Per [decision 0001](docs/decisions/0001-git-engine.md): **go-git for the network
clone, the git binary for everything else** (receive-pack/unpack/diff). The git
binary is authoritative for the operations where byte-exact behaviour matters;
go-git provides a dependency-light, in-process clone.

- `Clone` / `CloneSSH` — go-git network clone (HTTPS basic auth / SSH forwarded
  agent + host-key verification).
- `Run` — runs the git binary in a directory, feeding stdin (used by
  `writePack`, `getDiff`, `checkHiddenCommits`).

## SSH (`internal/proxy/ssh`)

- **Server** — `x/crypto/ssh` (not Node's `ssh2`). Public-key auth only;
  one git command per session.
- **Agent forwarding** — the proxy opens the `auth-agent@openssh.com` channel
  back to the client and authenticates upstream as the user; the client's
  private key never reaches the proxy.
- **Host keys** — an ed25519 host key generated/persisted in-process; upstream
  host keys verified by SHA256 fingerprint against built-in github.com/gitlab.com
  defaults (overridable via `GIT_PROXY_SSH_KNOWN_HOSTS`).
- **Chain routing** — `ProxyHandler` synthesises the chain request, relays the
  upstream ref advertisement, buffers and validates the pack, and forwards only
  if approved. For pushes it puts `SSHCloneAuth` (the forwarded-agent signers +
  host-key callback) on the chain context so `pullRemote` clones over SSH.

The live SSH interop matrix is a staging gate
([`docs/gates/p5-6-ssh-interop.md`](docs/gates/p5-6-ssh-interop.md)).

## Auth & persistence

- **Auth** (`internal/auth`): a strategy registry — local bcrypt, JWT
  (JWKS-verified, role mapping), and OIDC (go-oidc discovery + provisioning).
  Sessions use `scs` over the Postgres `sessions` table; mutating routes are
  CSRF-protected (double-submit cookie). API route groups are JWT-guarded.
- **Persistence** (`internal/db`): Postgres only. Queries are sqlc-generated
  (`pgx`); migrations are embedded goose SQL. The `pushes` table doubles as the
  audit trail (JSONB payload + promoted scalar columns). Legacy NeDB/Mongo data
  is imported once by `cmd/migrate-data`.

## Config (`internal/config`)

`proxy.config.json` is validated against an embedded JSON schema with embedded
defaults (quicktype-generated structs in `generated/`). Listener ports, the SSH
toggle, and DB DSN come from the environment (`ServerEnv`) — the git-proxy needs
`GIT_PROXY_DB_DSN` to enable the store-backed features; without it the binary
serves only the management healthcheck.

## Build, release, CI

- **Container** ([`Dockerfile`](Dockerfile), X-2): multi-stage, static binary on
  Alpine, bundling `git`, `gitleaks`, and `tini` (PID 1, reaps the child git
  processes).
- **Release** ([`.goreleaser.yaml`](.goreleaser.yaml) + `release.yml`, X-1):
  multi-arch (linux/darwin × amd64/arm64) static binaries + checksums on tag.
- **CI** (`ci.yml`): `go build`, `go test -race`, `golangci-lint`, `govulncheck`,
  and dockertest integration. Dependencies are kept current by Dependabot
  (`.github/dependabot.yml`).

## Porting notes (deviations from Node / PR #1332)

- **Postgres only** — Mongo and NeDB are dropped (migrate-data imports legacy
  data once). The config `sink` field is a Node artifact.
- **Hybrid git engine** — go-git clone + git binary, vs Node's `isomorphic-git`
  / `simple-git` usage.
- **SSH on `x/crypto/ssh`** — replaces Node's `ssh2`. Agent forwarding uses the
  library's channel support directly; there is **no UNIX-socket agent bridge**
  (Node's `PullRemoteSSH` shelled out to system `git` with `GIT_SSH_COMMAND`
  and a socket — an `ssh2` artifact). The SSH clone uses go-git's SSH transport
  with the forwarded agent's `Signers`.
- **Same chain for SSH** — the SSH handler synthesises an `*http.Request`
  instead of refactoring the chain to be transport-agnostic.
- **Auth via session, not `passport`** — `scs` sessions + a small strategy
  registry rather than Passport middleware.
- **Deferred**: AD/LDAP (#33), upstream HTTP(S)-proxy egress, and the
  proxy-restart-on-origin-change behaviour (origins resolve at startup).

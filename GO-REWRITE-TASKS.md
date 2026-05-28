# `git-proxy` Go Rewrite — Task Breakdown

Companion to [`GO-REWRITE-PLAN.md`](./GO-REWRITE-PLAN.md). This decomposes the phased plan into discrete, checkable tasks suitable for GitHub issues.

**Conventions**

- Task IDs (`P0-1`, …) are stable — use them as issue titles / cross-references.
- `[ ]` = open, `[x]` = done. **(gate)** = hard phase exit gate (blocks the next phase).
- `→ Pn-m` = depends on that task. Size: **S** ≤1d · **M** 2–4d · **L** ≥1wk.
- Phase weeks and exit gates mirror §6 of the plan.

---

## Pre-flight (Week 1, §10)

- [ ] **PF-1** Create `git-proxy-go` repo, Apache-2.0 (match current). **S** — *see "Repo" note at bottom; decision required before this starts.*
- [ ] **PF-2** `go mod init github.com/<org>/git-proxy-go`; add `chi/v5`, `pgx/v5`, `koanf/v2`, `slog`. **S** → PF-1
- [ ] **PF-3** Stand up CI: `golangci-lint`, `go test -race`, `govulncheck`, `dockertest` integration job. **M** → PF-2
- [ ] **PF-4** Pin SSH source: latest commit SHA from finos PR #1332 → `docs/ssh-source-pin.md`. **S**
- [ ] **PF-5** Pin module versions per §5 "Versions" note — confirm `go-git/v5`, `koanf/v2`, `jsonschema/v6`, `golang-jwt/jwt/v5`; `go.mod` committed. **S** → PF-2

---

## P0 — Contract freeze + spikes (1 wk)

**(gate)** OpenAPI + ~200 golden fixtures committed · go-git spike passes · §3.9 git-engine decision locked.

- [ ] **P0-1** Extract OpenAPI v1 from the ~30 Express routes → `api/v1/openapi.yaml`. **M**
- [ ] **P0-2** Capture golden fixtures: run the Node build through the Cypress + integration suites with response capture on → `test/golden/` (~200). **M**
- [ ] **P0-3** **go-git spike** (§3.9): go-git clone → `git receive-pack`/`--unpack` → `git diff`, byte/semantic compare to Node path. **L** → **(gate)**
- [ ] **P0-4** Lock the git-engine decision (hybrid vs pure go-git) based on P0-3; record in `docs/decisions/0001-git-engine.md`. **S** → P0-3 **(gate)**
- [ ] **P0-5** Day-1 interop: bcrypt `$2a$` round-trip (Go `bcrypt` ↔ `bcryptjs`). **S**
- [ ] **P0-6** Day-1 interop: `csrf` cookie + `X-CSRF-TOKEN` double-submit prototype the existing UI accepts. **S**

---

## P1 — Skeleton + config (1 wk)

**(gate)** Binary boots, loads existing `proxy.config.json` unchanged, `/api/health` returns 200.

- [ ] **P1-1** `cmd/git-proxy` entrypoint + chi router skeleton + graceful shutdown. **S** → PF-2
- [ ] **P1-2** Config structs via `quicktype --lang go` from `config.schema.json`; commit generated `internal/config`. **S**
- [ ] **P1-3** Config loader (`koanf/v2`) + schema validation (`jsonschema/v6`); load `proxy.config.json` unchanged. **M** → P1-2
- [ ] **P1-4** `/api/health` (and other healthcheck routes) → 200. **S** → P1-1 **(gate)**
- [ ] **P1-5** Structured logging (`log/slog`). **S**
- [ ] **P1-6** CORS (`rs/cors`): allow `X-CSRF-TOKEN`, expose `Set-Cookie`, `credentials: true` — mirror `service/index.ts:126`. **S**
- [ ] **P1-7** Security headers (`unrolled/secure`): HSTS, nosniff, referrer-policy `same-origin`, `X-Frame-Options`; plus route-level `x-frame-options: DENY`. **S**
- [ ] **P1-8** Rate limiter (`golang.org/x/time/rate`) mirroring `express-rate-limit` config. **S**
- [ ] **P1-9** TLS: HTTP + HTTPS listeners, key/cert from config (match `getTLSEnabled`/key/cert paths). **M**

---

## P2 — Postgres store + migrate-data (2 wks)

**(gate)** Schema migrations defined · Mongo + neDB sample data import cleanly · round-trip tests pass.

- [ ] **P2-1** `internal/db` `Store` interface mirroring `src/db` (users, repos, pushes, sessions). **M**
- [ ] **P2-2** `goose` migrations: users, repos, pushes, sessions, audit. **M** → P2-1
- [ ] **P2-3** `sqlc` setup + generated queries; `pgx/v5` pool wiring. **M** → P2-2
- [ ] **P2-4** `cmd/migrate-data`: neDB JSON reader. **M**
- [ ] **P2-5** `cmd/migrate-data`: MongoDB reader (`ObjectID` → string, per §7 landmine). **M**
- [ ] **P2-6** `cmd/migrate-data`: ETL into Postgres — **dry-run by default**, typed audit log. **M** → P2-3, P2-4, P2-5 **(gate)**
- [ ] **P2-7** Round-trip tests with `dockertest` (write → read → diff). **M** → P2-3 **(gate)**

---

## P3 — Auth: local + OIDC + AD + JWT (2 wks)

**(gate)** UI logs in against Go backend · session persists across requests · `csrf` cookie / `X-CSRF-TOKEN` round-trips (functional equivalence — *not* byte-identical cookies).

- [ ] **P3-1** Session middleware (`scs/v2` + `postgresstore`); flags match behaviour (httpOnly, `secure: auto`/`trust proxy`, maxAge from config). **M** → P2-3
- [ ] **P3-2** CSRF: `csrf` cookie + `X-CSRF-TOKEN` double-submit, gated on `csrfProtection` flag (lusca-compatible). **M** → P0-6
- [ ] **P3-3** Strategy registry / multiplex (Passport-equivalent). **M**
- [ ] **P3-4** Local strategy — `bcrypt` verify against existing `$2a$` hashes. **S** → P0-5, P3-3
- [ ] **P3-5** OIDC strategy (`go-oidc/v3` + `oauth2`). **M** → P3-3
- [ ] **P3-6** AD/LDAP strategy (`go-ldap/v3`). **M** → P3-3 · *skip if §9 confirms no AD consumer (saves ~2–3d).*
- [ ] **P3-7** JWT strategy (`golang-jwt/jwt/v5`). **S** → P3-3
- [ ] **P3-8** UI login + mutation E2E against Go backend (proves CSRF + session). **M** → P3-1, P3-2 **(gate)**

---

## P4 — Git HTTP proxy + 15 chain processors (3 wks)

**(gate)** `git clone` + `git push` work end-to-end · chain blocks/approves identically to Node. *(Depends on the P0-3 spike outcome.)*

- [ ] **P4-1** Smart-HTTP reverse proxy (`httputil.ReverseProxy`) for git transport. **M** → P0-4
- [ ] **P4-2** Chain engine: pre-processor `parseAction`; sequential executor honouring `continue()`/`allowPush`/`error`; post-processors `clearBareClone` + `audit`; auto-approve/reject. **L** → P2-1
- [ ] **P4-3** Pull chain + default chain (`checkRepoInAuthorisedList`). **S** → P4-2
- [ ] **P4-4** Route groups mirroring `src/service/routes`: push, repo, users, config, home, auth, healthcheck. **M** → P1-1

**Processors** (one PR each — §7 chunking rule; order per `chain.ts:25-41`):

- [ ] **P4-5** `parsePush` — pkt-line + pack parse. **(fuzz target)** **L** → P4-2
- [ ] **P4-6** `checkEmptyBranch`. **S**
- [ ] **P4-7** `checkRepoInAuthorisedList`. **S**
- [ ] **P4-8** `checkCommitMessages`. **S**
- [ ] **P4-9** `checkAuthorEmails`. **S**
- [ ] **P4-10** `checkUserPushPermission`. **M**
- [ ] **P4-11** `pullRemote` — go-git network clone. **M** → P0-4
- [ ] **P4-12** `writePack` — `git receive-pack` / unpack (`receive.unpackLimit 0`), per §3.9. **(fuzz: pack)** **M** → P0-4
- [ ] **P4-13** `checkHiddenCommits`. **M**
- [ ] **P4-14** `checkIfWaitingAuth`. **S**
- [ ] **P4-15** `preReceive`. **M**
- [ ] **P4-16** `getDiff` — `git diff` via binary. **S** → P0-4
- [ ] **P4-17** `gitleaks` — spawn the gitleaks binary (as today). **M**
- [ ] **P4-18** `scanDiff`. **M**
- [ ] **P4-19** `blockForAuth`. **S** **(gate)**

---

## P5 — SSH + agent forwarding (3 wks)

**(gate)** SSH push routed through the same chain processors · agent-forwarding round-trip verified vs GitHub + GitLab. *(Highest-risk phase — porting an unmerged 10.8k-LOC PR; 3 wks is thin.)*

- [ ] **P5-1** SSH server (`golang.org/x/crypto/ssh`), behaviour ported from PR #1332. **L** → PF-4
- [ ] **P5-2** git-over-SSH: route `git-upload-pack` / `git-receive-pack` into the **same** chain (P4-2). **L** → P4-2
- [ ] **P5-3** Host-key management + known-hosts handling. **M**
- [ ] **P5-4** Agent forwarding (`.../ssh/agent`). **L**
- [ ] **P5-5** Fuzz SSH agent protocol round-trip. **(fuzz target)** **M** → P5-4
- [ ] **P5-6** Interop matrix: OpenSSH, gpg-agent, 1Password, Windows ssh-agent; against GitHub + GitLab. **M** → P5-4 **(gate)**

---

## P6 — Parity & soak (2 wks)

**(gate)** JSON API responses byte-diff clean · git protocol ops semantic-diff clean (identical resulting refs/object SHAs) · <0.1% request divergence over 1 wk mirrored staging traffic.

- [ ] **P6-1** Parity harness in `test/parity/` — traffic mirror Node ↔ Go. **L**
- [ ] **P6-2** JSON API byte-diff against golden fixtures (P0-2). **M** → P6-1
- [ ] **P6-3** Git ops semantic-diff (resulting refs/SHAs, **not** raw bytes). **M** → P6-1
- [ ] **P6-4** AD/OIDC tested against **real** corp AD + OIDC provider in staging. **M** → P3-5, P3-6
- [ ] **P6-5** 1-week soak on mirrored staging; divergence report < 0.1%. **L** → P6-2, P6-3 **(gate)**

---

## P7 — Cutover + data migration (1 wk)

**(gate)** DNS/LB flip · 14-day warm Node rollback window starts.

- [ ] **P7-1** Final `migrate-data` dry-run review → real run. **M** → P2-6
- [ ] **P7-2** DNS / load-balancer flip to Go backend. **S** → P6-5 **(gate)**
- [ ] **P7-3** Keep Node deployment warm; rollback runbook documented. **S**
- [ ] **P7-4** Decommission Node at day 14 (not before, even if clean). **S** → P7-2

---

## Cross-cutting (continuous)

- [ ] **X-1** `goreleaser` config — multi-arch static binary. → PF-3
- [ ] **X-2** Dockerfile — bundle `git` + `gitleaks` + `tini`; multi-stage. → P4-12, P4-17
- [ ] **X-3** `docs/ARCHITECTURE.md` (ported from #1332) + porting notes.
- [ ] **X-4** Weekly review of PR #1332 upstream deltas (§8 risk). → PF-4
- [ ] **X-5** `govulncheck` + dependency-update job in CI. → PF-3

---

## Deferred (tracked, not scheduled — §4)

Create as backlog issues labelled `deferred`; do not schedule for v1.

- [ ] **D-1** Plugin host — `go-plugin` gRPC sidecar (defer until first real consumer).
- [ ] **D-2** CLI port (`@finos/git-proxy-cli`).
- [ ] **D-3** UI modernization (React 16 → modern).
- [ ] **D-4** SQLite dev backend (`modernc.org/sqlite`).
- [ ] **D-5** Performance profiling / tuning.
- [ ] **D-6** Native gitleaks port.
- [ ] **D-7** HTTP/3 / advanced TLS.

---

## Repo

The plan (§3.2) calls for a **new repo `git-proxy-go`**. Decision needed before **PF-1**: owning org (`G-Research` vs personal), visibility (private vs public), and whether CI/registry reuse today's infra (§9.6). I can create it via `gh repo create` once you confirm those, or you can create it and I'll scaffold into it.

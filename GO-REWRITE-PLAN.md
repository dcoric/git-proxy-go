# `git-proxy` Go Rewrite — Proposal for Team Review

**Status:** Draft for discussion
**Author:** Denis Ćorić (with AI-assisted analysis)
**Target:** new repo `git-proxy-go`, big-bang cutover at parity

---

## 1. Goal

Rewrite `git-proxy` from TypeScript/Node into Go, delivering a single static binary that runs without a Node runtime, with lower memory footprint and a tighter operational story.

**Success criteria:**

- Single static binary, multi-arch, ~1 process to run in prod (today: Node + npm dep surface).
- HTTP contract preserved exactly — the existing React 16 UI works against the Go backend without changes.
- All current security/policy enforcement behavior preserved (the 15-step push-processing chain).
- SSH support included on day one (port of finos PR #1332, including SSH agent forwarding).
- Postgres-only data layer.
- Big-bang cutover after demonstrated parity in a traffic-mirrored staging environment.

**Non-goals:**

- Performance gains over the current Node implementation. The current bottleneck is I/O (git remote calls), not CPU. Memory and ops simplicity are the wins; throughput is not the headline.
- UI modernization (React 16 → modern stack is a separate project).
- Feature parity *plus* new features. This is a port, not a redesign.

---

## 2. What we're rewriting (scope baseline)

Current `git-proxy`, as it stands today:

| Aspect | Today |
|---|---|
| Language | TypeScript (~19k LOC src, ~18k LOC tests) |
| Runtime | Node.js 22+ |
| HTTP server | Express 5 |
| Git library | **Three mechanisms**: `isomorphic-git` (network clone in `pullRemote`) + the `git` binary `receive-pack`/`--unpack` (in `writePack`, `receive.unpackLimit 0`) + `simple-git`→`git` binary (diff in `getDiff`, branch checks in `checkEmptyBranch`). The container deliberately ships `git`; the binary is used where correctness is non-negotiable (unpack, diff). |
| SSH support | Open PR (#1332 in finos/git-proxy) — `ssh2` npm package, includes agent forwarding |
| Auth | Passport.js — local, OIDC, Active Directory, JWT |
| Database | MongoDB (prod) or neDB (file-based, dev/small) |
| Plugin system | `load-plugin` — dynamic JS injection into the processor chain |
| UI | React 16 + Material UI v4, 55 components |
| CLI | `@finos/git-proxy-cli` (yargs, HTTP client to the API) |
| Tests | 141 files (Vitest unit/integration + Cypress E2E) |
| Push processing chain | 15 sequential processors (actual order, `chain.ts:25-41`): `parsePush` → `checkEmptyBranch` → `checkRepoInAuthorisedList` → `checkCommitMessages` → `checkAuthorEmails` → `checkUserPushPermission` → `pullRemote` → `writePack` → `checkHiddenCommits` → `checkIfWaitingAuth` → `preReceive` → `getDiff` → `gitleaks` → `scanDiff` → `blockForAuth`. Note commit-message/email/permission checks run on the **parsed pack before** the remote clone, not on a cloned tree. Wrapped by a pre-processor (`parseAction`) and post-processors (`clearBareClone`, `audit`) + auto-approve/reject, which the port must also cover. |
| Deployment | Docker multi-stage, ports 8080/8000/8444 |

Substantial moving parts. The processor chain and the auth multiplex are the two most complex subsystems.

---

## 3. Decisions

### 3.1 Language: **Go**

**Considered:** Go vs Rust (ops team suggestion).

**Picked Go because:**

| Criterion | Go | Rust |
|---|---|---|
| Git library maturity for *server* use | `go-git` powers Gitea, Forgejo, Argo CD, Flux, Drone — battle-tested as a git server for ~8 years | `gitoxide` is fast but still maturing for server-side; `git2-rs` is libgit2 bindings |
| SSH server for git | `golang.org/x/crypto/ssh` — stdlib-grade, used by Gitea/Caddy/Drone as a git-over-SSH host | `russh` — works, smaller ecosystem footprint for git use cases |
| Pkt-line / upload-pack / receive-pack | Built into go-git, ready to embed | Primitives exist in gitoxide, more assembly required |
| Reference architecture | Gitea/Forgejo are essentially this app's bigger cousin | No close analog |
| Concurrency model fit | Goroutines + channels map cleanly onto per-connection proxy sessions | Tokio works but async Rust + lifetimes on streaming bodies is real overhead |
| Team ramp / hiring | Moderate | High |
| Perf for this workload (I/O-bound) | Captures ~95% of the wins over Node | Single-digit % advantage over Go |

The ops team's Rust preference is reasonable for CPU-bound or low-level systems work. For an I/O-bound HTTP/SSH proxy where the dominant cost is waiting on upstream git operations, Go gets us most of the perf and memory wins for substantially less team velocity cost.

### 3.2 Repository strategy: **new repo `git-proxy-go`**

Clean separation from the Node implementation. Allows parallel development without affecting current shipping. Old repo stays as-is until cutover.

### 3.3 Database: **Postgres only (drop MongoDB and neDB)**

**Considered:** preserve dual MongoDB + file-DB, or switch.

**Picked single-Postgres because:**

- Removes the storage abstraction layer entirely → simpler code, less to test.
- Single operational story across dev/staging/prod (Postgres in Docker for dev).
- Sessions, audit trails, and push records benefit from real transactions and SQL.
- Postgres is already common infra in most deployments.
- The cost — a one-shot ETL job to migrate existing data — is contained and isolated.

A standalone `cmd/migrate-data` binary will be built specifically for migrating existing neDB JSON files and MongoDB collections into Postgres.

### 3.4 UI: **keep current React 16, preserve backend HTTP contract**

UI rewrite is its own project, deferred. The new Go backend must serve the existing UI without UI changes.

### 3.5 SSH: **port finos PR #1332 at a pinned commit**

PR #1332 (+10,821 LOC, 73 files) is significantly more complete than the local branch — includes SSH **agent forwarding**, host-key management, known-hosts handling, and a dedicated test suite of ~3,700 LOC.

Pin to a specific commit SHA at project start; review upstream deltas weekly.

### 3.6 Migration approach: **big-bang cutover at parity**

**Considered:** strangler fig (gradual traffic shift), big-bang, or fork (Go for new repos, Node for old).

**Picked big-bang because:**

- Eliminates dual-stack glue code that would otherwise persist for months.
- Parity validation is done up front via traffic mirroring, so the cutover is informed by real-data evidence, not faith.
- Rollback path is straightforward: keep Node deployment warm for 14 days post-cutover.

### 3.7 Plugin system: **deferred**

The current `load-plugin` system supports dynamic JS extensions, but no consumers exist today.

**Picked deferral because:**

- Designing a plugin API speculatively (without real consumers) typically requires a v2 rework once consumers appear.
- The Go chain implementation will use an interface, so adding a gRPC sidecar plugin host later (HashiCorp `go-plugin`) is a contained ~1.5-week addition when needed.
- Saves ~1.5 weeks of upfront effort with zero immediate user impact.

If/when plugins are needed, the architecture is: HashiCorp `go-plugin` over gRPC, sidecar processes, language-agnostic plugins.

### 3.8 CLI: **deferred**

`@finos/git-proxy-cli` is an HTTP wrapper around the API. No urgent need to port; the HTTP API is the only required surface.

### 3.9 Git engine: **go-git for network ops, keep the `git` binary for receive-pack/diff**

**Considered:** pure go-git (no git binary) vs. mirror today's hybrid.

The current code is **not** "isomorphic-git does everything" — it shells out to the real `git` binary for `receive-pack`/unpack (`writePack`) and for `diff` (`getDiff` via `simple-git`), and only uses `isomorphic-git` for the network clone. go-git is pure-Go and reproduces neither `receive-pack --unpack` semantics nor canonical `git diff` output byte-for-byte; these are known go-git gaps and exactly the operations where a divergence corrupts pushes silently.

**Picked the hybrid because:**

- It mirrors the proven design and is the lowest-risk path to parity.
- It is consistent with §3.6 / §4 already keeping the **gitleaks binary** — keeping the `git` binary is the same call.
- go-git is used only where it is mature (smart-HTTP clone/fetch); the binary handles unpack and diff.
- The stable go-git line is **v5.x** (`v5.19.1` at planning time); **v6 is still alpha** (`v6.0.0-alpha.4`), so the network-clone dependency rides v5 — a further reason not to over-rely on go-git for v1.

**This is gated by a P0 spike** (see §6): prove go-git clone → `git receive-pack` unpack → `git diff` round-trips against real repos and matches the Node output before committing the chain port. If the spike shows go-git can safely own unpack/diff too, we revisit; the default assumption is "keep the binary."

---

## 4. What we're deferring (explicit list)

Things that *will* be needed eventually but are out of scope for the v1 cutover:

1. **Plugin host** — gRPC sidecar architecture. Defer until first concrete consumer.
2. **CLI** — port later when there's a clear ops/scripting need.
3. **UI modernization** — React 16 → modern React; separate project.
4. **File-DB / SQLite dev backend** — currently Postgres-only; can add a pure-Go SQLite (`modernc.org/sqlite`) backend later if the dev experience demands it.
5. **Performance optimization** — we target operational parity, not throughput gains. Profiling and tuning come post-cutover.
6. **Native gitleaks port** — keep spawning the gitleaks binary as today.
7. **HTTP/3, advanced TLS** — match current TLS support, no extensions.

---

## 5. Technology picks

### Final library list

| Concern | Library | Replaces |
|---|---|---|
| HTTP router | `github.com/go-chi/chi/v5` | Express 5 |
| Reverse proxy | `net/http/httputil.ReverseProxy` (stdlib) | `express-http-proxy` |
| Git network ops (clone/fetch) | `github.com/go-git/go-git/v5` | `isomorphic-git` |
| Receive-pack / unpack / diff | shell out to the `git` binary, as today (see §3.9) | `git` binary + `simple-git` |
| SSH server | `golang.org/x/crypto/ssh` + `.../ssh/agent` | `ssh2` npm |
| Postgres driver/pool | `github.com/jackc/pgx/v5` | (new — MongoDB driver removed) |
| Schema migrations | `github.com/pressly/goose/v3` | (new) |
| SQL → Go codegen (build-time) | `github.com/sqlc-dev/sqlc` | (new — no runtime ORM) |
| Sessions | `github.com/alexedwards/scs/v2` + `postgresstore` | `express-session` + `connect-mongo` |
| OIDC | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` | `openid-client` |
| LDAP/AD | `github.com/go-ldap/ldap/v3` | `passport-activedirectory` |
| JWT | `github.com/golang-jwt/jwt/v5` | `jsonwebtoken` |
| Passwords | `golang.org/x/crypto/bcrypt` | `bcryptjs` (hashes interop) |
| Config | `github.com/knadh/koanf/v2` + `github.com/santhosh-tekuri/jsonschema/v6` | custom config loader |
| Config struct generation | `quicktype --lang go` | already used for TS |
| Logging | `log/slog` (stdlib) | `bunyan`/`pino`-style |
| Rate limit | `golang.org/x/time/rate` | `express-rate-limit` |
| CORS | `github.com/rs/cors` (must allow `X-CSRF-TOKEN`, expose `Set-Cookie`, `credentials: true`) | `cors` npm |
| CSRF | hand-rolled double-submit matching the existing `csrf` cookie + `X-CSRF-TOKEN` header (or `gorilla/csrf` configured to those names) — **the React UI reads the `csrf` cookie and echoes it** (`ui/services/auth.ts:66`), so names must match | `lusca` (csrf) |
| Security headers | `github.com/unrolled/secure` (HSTS, nosniff, referrer-policy, X-Frame-Options) | `lusca` (hsts/nosniff/xframe/xss) |
| Testing | stdlib `testing` + `stretchr/testify` + `ory/dockertest/v3` | Vitest + Supertest |
| Property/fuzz | stdlib `testing.F` | `fast-check` |
| Build/release | `goreleaser` | npm publish |

Notable absences (intentional): no ORM, no DI framework, no large web framework (chi is a thin router).

**Versions** (verified against the Go module proxy, 2026-05-28): every import path above is on its current stable major — pin exact versions in `go.mod` at kickoff. Watch-items: `go-git` stable is the **v5.x** line (`v5.19.1`); **v6 is still alpha** (`v6.0.0-alpha.4`) — do not adopt for v1 (see §3.9). `koanf` is now **v2** (`v2.3.4`) and `santhosh-tekuri/jsonschema` is now **v6** (`v6.0.2`); the un-versioned import paths silently resolve to abandoned 2023/2018 releases, so the `/v2` and `/v6` suffixes are mandatory.

### Repo layout

```
git-proxy-go/
├── cmd/
│   ├── git-proxy/              main entrypoint
│   └── migrate-data/           neDB+Mongo → Postgres ETL (one-shot)
├── internal/
│   ├── proxy/http/             HTTP proxy + routes
│   ├── proxy/ssh/              SSH server (from PR #1332)
│   ├── proxy/ssh/agent/        SSH agent forwarding
│   ├── chain/                  push processor chain
│   ├── chain/processors/       15 processors
│   ├── git/                    go-git wrappers (clone/fetch) + `git` binary exec (receive-pack/diff), per §3.9
│   ├── git/pktline/            pkt-line parser (fuzz target)
│   ├── auth/{local,oidc,ad,jwt}/
│   ├── db/                     Store interface
│   ├── db/postgres/            sqlc-generated queries
│   ├── db/migrations/          goose migrations
│   ├── config/                 quicktype-generated structs + loader
│   └── chainext/               placeholder interface for future plugins
├── api/v1/                     OpenAPI spec (P0 deliverable)
├── docs/                       ARCHITECTURE.md (ported from #1332) + porting notes
└── test/
    ├── golden/                 captured request/response fixtures
    └── parity/                 traffic-mirror harness
```

---

## 6. Phased plan

Big-bang cutover, but internal phases give us checkpoints for parity validation.

| # | Phase | Weeks | Exit gate |
|---|---|---|---|
| **P0** | Contract freeze **+ go-git spike** | 1 | OpenAPI spec extracted from Express routes + ~200 golden request/response fixtures committed. **go-git spike passes**: go-git clone → `git receive-pack` unpack → `git diff` round-trips against real repos and matches Node output; the §3.9 git-engine decision (hybrid vs pure go-git) is locked. |
| **P1** | Skeleton + config | 1 | `git-proxy-go` binary boots, loads existing `proxy.config.json` unchanged, `/api/health` returns 200 |
| **P2** | Postgres store + migrate-data | 2 | Schema migrations defined; Mongo + neDB sample data imports cleanly; round-trip tests pass |
| **P3** | Auth (local + OIDC + AD + JWT) | 2 | UI logs in successfully against Go backend; session **persists across requests and the CSRF `csrf` cookie / `X-CSRF-TOKEN` flow round-trips** (functional equivalence — cookies are *not* byte-identical to express-session/lusca and need not be) |
| **P4** | Git HTTP proxy + 15 chain processors | 3 | `git clone` + `git push` work end-to-end; chain blocks/approves identically to Node. *(Depends on the P0 spike outcome.)* |
| **P5** | SSH + agent forwarding | 3 | SSH push routed through same chain processors; agent forwarding round-trip verified against GitHub + GitLab |
| **P6** | Parity & soak | 2 | **JSON API responses byte-diff clean**; **git protocol ops semantic-diff clean** (clone/push succeed with identical resulting refs/object SHAs — raw git bytes legitimately differ on compression/ref-ordering and are *not* byte-compared); <0.1% request divergence over 1 week of mirrored staging traffic |
| **P7** | Cutover + data migration | 1 | DNS/LB flip; 14-day warm Node rollback window starts |

**Sum of phases: ~15 person-weeks for one experienced Go engineer + LLM assistance.**

Add ~30% buffer if the primary engineer is learning Go on the job. Add ~1 week of slack for inevitable surprises during data migration. The two riskiest line items are **P5** (porting an unmerged 10.8k-LOC / 73-file SSH PR including agent forwarding — 3 weeks is thin) and **P4** (gated by the P0 spike); both skew the estimate up rather than down.

**Realistic range: 20–25 weeks**, with ~20 achievable only if the go-git spike passes cleanly and AD is dropped (see §9).

---

## 7. LLM-assisted rewrite approach

The intent is to use an LLM heavily during the port, but in a structured way that doesn't produce subtle bugs.

**The trap to avoid:** asking the LLM to "translate file X to Go." LLMs are bad at preserving subtle behavior across paradigms (error handling, async semantics, type coercion).

**The pattern that works:**

For each module:

1. **Human writes the contract** — Go test from a golden fixture, or a behavioral spec from the OpenAPI.
2. **LLM implements the module** — referenced against the TS source for behavior (not line-by-line translation), using the pinned libraries from §5.
3. **Human reviews** — focus on idiomaticity, error handling, resource cleanup (`defer`), context propagation, race conditions.
4. **CI gate** — `go test -race`, `golangci-lint`, `govulncheck`.
5. **Parity gate** — same input through Node and Go; byte-diff the response.

**Chunking rules:**

- One processor at a time (the 15 chain steps are ideal LLM-sized units).
- One auth strategy at a time.
- One route group at a time.
- Never delegate "the auth layer" or "the proxy" as a whole.

**Forced-fuzz targets:**

- `pktline` parsing — off-by-one bugs here break git silently.
- Pack file parsing — same.
- SSH agent protocol round-trip.
- JSON body parsing edge cases (Express was permissive; Go is strict).

**Landmines to brief the LLM about explicitly:**

- `bcryptjs` and Go `bcrypt` produce compatible hashes (`$2a$` format) — verify with round-trip on day 1.
- **CSRF must be replicated**, not invented. The UI reads a cookie named `csrf` and sends it back as the `X-CSRF-TOKEN` header (`ui/services/auth.ts:66`); the Go side must set that exact cookie name and validate that exact header (double-submit), gated on the `csrfProtection` config flag, or every UI mutation 403s.
- Session cookies do **not** need to be byte-identical — the browser re-sends whatever the server sets (`credentials: 'include'`), and the UI doesn't hardcode the session cookie name. Match *behaviour* (httpOnly, `secure: auto`/`trust proxy`, maxAge from config), not bytes. express-session and `scs` use different token formats; byte-identity is impossible and unnecessary.
- Replicate the other security headers lusca emits (HSTS, nosniff, referrer-policy `same-origin`, `X-Frame-Options`) and the route-level `x-frame-options: DENY` (`proxy/routes/index.ts:114`), or the UI's browser-side expectations shift.
- MongoDB ObjectIDs round-trip as strings in TS; Go uses `bson.ObjectID` — pin JSON marshaling. (Less relevant post-Postgres but the migrate-data binary still hits this.)
- Express's body parsing is loose; Go's `json.Decoder` is strict by default — `DisallowUnknownFields()` will surface real bugs but may break "permissive" clients.

---

## 8. Material risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **go-git cannot replicate a git-binary behavior the chain relies on** (`receive-pack --unpack`, `diff` output, pack negotiation) | Medium–High | High (blocks the chain port) | **P0 spike before committing the chain port** (§3.9). Default to keeping the `git` binary for unpack/diff; use go-git only for network clone/fetch. |
| SSH agent forwarding correctness — byte-perfect protocol round-trip | Medium | High | Test against multiple agents (OpenSSH, gpg-agent, 1Password, Windows ssh-agent). Fuzz the wire format. |
| Subtle git protocol divergence (pkt-line, ref advertisement) | Medium | High (silent data corruption) | Fuzz tests + traffic mirror in P6. **Semantic-diff git ops** (resulting refs/object SHAs), not raw bytes — raw git bytes legitimately differ on compression/ref-ordering. Byte-diff is reserved for JSON API responses. |
| CSRF/security-header contract drift breaks the existing React UI | Medium | High (UI mutations 403) | Replicate the `csrf` cookie + `X-CSRF-TOKEN` double-submit and lusca's security headers exactly (§5, §7); cover with a UI login + mutation test in P3. |
| PR #1332 changes upstream while porting | Medium | Medium | Pin to a commit SHA; review deltas weekly. |
| Postgres data migration surfaces dirty data | High | Medium | `cmd/migrate-data` is dry-run by default, emits typed audit log. Keep neDB/Mongo dumps for 90 days post-cutover. |
| AD / OIDC corp-specific edge cases (nested groups, claim mappings) | Medium | High (auth lockout) | Test against real corp AD + real OIDC provider in staging during P3. |
| Big-bang cutover finds an unforeseen parity gap | Medium | High | 14-day warm Node rollback. Don't decommission Node until day 14, even if cutover looks clean. |
| Hiring / Go ramp-up cost | Low–Medium | Medium | One Go-experienced owner drives; second engineer pair-codes with LLM. |
| bcrypt hash incompatibility | Low | High (password lockout) | Verified with round-trip test on day 1 of P3. |

---

## 9. Open items for team discussion

Things genuinely worth a debate before kickoff:

1. **Is the I/O-bound assumption true under our actual load?** If there's CPU-bound work I haven't accounted for (e.g., heavy pack inspection on large repos, gitleaks scan time dominating), the Go vs Rust calculus could shift slightly. Anyone have load profiles?
2. **Postgres version target.** `pgx` supports 12+, most orgs deploy 14+. What's our target floor?
3. **Active Directory usage.** If no consumer is actually using AD auth, dropping that strategy saves ~2–3 days. Is anyone on it?
4. **Big-bang appetite.** The plan assumes we're comfortable with a single cutover window with rollback safety net. If the appetite is lower, strangler fig is viable but adds 2–3 weeks of dual-stack glue.
5. **Who owns this.** This is ~15–20 person-weeks. Is it one person, two paired, or rotating? Estimate scales accordingly.
6. **CI / release infrastructure.** Goreleaser → where? Same Docker registry as today, or new?

---

## 10. Week-1 concrete actions (if approved)

1. Create `git-proxy-go` repo, Apache-2.0 license (match current).
2. `go mod init github.com/<org>/git-proxy-go`; add `chi`, `pgx`, `koanf`, `slog`.
3. Pin SSH source: grab latest commit SHA from finos PR #1332 → write to `docs/ssh-source-pin.md`.
4. Stand up CI: `golangci-lint`, `go test -race`, `govulncheck`, `dockertest` integration job.
5. **Run the go-git spike (§3.9 / P0 gate):** clone a real repo with go-git, hand the pack to `git receive-pack`/`--unpack`, run `git diff`, and compare results to the Node path. Lock the git-engine decision before anything else depends on it.
6. **Day-1 interop checks:** bcrypt `$2a$` round-trip (Go `bcrypt` ↔ `bcryptjs`), and a `csrf` cookie / `X-CSRF-TOKEN` double-submit prototype the existing UI accepts.
7. Begin P0: run current Node build against the full Cypress + integration suite with response capture turned on. Save responses → `test/golden/`.
8. Generate OpenAPI from the Express routes (~30 routes; can be done by inspection if no tooling fits).

---

## 11. What this plan does *not* commit to

- A specific calendar date for cutover.
- A specific engineer or team.
- A specific Postgres deployment target.
- Any change to the upstream finos/git-proxy repository or its roadmap.

This is a technical proposal. Resourcing, scheduling, and stakeholder communication are separate decisions.

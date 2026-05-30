# P3-8 — UI login + mutation E2E against the Go backend

- **Status:** Automated portion PASS · full browser run is a **manual gate** (deferred to staging, see below)
- **Date:** 2026-05-30
- **Tasks:** closes the P3-8 gate (#35); depends on P3-1 session (#28) and P3-2 CSRF (#30).
- **Plan ref:** §5 (auth), §6 (parity)

## What the gate proves

The React UI logs in against the Go backend and performs a state-mutating
action, exercising two backend contracts end-to-end:

1. **Session persistence** — a login establishes an scs session (Postgres
   `sessions` table) that subsequent requests reuse.
2. **CSRF round-trip** — the UI reads the `csrf` cookie and echoes it in the
   `X-CSRF-TOKEN` header on every mutating request (`src/ui/services/auth.ts`,
   `getAxiosConfig`); the backend rejects mutations whose header does not match.

## UI ↔ backend contract (parity reference)

The Go backend is byte-compatible with what the existing FINOS React UI already
speaks, so **no UI changes are required**:

| UI action | Request | Go handler |
|---|---|---|
| Load login page | `GET /api/auth/config` → `{usernamePasswordMethod, otherMethods}` | `authHandler.config` |
| Username/password login | `POST /api/auth/login` + `X-CSRF-TOKEN` | `authHandler.login` |
| OIDC login button (`otherMethods` entry) | `window.location = /api/auth/openidconnect` → 302 to IdP | `authHandler.oidcStart` |
| OIDC return | IdP → `GET /api/auth/openidconnect/callback?code&state` → 302 to `${GIT_PROXY_UI_HOST}:${GIT_PROXY_UI_PORT}/dashboard/profile` | `authHandler.oidcCallback` |
| Read profile | `GET /api/auth/profile` (session cookie) | `authHandler.profile` |
| Logout | `POST /api/auth/logout` + `X-CSRF-TOKEN` | `authHandler.logout` |

CSRF cookie/header names (`csrf` / `X-CSRF-TOKEN`) are fixed by the UI contract
— see `internal/proxy/http/csrf.go`.

## Automated coverage (the API-level stand-in)

The session-persistence + CSRF round-trip the gate cares about is verified
without a browser by the HTTP integration tests (real Postgres via dockertest,
`go test -tags=integration ./internal/proxy/http/`):

- **`auth_integration_test.go`** (`TestAuthLoginProfileLogoutFlow`): a cookie-jar
  client mints the `csrf` cookie on a safe GET, logs in with the token echoed in
  `X-CSRF-TOKEN`, reads `/api/auth/profile` from the resulting session, then
  logs out and confirms the session is gone. `TestAuthLoginRejectedWithoutCSRF`
  asserts a mutating POST without the header is **403**. `TestAuthConfig`
  asserts the `/api/auth/config` shape the UI consumes.
- **`oidc_routes_integration_test.go`** (`TestOIDCLoginFlow`): drives the OIDC UI
  login path against a mock IdP — `GET /openidconnect` 302s to the authorization
  endpoint and stashes a session `state`; the `callback` verifies that `state`,
  exchanges the code, provisions the user, establishes the session and 302s to
  the profile URL; `GET /profile` then returns the provisioned user.
  `TestOIDCCallbackRejectsBadState` asserts a mismatched/missing `state` is 400.

These constitute the automated gate: a login (local **and** OIDC) establishes a
persistent session, and the CSRF double-submit is enforced on mutations.

## Manual browser gate (deferred to staging)

A literal browser run requires (a) a built React UI and (b) the data-mutating
route groups (users / repo / push), which land in **P4** — there is no
non-auth mutation endpoint to exercise yet. The full browser walkthrough is
therefore a manual gate, executed in staging once P4 + a UI build exist, and is
tracked by **P6-4** (AD/OIDC vs real corp providers in staging) and the P6-1
parity harness.

### Procedure (run in staging)

1. Point the React UI's `baseUrl` at the Go backend (`GIT_PROXY_UI_HOST` /
   `GIT_PROXY_UI_PORT`); start the Go backend with `GIT_PROXY_DB_DSN` set and a
   `proxy.config.json` enabling `local` and/or `openidconnect`.
2. **Local login:** open the login page, sign in with username/password, and
   confirm you land authenticated (profile loads). In dev tools, confirm a
   session cookie and a readable `csrf` cookie were set.
3. **OIDC login:** click the OIDC method button; complete login at the IdP;
   confirm the browser returns to `…/dashboard/profile` authenticated and the
   user was provisioned (username = email local-part, `gitAccount` = "Edit me").
4. **Mutation + CSRF:** perform a UI action that mutates state (e.g. authorise /
   reject a push, or edit a user once P4 ships those routes). Confirm it
   succeeds with the `X-CSRF-TOKEN` header, and that tampering with / dropping
   the header yields **403**.
5. **Logout:** confirm the session is destroyed and protected views redirect to
   login.

### Pass criteria

- Local and OIDC logins both establish a session that survives navigation.
- Mutations carry and require a matching CSRF token (missing/mismatched → 403).
- Logout clears the session.

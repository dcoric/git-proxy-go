# Golden fixtures (task P0-2 / #7)

Captured request/response pairs from the **Node reference** implementation. The
Go backend is asserted byte-for-byte against these for JSON API responses
(see plan §6 / P6 — git protocol streams are compared *semantically*, not here).

## Format

One JSON file per fixture, loaded by `internal/testsupport/golden`:

```json
{
  "name": "healthcheck-get",
  "description": "GET /api/v1/healthcheck",
  "request":  { "method": "GET", "path": "/api/v1/healthcheck", "headers": {}, "body": "" },
  "response": { "status": 200, "headers": { "content-type": "application/json; charset=utf-8" }, "body": "{\"message\":\"ok\"}" }
}
```

Decoding is strict (`DisallowUnknownFields`), so a typo in a fixture fails fast.
A worked example lives in `internal/testsupport/golden/testdata/`.

## Capturing from the Node reference

The ~200 fixtures are recorded against a running Node `git-proxy`, not authored
by hand. Recommended approach:

1. Start the Node app (neDB backend needs no external DB):
   `cd <node-git-proxy> && npm run server`.
2. Drive it with the Cypress + integration suites (or a curated request list)
   through a recording reverse proxy, or add response-capture middleware that
   writes each request/response into the format above.
3. Commit the captured files under `test/golden/<group>/` (e.g. `auth/`, `repo/`,
   `push/`), one file per case, named `<route>-<method>[-<variant>].json`.
4. Redact secrets (session cookies, tokens, bcrypt hashes) before committing —
   public repo.

## Status

- [x] Format + loader (`internal/testsupport/golden`) + example.
- [ ] Capture the ~200 fixtures from the Node suite (remaining work on #7).

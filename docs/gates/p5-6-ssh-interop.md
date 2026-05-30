# P5-6 — git-over-SSH interop matrix

- **Status:** Automated portion PASS · full live matrix is a **manual gate** (staging, see below)
- **Date:** 2026-05-30
- **Tasks:** closes the P5-6 gate (#60); depends on the SSH server (#55), agent forwarding (#58), chain routing (#56), host-key verification (#57) and SSH clone (#105).
- **Plan ref:** §7 (SSH)

## What the gate proves

A real git client clones and pushes through the proxy over SSH, end-to-end,
against real upstream git hosts using real SSH agents — exercising every SSH
component together:

1. **Public-key auth** (#55) — the proxy maps the client's offered key to a user
   (`FindUserBySSHKey`); unknown keys are rejected.
2. **Agent forwarding** (#58) — the client forwards its agent; the proxy never
   sees the private key but can authenticate upstream as the user.
3. **Chain routing** (#56) — clone runs the pull chain; push relays the upstream
   ref advertisement, buffers the pack, runs the full push chain, and only
   forwards an approved pack.
4. **Host-key verification** (#57) — the proxy verifies the upstream host key
   against the known hosts before connecting.
5. **SSH clone** (#105) — the push chain's `pullRemote` clones the repo over SSH
   using the forwarded agent.

## Why this is a manual/staging gate

The behaviour above can only be exercised against a **real SSH git server**
(GitHub/GitLab) and a **real SSH agent** holding a key that is authorised on
that host. Neither is available in CI (no outbound SSH to git hosts, no agent
with a registered key), so the live matrix runs in staging. The automated tests
below cover the proxy's logic and parsing; this gate covers the live transport.

## The matrix

Run each cell: **clone** (read), then **push** (write → held for review →
authorise in the UI → push again → forwarded).

| SSH agent | GitHub | GitLab |
|---|---|---|
| OpenSSH `ssh-agent` (Linux/macOS) | ☐ | ☐ |
| `gpg-agent` (`enable-ssh-support`) | ☐ | ☐ |
| 1Password SSH agent | ☐ | ☐ |
| Windows OpenSSH agent | ☐ | ☐ |

## Setup

1. **Proxy.** Enable SSH and point at a DB:

   ```
   GIT_PROXY_SSH_ENABLED=true
   GIT_PROXY_SSH_PORT=2222
   GIT_PROXY_SSH_HOST_KEY_PATH=.ssh/proxy_host_key   # generated on first start
   GIT_PROXY_DB_DSN=postgres://…
   # Optional: extend the built-in github.com/gitlab.com host-key fingerprints
   # GIT_PROXY_SSH_KNOWN_HOSTS="git.internal=SHA256:…"
   ```

2. **User key.** Register the tester's SSH **public** key on their git-proxy
   user (the `public_keys` column, seeded via the migrate tool or the API) and
   ensure the same key is authorised on the upstream host (GitHub/GitLab).

3. **Agent.** Load the key into the agent under test and confirm `ssh-add -l`
   lists it.

4. **Client.** Point the repo's remote at the proxy and enable agent forwarding,
   e.g. in `~/.ssh/config`:

   ```
   Host git-proxy
     HostName <proxy-host>
     Port 2222
     ForwardAgent yes
   ```

   and set the remote to `git-proxy:<host>/<org>/<repo>.git`, e.g.
   `git-proxy:github.com/org/repo.git`.

## Procedure (per cell)

1. **Clone:** `git clone git-proxy:<host>/<org>/<repo>.git` → succeeds.
2. **Push (held):** commit, then `git push` → the proxy blocks it for review and
   prints the shareable approval URL on stderr.
3. **Approve:** authorise the push in the git-proxy UI.
4. **Push (forwarded):** `git push` again → the proxy forwards the pack upstream
   and relays the report-status; the ref updates on the host.

## Pass criteria

- Clone and the final push both succeed against the real host.
- An **unauthorised** repo or user is rejected with a clear message on the SSH
  client's stderr (no upstream connection attempted for a blocked pull).
- Connecting **without agent forwarding** is rejected with the
  "agent forwarding is required" guidance.
- A tampered/unknown **upstream host key** is rejected (host-key verification).
- The client's **private key never reaches the proxy** (only the agent socket is
  forwarded; verify the proxy logs/files contain no private key material).

## Things to confirm at the gate (open assumptions)

- **Pack-buffer termination.** `handlePush` reads the client's pack with
  `io.ReadAll` until the channel EOFs (mirrors PR #1332's `'end'` handling). If a
  real `git send-pack` does not half-close its write side before reading the
  report-status, this would stall — confirm against each client and add an
  explicit end-of-pack detection if needed.
- **Host-key fingerprints current.** The built-in github.com/gitlab.com SHA256
  fingerprints (`internal/proxy/ssh/knownhosts.go`) match the providers'
  published values at test time.

## Automated coverage (the stand-in)

What can be verified without the live transport is, in `go test [-race] ./...`:

- **Handler orchestration** (`internal/proxy/ssh/githandler_test.go`): the
  agent-forwarding gate, ref-advertisement relay, pack buffering, block-vs-
  forward decision, the SSH-clone credentials reaching the chain, and the
  upstream dial details — all with a fake upstream + fake engine.
- **Host-key verification** (`knownhosts_test.go`): fingerprint match/mismatch/
  unknown-host and the `HostKeyCallback`.
- **SSH-clone dispatch** (`internal/chain/pull_remote_test.go`): SSH vs HTTPS
  selection and `convertToSSHURL`.
- **Agent forwarding round-trip** (`agentforward_test.go`): a real
  `x/crypto/ssh` handshake forwarding an in-memory keyring.
- **Fuzzers** (`fuzz_test.go`, #59): `FuzzReadRefAdvertisement` (untrusted
  upstream pkt-lines), `FuzzParseGitCommand` (untrusted client command +
  path-traversal invariants), `FuzzAgentResponse` (hostile forwarded-agent
  replies). Run e.g. `go test -run='^$' -fuzz=FuzzReadRefAdvertisement ./internal/proxy/ssh/`.

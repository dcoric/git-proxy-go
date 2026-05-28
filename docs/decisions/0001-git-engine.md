# 0001 — Git engine: hybrid (go-git for clone, git binary for unpack/diff)

- **Status:** Accepted
- **Date:** 2026-05-28
- **Tasks:** closes the P0 spike (#8) and decision gate (#9); informs P4-11/P4-12/P4-16.
- **Plan ref:** §3.9

## Context

The Node implementation does **not** use one git mechanism — it uses three:

| Operation | Node mechanism |
|---|---|
| Network clone of the remote (`pullRemote`) | `isomorphic-git` |
| Receive-pack / unpack the pushed pack (`writePack`) | the `git` binary (`receive.unpackLimit 0`, `git receive-pack`) |
| Diff (`getDiff`), branch checks (`checkEmptyBranch`) | `simple-git` → the `git` binary |

The container deliberately ships `git`. The binary is used where byte-fidelity
to canonical git matters (unpack, diff). go-git is pure-Go and reproduces
neither `receive-pack --unpack` semantics nor canonical `git diff` output
byte-for-byte, so replacing all three with go-git was a material risk to parity.

## Spike

`internal/git/engine_spike_test.go` exercises the hybrid end-to-end on a
hermetic local repo (no network, skips if `git` is absent):

1. **go-git clone** of a 2-commit source repo — cloned HEAD matched the binary's
   `rev-parse HEAD` exactly.
2. **git binary unpack** — packed all objects (`git pack-objects --stdout`, a
   415-byte pack) and unpacked them into a fresh bare repo with
   `git unpack-objects` after setting `receive.unpackLimit 0`; the commit object
   was present and typed `commit` afterwards.
3. **git binary diff** — `git diff HEAD~1 HEAD` produced the expected change.

All three steps passed (`go test ./internal/git/ -run TestGitEngineSpike`,
~1.5s). go-git also resolved the same commit object from its own store,
confirming object-store fidelity between the two engines.

## Decision

Adopt the **hybrid** engine:

- **go-git** (`github.com/go-git/go-git/v5`, v5.x — v6 is still alpha) for
  network **clone/fetch** only.
- the **`git` binary** for **receive-pack/unpack** and **diff**.

This mirrors the proven Node design and is consistent with keeping the
**gitleaks** binary (§3.6/§4) rather than reimplementing it. The Dockerfile
must ship `git` (task X-2).

## Consequences

- `internal/git` wraps go-git for clone and shells out to `git` for unpack/diff;
  `pullRemote` (P4-11) uses go-git, `writePack` (P4-12) and `getDiff` (P4-16)
  use the binary.
- Parity for git protocol streams is validated **semantically** (resulting
  refs/object SHAs), not by raw-byte diff (P6, §6).
- Revisit only if a future go-git (≥ v6 stable) demonstrably matches the binary
  for unpack and diff; until then the binary stays.

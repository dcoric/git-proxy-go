# Upstream PR #1332 delta review (X-4)

The SSH support is ported from FINOS git-proxy **PR #1332**, which is not in the
local Node checkout and is still evolving. This is a **recurring weekly review**
to catch upstream changes (bug fixes, security fixes, protocol tweaks) that the
Go port should track. It mirrors the plan's §8 risk: porting against a moving
target.

## Source pin

The port was made against the commit recorded in
[`docs/ssh-source-pin.md`](../ssh-source-pin.md). Files fetched via:

```
gh api repos/finos/git-proxy/contents/<path>?ref=<sha> --jq '.content' | base64 -d
```

## Weekly procedure

1. List commits on the PR branch since the last reviewed SHA:

   ```
   gh api repos/finos/git-proxy/pulls/1332/commits --jq '.[].sha'
   # or compare: gh api repos/finos/git-proxy/compare/<last-sha>...<branch-head>
   ```

2. For each touched SSH file (`src/proxy/ssh/*`, `src/proxy/processors/push-action/PullRemote*`),
   diff against what we ported and assess impact on:
   - `internal/proxy/ssh/` (server, agent forwarding, host keys, chain routing)
   - `internal/chain/pull_remote.go` (SSH clone)
3. File an issue for any change that needs porting; note security fixes as
   priority. Record the review below and bump the pin in `docs/ssh-source-pin.md`
   if adopting a newer SHA.

## Review log

| Date | Reviewed up to (SHA) | Outcome |
|---|---|---|
| 2026-05-30 | `222994f` (initial pin) | Baseline — P5 ported from this commit. No deltas to assess yet. |

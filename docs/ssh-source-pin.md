# SSH source pin (task PF-4)

The SSH server, host-key/known-hosts handling, and agent forwarding (milestone
P5) are ported from finos/git-proxy **PR #1332**, not written from scratch.

To keep the port reproducible we pin to a specific upstream commit and review
deltas weekly (task X-4).

| Field | Value |
|---|---|
| Upstream PR | https://github.com/finos/git-proxy/pull/1332 |
| Pinned commit SHA | `TODO — fill in at PF-4` |
| Pinned at (date) | `TODO` |
| Last delta review | `TODO` |

## How to refresh

1. Fetch the PR head: `gh pr view 1332 --repo finos/git-proxy --json headRefOid`.
2. Compare against the pinned SHA above.
3. Note any behavioural changes that affect the Go port; open follow-up issues.
4. Update the table and the "Last delta review" date.

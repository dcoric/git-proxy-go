# git-proxy UI — redesign spec

This document specifies the **management web UI** for git-proxy-go so it can be
rebuilt from scratch in a clean stack (the legacy FINOS UI is a Material
Dashboard React 16 template entangled with the Node backend — see #77). It is
written to be handed to a UI generator: each page lists its route, purpose,
audience, content, data, actions, and states.

The UI is a **single-page app** that talks to the git-proxy management API. It is
**not** in the request path of git traffic — it is the console where reviewers
approve/reject held pushes and admins manage repos and users.

---

## 1. Product context

git-proxy sits in front of upstream git hosts and runs every **push** through a
policy chain. A normal push is **held** (blocked) pending human review; a
reviewer approves or rejects it in this UI. The UI's center of gravity is the
**push review queue** and the **push detail** view.

**Audiences**
- **Reviewer / contributor** (logged-in user): sees the push queue, opens a push,
  approves/rejects/cancels pushes they're allowed to act on, manages their own
  profile.
- **Admin**: everything above, plus the Users list and Settings.

**Tone**: a focused internal devtool/console — dense but legible tables, clear
status, fast review actions. Think "CI dashboard / code-review queue", not a
marketing site.

---

## 2. Recommended stack (suggestion, not a constraint)

- **React + TypeScript + Vite** (SPA).
- **Component system**: a modern, accessible kit — e.g. shadcn/ui + Tailwind, or
  MUI v6. Pick one and use it consistently.
- **Data fetching**: TanStack Query (server cache, loading/error states, refetch).
- **Routing**: React Router (or TanStack Router).
- **Diff rendering**: a unified-diff viewer (e.g. react-diff-view / diff2html).
- **Build output**: static assets served by the Go server at `/`, with API calls
  to `/api/*` on the same origin (no separate host). SPA fallback: unknown paths
  serve `index.html`.

Cross-cutting requirements for **every** page:
- **Auth gating**: all `/dashboard/*` pages require a session; unauthenticated →
  redirect to Login. Admin-only pages → show the 403 page for non-admins.
- **States**: every data view has explicit **loading**, **empty**, and **error**
  states (don't render a blank table).
- **Responsive** down to tablet; **keyboard accessible**; **dark + light** themes.
- **Optimistic-free actions**: mutations (approve/reject/etc.) show a pending
  state, then a success/error toast, then refetch.

---

## 3. Global shell

A persistent **left sidebar** + **top bar**, wrapping all `/dashboard/*` pages.

- **Brand**: "GitProxy" (logo + wordmark) at the top of the sidebar.
- **Sidebar nav** (icon + label), in order:
  | Label | Route | Icon idea | Visible to |
  |---|---|---|---|
  | Repositories | `/dashboard/repo` | repo | all |
  | Dashboard | `/dashboard/push` | grid/dashboard | all |
  | My Account | `/dashboard/profile` | account | all |
  | Users | `/dashboard/admin/user` | group | admin |
  | Settings | `/dashboard/admin/settings` | gear | admin |
  - "Dashboard" is the **push review queue** (the primary landing page after login).
- **Top bar**: page title, the signed-in user (name + avatar/initials), and a
  **Logout** action.
- **Auth context**: the shell loads the current user once (`GET /api/auth/profile`)
  and exposes `{ username, displayName, email, gitAccount, admin, title }`; it
  hides admin nav items when `admin` is false.

---

## 4. Pages

### 4.1 Login — `/login`
- **Purpose**: authenticate. **Access**: public (the only unauthenticated page).
- **Content**:
  - Brand/heading "GitProxy".
  - **Username** + **Password** fields, **Sign in** button (username/password is
    shown only when that method is enabled).
  - **Alternative methods**: for each method returned by the API (e.g. OIDC),
    a button that navigates the browser to `GET /api/auth/{method}` (full-page
    redirect, e.g. `/api/auth/openidconnect`). Do not fetch this — it's a redirect.
  - Inline error on bad credentials.
- **Data**: `GET /api/auth/config` → `{ usernamePasswordMethod: bool, otherMethods: [{ type, displayName, href }] }` decides which inputs/buttons to show.
- **Actions**: `POST /api/auth/login { username, password }` → on success, redirect
  to `/dashboard/push`.
- **States**: submitting (disabled button + spinner), error (message), method list
  empty (show only what's enabled).

### 4.2 Push review queue ("Dashboard") — `/dashboard/push`
- **Purpose**: the primary screen — the list of pushes to triage. **Access**: all.
- **Content**:
  - **Filter tabs / segmented control** over push state: **Pending review** (the
    default: blocked & not yet authorised), **Approved**, **Rejected**,
    **Canceled**, **Errored**. (Maps to the boolean filters below.)
  - A **table**, newest first, with columns:
    **Timestamp**, **Repository**, **Branch**, **Commit SHA** (short, monospace),
    **Commit Message** (truncated), **Committer**, **Author(s)**, and a **status**
    chip. Row → opens the push detail.
  - Optional **quick actions** per row for pending items (Approve / Reject), but
    the detail page is the main action surface.
- **Data**: `GET /api/v1/push?type=push&<flags>` returns an array of Push objects
  (see §5). Filter flags are booleans: `blocked`, `authorised`, `rejected`,
  `canceled`, `error`, `allowPush`. "Pending" = `blocked=true&authorised=false`.
- **States**: loading skeleton rows; empty ("No pushes to review"); error banner
  with retry.

### 4.3 Push detail — `/dashboard/push/:id`
- **Purpose**: review one push and decide. **Access**: all (acting on it is
  permission-checked server-side). **This is the most important page.**
- **Content** (top to bottom):
  1. **Header**: repository, branch, short commit range (`commitFrom..commitTo`),
     and a prominent **status** (Pending / Approved / Rejected / Canceled /
     Auto-approved by system / Auto-rejected by system / Error).
  2. **Summary fields**: Repository, Branch, Commit SHA, Committer, Author,
     Message, Timestamp, **Remote Head**.
  3. **Commits list**: each commit — SHA, message, author (name + email),
     committer, timestamp.
  4. **Push Validation Steps Summary**: the chain result as counts —
     **Total / Success / Blocked / Error** steps (chips) — then a per-step list:
     each `Step` shows its name, an ok/blocked/error state, and expandable
     **logs / content** (e.g. the secret-scan or diff-scan findings, the
     block reason). This is how the reviewer sees *why* a push was held.
  5. **Diff**: the push's changes rendered as a unified diff (collapsible per file).
  6. **Decision panel** (only when the push is pending and the viewer is allowed):
     - **Attestation form**: render the configured attestation questions
       (`GET /api/v1/config/attestation` → `{ questions: [{ label, tooltip }] }`)
       as checkboxes the reviewer must tick; each has a tooltip.
     - **Approve** (enabled only when all attestation boxes are checked),
       **Reject** (opens a reason field), **Cancel**.
- **Data**: `GET /api/v1/push/{id}` → Push (with `commitData`/commits, `steps`,
  `attestation` if already decided). Attestation questions from the config endpoint.
- **Actions**:
  - Approve → `POST /api/v1/push/{id}/authorise` with
    `{ params: { attestation: [{ label, checked: true }, …] } }`.
  - Reject → `POST /api/v1/push/{id}/reject` with `{ reason }`.
  - Cancel → `POST /api/v1/push/{id}/cancel`.
  - After any action: toast + refetch (status updates); a reviewer **cannot
    approve/reject their own push** (server returns 403 — surface it clearly).
- **States**: loading; "No push data found" (unknown id); already-decided
  (hide the decision panel, show who decided + the attestation answers/reason);
  action pending; action error (e.g. "Cannot approve your own changes",
  "not authorised to approve pushes on this project").

### 4.4 Repositories — `/dashboard/repo`
- **Purpose**: list the repos the proxy guards. **Access**: all.
- **Content**: a **table** with **Name**, **Organization** (project), **URL**
  (the upstream git URL). Row → repo detail. An admin **"Add repository"** action
  (form: organization/name/URL).
- **Data**: `GET /api/v1/repo` → array of Repo (see §5).
- **Actions** (admin): create → `POST /api/v1/repo { project, name, url }`
  (409 on duplicate URL).
- **States**: loading; empty ("No repositories"); error.

### 4.5 Repository detail — `/dashboard/repo/:id`
- **Purpose**: view a repo and manage who may push / approve. **Access**: all to
  view; mutations are admin/authorised.
- **Content**:
  - **Repo info**: Name, Organization, URL (and a copyable clone URL).
  - **Access lists**: two user tables (or one with role column) — **Can push**
    and **Can authorise** — each row a **Username** with a **remove** action; plus
    an **add user** input per list.
  - **Danger zone**: **Delete Repository** (confirm dialog).
- **Data**: `GET /api/v1/repo/{id}` → Repo with `users.canPush[]` and
  `users.canAuthorise[]`.
- **Actions** (permission-checked):
  - Add to authorise list → `PATCH /api/v1/repo/{id}/user/authorise { username }`.
  - Remove from authorise → `DELETE /api/v1/repo/{id}/user/authorise/{username}`.
  - Remove from push → `DELETE /api/v1/repo/{id}/user/push/{username}`.
  - Delete repo → `DELETE /api/v1/repo/{id}/delete`.
  - (Add-to-push uses the equivalent push add endpoint.)
- **States**: loading; "No repository data found"; per-action pending/error;
  confirm before delete.

### 4.6 Users — `/dashboard/admin/user`
- **Purpose**: directory of git-proxy users. **Access**: **admin only** (else 403).
- **Content**: a **table** with **Name** (display name), **E-mail**,
  **GitHub Username** (gitAccount), **Administrator** (yes/no), **Role**. Row →
  that user's profile.
- **Data**: `GET /api/v1/user` → array of public users (passwords never present).
- **States**: loading; empty; error.

### 4.7 My Account / User profile — `/dashboard/profile` and `/dashboard/user/:id`
- **Purpose**: view a user; on your own profile, edit your linked git account.
  **Access**: all (own profile); viewing others is fine, editing is self/admin.
- **Content**: **Name**, **E-mail**, **GitHub Username**, **Administrator**,
  **Role**. On the **own** profile, the GitHub Username is **editable** (save).
- **Data**: own → `GET /api/auth/profile`; by id → `GET /api/v1/user/{id}`.
- **Actions**: update linked git account (save) — *needs a backend endpoint;
  the Go API does not yet expose a profile-update route, flag for implementation.*
- **States**: loading; "No user data available"; save pending/success/error.

### 4.8 Settings — `/dashboard/admin/settings`
- **Purpose**: operator settings. Today this is **JWT token management**: when the
  API is JWT-guarded, paste/save a JWT used for API calls, with show/hide.
  **Access**: **admin only**.
- **Content**: an explanatory note ("shown only when JWT auth is enabled in the
  config"), a **JWT token** text field (masked, with a show/hide toggle), a
  **Save** button, and a confirmation snackbar.
- **Data/Actions**: client-side token persistence (used as a bearer for API
  calls). Surface read-only config values from `GET /api/v1/config/*`
  (attestation, urlShortener, contactEmail, uiRouteAuth) if useful.
- **States**: saved confirmation; empty token allowed.

### 4.9 Error pages
- **403 Not Authorized** (`NotAuthorized`): shown when a non-admin hits an
  admin-only route, or an action is forbidden. Message + link back to the queue.
- **404 Not Found** (`NotFound`): unknown route. Message + link back to the queue.

---

## 5. Data models (fields the UI renders)

These mirror the API responses (JSON). The UI is read-mostly; only the listed
actions mutate.

**Push** (`GET /api/v1/push`, `/push/{id}`)
```
id              string            // e.g. "<commitFrom>__<commitTo>"
type            "push" | "pull"
timestamp       number (epoch ms)
project         string            // organization
repoName        string
repo            string            // "<host>/<org>/<repo>.git"
url             string            // "https://<host>/<org>/<repo>.git"
branch          string            // e.g. "refs/heads/main"
commitFrom      string            // SHA
commitTo        string            // SHA
commitData      [ { hash/sha, message, author, authorEmail, committer,
                    committerEmail, commitTimestamp } ]
steps           [ Step ]          // chain result (see below)
attestation     { answers: [{label, checked}], reviewer, timestamp } | null
rejection       { reason, reviewer, timestamp } | null
error           bool
blocked         bool
allowPush       bool
authorised      bool
canceled        bool
rejected        bool
autoApproved    bool
autoRejected    bool
user            string
```

**Step** (one chain processor's result)
```
stepName        string            // e.g. "checkCommitMessages", "gitleaks", "scanDiff"
error           bool
blocked         bool
blockedMessage  string | null
errorMessage    string | null
logs            string[]          // human-readable log lines
content         any | null        // structured findings (e.g. scan results, diff)
```

**Repo** (`GET /api/v1/repo`, `/repo/{id}`)
```
id              string
project         string            // organization
name            string
url             string
users           { canPush: string[], canAuthorise: string[] }
```

**User / PublicUser** (`GET /api/v1/user`, `/api/auth/profile`)
```
username        string
displayName     string | null
email           string
gitAccount      string            // GitHub username
admin           bool
title           string | null
// passwords are never returned
```

---

## 6. API reference (the redesign targets the Go backend)

All same-origin under `/api`. Session cookie auth + CSRF double-submit: safe GETs
mint a readable `csrf` cookie; **unsafe requests must echo it in the
`X-CSRF-TOKEN` header** or get 403. JSON in/out. Mutations may also be JWT-guarded.

| Area | Method + path | Used by |
|---|---|---|
| Auth methods | `GET /api/auth/config` | Login |
| Login | `POST /api/auth/login` | Login |
| Logout | `POST /api/auth/logout` | Shell |
| Current user | `GET /api/auth/profile` | Shell, My Account |
| OIDC start | `GET /api/auth/openidconnect` (redirect) | Login |
| List pushes | `GET /api/v1/push?type=push&blocked=&authorised=&rejected=&canceled=&error=&allowPush=` | Queue |
| Get push | `GET /api/v1/push/{id}` | Push detail |
| Approve | `POST /api/v1/push/{id}/authorise` `{params:{attestation:[{label,checked}]}}` | Push detail |
| Reject | `POST /api/v1/push/{id}/reject` `{reason}` | Push detail |
| Cancel | `POST /api/v1/push/{id}/cancel` | Push detail |
| List repos | `GET /api/v1/repo` | Repositories |
| Get repo | `GET /api/v1/repo/{id}` | Repo detail |
| Create repo | `POST /api/v1/repo` `{project,name,url}` | Repositories (admin) |
| Add authorise | `PATCH /api/v1/repo/{id}/user/authorise` `{username}` | Repo detail |
| Remove authorise | `DELETE /api/v1/repo/{id}/user/authorise/{username}` | Repo detail |
| Remove push | `DELETE /api/v1/repo/{id}/user/push/{username}` | Repo detail |
| Delete repo | `DELETE /api/v1/repo/{id}/delete` | Repo detail |
| List users | `GET /api/v1/user` | Users (admin) |
| Get user | `GET /api/v1/user/{id}` | User profile |
| Config values | `GET /api/v1/config/{attestation,urlShortener,contactEmail,uiRouteAuth}` | Push detail, Settings |

**Backend gaps to flag for implementation** (the legacy UI used Node-only routes):
- **Profile update** (edit linked git account) — no Go endpoint yet.
- **Add-user-can-push** — confirm the Go route name/verb for adding to the push
  list (authorise add/remove + push remove exist; verify push add).

---

## 7. Notes for the UI generator

- **Keep**: the information architecture (queue → detail → decide; repos; users;
  settings), the field sets in §5, the API contract in §6, and the auth/CSRF
  behaviour.
- **Free to redesign**: visual language, component library, layout specifics,
  navigation styling, table vs card density, theming. The legacy Material
  Dashboard look is **not** a requirement.
- **Priorities**: make the **push review** flow excellent — fast scanning of the
  queue, a detail page where the validation-step findings and the diff are easy
  to read, and an unambiguous approve/reject with the attestation gate.
- **Don't** put secrets/tokens in logs or localStorage beyond what Settings
  explicitly manages; respect the CSRF + session model.

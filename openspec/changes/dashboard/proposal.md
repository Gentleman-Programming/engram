# Dashboard — Embedded Web UI for engram-cloud

**Change:** `dashboard`
**Status:** Proposed
**Created:** 2026-04-14

---

## 1. Intent

Teams using engram-cloud need a visual interface to manage their shared memory system. Today, interaction is limited to CLI commands, MCP tools (via agents), and the Bubbletea TUI (local only). None of these provide a team-wide view of memories, skills, members, roles, or audit history.

The dashboard is an **embedded web UI** served by the same `engram-cloud` binary. No separate frontend build, no additional deployment — one binary, one port, both API and dashboard.

### Why embedded?

- **Zero additional infrastructure**: `engram-cloud serve` starts the API and the dashboard on the same port
- **Single binary deployment**: HTML, CSS, and JS are embedded via `go:embed` — no static file server needed
- **Aligned with project philosophy**: no Node.js, no build pipeline, no npm

### Why htmx?

- **14kb single JS file** — embedded alongside templates, no bundler
- **Server-rendered HTML** — Go templates + htmx partial updates
- **No client-side state management** — the server IS the state
- **Progressive enhancement** — works without JS for basic navigation
- **Already planned** — `dashboard-htmx` skill exists in the project registry

---

## 2. Scope

### In Scope

1. **Memories browser** — search, filter by project/type/scope/date, view observation detail
2. **Skills manager** — list by category, view/edit with markdown preview, revision history, diff between versions, rollback
3. **Member management** — list users, assign roles per project, invite (generate API key)
4. **Skill policy configuration** — set min_edit_role and min_delete_role per project
5. **Audit log viewer** — timeline of skill changes with filters (user, project, action, date range)
6. **Project overview** — stats, enrolled projects, sync health, active members
7. **Authentication** — login with API key, session cookie for dashboard access
8. **Responsive layout** — usable on desktop and tablet

### Out of Scope

- **Real-time collaboration** — no simultaneous editing of the same skill (LWW handles conflicts)
- **Rich text editor** — markdown source editing only (with preview). No WYSIWYG.
- **Mobile-first design** — functional on mobile but optimized for desktop
- **Notifications** — no email/slack notifications for skill changes
- **Themes/customization** — single clean theme, no user preferences
- **Public/anonymous access** — all pages require authentication

---

## 3. Design

### 3.1 Architecture

```
engram-cloud serve
    │
    ├── /api/v1/...              ← JSON API (existing)
    │
    ├── /dashboard/              ← HTML pages (new)
    │   ├── /dashboard/login
    │   ├── /dashboard/memories
    │   ├── /dashboard/skills
    │   ├── /dashboard/skills/{topic_key}
    │   ├── /dashboard/skills/{topic_key}/revisions
    │   ├── /dashboard/members
    │   ├── /dashboard/audit
    │   └── /dashboard/projects
    │
    └── /static/                 ← Embedded assets (new)
        ├── htmx.min.js          (14kb)
        ├── style.css
        └── icons/
```

### 3.2 Technology Stack

| Component | Technology | Reason |
|-----------|-----------|--------|
| Router | chi (existing) | Already in use for API |
| Templates | `html/template` (Go stdlib) | Zero dependency, embedded in binary |
| Interactivity | htmx 2.x | Partial page updates without JS framework |
| Styling | Minimal CSS (custom) | No Tailwind build step, keep it simple |
| Static assets | `go:embed` | Compiled into binary |
| Markdown rendering | Server-side (goldmark or similar) | Render skill content as HTML for preview |
| Auth | Session cookie + API key validation | Reuse existing auth infrastructure |

### 3.3 Embedded Assets

```go
//go:embed dashboard/templates/*.html
var templateFS embed.FS

//go:embed dashboard/static/*
var staticFS embed.FS
```

All templates and static files compile into the `engram-cloud` binary. No external file dependencies at runtime.

### 3.4 Page Structure

#### Layout

```
┌─────────────────────────────────────────────────────┐
│  engram dashboard              [project ▼]  [logout]│
├──────────┬──────────────────────────────────────────┤
│          │                                          │
│ Memories │  Content area                            │
│ Skills   │  (htmx partial updates)                  │
│ Members  │                                          │
│ Policies │                                          │
│ Audit    │                                          │
│ Stats    │                                          │
│          │                                          │
└──────────┴──────────────────────────────────────────┘
```

#### Memories Page

- Search bar with full-text query
- Filter chips: project, type (decision/bugfix/pattern/skill/...), scope (project/personal), date range
- Results list with title, type badge, project, date, truncated content
- Click → detail view with full content, metadata, session info
- htmx: search/filter triggers `GET /dashboard/memories?q=...&type=...` → replaces results partial

#### Skills Page

- Tree view grouped by category prefix (architecture/, conventions/, patterns/, stack/)
- Each skill shows: title, last updated, updated_by, revision count
- Click → skill detail with rendered markdown content
- Edit button → textarea with raw markdown + live preview panel (htmx swap on keyup with debounce)
- Revision history → list of versions with author, date, diff summary
- Rollback button → confirmation dialog → POST rollback
- htmx: edit submits `PUT /dashboard/skills/{topic_key}` → replaces content partial

#### Members Page

- Table: name, email, role (dropdown per project), joined date
- Role dropdown: viewer/member/senior/lead/owner — change triggers `PUT /api/v1/projects/{name}/members/{id}`
- Invite button → generates API key, shows once
- Only visible to users with owner role

#### Audit Log Page

- Timeline view: chronological list of skill changes
- Each entry: timestamp, user, action (create/edit/delete/rollback), skill topic_key, revision numbers
- Filters: user, project, action type, date range
- htmx: filter changes trigger partial update of timeline

### 3.5 Authentication Flow

```
User visits /dashboard/
    │
    ├── No session cookie → redirect to /dashboard/login
    │   └── User enters API key → POST /dashboard/login
    │       └── Server validates key via store.ValidateAPIKey()
    │           ├── Valid → set session cookie (httpOnly, secure, 24h) → redirect to /dashboard/
    │           └── Invalid → show error on login page
    │
    └── Has valid session cookie → render dashboard
        └── Session expired → redirect to /dashboard/login
```

Sessions stored in-memory (map of session_id → user_id + expiry). No additional database table needed. Server restart clears sessions (acceptable — users re-enter API key).

### 3.6 htmx Patterns

**Search with debounce**:
```html
<input type="search" name="q"
    hx-get="/dashboard/memories"
    hx-trigger="keyup changed delay:300ms"
    hx-target="#results"
    hx-swap="innerHTML" />
```

**Skill edit with live preview**:
```html
<textarea name="content"
    hx-post="/dashboard/skills/preview"
    hx-trigger="keyup changed delay:500ms"
    hx-target="#preview-panel"
    hx-swap="innerHTML">
</textarea>
```

**Inline role change**:
```html
<select hx-put="/dashboard/members/{{.UserID}}/role"
    hx-vals='{"role": this.value}'
    hx-swap="none">
  <option value="member">member</option>
  <option value="senior">senior</option>
  ...
</select>
```

---

## 4. File Layout

```
internal/cloudserver/
├── server.go              ← existing API routes
├── middleware.go           ← existing auth middleware
├── dashboard.go           ← dashboard route registration + handlers (new)
├── dashboard_auth.go      ← session management for dashboard (new)
└── dashboard/
    ├── templates/
    │   ├── layout.html    ← base layout with nav
    │   ├── login.html
    │   ├── memories.html
    │   ├── memory_detail.html
    │   ├── skills.html
    │   ├── skill_detail.html
    │   ├── skill_edit.html
    │   ├── skill_revisions.html
    │   ├── members.html
    │   ├── audit.html
    │   └── stats.html
    └── static/
        ├── htmx.min.js
        └── style.css
```

---

## 5. Skill Ingestion from Filesystem

### CLI Command

```bash
engram cloud skills import <folder> [--project <name>] [--prefix <topic_prefix>]
```

### Behavior

1. Recursively scan `<folder>` for `*.md` files
2. For each file:
   - Derive `topic_key` from relative path: `conventions/testing.md` → `skill/conventions/testing`
   - Optional `--prefix` overrides the base: `--prefix stack` → `skill/stack/conventions/testing`
   - Read markdown content
   - POST to `/api/v1/sync/push` as a skill mutation (or direct `mem_save` if local)
3. Report summary: created N, updated N, skipped N (unchanged)

### Idempotency

- If a skill with the same `topic_key` already exists and content is identical → skip (no revision created)
- If content differs → update (creates revision, audit log entry)
- New files → create

### Folder Structure Convention

```
project-docs/
├── architecture/
│   ├── overview.md          → skill/architecture/overview
│   ├── modules.md           → skill/architecture/modules
│   └── decisions/
│       └── use-hexagonal.md → skill/architecture/decisions/use-hexagonal
├── conventions/
│   ├── naming.md            → skill/conventions/naming
│   ├── commits.md           → skill/conventions/commits
│   └── testing.md           → skill/conventions/testing
├── stack/
│   ├── go.md                → skill/stack/go
│   └── react.md             → skill/stack/react
└── onboarding.md            → skill/onboarding
```

### Watch Mode (future)

```bash
engram cloud skills import <folder> --watch
```

Monitors the folder for changes and auto-syncs on file save. Not in v1 — listed as future enhancement.

---

## 6. Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| htmx learning curve for contributors | Medium | Low | htmx is simple (HTML attributes). Include examples in contribution guide. |
| Template maintenance overhead | Medium | Medium | Keep templates small and composable. Use Go template partials. |
| Session management in-memory loses sessions on restart | Low | Low | Acceptable tradeoff — users re-enter API key. Add persistent sessions later if needed. |
| Markdown rendering adds dependency (goldmark) | Low | Low | goldmark is pure Go, well-maintained, small. Only alternative is unsafe regex-based rendering. |
| Skill import of large folder (1000+ files) | Low | Medium | Batch processing with progress output. Rate limit to avoid overwhelming the server. |
| Dashboard on same port as API — resource contention | Low | Low | Dashboard is lightweight HTML. API handles the real load. Separate ports as future option. |

---

## 7. Dependencies

### Hard Dependencies

- **Cloud server (Phase 2)** — complete, provides the base HTTP server and auth
- **Project skills feature** — needed for skills management pages, revisions, rollback
- **Role hierarchy** — needed for member management and policy pages

### New Go Dependencies

| Dependency | Purpose | Size |
|-----------|---------|------|
| `github.com/yuin/goldmark` | Markdown → HTML rendering for skill preview | Pure Go, ~2MB |

### Embedded Assets (no runtime deps)

| Asset | Size | Source |
|-------|------|--------|
| htmx.min.js | ~14kb | https://htmx.org |
| style.css | ~5kb | Custom |

---

## 8. Implementation Order

| Step | Description | Effort | Dependencies |
|------|-------------|--------|-------------|
| 1 | Scaffold: `dashboard.go`, embedded FS, layout template, static serving | S | cloudserver |
| 2 | Login page + session management (`dashboard_auth.go`) | M | auth middleware |
| 3 | Memories page: search + filter + detail view | M | existing API |
| 4 | Stats/project overview page | S | existing API |
| 5 | Skills list page (grouped by category) | M | project-skills feature |
| 6 | Skill detail + rendered markdown preview | M | goldmark |
| 7 | Skill edit page with live preview | M | htmx + goldmark |
| 8 | Skill revision history + diff view | M | project-skills revisions endpoint |
| 9 | Skill rollback UI | S | project-skills rollback endpoint |
| 10 | Members management page | M | role hierarchy |
| 11 | Skill policy configuration page | S | project_skill_policies |
| 12 | Audit log page with timeline view | M | skill_audit_log |
| 13 | `engram cloud skills import <folder>` CLI command | M | project-skills |
| 14 | CSS polish + responsive layout | S | — |
| 15 | Integration tests for dashboard routes | M | httptest |

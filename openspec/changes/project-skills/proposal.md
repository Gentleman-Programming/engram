# Project Skills — Role-Controlled Knowledge Base per Project

**Change:** `project-skills`
**Status:** Proposed
**Created:** 2026-04-14

---

## 1. Intent

Teams using engram-cloud need a way to store and share **project-level knowledge** — architecture decisions, coding conventions, technology choices, module documentation, testing patterns — in a structured, versioned, and access-controlled way.

Today, any team member can create and modify any observation. This is fine for ephemeral memories (bug fixes, session notes), but project-defining knowledge like "our architecture is hexagonal" or "we use table-driven tests" should be **governed**: only certain roles should be able to modify them, every change should be audited, and rollback should be possible.

### Why not just use files in the repo?

Files in a repo are static — agents can read them, but they can't update them collaboratively in real-time across sessions. Engram skills are **living documents** that sync across all team members via the cloud, are searchable via FTS5/tsvector, and are surfaced automatically when an agent calls `mem_search` or `mem_context`.

### Why not a separate system?

Skills are observations. They use the same storage, the same sync protocol, the same search. Adding a parallel system would duplicate infrastructure for no benefit. The difference is purely **policy**: skills have role-based write control, mandatory versioning, and audit logging.

---

## 2. Scope

### In Scope

1. **Skills as typed observations** — `type: "skill"` with `topic_key: "skill/..."` convention
2. **Role hierarchy** — `viewer < member < senior < lead < owner` on `project_members`
3. **Skill policy per project** — configurable minimum role for edit and delete operations
4. **Mandatory versioning** — every skill edit creates a revision (not just on LWW conflicts)
5. **Audit log** — append-only record of every skill operation (create, edit, delete, rollback)
6. **Rollback** — restore a skill to a specific previous revision (creates a new version, history preserved)
7. **Server-side enforcement** — permission checks happen in cloudserver, not in the client
8. **MCP tool** — `mem_list_skills` for agent discovery of project skills
9. **Cloud endpoints** — revisions listing, rollback, skill policy management
10. **CLI commands** — `engram cloud skills list/history/rollback`

### Out of Scope

- **Approval workflows** — no "pull request for skills" concept yet. A qualified role can edit directly.
- **Skill templates** — no pre-defined skill categories or schemas. Content is free-form markdown.
- **Cross-project skill inheritance** — each project has its own skills. No global skills that cascade.
- **Client-side enforcement** — the local SQLite store does not block edits. Enforcement is server-side on push. This is intentional: local-first means you can always work offline.
- **Skill activation/deactivation** — all skills are active. Soft-delete via observation `deleted_at` is sufficient.
- **Import from filesystem** — agents can read `.md` files and `mem_save` them as skills. No bulk import tool.

---

## 3. Design

### 3.1 Skills are Observations

No new tables for skill content. A skill is an observation with:

```
type:      "skill"
scope:     "project"            (always — skills are team-visible)
topic_key: "skill/<category>"   (hierarchical, upsert behavior)
project:   "<project-name>"
```

The `topic_key` prefix `skill/` is a convention enforced by the MCP tool and CLI, not a hard constraint in the database. Suggested taxonomy:

```
skill/architecture          → System structure, module boundaries
skill/architecture/modules  → Specific module documentation
skill/conventions/naming    → Naming conventions
skill/conventions/commits   → Commit message conventions
skill/patterns/repository   → Repository pattern usage
skill/patterns/cqrs         → CQRS pattern usage
skill/stack/go              → Go-specific conventions
skill/stack/react           → React-specific conventions
skill/testing               → Testing strategy and patterns
skill/deploy                → Deployment pipeline
skill/security              → Security guidelines
skill/onboarding            → New developer onboarding guide
```

### 3.2 Role Hierarchy

Extend `project_members.role` from the current `(owner, member)` to a 5-level hierarchy:

| Role | Level | Can read memories | Can write memories | Can edit skills | Can delete skills | Can manage members |
|------|-------|-------------------|--------------------|-----------------|-------------------|--------------------|
| `viewer` | 1 | Yes | No | No | No | No |
| `member` | 2 | Yes | Yes | No | No | No |
| `senior` | 3 | Yes | Yes | Yes (if policy allows) | No | No |
| `lead` | 4 | Yes | Yes | Yes | Yes (if policy allows) | No |
| `owner` | 5 | Yes | Yes | Yes | Yes | Yes |

The role hierarchy is ordinal: `owner > lead > senior > member > viewer`. A user with role level N can do anything that level N-1 can do.

**Migration**: Existing `member` rows stay as `member`. Existing `owner` rows stay as `owner`. No data migration needed — new roles are additive.

### 3.3 Skill Policy per Project

New PostgreSQL table:

```sql
CREATE TABLE project_skill_policies (
    project        TEXT PRIMARY KEY REFERENCES projects(name),
    min_edit_role  TEXT NOT NULL DEFAULT 'senior',
    min_delete_role TEXT NOT NULL DEFAULT 'lead',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Default policy: seniors can edit, leads can delete. Owners can always do everything (hardcoded, not configurable).

If no policy row exists for a project, use the defaults. This means skills work out of the box without explicit policy configuration.

### 3.4 Mandatory Versioning for Skills

Current behavior: `observation_revisions` is created ONLY on `topic_key` LWW conflicts (two different `sync_id`s with the same `topic_key`).

New behavior for `type = "skill"`: EVERY update creates a revision BEFORE overwriting, regardless of whether there's a `topic_key` conflict. This applies to both push protocol mutations and direct CRUD updates.

The `observation_revisions` table already has all needed columns:

```sql
-- Already exists
CREATE TABLE observation_revisions (
    id                SERIAL PRIMARY KEY,
    observation_id    TEXT NOT NULL,
    project           TEXT NOT NULL,
    title             TEXT,
    content           TEXT,
    type              TEXT,
    topic_key         TEXT,
    updated_by        TEXT,
    revision_number   INTEGER NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

No schema change needed — just change the trigger condition.

### 3.5 Audit Log

New PostgreSQL table (append-only):

```sql
CREATE TABLE skill_audit_log (
    id              BIGSERIAL PRIMARY KEY,
    project         TEXT NOT NULL,
    topic_key       TEXT NOT NULL,
    action          TEXT NOT NULL,  -- 'create', 'edit', 'delete', 'rollback'
    user_id         TEXT NOT NULL REFERENCES users(id),
    revision_from   INTEGER,       -- NULL on create
    revision_to     INTEGER NOT NULL,
    summary         TEXT,          -- optional: what changed
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_skill_audit_project ON skill_audit_log(project, topic_key);
CREATE INDEX idx_skill_audit_user ON skill_audit_log(user_id);
```

Every skill mutation writes an audit record. This table is never updated or deleted — append-only. It can be queried for compliance, blame, or debugging.

### 3.6 Rollback Mechanism

Rollback creates a NEW version with the content from a previous revision. It does NOT rewrite history.

```
v1 (Juan creates "hexagonal architecture")
v2 (María edits, adds "port naming conventions")
v3 (Pedro edits, accidentally deletes half the content)
v4 (Juan rolls back to v2 → new version with María's content)
```

Revision chain after rollback: `[v1, v2, v3, v4]`. v3 is preserved in revisions. v4 has the same content as v2 but a new `updated_by`, `updated_at`, and audit log entry.

API:

```
POST /api/v1/skills/{topic_key}/rollback
{
    "project": "my-api",
    "to_revision": 2
}
→ 200 { "revision": 4, "rolled_back_from": 3, "rolled_back_to": 2 }
```

### 3.7 Server-Side Enforcement Flow

```
Client pushes mutation (type: "skill")
    │
    ▼
cloudserver receives push
    │
    ▼
┌── Is type == "skill"? ──────────────────────────┐
│ NO → normal push flow (no role check)           │
│ YES ↓                                            │
│                                                  │
│ ┌── Load project_skill_policies ──┐             │
│ │ Get min_edit_role for project   │             │
│ └──────────────┬──────────────────┘             │
│                │                                 │
│ ┌── Check user role ──────────────┐             │
│ │ user.role >= min_edit_role?     │             │
│ │ NO → 403 "insufficient role"   │             │
│ │ YES ↓                          │             │
│ └──────────────┬──────────────────┘             │
│                │                                 │
│ ┌── Save revision (mandatory) ────┐             │
│ │ INSERT INTO observation_revisions│             │
│ └──────────────┬──────────────────┘             │
│                │                                 │
│ ┌── Apply change ─────────────────┐             │
│ │ UPDATE observation              │             │
│ │ revision_count++                │             │
│ └──────────────┬──────────────────┘             │
│                │                                 │
│ ┌── Audit log ────────────────────┐             │
│ │ INSERT INTO skill_audit_log     │             │
│ └─────────────────────────────────┘             │
└──────────────────────────────────────────────────┘
    │
    ▼
Response to client (200 or 403)
    │
    ▼
Change syncs to all team members via pull
```

### 3.8 Local-First Implications

The local SQLite store does NOT enforce skill permissions. A developer working offline can edit any skill locally. When they reconnect and push:

- If they have sufficient role → push succeeds, skill updated on server
- If they don't have sufficient role → push returns 403, mutation stays pending

This is the correct local-first behavior: never block offline work. The server is the authority for access control.

The SyncClient should handle 403 on skill pushes gracefully: log a warning, skip that mutation (don't retry), and continue pushing other mutations.

---

## 4. API Additions

### New Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/skills` | Yes | List all skills for a project (filterable by category prefix) |
| GET | `/api/v1/skills/{topic_key}/revisions` | Yes | List all revisions of a skill |
| POST | `/api/v1/skills/{topic_key}/rollback` | Yes | Rollback to a specific revision |
| GET | `/api/v1/skills/audit` | Yes | Query audit log (by project, user, date range) |
| GET | `/api/v1/projects/{name}/skill-policy` | Yes | Get skill policy for a project |
| PUT | `/api/v1/projects/{name}/skill-policy` | Yes (owner) | Update skill policy |

### New MCP Tool

```
mem_list_skills(project?: string, category?: string)
→ Returns all observations where type = "skill", optionally filtered by topic_key prefix
→ Grouped by category (first segment after "skill/")
```

### CLI Commands

```bash
engram cloud skills list [--project <name>]
engram cloud skills history <topic_key> [--project <name>]
engram cloud skills rollback <topic_key> --to <revision> [--project <name>]
```

---

## 5. Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Role hierarchy breaks existing workflows (current members lose skill edit access) | Medium | Medium | Default policy is `min_edit_role: "senior"`. Existing owners keep full access. Teams upgrade members to senior explicitly. |
| 403 on skill push confuses offline developers | Medium | Low | Clear error message: "Your role (member) cannot edit skills. Minimum: senior. Contact project owner." SyncClient logs warning and skips. |
| Audit log grows large on frequently-edited skills | Low | Low | Append-only is intentional. Add retention policy later if needed. Index on (project, topic_key) keeps queries fast. |
| topic_key convention not enforced — users create skills with arbitrary keys | Medium | Low | MCP tool and CLI enforce `skill/` prefix. Direct API allows any key (flexibility > strictness). |
| Revision table grows with mandatory versioning | Low | Low | Skills change infrequently (that's their nature). Even 100 revisions per skill is tiny. |

---

## 6. Dependencies

### Hard Dependencies

- **Cloud server (Phase 2)** — complete, provides the enforcement layer
- **Cloud sync client (Phase 3)** — in progress, needed for 403 handling on rejected skill pushes
- **`project_members` table** — exists, needs role column expansion
- **`observation_revisions` table** — exists, needs trigger change for skills

### No New External Dependencies

All implementation uses existing Go stdlib + pgx. No new libraries needed.

---

## 7. Implementation Order

| Step | Description | Effort |
|------|-------------|--------|
| 1 | Extend `project_members.role` with 5-level hierarchy + migration | S |
| 2 | Create `project_skill_policies` table + defaults | S |
| 3 | Add role-check middleware in cloudserver for `type: "skill"` mutations | M |
| 4 | Change revision trigger: always create revision for skill edits | S |
| 5 | Create `skill_audit_log` table + writes on every skill operation | S |
| 6 | Add `/api/v1/skills` list endpoint | S |
| 7 | Add `/api/v1/skills/{topic_key}/revisions` endpoint | S |
| 8 | Add `/api/v1/skills/{topic_key}/rollback` endpoint | M |
| 9 | Add `/api/v1/skills/audit` endpoint | S |
| 10 | Add `/api/v1/projects/{name}/skill-policy` GET/PUT endpoints | S |
| 11 | Add `mem_list_skills` MCP tool | S |
| 12 | Handle 403 on skill pushes in SyncClient (skip + warn) | S |
| 13 | CLI: `engram cloud skills list/history/rollback` | M |
| 14 | Integration tests | M |

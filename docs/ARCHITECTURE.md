[← Back to README](../README.md)

# Architecture

Local `POST /prompts` accepts optional `source_inbox_id` alongside `session_id`, `content`, and `project`. A nonempty ID identifies one prompt within its session: replay returns the existing prompt ID with the same `201` and `{"id":…, "status":"saved"}` response, without another sync mutation or write notification. Distinct IDs may contain identical text. Omitting the ID continues to append a new prompt on every call. Project ownership checks still apply before replay. Prompt sync upserts and exports preserve the optional identity, so replay after sync or import returns the existing prompt without a new mutation. Older payloads without the field remain valid. A pulled prompt upsert cannot move an established nonempty inbox identity to a different session or replace it with another nonempty ID for the same sync ID; it fails with an identity conflict. A missing payload ID retains the existing identity in the same session; a legacy row with no established identity may move sessions or acquire an ID. Import adoption of an inbox identity for the same sync ID refuses cross-project reassignment, comparing canonical effective projects (including session inheritance for blank prompt projects), and appends a canonical local upsert for enrolled projects, leaving earlier journal entries unchanged so acknowledgement of an in-flight original cannot consume the adopted identity. Deletion tombstones retain optional inbox identity through sync and direct backup. A pulled delete for a live prompt records the live row's session-and-inbox identity, not conflicting identity fields from its payload. Any pulled delete claiming a session-and-inbox pair held by another live prompt is quarantined before a tombstone is recorded, even when its sync ID resolves to a live prompt. Backup import rejects a tombstone that would delete a matched live prompt belonging to a different effective project; the import rolls back atomically. Reusing a deleted session-and-inbox ID fails with HTTP `409 Conflict` (no ID, mutation, or write notification), including after restore; a different ID remains a new prompt. Pulled deletes without a live prompt quarantine an inbox ID without a nonblank session ID as dead-letter evidence and advance the pull cursor without writing a tombstone; legacy deletes without an inbox ID remain valid. Sparse pulled deletes inherit project ownership from the live prompt first (which can differ from its session), then their own established prompt tombstone, then the live session or active deleted-session tombstone; project exports recover ownership from an active deleted-session tombstone after that session is removed and emit that resolved project for legacy blank-project tombstones, preserving ownership through backup import. Unscoped exports also emit the resolved project so full backup restores retain that ownership. Backup import rejects tombstones carrying an inbox ID without a session ID; legacy tombstones without an inbox ID remain valid. Once a tombstone establishes a session ID, inbox ID, or nonempty project for a sync ID, later deletes and backup imports cannot change that component: conflicting pulled deletes are quarantined with their raw evidence and advance the cursor, while conflicting imports fail atomically. Projects are compared after normalization; sparse legacy repeats retain established components, and missing components may be filled consistently.

- [How It Works](#how-it-works)
- [Session Lifecycle](#session-lifecycle)
- [MCP Tools](#mcp-tools)
- [Progressive Disclosure](#progressive-disclosure-3-layer-pattern)
- [Memory Hygiene](#memory-hygiene)
- [Topic Key Workflow](#topic-key-workflow-recommended)
- [Project Structure](#project-structure)
- [CLI Reference](#cli-reference)

---

## How It Works

<p align="center">
  <img src="../assets/agent-save.png" alt="Agent saving a memory via mem_save" width="800" />
  <br />
  <em>The agent proactively calls <code>mem_save</code> after significant work — structured, searchable, no noise.</em>
</p>

Engram trusts the **agent** to decide what's worth remembering — not a firehose of raw tool calls.

### The Agent Saves, Engram Stores

```
1. Agent completes significant work (bugfix, architecture decision, etc.)
2. Agent calls mem_save with a structured summary:
   - title: "Fixed N+1 query in user list"
   - type: "bugfix"
   - content: What/Why/Where/Learned format
3. Engram persists to SQLite with FTS5 indexing
4. Next session: agent searches memory, gets relevant context
```

---

## Session Lifecycle

Codex runtime identity is core-owned: startup registers a resumable root, while
writes, prompt capture and compaction resolve an existing active identity.
Runtime closing resolves and ends atomically without creating a continuation.
Both root and continuation must satisfy existing project-sharing policy, exact
ownership and canonical worktree; equivalent directory spelling is accepted without rewriting
stored rows. Ended roots remain terminal. These operations are separate from
ordinary exact-ID session ending; adapters maintain no identity cache.

```
Session starts → Agent works → Agent saves memories proactively
                                    ↓
Session ends → Agent writes session summary (Goal/Discoveries/Accomplished/Next Steps/Files)
                                    ↓
Next session starts → Agent may retrieve prior context; a host plugin may inject it
```

Runtime registration through `POST /sessions` supports opt-in `resume: true`. Go selects and registers an effective continuation for an ended root inside one SQLite write transaction, preserving terminal rows, ownership checks, and runtime leases. Adapters forward the acknowledged ID rather than choosing suffixes. Ordinary registration and MCP callers retain the ended-session refusal; see the [session API contract](../DOCS.md#sessions).

Context injection depends on the integration and available context: Claude Code with plugin setup can inject prior context at startup when available, while bare MCP does not inject it. Agents can request it with `mem_context`; see [plugin behavior](PLUGINS.md#what-the-plugin-provides-with-setup-vs-bare-mcp) and [adapter boundaries](codebase/integrations.md#runtime-session-identity).

---

## MCP Tools

The stdio MCP server exposes memory capture, retrieval, session context, and administration tools to agents; project resolution occurs at tool call time. The canonical inventory and tool contracts are in [DOCS.md MCP Tools](../DOCS.md#mcp-tools-23-tools).

---

## Progressive Disclosure (3-Layer Pattern)

Token-efficient memory retrieval — don't dump everything, drill in:

```
1. mem_search "auth middleware"     → compact results with IDs (~100 tokens each)
2. mem_timeline observation_id=42  → what happened before/after in that session
3. mem_get_observation id=42       → full untruncated content
```

---

## Memory Hygiene

- `mem_save` now supports `scope` (`project` default, `personal` and `global` also accepted)
- `mem_save` also supports `topic_key`; with a topic key, saves become upserts (same project+scope+topic updates the existing memory)
- `mem_save` supports `capture_prompt` (`true` by default) in MCP and native Pi. Runtime adapters supply current prompt context for the observation's session/project; `Store.CapturePromptForSave` owns opt-out and exact prompt dedupe. MCP supplies process-local context (typically fed by `mem_save_prompt`), while Pi supplies the eligible current turn's redacted/truncated payload. Pi preserves that payload when steer/follow-up input is enqueued and when the initial user message starts after `before_agent_start`; subsequent user `message_start` events mark queued-input consumption and invalidate the preceding prompt context and ownership proof. Queued turns neither capture nor inherit context, including repeated identical input; a normal subsequent run establishes fresh context. Capture stores an independent prompt row, not an observation-ID link, and errors are nonfatal after a successful observation save. `capture_prompt=false` skips only this per-save attempt, not Pi's independent lifecycle capture.
- `mem_save` and `mem_search` expose lifecycle metadata: computed `state` (`active` or `needs_review`) and `review_after` when a review cycle applies.
- `mem_review` supports `action="list"` (`project`, `limit`) and `action="mark_reviewed"` (`observation_id`). Marking reviewed atomically enqueues an observation sync mutation. `review_after` is replicated, including explicit null resets; legacy missing fields preserve existing dates or derive new dates from the observation's creation time.
- Exact dedupe prevents repeated inserts in a rolling window (hash + project + scope + type + title)
- Duplicates update metadata (`duplicate_count`, `last_seen_at`, `updated_at`) instead of creating new rows
- Topic upserts increment `revision_count` so evolving decisions stay in one memory
- `mem_delete` uses soft-delete by default (`deleted_at`), with optional hard delete
- `mem_search`, `mem_context`, recent lists, and timeline ignore soft-deleted observations

---

## Topic Key Workflow (Recommended)

### What topic_key is

`topic_key` turns `mem_save` into an **upsert**: if a memory with the same `project + scope + topic_key` already exists, the existing observation is updated in place (`revision_count++`) instead of creating a new row. Without a `topic_key`, every `mem_save` creates a new observation even when the content describes the same evolving topic.

Use topic keys for knowledge that changes over time: architecture decisions, long-running feature notes, recurring patterns, configuration choices. Skip them for one-off bugs, single facts, or anything that does not evolve.

### Format convention

Topic keys follow **slash-separated lowercase kebab-case**:

```
family/specific-description
```

Examples:
- `architecture/auth-model`
- `bug/nil-panic-in-user-list`
- `decision/database-choice`
- `pattern/error-handling-convention`
- `config/ci-environment`

**Why this format?** Topic keys support exact key lookups and trigram FTS substring search. Lowercase kebab-case keeps identifiers stable, readable, and consistent across callers.

**Anti-patterns to avoid:**

| Anti-pattern | Problem | Correct form |
|---|---|---|
| `authModel` | camelCase is inconsistent with canonical topic keys | `architecture/auth-model` |
| `auth model` | spaces make exact topic identifiers harder to use | `architecture/auth-model` |
| `ARCHITECTURE/AUTH` | uppercase is inconsistent with FTS5 normalisation | `architecture/auth-model` |
| `auth/model/v2/final` | more than 2 levels — use `v2` in the description | `architecture/auth-model-v2` |
| `bugfix` | no slash — looks like a family with no description | `bug/auth-nil-panic` |

### Decision table — when to use topic_key

| Situation | Use topic_key? | Reasoning |
|---|---|---|
| Architecture or design decision that may evolve | Yes | Keeps history in one observation, incrementing `revision_count` |
| Long-running feature work (spans multiple sessions) | Yes | Single source of truth across sessions |
| A pattern or convention established for the project | Yes | One canonical entry, updated as the pattern matures |
| Bug fix that was self-contained and is now closed | No | A single observation is fine; no future updates expected |
| One-off discovery or fact | No | Creating a key you will never reuse adds noise |
| Multiple independent decisions on the same broad topic | No — use distinct keys | Different decisions must have different keys or they will overwrite each other |

### The mem_suggest_topic_key-first workflow

When you are not sure which key to use, call `mem_suggest_topic_key` before `mem_save`. It applies a family heuristic based on the observation type and title, returning a suggested key you can use directly or adjust:

```text
1. mem_suggest_topic_key(type="architecture", title="Auth model")
   → returns: "architecture/auth-model"

2. mem_save(..., topic_key="architecture/auth-model")
   → creates new observation (revision_count=1)

3. (later session) mem_save(..., topic_key="architecture/auth-model")
   → updates the existing observation (revision_count=2) and attributes it to the latest writer session
```

`mem_suggest_topic_key` families:

- `architecture/*` — architecture, design, ADR-like observations
- `bug/*` — bug fixes, regressions, panics, error root causes
- `decision/*` — explicit decisions with tradeoffs
- `pattern/*` — naming conventions, structural patterns, coding standards
- `config/*` — configuration and environment setup
- `discovery/*` — non-obvious findings about the codebase
- `learning/*` — team knowledge and onboarding notes

If none of these families fit, it is usually fine to skip the key and let `mem_save` create a plain observation.

### Hierarchical keys — max 2 levels

Keys are organisational only; there is no parent–child relationship in the store. Two levels (`family/description`) cover almost every case. Use the description segment to add specificity rather than adding more slashes:

```
architecture/auth-model          ✓ two levels, specific
architecture/auth-model-v2       ✓ version in description
architecture/auth/model/detail   ✗ three levels — flatten to two
```

### Lifecycle and pruning

Topic keys are not pruned automatically. An observation updated via upsert keeps a single row with the latest content and an incremented `revision_count`. Use `mem_delete` to remove an observation (soft-delete by default) when a topic is no longer relevant. Soft-deleted observations are excluded from search and context but their IDs remain in the store for audit purposes. Use `--hard` to remove them permanently.

### Scope interaction

`topic_key` upsert is scoped to `project + scope + topic_key`. The same key used with different scopes creates independent observations:

```
project=engram, scope=project, topic_key=architecture/auth-model  → observation A
project=engram, scope=personal, topic_key=architecture/auth-model → observation B (independent)
```

This means a `personal` note on the same topic does not overwrite the shared `project` observation.

---

## Project Structure

```
engram/
├── cmd/engram/main.go              # CLI entrypoint
├── internal/
│   ├── store/store.go              # Core: SQLite + FTS5 + all data ops
│   ├── server/server.go            # HTTP REST API (port 7437)
│   ├── mcp/mcp.go                  # MCP stdio server (23 tools)
│   ├── setup/setup.go              # Agent plugin installer (go:embed)
│   ├── cloud/                       # Optional cloud runtime (Postgres + dashboard)
│   │   ├── cloudserver/             # /sync API + dashboard mount + auth/session bridge
│   │   ├── cloudstore/              # Cloud chunk storage and dashboard read-model queries
│   │   ├── dashboard/               # Server-rendered dashboard routes + embedded static assets
│   │   └── auth/                    # Bearer token auth + signed dashboard sessions
│   ├── project/                     # Project name detection + similarity matching
│   │   └── detect.go               # DetectProject, DetectProjectFull, 5-case algorithm
│   ├── sync/sync.go                # Git sync: manifest + compressed chunks
│   └── tui/                        # Bubbletea terminal UI
│       ├── model.go                # Screen constants, Model, Init()
│       ├── styles.go               # Lipgloss styles (Catppuccin Mocha)
│       ├── update.go               # Input handling, per-screen handlers
│       └── view.go                 # Rendering, per-screen views
├── plugin/
│   ├── opencode/engram.ts          # OpenCode adapter plugin
│   └── claude-code/                # Claude Code plugin (hooks + skill)
│       ├── .claude-plugin/plugin.json
│       ├── .mcp.json
│       ├── hooks/hooks.json
│       ├── scripts/                # session-start, post-compaction, subagent-stop, session-end
│       └── skills/memory/SKILL.md
├── skills/                         # Contributor AI skills (repo-wide standards + Engram-specific guardrails)
├── setup.sh                        # Links repo skills into .claude/.codex/.gemini (project-local)
├── assets/                         # Screenshots and media
├── DOCS.md                         # Full technical documentation
├── CONTRIBUTING.md                 # Contribution workflow and standards
├── go.mod
└── go.sum
```

---

## CLI Reference

The CLI offers local memory and project operations alongside optional cloud replication and administration. The canonical command inventory and detailed links are in [DOCS.md CLI Reference](../DOCS.md#cli-reference).

Local server auth:

- `ENGRAM_HTTP_TOKEN`: optional Bearer auth for `engram serve`. When set, `DELETE /sessions/{id}`, `DELETE /observations/{id}`, `DELETE /prompts/{id}`, `GET /export`, and `POST /import` require `Authorization: Bearer <token>`. `POST /projects/rescue-ownership` always requires a configured token and matching Bearer credential; deprecated alias `POST /projects/migrate` uses the same handler and requirement. Comparison is constant-time; token is read per-request. Other routes remain open when unset (zero-config default). Ownership repair does not depend on this token: `engram projects rescue-ownership` does the same work against the local store.
- `ENGRAM_TIMEZONE`: IANA zone name for timestamp display in TUI and cloud dashboard (e.g. `America/New_York`). Falls back to system local when unset or invalid.

Project selection for reads is explicit: omitted project selectors resolve the current project (explicit project, then `ENGRAM_PROJECT`, then cwd detection). Use `--all` in the CLI or `all_projects=true` in HTTP to intentionally read every project. Do not combine an explicit project with an all-project selector. `engram context` accepts its legacy positional project as an alias for `--project`; the two forms cannot be combined. `GET /sync/status` supports only one resolved project and rejects `all_projects=true`.

Cloud constraints (current behavior):

- Cloud is opt-in replication/shared access; local SQLite remains source of truth.
- `engram cloud serve` requires `ENGRAM_CLOUD_ALLOWED_PROJECTS` in both token-auth and insecure no-auth mode. Use `*` to allow all projects (dev/internal deploys) — bypasses per-project name enforcement while still requiring a non-empty project on each request.
- Authenticated cloud serve requires `ENGRAM_CLOUD_TOKEN` + explicit non-default `ENGRAM_JWT_SECRET`.
- Insecure local-dev mode (`ENGRAM_CLOUD_INSECURE_NO_AUTH=1`) still requires the project allowlist and must not be used in production.

Cloud route/auth split (current behavior):

`engram serve` and `engram cloud serve` are distinct runtimes: the former serves local APIs, while the latter serves cloud sync and the browser dashboard. In authenticated cloud mode, sync requests use a bearer token in the Authorization header; protected dashboard browser routes instead require a signed cookie obtained through login, not a bearer header as a browser-session substitute. Insecure development mode bypasses dashboard authentication and is distinct from authenticated mode.

For documented methods and paths and the dashboard route tree, see [HTTP API Endpoints](../DOCS.md#http-api-endpoints). For routes not yet documented there, check the [cloud server route registrations](../internal/cloud/cloudserver/cloudserver.go).

---

## Dashboard visual-parity layer

The cloud dashboard (`internal/cloud/dashboard/`) is rendered server-side using [templ](https://templ.guide/) components. This section documents the key invariants introduced in the `cloud-dashboard-visual-parity` change.

### Principal bridge

Display name is surfaced through `MountConfig.GetDisplayName func(r *http.Request) string`. If nil or if the closure returns an empty string, all handlers fall back to `"OPERATOR"`. The bridge is implemented in `internal/cloud/dashboard/principal.go` and accessed via `h.principalFromRequest(r)`. Handlers never read `r.Context()` for identity — they read the `MountConfig` closures.

### Push-path pause guard

A per-project sync pause is stored in `cloud_project_controls` (Postgres). The `POST /sync/push` handler in `cloudserver.go` checks `IsProjectSyncEnabled(project)` immediately after `authorizeProjectScope` succeeds, using a structural interface assertion. A paused project returns HTTP 409 Conflict with `error_code: "sync-paused"`. This is enforced server-side — the admin toggle is never purely cosmetic. Regression guard: `TestPushPathPauseEnforcement` in `cloudserver_test.go`.

### Audit log boundary (structural interface pattern)

When a push is rejected due to a paused project, both `handleMutationPush` and `handlePushChunk` emit an audit entry via `cloudstore.InsertAuditEntry`. The audit capability is accessed through a structural type assertion against an anonymous interface:

```go
if auditor, ok := s.store.(interface {
    InsertAuditEntry(ctx context.Context, entry cloudstore.AuditEntry) error
}); ok {
    _ = auditor.InsertAuditEntry(r.Context(), cloudstore.AuditEntry{...})
}
```

**Why `ChunkStore` and `MutationStore` are NOT extended**: these interfaces are consumed by a wide surface of test fakes, integration adapters, and future clients. Adding `InsertAuditEntry` to them would require every implementer (real and fake) to implement a method that is only relevant to the rejection path. The structural assertion isolates the audit concern at the call site and makes it trivially optional: stores that do not implement `InsertAuditEntry` (e.g., legacy test fakes) silently skip the audit with a log warning — no panic, no 5xx.

**Synchronous insert rationale**: the insert is synchronous (no goroutine, no channel) because the 409 is already a fast path (the data was never stored). The ~1ms overhead of a Postgres `INSERT` is acceptable; the alternative (buffered async) would complicate recovery, lose entries on process restart, and add concurrency concerns with no meaningful latency benefit for the caller.

**Audit boundary**: audit entries are only emitted at the pause-rejection site. Pull requests (`GET /sync/mutations/pull`) never emit audit entries; paused projects continue to serve reads unrestricted.

### Composite-ID URL scheme

Detail pages use composite path parameters because the integrated store is chunk-centric (no globally unique numeric IDs):

| Page | URL pattern |
|------|-------------|
| Session detail | `GET /dashboard/sessions/{project}/{sessionID}` |
| Observation detail | `GET /dashboard/observations/{project}/{sessionID}/{syncID}` |
| Prompt detail | `GET /dashboard/prompts/{project}/{sessionID}/{syncID}` |

Path values are extracted via `r.PathValue(name)` (Go 1.22 `net/http.ServeMux`). `syncID` values are validated as non-empty with `len <= 128`.

### Insecure-mode regression guard

`TestInsecureModeLoginRedirects` in `cloudserver_test.go` asserts that `GET /dashboard/login` with `auth == nil` returns 303 to `/dashboard/`. This prevents silent regressions if the no-auth short-circuit path is modified.

---

## Cloud Autosync Manager

`internal/cloud/autosync/Manager` is a lease-guarded background goroutine started by `engram serve` and `engram mcp` when `ENGRAM_CLOUD_AUTOSYNC=1`. It implements the local-first invariant: all network I/O happens in its own goroutine and never holds locks shared with the SQLite write path.

### Data flow

```
Local Write → store.WriteObservation → [SQLite sync_mutations journal]
                                               ↓ (onWrite hook)
                                       Manager.NotifyDirty() [buffered-1 chan]
                                               ↓ (debounce 500ms)
                                       Manager.cycle()
                                         ├─ AcquireSyncLease (SQLite)
                                         ├─ push: ListPendingSyncMutations → MutationTransport.PushMutations
                                         │         → POST /sync/mutations/push → cloudstore.InsertMutationBatch
                                         └─ pull: GetSyncState → MutationTransport.PullMutations
                                                   → GET /sync/mutations/pull (filtered by enrollment)
                                                   → store.ApplyPulledMutation
                                               ↓
                                       autosyncStatusAdapter → /sync/status → dashboard pill
```

### Lease semantics

The Manager holds a SQLite-backed lease during each cycle. `StopForUpgrade` sets `PhaseDisabled` and does NOT release the lease, so no other worker can pick up sync during an upgrade window. `ResumeAfterUpgrade` clears the disabled flag and re-arms the loop without restarting the process.

### Mutation endpoints

The cloud server exposes two routes registered in `cloudserver.go` and handled by `mutations.go`:

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/sync/mutations/push` | Accept a batch of up to 100 mutations from the client |
| `GET` | `/sync/mutations/pull` | Return mutations since a cursor, filtered by caller's enrolled projects |

Both require `Authorization: Bearer <token>`. Push enforces the project-level sync pause (HTTP 409 on `sync_enabled=false`) and the configured cloud push body limit (`ENGRAM_CLOUD_MAX_PUSH_BYTES`, default 8 MiB). Pull filters server-side.

---

## Next Steps

- [Agent Setup](AGENT-SETUP.md) — connect your agent to Engram
- [Plugins](PLUGINS.md) — what the OpenCode and Claude Code plugins add
- [Obsidian Brain](beta/obsidian-brain.md) — visualize memories as a knowledge graph (beta)

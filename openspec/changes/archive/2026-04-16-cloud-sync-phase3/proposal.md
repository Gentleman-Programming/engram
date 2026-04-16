# Cloud Sync Phase 3 — Client Integration

**Change:** `cloud-sync-phase3`
**Status:** Proposed
**Created:** 2026-04-14

---

## 1. Intent

Connect the local engram client to the cloud server built in Phase 2. Today, engram stores memories exclusively in local SQLite (with git-based chunk sync for sharing). Phase 3 adds two new operating modes:

- **Cloud-only (`RemoteStore`)**: Proxy all StoreInterface operations to the cloud server via HTTP. No local SQLite needed. For users who want a single source of truth on the server.
- **Local-first with cloud sync (`SyncClient`)**: Keep SQLite as the primary store and sync mutations bidirectionally with the cloud server in the background. For users who want offline-capable local storage with cloud backup and team sharing.

This completes the cloud story: Phase 1 laid the local foundation (sync_mutations, enrollment, StoreInterface), Phase 2 built the server, Phase 3 connects them.

## 2. Scope

### In Scope

1. **`internal/remote/store.go` — RemoteStore**
   - Implements `types.StoreInterface` (both `StoreReader` and `StoreWriter`)
   - Does NOT implement `types.StoreSyncer` — it IS the cloud, no syncing needed
   - Every method is an HTTP call to the cloud server endpoints
   - Handles authentication (API key in header), retries with exponential backoff, timeout
   - Constructor takes `serverURL`, `apiKey`, `project` (scoped to one project)

2. **`internal/remote/client.go` — HTTP client wrapper**
   - Shared HTTP client with connection pooling, timeouts, User-Agent
   - Methods: `get`, `post`, `delete` mapping to cloud API routes
   - Error types: `ErrUnauthorized`, `ErrNotFound`, `ErrRateLimited`, `ErrServerError`
   - Retry logic: exponential backoff for 429/5xx, no retry for 4xx

3. **`internal/remote/sync.go` — SyncClient**
   - Works WITH the existing `*store.Store` (SQLite), not instead of it
   - **Push path**: reads `store.ListPendingSyncMutations()`, batches them, POSTs to `/api/v1/sync/push`, ACKs via `store.AckSyncMutations()`
   - **Pull path**: GETs `/api/v1/sync/pull?since_seq=<cursor>`, applies via `store.ApplyPulledMutation()`, updates local cursor
   - **Auto-push**: hooks into `server.SetOnWrite()` with a 10-second debounce timer — any local write resets the timer, push fires 10s after last write
   - **Background pull**: periodic loop (default 120s, configurable) polls for remote changes
   - Uses `store.AcquireSyncLease()` / `ReleaseSyncLease()` to prevent concurrent sync
   - Uses `store.MarkSyncFailure()` / `MarkSyncHealthy()` for circuit-breaker backoff
   - Respects project enrollment: only syncs projects registered via `store.EnrollProject()`

4. **`internal/remote/config.go` — Configuration**
   - `CloudConfig` struct: `ServerURL`, `APIKey`, `Mode` (cloud-only | local-sync), `Project`, `PushDebounce`, `PullInterval`
   - `LoadFromStore(s *store.Store)` — reads from `sync_cloud_config` table (already exists)
   - `SaveToStore(s *store.Store)` — persists to `sync_cloud_config` table
   - Environment variable overrides: `ENGRAM_CLOUD_URL`, `ENGRAM_CLOUD_KEY`, `ENGRAM_CLOUD_MODE`

5. **CLI commands (`cmd/engram/main.go`)**
   - `engram cloud setup` — interactive: prompts for server URL, API key, mode; validates connection; saves to sync_cloud_config
   - `engram cloud sync` — manual one-shot: push pending, then pull new; shows summary
   - `engram cloud status` — shows: mode, server URL, connection health, pending mutations count, last sync time, enrolled projects
   - `engram cloud enroll <project>` — enrolls a project for sync (calls `store.EnrollProject`)
   - `engram cloud unenroll <project>` — removes project from sync

6. **Backend flag on existing commands**
   - `engram mcp --backend cloud` — creates `RemoteStore` instead of `*store.Store`, passes to MCP handler
   - `engram serve --backend cloud` — creates `RemoteStore` instead of `*store.Store`, passes to HTTP server
   - `engram mcp --backend local-sync` — uses `*store.Store` + starts `SyncClient` in background
   - `engram serve --backend local-sync` — uses `*store.Store` + starts `SyncClient` in background
   - Default remains `--backend local` (current behavior, no cloud)

7. **Tests**
   - Unit tests for RemoteStore with httptest server mocking cloud endpoints
   - Unit tests for SyncClient push/pull/debounce/backoff logic
   - Integration test: local store -> SyncClient -> httptest cloud -> verify round-trip
   - Config load/save tests

### Out of Scope

- **Conflict resolution** — Phase 2's server already handles last-writer-wins via server_seq ordering. No client-side CRDT needed.
- **Multi-project RemoteStore** — RemoteStore is scoped to one project. Multi-project requires multiple instances (or a future enhancement).
- **WebSocket/SSE real-time push** — Pull-based polling is sufficient for v1. Real-time can be added later.
- **Encryption at rest** — The cloud server handles storage; client trusts TLS for transport.
- **TUI cloud integration** — TUI continues to use whatever store is configured; no TUI-specific cloud UI.
- **User management / registration** — Users are provisioned server-side (Phase 2). Client only authenticates.
- **Obsidian export from cloud** — Obsidian export works on local store only.

## 3. Approach

### Architecture

```
┌─────────────────────────────────────────────────────┐
│                   cmd/engram/main.go                 │
│                                                     │
│  --backend local       (default, current behavior)  │
│  --backend cloud       (RemoteStore)                │
│  --backend local-sync  (Store + SyncClient)         │
└──────────┬──────────────────────┬───────────────────┘
           │                      │
     ┌─────▼──────┐        ┌─────▼──────┐
     │ RemoteStore │        │  store.Store│
     │ (HTTP proxy)│        │  (SQLite)   │
     └─────┬──────┘        └──┬───┬──────┘
           │                  │   │
           │            ┌─────┘   └─────┐
           │            │               │
           ▼            ▼               ▼
     ┌──────────┐  ┌─────────┐   ┌───────────┐
     │  Cloud   │  │  MCP /  │   │ SyncClient │
     │  Server  │  │  HTTP   │   │ (push/pull)│
     │ (Phase 2)│  │ Handler │   └─────┬──────┘
     └──────────┘  └─────────┘         │
                                       ▼
                                 ┌──────────┐
                                 │  Cloud   │
                                 │  Server  │
                                 └──────────┘
```

### Key Design Decisions

1. **RemoteStore is stateless** — no caching, no local state. Every call hits the server. Simple, predictable, easy to test. If the server is down, operations fail immediately (with retries for transient errors).

2. **SyncClient is decoupled from store operations** — it reads the `sync_mutations` table (already auto-populated by SQLite triggers from Phase 1) and pushes them. It does NOT intercept or wrap store writes. This means zero changes to the existing store.Store code paths.

3. **ID mapping for RemoteStore** — The cloud server uses string-based sync_ids (e.g., `obs-abc123`), but StoreInterface uses `int64` IDs. RemoteStore will need to handle this mapping. Two options:
   - **(a)** Cloud server returns numeric IDs alongside sync_ids (requires server change)
   - **(b)** RemoteStore maintains a local ID cache (sync_id -> sequential int64)
   - **Decision: (a)** — add an `id` field to cloud responses. Cleaner, no local state in RemoteStore.

4. **Push batching** — SyncClient reads up to 100 pending mutations per push cycle. If more exist, it loops until drained. Each batch is a single POST to `/api/v1/sync/push`.

5. **Pull cursor persistence** — SyncClient stores `last_pulled_seq` in `sync_state` table (already exists, keyed by target_key "cloud"). On restart, it resumes from where it left off.

6. **Graceful shutdown** — SyncClient exposes `Start(ctx)` and `Stop()`. On stop: cancel background goroutines, flush pending push (best-effort with 5s timeout), release sync lease.

### File Layout

```
internal/remote/
  client.go      — HTTP client wrapper (auth, retries, errors)
  store.go       — RemoteStore implementing StoreInterface
  sync.go        — SyncClient (push/pull/auto-sync)
  config.go      — CloudConfig + load/save
  client_test.go
  store_test.go
  sync_test.go
  config_test.go
```

### Integration Points

| Existing code | How Phase 3 connects |
|---|---|
| `types.StoreInterface` | RemoteStore implements it |
| `store.ListPendingSyncMutations()` | SyncClient reads from it |
| `store.AckSyncMutations()` | SyncClient ACKs after successful push |
| `store.ApplyPulledMutation()` | SyncClient applies pulled changes |
| `store.AcquireSyncLease()` | SyncClient holds lease during sync |
| `store.MarkSyncFailure/Healthy()` | SyncClient reports health |
| `store.EnrollProject()` | CLI `cloud enroll` calls it |
| `sync_cloud_config` table | Config load/save |
| `server.SetOnWrite()` | SyncClient hooks for debounced auto-push |
| `cloudserver` endpoints | RemoteStore and SyncClient call them |
| `cmd/engram/main.go` | --backend flag, cloud subcommands |

## 4. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **ID type mismatch** (cloud uses string sync_ids, StoreInterface uses int64) | High | Medium | Add numeric ID field to cloud API responses; or use a deterministic hash-to-int64 mapping |
| **Network reliability** — push/pull fails mid-batch | Medium | Low | Idempotency keys on push (already supported by server), ACK only after confirmed success, pull resumes from cursor |
| **Race between auto-push and manual sync** | Medium | Low | Sync lease (already implemented) prevents concurrent execution |
| **StoreInterface method coverage gaps** — some methods may not have cloud server equivalents | Medium | High | Audit every StoreInterface method against cloud endpoints before implementation. Methods like `PassiveCapture`, `MigrateProject`, `Timeline`, `DeleteSession` need cloud endpoint additions |
| **Breaking change in cloud API** | Low | High | Protocol version header (already implemented in Phase 2 middleware) enables graceful version negotiation |
| **Large initial sync** — first pull for a project with thousands of observations | Medium | Medium | Chunked background pull (see below) — developer never waits |

### Large Sync Mitigation: Chunked Background Pull

Sync is an **async enrichment**, not a prerequisite. The local store works 100% while sync runs in background.

**Initial sync (seq=0)**:
- Pull loop runs in background goroutine
- Pages through entities: `GET /pull?since_seq={cursor}&limit=500`
- Each page: apply to local store → update cursor → sleep 100ms → next page
- If interrupted (app closes), resumes from last cursor on next start
- `engram cloud status` shows progress: `"syncing: 2500/8000 entities (31%)"`

**Reconnection after long offline**:
- Push first: drain pending `sync_mutations` (local changes while offline) — fast, only delta
- Pull after: resume from last cursor, page through accumulated remote changes
- Developer keeps working — reads/writes hit local SQLite, never blocked

**Key invariant**: MCP/serve commands NEVER wait for sync. SyncClient.Start() launches goroutines and returns immediately.

### Endpoint Gap Analysis

The following StoreInterface methods do NOT have direct cloud server endpoints yet:

| Method | Status | Plan |
|---|---|---|
| `CreateSession` | No endpoint | Push via sync/push mutation (entity=session, op=create) |
| `EndSession` | No endpoint | Push via sync/push mutation (entity=session, op=update) |
| `DeleteSession` | No endpoint | Push via sync/push mutation (entity=session, op=delete) |
| `UpdateObservation` | No endpoint | Push via sync/push mutation (entity=observation, op=update) |
| `DeleteObservation` | No endpoint | Push via sync/push mutation (entity=observation, op=delete) |
| `AddPrompt` | No endpoint | Push via sync/push mutation (entity=prompt, op=create) |
| `DeletePrompt` | No endpoint | Push via sync/push mutation (entity=prompt, op=delete) |
| `RecentSessions` | No endpoint | **Needs new cloud endpoint** or query via search |
| `AllSessions` | No endpoint | **Needs new cloud endpoint** |
| `SessionObservations` | No endpoint | **Needs new cloud endpoint** |
| `RecentObservations` | No endpoint | **Needs new cloud endpoint** or covered by search |
| `AllObservations` | No endpoint | **Needs new cloud endpoint** |
| `Timeline` | No endpoint | **Needs new cloud endpoint** |
| `RecentPrompts` | No endpoint | **Needs new cloud endpoint** |
| `SearchPrompts` | No endpoint | **Needs new cloud endpoint** |
| `PassiveCapture` | No endpoint | **Needs new cloud endpoint** (composite operation) |
| `MigrateProject` | No endpoint | **Needs new cloud endpoint** |

For **SyncClient** (local-first mode), this gap is irrelevant — all operations happen on local SQLite and sync via mutations. For **RemoteStore** (cloud-only mode), we need ~8 new cloud endpoints. These can be added as part of Phase 3 since the cloudstore already has the data; it just needs HTTP handlers.

## 5. Dependencies

### Hard Dependencies (must exist before Phase 3)

- **Phase 1** (complete): StoreInterface extraction, sync_mutations table, import idempotency
- **Phase 2** (complete): Cloud server with push/pull, CRUD, search, auth, batch endpoints
- **`sync_cloud_config` table** (complete): Created in Phase 1 cloud migration
- **`sync_state` table** (complete): Created in Phase 1, keyed by "cloud" target
- **Project enrollment** (complete): `EnrollProject`, `IsProjectEnrolled`, `ListEnrolledProjects`

### Soft Dependencies (nice to have, can be added incrementally)

- **Additional cloud endpoints** for full RemoteStore coverage (sessions, prompts, timeline, etc.) — can be added as part of this phase or deferred to a follow-up if only SyncClient is needed first
- **Cloud server session management endpoints** — needed for RemoteStore but not SyncClient

### Go Dependencies

- No new external dependencies expected. `net/http` stdlib for the HTTP client. Cloud server already uses `chi` and `pgx`.

## 6. Implementation Order

Recommended phasing within Phase 3:

1. **HTTP client wrapper** (`internal/remote/client.go`) — foundation for everything
2. **Config** (`internal/remote/config.go`) — load/save cloud configuration
3. **SyncClient** (`internal/remote/sync.go`) — push/pull using existing mutations infrastructure
4. **CLI cloud commands** — `cloud setup`, `cloud sync`, `cloud status`, `cloud enroll`
5. **RemoteStore** (`internal/remote/store.go`) — requires new cloud endpoints for full coverage
6. **Backend flag** on `mcp` and `serve` commands
7. **Additional cloud endpoints** (cloudserver) — for RemoteStore methods without existing endpoints

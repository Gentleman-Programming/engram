# Design: cloud-sync-phase3 — Client Integration

**Change:** `cloud-sync-phase3`
**Status:** Designed
**Created:** 2026-04-14

---

## 1. Architecture Overview

```
                            cmd/engram/main.go
                    ┌──────────────┼──────────────┐
                    │              │              │
              --backend       --backend      --backend
               local           cloud        local-sync
                    │              │              │
                    ▼              ▼              ▼
              ┌──────────┐  ┌───────────┐  ┌──────────┐
              │store.Store│  │RemoteStore│  │store.Store│
              │ (SQLite)  │  │(HTTP proxy│  │ (SQLite)  │
              └─────┬─────┘  └─────┬─────┘  └──┬───┬───┘
                    │              │            │   │
                    ▼              │            ▼   ▼
              ┌──────────┐        │     ┌────────┐ ┌──────────┐
              │ MCP/HTTP │        │     │MCP/HTTP│ │SyncClient│
              │ Handlers │        │     │Handlers│ │(push/pull│
              └──────────┘        │     └────────┘ │ debounce)│
                                  │                └────┬─────┘
                                  ▼                     ▼
                           ┌─────────────────────────────────┐
                           │     Cloud Server (Phase 2)      │
                           │  /api/v1/sync/push|pull          │
                           │  /api/v1/observations|sessions   │
                           │  /api/v1/search|context|stats    │
                           └─────────────────────────────────┘
```

**Key architectural invariant**: The `types.StoreInterface` boundary is never broken. MCP and HTTP handlers receive a `StoreInterface` and never know whether it is backed by SQLite or HTTP. SyncClient operates alongside the local store, not inside the interface chain.

---

## 2. Design Decisions

### Decision 1: ID Mapping Strategy — Option A (Server-Returned Numeric IDs)

**Chosen:** The cloud server returns a numeric `id` (int64) alongside the string `sync_id` in all responses.

**Rejected alternatives:**
- *Option B (deterministic hash)*: `int64(fnv64(sync_id))` would produce collisions at scale (~50k observations reaches birthday-problem territory for 64-bit space with non-uniform distribution). Hash-based IDs also break referential expectations — two different clients would compute the same ID for the same sync_id, but the ID has no server-side meaning.
- *Option C (in-memory map)*: Violates the "RemoteStore is stateless" requirement (REQ-REMOTE-002). Would leak memory on long-running processes and lose mappings on restart.

**Implementation:** Add a `BIGSERIAL id` column (or use a sequence) to cloud Postgres tables. The cloud already has UUID `id` columns; we add a `numeric_id BIGSERIAL` column to `observations`, `sessions`, and `prompts`. Responses include both `id` (UUID) and `numeric_id` (int64). RemoteStore uses `numeric_id` for all `int64` return values.

**Server change required:** Add `numeric_id` to:
- `handleCreateObservation` response
- `handleGetObservation` response
- All new list/query endpoints
- Pull response entities (for SyncClient → ApplyPulledMutation mapping)

**Migration:** `ALTER TABLE observations ADD COLUMN numeric_id BIGSERIAL;` (same for sessions, prompts). Backfill existing rows with sequence values.

### Decision 2: SyncClient Goroutine Model

```
SyncClient.Start(ctx)
    │
    ├─► goroutine 1: pushLoop (long-lived)
    │     • blocks on debounce timer channel
    │     • on timer fire: acquireLease → drain mutations → releaseLease
    │     • on ctx.Done(): exit
    │
    ├─► goroutine 2: pullLoop (long-lived)
    │     • ticker at PullInterval (default 120s)
    │     • on tick: acquireLease → paginated pull → releaseLease
    │     • on ctx.Done(): exit
    │
    └─► goroutine 3: debounceRelay (long-lived)
          • receives write notifications from server.SetOnWrite()
          • resets/starts the debounce timer (time.AfterFunc)
          • sends on pushTrigger channel when timer fires
          • on ctx.Done(): stop timer, exit
```

**Design details:**

- **Total goroutines:** 3 (fixed, not per-project).
- **Push and pull are sequential** with respect to each other via the sync lease. They do NOT run concurrently — one waits for the other to release the lease. This prevents interleaving of push/pull that could cause ordering issues.
- **Debounce timer:** `time.AfterFunc` pattern. The `debounceRelay` goroutine owns the timer. On each `SetOnWrite()` callback: if timer is running, `timer.Stop()` then `timer.Reset(PushDebounce)`. If not running, `time.AfterFunc(PushDebounce, triggerPush)`. The callback sends on a buffered(1) `pushTrigger` channel.
- **Graceful shutdown:** `SyncClient.Stop()` cancels the context, then does a best-effort flush:
  ```
  Stop():
    1. cancel(ctx)                          // signals all 3 goroutines
    2. flushCtx, cancel := context.WithTimeout(bg, 5s)
    3. pushOnce(flushCtx)                   // best-effort drain
    4. releaseLease()                        // cleanup
    5. wg.Wait()                            // wait for goroutines (bounded by ctx cancel)
  ```
- **Error reporting:** Errors are logged via `log.Printf` (standard library logger). No channel or callback — callers observe health via `store.GetSyncStatus("cloud")` which reads the `sync_state` table.

### Decision 3: Missing Cloud Endpoints — Incremental with Priority Tiers

**Tier 1 — Required for RemoteStore MVP (8 new endpoints):**

| Endpoint | Method | Route | cloudstore method |
|----------|--------|-------|-------------------|
| RecentSessions | GET | `/api/v1/sessions` | `ListRecentSessions` |
| AllSessions | GET | `/api/v1/sessions/all` | `ListAllSessions` |
| SessionObservations | GET | `/api/v1/sessions/{id}/observations` | `GetSessionObservations` |
| RecentObservations | GET | `/api/v1/observations/list` | `ListRecentObservations` |
| AllObservations | GET | `/api/v1/observations/all` | `ListAllObservations` |
| Timeline | GET | `/api/v1/observations/{id}/timeline` | `GetTimeline` |
| RecentPrompts | GET | `/api/v1/prompts` | `ListRecentPrompts` |
| SearchPrompts | GET | `/api/v1/prompts/search` | `SearchPrompts` |

**Tier 2 — Required for full RemoteStore (2 composite endpoints):**

| Endpoint | Method | Route | cloudstore method |
|----------|--------|-------|-------------------|
| PassiveCapture | POST | `/api/v1/passive-capture` | `PassiveCapture` |
| MigrateProject | POST | `/api/v1/projects/migrate` | `MigrateProject` |

**Incremental strategy:** SyncClient needs ZERO new endpoints — it uses only `/sync/push` and `/sync/pull` which already exist. RemoteStore can be implemented incrementally: Tier 1 endpoints enable all read operations, while write operations go through the push mutation path (see Decision 4). Tier 2 can follow.

**Handler pattern:** All new handlers follow the exact pattern in `server.go`:
```go
func handleListRecentSessions(store *cloudstore.Store) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        userID := UserIDFromContext(r.Context())
        project := r.URL.Query().Get("project")
        limit := queryInt(r, "limit", 10)
        // ... membership check, query, writeJSON
    }
}
```

All Tier 1 endpoints are added to the existing CRUD route group (rate-limited at 600/min).

**Batch optimization:** The batch endpoint (`POST /api/v1/batch`) can aggregate multiple read calls in a single round trip. RemoteStore MAY use batch for `FormatContext` (which is already a single endpoint) but SHOULD NOT batch write operations to preserve per-operation error isolation.

### Decision 4: RemoteStore Write Path — Option B (Sync/Push Mutations)

**Chosen:** RemoteStore wraps all write operations as sync/push mutations and POSTs them to `/api/v1/sync/push`.

**Rejected alternative:**
- *Option A (dedicated CRUD endpoints)*: Would require 7 additional write endpoints (CreateSession, EndSession, DeleteSession, UpdateObservation, DeleteObservation, AddPrompt, DeletePrompt). Each would duplicate logic already in `processObservationMutation`, `processSessionMutation`, `processPromptMutation`. More surface area, more tests, more maintenance.

**Justification:**
1. The push endpoint already handles all entity types (observation, session, prompt) and all operations (upsert, delete).
2. `ProcessPush` applies changes immediately and synchronously — the response includes the `server_seq`, confirming the write.
3. RemoteStore constructs a `cloudstore.Mutation` struct for each write, POSTs a single-mutation batch, and reads the result.
4. The `numeric_id` for the created entity is returned via an extended `PushResult` (see API Additions below).

**Example — AddObservation via push:**
```go
func (rs *RemoteStore) AddObservation(p types.AddObservationParams) (int64, error) {
    syncID := "obs-" + randomHex(8)
    mutation := Mutation{
        Seq:       1,
        Entity:    "observation",
        EntityKey: syncID,
        Op:        "upsert",
        Payload: map[string]any{
            "sync_id":    syncID,
            "session_id": p.SessionID,
            "type":       p.Type,
            "title":      p.Title,
            "content":    p.Content,
            "tool_name":  p.ToolName,
            "scope":      p.Scope,
            "topic_key":  p.TopicKey,
        },
        OccurredAt: time.Now().UTC().Format(time.RFC3339),
    }
    result, err := rs.client.push(rs.ctx, rs.project, []Mutation{mutation})
    if err != nil {
        return 0, err
    }
    return result.EntityIDs[syncID], nil // numeric_id from extended response
}
```

**Server change required:** Extend `PushResult` to include a map of `sync_id → numeric_id` for created/updated entities:
```go
type PushResult struct {
    AckedSeq   int64            `json:"acked_seq"`
    ServerSeq  int64            `json:"server_seq"`
    Conflicts  []Conflict       `json:"conflicts,omitempty"`
    EntityIDs  map[string]int64 `json:"entity_ids,omitempty"` // NEW: sync_id → numeric_id
}
```

### Decision 5: --backend Flag Wiring

**Store creation fork point:** In `cmdServe` and `cmdMCP`, immediately after config loading and before handler creation.

```go
func cmdServe(cfg store.Config) {
    // ... port parsing ...

    backend := parseBackendFlag()  // "local" | "cloud" | "local-sync"

    var si types.StoreInterface
    var cleanup func()

    switch backend {
    case "local":
        s, err := storeNew(cfg)
        // ...
        si = s
        cleanup = func() { s.Close() }

    case "cloud":
        cloudCfg, err := loadCloudConfig(cfg)
        // ...
        rs, err := remote.NewStore(cloudCfg)
        // ...
        si = rs
        cleanup = func() {} // stateless, nothing to close

    case "local-sync":
        s, err := storeNew(cfg)
        // ...
        cloudCfg, err := remote.LoadFromStore(s)
        // ...
        sc := remote.NewSyncClient(s, cloudCfg)
        ctx, cancel := context.WithCancel(context.Background())
        sc.Start(ctx)
        si = s
        cleanup = func() { sc.Stop(); cancel(); s.Close() }
    }

    defer cleanup()

    srv := newHTTPServer(si, port)  // server.New accepts StoreInterface
    // ...
}
```

**Key wiring changes:**
1. `server.New` signature changes from `func New(s *store.Store, port int)` to `func New(s types.StoreInterface, port int)`. The `SetOnWrite` hook is only called when the underlying store is `*store.Store` (type assertion).
2. `mcp.NewServerWithConfig` signature changes from `func NewServerWithConfig(s *store.Store, ...)` to `func NewServerWithConfig(s types.StoreInterface, ...)`.
3. `parseBackendFlag()` helper reads `--backend` from `os.Args`. Default: `"local"`.
4. For `local-sync`, the `SyncClient` hooks into `server.SetOnWrite` for debounced auto-push. The hook is wired AFTER server creation but BEFORE `Start()`:
   ```go
   srv := newHTTPServer(s, port)
   srv.SetOnWrite(sc.NotifyWrite) // SyncClient receives write notifications
   sc.Start(ctx)
   ```

**Shutdown sequence for `local-sync`:**
```
SIGTERM received
  → sc.Stop()       // cancel goroutines, flush pending (5s max), release lease
  → cancel()        // cancel background context
  → s.Close()       // close SQLite
  → exit
```

### Decision 6: Chunked Pull Design

**Page size:** 500 entities (matches server-side `Pull` limit cap).

**Sleep between pages:** 100ms (`time.Sleep(100 * time.Millisecond)`) — prevents hammering the server during large initial syncs while maintaining reasonable throughput (~5000 entities/second theoretical max).

**Progress tracking:**
- After each page: update `last_pulled_seq` in `sync_state` table via `store.UpdateSyncCursor("cloud", maxSeq)`.
- This is the cursor persistence mechanism — if the process crashes mid-pull, the next start resumes from the last committed cursor.
- No separate progress callback or channel. Progress is observable via `engram cloud status` which reads `sync_state.last_pulled_seq` and compares to the server's `max_seq` (from `/api/v1/health`).

**Progress display formula:**
```
pulled_seq / server_max_seq * 100 = progress%
Example: "syncing: 2500/8000 entities (31%)"
```

**Entities arriving during multi-page pull:**
- The pull query is `WHERE server_seq > $since_seq ORDER BY server_seq LIMIT 500`.
- New entities inserted during the pull get higher `server_seq` values.
- They naturally appear in later pages. No special handling needed.
- After the last partial page (< 500), the pull loop stops. The new entities are picked up on the next periodic pull (120s later) or on the next triggered pull.

**Cursor persistence between restarts:**
- Stored in `sync_state` table, column `last_pulled_seq`, key `"cloud"`.
- Already exists from Phase 1 schema. Read on startup, updated after each page.

**Pull algorithm pseudocode:**
```
func (sc *SyncClient) pullOnce(ctx context.Context) error {
    state, _ := sc.store.GetSyncStatus("cloud")
    cursor := state.LastPulledSeq

    for {
        result, err := sc.client.pull(ctx, sc.project, cursor, 500)
        if err != nil {
            return err
        }

        for _, entity := range result.Entities {
            mutation := entityToSyncMutation(entity)
            if err := sc.store.ApplyPulledMutation("cloud", mutation); err != nil {
                log.Printf("[sync] skip mutation %s: %v", entity.SyncID, err)
                continue  // skip failed, continue with rest
            }
        }

        cursor = result.MaxSeq
        sc.store.UpdateSyncCursor("cloud", cursor)

        if !result.HasMore {
            break
        }
        
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(100 * time.Millisecond):
            // throttle between pages
        }
    }
    return nil
}
```

---

## 3. Component Designs

### 3.1 HTTP Client (`internal/remote/client.go`)

```go
type Client struct {
    http      *http.Client
    baseURL   string
    apiKey    string
    version   string       // "engram-client/<version>"
    maxRetry  int          // default 3
}
```

**Connection pooling:** Uses a single `*http.Client` with `Transport: &http.Transport{MaxIdleConnsPerHost: 10, IdleConnTimeout: 90s}`.

**Request flow:**
```
caller → client.get/post/delete
  → buildRequest (set headers: Authorization: Bearer <apiKey>, User-Agent, X-Engram-Protocol: 1)
  → attempt loop (max 3):
      → http.Do(req.WithContext(attemptCtx))
      → if 429/5xx: sleep(backoff + jitter) → retry
      → if 4xx (not 429): return typed error immediately
      → if 2xx: return response
  → all retries exhausted: return last error
```

**Backoff calculation:** `min(500ms * 2^attempt, 30s) ± 10% jitter`

**Error types:**
```go
var (
    ErrUnauthorized = errors.New("unauthorized")
    ErrNotFound     = errors.New("not found")
    ErrRateLimited  = errors.New("rate limited")
    ErrServerError  = errors.New("server error")
)
```

### 3.2 Config (`internal/remote/config.go`)

```go
type CloudConfig struct {
    ServerURL     string        `json:"server_url"`
    APIKey        string        `json:"api_key"`
    Mode          string        `json:"mode"`           // "cloud-only" | "local-sync"
    Project       string        `json:"project"`
    PushDebounce  time.Duration `json:"push_debounce"`  // default 10s
    PullInterval  time.Duration `json:"pull_interval"`  // default 120s
}
```

**Load chain:** `sync_cloud_config` table → apply env overrides → validate.

The `sync_cloud_config` table already exists (Phase 1 migration). It stores key-value pairs. `LoadFromStore` reads all rows and populates the struct. `SaveToStore` upserts each field.

### 3.3 RemoteStore (`internal/remote/store.go`)

```go
type RemoteStore struct {
    client  *Client
    project string
}

var _ types.StoreInterface = (*RemoteStore)(nil)
```

**Method routing:**

| StoreInterface method | HTTP call |
|----------------------|-----------|
| GetObservation(id) | GET /observations/{id}?project= |
| Search(query, opts) | GET /search?q=&project=&limit= |
| RecentSessions(...) | GET /sessions?project=&limit= |
| AllSessions(...) | GET /sessions/all?project=&limit= |
| SessionObservations(...) | GET /sessions/{id}/observations |
| RecentObservations(...) | GET /observations/list?project=&scope=&limit= |
| AllObservations(...) | GET /observations/all?project=&scope=&limit= |
| FormatContext(...) | GET /context?project=&scope= |
| Timeline(id, ...) | GET /observations/{id}/timeline?before=&after= |
| Stats() | GET /stats?project= |
| RecentPrompts(...) | GET /prompts?project=&limit= |
| SearchPrompts(...) | GET /prompts/search?q=&project=&limit= |
| CreateSession(...) | POST /sync/push (mutation: session/upsert) |
| EndSession(...) | POST /sync/push (mutation: session/upsert) |
| DeleteSession(...) | POST /sync/push (mutation: session/delete) |
| AddObservation(...) | POST /sync/push (mutation: observation/upsert) |
| UpdateObservation(...) | POST /sync/push (mutation: observation/upsert) |
| DeleteObservation(...) | POST /sync/push (mutation: observation/delete) |
| AddPrompt(...) | POST /sync/push (mutation: prompt/upsert) |
| DeletePrompt(...) | POST /sync/push (mutation: prompt/delete) |
| PassiveCapture(...) | POST /passive-capture |
| MigrateProject(...) | POST /projects/migrate |

### 3.4 SyncClient (`internal/remote/sync.go`)

```go
type SyncClient struct {
    store       *store.Store
    client      *Client
    project     string
    pushDebounce time.Duration
    pullInterval time.Duration

    // internal
    cancel      context.CancelFunc
    wg          sync.WaitGroup
    pushTrigger chan struct{}    // buffered(1)
    leaseOwner  string          // unique per process: hostname-pid
}
```

**Lease owner identity:** `fmt.Sprintf("%s-%d", hostname, os.Getpid())` — unique enough to prevent self-deadlock on crash recovery (lease TTL expires, new process acquires).

**Lease TTL:** 60 seconds. Renewed implicitly by completing the sync cycle (release + re-acquire on next trigger).

---

## 4. Sequence Diagrams

### 4.1 Push Flow (Auto-Push via Debounce)

```
  MCP Handler        server.Server      SyncClient         Cloud Server
      │                   │                  │                    │
      │ AddObservation()  │                  │                    │
      ├──────────────────►│                  │                    │
      │                   │ notifyWrite()    │                    │
      │                   ├─────────────────►│                    │
      │                   │                  │ reset debounce     │
      │                   │                  │ timer (10s)        │
      │                   │                  │                    │
      │                   │   ... 10s pass with no writes ...    │
      │                   │                  │                    │
      │                   │                  │ timer fires        │
      │                   │                  │ AcquireSyncLease() │
      │                   │                  │ ListPending(100)   │
      │                   │                  │                    │
      │                   │                  │ POST /sync/push    │
      │                   │                  ├───────────────────►│
      │                   │                  │   {mutations:[...]}│
      │                   │                  │                    │
      │                   │                  │◄───────────────────┤
      │                   │                  │ {acked_seq, ...}   │
      │                   │                  │                    │
      │                   │                  │ AckSyncMutations() │
      │                   │                  │ ReleaseSyncLease() │
      │                   │                  │ MarkSyncHealthy()  │
```

### 4.2 Pull Flow (Periodic)

```
  SyncClient              store.Store          Cloud Server
      │                       │                     │
      │ (ticker fires)        │                     │
      │ AcquireSyncLease()    │                     │
      ├──────────────────────►│                     │
      │                       │                     │
      │ GetSyncStatus("cloud")│                     │
      ├──────────────────────►│                     │
      │ cursor = last_pulled  │                     │
      │                       │                     │
      │ GET /sync/pull?since_seq=cursor&limit=500   │
      ├────────────────────────────────────────────►│
      │                                             │
      │◄────────────────────────────────────────────┤
      │ {entities: [...], max_seq, has_more}        │
      │                       │                     │
      │ for each entity:      │                     │
      │  ApplyPulledMutation()│                     │
      ├──────────────────────►│                     │
      │                       │                     │
      │ UpdateSyncCursor()    │                     │
      ├──────────────────────►│                     │
      │                       │                     │
      │ if has_more: sleep 100ms, loop              │
      │ else: ReleaseSyncLease(), done              │
```

### 4.3 Initial Sync (First Start, seq=0)

```
  SyncClient.Start(ctx)
      │
      ├─► pushLoop goroutine starts (waits for trigger)
      ├─► pullLoop goroutine starts
      │     │
      │     │ immediate first pull (no ticker wait)
      │     │ cursor = 0 (no sync_state row yet)
      │     │
      │     │ GET /sync/pull?since_seq=0&limit=500
      │     │   → 500 entities, has_more=true
      │     │   → apply all, update cursor
      │     │   → sleep 100ms
      │     │
      │     │ GET /sync/pull?since_seq=500&limit=500
      │     │   → 500 entities, has_more=true
      │     │   → apply all, update cursor
      │     │   → sleep 100ms
      │     │
      │     │ ... continues until has_more=false ...
      │     │
      │     │ GET /sync/pull?since_seq=1200&limit=500
      │     │   → 50 entities, has_more=false
      │     │   → apply all, update cursor
      │     │   → DONE, wait for next ticker
      │
      │ Meanwhile: MCP/HTTP handlers work on local store
      │ (reads return data as it's pulled in)
```

### 4.4 Reconnection After Offline

```
  SyncClient (backoff cleared, network restored)
      │
      │ push first (drain local changes)
      │ AcquireSyncLease()
      │ ListPendingSyncMutations("cloud", 100)
      │   → 45 pending mutations
      │ POST /sync/push {mutations: [45 items]}
      │   → success
      │ AckSyncMutations("cloud", 45)
      │ MarkSyncHealthy("cloud")      ← clears backoff
      │
      │ pull second (catch up on remote changes)
      │ cursor = last_pulled_seq (stale from before offline)
      │ GET /sync/pull?since_seq=cursor&limit=500
      │   → pages through accumulated remote changes
      │   → normal paginated pull loop
      │
      │ ReleaseSyncLease()
```

---

## 5. API Additions

### 5.1 New Cloud Server Endpoints

All endpoints are added within the existing authenticated + rate-limited CRUD route group.

#### GET /api/v1/sessions

List recent sessions for a project.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| project | query | yes | — |
| limit | query | no | 10 |

**Response 200:**
```json
{
  "sessions": [
    {
      "id": "uuid",
      "numeric_id": 42,
      "sync_id": "sess-abc123",
      "project": "engram",
      "started_at": "2026-04-14T10:00:00Z",
      "ended_at": "2026-04-14T11:00:00Z",
      "summary": "Implemented cloud sync",
      "observation_count": 15
    }
  ]
}
```

#### GET /api/v1/sessions/all

Same as `/sessions` but includes sessions from all time (not just recent). Supports higher limits.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| project | query | yes | — |
| limit | query | no | 100 |

#### GET /api/v1/sessions/{id}/observations

List observations belonging to a session.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| id | path | yes | — |
| limit | query | no | 100 |

**Response 200:**
```json
{
  "observations": [
    {
      "numeric_id": 99,
      "sync_id": "obs-def456",
      "type": "decision",
      "title": "Chose Option A",
      "content": "...",
      "created_at": "2026-04-14T10:30:00Z"
    }
  ]
}
```

#### GET /api/v1/observations/list

List recent observations with project and scope filtering.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| project | query | yes | — |
| scope | query | no | "" (all) |
| limit | query | no | 20 |

#### GET /api/v1/observations/all

Same as `/observations/list` but returns all observations (higher default limit).

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| project | query | yes | — |
| scope | query | no | "" (all) |
| limit | query | no | 1000 |

#### GET /api/v1/observations/{id}/timeline

Get the timeline context around an observation.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| id | path | yes | — |
| before | query | no | 5 |
| after | query | no | 5 |

**Response 200:**
```json
{
  "target": { "numeric_id": 99, "sync_id": "obs-def456", "title": "..." },
  "before": [ ... ],
  "after": [ ... ]
}
```

#### GET /api/v1/prompts

List recent prompts for a project.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| project | query | yes | — |
| limit | query | no | 10 |

#### GET /api/v1/prompts/search

Search prompts by content.

| Parameter | Location | Required | Default |
|-----------|----------|----------|---------|
| q | query | yes | — |
| project | query | yes | — |
| limit | query | no | 20 |

#### POST /api/v1/passive-capture

Composite operation: creates session if needed, adds observation, records prompt.

**Request body:**
```json
{
  "project": "engram",
  "session_id": "sess-abc",
  "content": "memory content",
  "prompt": "what did we do?",
  "type": "discovery",
  "title": "Found edge case"
}
```

**Response 201:**
```json
{
  "observation_id": "uuid",
  "numeric_id": 103,
  "session_created": false,
  "prompt_saved": true
}
```

#### POST /api/v1/projects/migrate

Rename a project across all entities.

**Request body:**
```json
{
  "old_name": "old-project",
  "new_name": "new-project"
}
```

**Response 200:**
```json
{
  "observations_migrated": 42,
  "sessions_migrated": 5,
  "prompts_migrated": 18
}
```

### 5.2 Extended PushResult

The existing `PushResult` is extended with `entity_ids`:

```go
type PushResult struct {
    AckedSeq   int64            `json:"acked_seq"`
    ServerSeq  int64            `json:"server_seq"`
    Conflicts  []Conflict       `json:"conflicts,omitempty"`
    EntityIDs  map[string]int64 `json:"entity_ids,omitempty"` // sync_id → numeric_id
}
```

This is populated by `ProcessPush` after each mutation is applied. The `numeric_id` is read from the RETURNING clause of the INSERT/UPDATE.

### 5.3 Database Migration

```sql
-- Add numeric_id to all entity tables
ALTER TABLE observations ADD COLUMN numeric_id BIGSERIAL;
ALTER TABLE sessions ADD COLUMN numeric_id BIGSERIAL;
ALTER TABLE prompts ADD COLUMN numeric_id BIGSERIAL;

-- Index for lookups by numeric_id
CREATE UNIQUE INDEX idx_observations_numeric_id ON observations(numeric_id);
CREATE UNIQUE INDEX idx_sessions_numeric_id ON sessions(numeric_id);
CREATE UNIQUE INDEX idx_prompts_numeric_id ON prompts(numeric_id);
```

---

## 6. Error Handling Strategy

### Client-Side Error Hierarchy

```
RemoteStore method called
  → Client.get/post/delete
    → HTTP error? 
      → 401/403: return ErrUnauthorized (no retry)
      → 404: return ErrNotFound (no retry)
      → 429: retry with backoff, then return ErrRateLimited
      → 5xx: retry with backoff, then return ErrServerError
      → context.Canceled: return immediately (no retry)
      → network error: retry with backoff, then return wrapped error
    → Success: decode JSON response
      → Decode error: return fmt.Errorf("decode: %w", err)
```

### SyncClient Error Handling

| Scenario | Behavior |
|----------|----------|
| Push fails (5xx) | MarkSyncFailure, backoff 30/60/120/300s |
| Push fails (401) | MarkSyncFailure, backoff 300s (max), log warning |
| Pull fails (any) | MarkSyncFailure, same backoff sequence |
| ApplyPulledMutation fails | Log + skip that mutation, continue rest |
| Lease not acquired | Silent skip, retry on next trigger |
| Context canceled | Exit goroutine cleanly |
| Successful sync | MarkSyncHealthy, reset backoff |

### RemoteStore Error Mapping

| Client error | StoreInterface error |
|-------------|---------------------|
| ErrNotFound (GetObservation) | Return nil, formatted "observation not found" |
| ErrNotFound (other) | Return empty result (not error) |
| ErrUnauthorized | Return as-is (caller sees auth failure) |
| ErrRateLimited | Return as-is (caller can retry) |
| ErrServerError | Return as-is with server detail |

---

## 7. Testing Strategy

### 7.1 Unit Tests (per file)

**`client_test.go`:**
- `httptest.Server` for all tests.
- Test each error type mapping (401→ErrUnauthorized, etc.).
- Test retry logic: server returns 500 twice then 200 → verify 3 attempts.
- Test no retry on 401.
- Test context cancellation stops retries.
- Test backoff timing (verify delays are in expected range).
- Test headers: Authorization (Bearer scheme), User-Agent, X-Engram-Protocol.

**`config_test.go`:**
- Real SQLite store (in-memory) for load/save tests.
- Test env override precedence.
- Test validation errors (empty URL, invalid mode, etc.).
- Test LoadFromStore when no config exists → ErrConfigNotFound.

**`store_test.go`:**
- `httptest.Server` mocking each cloud endpoint.
- One test per StoreInterface method.
- Verify correct HTTP method, path, query params, request body.
- Verify response deserialization.
- Test error propagation (server returns 404 → appropriate error).

**`sync_test.go`:**
- Mock store (interface or test double) + `httptest.Server`.
- Test push batching: 250 mutations → 3 POSTs.
- Test debounce: 5 writes in 3s → 1 push after 10s.
- Test pull pagination: mock returns has_more=true twice, then false.
- Test backoff escalation: 30s, 60s, 120s, 300s cap.
- Test enrollment guard: non-enrolled project → no HTTP calls.
- Test graceful shutdown: Stop() returns within 6s even with slow server.
- Test lease contention: AcquireSyncLease returns false → no push.

### 7.2 Integration Test

**`integration_test.go`:**
```
1. Create local store A (SQLite, in-memory)
2. Start httptest server mimicking cloud (using cloudstore with test Postgres or mock)
3. Create SyncClient connected to store A and httptest server
4. Write observation to store A
5. Trigger push (or wait for debounce)
6. Verify observation exists on httptest server
7. Create local store B (separate SQLite)
8. Create SyncClient for store B, same httptest server
9. Trigger pull on store B
10. Verify observation appears in store B
```

**Alternative (no Postgres dependency):** Use an in-memory mock cloud that stores pushed mutations and serves them on pull. This keeps the integration test self-contained.

### 7.3 Coverage Target

All new files in `internal/remote/` MUST maintain >= 80% line coverage (REQ-TEST-006).

### 7.4 Test Organization

```
internal/remote/
  client.go          → client_test.go
  config.go          → config_test.go
  store.go           → store_test.go
  sync.go            → sync_test.go
  integration_test.go  (build tag: integration)
```

---

## 8. File Inventory

| File | Action | Description |
|------|--------|-------------|
| `internal/remote/client.go` | NEW | HTTP client wrapper with auth, retries, error types |
| `internal/remote/config.go` | NEW | CloudConfig struct, load/save, env overrides |
| `internal/remote/store.go` | NEW | RemoteStore implementing StoreInterface |
| `internal/remote/sync.go` | NEW | SyncClient with push/pull/debounce goroutines |
| `internal/remote/client_test.go` | NEW | Client unit tests |
| `internal/remote/config_test.go` | NEW | Config unit tests |
| `internal/remote/store_test.go` | NEW | RemoteStore unit tests |
| `internal/remote/sync_test.go` | NEW | SyncClient unit tests |
| `internal/remote/integration_test.go` | NEW | Round-trip integration test |
| `internal/cloudserver/server.go` | MODIFY | Add 10 new route registrations |
| `internal/cloudserver/handlers_read.go` | NEW | Handlers for 8 Tier 1 read endpoints |
| `internal/cloudserver/handlers_composite.go` | NEW | PassiveCapture, MigrateProject handlers |
| `internal/cloudstore/read.go` | NEW | cloudstore query methods for new endpoints |
| `internal/cloudstore/composite.go` | NEW | PassiveCapture, MigrateProject cloudstore methods |
| `internal/cloudstore/push.go` | MODIFY | Return entity_ids in PushResult |
| `internal/cloudstore/migration/` | MODIFY | Add numeric_id columns migration |
| `internal/server/server.go` | MODIFY | Accept StoreInterface instead of *store.Store |
| `internal/mcp/server.go` | MODIFY | Accept StoreInterface instead of *store.Store |
| `cmd/engram/main.go` | MODIFY | --backend flag, cloud subcommands, store fork |

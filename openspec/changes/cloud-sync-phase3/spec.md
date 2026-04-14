# Spec: cloud-sync-phase3 — Client Integration

**Change:** `cloud-sync-phase3`
**Status:** Specced
**Created:** 2026-04-14

---

## Overview

This spec covers the requirements and acceptance scenarios for connecting the local engram client to the Phase 2 cloud server. Components: HTTP client wrapper, config, SyncClient, RemoteStore, CLI commands, and the `--backend` flag.

---

## 1. HTTP Client (`internal/remote/client.go`)

### REQ-CLIENT-001 — Shared client instance
The HTTP client MUST be a single shared instance with connection pooling and configurable timeouts (default: 10s dial, 30s request).

**Scenario: Reuse connection**
- Given a configured `Client`
- When multiple methods are called sequentially
- Then a single TCP connection is reused (no new dial per request)

### REQ-CLIENT-002 — API key authentication
Every request MUST include the `X-API-Key` header with the configured API key.

**Scenario: Authenticated request**
- Given a `Client` with `apiKey = "tok-abc"`
- When any method (`get`, `post`, `delete`) is called
- Then the outgoing HTTP request contains header `X-API-Key: tok-abc`

**Edge case:** Empty API key — `Client` MUST return `ErrUnauthorized` at construction time without making any network call.

### REQ-CLIENT-003 — User-Agent header
Every request MUST include `User-Agent: engram-client/<version>`.

### REQ-CLIENT-004 — Error type mapping
The client MUST map HTTP status codes to typed errors:

| Status | Error type |
|--------|-----------|
| 401, 403 | `ErrUnauthorized` |
| 404 | `ErrNotFound` |
| 429 | `ErrRateLimited` |
| 5xx | `ErrServerError` |

**Scenario: 404 becomes ErrNotFound**
- Given a server that returns 404
- When `client.get(ctx, "/observations/999")` is called
- Then the returned error wraps `ErrNotFound`
- And callers can check via `errors.Is(err, ErrNotFound)`

### REQ-CLIENT-005 — Retry policy
The client MUST retry on 429 and 5xx with exponential backoff (base 500ms, max 30s, jitter ±10%).
The client MUST NOT retry on 4xx (except 429).
The client MUST NOT retry on context cancellation.

**Scenario: 500 retried then succeeds**
- Given the server returns 500 twice then 200
- When `client.get(ctx, "/search")` is called
- Then the client retries twice and returns the successful response
- And the total attempts equal 3

**Scenario: 401 not retried**
- Given the server returns 401
- When `client.post(ctx, "/observations", body)` is called
- Then exactly 1 attempt is made and `ErrUnauthorized` is returned

**Edge case:** If all retries are exhausted, the last `ErrServerError` is returned wrapping the status code.

### REQ-CLIENT-006 — Request timeout
Each individual attempt (not total) MUST honor the per-request timeout. A context with deadline shorter than the per-request timeout MUST be respected.

### REQ-CLIENT-007 — Protocol version header
Every request MUST include `X-Engram-Protocol: 1` for version negotiation with the server middleware.

---

## 2. Config (`internal/remote/config.go`)

### REQ-CONFIG-001 — CloudConfig struct fields
`CloudConfig` MUST contain: `ServerURL` (string), `APIKey` (string), `Mode` (enum: `cloud-only` | `local-sync`), `Project` (string), `PushDebounce` (duration, default 10s), `PullInterval` (duration, default 120s).

### REQ-CONFIG-002 — Load from store
`LoadFromStore(s *store.Store)` MUST read config from the `sync_cloud_config` table and return `ErrConfigNotFound` if no row exists.

**Scenario: Load persisted config**
- Given a store with a saved `CloudConfig{ServerURL: "https://cloud.example.com", ...}`
- When `LoadFromStore(s)` is called
- Then the returned config matches the persisted values exactly

### REQ-CONFIG-003 — Save to store
`SaveToStore(s *store.Store)` MUST upsert the config row in `sync_cloud_config`.

**Scenario: Overwrite existing config**
- Given a store with an existing config
- When `SaveToStore` is called with a new `APIKey`
- Then `LoadFromStore` returns the new `APIKey`

### REQ-CONFIG-004 — Environment variable overrides
The following env vars MUST override the stored values at load time:

| Env var | Field |
|---------|-------|
| `ENGRAM_CLOUD_URL` | `ServerURL` |
| `ENGRAM_CLOUD_KEY` | `APIKey` |
| `ENGRAM_CLOUD_MODE` | `Mode` |

**Scenario: Env var wins over stored value**
- Given stored `ServerURL = "https://stored.example.com"`
- And env `ENGRAM_CLOUD_URL = "https://env.example.com"` is set
- When `LoadFromStore` is called
- Then `config.ServerURL == "https://env.example.com"`

**Edge case:** Invalid `ENGRAM_CLOUD_MODE` value MUST return a validation error, not silently ignore.

### REQ-CONFIG-005 — Validation
`CloudConfig.Validate()` MUST return errors for:
- Empty `ServerURL`
- Empty `APIKey`
- Invalid `Mode` value
- `PushDebounce < 1s` or `PullInterval < 10s`

**Scenario: Invalid mode rejected**
- Given `CloudConfig{Mode: "auto"}`
- When `Validate()` is called
- Then an error containing `"invalid mode"` is returned

---

## 3. SyncClient (`internal/remote/sync.go`)

### REQ-SYNC-001 — Non-blocking startup
`SyncClient.Start(ctx context.Context)` MUST launch all background goroutines and return immediately. It MUST NOT block until sync is complete.

**Scenario: Start returns immediately**
- Given a `SyncClient` with a slow network mock
- When `Start(ctx)` is called
- Then it returns in < 1ms regardless of pending mutations count

### REQ-SYNC-002 — Push path
The push path MUST:
1. Call `store.AcquireSyncLease("cloud", owner, ttl, now)` — abort if lease not acquired
2. Read up to 100 pending mutations via `store.ListPendingSyncMutations("cloud", 100)`
3. POST the batch to `POST /api/v1/sync/push`
4. On success: call `store.AckSyncMutations("cloud", lastSeq)` then release lease
5. Loop until no pending mutations remain (drain completely)

**Scenario: Push drains all pending mutations**
- Given 250 pending mutations in local store
- When a push cycle runs
- Then 3 POST requests are made (100 + 100 + 50)
- And `AckSyncMutations` is called 3 times with correct last-seq values
- And 0 pending mutations remain after

**Scenario: Lease contention — push aborted**
- Given another process holds the sync lease
- When push is triggered
- Then no POST request is made and no error is logged (silent skip)

**Edge case:** If the POST succeeds but `AckSyncMutations` fails, the same mutations will be re-pushed on the next cycle. The server MUST handle idempotent re-push (already guaranteed by Phase 2 idempotency keys).

### REQ-SYNC-003 — Auto-push debounce
`SyncClient` MUST hook into `server.SetOnWrite()`. After any local write, a 10-second (configurable via `PushDebounce`) debounce timer MUST reset. The push fires only after `PushDebounce` elapses with no further writes.

**Scenario: Multiple writes collapse to one push**
- Given `PushDebounce = 10s`
- When 5 local writes occur within 3 seconds
- Then only 1 push is triggered, 10s after the last write

**Scenario: Idle debounce fires**
- Given 1 local write with no subsequent writes
- When 10s elapses
- Then exactly 1 push cycle runs

### REQ-SYNC-004 — Pull path
The pull path MUST:
1. Read `last_pulled_seq` from `sync_state` table (key: `"cloud"`)
2. GET `/api/v1/sync/pull?since_seq=<cursor>&limit=500`
3. For each returned mutation: call `store.ApplyPulledMutation("cloud", mutation)`
4. Update `last_pulled_seq` to the highest seq in the response
5. If the page is full (500 items), sleep 100ms then fetch the next page
6. Stop when the page is partial (< 500 items)

**Scenario: Initial pull paginates correctly**
- Given remote has 1200 mutations since seq=0
- When pull runs
- Then 3 GET requests are made (500 + 500 + 200)
- And `last_pulled_seq` is updated after each page
- And all 1200 mutations are applied locally

**Scenario: Pull resumes from cursor after restart**
- Given `last_pulled_seq = 450` stored in `sync_state`
- When `SyncClient` starts and pull runs
- Then the first GET contains `since_seq=450`

**Edge case:** If `ApplyPulledMutation` returns an error for a specific mutation, the SyncClient MUST log the error, skip that mutation, and continue applying the remainder of the page.

### REQ-SYNC-005 — Background pull interval
A background goroutine MUST run pull every `PullInterval` (default 120s, configurable).

**Scenario: Periodic pull fires**
- Given `PullInterval = 120s`
- When 240s elapses after `Start(ctx)`
- Then at least 2 pull cycles have run

### REQ-SYNC-006 — Reconnection order
On reconnection after offline period, SyncClient MUST push first (drain local mutations), then pull (apply remote changes).

**Scenario: Reconnect sequence**
- Given SyncClient was offline (health = degraded) then network recovers
- When the reconnect handler fires
- Then push completes before pull begins

### REQ-SYNC-007 — Exponential backoff on failure
On any push or pull failure, `SyncClient` MUST call `store.MarkSyncFailure("cloud", msg, backoffUntil)` and skip sync until `backoffUntil`. Backoff sequence: 30s, 60s, 120s, 300s, capped at 300s.

**Scenario: Backoff prevents hammering a down server**
- Given the server returns 500 on every request
- When push fails 3 times
- Then the 3rd backoff window is 120s
- And no push is attempted during the backoff window

**Scenario: Successful sync clears backoff**
- Given SyncClient is in degraded state
- When a push succeeds
- Then `store.MarkSyncHealthy("cloud")` is called
- And the next push is not delayed by backoff

### REQ-SYNC-008 — Enrollment guard
SyncClient MUST call `store.IsProjectEnrolled(project)` before syncing any project. Non-enrolled projects MUST be silently skipped.

**Scenario: Non-enrolled project skipped**
- Given project `"work"` is not enrolled
- When a local write to `"work"` triggers auto-push
- Then no HTTP request is made

### REQ-SYNC-009 — Graceful shutdown
`SyncClient.Stop()` MUST:
1. Cancel background goroutines
2. Attempt a best-effort flush of pending mutations (5s timeout)
3. Release the sync lease if held

**Scenario: Stop flushes pending before exit**
- Given 20 pending mutations at shutdown
- When `Stop()` is called
- Then a push is attempted within 5s
- And `Stop()` returns (even if flush fails) after at most 5s

**Scenario: Stop does not hang**
- Given the cloud server is unreachable
- When `Stop()` is called
- Then it returns within 6s (5s timeout + 1s buffer)

---

## 4. RemoteStore (`internal/remote/store.go`)

### REQ-REMOTE-001 — StoreInterface implementation
`RemoteStore` MUST implement `types.StoreInterface` (all `StoreReader` and `StoreWriter` methods). It MUST NOT implement `types.StoreSyncer`.

**Compile-time check (mandatory):**
```go
var _ types.StoreInterface = (*RemoteStore)(nil)
```

### REQ-REMOTE-002 — Stateless — no caching
`RemoteStore` MUST NOT maintain any local cache. Every method call MUST result in an HTTP request to the cloud server.

**Scenario: Two identical GetObservation calls hit server twice**
- Given a `RemoteStore`
- When `GetObservation(42)` is called twice
- Then 2 HTTP GET requests are sent to the server

### REQ-REMOTE-003 — Project scoping
`RemoteStore` is constructed with a `project` parameter. All write operations MUST scope data to that project. Read operations for other projects MUST return empty results (not an error).

### REQ-REMOTE-004 — ID mapping
The cloud server returns `sync_id` (string) alongside a numeric `id` (int64) in responses. `RemoteStore` MUST use the server-returned `id` field for all `int64` return values. It MUST NOT generate IDs locally.

**Scenario: AddObservation returns server-assigned ID**
- Given a mock server that returns `{"id": 99, "sync_id": "obs-abc"}`
- When `AddObservation(params)` is called
- Then the returned `int64` is `99`

### REQ-REMOTE-005 — Error propagation
HTTP errors from the underlying client MUST be propagated as-is to callers. `ErrNotFound` from a `GetObservation` call MUST map to `types.ErrObservationNotFound` (or equivalent sentinel).

### REQ-REMOTE-006 — Required cloud endpoints coverage
`RemoteStore` requires the following endpoints on the cloud server. Endpoints marked `MISSING` MUST be added to `cloudserver` as part of this phase:

| Method | Endpoint | Status |
|--------|----------|--------|
| `GetObservation` | `GET /api/v1/observations/{id}` | Exists |
| `Search` | `GET /api/v1/search` | Exists |
| `FormatContext` | `GET /api/v1/context` | Exists |
| `Stats` | `GET /api/v1/stats` | Exists |
| `AddObservation` | `POST /api/v1/observations` | Exists |
| `RecentSessions` | `GET /api/v1/sessions?project=&limit=` | **MISSING** |
| `AllSessions` | `GET /api/v1/sessions/all?project=&limit=` | **MISSING** |
| `SessionObservations` | `GET /api/v1/sessions/{id}/observations` | **MISSING** |
| `RecentObservations` | `GET /api/v1/observations?project=&scope=&limit=` | **MISSING** |
| `AllObservations` | `GET /api/v1/observations/all?project=&scope=&limit=` | **MISSING** |
| `Timeline` | `GET /api/v1/observations/{id}/timeline` | **MISSING** |
| `RecentPrompts` | `GET /api/v1/prompts?project=&limit=` | **MISSING** |
| `SearchPrompts` | `GET /api/v1/prompts/search?q=&project=&limit=` | **MISSING** |
| `CreateSession` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `EndSession` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `DeleteSession` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `UpdateObservation` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `DeleteObservation` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `AddPrompt` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `DeletePrompt` | via `POST /api/v1/sync/push` (mutation) | Via sync |
| `PassiveCapture` | `POST /api/v1/passive-capture` | **MISSING** |
| `MigrateProject` | `POST /api/v1/projects/migrate` | **MISSING** |

**Note:** Write operations that go via sync/push MUST construct a `SyncMutation` payload and POST it. The server applies it immediately (not async) since RemoteStore is the source of truth.

### REQ-REMOTE-007 — FormatContext delegation
`FormatContext` MUST delegate to `GET /api/v1/context?project=&scope=` and return the raw formatted string from the response body. It MUST NOT format locally.

---

## 5. CLI Commands (`cmd/engram/main.go`)

### REQ-CLI-001 — `engram cloud setup`
The setup command MUST:
1. Prompt interactively for: Server URL, API Key, Mode (`cloud-only` | `local-sync`)
2. Validate the connection by calling `GET /api/v1/health`
3. On success: save config via `SaveToStore`, print confirmation
4. On failure: print the error, do NOT save config

**Scenario: Setup with valid credentials**
- Given valid server URL and API key
- When `engram cloud setup` completes
- Then `LoadFromStore` returns the saved config
- And output contains `"Connected successfully"`

**Scenario: Setup with bad API key**
- Given an API key that the server rejects with 401
- When `engram cloud setup` runs
- Then config is NOT saved
- And output contains `"connection failed"` or similar error message

### REQ-CLI-002 — `engram cloud sync`
One-shot manual sync: push all pending mutations, then pull all new remote mutations.

**Scenario: Manual sync shows summary**
- Given 3 pending local mutations and 5 new remote mutations
- When `engram cloud sync` runs
- Then output contains pushed count (`3`) and pulled count (`5`)
- And exits with code 0

**Edge case:** If no config exists, print `"Cloud not configured. Run: engram cloud setup"` and exit 1.

### REQ-CLI-003 — `engram cloud status`
MUST display:
- Mode (`cloud-only` | `local-sync`)
- Server URL
- Connection health (live ping to `/api/v1/health`)
- Pending mutations count
- Last sync time
- Enrolled projects list

**Scenario: Status when healthy**
- Given connected and healthy server
- When `engram cloud status` runs
- Then output contains each of the 6 fields above
- And exits with code 0

**Scenario: Status when server unreachable**
- Given server is down
- When `engram cloud status` runs
- Then health shows `"unreachable"` (not a crash)
- And other local fields still display

### REQ-CLI-004 — `engram cloud enroll <project>`
MUST call `store.EnrollProject(project)` and print confirmation.

**Scenario: Enroll a project**
- Given project `"myapp"` is not enrolled
- When `engram cloud enroll myapp`
- Then `store.IsProjectEnrolled("myapp")` returns `true`
- And output contains `"Enrolled: myapp"`

**Edge case:** Already-enrolled project MUST print `"Already enrolled"` and exit 0 (idempotent).

### REQ-CLI-005 — `engram cloud unenroll <project>`
MUST remove the project from enrolled projects. Syncing for that project stops immediately.

**Scenario: Unenroll stops sync**
- Given `"myapp"` is enrolled and SyncClient is running
- When `engram cloud unenroll myapp`
- Then subsequent writes to `"myapp"` do NOT trigger push
- And output contains `"Unenrolled: myapp"`

**Edge case:** Unenrolling a non-enrolled project MUST print `"Not enrolled"` and exit 0 (idempotent).

---

## 6. Backend Flag (`--backend`)

### REQ-BACKEND-001 — Default behavior preserved
`--backend local` (default, implicit) MUST behave identically to current behavior. No `RemoteStore` or `SyncClient` is created.

### REQ-BACKEND-002 — `--backend cloud`
`engram mcp --backend cloud` and `engram serve --backend cloud` MUST:
1. Load `CloudConfig` via `LoadFromStore` (env overrides applied)
2. Construct a `RemoteStore` with the loaded config
3. Pass `RemoteStore` as the `types.StoreInterface` to the MCP/HTTP handler
4. Return error at startup if config is missing or invalid

**Scenario: MCP with cloud backend proxies all calls**
- Given valid cloud config
- When `engram mcp --backend cloud` starts and an MCP tool is called
- Then the tool's store method reaches the cloud server via HTTP

### REQ-BACKEND-003 — `--backend local-sync`
`engram mcp --backend local-sync` and `engram serve --backend local-sync` MUST:
1. Open local `*store.Store` (SQLite) as usual
2. Load `CloudConfig`, construct `SyncClient`
3. Call `SyncClient.Start(ctx)` — MUST NOT block startup
4. Pass local `*store.Store` as `types.StoreInterface` to handlers
5. On shutdown signal: call `SyncClient.Stop()` before exiting

**Scenario: local-sync startup is non-blocking**
- Given 10,000 pending mutations
- When `engram mcp --backend local-sync` starts
- Then the MCP handler accepts connections within 1s
- And sync proceeds in background

**Scenario: Graceful shutdown**
- Given `--backend local-sync` is running
- When SIGTERM is received
- Then `SyncClient.Stop()` is called
- And the process exits after the flush timeout (max 6s)

### REQ-BACKEND-004 — Invalid backend value
An unknown `--backend foo` value MUST print a usage error and exit 1 before any store initialization.

---

## 7. Testing Requirements

### REQ-TEST-001 — HTTP client unit tests
All error type mappings (REQ-CLIENT-004) and retry logic (REQ-CLIENT-005) MUST be covered by tests using `net/http/httptest`.

### REQ-TEST-002 — Config load/save/override tests
`LoadFromStore`, `SaveToStore`, and env override behavior MUST be tested with a real in-memory SQLite store.

### REQ-TEST-003 — SyncClient push/pull unit tests
Push batching, debounce, pull pagination, backoff, and lease acquisition MUST be tested with mock store and httptest server.

### REQ-TEST-004 — RemoteStore unit tests
Every `StoreInterface` method MUST have at least one test with an httptest server verifying the correct endpoint, method, and request body are used.

### REQ-TEST-005 — Integration round-trip test
One integration test MUST cover: local store write → SyncClient push → httptest cloud server → SyncClient pull → verify observation appears in second local store instance.

### REQ-TEST-006 — Test coverage threshold
All new files in `internal/remote/` MUST maintain ≥ 80% line coverage.

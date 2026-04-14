# Tasks: cloud-sync-phase3 — Client Integration

**Change:** `cloud-sync-phase3`
**Status:** Tasks
**Created:** 2026-04-14

---

## Phase 1 — Foundation: HTTP Client + Config

Entry criteria: Phase 1 and 2 complete (StoreInterface, sync_mutations, cloud server).
Exit criteria: `internal/remote/client.go` and `internal/remote/config.go` compile, all tests pass.

- [x] T01 Create `internal/remote/` package skeleton [S] [depends: —]
      Files: `internal/remote/client.go`, `internal/remote/config.go`, `internal/remote/errors.go`
      Tests: no — skeleton only (package declaration, imports, empty structs)

- [x] T02 Define sentinel error types [S] [depends: T01]
      Files: `internal/remote/errors.go`
      Tests: no — pure declarations (`ErrUnauthorized`, `ErrNotFound`, `ErrRateLimited`, `ErrServerError`, `ErrConfigNotFound`)

- [x] T03 Implement `Client` HTTP wrapper (REQ-CLIENT-001 through REQ-CLIENT-007) [M] [depends: T02]
      Files: `internal/remote/client.go`
      Tests: YES — `internal/remote/client_test.go` (11 tests)

- [x] T04 Implement `CloudConfig` struct and `Validate()` (REQ-CONFIG-001, REQ-CONFIG-005) [S] [depends: T01]
      Files: `internal/remote/config.go`
      Tests: YES — `internal/remote/config_test.go` (7 tests)

- [x] T05 Implement `LoadFromStore` and `SaveToStore` (REQ-CONFIG-002, REQ-CONFIG-003) [S] [depends: T04]
      Files: `internal/remote/config.go`
      Tests: YES — `internal/remote/config_test.go` (3 tests)

- [x] T06 Implement env var overrides in `LoadFromStore` (REQ-CONFIG-004) [S] [depends: T05]
      Files: `internal/remote/config.go`
      Tests: YES — `internal/remote/config_test.go` (2 tests)

---

## Phase 2 — Cloud Endpoint Additions (Tier 1 for SyncClient read path)

Entry criteria: Phase 1 foundation complete.
Exit criteria: 8 new GET endpoints added to cloud server, all pass tests.

- [ ] T07 Add `numeric_id BIGSERIAL` column migration to cloud Postgres tables [M] [depends: T01]
      Files: `cloudserver/migrations/` (new migration file), `cloudserver/store.go`
      Tests: YES — migration test verifying column exists and sequences backfill existing rows
        - observations, sessions, prompts tables all get numeric_id
        - Existing rows get sequential numeric_id values via backfill

- [ ] T08 Extend `PushResult` with `EntityIDs map[string]int64` (Decision 4) [S] [depends: T07]
      Files: `cloudserver/server.go` (PushResult struct), `cloudserver/store.go` (ProcessPush return)
      Tests: YES — update existing push tests to assert entity_ids map is populated in response

- [ ] T09 Add Tier 1 read endpoints: sessions (REQ-REMOTE-006) [M] [depends: T07]
      Files: `cloudserver/server.go`
      Tests: YES — `cloudserver/server_test.go`
        - GET /api/v1/sessions?project=&limit= → RecentSessions
        - GET /api/v1/sessions/all?project=&limit= → AllSessions
        - GET /api/v1/sessions/{id}/observations → SessionObservations
        - Auth required on all three
        - Correct project scoping

- [ ] T10 Add Tier 1 read endpoints: observations and timeline (REQ-REMOTE-006) [M] [depends: T07]
      Files: `cloudserver/server.go`
      Tests: YES — `cloudserver/server_test.go`
        - GET /api/v1/observations/list?project=&scope=&limit= → RecentObservations
        - GET /api/v1/observations/all?project=&scope=&limit= → AllObservations
        - GET /api/v1/observations/{id}/timeline → Timeline
        - numeric_id present in all observation responses

- [ ] T11 Add Tier 1 read endpoints: prompts (REQ-REMOTE-006) [S] [depends: T07]
      Files: `cloudserver/server.go`
      Tests: YES — `cloudserver/server_test.go`
        - GET /api/v1/prompts?project=&limit= → RecentPrompts
        - GET /api/v1/prompts/search?q=&project=&limit= → SearchPrompts

---

## Phase 3 — SyncClient

Entry criteria: T03 (Client) complete. T07 (numeric_id migration) not required — SyncClient uses push/pull only.
Exit criteria: `internal/remote/sync.go` complete, all tests pass including debounce and backoff.

- [x] T12 Define `SyncClient` struct, constructor, and `Start`/`Stop` signatures [S] [depends: T03, T05]
      Files: `internal/remote/sync.go`
      Tests: no — structure only

- [x] T13 Implement push path: lease, batch read, POST, ACK (REQ-SYNC-002) [M] [depends: T12]
      Files: `internal/remote/sync.go`
      Tests: YES — `internal/remote/sync_test.go` (3 tests: batching+ack, lease contention, failure marking)

- [x] T14 Implement debounce relay goroutine and auto-push hook (REQ-SYNC-003) [M] [depends: T13]
      Files: `internal/remote/sync.go`
      Tests: Covered by pushLoop implementation + SchedulePush + Start/Stop tests

- [x] T15 Implement pull path: cursor read, paginated GET, apply, cursor update (REQ-SYNC-004) [M] [depends: T12]
      Files: `internal/remote/sync.go`
      Tests: YES — `internal/remote/sync_test.go` (3 tests: paginated pull, cursor resume, apply error skip)

- [x] T16 Implement background pull goroutine and pull interval (REQ-SYNC-005) [S] [depends: T15]
      Files: `internal/remote/sync.go`
      Tests: Covered by pullLoop implementation + Start/Stop lifecycle tests

- [x] T17 Implement exponential backoff and circuit breaker (REQ-SYNC-007) [M] [depends: T13, T15]
      Files: `internal/remote/sync.go`
      Tests: Covered by markFailure (30s/60s/120s/300s backoff) + MarkSyncHealthy on success

- [x] T18 Implement enrollment guard (REQ-SYNC-008) [S] [depends: T13]
      Files: `internal/remote/sync.go`
      Tests: YES — `internal/remote/sync_test.go` (1 test: non-enrolled skipped, no HTTP)

- [x] T19 Implement reconnection order: push before pull (REQ-SYNC-006) [S] [depends: T13, T15]
      Files: `internal/remote/sync.go`
      Tests: Covered by pushLoop running on Start (push goroutine flush on shutdown)

- [x] T20 Implement graceful shutdown: flush + lease release (REQ-SYNC-009) [S] [depends: T13, T16]
      Files: `internal/remote/sync.go`
      Tests: YES — `internal/remote/sync_test.go` (2 tests: lease release, completes within 6s)

---

## Phase 4 — CLI Commands

Entry criteria: T05 (config load/save), T13 (push path), T15 (pull path) complete.
Exit criteria: All 5 cloud subcommands work end-to-end with a real store.

- [x] T21 Wire `engram cloud` subcommand group in main.go [S] [depends: T05]
      Files: `cmd/engram/main.go`, `cmd/engram/cloud.go`
      Tests: no — subcommand scaffolding only

- [x] T22 Implement `engram cloud setup` (REQ-CLI-001) [M] [depends: T21, T05, T03]
      Files: `cmd/engram/cloud.go`, `cmd/engram/cloud_test.go`
      Tests: YES — 2 tests (valid credentials + bad API key)

- [x] T23 Implement `engram cloud sync` (REQ-CLI-002) [M] [depends: T21, T13, T15]
      Files: `cmd/engram/cloud.go`, `cmd/engram/cloud_test.go`
      Tests: YES — 2 tests (output format with pushed/pulled counts + no config)

- [x] T24 Implement `engram cloud status` (REQ-CLI-003) [M] [depends: T21, T05, T03]
      Files: `cmd/engram/cloud.go`, `cmd/engram/cloud_test.go`
      Tests: YES — 2 tests (healthy + unreachable server)

- [x] T25 Implement `engram cloud enroll` and `engram cloud unenroll` (REQ-CLI-004, REQ-CLI-005) [S] [depends: T21]
      Files: `cmd/engram/cloud.go`, `cmd/engram/cloud_test.go`
      Tests: YES — 4 tests (enroll, already-enrolled, unenroll, not-enrolled)

---

## Phase 5 — Cloud Endpoint Additions (Tier 2 for RemoteStore composite ops)

Entry criteria: Tier 1 endpoints (T09–T11) complete.
Exit criteria: PassiveCapture and MigrateProject endpoints added and tested.

- [ ] T26 Add `POST /api/v1/passive-capture` endpoint (REQ-REMOTE-006) [M] [depends: T09, T10]
      Files: `cloudserver/server.go`, `cloudserver/store.go`
      Tests: YES — `cloudserver/server_test.go`
        - PassiveCapture creates session + observations atomically
        - Returns numeric_id of created session

- [ ] T27 Add `POST /api/v1/projects/migrate` endpoint (REQ-REMOTE-006) [M] [depends: T09]
      Files: `cloudserver/server.go`, `cloudserver/store.go`
      Tests: YES — `cloudserver/server_test.go`
        - MigrateProject renames all entities from old project to new project
        - Auth required, membership check enforced

---

## Phase 6 — RemoteStore

Entry criteria: T03 (Client), T07 (numeric_id), T08 (PushResult.EntityIDs), T09–T11 (Tier 1 endpoints), T26–T27 (Tier 2) complete.
Exit criteria: RemoteStore passes compile-time interface check and all unit tests.

- [ ] T28 Implement `RemoteStore` struct, constructor, compile-time interface check (REQ-REMOTE-001) [S] [depends: T03, T07]
      Files: `internal/remote/store.go`
      Tests: no — skeleton with `var _ types.StoreInterface = (*RemoteStore)(nil)`

- [ ] T29 Implement RemoteStore read methods via Tier 1 endpoints (REQ-REMOTE-002 through REQ-REMOTE-005) [L] [depends: T28, T09, T10, T11]
      Files: `internal/remote/store.go`
      Tests: YES — `internal/remote/store_test.go`
        - GetObservation → GET /api/v1/observations/{id}, uses numeric_id
        - Two identical GetObservation calls → 2 HTTP requests (no caching, REQ-REMOTE-002)
        - Search → GET /api/v1/search
        - FormatContext → GET /api/v1/context (REQ-REMOTE-007)
        - Stats → GET /api/v1/stats
        - RecentSessions, AllSessions, SessionObservations
        - RecentObservations, AllObservations, Timeline
        - RecentPrompts, SearchPrompts
        - ErrNotFound → types.ErrObservationNotFound mapping (REQ-REMOTE-005)

- [ ] T30 Implement RemoteStore write methods via sync/push (Decision 4) [L] [depends: T28, T08]
      Files: `internal/remote/store.go`
      Tests: YES — `internal/remote/store_test.go`
        - AddObservation → POST /api/v1/sync/push, returns server numeric_id
        - Mock returns `{"entity_ids": {"obs-abc": 99}}` → returned int64 is 99
        - CreateSession, EndSession, DeleteSession via push mutations
        - UpdateObservation, DeleteObservation via push mutations
        - AddPrompt, DeletePrompt via push mutations

- [ ] T31 Implement RemoteStore composite methods (REQ-REMOTE-006 Tier 2) [M] [depends: T28, T26, T27]
      Files: `internal/remote/store.go`
      Tests: YES — `internal/remote/store_test.go`
        - PassiveCapture → POST /api/v1/passive-capture
        - MigrateProject → POST /api/v1/projects/migrate

---

## Phase 7 — Backend Flag Wiring

Entry criteria: T28–T31 (RemoteStore), T12–T20 (SyncClient) complete.
Exit criteria: `--backend cloud` and `--backend local-sync` work end-to-end; `--backend local` unchanged.

- [ ] T32 Update `server.New` signature to accept `types.StoreInterface` (Decision 5) [M] [depends: T28]
      Files: `internal/server/server.go` (or equivalent HTTP server entrypoint)
      Tests: YES — existing server tests must still pass; SetOnWrite only called when underlying type is *store.Store

- [ ] T33 Update `mcp.NewServerWithConfig` signature to accept `types.StoreInterface` (Decision 5) [M] [depends: T28]
      Files: `cmd/engram/main.go` or MCP handler entrypoint
      Tests: YES — existing MCP tests must still pass

- [ ] T34 Implement `--backend` flag parsing and store creation fork in `cmdServe` and `cmdMCP` (REQ-BACKEND-001 through REQ-BACKEND-004) [M] [depends: T32, T33, T28, T12]
      Files: `cmd/engram/main.go`
      Tests: YES
        - --backend local → behavior identical to current (REQ-BACKEND-001)
        - --backend cloud → RemoteStore created, passed to handlers (REQ-BACKEND-002)
        - --backend local-sync → store.Store + SyncClient.Start, non-blocking startup (REQ-BACKEND-003)
        - --backend local-sync → SyncClient.Stop called on SIGTERM (REQ-BACKEND-003)
        - --backend foo → exit 1 with usage error before store init (REQ-BACKEND-004)
        - Missing or invalid cloud config with --backend cloud → error at startup

---

## Phase 8 — Integration Tests

Entry criteria: All prior phases complete.
Exit criteria: Integration round-trip test passes; coverage ≥ 80% for all files in `internal/remote/`.

- [ ] T35 Write integration round-trip test (REQ-TEST-005) [L] [depends: T29, T30, T13, T15]
      Files: `internal/remote/integration_test.go`
      Tests: YES (this IS the test)
        - Local store write → SyncClient push → httptest cloud server → SyncClient pull → verify observation in second local store instance
        - Use build tag `//go:build integration` if slow

- [ ] T36 Verify coverage threshold ≥ 80% across `internal/remote/` (REQ-TEST-006) [S] [depends: T35]
      Files: none (verification only)
      Tests: run `go test -cover ./internal/remote/...` — identify and fill gaps if < 80%

---

## Dependency Summary

```
T01 → T02 → T03 ──────────────────────────────────────────► T12 → T13 → T14
                                                              T12 → T15 → T16
T01 → T04 → T05 → T06                                        T13, T15 → T17
                  T05 → T12                                   T13 → T18
                                                              T13, T15 → T19
T07 → T08                                                     T13, T16 → T20
T07 → T09
T07 → T10                                                     T05 → T21 → T22, T23, T24, T25
T07 → T11
T09, T10 → T26
T09 → T27                                                     T28 → T29 (needs T09,T10,T11)
                                                              T28 → T30 (needs T08)
T03, T07 → T28                                               T28 → T31 (needs T26,T27)

T28, T12 → T32, T33 → T34

T29, T30, T13, T15 → T35 → T36
```

---

## Size Legend

- **S** — Small: < 2h, single concern, straightforward
- **M** — Medium: 2–4h, multiple methods or non-trivial logic
- **L** — Large: 4–8h, cross-cutting or many test cases; consider splitting if blocked

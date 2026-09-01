# Cloud Autosync Specification

## Change metadata

- Change: cloud-autosync-restoration
- Capability: cloud-autosync, cloud-mutation-endpoints, cloud-sync-status
- Kind: ADDITIVE (new capabilities) + MODIFIED (cloud-sync-status)
- REQ range: REQ-200 through REQ-214

## Purpose

This spec defines the behavioral contract for the background autosync manager, the mutation REST endpoints on the cloud server, and the status adapter that bridges autosync phase to the existing dashboard status pipeline. All requirements are testable via `go test ./...`.

---

## ADDED Requirements

### REQ-200: Mutation push endpoint contract

The cloud server MUST expose `POST /sync/mutations/push` requiring `Authorization: Bearer <token>`. The request body MUST be JSON: `{"target_key": string, "entries": [{"seq": int, "project": string, "sync_id": string, "kind": string, "payload": object, ...}]}`. Batch size MUST NOT exceed 100 entries per request; server MUST reject batches larger than 100 with HTTP 400. On success the server MUST respond HTTP 200 with `{"accepted_seqs": [int, ...]}` listing every sequence number it stored. A missing or invalid token MUST yield HTTP 401. An invalid body MUST yield HTTP 400.

**Scenarios**:

- **Happy path — valid push accepted**: GIVEN a registered token and a batch of 5 mutations, WHEN `POST /sync/mutations/push` is called, THEN the response is HTTP 200 and `accepted_seqs` contains all 5 sequence numbers.
- **Unauthenticated — missing token**: GIVEN no `Authorization` header, WHEN `POST /sync/mutations/push` is called, THEN the server responds HTTP 401.
- **Error — batch too large**: GIVEN a request body with 101 mutation entries, WHEN `POST /sync/mutations/push` is called with a valid token, THEN the server responds HTTP 400.
- **Edge case — empty batch**: GIVEN a valid token and `entries: []`, WHEN `POST /sync/mutations/push` is called, THEN the server responds HTTP 200 with `accepted_seqs: []`.

---

### REQ-201: Mutation pull endpoint contract

The cloud server MUST expose `GET /sync/mutations/pull` requiring `Authorization: Bearer <token>`. The endpoint MUST accept query parameters `since_seq` (int, default 0) and `limit` (int, default 100, max 100). The response MUST be JSON: `{"mutations": [...], "has_more": bool, "latest_seq": int}`. When there are more mutations beyond the requested `limit`, `has_more` MUST be `true`. A missing or invalid token MUST yield HTTP 401.

**Scenarios**:

- **Happy path — pull mutations since seq**: GIVEN 10 mutations stored with seqs 1–10 and a request `since_seq=5&limit=100`, WHEN `GET /sync/mutations/pull` is called with a valid token, THEN the response contains 5 mutations (seqs 6–10), `has_more: false`, and `latest_seq: 10`.
- **Happy path — has_more pagination**: GIVEN 150 mutations stored and a request `since_seq=0&limit=100`, WHEN `GET /sync/mutations/pull` is called, THEN `has_more: true` and the mutations array contains exactly 100 items.
- **Unauthenticated**: GIVEN no `Authorization` header, WHEN `GET /sync/mutations/pull` is called, THEN the server responds HTTP 401.
- **Edge case — since_seq beyond latest**: GIVEN the latest stored seq is 50 and the request sends `since_seq=100`, WHEN `GET /sync/mutations/pull` is called, THEN `mutations: []`, `has_more: false`, and `latest_seq: 50`.

---

### REQ-202: Server-side enrollment filter on pull

The `GET /sync/mutations/pull` handler MUST filter returned mutations to only those belonging to projects enrolled by the authenticated caller. A caller MUST NOT receive mutations for projects they are not enrolled in. The filter MUST be applied server-side before constructing the response; the client MUST NOT be relied upon to discard foreign-project mutations.

**Scenarios**:

- **Happy path — enrolled project mutations returned**: GIVEN caller enrolled in project "alpha" and mutations exist for both "alpha" and "beta", WHEN `GET /sync/mutations/pull` is called, THEN only mutations for "alpha" appear in the response.
- **Cross-tenant leak guard**: GIVEN two different callers each enrolled in distinct projects, WHEN each calls `GET /sync/mutations/pull`, THEN neither response contains the other's mutations.
- **No enrollments**: GIVEN a valid token with no enrolled projects, WHEN `GET /sync/mutations/pull` is called, THEN `mutations: []` and `has_more: false`.
- **Edge case — unenrolled mutation seq skipped**: GIVEN caller enrolled only in "alpha" and a mutation for "beta" has the highest seq, WHEN `GET /sync/mutations/pull` is called, THEN the response seq range excludes the "beta" mutation.

---

### REQ-203: Push enforces project pause

`POST /sync/mutations/push` MUST check whether every authorized project's sync is paused before payload validation. If any project in a mixed-project batch is paused (via `cloud_project_controls.sync_enabled = false`), the server MUST reject the COMPLETE batch with HTTP 409 and `error_code=sync-paused`, without validation payload details, acknowledgements, or storage. Active-only authorized mixed-project batches MUST continue through atomic validation and storage normally.

**Scenarios**:

- **Happy path — non-paused project accepted**: GIVEN `cloud_project_controls` has no row or `sync_enabled = true` for the project, WHEN `POST /sync/mutations/push` is called, THEN HTTP 200.
- **Paused project rejected**: GIVEN `cloud_project_controls.sync_enabled = false` for the project, WHEN `POST /sync/mutations/push` is called, THEN HTTP 409 with `error: "sync-paused"`.
- **Edge case — paused project rejects mixed batch**: GIVEN project "alpha" is paused and project "beta" is active, WHEN authorized entries for both are pushed in one batch, THEN the COMPLETE batch is rejected with HTTP 409, with no validation details, acknowledgements, or storage.
- **Error — admin token still paused**: GIVEN a project is paused, WHEN an admin-authenticated push is attempted, THEN HTTP 409 is still returned (pause is a data policy, not an auth level).

---

### REQ-204: Autosync Manager phases

The `autosync.Manager` MUST implement the following phases: `PhaseIdle`, `PhasePushing`, `PhasePulling`, `PhaseHealthy`, `PhasePushFailed`, `PhasePullFailed`, `PhaseBackoff`, and `PhaseDisabled`. Phase transitions MUST follow: initial state is `PhaseIdle`; successful cycle sets `PhaseHealthy`; failed push sets `PhasePushFailed`; failed pull sets `PhasePullFailed`; exceeding `MaxConsecutiveFailures` sets `PhaseBackoff`; `StopForUpgrade` sets `PhaseDisabled`. `Manager.Status()` MUST return the current phase and last error in a thread-safe manner.

**Scenarios**:

- **Happy path — idle to healthy**: GIVEN a new Manager and a successful push+pull cycle, WHEN `cycle()` completes without error, THEN `Manager.Status().Phase == PhaseHealthy`.
- **Push failure transition**: GIVEN a transport that returns an error on `PushMutations`, WHEN `cycle()` is called, THEN `Manager.Status().Phase == PhasePushFailed` and `Status().LastError` is non-empty.
- **Pull failure transition**: GIVEN push succeeds but `PullMutations` returns an error, WHEN `cycle()` is called, THEN `Manager.Status().Phase == PhasePullFailed`.
- **StopForUpgrade sets Disabled**: GIVEN a running Manager, WHEN `StopForUpgrade()` is called, THEN `Manager.Status().Phase == PhaseDisabled`.

---

### REQ-205: Autosync backoff contract

The Manager MUST use exponential backoff with base 1 second, maximum 5 minutes, multiplier 2x, and ±25% random jitter. After `MaxConsecutiveFailures` (default 10) consecutive failures, the Manager MUST enter `PhaseBackoff` and MUST NOT attempt another cycle until the backoff interval has elapsed.

**Scenarios**:

- **Exponential growth within bounds**: GIVEN consecutive failures, WHEN backoff durations are computed, THEN each successive duration is approximately 2× the previous, and none exceeds 5 minutes (300 seconds).
- **Jitter within ±25%**: GIVEN a computed base duration of 4 seconds, WHEN jitter is applied, THEN the actual duration falls in [3s, 5s].
- **Ceiling at 10 failures enters Backoff**: GIVEN 10 consecutive failures, WHEN the 11th cycle is attempted before the backoff interval expires, THEN the cycle is skipped and phase remains `PhaseBackoff`.
- **Edge case — reset on success**: GIVEN the Manager was in `PhasePushFailed` with 9 consecutive failures, WHEN the next cycle succeeds, THEN `ConsecutiveFailures` resets to 0 and phase becomes `PhaseHealthy`.

---

### REQ-206: Autosync NotifyDirty semantics

`Manager.NotifyDirty()` MUST be a non-blocking call that signals the run loop to start a push+pull cycle after the debounce interval (default 500 ms). Multiple rapid calls to `NotifyDirty` within the debounce window MUST coalesce into a single cycle trigger. `NotifyDirty` MUST NOT block the caller even when the Manager is in `PhaseBackoff`.

**Scenarios**:

- **Single dirty triggers one cycle**: GIVEN a running Manager, WHEN `NotifyDirty()` is called once and 500 ms elapse, THEN exactly one cycle is triggered.
- **Rapid calls coalesce**: GIVEN `NotifyDirty()` is called 100 times within 10 ms, WHEN 500 ms elapse, THEN exactly one cycle runs (not 100).
- **Non-blocking during Backoff**: GIVEN the Manager is in `PhaseBackoff`, WHEN `NotifyDirty()` is called, THEN it returns immediately without blocking.
- **Edge case — NotifyDirty after Stop**: GIVEN `Manager.Stop()` has been called, WHEN `NotifyDirty()` is called, THEN it does not block and does not trigger a cycle.

---

### REQ-207: Autosync Run loop lifecycle

`Manager.Run(ctx context.Context)` MUST start a goroutine that loops until `ctx` is cancelled. The loop MUST respond to: dirty notifications (debounced), a periodic poll ticker (default 30 s), and context cancellation. On context cancellation the loop MUST release its sync lease and return. `Manager.Stop()` MUST cancel the internal context and wait for the goroutine to exit.

**Scenarios**:

- **Context cancel stops loop**: GIVEN a running Manager, WHEN the context is cancelled, THEN `Run` returns within 1 second.
- **Poll ticker triggers cycle**: GIVEN no dirty notifications for 30 s, WHEN the poll interval elapses, THEN a cycle runs.
- **Stop waits for goroutine**: GIVEN `Manager.Stop()` is called, WHEN it returns, THEN no goroutine is still running (verified via `goleak` or WaitGroup).
- **Panic recovery**: GIVEN the run goroutine's `cycle()` call panics, WHEN the panic is caught by a `recover()` wrapper, THEN the goroutine logs the panic, sets `PhaseBackoff` with `reason_code: internal_error`, and continues looping without crashing the process.

---

### REQ-208: StopForUpgrade / ResumeAfterUpgrade contract

`Manager.StopForUpgrade()` MUST set phase to `PhaseDisabled`, drain the run goroutine's pending work, and retain the sync lease so no other worker picks it up during the upgrade window. `Manager.ResumeAfterUpgrade()` MUST set phase back to `PhaseIdle` and re-arm the cycle without requiring a full Manager restart.

**Scenarios**:

- **StopForUpgrade halts cycle**: GIVEN a Manager in `PhaseHealthy`, WHEN `StopForUpgrade()` is called, THEN no further cycles run and phase is `PhaseDisabled`.
- **Lease retained during upgrade window**: GIVEN `StopForUpgrade()` was called, WHEN the lease expiry check runs, THEN the Manager does NOT release the lease until `Stop()` or a full shutdown.
- **ResumeAfterUpgrade restarts cycle**: GIVEN `StopForUpgrade()` was called, WHEN `ResumeAfterUpgrade()` is called and 30 s elapse, THEN a cycle runs and phase transitions out of `PhaseDisabled`.
- **Edge case — Resume without prior Stop is no-op**: GIVEN a Manager in `PhaseHealthy`, WHEN `ResumeAfterUpgrade()` is called, THEN the phase remains `PhaseHealthy` and behavior is unchanged.

---

### REQ-209: Status adapter — Manager phase to SyncStatus

An `autosyncStatusAdapter` MUST implement `server.SyncStatusProvider` (i.e., `Status(project string) server.SyncStatus`). The adapter MUST map autosync phases as follows: `PhaseHealthy` → `{Phase: "healthy"}`; `PhaseBackoff` → `{Phase: "degraded", ReasonCode: "transport_failed"}`; `PhasePushFailed` / `PhasePullFailed` → `{Phase: "degraded", ReasonCode: last_error_reason}`; `PhaseDisabled` → `{Phase: "degraded", ReasonCode: "upgrade_paused"}`; `PhaseIdle` / `PhasePushing` / `PhasePulling` → `{Phase: "running"}`. When the Manager is `nil` (autosync disabled), the adapter MUST fall back to `storeSyncStatusProvider`.

**Scenarios**:

- **Healthy phase maps correctly**: GIVEN Manager in `PhaseHealthy`, WHEN `adapter.Status("proj")` is called, THEN `SyncStatus.Phase == "healthy"`.
- **Backoff maps to degraded**: GIVEN Manager in `PhaseBackoff`, WHEN `adapter.Status("proj")` is called, THEN `SyncStatus.Phase == "degraded"` and `ReasonCode == "transport_failed"`.
- **Nil manager falls back**: GIVEN `autosyncStatusAdapter{mgr: nil}`, WHEN `adapter.Status("proj")` is called, THEN the result equals what `storeSyncStatusProvider.Status("proj")` would return.
- **Disabled maps upgrade_paused**: GIVEN Manager in `PhaseDisabled`, WHEN `adapter.Status("proj")` is called, THEN `SyncStatus.ReasonCode == "upgrade_paused"`.

---

### REQ-210: Explicit opt-in via ENGRAM_CLOUD_AUTOSYNC env var

Autosync MUST only start when `ENGRAM_CLOUD_AUTOSYNC=1` is set in the environment. Setting this env var without a valid token or server URL MUST NOT silently succeed (see REQ-211). Absence of the env var MUST leave autosync disabled with no log output beyond a single debug-level note. The env var MUST NOT auto-enable when only `ENGRAM_CLOUD_TOKEN` and `ENGRAM_CLOUD_SERVER` are set.

**Scenarios**:

- **Opt-in required — env absent**: GIVEN `ENGRAM_CLOUD_AUTOSYNC` is not set but token and server are set, WHEN `cmdServe` runs, THEN autosync does not start.
- **Opt-in present — autosync starts**: GIVEN `ENGRAM_CLOUD_AUTOSYNC=1`, token, and server URL are all set, WHEN `cmdServe` runs, THEN the Manager starts and a single info log confirms it.
- **Edge case — env value not "1"**: GIVEN `ENGRAM_CLOUD_AUTOSYNC=true`, WHEN `cmdServe` runs, THEN autosync does not start (only exact `"1"` is accepted).
- **Previous fatal inverted**: GIVEN `ENGRAM_CLOUD_AUTOSYNC=1` and valid config, WHEN `cmdServe` runs, THEN the process does NOT call `fatal()`; test `TestCmdServeAutosyncLifecycleGating` asserts the new behavior.

---

### REQ-211: Runtime gating — token and server URL required

When `ENGRAM_CLOUD_AUTOSYNC=1` is set but `ENGRAM_CLOUD_TOKEN` or `ENGRAM_CLOUD_SERVER` is missing, the runtime MUST log an error and skip starting autosync; it MUST NOT start the Manager with an empty token. The error MUST be logged at `ERROR` level and include which variable is missing. The `engram serve` process MUST still start successfully (autosync is optional; a missing token is not a fatal condition).

**Scenarios**:

- **Token missing — skip with error log**: GIVEN `ENGRAM_CLOUD_AUTOSYNC=1` and `ENGRAM_CLOUD_SERVER` set but `ENGRAM_CLOUD_TOKEN` empty, WHEN `cmdServe` runs, THEN autosync does not start and the log contains "ENGRAM_CLOUD_TOKEN".
- **Server URL missing — skip with error log**: GIVEN `ENGRAM_CLOUD_AUTOSYNC=1` and token set but `ENGRAM_CLOUD_SERVER` empty, WHEN `cmdServe` runs, THEN autosync does not start and the log contains "ENGRAM_CLOUD_SERVER".
- **Both present — starts normally**: GIVEN all three env vars set, WHEN `cmdServe` runs, THEN `tryStartAutosync` returns a non-nil Manager.
- **Edge case — serve continues without autosync**: GIVEN token is missing, WHEN `cmdServe` runs, THEN the HTTP server starts and handles requests normally.

---

### REQ-212: Local-first invariant

Autosync MUST run entirely in its own goroutine and MUST NOT hold any lock shared with the local write path during network I/O. A local write to the SQLite store MUST succeed and return within its normal latency bounds even when the cloud transport is unreachable or returning 5xx errors. The autosync loop MUST NOT block `store.WriteObservation` or any equivalent write method.

**Scenarios**:

- **Local write succeeds during transport 500**: GIVEN the cloud transport returns HTTP 500 on every call, WHEN a local observation is written, THEN the write returns without error and within the normal latency bound.
- **Goroutine isolation**: GIVEN the Manager's `cycle()` is sleeping in backoff, WHEN 1000 concurrent local writes are executed, THEN all writes complete without deadlock or timeout.
- **Transport error does not propagate to caller**: GIVEN `PushMutations` returns a network error, WHEN `Manager.NotifyDirty()` is called from a write hook, THEN the hook returns immediately and does not surface the error to the caller.
- **Edge case — cloud unreachable at startup**: GIVEN the cloud server is unreachable from process start, WHEN `cmdServe` starts autosync, THEN the server starts, accepts local requests, and the Manager enters `PhasePushFailed` after the first failed cycle.

---

### REQ-213: Goroutine lifecycle — no leaks and panic recovery

`Manager.Stop()` MUST guarantee all goroutines spawned by `Run()` have exited before it returns. The `Run()` goroutine MUST wrap `cycle()` in a `recover()` call; a panic inside `cycle()` MUST set `PhaseBackoff` with `reason_code: "internal_error"`, log the stack trace, and allow the loop to continue. A panic MUST NOT crash the host process.

**Scenarios**:

- **Stop exits cleanly**: GIVEN a running Manager, WHEN `Stop()` returns, THEN all goroutines spawned by `Run()` have exited (verified via `goleak.VerifyNone`).
- **Panic recovery sets Backoff**: GIVEN `cycle()` panics with a runtime error, WHEN the `recover()` wrapper catches it, THEN `Manager.Status().Phase == PhaseBackoff` and `ReasonCode == "internal_error"`.
- **Loop continues after panic**: GIVEN a panic on the first cycle, WHEN the backoff interval elapses, THEN the Manager attempts another cycle without needing a restart.
- **Edge case — Stop called before Run**: GIVEN `Stop()` is called on a Manager that has never started `Run()`, THEN `Stop()` returns immediately without blocking.

---

### REQ-214: Client 404 on mutation endpoints maps to PhaseBackoff

When the autosync transport receives HTTP 404 from `POST /sync/mutations/push` or `GET /sync/mutations/pull`, the Manager MUST treat it as a `PhaseBackoff` condition with `reason_code: "server_unsupported"`. This indicates the server has not yet deployed the mutation endpoints. The Manager MUST log a warning that advises the operator to deploy the server before enabling `ENGRAM_CLOUD_AUTOSYNC=1`.

**Scenarios**:

- **404 on push → server_unsupported**: GIVEN the cloud server returns HTTP 404 on `POST /sync/mutations/push`, WHEN a push cycle runs, THEN `Manager.Status().Phase == PhaseBackoff` and `ReasonCode == "server_unsupported"`.
- **404 on pull → server_unsupported**: GIVEN the cloud server returns HTTP 404 on `GET /sync/mutations/pull`, WHEN a pull cycle runs, THEN `Manager.Status().Phase == PhaseBackoff` and `ReasonCode == "server_unsupported"`.
- **Warning logged once**: GIVEN the first 404 is received, WHEN the manager logs the warning, THEN the log message contains "server_unsupported" and advice to deploy the server first.
- **Edge case — 404 vs 401 distinct codes**: GIVEN the cloud server returns HTTP 401, WHEN a push cycle runs, THEN `ReasonCode == "auth_required"` (not `"server_unsupported"`).


### REQ-215: Canonical validation before mutation storage

`POST /sync/mutations/push` MUST validate every supported `upsert` against the canonical payload contract before mutation storage. Required fields (non-blank strings) are:

| Entity | Required upsert payload fields |
|---|---|
| `session` | `id`, `directory` |
| `observation` | `sync_id`, `session_id`, `type`, `title`, `content`, `scope` |
| `prompt` | `sync_id`, `session_id`, `content` |
| `relation` | `sync_id`, `source_id`, `target_id`, `relation`, `judgment_status`, `marked_by_actor`, `marked_by_kind` |

The mutation payload MUST be either a native JSON object or a JSON string containing an encoded JSON object. Both forms MUST pass the same canonical validation. Encoded arrays, scalars, malformed strings, and decoded invalid objects MUST be rejected. If `entity_key` is supplied, it MUST match the canonical identity in the payload; an omitted key MAY be derived from that identity.

#### Scenario: Blank observation content is repairably rejected

- GIVEN an authorized, active-project observation upsert whose `content` is `""`, `"   "`, or whitespace-only
- WHEN the push is submitted
- THEN the server responds HTTP 400 with `error_class=repairable` and `error_code=payload_invalid`, before mutation storage

#### Scenario: Other canonical fields are enforced

- GIVEN an active-project upsert missing or blank in turn `session.directory`, `observation.scope`, `prompt.session_id`, or `relation.marked_by_kind`
- WHEN the push is submitted
- THEN the server responds HTTP 400 identifying the corresponding canonical field

#### Scenario: Complete relation remains compatible

- GIVEN a relation upsert containing all seven required fields
- WHEN the push is submitted
- THEN the server responds HTTP 200 and accepts the relation compatibly

### REQ-216: Delete compatibility and whole-batch admission

Session, observation, and prompt `delete` mutations MUST remain supported and require their delete identity (`session.id`, `observation.sync_id`, or `prompt.sync_id`); upsert-only fields MUST NOT be required for deletes. Relation deletes remain unsupported. Any invalid entry MUST reject the complete batch before mutation storage; the server MUST NOT store a subset, return accepted sequences, or partially persist the batch.

#### Scenario: Valid upserts and deletes are preserved

- GIVEN a batch containing valid session, observation, prompt, and supported delete mutations
- WHEN the push is submitted
- THEN the server responds HTTP 200 with an acknowledgement for every stored sequence

#### Scenario: Mixed valid and invalid entries are atomic

- GIVEN a batch with valid entries and one observation upsert with blank `content`
- WHEN the push is submitted
- THEN the server responds HTTP 400, makes no mutation-storage call, returns no partial acknowledgement, and persists no entry

### REQ-217: Structured repairable errors and policy ordering

Validation failures MUST use the JSON error envelope: HTTP 400, `error_class=repairable`, `error_code=payload_invalid`, and `error`. The response MUST include `invalid`, with each offending entry represented by zero-based `index`, `entity`, and canonical `field`; all invalid entries SHOULD be reported. Authentication and project authorization MUST occur before payload-validation details, and pause policy MUST be checked after authorization but before payload validation. A paused authorized project MUST retain HTTP 409 without payload details.

#### Scenario: Structured details identify the offender

- GIVEN invalid entries at indexes 1 and 3 for entities `observation` and `prompt`
- WHEN the push is submitted to an active project
- THEN the 400 response includes `invalid` details identifying indexes 1 and 3 and their missing fields

#### Scenario: Authorization and pause precede payload validation

- GIVEN a malformed payload for an unauthorized project, or an invalid payload for an authorized paused project
- WHEN the push is submitted
- THEN the first case returns the existing authorization error and the second returns HTTP 409 `sync-paused`; neither exposes payload-validation details

#### Scenario: Valid requests retain existing acknowledgement behavior

- GIVEN an authorized active project and a batch in which every upsert and delete satisfies this contract
- WHEN the push is submitted
- THEN the server responds HTTP 200 and acknowledges all entries exactly as before
---

## MODIFIED Requirements

### REQ-cloud-sync-status: Autosync phase as authoritative status source

The `server.SyncStatusProvider` implementation wired into `cmdServe` and `cmdMCP` MUST be the `autosyncStatusAdapter` when autosync is enabled. When autosync is disabled (Manager is nil), the provider MUST fall back to `storeSyncStatusProvider`. The dashboard status pill MUST reflect autosync phase transitions without any additional dashboard code changes.

(Previously: `storeSyncStatusProvider` was the sole status source; autosync phase had no representation in the status pipeline.)

**Scenarios**:

- **Autosync enabled — adapter wins**: GIVEN autosync is started, WHEN `/sync/status` is queried, THEN the returned phase reflects the autosync Manager's current phase.
- **Autosync disabled — store fallback**: GIVEN `ENGRAM_CLOUD_AUTOSYNC` is not set, WHEN `/sync/status` is queried, THEN the result comes from `storeSyncStatusProvider`.
- **Phase change reflects in status immediately**: GIVEN Manager transitions from `PhaseHealthy` to `PhasePushFailed`, WHEN `adapter.Status("proj")` is called next, THEN the returned phase is `"degraded"`.
- **Edge case — status per project**: GIVEN multiple projects enrolled, WHEN `adapter.Status("proj-A")` and `adapter.Status("proj-B")` are called, THEN both return the same autosync phase (phase is global to the Manager, not per-project).

---

## Test Seam Summary

| REQ | Test name(s) |
|-----|-------------|
| REQ-200 | `TestMutationPushEndpointAccepted`, `TestMutationPushEndpointUnauth`, `TestMutationPushEndpointBatchTooLarge`, `TestMutationPushEndpointEmptyBatch` |
| REQ-201 | `TestMutationPullEndpointSinceSeq`, `TestMutationPullEndpointHasMore`, `TestMutationPullEndpointUnauth`, `TestMutationPullEndpointBeyondLatest` |
| REQ-202 | `TestMutationPullEnrollmentFilter`, `TestMutationPullCrossTenantLeak`, `TestMutationPullNoEnrollments`, `TestMutationPullUnenrolledSeqSkipped` |
| REQ-203 | `TestMutationPushSyncPaused409`, `TestMutationPushNonPausedAccepted`, `TestMutationPushPausePerProject`, `TestMutationPushPauseAdminStillBlocked` |
| REQ-204 | `TestManagerPhaseTransitions`, `TestManagerPushFailedPhase`, `TestManagerPullFailedPhase`, `TestManagerStopForUpgradeDisabled` |
| REQ-205 | `TestManagerBackoffExponentialGrowth`, `TestManagerBackoffJitterBounds`, `TestManagerBackoffCeiling`, `TestManagerBackoffResetOnSuccess` |
| REQ-206 | `TestManagerNotifyDirtyOneCycle`, `TestManagerNotifyDirtyCoalesce`, `TestManagerNotifyDirtyDuringBackoff`, `TestManagerNotifyDirtyAfterStop` |
| REQ-207 | `TestManagerRunContextCancel`, `TestManagerRunPollTicker`, `TestManagerStopWaitsGoroutine`, `TestManagerRunPanicRecovery` |
| REQ-208 | `TestManagerStopForUpgradeHaltsCycle`, `TestManagerStopForUpgradeRetainsLease`, `TestManagerResumeAfterUpgrade`, `TestManagerResumeWithoutStop` |
| REQ-209 | `TestSyncStatusAdapterHealthy`, `TestSyncStatusAdapterBackoff`, `TestSyncStatusAdapterNilFallback`, `TestSyncStatusAdapterDisabled` |
| REQ-210 | `TestCmdServeAutosyncLifecycleGating` (inverted), `TestAutosyncEnvAbsent`, `TestAutosyncEnvNotOne`, `TestAutosyncOptInRequired` |
| REQ-211 | `TestAutosyncGatingTokenMissing`, `TestAutosyncGatingServerMissing`, `TestAutosyncGatingBothPresent`, `TestCmdServeStartsWithoutAutosync` |
| REQ-212 | `TestLocalWriteDuringTransport500`, `TestGoroutineIsolationConcurrentWrites`, `TestNotifyDirtyHookNonBlocking`, `TestLocalWriteCloudUnreachable` |
| REQ-213 | `TestManagerStopNoGoroutineLeak`, `TestManagerPanicSetsBackoff`, `TestManagerLoopContinuesAfterPanic`, `TestManagerStopBeforeRun` |
| REQ-214 | `TestTransport404PushServerUnsupported`, `TestTransport404PullServerUnsupported`, `TestTransport404WarningLogged`, `TestTransport401AuthRequired` |

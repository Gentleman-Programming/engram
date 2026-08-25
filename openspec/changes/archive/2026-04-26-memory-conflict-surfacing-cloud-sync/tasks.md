# Tasks: memory-conflict-surfacing-cloud-sync

Change: memory-conflict-surfacing-cloud-sync
Artifact store: hybrid (engram + openspec mirror)
Engram topic_key: `sdd/memory-conflict-surfacing-cloud-sync/tasks`

---

## Phase A — Migration test infrastructure (RED first)

Satisfies: REQ-010 | Design §10 (Migration test pattern)

- [x] A.1 Define `legacyDDLPostMemoryConflictSurfacing` constant in `internal/store/store_legacy_ddl_test.go` capturing the full post-Phase-1 schema (observations, memory_relations, indexes, FTS5 triggers).
- [x] A.2 Add `newTestStoreWithLegacySchemaPostP1` helper and `legacyRelationRow` type in `internal/store/store_migration_test.go`; add `migrationFixtureRowsPostP1` producing 3 obs + 2 relation rows.
- [x] A.3 Write RED tests `TestMigrate_PostPhase1_AddsSyncApplyDeferred` and `TestMigrate_PostPhase1_PreservesExistingRows` in `internal/store/store_migration_test.go`; both fail RED confirming sync_apply_deferred is absent.

**Acceptance**: `go test ./internal/store/... -run TestMigrate_PostPhase1` → 2 failures (expected RED). ✅ CONFIRMED (Phase A)

---

## Phase B — Schema additions (GREEN for A)

Satisfies: REQ-010 | Design §2 (Schema additions)

- [x] B.1 Add `sync_apply_deferred` table via `CREATE TABLE IF NOT EXISTS` in `migrate()` in `internal/store/store.go` with columns: `sync_id TEXT PK`, `entity TEXT NOT NULL`, `payload TEXT NOT NULL`, `apply_status TEXT NOT NULL DEFAULT 'deferred'`, `retry_count INTEGER NOT NULL DEFAULT 0`, `last_error TEXT`, `last_attempted_at TEXT`, `first_seen_at TEXT NOT NULL DEFAULT (datetime('now'))`.
- [x] B.2 Add `CREATE INDEX IF NOT EXISTS idx_sad_status_seen ON sync_apply_deferred(apply_status, first_seen_at)` in same migration block.
- [x] B.3 Add error sentinels `ErrRelationFKMissing`, `ErrApplyDead`, `ErrCrossProjectRelation` to `internal/store/store.go`.

**Acceptance**: Phase A tests now PASS; `go test ./internal/store/... -run TestMigrate_PostPhase1` → green. CONFIRMED 2026-04-26.

---

## Phase C — Store layer: push side + pull side (parallel with D)

Satisfies: REQ-001, REQ-002, REQ-003, REQ-009, REQ-011 | Design §1, §3, §4

### C.1 — Write RED tests for push side
- [x] C.1a `internal/store/relations_test.go` — `JudgeRelation_EnqueuesSyncMutation_WhenEnrolled`: assert `sync_mutations` gains row with `entity='relation'`, `entity_key=relation.sync_id`, payload has `source_id`, `target_id`, `judgment_status='judged'`, `project='proj-a'`. Must FAIL.
- [x] C.1b `JudgeRelation_DoesNotEnqueue_WhenNotEnrolled`: same call without enrollment; assert NO new row in `sync_mutations`. Must FAIL.
- [x] C.1c `FindCandidates_DoesNotEnqueue`: call `FindCandidates`, assert no `sync_mutations` row added. Must FAIL.
- [x] C.1d `JudgeRelation_RejectsCrossProject`: source.project != target.project → expect `ErrCrossProjectRelation` and no `memory_relations` insert. Must FAIL.
- [x] C.1e `JudgeRelation_MissingSource_EnqueuesEmptyProject`: source obs missing → mutation has `project=''`. Must FAIL.

### C.2 — Implement push side
- [x] C.2a Define `SyncEntityRelation = "relation"` constant in `internal/store/store.go`.
- [x] C.2b Define `syncRelationPayload` struct (13 fields per design §1) in `internal/store/store.go`.
- [x] C.2c Add `marshalRelationPayload` / `unmarshalRelationPayload` helpers in same file. (marshal: standard json.Marshal via enqueueSyncMutationTx; unmarshal: decodeSyncPayload)
- [x] C.2d Wrap `JudgeRelation` in `internal/store/relations.go` in a transaction; add cross-project guard before UPDATE; call `enqueueSyncMutationTx` when project is enrolled; derive project via `SELECT ifnull(project,'') FROM observations WHERE sync_id = source_id`.

**Acceptance**: C.1a–e tests PASS. ✅ CONFIRMED 2026-04-26 — 5/5 tests GREEN.

### C.3 — Write RED tests for pull side
- [x] C.3a Create `internal/store/sync_apply_test.go` — `ApplyPulledRelation_InsertsWhenObsExist`: both obs present → row upserted in `memory_relations`. Must FAIL.
- [x] C.3b `ApplyPulledRelation_DefersOnFKMiss`: target obs absent → row in `sync_apply_deferred` with `retry_count=0`, `apply_status='deferred'`; seq ACKed; no halt. Must FAIL.
- [x] C.3c `ApplyPulledRelation_IdempotentOnSyncID` (REQ-009): same mutation pulled twice → one row in `memory_relations`. Must FAIL.
- [x] C.3d `ApplyPulledRelation_MultiActorSamePair`: two mutations, same (source,target) pair, different `sync_id` → two distinct rows in `memory_relations`. Must FAIL.

### C.4 — Implement pull side
- [x] C.4a Add `case store.SyncEntityRelation:` branch in `applyPulledMutationTx` in `internal/store/store.go` delegating to new `applyRelationUpsertTx`.
- [x] C.4b Implement `applyRelationUpsertTx`: decode payload; check both obs exist via `SELECT count(*)`; `INSERT INTO memory_relations ... ON CONFLICT(sync_id) DO UPDATE SET ...`; on success `DELETE FROM sync_apply_deferred WHERE sync_id=?`.
- [x] C.4c Implement caller-level `ErrRelationFKMissing` catch in the pull loop: `INSERT INTO sync_apply_deferred ... ON CONFLICT(sync_id) DO UPDATE SET ...`; ACK seq; continue. (C.4c is caller-level for Phase E autosync; pull-side defer logic in test is complete)

**Acceptance**: C.3a–d tests PASS. ✅ CONFIRMED 2026-04-26 — 4/4 tests GREEN.

---

## Phase D — Cloudserver validation (parallel with C)

Satisfies: REQ-006, REQ-008 | Design §6

### D.1 — Write RED tests
- [x] D.1a `internal/cloud/cloudserver/mutations_test.go` — `HandleMutationPush_ValidRelation_Returns200`: full valid relation payload → HTTP 200. Must FAIL.
- [x] D.1b `HandleMutationPush_RelationMissingEachRequiredField`: parametrized test for each of {sync_id, source_id, target_id, judgment_status, marked_by_actor, marked_by_kind} → HTTP 400, body has correct `field` name. Must FAIL.
- [x] D.1c `HandleMutationPush_PartialBatch_Atomic`: 2-entry batch, one invalid → HTTP 400, neither entry stored. Must FAIL.
- [x] D.1d `HandleMutationPush_LegacyObsMissingOptional_Returns200`: `entity='observation'` with only `sync_id` populated → HTTP 200. Must FAIL.

### D.2 — Implement validation
- [x] D.2a Add `validateRelationPayload(payload json.RawMessage) (field string, ok bool)` in `internal/cloud/cloudserver/mutations.go` — checks all 6 required fields non-empty.
- [x] D.2b Add `validateLegacyPayload(entity string, payload json.RawMessage) (field string, ok bool)` — no-op for legacy entities (REQ-008: unchanged behavior).
- [x] D.2c Add `validateMutationEntry(entry MutationEntry)` dispatch switch.
- [x] D.2d Wire validation loop into `handleMutationPush` BEFORE `InsertMutationBatch`; collect invalid indices; return 400 with `invalid` list if any.

**Acceptance**: D.1a–d tests PASS. ✅ CONFIRMED 2026-04-26 — all 19 packages GREEN.

---

## Phase E — Autosync resilience: skip+log + replayDeferred

Satisfies: REQ-007, REQ-008 | Design §5, §9

### E.1 — Write RED tests
- [x] E.1a `internal/cloud/autosync/manager_test.go` — `ReplayDeferred_RetriesAndApplies`: deferred row exists; missing obs arrives; `replayDeferred()` runs → row applied, removed from `sync_apply_deferred`. Must FAIL.
- [x] E.1b `ReplayDeferred_DeadAfterFiveRetries`: row at `retry_count=4`, dep still missing → after one `replayDeferred()` call `apply_status='dead'`. Must FAIL.
- [x] E.1c `ReplayDeferred_DeadRowNotRetried`: dead row present → `replayDeferred()` does NOT attempt apply. Must FAIL.
- [x] E.1d `Pull_LegacyEntityNonFKError_StillHalts` (REQ-008): `entity='observation'` apply error → pull loop halts, cursor does NOT advance. Must FAIL.
- [x] E.1e `SyncStatus_IncludesDeferredAndDeadCounts` (REQ-007 edge): 3 deferred + 1 dead → `/sync/status` response has `deferred_count:3`, `dead_count:1`. Must FAIL.

### E.2 — Implement resilience
- [x] E.2a Implement `replayDeferred()` in `internal/store/store.go`: query `WHERE apply_status='deferred' ORDER BY first_seen_at LIMIT 50`; call `applyPulledMutationTx`; on success delete; on FK-miss increment `retry_count`, set `apply_status='dead'` if `>= 5`; on decode error set `dead`.
- [x] E.2b Wire `replayDeferred()` call at start of pull cycle in `internal/cloud/autosync/manager.go`, BEFORE `PullMutations`.
- [x] E.2c Enforce per-entity error policy in pull apply loop in `manager.go`: `relation` FK miss → defer+ACK+continue (handled inside `ApplyPulledMutation`); legacy errors → halt (existing behavior).
- [x] E.2d Expose `deferred_count` and `dead_count` from `sync_apply_deferred` in `/sync/status` handler (query two counts by `apply_status`).

**Acceptance**: E.1a–e tests PASS; `go test ./internal/cloud/autosync/...` green. ✅ CONFIRMED 2026-04-26 — all 19 packages PASS.

---

## Phase F — mem_search annotation upgrade (parallel with G)

Satisfies: REQ-004, REQ-005, REQ-012 | Design §7, §8

### F.1 — Write RED tests
- [x] F.1a `internal/mcp/mcp_test.go` — `MemSearch_AnnotatesConflictsWith_Judged`: obs with judged `conflicts_with` rel → annotation line `conflicts: #<id> (Use Redis for caching)`. Must FAIL.
- [x] F.1b `MemSearch_PendingConflict_KeepsPhase1Annotation`: pending `conflicts_with` rel → line reads `conflict: contested by #<id> (pending)` (unchanged). Must FAIL.
- [x] F.1c `MemSearch_TitleEnrichment_SupersedesAndSupersededBy`: judged supersedes/superseded_by → title in parens. Must FAIL.
- [x] F.1d `MemSearch_TitleEnrichment_FallsBackToDeleted`: target hard-deleted → annotation reads `(deleted)`. Must FAIL.
- [x] F.1e `MemSearch_AllThreeTypes_FormatExact` (REQ-012): all 3 annotation types present → format matches contract byte-for-byte. Must FAIL.

### F.2 — Implement annotation upgrade
- [x] F.2a Extend `GetRelationsForObservations` in `internal/store/relations.go` with LEFT JOIN to `observations` for `source_title`, `target_title`, `source_missing`, `target_missing`.
- [x] F.2b Add title fields to `Relation` struct (or new `AnnotatedRelation` wrapper) without touching wire payload.
- [x] F.2c In `internal/mcp/mcp.go` annotation switch: add `case RelationConflictsWith:` for `judgment_status='judged'` emitting `conflicts: #<id> (<title>)`.
- [x] F.2d Upgrade `supersedes:` and `superseded_by:` annotation lines to include `(<title>)` from JOIN result; `(deleted)` fallback when target missing.
- [x] F.2e Add inline comment in `mcp.go` documenting the annotation format contract (REQ-012).

**Acceptance**: F.1a–e tests PASS. ✅ CONFIRMED 2026-04-26 — all 19 packages GREEN.

---

## Phase G — Integration tests (parallel with F)

Satisfies: REQ-001–REQ-009 cross-cutting | Design §11 (cross-machine test)

- [x] G.1 Write integration test `TestRelationSync_PushPull_CrossMachine` in `internal/store/sync_apply_test.go` (or new `integration_test.go`): store A judges relation, pushes to fake cloudserver, store B pulls → both `memory_relations` match by `sync_id`.
- [x] G.2 Write `TestRelationSync_FKMissDeferRetrySuccess`: push relation before source obs; pull on B → deferred; push source obs; pull again → deferred row applied and deleted.
- [x] G.3 Write `TestRelationSync_RetryCapDead`: simulate 5 failed retries on deferred row → `apply_status='dead'`; 6th call does not retry.
- [x] G.4 Write `TestRelationSync_ServerValidation_MissingField`: client pushes relation missing `source_id` → server returns 400; no row stored.
- [x] G.5 Write `TestRelationSync_BackwardsCompat_LegacyClient`: client pushes only `session` + `observation` mutations → server returns 200 for all; response shape unchanged.
- [x] G.6 Write `TestRelationSync_MultiActor_TwoDistinctRows`: two actors judge same (obs-A, obs-B) pair with different `sync_id`; both rows land on machine C after pull.

**Acceptance**: All G tests PASS; `go test ./... -run TestRelationSync` green. ✅ CONFIRMED 2026-04-26 — all 6 G tests GREEN; 19 packages PASS.

---

## Phase H — Documentation (sequential, after F and G)

Satisfies: REQ-012 | Design §7 annotation contract

- [x] H.1 Update `docs/PLUGINS.md`: document the annotation format contract (supersedes, superseded_by, conflicts, pending), note prefix-based parser stability requirement, note Phase 3 will not alter prefixes.
- [x] H.2 Update `DOCS.md` (root): add entry that `mem_search` returns relation annotation lines with title enrichment; document new `/sync/status` fields (`deferred_count`, `dead_count`).
- [x] H.3 Note Phase 2 deferrals in `docs/AGENT-SETUP.md` if any user-visible behavior changed (annotation format with title, new `/sync/status` fields).
- [x] H.4 Add inline note in `internal/store/store.go` atop `sync_apply_deferred` migration block: "Phase 3: add republish CLI, surface dead rows via mem_status." (ALREADY DONE in Phase B)

**Acceptance**: `docs/PLUGINS.md` and `DOCS.md` updated; `go test ./...` still green after doc edits (no compile regressions). ✅ CONFIRMED 2026-04-26 — all 19 packages PASS.

---

## Dependency Graph

```
A (migration test RED) → B (schema GREEN)
                              │
                    ┌─────────┴──────────┐
                    C (store push+pull)   D (cloudserver validation)
                    │
                    E (autosync resilience)
                              │
                    ┌─────────┴──────────┐
                    F (annotation upgrade)  G (integration tests)
                              │
                              H (docs)
```

Parallel groups: {C, D} run together after B; {F, G} run together after E.

---

## Task Count by Phase

| Phase | Tasks | Spec REQs | Status |
|-------|-------|-----------|--------|
| A — Migration test infra | 3 | REQ-010 | COMPLETE |
| B — Schema additions | 3 | REQ-010 | COMPLETE |
| C — Store push + pull | 9 | REQ-001, 002, 003, 009, 011 | COMPLETE |
| D — Cloudserver validation | 8 | REQ-006, 008 | COMPLETE |
| E — Autosync resilience | 9 | REQ-007, 008 | COMPLETE |
| F — Annotation upgrade | 10 | REQ-004, 005, 012 | COMPLETE |
| G — Integration tests | 6 | REQ-001–009 | COMPLETE |
| H — Documentation | 4 | REQ-012 | COMPLETE |
| **Total** | **52** | | 52/52 complete |

---

## Strict TDD Ordering

Within every phase, test tasks (RED) always precede implementation tasks (GREEN). No implementation task is listed before its paired failing test. Refactor step is implicit after GREEN.

Mirror: `openspec/changes/memory-conflict-surfacing-cloud-sync/tasks.md`
Engram: `sdd/memory-conflict-surfacing-cloud-sync/tasks` (project: engram)

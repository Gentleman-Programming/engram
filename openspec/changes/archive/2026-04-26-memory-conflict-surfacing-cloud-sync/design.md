# Design: memory-conflict-surfacing-cloud-sync

Phase: SDD design (Phase 2 of memory-conflict-surfacing)
Change: memory-conflict-surfacing-cloud-sync
Engram topic_key: `sdd/memory-conflict-surfacing-cloud-sync/design`
Artifact store: hybrid (engram + openspec mirror)

---

## Technical Approach

Phase 2 layers cloud replication onto the Phase 1 local conflict-surfacing
substrate without altering Phase 1 semantics. Four orthogonal pieces:

1. A new sync entity `relation` riding the existing
   `sync_mutations` / `cloud_mutations` rails — same push/pull/ack/cursor
   machinery as `session` / `observation` / `prompt`.
2. A new local table `sync_apply_deferred` for FK-aware retry of pulled
   relation mutations whose source/target observation has not arrived yet.
3. A per-entity validator at the cloud ingestion boundary
   (`handleMutationPush`): strict for `relation`, lenient floor for legacy.
4. Annotation polish in `mem_search` (`conflicts:` line + `(<title>)`
   enrichment) atop the same `GetRelationsForObservations` helper.

Boundary respected: persistence → `internal/store`, transport → `internal/mcp`,
cloud ingestion → `internal/cloud/cloudserver`, replication loop →
`internal/cloud/autosync`. No FTS5 changes, no schema changes on legacy tables.

## 1. Wire payload — `syncRelationPayload`

```go
type syncRelationPayload struct {
    SyncID          string   `json:"sync_id"`
    SourceID        string   `json:"source_id"`
    TargetID        string   `json:"target_id"`
    Relation        string   `json:"relation"`
    Reason          *string  `json:"reason,omitempty"`
    Evidence        *string  `json:"evidence,omitempty"`
    Confidence      *float64 `json:"confidence,omitempty"`
    JudgmentStatus  string   `json:"judgment_status"`
    MarkedByActor   *string  `json:"marked_by_actor,omitempty"`
    MarkedByKind    *string  `json:"marked_by_kind,omitempty"`
    MarkedByModel   *string  `json:"marked_by_model,omitempty"`
    SessionID       *string  `json:"session_id,omitempty"`
    Project         string   `json:"project"`
    CreatedAt       string   `json:"created_at"`
    UpdatedAt       string   `json:"updated_at"`
}
```

13-field subset of the 17-column row. Excluded:
- `id` (local autoincrement, not portable)
- `superseded_at`, `superseded_by_relation_id` (Phase 3 supersede chain;
  schema present, behavior deferred — `omitempty` would mean Phase 3 can add
  them with no wire bump).

`omitempty` on every optional field keeps payloads compact and matches
existing `syncSessionPayload` / `syncObservationPayload` style.

## 2. Schema additions

```sql
CREATE TABLE IF NOT EXISTS sync_apply_deferred (
    sync_id           TEXT    PRIMARY KEY,
    entity            TEXT    NOT NULL,
    payload           TEXT    NOT NULL,
    apply_status      TEXT    NOT NULL DEFAULT 'deferred', -- 'deferred' | 'applied' | 'dead'
    retry_count       INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_attempted_at TEXT,
    first_seen_at     TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_sad_status_seen
    ON sync_apply_deferred(apply_status, first_seen_at);
```

- `sync_id` PK enables idempotent re-defer (same FK miss arriving twice updates
  the existing row, not duplicates it).
- No FK to `memory_relations` — the row may persist when the target
  relation does not exist locally yet.
- No `op` column — relation pulls are upsert-only Phase 2; symmetric delete is
  Phase 3.
- Index supports `WHERE apply_status='deferred' ORDER BY first_seen_at`
  in `replayDeferred()`.

Migration baseline: define `legacyDDLPostMemoryConflictSurfacing` in
`internal/store/store_test.go` capturing the post-Phase-1 schema (pre this
change). It becomes the new baseline constant for future migration tests.

## 3. Push side flow

`JudgeRelation` (Phase 1) becomes transactional:

```
BEGIN
  UPDATE memory_relations SET ... WHERE sync_id=?
  -- new in Phase 2:
  IF project enrolled for cloud sync THEN
    project := SELECT ifnull(project,'') FROM observations WHERE sync_id = source_id
    enqueueSyncMutationTx(tx, SyncEntityRelation, syncID, SyncOpUpsert,
                           project, marshalRelationPayload(row))
COMMIT
```

Cross-project guard runs BEFORE the UPDATE: if
`source.project != target.project`, return `ErrCrossProjectRelation` and abort
the transaction. Pre-Phase-2 cross-project rows in the DB stay readable
locally; the push path never enqueues them because the guard sits at write
time, not at backfill.

Edge case (REQ-011): if the source observation is missing locally (race with
a deferred-pulled stub), `project` resolves to `''`. The mutation enqueues
anyway; the server's `validateRelationMutation` rejects it with 400, the
client logs at WARNING, and the seq is dropped (existing pending-mutation
fail behavior). Loud failure, no silent drop.

`FindCandidates` does NOT enqueue (REQ-001 negative case). The pending row it
inserts has `judgment_status='pending'` — stays local.

## 4. Pull side flow — `applyPulledMutationTx` extension

New branch in the existing switch:

```go
case store.SyncEntityRelation:
    return applyRelationUpsertTx(tx, mutation)
```

`applyRelationUpsertTx`:

1. JSON-decode `mutation.Payload` into `syncRelationPayload`.
   On decode error → return `ErrApplyDead` (non-retryable) → caller writes
   `apply_status='dead'`, ACKs seq, continues.
2. Verify both observations exist:
   `SELECT count(*) FROM observations WHERE sync_id IN (source, target)`.
   Result < 2 → return `ErrRelationFKMissing`.
3. Otherwise:
   `INSERT INTO memory_relations (...) VALUES (...) ON CONFLICT(sync_id) DO UPDATE SET ...`.
   Last-write-wins on every column except `id` and `created_at` (preserve
   original creation timestamp).
4. After successful apply, `DELETE FROM sync_apply_deferred WHERE sync_id = ?`
   (cleans up rows that got deferred earlier and now succeed).

The caller (autosync pull loop) catches `ErrRelationFKMissing` and:

```go
INSERT INTO sync_apply_deferred (sync_id, entity, payload, apply_status, retry_count, first_seen_at)
VALUES (?, 'relation', ?, 'deferred', 0, datetime('now'))
ON CONFLICT(sync_id) DO UPDATE
  SET payload = excluded.payload,
      last_attempted_at = datetime('now')
```

Then ACKs the seq and continues. The cursor never blocks (REQ-002 negative).

## 5. `replayDeferred()` — retry loop

Called at the START of every pull cycle, BEFORE `PullMutations`:

```go
rows := SELECT sync_id, entity, payload, retry_count
        FROM sync_apply_deferred
        WHERE apply_status = 'deferred'
        ORDER BY first_seen_at
        LIMIT 50
for r := range rows:
    mutation := store.SyncMutation{Entity: r.entity, EntityKey: r.sync_id, Payload: r.payload, Op: SyncOpUpsert}
    err := applyPulledMutationTx(tx, mutation)
    switch:
      err == nil:
        UPDATE sync_apply_deferred SET apply_status='applied', last_attempted_at=now WHERE sync_id=?
        (or just DELETE — applyRelationUpsertTx already deletes on success)
      errors.Is(err, ErrRelationFKMissing):
        retry_count := r.retry_count + 1
        if retry_count >= 5:
          UPDATE ... SET apply_status='dead', retry_count=?, last_error=?, last_attempted_at=now
          log.Warn("sync_apply_deferred: dead", sync_id, retries=5)
        else:
          UPDATE ... SET retry_count=?, last_error=?, last_attempted_at=now
      default (decode/validation):
        UPDATE ... SET apply_status='dead', last_error=?, last_attempted_at=now
```

Cap 50 rows/cycle keeps the work bounded; backlog drains across cycles.
`dead` rows never retried, never deleted (audit trail; Phase 3 surfaces them
in `mem_search`/`mem_status` and adds a republish CLI).

## 6. Server-side validation

New helper in `internal/cloud/cloudserver/mutations.go`:

```go
func validateMutationPayload(entry MutationEntry) (field string, ok bool) {
    switch entry.Entity {
    case "relation":
        return validateRelationPayload(entry.Payload)
    default:
        // Lenient floor for legacy entities (REQ-006 negative case).
        return validateLegacyPayload(entry.Entity, entry.Payload)
    }
}
```

`validateRelationPayload` checks all six required fields per REQ-006:
`sync_id`, `source_id`, `target_id`, `judgment_status`, `marked_by_actor`,
`marked_by_kind`. Each missing field returns its field name and `false`.

`validateLegacyPayload` enforces only the existing de-facto floor: `sync_id`
(or `id` for `session`) non-empty. No new fields required for legacy entities
(REQ-008 backwards compat).

Integration into `handleMutationPush`: after the empty-batch + empty-project +
auth + pause-gate checks, BEFORE `InsertMutationBatch`:

```go
var invalid []map[string]any
for i, entry := range req.Entries {
    if field, ok := validateMutationPayload(entry); !ok {
        invalid = append(invalid, map[string]any{"index": i, "field": field, "entity": entry.Entity})
    }
}
if len(invalid) > 0 {
    jsonResponse(w, http.StatusBadRequest, map[string]any{
        "error":       "invalid relation payload",
        "reason_code": "validation_error",
        "invalid":     invalid,
    })
    return
}
```

Atomic batch: any failure → no entries inserted (REQ-006 partial-batch case).

## 7. Annotation format upgrade

`internal/mcp/mcp.go` annotation switch (Phase 1 had pending case + supersedes
case + superseded_by case; missing `conflicts_with` judged case):

```
supersedes:    #<id> (<title>)        // judged supersedes
superseded_by: #<id> (<title>)        // judged superseded_by
conflicts:     #<id> (<title>)        // NEW: judged conflicts_with
conflict: contested by #<id> (pending) // UNCHANGED from Phase 1
```

CRITICAL: the Phase 1 pending annotation stays exactly as it is. New
behavior is purely additive — `case RelationConflictsWith:` for
`judgment_status='judged'` plus title enrichment on supersedes/superseded_by.

Title enrichment is a JOIN at search time — no denormalization, no N+1.
Format contract documented in inline comment above the switch and in
`docs/PLUGINS.md` (REQ-012). Prefix-based parsers are unaffected.

## 8. mem_search query strategy for annotations

Extend `GetRelationsForObservations` with LEFT JOIN to `observations`:

```sql
SELECT r.id, r.sync_id, r.source_id, r.target_id, r.relation, r.reason,
       r.evidence, r.confidence, r.judgment_status, r.marked_by_actor,
       r.marked_by_kind, r.marked_by_model, r.session_id, r.created_at, r.updated_at,
       src.title AS source_title,
       tgt.title AS target_title,
       src.deleted_at IS NOT NULL OR src.id IS NULL AS source_missing,
       tgt.deleted_at IS NOT NULL OR tgt.id IS NULL AS target_missing
FROM memory_relations r
LEFT JOIN observations src ON src.sync_id = r.source_id
LEFT JOIN observations tgt ON tgt.sync_id = r.target_id
WHERE (r.source_id IN (...) OR r.target_id IN (...))
  AND r.judgment_status != 'orphaned'
```

Annotation builder picks the OTHER side's title relative to the search
result. Missing/deleted → fall back to `(deleted)` (REQ-005 edge case).

Add fields to `Relation` struct (or a wrapper `AnnotatedRelation`) without
touching the wire payload — annotations are a read-side concern.

## 9. autosync.pull skip+log behavior — per-entity policy

The skip-and-continue logic differentiates by entity AND error type:

| Entity     | FK miss                     | Decode/validation error      | Other error |
|------------|-----------------------------|------------------------------|-------------|
| `relation` | `sync_apply_deferred`, ACK  | `apply_status='dead'`, ACK   | halt (existing) |
| legacy     | log + skip + ACK (no defer) | log + skip + ACK             | halt (existing) |

Rationale: relations are the only entity with cross-row FK dependencies
that legitimately resolve later (REQ-002). Sessions/observations/prompts have
simpler dependency models — when they fail, log+skip is enough; deferring
adds complexity without payoff. REQ-008 negative case (legacy halts on
non-FK error) is preserved.

## 10. Migration test pattern

```go
const legacyDDLPostMemoryConflictSurfacing = `
    -- (full DDL post-Phase-1: observations + new columns + memory_relations + indexes + FTS5)
`

func TestMigrate_PostPhase1_AddsSyncApplyDeferred(t *testing.T) {
    s := newTestStoreWithLegacySchema(t, legacyDDLPostMemoryConflictSurfacing,
        []legacyObsRow{...}, []legacyRelationRow{...})
    // Assertions:
    // 1. sync_apply_deferred table exists with correct columns
    // 2. Index idx_sad_status_seen exists
    // 3. All fixture observations and memory_relations rows preserved byte-for-byte
    // 4. Re-running migrate is a no-op (REQ-010 idempotent)
}
```

`newTestStoreWithLegacySchema` is the helper introduced in Phase 1; this
phase reuses it with the new constant. Each future schema change appends a
new constant snapshot — accumulating versioned migration test infrastructure.

## 11. Test strategy (strict TDD)

Each REQ → at least one failing test before implementation.

| REQ | Test file | Test name |
|-----|-----------|-----------|
| 001 | `internal/store/relations_test.go` | `JudgeRelation_EnqueuesSyncMutation_WhenEnrolled` |
| 001 | `internal/store/relations_test.go` | `JudgeRelation_DoesNotEnqueue_WhenNotEnrolled` |
| 001 | `internal/store/relations_test.go` | `FindCandidates_DoesNotEnqueue` |
| 002 | `internal/store/sync_apply_test.go` (NEW) | `ApplyPulledRelation_InsertsWhenObsExist` |
| 002 | `internal/store/sync_apply_test.go` | `ApplyPulledRelation_DefersOnFKMiss` |
| 003 | `internal/store/relations_test.go` | `JudgeRelation_RejectsCrossProject` |
| 004 | `internal/mcp/mcp_test.go` | `MemSearch_AnnotatesConflictsWith` |
| 004 | `internal/mcp/mcp_test.go` | `MemSearch_PendingConflict_KeepsPhase1Annotation` |
| 005 | `internal/mcp/mcp_test.go` | `MemSearch_TitleEnrichment_FallsBackToDeleted` |
| 006 | `internal/cloud/cloudserver/mutations_test.go` | `HandleMutationPush_RejectsRelationMissingActor` |
| 006 | `internal/cloud/cloudserver/mutations_test.go` | `HandleMutationPush_AcceptsLegacyMissingOptional` |
| 007 | `internal/cloud/autosync/manager_test.go` | `ReplayDeferred_RetriesAndApplies` |
| 007 | `internal/cloud/autosync/manager_test.go` | `ReplayDeferred_DeadAfterFiveRetries` |
| 008 | `internal/cloud/autosync/manager_test.go` | `Pull_LegacyEntityNonFKError_StillHalts` |
| 009 | `internal/store/sync_apply_test.go` | `ApplyPulledRelation_IdempotentOnSyncID` |
| 010 | `internal/store/store_migration_test.go` | `Migrate_PostPhase1_AddsSyncApplyDeferred` |
| 011 | `internal/store/relations_test.go` | `JudgeRelation_MissingSource_EnqueuesEmptyProject` |
| 012 | `internal/mcp/mcp_test.go` | `MemSearch_AllThreeTypes_FormatExact` |

Cross-machine integration test: spin up two stores against one fake
cloudserver, judge on A, push, pull on B, assert `memory_relations` rows
match by sync_id. Lives in `internal/cloud/autosync/integration_test.go`
(if exists) or new file.

## 12. Phase 3 hooks

| Phase 3 capability | Phase 2 substrate |
|--------------------|-------------------|
| Republish CLI | `syncRelationPayload` finalized; one fn writes from `memory_relations` to `sync_mutations` |
| Dead-row UX | `sync_apply_deferred.apply_status='dead'` already populated; surface via mem_search/mem_status |
| Cross-team admin dashboard | Provenance crosses wire; query layer reads from `cloud_mutations` |
| Multi-actor disagreement resolver | Schema permits N rows per pair; resolver is read-side |
| `engram review` decay activation | Unchanged from Phase 1 substrate |
| Soft delete on relations | `op='delete'` branch in `applyPulledMutationTx`; wire format already has it |

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/store/store.go` | Modify | Add `SyncEntityRelation` const; add `syncRelationPayload` type; extend `applyPulledMutationTx` with relation case; add `applyRelationUpsertTx`; add `replayDeferredMutations` query helpers; create `sync_apply_deferred` table in `migrate()`; declare `ErrCrossProjectRelation`, `ErrRelationFKMissing`, `ErrApplyDead` |
| `internal/store/relations.go` | Modify | Wrap `JudgeRelation` in tx; add cross-project guard; call `enqueueSyncMutationTx` for enrolled projects; extend `GetRelationsForObservations` with LEFT JOIN for titles |
| `internal/store/store_test.go` | Modify | Add `legacyDDLPostMemoryConflictSurfacing` constant |
| `internal/store/store_migration_test.go` | Modify | Add `TestMigrate_PostPhase1_AddsSyncApplyDeferred` |
| `internal/store/relations_test.go` | Modify | Add push-side tests (REQ-001, 003, 011) |
| `internal/store/sync_apply_test.go` | Create | Pull-apply tests (REQ-002, 009) |
| `internal/cloud/cloudserver/mutations.go` | Modify | Add `validateMutationPayload`, `validateRelationPayload`, `validateLegacyPayload`; integrate into `handleMutationPush` |
| `internal/cloud/cloudserver/mutations_test.go` | Modify | Add validation tests (REQ-006) |
| `internal/cloud/autosync/manager.go` | Modify | Add `replayDeferred()` step at pull-cycle start; per-entity error policy in apply loop |
| `internal/cloud/autosync/manager_test.go` | Modify | Add deferred-retry tests (REQ-007, 008) |
| `internal/mcp/mcp.go` | Modify | Add `case RelationConflictsWith` in annotation switch; title enrichment from JOIN; document format contract |
| `internal/mcp/mcp_test.go` | Modify | Annotation tests (REQ-004, 005, 012) |
| `docs/PLUGINS.md` | Modify | Document annotation format contract |

NOT touched: `internal/dashboard/*`, `internal/cloud/cloudstore/*` (entity is
free TEXT), FTS5 triggers, `cmd/engram/*`.

## Architecture Decisions

### Decision: Defer-table key by sync_id (not autoincrement)

**Choice**: `sync_apply_deferred.sync_id` is the PRIMARY KEY.
**Alternatives considered**: separate `id INTEGER PRIMARY KEY` autoincrement with `UNIQUE(sync_id)`.
**Rationale**: idempotent re-defer is the dominant pattern (same FK miss arrives every retry until source observation lands). PK on sync_id makes `INSERT ... ON CONFLICT DO UPDATE` natural; no risk of unbounded duplicate rows for the same mutation.

### Decision: Strict validation only for new entity (`relation`)

**Choice**: `validateRelationPayload` strict; `validateLegacyPayload` floor only.
**Alternatives considered**: bump validation strictness for all entities together.
**Rationale**: legacy clients in the wild push payloads with optional fields missing. Tightening retroactively breaks REQ-008 (old client → new server). New entity has no legacy clients; we lock the contract on day 1. Phase 3 can graduate legacy entities incrementally.

### Decision: Per-entity skip policy (defer for relation, log+skip for legacy)

**Choice**: Only `relation` FK miss enters `sync_apply_deferred`; legacy entity errors keep current halt-on-error behavior.
**Alternatives considered**: defer-table for all entities; log+skip for all entities.
**Rationale**: relations are the only entity with legitimate "dependency arrives later" pattern. Sessions/observations/prompts that fail apply are real bugs — silencing them with skip+continue masks the bug. REQ-008 negative case codifies this.

### Decision: Title enrichment via JOIN at search time

**Choice**: LEFT JOIN `observations` in `GetRelationsForObservations`; `(deleted)` fallback.
**Alternatives considered**: denormalize titles into `memory_relations` columns and update on observation rename; cache in memory.
**Rationale**: titles can change (revision), and denormalization creates staleness without a clear win — search is rare enough that one JOIN is free. No N+1 because the existing query already batches by IN clause.

### Decision: Cross-project guard at write time, not at backfill

**Choice**: `JudgeRelation` returns `ErrCrossProjectRelation` if source.project != target.project. Pre-Phase-2 cross-project rows stay locally readable but never push.
**Alternatives considered**: server-side rejection only; sweep + audit existing cross-project rows.
**Rationale**: server rejection is too late — the local state already has the row. Sweeping risks deleting data users intended. Single guard at write time is sufficient and Phase 3 can relax it once project-merge UX exists (proposal §4 Q5 → resolved: leave existing rows alone).

### Decision: Retry cap 5, then `dead` (no retry, no delete)

**Choice**: 5 retries → `apply_status='dead'`. Dead rows never retried automatically. Phase 3 adds CLI surface.
**Alternatives considered**: exponential backoff; retry forever; auto-cleanup of dead > 30 days.
**Rationale**: 5 retries × pull cycle interval covers normal observation-arrival latency. Forever-retry leaks rows. Auto-cleanup loses audit trail. Manual republish in Phase 3 is the right boundary — operators decide.

## Data Flow

```
Machine A                                  Cloud Server                  Machine B
─────────                                  ────────────                  ─────────

mem_judge → JudgeRelation (tx)
              │
              ├─ UPDATE memory_relations
              ├─ guard: source.project == target.project
              └─ enqueueSyncMutationTx(entity='relation', ...)

autosync.push ────────────────► /sync/mutations/push
                                     │
                                     ├─ auth + pause + empty-project
                                     ├─ validateMutationPayload (NEW)
                                     │   ├─ relation → strict
                                     │   └─ legacy   → floor
                                     ├─ InsertMutationBatch
                                     └─ 200 / 400 + reason_code

                                                                  ◄──── /sync/mutations/pull
                                                                          │
                                                                          ├─ replayDeferred() (NEW, before pull)
                                                                          │   └─ retry deferred rows, mark dead at 5
                                                                          │
                                                                          └─ apply each:
                                                                              switch entity:
                                                                                case relation:
                                                                                  applyRelationUpsertTx
                                                                                  ├─ ok      → memory_relations upsert
                                                                                  ├─ FK miss → sync_apply_deferred + ACK
                                                                                  └─ decode  → dead + ACK
                                                                                case legacy:
                                                                                  existing path (halt on error)
```

## Open Questions

None blocking. The 5 questions in proposal §4 are now resolved:
- Q1 (replay cadence): same as pull.
- Q2 (`dead` UX): logs only Phase 2; surfacing deferred to Phase 3.
- Q3 (missing source project): enqueue `project=''`; server rejects; WARNING log (REQ-011).
- Q4 (validator location): inline switch in `cloudserver/mutations.go`.
- Q5 (cross-project retroactivity): leave existing rows; guard new writes.

---

## Mirror

File: `openspec/changes/memory-conflict-surfacing-cloud-sync/design.md`
Engram: `sdd/memory-conflict-surfacing-cloud-sync/design` (project: engram)

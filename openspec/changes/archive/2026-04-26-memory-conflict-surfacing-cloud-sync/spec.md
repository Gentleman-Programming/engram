# Spec: memory-conflict-surfacing-cloud-sync

Phase: SDD spec (Phase 2 of memory-conflict-surfacing)
Change: memory-conflict-surfacing-cloud-sync
Engram topic_key: `sdd/memory-conflict-surfacing-cloud-sync/spec`
Artifact store: hybrid (engram + openspec mirror)

---

## Purpose

Define what MUST be true after Phase 2 of memory-conflict-surfacing ships.
Phase 1 established local-only conflict detection and judgment. Phase 2 adds:
- Bidirectional cloud sync of `memory_relations` rows
- Complete annotation coverage including `conflicts_with` and title enrichment
- Server-side payload validation for the new `relation` entity type
- Client autosync resilience: skip-and-defer on FK-fail, bounded retry, dead-letter

Does NOT specify how — that is the design phase.

---

## Requirements

### REQ-001 — Relation push enqueueing

When `JudgeRelation` succeeds for a (source, target, judgment) tuple where the
source observation belongs to an enrolled project, the system MUST enqueue a sync
mutation row with:
- `entity = 'relation'`
- `entity_key = relation.sync_id`
- `op = 'upsert'`
- `project` derived from the source observation's project field
- payload containing the full relation row fields: sync_id, source_id, target_id,
  relation, reason, evidence, confidence, judgment_status, marked_by_actor,
  marked_by_kind, marked_by_model, session_id, project, created_at, updated_at

`FindCandidates` MUST NOT enqueue — pending rows stay local only.
Cross-project relations MUST be rejected before enqueueing (see REQ-003).

#### Scenario: Happy path — judged relation enqueues mutation

- GIVEN cloud sync is enrolled and source observation belongs to project "proj-a"
- WHEN `JudgeRelation` is called and succeeds, flipping status to `judged`
- THEN a row exists in `sync_mutations` with `entity='relation'`, `op='upsert'`, `entity_key=relation.sync_id`
- AND the payload contains `source_id`, `target_id`, `judgment_status='judged'`, `project='proj-a'`
- AND the mutation is written in the same transaction as the relation update

#### Scenario: Edge case — source project is empty string (deferred source)

- GIVEN source observation is missing locally (race condition)
- WHEN `JudgeRelation` is called
- THEN the mutation is enqueued with `project=''` (empty string)
- AND the server rejects it with 400 (cross-project or missing-project check fails)
- AND the failure is logged; behavior is documented as a known edge case

#### Scenario: Negative — FindCandidates does not enqueue

- GIVEN cloud sync is enrolled
- WHEN `FindCandidates` is called after a `mem_save`
- THEN `sync_mutations` gains no new row for `entity='relation'`
- AND the pending relation row exists only in `memory_relations`

#### Scenario: Negative — unenrolled project does not enqueue

- GIVEN the source observation belongs to a project not enrolled in cloud sync
- WHEN `JudgeRelation` succeeds
- THEN no row is added to `sync_mutations`

---

### REQ-002 — Relation pull apply

When `applyPulledMutationTx` receives a mutation with `entity='relation'` and
`op='upsert'`, the system MUST apply an `INSERT OR REPLACE INTO memory_relations`
keyed on `sync_id` (last-write-wins).

If the source or target observation does not exist locally at apply time, the
system MUST NOT halt the pull loop. Instead it MUST:
1. Record the mutation in `sync_apply_deferred` with `retry_count=0`
2. Advance the pull cursor (ACK the seq to the server)
3. Log the deferral and continue processing the next mutation

#### Scenario: Happy path — relation applied when observations exist

- GIVEN pulled mutation has `entity='relation'`, `op='upsert'`, and both source and target observations exist locally
- WHEN `applyPulledMutationTx` processes the mutation
- THEN a row is inserted (or replaced) in `memory_relations` with matching sync_id
- AND the pull cursor advances past this seq

#### Scenario: Edge case — same relation pulled twice yields identical state

- GIVEN a relation with `sync_id='rel_abc'` already exists in `memory_relations`
- WHEN the same mutation is pulled again (e.g. replay, reconnect)
- THEN the row is replaced in place; no duplicate, no error
- AND the resulting row is identical to the first apply

#### Scenario: Negative — FK-missing relation is deferred, not halted

- GIVEN pulled mutation references a target observation that does not exist locally
- WHEN `applyPulledMutationTx` processes the mutation
- THEN a row is inserted into `sync_apply_deferred` with `retry_count=0`, `apply_status='deferred'`
- AND the seq is acknowledged to the server
- AND the pull loop continues to the next mutation without error

---

### REQ-003 — Cross-project relation rejection

Relations whose source observation's project differs from the target observation's
project MUST be rejected at write time. `SaveRelation` (or `JudgeRelation`) MUST
return `ErrCrossProjectRelation` when this constraint is violated.

Pre-existing relations from Phase 1 that violate this rule MUST remain locally
readable but MUST NOT be pushed to cloud sync.

#### Scenario: Happy path — same-project relation is accepted

- GIVEN source and target observations both belong to project "proj-a"
- WHEN `JudgeRelation` is called
- THEN the call succeeds; no error returned

#### Scenario: Negative — cross-project relation is rejected at write

- GIVEN source observation belongs to "proj-a" and target belongs to "proj-b"
- WHEN `JudgeRelation` is called
- THEN `ErrCrossProjectRelation` is returned
- AND no row is inserted or updated in `memory_relations`
- AND no row is added to `sync_mutations`

#### Scenario: Edge case — pre-Phase-2 cross-project row is not pushed

- GIVEN a `memory_relations` row exists from Phase 1 where source.project != target.project
- WHEN the sync push cycle runs
- THEN that row is not enqueued into `sync_mutations`
- AND the row remains locally readable via `GetRelationsForObservation`

---

### REQ-004 — `conflicts_with` text annotation in mem_search

When `mem_search` returns an observation that has at least one `judged` relation
with `relation='conflicts_with'`, the result entry MUST include annotation lines
of the form: `conflicts: #<id> (<title>)` — one per conflicting related memory.

Observations with no `conflicts_with` relations MUST produce results unchanged
from today's format (regression guard).

#### Scenario: Happy path — conflicts_with relation annotated

- GIVEN observation #10 has a judged `conflicts_with` relation targeting observation #20 titled "Use Redis for caching"
- WHEN `mem_search` returns #10
- THEN the result entry includes the line `conflicts: #20 (Use Redis for caching)`

#### Scenario: Edge case — multiple conflicts each produce a separate line

- GIVEN observation #10 has `conflicts_with` relations to both #20 and #30
- WHEN `mem_search` returns #10
- THEN two annotation lines appear: one for #20, one for #30, each on its own line

#### Scenario: Negative — pending conflicts_with does not produce annotation

- GIVEN observation #10 has a `conflicts_with` relation with `judgment_status='pending'`
- WHEN `mem_search` returns #10
- THEN no `conflicts:` annotation line appears (only judged relations annotate)

#### Scenario: Negative — no conflicts_with means format unchanged

- GIVEN observation #10 has no relations of any kind
- WHEN `mem_search` returns #10
- THEN the result entry is byte-for-byte identical to today's format (regression guard)

---

### REQ-005 — Title enrichment in annotations

All `mem_search` annotation lines for judged relations (`supersedes:`,
`superseded_by:`, `conflicts:`) MUST include the related memory's title in
parentheses. The title MUST be retrieved via JOIN at search time (no N+1 queries).

When the related observation has been soft-deleted or does not exist locally,
the annotation MUST fall back to `(deleted)`.

The annotation format contract is:
```
supersedes: #<id> (<title>)
superseded_by: #<id> (<title>)
conflicts: #<id> (<title>)
```
Multiple entries appear on separate lines, in the order returned by the query.

#### Scenario: Happy path — title included for existing target

- GIVEN observation #42 has a `supersedes` relation targeting #18 titled "Old JWT approach"
- WHEN `mem_search` returns #42
- THEN the annotation line reads: `supersedes: #18 (Old JWT approach)`

#### Scenario: Edge case — deleted target falls back to (deleted)

- GIVEN observation #42's `supersedes` target has been hard-deleted
- WHEN `mem_search` returns #42
- THEN the annotation line reads: `supersedes: #<id> (deleted)`

#### Scenario: Negative — bare `#<id>` format without title is NOT acceptable

- GIVEN any judged relation exists with a live target
- WHEN `mem_search` returns an observation with that relation
- THEN the annotation line MUST include a parenthesized title, never bare `#<id>`

---

### REQ-006 — Server-side schema validation for relation push

`handleMutationPush` on the cloud server MUST validate each mutation payload by
entity type. For `entity='relation'`, validation MUST be strict: all of the
following fields are REQUIRED and must be non-empty:
`sync_id`, `source_id`, `target_id`, `judgment_status`, `marked_by_actor`, `marked_by_kind`.

A missing or empty required field MUST cause a 400 response with body:
`{"error": "invalid relation payload", "reason_code": "validation_error", "field": "<field-name>"}`.

Batch atomicity: if any entry in a batch fails validation, the entire batch is
rejected with 400 listing all offending indices. No entries are inserted.

Legacy entities (`session`, `observation`, `prompt`) MUST retain current lenient
validation: only `sync_id` (or `id` for sessions) must be non-empty.

#### Scenario: Happy path — valid relation payload accepted

- GIVEN a push batch with one `entity='relation'` entry containing all required fields
- WHEN `handleMutationPush` processes it
- THEN HTTP 200 is returned and the mutation is stored

#### Scenario: Negative — missing required field rejected

- GIVEN a push batch with one `entity='relation'` entry missing `marked_by_actor`
- WHEN `handleMutationPush` processes it
- THEN HTTP 400 is returned with `reason_code='validation_error'` and `field='marked_by_actor'`
- AND no entry is written to storage

#### Scenario: Edge case — partial batch rejection (multiple entries, one invalid)

- GIVEN a batch with two entries: one valid `relation` and one invalid `relation` (missing `target_id`)
- WHEN `handleMutationPush` processes the batch
- THEN HTTP 400 is returned listing the offending index
- AND neither entry is written (atomic batch)

#### Scenario: Negative — legacy observation with missing optional fields still accepted

- GIVEN a push batch with `entity='observation'` that is missing `review_after` and `embedding*` fields
- WHEN `handleMutationPush` processes it
- THEN HTTP 200 is returned (lenient floor, no new required fields for legacy entities)

---

### REQ-007 — Deferred retry mechanism

Mutations that fail on apply due to FK-missing MUST be tracked in the
`sync_apply_deferred` table with columns:
`sync_id` (PK), `entity`, `payload`, `retry_count`, `last_attempted_at`,
`last_error`, `apply_status` (`deferred` | `applied` | `dead`), `first_seen_at`.

On every pull cycle, a `replayDeferred()` step MUST run. It MUST:
1. Iterate deferred rows in insertion order (up to 50 rows per cycle)
2. Attempt to re-apply each mutation
3. On success: update `apply_status='applied'`
4. On failure with `retry_count < 5`: increment `retry_count`, update `last_attempted_at`, update `last_error`
5. On failure with `retry_count >= 5`: update `apply_status='dead'` — no further retries

The count of `deferred` and `dead` rows MUST be exposed in `/sync/status`.

#### Scenario: Happy path — deferred row retried and applied on next cycle

- GIVEN a deferred row exists with `retry_count=0` and the missing observation arrives
- WHEN `replayDeferred()` runs in the next pull cycle
- THEN the relation is applied to `memory_relations`, `apply_status='applied'`

#### Scenario: Edge case — row reaches dead status after 5 failures

- GIVEN a deferred row has `retry_count=4` and the dependency still missing
- WHEN `replayDeferred()` runs and fails
- THEN `retry_count` becomes 5 and `apply_status='dead'`
- AND on subsequent cycles the row is not retried

#### Scenario: Negative — non-retryable error (decode/validation failure) goes straight to dead

- GIVEN a pulled mutation has malformed JSON payload
- WHEN `applyPulledMutationTx` processes it
- THEN `apply_status='dead'` immediately, `retry_count=0`
- AND the pull loop continues without halting

#### Scenario: Edge case — /sync/status includes deferred and dead counts

- GIVEN 3 deferred rows and 1 dead row exist in `sync_apply_deferred`
- WHEN `/sync/status` is called
- THEN the response includes `deferred_count: 3` and `dead_count: 1`

---

### REQ-008 — Backwards compatibility for older clients

Clients that do NOT push relation mutations and do NOT understand `entity='relation'`
on pull MUST be unaffected. Their session/observation/prompt push and pull flows
MUST be unchanged. Server response shape MUST preserve all existing fields.

The skip-and-defer behavior on pull (REQ-002) MUST be gated on
`entity='relation'` or `ErrRelationFKMissing`. Legacy entity error paths MUST
retain the existing halt behavior.

#### Scenario: Happy path — old client push/pull cycle unaffected

- GIVEN an old client pushing only observation and session mutations
- WHEN it completes a push/pull cycle against a Phase 2 server
- THEN all existing mutations are processed identically to today
- AND no new fields appear in the server response that break the client

#### Scenario: Negative — legacy entity error still halts (no silent skip)

- GIVEN a pull mutation has `entity='observation'` and causes a non-FK error
- WHEN `applyPulledMutationTx` processes it
- THEN the pull loop halts (existing behavior preserved); the cursor does NOT advance past this error

---

### REQ-009 — Idempotency on relation pull

The same relation (identified by `sync_id`) pulled multiple times MUST yield
identical local state. `INSERT OR REPLACE` semantics on `sync_id` MUST be used.

Multi-actor judgments for the same (source, target) pair have distinct `sync_id`
values. Pulling both creates 2 distinct local rows.

#### Scenario: Happy path — same sync_id pulled twice is idempotent

- GIVEN `memory_relations` contains a row with `sync_id='rel_abc'`
- WHEN the same mutation (`sync_id='rel_abc'`) is pulled again
- THEN only one row exists with `sync_id='rel_abc'`, with fields matching the latest apply

#### Scenario: Edge case — two actors same pair create two rows

- GIVEN two mutations arrive: both for (obs-A, obs-B) but with `sync_id='rel_x'` and `sync_id='rel_y'`
- WHEN both are applied
- THEN two distinct rows exist in `memory_relations` for the same (source_id, target_id) pair

---

### REQ-010 — Migration safety for sync_apply_deferred

The `sync_apply_deferred` table MUST be added via `CREATE TABLE IF NOT EXISTS`
following the existing migration pattern. No existing rows in `observations`,
`memory_relations`, or `sync_mutations` MUST be altered.

A migration test MUST use a `legacyDDLPostMemoryConflictSurfacing` snapshot (the
DDL state at end of Phase 1) and verify all existing rows survive migration intact.

Running `migrate()` twice MUST be a no-op (idempotent).

#### Scenario: Happy path — Phase 1 schema migrates to Phase 2 cleanly

- GIVEN a DB created with the post-Phase-1 DDL containing existing rows in `memory_relations` and `observations`
- WHEN `migrate()` runs
- THEN `sync_apply_deferred` table exists with the correct schema
- AND all existing rows in `memory_relations` and `observations` are intact

#### Scenario: Edge case — idempotent second migration run

- GIVEN `migrate()` has already run successfully on a Phase 2 DB
- WHEN `migrate()` is called again
- THEN no error is returned and schema is identical

#### Scenario: Negative — no existing data is altered by migration

- GIVEN `memory_relations` contains 3 rows from Phase 1
- WHEN `migrate()` runs
- THEN all 3 rows retain their original id, sync_id, source_id, target_id, and judgment_status values

---

### REQ-011 — Project derivation failure is loud

When enqueueing a relation push and the source observation's project cannot be
determined (e.g. source observation is itself missing locally), the system MUST
persist `project=''` (empty string) in the mutation payload. The server MUST
reject this with 400 (cross-project or missing-project check). This edge case
MUST be logged at WARNING level; it MUST NOT be silently swallowed.

#### Scenario: Happy path — normal case has populated project

- GIVEN source observation is present locally with `project='proj-a'`
- WHEN `JudgeRelation` enqueues the sync mutation
- THEN the mutation payload contains `project='proj-a'`

#### Scenario: Negative — missing source yields empty project, server rejects

- GIVEN source observation is missing locally (race)
- WHEN the mutation with `project=''` reaches the server
- THEN the server returns 400
- AND the WARNING log entry is present locally

---

### REQ-012 — Annotation format contract (documented)

The `mem_search` result text format for relation annotations is a versioned
contract. The format MUST be:
```
supersedes: #<id> (<title>)
superseded_by: #<id> (<title>)
conflicts: #<id> (<title>)
```
Multiple entries appear on separate lines. This format MUST be documented in
`internal/mcp/mcp.go` (inline comment) and `docs/PLUGINS.md`. Agent parsers
use prefix-based matching; the prefix (`supersedes:`, `superseded_by:`,
`conflicts:`) MUST remain stable across Phase 3.

#### Scenario: Happy path — format matches contract exactly

- GIVEN an observation has all three relation types (supersedes, superseded_by, conflicts_with)
- WHEN `mem_search` returns it
- THEN annotation lines match the documented format exactly, one per line

#### Scenario: Negative — format change breaks parser contract

- GIVEN an agent parses annotation lines by prefix (`supersedes:`, `superseded_by:`, `conflicts:`)
- WHEN annotation format is altered (e.g. changing to `→` or dropping parentheses)
- THEN this constitutes a breaking change — MUST NOT be done without version bump

---

## Acceptance Criteria Summary

| REQ | Statement | Happy Path | Edge Case | Negative |
|-----|-----------|-----------|-----------|----------|
| 001 | JudgeRelation enqueues sync mutation | judged relation → mutation row | source missing → empty project | FindCandidates no enqueue |
| 002 | Pull applies relation; FK-fail defers | obs exist → row applied | same sync_id twice → idempotent | FK-miss → defer, loop continues |
| 003 | Cross-project rejected at write | same project → accepted | pre-Phase-2 row not pushed | cross-project → ErrCrossProjectRelation |
| 004 | conflicts_with annotated in search | conflicts relation → annotation | multiple conflicts → multiple lines | pending → no annotation |
| 005 | Title enrichment on all annotations | live target → title in parens | deleted target → (deleted) | bare #id without title NOT acceptable |
| 006 | Server validates relation entity strictly | valid payload → 200 | partial batch → 400, none inserted | legacy entity missing opt fields → 200 |
| 007 | Deferred retry with dead-letter cap | retry succeeds on next cycle | dead after 5 failures | decode error → straight to dead |
| 008 | Old clients unaffected | old client push/pull unchanged | — | legacy error still halts loop |
| 009 | Pull idempotency by sync_id | same sync_id twice → one row | two actors → two rows | — |
| 010 | sync_apply_deferred added safely | Phase-1 schema migrates cleanly | idempotent re-run | existing rows unchanged |
| 011 | Empty project fails loud | normal case → populated project | — | missing source → empty project → 400 + WARNING |
| 012 | Annotation format is stable contract | all three types formatted correctly | — | format change = breaking change |

---

## Implementation Hints

- `internal/store/store.go` / `relations.go`: `JudgeRelation` wraps in transaction with `enqueueSyncMutationTx`; cross-project guard before INSERT; `SaveRelation` returns `ErrCrossProjectRelation`; `sync_apply_deferred` table via `CREATE TABLE IF NOT EXISTS` in `migrate()`; `replayDeferred()` helper.
- `internal/mcp/mcp.go`: add `case store.RelationConflictsWith` in annotation switch; `GetRelationsForObservations` gains LEFT JOIN for title; document format contract in inline comment.
- `internal/cloud/cloudserver/mutations.go`: `validateMutationPayload(entity, payload)` helper; strict for `"relation"`, lenient floor for legacy; per-entry 400 with field name.
- `internal/cloud/autosync/manager.go`: pull loop catches `ErrRelationFKMissing` → insert `sync_apply_deferred`, continue; `replayDeferred()` called at cycle start; cap 50 rows/cycle.
- `internal/store/store_test.go`: add `legacyDDLPostMemoryConflictSurfacing` snapshot; migration test verifies row preservation.
- Test files: follow TDD loop — write failing test first, then make it pass, then refactor.

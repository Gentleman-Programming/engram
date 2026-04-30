# Proposal: memory-conflict-surfacing-cloud-sync

Phase: SDD propose (Phase 2 of memory-conflict-surfacing)
Change: memory-conflict-surfacing-cloud-sync
Engram topic_key: `sdd/memory-conflict-surfacing-cloud-sync/proposal`
Artifact store: hybrid (engram + openspec mirror)

---

## 1. Why

Phase 1 (`memory-conflict-surfacing`) shipped the local-first half of
conflict surfacing: schema, candidate detection, the `mem_judge` tool,
provenance columns, decay defaults, and orphaning semantics. The verify
report explicitly deferred two cosmetic gaps and the entire cloud-sync
surface to Phase 2.

What is incomplete after Phase 1:

1. **Relations never leave the laptop.** `JudgeRelation` writes to
   `memory_relations` but does not enqueue a `sync_mutation`. A team
   judging the same memory pair on two machines silently diverges; the
   cloud never sees the verdict; cross-team observability is impossible.
   This violates the local-first-but-shareable mental model the rest of
   engram (sessions, observations, prompts) already obeys.
2. **`mem_search` annotations are incomplete.** Phase 1 wires
   `supersedes` and `pending` markers but skips `conflicts_with` entirely
   and never includes the target title — agents see `supersedes: #42`
   instead of `supersedes: #42 (Switched from sessions to JWT)`. Verify
   flagged this as a SUGGESTION; we promised to close it in Phase 2.
3. **The cloud server validates nothing per entity type.** Today
   `handleMutationPush` enforces auth, project presence, batch size, and
   the pause gate, but a malformed observation payload (missing
   `session_id`) lands as raw JSONB and only blows up later on pull.
   With a new entity (`relation`) about to enter the wire protocol, the
   ingestion boundary needs schema validation — strictly for the new
   entity, leniently for legacy entities so older clients keep working.
4. **One bad pull mutation halts the entire sync loop.** When
   `applyPulledMutationTx` returns an error, the autosync manager
   propagates it up and freezes the pull cursor. Once relations cross
   the wire, FK ordering between observations and relations becomes a
   real failure mode (relation arrives before its source observation).
   The pull loop must skip+log+continue, with bounded deferred retry so
   late-arriving FKs eventually resolve instead of being silently lost.

Closing all four pieces together makes Phase 2 the moment "memory
conflict surfacing" actually becomes a multi-machine, team-visible
capability.

### Success criteria

- A verdict recorded via `mem_judge` on machine A appears in
  `memory_relations` on machine B within one autosync cycle, with
  identical `sync_id`, `relation`, `reason`, `evidence`, `confidence`,
  and `marked_by_*` provenance.
- `mem_search` annotates all three relation states
  (`supersedes`, `superseded_by`, `conflicts_with`, `pending`) and every
  marker carries the target title in `(<title>)` format.
- Push of a malformed `relation` payload returns 400 with a clear
  `validation_error` reason; push of a malformed legacy
  `observation`/`session`/`prompt` payload retains today's lenient
  behavior. Backwards compat verified by replaying real production
  payloads against the new validator.
- A pull batch containing a relation whose source observation has not
  yet arrived locally does NOT halt the cycle. The relation is recorded
  as `apply_status='deferred'` and re-attempted on the next pull cycle;
  after 5 failed retries it transitions to `apply_status='dead'` with a
  permanent log entry.

---

## 2. What Changes (high-level shape)

### Piece 1 — Cloud sync of `memory_relations` (bidirectional, day 1)

- New entity constant `SyncEntityRelation = "relation"` in
  `internal/store/store.go`. Additive — no DDL on either side
  (`sync_mutations.entity` and `cloud_mutations.entity` are free TEXT).
- New payload struct `syncRelationPayload` mirroring the wire shape:
  `sync_id`, `source_id`, `target_id`, `relation`, `reason`, `evidence`,
  `confidence`, `judgment_status`, `marked_by_actor`, `marked_by_kind`,
  `marked_by_model`, `session_id`, `project`, `created_at`, `updated_at`.
- **Push side:** `JudgeRelation` becomes transactional. After the
  `UPDATE memory_relations`, in the same `tx`, it derives the project
  from the source observation, builds the payload, and calls
  `enqueueSyncMutationTx(tx, SyncEntityRelation, syncID, SyncOpUpsert,
  payload)`. Cross-project guard: if source and target observations are
  in different projects, the judgment is rejected with a clear error
  (this is also a Phase 1 schema-level fix — Phase 1 never enforced
  same-project, so a guard at the write boundary is a small correctness
  patch landing in this phase). `FindCandidates` does NOT enqueue —
  pending rows stay local (no wire noise).
- **Pull side:** `applyPulledMutationTx` gains a `case
  SyncEntityRelation` branch that calls `applyRelationUpsertTx` (and a
  symmetric `applyRelationDeleteTx` even though Phase 2 does not emit
  delete ops — present for forward compatibility). Apply uses
  `INSERT ... ON CONFLICT(sync_id) DO UPDATE` (last-write-wins keyed on
  `sync_id`). FK lookups: source/target observation `sync_id` must
  exist locally; if either is missing, return a typed
  `ErrRelationFKMissing` so the manager can route to deferred retry.
- **Project attribution:** `extractProjectFromPayload` gains a case for
  `syncRelationPayload` reading `payload.Project`. The project is
  populated at push time from the source observation's project.

### Piece 2 — Cosmetic polish (annotations + title enrichment)

- Add a `case store.RelationConflictsWith` to the annotation switch in
  `internal/mcp/mcp.go`. Format:
  `conflicts_with: #<id> (<title>)`.
- Add `(<title>)` to every annotation format. Approach: extend
  `GetRelationsForObservations` to LEFT JOIN `observations` on
  `source_id`/`target_id` so each `Relation` carries the counterpart
  title (no N+1, no schema change). When the joined observation is soft-
  deleted or missing, fall back to bare `#<id>` to avoid lying.
- Verify the bare-id regression test from Phase 1 (REQ-007) still
  passes — observations with NO relations remain byte-identical.

### Piece 3 — Server-side validation hardening

- New helper `validateMutationPayload(entity string, payload json.RawMessage)
  error` in `internal/cloud/cloudserver/mutations.go`. Called from
  `handleMutationPush` per entry, after auth and pause gate.
- **Strict for `relation`** (new entity, no legacy clients can send it):
  require non-empty `sync_id`, `source_id`, `target_id`, `relation`
  (must be one of the locked verb set), `judgment_status` (must be in
  the locked status set), and `project`. Reject with HTTP 400 and
  `reason_code: "validation_error"` plus the offending field name.
- **Lenient for legacy entities** (`session`, `observation`, `prompt`):
  add a minimal floor — require `sync_id` (or `id` for sessions) to be
  non-empty. Do NOT add new required fields that older clients may omit.
  The principle: validation can only TIGHTEN where the entity is new;
  for legacy entities it merely codifies the de-facto floor.
- Validation errors are reported per-entry in the response body so a
  batch with one bad entry returns 400 with all bad indices flagged.

### Piece 4 — Client autosync resilience (skip+log+continue with deferred retry)

- New local table `sync_apply_deferred` (small additive DDL):
  `sync_id TEXT PRIMARY KEY, entity TEXT NOT NULL, payload TEXT NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0, last_error TEXT,
  apply_status TEXT NOT NULL DEFAULT 'deferred',
  first_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
  last_attempt_at TEXT NOT NULL DEFAULT (datetime('now'))`. Status
  transitions: `deferred → applied` (success), `deferred → dead` (after
  5 retries).
- Pull loop change in `autosync/manager.go`: when
  `ApplyPulledMutation` returns `ErrRelationFKMissing` (or any typed
  retryable error we wire), the manager:
  1. Inserts/updates `sync_apply_deferred` with the payload.
  2. Logs at info level with the `sync_id` and reason.
  3. Advances `sinceSeq` past the failed seq (cursor moves forward —
     server never resends).
  4. Continues with the next mutation in the batch.
- New cycle step `replayDeferred()` runs at the start of each pull
  cycle, before `pull()`. It iterates `sync_apply_deferred` where
  `apply_status='deferred'`, calls `applyPulledMutationTx` per row,
  marks `applied` on success or increments `retry_count` on failure;
  rows reaching `retry_count=5` flip to `apply_status='dead'`.
- Non-retryable errors (e.g., decode failure, validation error post-
  pull) go straight to `apply_status='dead'` with no retry — log only.
- Backwards compat: if `applyPulledMutationTx` returns an unknown error
  type for an entity that already worked (session/observation/prompt),
  preserve today's halt behavior. The skip+log+continue path is gated
  on entity=`relation` OR error type=`ErrRelationFKMissing` (a typed
  sentinel) so we do not regress existing entity behavior.

---

## 3. Out of Scope (deferred)

- **Cloud admin dashboard for cross-team conflict visibility** — Phase
  2 ships the data substrate (provenance fields cross the wire); a
  read-only dashboard view is a separate, larger UX effort.
- **Decay activation (`engram review` command)** — `review_after` is
  populated since Phase 1; running the actual review surface requires
  CLI plumbing untouched here.
- **pgvector / embedding generation** — `embedding*` columns reserved
  in Phase 1; generation pipeline and hybrid retrieval stay future.
- **Multi-actor disagreement *resolution heuristic*** — schema and
  sync already permit N rows per pair (no `UNIQUE(source,target)`).
  Choosing how to *resolve* contradictory verdicts (last-writer-wins
  vs. confidence-weighted vs. operator override) is its own design and
  belongs in a later phase.
- **Cross-project relations** — explicitly rejected at the write
  boundary in this phase; supporting them later requires schema and UX
  work not included here.
- **Server-side `cloud_mutations.entity` enum tightening** — staying
  free TEXT keeps backwards compat; tightening to an enum is a Phase 3
  concern once all clients ship the new validator.

---

## 4. Open Questions (truly unresolved)

The six exploration questions are pre-decided (see Already-Decided
Context in the orchestration brief). The genuinely unresolved items
needing input from spec/design phases are:

1. **Deferred-retry cadence.** Does `replayDeferred()` run on every
   pull cycle, or on a slower secondary timer? Recommendation:
   every cycle (same cadence as pull), capped at 50 rows per cycle to
   bound work; design phase to confirm.
2. **`apply_status='dead'` UX surface.** Should dead rows show up in
   `mem_search` annotations as a tombstone, in `mem_status`, or only in
   logs? Recommendation: logs only Phase 2; expose in dashboard later.
   Spec phase to confirm.
3. **Project derivation when source observation is also deferred.** If
   I judge a relation whose source is itself a deferred observation
   (ordering edge case), what project do we attribute? Recommendation:
   the relation push fails locally with a clear error and the user re-
   judges once observations are settled. Design phase to validate this
   does not produce a deadlock loop.
4. **Validator schema location.** Inline `switch entity` in
   `mutations.go` or a separate `validators/` subpackage? Recommendation:
   inline first; refactor only if a third entity type lands. Design
   phase to lock.
5. **Same-project enforcement retroactivity.** Phase 1 may already
   contain cross-project relations in the wild on dev databases. Do we
   leave them or sweep them with a migration? Recommendation: leave
   them; the new guard prevents future cross-project relations and the
   sync layer simply skips ones it cannot project-attribute. Design
   phase to decide if a `mem_audit` log entry is warranted on first
   encounter.

---

## 5. Affected Modules

| Module | Files | Nature of change |
|--------|-------|------------------|
| `internal/store` | `store.go`, `relations.go` | Add entity constant + payload + apply funcs; transactional `JudgeRelation`; cross-project guard; new `sync_apply_deferred` table + helpers |
| `internal/mcp` | `mcp.go` | Annotation case for `conflicts_with`; title enrichment in all marker formats |
| `internal/cloud/cloudserver` | `mutations.go` | Per-entity payload validator; per-entry 400 reporting |
| `internal/cloud/cloudstore` | `cloudstore.go` | No code change (entity is free TEXT) — confirm only |
| `internal/cloud/autosync` | `manager.go` | Skip+log+continue for typed retryable errors; new `replayDeferred()` cycle step |
| `openspec/changes/memory-conflict-surfacing-cloud-sync/` | NEW | Spec, design, tasks artifacts |

NOT touched: `internal/dashboard/*`, `cmd/engram/*`, embedding/decay code paths, FTS5 triggers.

---

## 6. Test Strategy

Strict TDD is enabled (`go test ./...`). Test surface (per piece):

### Push side (relation enqueue)
- `JudgeRelation_EnqueuesSyncMutation` — assert exactly one
  `sync_mutations` row inserted with `entity='relation'`, correct
  payload, correct project derived from source observation.
- `JudgeRelation_RejectsCrossProject` — source and target in different
  projects → error, no relation update, no sync mutation.
- `FindCandidates_DoesNotEnqueue` — pending row inserted locally, no
  sync mutation row.

### Pull side (relation apply)
- `ApplyPulledRelationMutation_Inserts` — fresh sync_id, FK satisfied,
  row appears in `memory_relations` with all provenance fields.
- `ApplyPulledRelationMutation_OnConflictUpdates` — same sync_id pulled
  twice, last-write-wins, no duplicate row.
- `ApplyPulledRelationMutation_FKMissing_ReturnsTypedError` — source
  observation absent, returns `ErrRelationFKMissing` (sentinel),
  `memory_relations` untouched.

### Round-trip integration
- `RoundTripJudgment_TwoStores` — store A judges, push to mock cloud,
  store B pulls, assert identical row in B's `memory_relations`.
- `RoundTrip_PreservesProvenance` — `marked_by_*` and `confidence`
  survive the round-trip.

### Annotations
- `MemSearch_AnnotatesConflictsWith_WithTitle` — relation with
  `relation='conflicts_with'` produces
  `conflicts_with: #<id> (<title>)`.
- `MemSearch_AnnotatesSupersedes_WithTitle` — Phase 1 case enriched
  with title.
- `MemSearch_FallsBackToBareID_WhenTargetMissing` — soft-deleted or
  unknown target → no `(...)` suffix.
- `MemSearch_NoRelations_ByteIdentical` — REQ-007 regression intact.

### Server validation
- `HandleMutationPush_RejectsRelationMissingSyncID` — 400 with
  field name in error body.
- `HandleMutationPush_RejectsRelationInvalidVerb` — verb not in locked
  set → 400.
- `HandleMutationPush_AcceptsLegacyObservationWithMissingOptionalFields`
  — backwards compat: legacy lenient path still passes.
- `HandleMutationPush_PartialBatchRejection` — batch of 3 with one bad
  relation entry returns 400 listing the offending index, others NOT
  inserted (atomic — easier to reason about).

### Autosync resilience
- `Pull_FKMissing_DefersAndContinues` — batch of 2: one good, one
  relation with missing source. Cursor advances past both, deferred row
  inserted, no retryable error returned to manager.
- `ReplayDeferred_RetriesUntilSuccess` — deferred relation, source
  observation arrives in next batch, replay applies it, marks
  `applied`.
- `ReplayDeferred_DeadAfter5Retries` — FK never resolves, row flips to
  `dead` with `retry_count=5` after the 5th cycle.
- `Pull_LegacyEntityError_StillHalts` — sanity: a malformed observation
  payload still halts the pull cycle (no regression).

### Boundary regression
- `internal/sync/sync_test.go` and the existing autosync test suite
  pass unmodified (no behavioral change on the existing entity types).

---

## 7. Migration Safety

**Local schema:** ZERO changes to existing tables. The
`memory_relations` table from Phase 1 is reused unchanged. Only one
*new* table is added: `sync_apply_deferred`, idempotent via
`CREATE TABLE IF NOT EXISTS`. No `ALTER` on existing tables. No new
columns.

**Cloud schema:** ZERO changes. `cloud_mutations.entity` is free TEXT;
adding the string `"relation"` requires no DDL.

**Pre-existing relation rows from Phase 1.** All Phase 1 relation rows
were written locally with no `sync_mutations` enqueue. On the day Phase
2 ships, each laptop's `memory_relations` may contain N rows that the
cloud has never seen. **Backfill decision: NO automatic backfill.** The
rationale: backfill would re-emit verdicts authored months ago with
stale provenance; agents and humans expect verdicts to surface near the
time of judgment. Instead:

- New verdicts after Phase 2 deploys flow naturally over the wire.
- Pre-existing local verdicts remain local until re-judged. A re-judge
  through `mem_judge` enqueues fresh sync mutations.
- An optional `engram relations republish --since <date>` CLI command
  is reserved for Phase 3 — explicitly out of scope here.

**Migration test infrastructure.** Phase 1 introduced
`legacyDDLPreMemoryConflictSurfacing` + `newTestStoreWithLegacySchema`
as standing infrastructure for every future schema change. This phase
adds ONE new table (`sync_apply_deferred`) so the pattern applies:
add a new constant `legacyDDLPreCloudSyncRelations` that captures the
post-Phase-1 schema, then test that migrating from it to the Phase 2
schema is byte-preserving and idempotent. Single-table addition keeps
the migration test small.

**Rollback.** Reverting the binary leaves `sync_apply_deferred` empty
on most machines; even when populated, the table is harmless to a
reverted client (unused). On-server: the entity string `"relation"`
remains in `cloud_mutations` but reverted clients ignore it via the
default branch — current behavior. A reverted server (highly unlikely)
would 200-OK relation pushes from Phase 2 clients without validating
them; this is identical to today's behavior for all entities and is
acceptable because reverts are exceptional and deliberate.

---

## 8. Backwards Compatibility

- **Old client → New server.** Old clients never send entity
  `"relation"`. The new server validator routes `session`, `observation`,
  `prompt` through the lenient floor (only `sync_id`/`id` required) —
  identical to today's de-facto behavior. No regressions.
- **New client → Old server.** New clients push `entity='relation'`.
  Old servers do not know the entity. **Failure mode:** old server
  stores the row in `cloud_mutations` (entity is free TEXT) and any
  pulling client that doesn't recognize `relation` returns the default
  unknown-entity error. With Piece 4 in place, new clients
  skip+log+continue; old clients halt their pull (today's behavior).
  This is acceptable: rolling out Piece 4 (client) before relation push
  (server) is the correct deploy order, captured in the design phase.
- **Annotation enrichment.** `(<title>)` is appended to existing marker
  lines. Agents that parse annotations by `supersedes:` prefix still
  match. Agents that parse by exact full line should be reviewed
  (engram-internal agents only — external agents read JSON envelope).
- **`conflicts_with` annotation.** Pure addition; no existing format
  changes.
- **Server validation strictness.** Strict only for the new
  `"relation"` entity. Legacy entities receive a floor that codifies
  current de-facto requirements (`sync_id` non-empty for
  observation/prompt; `id` non-empty for session). Regression test in
  the validation suite replays a snapshot of real Phase 1 payloads to
  ensure 100% pass rate.

---

## 9. Phase 3 Hooks

Decisions in this phase that explicitly enable future work:

| Phase 3 capability | Substrate this phase ships |
|--------------------|----------------------------|
| Cloud admin dashboard for cross-team conflicts | Provenance (`marked_by_*`, `confidence`) crosses the wire and lands server-side in `cloud_mutations.payload`; a dashboard query layer can be added without touching the wire protocol |
| Multi-actor resolution heuristic | Schema permits N rows per pair (no `UNIQUE`); each row carries `marked_by_actor`/`marked_by_kind`; a resolver module can read all rows and emit a winning verdict purely as a read-side function |
| `engram relations republish --since` | The relation payload struct is finalized in this phase; a republish command writes payloads from existing `memory_relations` rows into `sync_mutations` — no new schema |
| Decay activation (`engram review`) | Unchanged — `review_after` already populated since Phase 1 |
| pgvector hybrid retrieval | Unchanged — `embedding*` columns reserved since Phase 1 |
| Cross-project relations | The same-project guard is implemented at one boundary (`JudgeRelation`); relaxing it later is a single-point change plus a project-merge UX |
| Server-side `entity` enum tightening | The validator is the gating function; once all clients ship Phase 2, we can graduate to a Postgres enum |
| Apply-status surfacing in `mem_search` | `sync_apply_deferred` rows expose `apply_status`; a future read can annotate observations whose remote relations are deferred |

---

## 10. Risks (open)

- **Deploy ordering.** New clients must ship the relation entity at the
  same time or after the new server (else they push to a server that
  does not validate, then a different new client pulls a relation the
  validator never blessed). Mitigation: deploy server validation first
  (it accepts everything that legacy clients send), then roll out
  clients with relation push enabled.
- **Cross-project relations in dev databases.** Phase 1 lacked a guard;
  some local DBs may contain them. The new guard prevents new ones; the
  sync layer skips ones it cannot project-attribute. Acceptable.
- **Deferred-retry growth.** If a project pulls thousands of relations
  whose observations never arrive (misconfigured project mapping),
  `sync_apply_deferred` could grow unbounded before reaching `dead`.
  Cap at `retry_count=5` (about a few minutes given backoff) limits
  this; design phase to add a periodic cleanup of `dead` rows older
  than 30 days.
- **Server validation false positives.** A legacy client field we
  consider safe to require may, in fact, be optional in some real
  payload. Mitigation: replay 30 days of production `cloud_mutations`
  payloads through the new validator before merging; any rejection is
  a bug.
- **Annotation format parsing.** Agents (including ours) that grep
  annotations by exact line equality break when titles are appended.
  Mitigation: prefix-based parsing already in use; document the format
  contract in the spec.

---

## Mirror

Engram: `sdd/memory-conflict-surfacing-cloud-sync/proposal` (project: engram)
File: `openspec/changes/memory-conflict-surfacing-cloud-sync/proposal.md`

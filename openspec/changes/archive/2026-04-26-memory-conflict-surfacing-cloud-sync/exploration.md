# Exploration: memory-conflict-surfacing-cloud-sync

Phase: SDD explore (Phase 2 of memory-conflict-surfacing)
Change: memory-conflict-surfacing-cloud-sync
Engram topic_key: `sdd/memory-conflict-surfacing-cloud-sync/explore`
Artifact store: hybrid (engram + openspec mirror)

This file is a brief mirror of the engram exploration artifact. The full version
lives in engram (observation #2668) and contains all option/tradeoff analysis.

---

## Current State (verified against code)

**Cloud mutation transport** is bidirectional and already in production:

- Server-side `cloud_mutations` (Postgres) uses `entity TEXT` — adding the new
  string `"relation"` requires NO DDL.
- Client-side `sync_mutations` (SQLite) is identical in shape; entity constants
  (`SyncEntitySession`, `SyncEntityObservation`, `SyncEntityPrompt`) live in
  `internal/store/store.go`. Adding `SyncEntityRelation = "relation"` is purely
  additive.
- `enqueueSyncMutationTx` (`store.go:4491`) extracts the project from payload
  via `extractProjectFromPayload` and falls back to a session lookup. Relations
  have no native project field — project must be derived from the source
  observation at enqueue time.
- `applyPulledMutationTx` (`store.go:4600`) is a switch on `mutation.Entity`
  with a hard-error default — unknown entities currently halt the pull loop.

**Pull error halt path** (`autosync/manager.go:535`): any error from
`ApplyPulledMutation` returns immediately, the cycle records a failure, and
`sinceSeq` is NOT advanced. Same seq is retried indefinitely on next cycle —
this is the FK-skip problem.

**handleMutationPush** (`cloudserver/mutations.go:63`) validates auth, project
presence, batch size, and pause-gate, but does NOT validate per-entity payload
shape. Garbage payloads are stored as raw JSONB.

**memory_relations** (created in Phase 1) has `sync_id TEXT NOT NULL UNIQUE` —
this is the natural idempotency key. No `project` column. NO
`UNIQUE(source_id,target_id)` — intentional for multi-actor disagreement.

**FindCandidates / JudgeRelation** (`relations.go`) do NOT enqueue sync
mutations today — relations are strictly local in Phase 1.

**mem_search annotation switch** (`mcp.go:841-857`) handles `RelationSupersedes`
and `RelationPending`. `RelationConflictsWith` has NO case. Title is not
included in any annotation; bare `#<id>` only.

## Affected Areas

- `internal/store/store.go` — add `SyncEntityRelation`, `syncRelationPayload`,
  `applyRelationUpsertTx`, extend `applyPulledMutationTx` switch, project
  derivation for relations.
- `internal/store/relations.go` — enqueue sync mutations from `JudgeRelation`
  (and only `JudgeRelation` — pending rows stay local).
- `internal/mcp/mcp.go` — add `RelationConflictsWith` annotation case; add
  `(<title>)` enrichment to all annotation formats.
- `internal/cloud/autosync/manager.go` — replace pull loop hard-halt with
  skip+log+continue (with deferred-retry book-keeping for FK errors).
- `internal/cloud/cloudserver/mutations.go` — per-entity-type payload schema
  validation (strict for new `relation`, lenient for legacy entities).
- `internal/cloud/cloudstore/cloudstore.go` — no schema changes (entity is
  TEXT).

## Open Questions Raised in Exploration

A. Pending relations sync? B. Bidirectional from day 1? C. Cross-project
relations? D. FK-error skip permanently or with deferred retry? E. Strictness
of server validation per entity? F. Title via JOIN at query time vs snapshot?

All six are answered authoritatively in the proposal — see `proposal.md`.

## Migration Posture

NO local schema change is needed. `memory_relations` already exists from
Phase 1; `sync_mutations.entity` is free TEXT. The Phase 1
`legacyDDLPreMemoryConflictSurfacing` migration-test infrastructure is in
place but **not exercised** by this change (no new column adds, no new
tables). Confirmed against `internal/store/store.go:790-826`.

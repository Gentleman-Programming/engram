# Proposal: memory-conflict-audit (Phase 3)

## Intent

Phase 1 shipped local conflict detection + agent-mediated judgment. Phase 2 shipped cloud sync of relations + a deferred-apply queue (`sync_apply_deferred`). Neither phase gave the **maintainer/admin role** any way to inspect, audit, or operate the conflict layer outside the agent conversation.

Phase 3 closes that gap by adding observability and audit surface for admins:

- A `engram conflicts` CLI sub-command tree
- An HTTP API on `engram serve` under `/conflicts/*`
- Store-layer read helpers for relations and the deferred queue
- A scan loop that pre-populates pending relation rows for batch agent review

Engram remains the agent's memory; Phase 3 simply gives the human operator the tools to see and steward what the agent is producing. No end-user-facing surface (those still receive conflicts via agent conversation per Phase 1).

## Scope

### In Scope

1. **`engram conflicts` CLI** (`cmd/engram/conflicts.go`):
   - `list [--project P] [--status S] [--limit N] [--since T]`
   - `show <sync_id>`
   - `stats [--project P]`
   - `scan [--project P] [--apply] [--dry-run] [--max-insert N]` — single-threaded, FTS5 BM25 only
   - `deferred [--status S] [--limit N]`
2. **HTTP API** on local serve (Go 1.22 mux):
   - `GET /conflicts` — list paginated
   - `GET /conflicts/{sync_id}` — show one
   - `GET /conflicts/stats` — aggregate counts
   - `POST /conflicts/scan` — synchronous scan
   - `GET /conflicts/deferred` — list deferred queue
   - `GET /conflicts/deferred/{sync_id}` — show one deferred row
3. **New store methods**:
   - `ListRelations(opts)` + `CountRelations(opts)` in `internal/store/relations.go` (JOIN to observations for `--project`)
   - `ListDeferred(status, limit)` + `GetDeferred(syncID)` in `internal/store/store.go`
   - `ReplayDeferredResult` type, `ReplayDeferred()`, `CountDeferredAndDead()` on `*store.Store` (already in `LocalStore` interface; faked in tests; missing on real store)
4. **New index** (additive migration): `idx_memrel_status_created ON memory_relations(judgment_status, created_at DESC)` to support paginated list-by-status without full scan.
5. **Phase 2 SUGGESTIONS folded in**:
   - Add `Seq` to `[store] ApplyPulledMutation: relation FK miss` log line for forensics
   - `TestApplyPulledRelation_MalformedPayload_StraightToDead` explicit decode-error test
   - Document multi-actor `sync_id` namespace in `docs/PLUGINS.md`

### Out of Scope (explicit Phase 4 deferrals)

- pgvector / sqlite-vec / embedding generation (Phase 4)
- Cloud admin dashboard widget (Phase 4 — separate change)
- Goroutine pool for scan parallelism (Phase 4 if perf becomes pain)
- Resumable scan checkpoint table (Phase 4 — only matters at 100k+ obs)
- `--json` flag for CLI output (no existing engram command has it; consistency)
- Adding `project` column to `memory_relations` (denormalization deferred to Phase 4)

## Capabilities

### New Capabilities
- `conflict-audit-cli`: `engram conflicts` sub-command tree (list, show, stats, scan, deferred)
- `conflict-audit-http`: 6 endpoints under `/conflicts/*` on `engram serve`
- `conflict-scan`: batch FTS5+BM25 candidate scan with `--max-insert` cap and `--dry-run`
- `deferred-queue-ops`: read helpers + replay surface for `sync_apply_deferred`

### Modified Capabilities
- None at spec level. Phase 1/2 capabilities (relation lifecycle, sync) remain unchanged. Phase 3 is purely additive.

## Approach

**Pattern**: thin CLI/HTTP handlers, rich store layer methods. Matches how `cmdStats`, `cmdSync`, and `/sync/status` already work in this codebase.

1. **Store layer first** — TDD `ListRelations`, `CountRelations`, `ListDeferred`, `GetDeferred`, `ReplayDeferred`, `CountDeferredAndDead` against real SQLite. Project filter via JOIN to `observations` at query time (no schema change).
2. **HTTP handlers** — thin wrappers over store methods. Pagination defaults `limit=50`, cap at `500`, override via `?limit=N`. Response shape mirrors `/sync/status` and `/sessions/recent` style: flat JSON with totals.
3. **CLI** — `cmd/engram/conflicts.go` matches `cloud.go` dispatch pattern (sub-switch on `os.Args[2]`). When `--project` omitted → `detectProject(cwd)` matching `cmdSync`. Output style matches `cmdStats` (label-colon aligned).
4. **Scan loop** — single-threaded, iterates observations for project, calls existing `FindCandidates` per row. Pre-checks via `idx_memrel_source` to skip pairs that already have any relation row (regardless of `judgment_status`). `--max-insert N` cap (default 100); above limit prints WARNING and stops, never silently truncates. `--dry-run` always reports without inserting. Synchronous response from `POST /conflicts/scan` (blocks; documented timeout guidance).
5. **Index migration** — append-only N→N+1 migration following Phase 1/2 pattern. Idempotent (`CREATE INDEX IF NOT EXISTS`).
6. **Phase 2 fold-ins** — small surgical edits in existing files (log line, store test, doc section).

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `cmd/engram/main.go` | Modified | Add `case "conflicts":` to top-level dispatch |
| `cmd/engram/conflicts.go` | New | All conflicts CLI sub-commands |
| `cmd/engram/main_test.go` | Modified | CLI tests via `captureOutput` + `withArgs` |
| `internal/server/server.go` | Modified | Register 6 new routes + 6 new handlers |
| `internal/server/server_test.go` | Modified | HTTP tests via httptest |
| `internal/store/relations.go` | Modified | `ListRelations`, `CountRelations` |
| `internal/store/store.go` | Modified | `ReplayDeferredResult`, `ReplayDeferred`, `CountDeferredAndDead`, `ListDeferred`, `GetDeferred`, new index migration, Seq in FK-miss log |
| `internal/store/store_test.go` | Modified | New store-method tests + `TestApplyPulledRelation_MalformedPayload_StraightToDead` |
| `docs/PLUGINS.md` | Modified | Multi-actor `sync_id` namespace section |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Scan flood — re-runs insert thousands of duplicate pending rows | High without guard | `--max-insert N` cap (default 100), pre-check `idx_memrel_source` before `FindCandidates`, `--dry-run` first as documented best practice |
| List-by-status full-scan on large `memory_relations` | Med | New `idx_memrel_status_created` index in migration |
| `POST /conflicts/scan` blocks long enough to time out HTTP client | Med | Document explicit timeout guidance; CLI is the recommended path; sync response stays simple for Phase 3 |
| JOIN-to-observations cost for `--project` filter at scale | Low for Phase 3 | Documented as Phase 4 denormalization candidate (project column on `memory_relations`) |
| `ReplayDeferred` / `CountDeferredAndDead` semantics drift from autosync expectation | Med | Tests pin behavior; `LocalStore` interface in `autosync` package is the contract |
| Multi-actor disagreement rows hidden by simple list | Low | `list` returns ALL rows; `sync_id` always shown; `show <sync_id>` for full provenance |

## Rollback Plan

All changes are additive:

1. **CLI rollback** — remove `case "conflicts":` from `cmd/engram/main.go` switch and delete `cmd/engram/conflicts.go`. Zero impact on other commands.
2. **HTTP rollback** — remove the 6 `s.mux.HandleFunc("X /conflicts/...", ...)` lines from `routes()`. Existing routes unaffected.
3. **Store methods rollback** — new exported methods (`ListRelations`, etc.) can be deleted. They have no callers outside Phase 3 surface.
4. **Index rollback** — `DROP INDEX IF EXISTS idx_memrel_status_created;` in a follow-up migration. No data loss; query plans revert to pre-Phase-3 behavior.
5. **Phase 2 fold-ins rollback** — log-line `Seq` addition, new store test, and PLUGINS.md section are all isolated edits trivially revertable.

No data migration, no destructive schema changes.

## Dependencies

- Phase 2 (`memory-conflict-surfacing-cloud-sync`) ARCHIVED — confirmed in archive report #2685
- Phase 2 substrate verified in exploration: `sync_apply_deferred` table, `applyRelationUpsertTx`, `LocalStore.ReplayDeferred`/`CountDeferredAndDead` interface methods all present on branch `feat/memory-conflict-surfacing-cloud-sync`
- No new external dependencies. Pure stdlib + existing SQLite + existing Go 1.22 mux

## Open Questions

Most architectural forks are pre-decided in proposal context. Remaining minor questions for spec/design:

1. `DeferredRow` exported type shape — include decoded payload struct or expose raw `payload TEXT` and let the admin decode? (Lean: raw string for Phase 3 simplicity; admins can pipe through `jq`.)
2. `engram conflicts deferred --replay` flag — should it call `ReplayDeferred()` directly (same path as autosync pull cycle) or a separate admin replay with bypass-retry-cap semantics? (Lean: same path for Phase 3; separate semantics is Phase 4.)
3. Where exactly does `TestApplyPulledRelation_MalformedPayload_StraightToDead` belong — `internal/store/store_test.go` or `internal/store/sync_apply_test.go`? (Lean: `sync_apply_test.go` since that file already covers pull-side apply behavior.)

## Success Criteria

- [ ] `engram conflicts list` and `show` return correct rows with project + status filters working end-to-end
- [ ] `engram conflicts stats` returns aggregate counts matching SQL ground truth
- [ ] `engram conflicts scan --dry-run` reports candidates without writing; `--apply --max-insert 50` writes ≤50 and warns + stops above cap
- [ ] `engram conflicts deferred` lists `sync_apply_deferred` rows by status; `--replay` invokes existing `ReplayDeferred` path
- [ ] All 6 `/conflicts/*` HTTP endpoints respond with documented JSON shape and stable pagination
- [ ] `idx_memrel_status_created` migration applies cleanly on legacy DBs (N→N+1 test)
- [ ] All Phase 2 SUGGESTIONS folded: `Seq` in FK-miss log, `TestApplyPulledRelation_MalformedPayload_StraightToDead`, PLUGINS.md multi-actor section
- [ ] All existing 19 packages remain GREEN (zero regressions)
- [ ] TDD followed throughout (RED → GREEN → REFACTOR per task)

## Phase 4 Hooks

Decisions in Phase 3 enable later work:

| Phase 4 Capability | Substrate from Phase 3 |
|--------------------|------------------------|
| Cloud admin dashboard widget | Stable `/conflicts/*` JSON shape ready to consume |
| Embedding/semantic conflict detection | `FindCandidates` is single integration point — swap BM25 for vector source |
| `project` column on `memory_relations` | Index choice today (`status, created_at`) does NOT block adding `project` column later; ListRelations becomes faster but signature stable |
| Resumable scan with checkpoint table | Scan loop is single function; checkpoint table can wrap it without API change |
| Goroutine pool for scan parallelism | Single-threaded loop is the simplest baseline; parallelism slots in behind the same CLI/HTTP signature |
| `--json` flag | Output formatting localized to CLI handlers; flag adds without rewrites |

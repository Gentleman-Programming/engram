# Exploration: memory-conflict-audit (Phase 3)

> **Source of truth**: Engram observation `sdd/memory-conflict-audit/explore` (#2697). This file is a brief mirror for filesystem auditability — read engram for the full untruncated content via `mem_get_observation(id: 2697)`.

## Phase 2 Verification (Substrate Ready)

All Phase 2 markers confirmed on `feat/memory-conflict-surfacing-cloud-sync`:

- `SyncEntityRelation = "relation"` — `internal/store/store.go:197`
- `sync_apply_deferred` table — `internal/store/store.go:1011`
- `DeferredCount` + `DeadCount` in `server.SyncStatus` — `internal/server/server.go:46-47`
- `applyRelationUpsertTx` with FK-miss → deferred path — `internal/store/store.go:4737`
- `validateRelationPayload` + `validateMutationEntry` — `internal/cloud/cloudserver/mutations.go:309,338`
- `case SyncEntityRelation:` in `applyPulledMutationTx` — `internal/store/store.go:4688`
- `ReplayDeferred()` and `CountDeferredAndDead()` in `LocalStore` interface — `internal/cloud/autosync/manager.go:90-91` (faked in tests; MUST be implemented on `*store.Store` for Phase 3 compile)

## Key Findings

- **CLI pattern**: top-level switch in `main.go` dispatches to `cmdXxx(cfg)` functions. Cloud sub-commands use a dedicated `cmd/engram/cloud.go` with sub-switch on `os.Args[2]`. New file at `cmd/engram/conflicts.go` matches this pattern.
- **HTTP pattern**: Go 1.22 `METHOD /path` syntax via `s.mux.HandleFunc`. Routes registered in `func (s *Server) routes()`. Response shape: `jsonResponse(w, http.StatusOK, ...)`.
- **`memory_relations` indexes**: `idx_memrel_source`, `idx_memrel_target`, `idx_memrel_supersede`. **Missing**: `(judgment_status, created_at)` — required for paginated list-by-status without full scan.
- **`memory_relations` schema**: NO `project` column — Phase 3 uses JOIN to observations at query time. Denormalization deferred to Phase 4.
- **No `--json` flag** in any existing CLI command — Phase 3 stays consistent (no JSON output).
- **`detectProject(cwd)`** is the existing default-project pattern (used in `cmdSync`).
- **Test pattern**: `captureOutput(t, fn)` + `withArgs(t, args...)` + `withCwd(t, dir)` + real store via `testConfig(t)`. No mock layer.

## Architectural Forks (All Pre-Decided in Proposal Context)

| Fork | Decision |
|------|----------|
| `conflicts.go` location | `cmd/engram/conflicts.go` (matches cloud.go pattern) |
| project filter on relations | JOIN to observations at query time (no schema change) |
| scan strategy | single-threaded loop, FTS5+BM25 only |
| `POST /conflicts/scan` sync vs async | synchronous (blocks); document timeout guidance |
| CLI default project | `detectProject(cwd)` when `--project` omitted |
| pagination | default `limit=50`, max `500` |
| scan flood mitigation | `--max-insert N` cap (default 100), warn + stop above limit, `--dry-run` always safe |
| duplicate pair prevention | pre-check `idx_memrel_source` before `FindCandidates` per observation |

## Affected Files

- `cmd/engram/main.go` — top-level switch
- `cmd/engram/conflicts.go` — NEW (all sub-commands)
- `cmd/engram/main_test.go` — CLI tests
- `internal/server/server.go` — 6 new routes + handlers
- `internal/server/server_test.go` — HTTP tests
- `internal/store/relations.go` — `ListRelations`, `CountRelations`
- `internal/store/store.go` — `ReplayDeferredResult`, `ReplayDeferred`, `CountDeferredAndDead`, `ListDeferred`, `GetDeferred`, new index, Seq in FK-miss log
- `internal/store/store_test.go` (or `sync_apply_test.go`) — new tests including `TestApplyPulledRelation_MalformedPayload_StraightToDead`
- `docs/PLUGINS.md` — multi-actor `sync_id` namespace section

## Risks (Surfaced for Proposal)

1. **CRITICAL** — missing `(judgment_status, created_at)` index → paginated lists full-scan → Phase 3 ships migration
2. Scan flood without cap → `--max-insert` mandatory
3. `ReplayDeferred` + `CountDeferredAndDead` not yet on `*store.Store` → Phase 3 deliverable
4. Project filter JOIN cost at scale → documented Phase 4 hook (project column denormalization)

## Status

Ready for proposal. All forks identified, decisions pre-recorded in orchestrator context.

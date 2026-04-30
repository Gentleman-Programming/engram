# Tasks: memory-conflict-audit (Phase 3)

**Strict TDD active.** Every GREEN task MUST have a preceding RED task (failing test). Phase order: A → B → C → {D ‖ E ‖ F} → G → H.

---

## Phase A — Migration test infra (foundation)

- [x] A.1 [RED] Add `TestMigrate_AddsIdxMemrelStatusCreated` to `internal/store/store_migration_test.go` — open real SQLite, assert `idx_memrel_status_created` does NOT exist before migration stub, confirm test fails. REQ: idx_memrel_status_created migration.
- [x] A.2 Capture post-Phase-2 schema constant `legacyDDLPostMemoryConflictAudit` in `internal/store/store_legacy_ddl_test.go`; verify existing migration tests still pass with `go test ./internal/store/...`. REQ: backwards-compatibility.

---

## Phase B — Schema addition

- [x] B.1 [GREEN] Append `CREATE INDEX IF NOT EXISTS idx_memrel_status_created ON memory_relations(judgment_status, created_at DESC)` at end of `migrate()` in `internal/store/store.go`. REQ: idx_memrel_status_created migration. Acceptance: A.1 goes GREEN; `go test ./internal/store/...` passes.

---

## Phase C — Store layer types and methods

- [x] C.1 [RED] In `internal/store/relations_test.go`, write failing tests for `ListRelations`, `CountRelations`, and `GetRelationStats` — seed a SQLite DB with observations for two projects and mixed-status relations; assert project filter, count accuracy, and stats grouping. REQ: ListRelations/CountRelations, GetRelationStats.
- [x] C.2 [RED] In `internal/store/store_test.go`, write failing tests for `ListDeferred` and `GetDeferred` — seed `sync_apply_deferred`; assert pagination, status filter, not-found error, and decoded payload. REQ: ListDeferred/GetDeferred.
- [x] C.3 [RED] In `internal/store/relations_test.go`, write failing test for `ScanProject` — seed multi-obs DB; assert dry-run inserts 0 rows, apply inserts up to cap, pre-check skips existing pairs. REQ: engram conflicts scan.
- [x] C.4 Add new types to `internal/store/relations.go`: `ListRelationsOptions`, `RelationListItem`, `RelationStats`, `DeferredRow`, `ScanResult`, `ScanOptions`. Add `SkipInsert bool` field to `CandidateOptions` in `internal/store/relations.go` (default false, existing behavior preserved). REQ: store layer types.
- [x] C.5 [GREEN] Implement `ListRelations(opts) ([]RelationListItem, error)` and `CountRelations(opts) (int, error)` in `internal/store/relations.go` — LEFT JOIN observations on src/tgt; project filter `(src.project=? OR tgt.project=?)`; ORDER BY created_at DESC; uses idx_memrel_status_created. REQ: ListRelations/CountRelations. Acceptance: C.1 tests GREEN.
- [x] C.6 [GREEN] Implement `GetRelationStats(project string) (RelationStats, error)` in `internal/store/relations.go` — 2 queries: GROUP BY relation+judgment_status then call existing `CountDeferredAndDead()`. REQ: engram conflicts stats. Acceptance: C.1 stats tests GREEN.
- [x] C.7 [GREEN] Implement `ListDeferred(opts ListDeferredOptions) ([]DeferredRow, error)` and `GetDeferred(syncID string) (DeferredRow, error)` in `internal/store/store.go` — decode payload JSON; set `PayloadValid=false` + preserve `PayloadRaw` on malformed; wrap `sql.ErrNoRows` with formatted message. REQ: ListDeferred/GetDeferred. Acceptance: C.2 tests GREEN.
- [x] C.8 [GREEN] Implement `ScanProject(opts ScanOptions) (ScanResult, error)` in `internal/store/relations.go` — walk observations, call `FindCandidates` with `SkipInsert:true`, pre-check via `SELECT 1 FROM memory_relations WHERE source_id=? AND target_id=? LIMIT 1`, cap-aware insert loop, set `Capped=true` and stop when cap reached. REQ: engram conflicts scan. Acceptance: C.3 tests GREEN.

---

## Phase D — CLI implementation (parallel with E and F)

- [x] D.1 [RED] Create `cmd/engram/conflicts_test.go` with failing tests for all 5 sub-commands (list, show, stats, scan, deferred) using `captureOutput` + `withArgs` helpers — assert output labels, exit codes, and flag behavior. REQ: all conflict-audit-cli requirements.
- [x] D.2 [GREEN] Create `cmd/engram/conflicts.go` with `cmdConflicts(cfg)` dispatcher and sub-commands `cmdConflictsList`, `cmdConflictsShow`, `cmdConflictsStats`, `cmdConflictsScan`, `cmdConflictsDeferred`. Use `resolveConflictsProject(explicit)` calling `detectProject(cwd)`. Output: label-colon aligned columns (no `--json`). REQ: all conflict-audit-cli. Acceptance: D.1 tests GREEN.
- [x] D.3 Add `case "conflicts": cmdConflicts(cfg)` to top-level switch in `cmd/engram/main.go` (alphabetical order, near "context"). Add `conflicts` entry to `printUsage()`. REQ: conflict-audit-cli wiring. Acceptance: `engram conflicts --help` prints usage.

---

## Phase E — HTTP route registration (parallel with D and F)

- [x] E.1 [RED] In `internal/server/server_test.go`, write failing tests for all 6 routes: `GET /conflicts`, `GET /conflicts/stats`, `GET /conflicts/deferred`, `POST /conflicts/scan`, `POST /conflicts/deferred/replay`, `GET /conflicts/{relation_id}` — assert status codes, response shapes, pagination, 404 on missing, 400 on missing project for scan. REQ: all conflict-audit-http requirements.
- [x] E.2 [GREEN] In `internal/server/server.go::routes()`, register the 6 routes in STRICT ORDER (literals before wildcard): `GET /conflicts`, `GET /conflicts/stats`, `GET /conflicts/deferred`, `POST /conflicts/scan`, `POST /conflicts/deferred/replay`, `GET /conflicts/{relation_id}`. Implement handler methods on `*Server`: `handleListConflicts`, `handleConflictsStats`, `handleListDeferred`, `handleScanConflicts`, `handleReplayDeferred`, `handleGetConflict`. Pagination clamps limit silently to 500. REQ: all conflict-audit-http. Acceptance: E.1 tests GREEN.

---

## Phase F — Phase 2 fold-ins (parallel with D and E)

- [x] F.1 In `internal/store/store.go`, add `Seq` field to the `applyRelationUpsertTx` FK miss log line: format `"relation FK miss seq=%d source=%s target=%s"`. REQ: Seq in FK miss log line.
- [x] F.2 [RED+GREEN] In `internal/store/sync_apply_test.go`, add `TestApplyPulledRelation_MalformedPayload_StraightToDead` — seed two cases: (a) payload `"not valid json"`, (b) payload `{"relation_type":"conflicts"}` (missing source_id/target_id); assert `apply_status='dead'` and `retry_count=0` for both. REQ: TestApplyPulledRelation_MalformedPayload_StraightToDead. Acceptance: test GREEN; no behavior change to passing cases.

---

## Phase G — Integration tests (after D + E + F)

- [x] G.1 In `cmd/engram/conflicts_test.go`, add end-to-end integration tests against a real seeded DB: list with project + status filter, show existing + missing, stats totals, scan dry-run (0 inserts), scan apply (inserts up to cap with warning), deferred list + inspect + replay. REQ: all conflict-audit-cli scenarios.
- [x] G.2 In `internal/server/server_test.go`, add HTTP integration tests against a real store: all 6 routes with seeded data, verify `total` field accuracy, 404 body shape, 400 on missing project, cap warning in scan response. REQ: all conflict-audit-http scenarios.
- [x] G.3 In `internal/store/relations_test.go`, add `TestScanProject_CapBehavior` — seed 150 candidate pairs with `max_insert=50`; assert exactly 50 inserted and `Capped=true`; run again with same DB and assert 0 new inserts (pre-check). REQ: scan apply/cap/pre-check scenarios.
- [x] G.4 Run `go test ./...` and `go test -cover ./...`; confirm all 19 pre-existing packages still GREEN (backwards-compatibility). REQ: additive-only surface.

---

## Phase H — Documentation (after G)

- [x] H.1 In `docs/PLUGINS.md`, add section "Multi-actor sync_id namespace" documenting that multiple agents can produce distinct relation rows for the same (source_id, target_id) pair and that annotation parsers may see duplicate `conflicts:` prefix lines. REQ: multi-actor sync_id documentation.
- [x] H.2 In `docs/PLUGINS.md`, add "Admin observability" section covering `engram conflicts` CLI sub-commands and `/conflicts/*` HTTP endpoints for operators. REQ: multi-actor sync_id documentation (admin context).
- [x] H.3 Update `DOCS.md` (or equivalent project root doc): add `/conflicts/*` HTTP endpoint table and `engram conflicts` CLI section with flag summaries. REQ: all conflict-audit-cli + conflict-audit-http.

---

## Dependency summary

```
A → B → C → {D ‖ E ‖ F} → G → H
```

| Phase | Parallel? | Blocks |
|-------|-----------|--------|
| A | no | B |
| B | no | C |
| C | no | D, E, F |
| D | yes (with E, F) | G |
| E | yes (with D, F) | G |
| F | yes (with D, E) | G |
| G | no | H |
| H | no | — |

**Total tasks**: 24
**RED tasks**: A.1, C.1, C.2, C.3, D.1, E.1, F.2 (7 mandatory failing tests)
**GREEN tasks**: B.1, C.5, C.6, C.7, C.8, D.2, E.2 (7 implementation tasks that make RED tests pass)

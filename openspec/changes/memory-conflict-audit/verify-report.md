# Verify Report: memory-conflict-audit (Phase 3)

**Change**: memory-conflict-audit
**Phase**: verify (final pre-archive validation)
**Reads**: spec #2710, design #2713, tasks #2715, apply-progress #2718
**Writes**: this file + engram topic `sdd/memory-conflict-audit/verify-report`
**Date**: 2026-04-27
**Verdict**: READY_TO_ARCHIVE — 0 CRITICAL, 1 WARNING, 4 SUGGESTIONs

---

## Section 1 — REQ-by-REQ verification

| REQ | Status | Code location | Test location | Notes |
|-----|--------|---------------|----------------|-------|
| REQ-001 `engram conflicts list` | PASS | `cmd/engram/conflicts.go:74-172` (cmdConflictsList) | `cmd/engram/conflicts_test.go` (D.1 + G.1) | All 4 scenarios verified. cwd fallback via `resolveConflictsProject` (line 53). |
| REQ-002 `engram conflicts show` | PASS | `cmd/engram/conflicts.go:174-233` (cmdConflictsShow) | `cmd/engram/conflicts_test.go` (TestG1_ConflictsShow_Lifecycle) | Both scenarios (existing + not-found) covered. |
| REQ-003 `engram conflicts stats` | PASS | `cmd/engram/conflicts.go:235-297` (cmdConflictsStats) | `cmd/engram/conflicts_test.go` | Per-relation + per-status + deferred + dead totals. |
| REQ-004 `engram conflicts scan` | PASS | `cmd/engram/conflicts.go:299-385` + `internal/store/relations.go:952-1080` (ScanProject) | `cmd/engram/conflicts_test.go` (TestG1_ConflictsScan_CapWarning) + `internal/store/relations_test.go` (TestScanProject_CapBehavior) | All 4 scenarios. WARNING line printed when `Capped=true`. Default max-insert=100 (line 953). |
| REQ-005 `engram conflicts deferred` | PASS | `cmd/engram/conflicts.go:389+` (cmdConflictsDeferred) | `cmd/engram/conflicts_test.go` (TestG1_DeferredLifecycle) | `--replay` calls `s.ReplayDeferred()` (line 435), same as autosync. `--inspect` prints decoded payload. |
| REQ-006 `GET /conflicts` | PASS | `internal/server/server.go:154` route + 682 handler | `internal/server/server_test.go` (E.1 + G.2) | Pagination clamp 500 verified (clampConflictsLimit line 668). `total` field present. |
| REQ-007 `GET /conflicts/{relation_id}` | PASS | `internal/server/server.go:159` (registered last) + 859 handler | `internal/server/server_test.go` (TestG2_GetConflict_404BodyShape) | 404 returns JSON error body. |
| REQ-008 `GET /conflicts/stats` | PASS | `internal/server/server.go:155` + 733 handler | `internal/server/server_test.go` | Per-status + deferred + dead counts. With/without project. |
| REQ-009 `POST /conflicts/scan` | PASS | `internal/server/server.go:157` + 788 handler | `internal/server/server_test.go` (TestG2_ScanConflicts_*) | Missing project → 400. Cap reached → `warning` field. Sync (no 202). |
| REQ-010 `GET /conflicts/deferred` | PASS | `internal/server/server.go:156` + 753 handler | `internal/server/server_test.go` (TestG2_ListDeferred_StatusFilter) | Pagination + status filter. |
| REQ-011 `POST /conflicts/deferred/replay` | PASS | `internal/server/server.go:158` + 841 handler | `internal/server/server_test.go` (TestG2_ReplayDeferred_ResponseShape) | Calls `store.ReplayDeferred()` (line 842), same path. |
| REQ-012 `ListRelations` + `CountRelations` | PASS | `internal/store/relations.go:792` + `:823` (uses `buildRelationsQuery` w/ JOIN to observations) | `internal/store/relations_test.go` (C.1) | Project filter via `(src.project=? OR tgt.project=?)`. ORDER BY created_at DESC. |
| REQ-013 `ListDeferred` + `GetDeferred` | PASS | `internal/store/store.go:6007` + `:6054` | `internal/store/store_test.go` (C.2) | `GetDeferred` wraps sql.ErrNoRows. PayloadValid=false on malformed (preserves PayloadRaw). |
| REQ-014 `idx_memrel_status_created` migration | PASS | `internal/store/store.go:1029` (CREATE INDEX IF NOT EXISTS) | `internal/store/store_migration_test.go:787` (TestMigrate_AddsIdxMemrelStatusCreated) + `internal/store/store_legacy_ddl_test.go` (legacyDDLPostMemoryConflictAudit baseline) | Idempotent via `IF NOT EXISTS`. Existing rows untouched. |
| REQ-015 Seq in FK miss log + malformed-payload-to-dead | PARTIAL (see WARNING-1) + PASS | `internal/store/store.go:3632` (Seq present); test `internal/store/sync_apply_test.go:473` (TestApplyPulledRelation_MalformedPayload_StraightToDead) | `internal/store/sync_apply_test.go` (both sub-cases) | Spec literal format `"source=%s target=%s"` not honored; actual format uses `entity_key=%s`. Spec scenario only asserts `seq=N` presence — that holds. Malformed-to-dead path implemented via ErrApplyDead branch in ApplyPulledMutation (line 3645) and required-field validation in applyRelationUpsertTx (line 4771). |
| REQ-016 multi-actor sync_id docs | PASS | `docs/PLUGINS.md:170` ("Multi-actor sync_id namespace" section) | n/a | Section exists; describes duplicate `conflicts:` annotations across actors. |
| REQ-017 backwards-compatibility (additive only) | PASS | All new code additive; no changes to existing CLI commands, MCP tool signatures, or schema columns | `go test ./...` (all 19 packages GREEN) | Phase 1 + 2 surfaces unchanged. Older clients without `--project` flag fall back via cwd detection. `CandidateOptions{}` (no SkipInsert) preserves prior behavior (verified by TestFindCandidates_SkipInsert_False_Regression). |

Total: 16 spec REQs + 1 cross-cutting backwards-compat REQ = 17 verified. (Spec lists REQ-001 to REQ-016 by section; REQ-017 here = "additive-only surface" requirement under domain `backwards-compatibility`.)

---

## Section 2 — Task-by-task verification

| Task | Status | Impl file:line | Test file:line |
|------|--------|----------------|-----------------|
| A.1 [RED] TestMigrate_AddsIdxMemrelStatusCreated | DONE | n/a | `internal/store/store_migration_test.go:787` |
| A.2 legacyDDLPostMemoryConflictAudit constant | DONE | `internal/store/store_legacy_ddl_test.go:225+` | n/a (constant) |
| B.1 [GREEN] Append CREATE INDEX | DONE | `internal/store/store.go:1029` | A.1 GREEN |
| C.1 [RED] ListRelations/CountRelations/GetRelationStats tests | DONE | n/a | `internal/store/relations_test.go` (TestListRelations*, TestCountRelations*, TestGetRelationStats*) |
| C.2 [RED] ListDeferred/GetDeferred tests | DONE | n/a | `internal/store/store_test.go` (TestListDeferred*, TestGetDeferred*) |
| C.3 [RED] ScanProject + SkipInsert tests | DONE | n/a | `internal/store/relations_test.go:1457+` (TestFindCandidates_SkipInsert*, TestScanProject*) |
| C.4 New types + SkipInsert field | DONE | `internal/store/relations.go` (ListRelationsOptions, RelationListItem, RelationStats, DeferredRow, ScanResult, ScanOptions, ListDeferredOptions, CandidateOptions.SkipInsert at line 71) | n/a |
| C.5 [GREEN] ListRelations + CountRelations | DONE | `internal/store/relations.go:792` + `:823` | C.1 GREEN |
| C.6 [GREEN] GetRelationStats | DONE | `internal/store/relations.go:886` | C.1 GREEN |
| C.7 [GREEN] ListDeferred + GetDeferred | DONE | `internal/store/store.go:6007` + `:6054` | C.2 GREEN |
| C.8 [GREEN] ScanProject | DONE | `internal/store/relations.go:952` | C.3 GREEN |
| D.1 [RED] CLI sub-command tests | DONE | n/a | `cmd/engram/conflicts_test.go` (15 tests) |
| D.2 [GREEN] cmdConflicts dispatcher + 5 sub-commands | DONE | `cmd/engram/conflicts.go:15+` | D.1 GREEN |
| D.3 main.go switch + printUsage | DONE | `cmd/engram/main.go:565` + `:2114` | TestCmdMain_ConflictsWired GREEN |
| E.1 [RED] HTTP route tests (14) | DONE | n/a | `internal/server/server_test.go` (14 tests) |
| E.2 [GREEN] Routes registered + handlers | DONE | `internal/server/server.go:154-159` (routes) + `:680-880` (handlers) + `internal/store/relations.go:457` (GetRelationByIntID) | E.1 GREEN |
| F.1 Seq in FK miss log | DONE (with format deviation) | `internal/store/store.go:3632` | (covered indirectly by TestApplyPulledMutation_DeferredOnFKMiss) |
| F.2 [RED+GREEN] TestApplyPulledRelation_MalformedPayload_StraightToDead | DONE | `internal/store/store.go:3645+` (ErrApplyDead branch) + `:4762+` (validation in applyRelationUpsertTx, line 4771) | `internal/store/sync_apply_test.go:473` |
| G.1 CLI integration tests | DONE | n/a | `cmd/engram/conflicts_test.go` (TestG1_*) |
| G.2 HTTP integration tests | DONE | n/a | `internal/server/server_test.go` (TestG2_*) |
| G.3 TestScanProject_CapBehavior | DONE | n/a | `internal/store/relations_test.go` |
| G.4 Full suite + coverage | DONE | n/a | `go test ./...` GREEN; store 78.6%, server 54.3%, cmd/engram 76.9% |
| H.1 Multi-actor sync_id docs | DONE (in F.2) | `docs/PLUGINS.md:170` | n/a |
| H.2 Admin Observability section | DONE | `docs/PLUGINS.md:228+` | n/a |
| H.3 DOCS.md conflict audit sections | DONE | `DOCS.md:177` (HTTP) + `DOCS.md:328` (CLI) | docs-alignment tests GREEN |

Total: 24/24 tasks complete. tasks.md: 25 `[x]` markers (24 tasks + 1 dependency-summary entry), 0 `[ ]` markers.

---

## Section 3 — Findings classified

### CRITICAL

None. All blockers cleared.

### WARNING

**WARNING-1**: F.1 FK-miss log line format deviates from spec literal text.

- **Spec REQ-013 says**: `Format: "relation FK miss seq=%d source=%s target=%s"`
- **Actual at `internal/store/store.go:3632`**: `"[store] ApplyPulledMutation: relation FK miss seq=%d entity_key=%s — deferring"`
- **Impact**: low-to-medium. The spec scenario only asserts `seq=N` presence in the log line, and that holds. Operators get the relation `sync_id` (entity_key) instead of source/target observation IDs.
- **Why I am not classifying as CRITICAL**: (a) spec scenarios are GREEN, (b) `entity_key` is a more useful identifier in deferred-queue forensics than source/target IDs (a single sync_id uniquely identifies the failed relation while source/target IDs can recur across many relations), (c) design §8-10 only commits to "Seq added to FK miss log line", not to the specific source/target field names.
- **Recommendation**: either tighten the log line to `"relation FK miss seq=%d entity_key=%s source=%s target=%s — deferring"` (best of both — adds source/target without losing entity_key) in a follow-up, OR amend the spec text to reflect the intentional decision. Either way, NOT a blocker for archive.

### SUGGESTION

**SUGGESTION-1**: F.1 has no dedicated unit test asserting `seq=N` substring presence in the log line. The apply-progress notes that `TestApplyPulledMutation_DeferredOnFKMiss` covers the log output indirectly, but a direct `t.Log` capture + substring assertion would lock the contract. Low priority — current coverage is adequate.

**SUGGESTION-2**: `handleListDeferred` performs a double-query (list + total) where total is currently re-derived from the slice — risk of drift on pagination. Apply-progress already flags this as a Phase 4 hook ("add CountDeferred method"). Defer to Phase 4.

**SUGGESTION-3**: `cmdConflictsShow` does an O(N) linear scan over `memory_relations`. Acceptable for admin-only Phase 3 sizes (apply-progress Risk #3) but tracked for Phase 4 (e.g., `GetRelationByIntID` direct lookup path is already there in relations.go:457 — `cmdConflictsShow` could be refactored to use it, identical to how `handleGetConflict` does).

**SUGGESTION-4**: ScanProject pre-check is bidirectional (`(src,tgt) OR (tgt,src)`) — the implementation deviates from design §7's unidirectional check. This is a CORRECT deviation (apply-progress Risk #4) because (A,B) and (B,A) describe the same conflict pair. Recommend amending design §7 in the archive entry to lock this in as the ratified behavior so the design document doesn't lie about the implementation.

---

## Section 4 — Test execution evidence

Command: `go test ./...`

```
ok  	github.com/Gentleman-Programming/engram/cmd/engram	2.430s
ok  	github.com/Gentleman-Programming/engram/internal/cloud	(cached)
ok  	github.com/Gentleman-Programming/engram/internal/cloud/auth	0.020s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/autosync	0.729s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec	0.011s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/cloudserver	0.055s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/cloudstore	0.019s
?   	github.com/Gentleman-Programming/engram/internal/cloud/constants	[no test files]
ok  	github.com/Gentleman-Programming/engram/internal/cloud/dashboard	0.036s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/remote	0.036s
ok  	github.com/Gentleman-Programming/engram/internal/mcp	2.443s
ok  	github.com/Gentleman-Programming/engram/internal/obsidian	0.211s
ok  	github.com/Gentleman-Programming/engram/internal/project	(cached)
ok  	github.com/Gentleman-Programming/engram/internal/server	0.366s
ok  	github.com/Gentleman-Programming/engram/internal/setup	(cached)
ok  	github.com/Gentleman-Programming/engram/internal/store	1.501s
ok  	github.com/Gentleman-Programming/engram/internal/sync	0.404s
ok  	github.com/Gentleman-Programming/engram/internal/tui	0.138s
ok  	github.com/Gentleman-Programming/engram/internal/version	(cached)
```

All 19 packages GREEN. `[no test files]` on `internal/cloud/constants` is expected (constants-only package).

Coverage (focused scope):
- `internal/store`: 78.6%
- `internal/server`: 54.3%
- `cmd/engram`: 76.9% (via coverage profile)

Coverage interpretation: well above the project's healthy bar for store and CLI. Server at 54.3% is dragged down by infrastructure handlers (CORS, error wrappers, dashboard relay) outside Phase 3 scope. New conflict handlers all have direct integration tests (E.1 + G.2).

---

## Section 5 — Recommendation

**READY_TO_ARCHIVE.**

- 0 CRITICAL findings
- 1 WARNING (log-line text deviation; spec scenarios still GREEN)
- 4 SUGGESTIONs (all Phase-4-deferrable or doc-amendment)
- 24/24 tasks complete
- All 19 packages GREEN
- Phase 1 + Phase 2 regression checks GREEN (mem_save, mem_search, mem_judge, cloud relation sync, pending-annotation byte format)
- Backwards compatibility honored (no schema breaks, no removed flags, no MCP tool signature changes)
- All locked architectural decisions verified:
  - Thin CLI/HTTP + rich store layer ✓
  - Project filter via JOIN (no new column) ✓
  - `idx_memrel_status_created` index present ✓
  - Single-threaded scan + max-insert default 100 ✓
  - Pagination default 50, max 500 ✓
  - HTTP route literals before wildcard (Go 1.22-safe) ✓
  - `FindCandidates.SkipInsert` flag (default false, backwards-compat) ✓
  - Pre-check via memory_relations for dup pair prevention ✓
  - DeferredRow returns decoded payload (PayloadRaw fallback) ✓
  - `--replay` shares ReplayDeferred path with autosync ✓

**Next step**: `sdd-archive memory-conflict-audit`. Recommend folding the WARNING-1 + SUGGESTION-4 into the archive entry as ratified deviations (log line uses `entity_key`, pre-check is bidirectional) so the historical design document stays truthful.

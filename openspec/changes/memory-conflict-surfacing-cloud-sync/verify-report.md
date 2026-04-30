# Verify Report: memory-conflict-surfacing-cloud-sync (Phase 2 — re-verified)

**Change**: memory-conflict-surfacing-cloud-sync
**Phase**: SDD verify (Phase 2 of memory-conflict-surfacing) — RE-VERIFICATION after followup batch
**Date**: 2026-04-26
**Test runner**: `go test ./...`
**Mode**: Strict TDD
**Engram topic_key**: `sdd/memory-conflict-surfacing-cloud-sync/verify-report`
**Mirror**: `openspec/changes/memory-conflict-surfacing-cloud-sync/verify-report.md`

---

## Executive Summary

**Status**: READY_TO_ARCHIVE — both prior WARNINGs resolved.

- 0 CRITICAL / 0 WARNING / 3 SUGGESTION (deferred to Phase 3 by user decision)
- 52/52 tasks + 2 verify-followup fixes complete
- All 19 packages PASS — `go test ./...` GREEN, 0 regressions
- New test `TestJudgeRelation_MissingSource_EmitsWarningLog` PASS
- Phase 1 mem_judge / pending-annotation regression tests still GREEN

**Recommendation**: `READY_TO_ARCHIVE`

---

## Section 1 — Status of previous findings

| Previous finding | Status | Evidence |
|------------------|--------|----------|
| WARNING: REQ-011 missing log emission | **RESOLVED** | `internal/store/relations.go:446-451` — `log.Printf("[store] WARNING: JudgeRelation enqueueing relation %s with project='' (source observation missing locally); server will reject", p.JudgmentID)` gated on `srcProject == ""`. Test: `internal/store/relations_test.go:1089` `TestJudgeRelation_MissingSource_EmitsWarningLog` PASSES. Asserts log contains `"WARNING"`, the relation `sync_id`, and `"project=''"`. |
| WARNING: DOCS.md:124 stale 409 language | **RESOLVED** | `DOCS.md:124` now reads: `"For cloud-enrolled projects: returns 200 and additionally enqueues a session/delete mutation that propagates the deletion to cloud replicas"`. The stale "409 / blocked / local-cloud divergence" language is gone. The remaining `409` on line 123 correctly describes the unrelated "session still has observations" case. No other stale "blocked"/"divergence" mentions found in the session-deletion area (`rg` confirmed all remaining "blocked" hits live in CLI upgrade/diagnosis sections, not session deletion). |
| SUGGESTION 1 — Tighten FK-miss log to include Seq | **DEFERRED to Phase 3** | Per user decision. |
| SUGGESTION 2 — Decode-error → ErrApplyDead unit test | **DEFERRED to Phase 3** | Per user decision. |
| SUGGESTION 3 — Document multi-actor sync_id namespace | **DEFERRED to Phase 3** | Per user decision. |

### REQ-011 status change

| Before | After |
|--------|-------|
| PARTIAL (behavior correct, observability gap) | **PASS** (log emitted at WARNING level with relation sync_id and `project=''` hint) |

---

## Section 2 — New findings (re-verification)

**None.**

The re-verification was scoped narrowly to the followup surfaces:
- `internal/store/relations.go:424-476` — log addition reviewed; tight, gated, no logic change.
- `internal/store/relations_test.go:1087-1130` — RED → GREEN test reviewed; uses `log.SetOutput`/`log.Writer()` capture pattern, restored via `t.Cleanup`. Assertions cover all three required substrings (REQ-011 §"loud failure" intent).
- `DOCS.md:120-124` — text accurately describes post-71fa9fe propagation behavior; no other stale language remains.
- `internal/store/relations.go:6` — `"log"` import added cleanly.
- `internal/store/relations_test.go:3-15` — `"bytes"`, `"log"`, `"strings"` imports already present (no duplicate-import build break).

No new CRITICALs or WARNINGs surfaced.

---

## Section 3 — Final recommendation

**READY_TO_ARCHIVE**

Both prior in-scope and out-of-scope WARNINGs are resolved. Test suite GREEN across all 19 packages. The 3 SUGGESTIONs were explicitly deferred by the user to Phase 3.

Next step: `sdd-archive`.

---

## Section 4 — Test execution

```
$ go test ./internal/store/... -run "TestJudgeRelation_MissingSource_EmitsWarningLog" -v -count=1
=== RUN   TestJudgeRelation_MissingSource_EmitsWarningLog
--- PASS: TestJudgeRelation_MissingSource_EmitsWarningLog (0.01s)
PASS
ok  	github.com/Gentleman-Programming/engram/internal/store	0.015s
```

```
$ go test ./... -count=1
ok  	github.com/Gentleman-Programming/engram/cmd/engram                    2.342s
ok  	github.com/Gentleman-Programming/engram/internal/cloud                0.008s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/auth           0.016s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/autosync       0.730s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec     0.021s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/cloudserver    0.068s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/cloudstore     0.019s
?   	github.com/Gentleman-Programming/engram/internal/cloud/constants      [no test files]
ok  	github.com/Gentleman-Programming/engram/internal/cloud/dashboard      0.038s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/remote         0.033s
ok  	github.com/Gentleman-Programming/engram/internal/mcp                  2.688s
ok  	github.com/Gentleman-Programming/engram/internal/obsidian             0.221s
ok  	github.com/Gentleman-Programming/engram/internal/project              0.746s
ok  	github.com/Gentleman-Programming/engram/internal/server               0.240s
ok  	github.com/Gentleman-Programming/engram/internal/setup                0.193s
ok  	github.com/Gentleman-Programming/engram/internal/store                1.447s
ok  	github.com/Gentleman-Programming/engram/internal/sync                 0.448s
ok  	github.com/Gentleman-Programming/engram/internal/tui                  0.158s
ok  	github.com/Gentleman-Programming/engram/internal/version              0.063s
```

All 19 packages PASS. 0 regressions.

---

## Section 5 — Updated REQ-by-REQ table

| REQ | Status | Code | Tests |
|-----|--------|------|-------|
| REQ-001 push enqueueing | PASS | `internal/store/relations.go:451-476` | `relations_test.go:878,942,979` |
| REQ-002 pull apply + FK-defer | PASS | `store.go:4737-4793`; `:3618-3640` | `sync_apply_test.go:81,114,180` |
| REQ-003 cross-project rejection | PASS | `relations.go:392-394` | `relations_test.go:1002` |
| REQ-004 conflicts_with annotation | PASS | `mcp.go:859-864` | `mcp_test.go:3862,3940` |
| REQ-005 title enrichment | PASS | `relations.go:534-553`, `mcp.go:854-877` | `mcp_test.go:4017,4100` |
| REQ-006 server validation | PASS | `cloudserver/mutations.go:172-193,295-345` | `mutations_test.go:899,935,1105,1446` |
| REQ-007 deferred retry | PASS | `store.go:5854-5933`; table `:1011-1023` | `sync_apply_test.go:238,317,354`; `manager_test.go:899,939,985` |
| REQ-008 backwards compat | PASS | `mutations.go:332-334`; `store.go:3637-3639` | `mutations_test.go:1105,1527` |
| REQ-009 idempotency by sync_id | PASS | `store.go:4757-4783` | `sync_apply_test.go:180,469`; `relation_sync_integration_test.go:312` |
| REQ-010 migration safety | PASS | `store.go:1010-1023` | `store_migration_test.go:520,611` |
| REQ-011 empty-project loud | **PASS** (was PARTIAL) | `relations.go:446-451`; `mutations.go:309-326` | `relations_test.go:1049,1089` |
| REQ-012 annotation contract | PASS | `mcp.go:841-849`; `docs/PLUGINS.md:164-183` | `mcp_test.go:4184` |

---

## Section 6 — Files changed in re-verification scope

| File | Change |
|------|--------|
| `internal/store/relations.go` | `"log"` import added; `log.Printf` WARNING gated on `srcProject == ""` (lines 446-451) |
| `internal/store/relations_test.go` | `"bytes"`, `"log"`, `"strings"` imports; `TestJudgeRelation_MissingSource_EmitsWarningLog` (line 1089) |
| `DOCS.md` | Line 124 rewritten: stale 409/blocked/divergence language replaced with accurate 200+propagation description |

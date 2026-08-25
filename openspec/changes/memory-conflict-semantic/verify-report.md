# Verify Report: memory-conflict-semantic (Phase 4)

**Change**: memory-conflict-semantic
**Mode**: Strict TDD
**Artifact store**: hybrid (engram + openspec)
**Verdict**: PASS WITH WARNINGS — READY_TO_ARCHIVE

---

## Executive Summary

All 30 tasks complete, 16 spec REQs fully implemented, full `go test ./...` GREEN across 20 packages, zero Phase 1/2/3 regressions, build clean. One pre-existing stale doc reference in `docs/AGENT-SETUP.md:50` (says "12 agent-facing tools") survives — flagged as WARNING, not blocking.

- 0 CRITICAL
- 1 WARNING
- 2 SUGGESTION

---

## Section 1: REQ-by-REQ verification

| REQ | Status | Code location | Test location | Notes |
|-----|--------|---------------|---------------|-------|
| REQ-001 AgentRunner interface + Verdict struct | ✅ COMPLIANT | `internal/llm/runner.go:22-51` | `internal/llm/runner_test.go` (TestAgentRunner_HappyPath, TestAgentRunner_ErrorPropagation, TestErrorSentinels, TestVerdict_ZeroValueSafe) | Interface single method `Compare`; Verdict has all 5 fields; 5 error sentinels |
| REQ-002 ClaudeRunner parses Claude CLI envelope | ✅ COMPLIANT | `internal/llm/claude.go` | `internal/llm/claude_test.go` (8 tests: GoldenEnvelope, FenceStrippingWith/Without, InvalidInner/OuterJSON, UnknownRelation, CLIError, MissingBinary) | Strips fences via `fenceRE`; parses `.result`; injectable `runCLI` |
| REQ-003 OpenCodeRunner parses NDJSON event stream | ✅ COMPLIANT | `internal/llm/opencode.go` | `internal/llm/opencode_test.go` (7 tests: GoldenNDJSON, MissingTextEvent, MalformedLine, MultipleTextEvents, InvalidInnerJSON, CLIError, UnknownRelation) | Uses `bufio.Scanner`; extracts `type=="text"` events |
| REQ-004 Runner factory selects via ENGRAM_AGENT_CLI | ✅ COMPLIANT | `internal/llm/factory.go:28-49` | `internal/llm/factory_test.go` (5 tests) | "claude"→ClaudeRunner, "opencode"→OpenCodeRunner; `""`/unknown error names ENGRAM_AGENT_CLI |
| REQ-005 --semantic flag on conflicts scan | ✅ COMPLIANT | `cmd/engram/conflicts.go:309,340-341,402-439` | `cmd/engram/conflicts_test.go` (TestCmdConflictsScan_NoSemanticFlag, TestCmdConflictsScan_SemanticFlagWithEnv, TestCmdConflictsScan_SemanticFlagNoEnv, TestResolveAgentRunner_*) | --semantic absent → Phase 3 unchanged path; cost prompt + ENGRAM_AGENT_CLI fast-fail; --yes skips prompt |
| REQ-006 --concurrency N flag (1-20, default 5) | ✅ COMPLIANT | `cmd/engram/conflicts.go:310,342-348,376-380` | `cmd/engram/conflicts_test.go` (D.2 tests cover validation paths) | Range validated up-front before any work |
| REQ-007 --timeout-per-call N flag (default 60s) | ✅ COMPLIANT | `cmd/engram/conflicts.go:311,349-355,435` | `internal/store/scan_semantic_test.go` (TestScanProject_Semantic_TimeoutCounted) | `context.WithTimeout` per pair; timeouts → SemanticErrors++; scan continues |
| REQ-008 --max-semantic N cap (default 100) | ✅ COMPLIANT | `cmd/engram/conflicts.go:313,358-364,436`; `internal/store/relations.go:1261-1284` | `internal/store/scan_semantic_test.go` (TestScanProject_Semantic_MaxSemanticCap) | Cap at pair collection; sets `result.Capped=true` |
| REQ-009 JudgeBySemantic store method | ✅ COMPLIANT | `internal/store/relations.go:725-810` | `internal/store/judge_by_semantic_test.go` (6 tests: SystemProvenance, UpsertIdempotency, NotConflictNoOp, ValidationErrors×5, CrossProjectRejected, AllValidRelations×5) | UPSERT semantics; `marked_by_kind="system"`, `marked_by_actor="engram"`; not_conflict no-op |
| REQ-010 ScanResult extended with semantic counters | ✅ COMPLIANT | `internal/store/relations.go:154-156` | `internal/store/scan_semantic_test.go` (all 8 scan_semantic tests check counters); `internal/server/server_test.go` (TestHandleScanConflicts_*) | JSON tags `semantic_judged`/`semantic_skipped`/`semantic_errors`; zero-value safe |
| REQ-011 mem_compare MCP tool | ✅ COMPLIANT | `internal/mcp/mcp.go:99,747-802,1684-1753` | `internal/mcp/mcp_compare_test.go` (8 tests: HappyPath, NotConflict_NoRow, MissingMemoryIDB, InvalidRelation, NonExistentObservation, Idempotency, ProfileAgent, ModelOptional) | Schema: memory_id_a/b int, relation enum, confidence float, reasoning string, model optional; resolves int IDs to sync_ids; calls JudgeBySemantic |
| REQ-012 per-pair failure isolation | ✅ COMPLIANT | `internal/store/relations.go:1361-1407` | `internal/store/scan_semantic_test.go` (TestScanProject_Semantic_ErrorIsolation) | panic recover; structured warn log; SemanticErrors++ on err/timeout; scan continues |
| REQ-013 conflict-scan modified | ✅ COMPLIANT | `cmd/engram/conflicts.go:300-470` | All Phase 3 test suite still GREEN + new D.2 semantic-off cases | Phase 3 dry-run/apply/max-insert/already-related logic preserved |
| REQ-014 POST /conflicts/scan accepts semantic params | ✅ COMPLIANT | `internal/server/server.go:817-933` (semantic body fields, defaults via `*int`, runner resolution, response counters) | `internal/server/server_test.go` (5 tests: SemanticFalse_CountersZero, SemanticTrue_NoFactory_500, SemanticTrue_WithMockRunner, InvalidConcurrency_400, InvalidTimeout_400) | `*int` distinguishes absent from explicit zero; concurrency [1,20] + timeout [1,600] validated; counters always present in response |
| REQ-015 backwards-compatibility — Phase 3 unchanged | ✅ COMPLIANT | `internal/store/relations.go:1152-1158, 1283-1330` (Semantic=false branches) | Full `go test ./...` GREEN (Phase 1/2/3 suites unchanged) | ScanOptions zero-value `Semantic=false` → Phase 3 codepath; no LLM calls |
| REQ-016 existing MCP tools unaffected | ✅ COMPLIANT | `internal/mcp/mcp.go` (mem_save, mem_search, mem_judge etc. unchanged) | `internal/mcp/mcp_test.go` (all pre-Phase-4 tests still GREEN, tool counts updated 17→18) | mem_compare additive only |

**Compliance summary**: 16/16 REQs COMPLIANT.

---

## Section 2: Task-by-task verification

| Task | Status | File:line where impl | File:line where test |
|------|--------|----------------------|----------------------|
| A.1 RED runner_test.go | ✅ | n/a (RED-first test file) | `internal/llm/runner_test.go:1-` |
| A.2 GREEN runner.go | ✅ | `internal/llm/runner.go:22-73` | `internal/llm/runner_test.go` |
| A.3 RED prompt_test.go | ✅ | n/a | `internal/llm/prompt_test.go:1-` |
| A.4 GREEN prompt.go | ✅ | `internal/llm/prompt.go` | `internal/llm/prompt_test.go` |
| A.5 RED cost_test.go | ✅ | n/a | `internal/llm/cost_test.go:1-` |
| A.6 GREEN cost.go | ✅ | `internal/llm/cost.go` (constants 300/50, EstimateScanCost) | `internal/llm/cost_test.go` |
| B.1 RED claude_test.go | ✅ | n/a | `internal/llm/claude_test.go:1-` |
| B.2 GREEN claude.go | ✅ | `internal/llm/claude.go` (ClaudeRunner, parseClaudeEnvelope, fenceRE) | `internal/llm/claude_test.go` (8 tests) |
| B.3 RED opencode_test.go | ✅ | n/a | `internal/llm/opencode_test.go:1-` |
| B.4 GREEN opencode.go | ✅ | `internal/llm/opencode.go` (OpenCodeRunner, parseOpenCodeNDJSON) | `internal/llm/opencode_test.go` (7 tests) |
| B.5 RED factory_test.go | ✅ | n/a | `internal/llm/factory_test.go:1-` |
| B.6 GREEN factory.go | ✅ | `internal/llm/factory.go:28-49` | `internal/llm/factory_test.go` (5 tests) |
| C.1 store/runner.go (duck-type) | ✅ | `internal/store/runner.go:5-25` | structural; covered by C.2/C.5 |
| C.2 RED judge_by_semantic_test.go | ✅ | n/a | `internal/store/judge_by_semantic_test.go` |
| C.3 GREEN JudgeBySemantic | ✅ | `internal/store/relations.go:725-810`; `validateCrossProjectGuard:694-708` | `internal/store/judge_by_semantic_test.go` (6 tests) |
| C.4 ScanOptions+ScanResult fields | ✅ | `internal/store/relations.go:142-197` (ScanResult, ObservationSnippet, ScanOptions) | covered by C.5 + scan_semantic tests |
| C.5 RED scan_semantic_test.go | ✅ | n/a | `internal/store/scan_semantic_test.go` (8 tests) |
| C.6 GREEN ScanProject worker pool | ✅ | `internal/store/relations.go:1150-1421` | `internal/store/scan_semantic_test.go` |
| D.1 cmd/engram/main.go agentRunnerFactory | ✅ | `cmd/engram/main.go` (`agentRunnerFactory = defaultAgentRunnerFactory`) + `cmd/engram/llm.go:14-50` | covered by D.2 |
| D.2 RED conflicts_test.go (7 tests) | ✅ | n/a | `cmd/engram/conflicts_test.go` (mockSemanticRunner, stubAgentRunnerFactory + 7 tests) |
| D.3 GREEN conflicts.go semantic flags | ✅ | `cmd/engram/conflicts.go:300-470` | `cmd/engram/conflicts_test.go` |
| E.1 store + cmd/engram tests GREEN | ✅ | n/a (gate) | full suite |
| E.2 full `go test ./...` GREEN | ✅ | n/a (gate) | full suite |
| F.1 RED server_test.go (5 tests) | ✅ | n/a | `internal/server/server_test.go` |
| F.2 GREEN server.go semantic params | ✅ | `internal/server/server.go:69-104, 817-933`; `cmd/engram/main.go` (SetRunnerFactory/SetPromptBuilder wiring) | `internal/server/server_test.go` (5 tests) |
| G.1 RED mcp_compare_test.go (8 tests) | ✅ | n/a | `internal/mcp/mcp_compare_test.go` (8 tests) |
| G.2 GREEN mem_compare in mcp.go | ✅ | `internal/mcp/mcp.go:99,747-802,1684-1753` | `internal/mcp/mcp_compare_test.go` |
| H.1 docs/PLUGINS.md updated | ✅ | `docs/PLUGINS.md:85,267-310` (tool count 18/14, semantic flags + mem_compare reference) | n/a (docs) |
| H.2 DOCS.md + README + ARCHITECTURE + AGENT-SETUP | ⚠️ | `DOCS.md:570,681`; `README.md:79`; `docs/ARCHITECTURE.md:128`; `docs/AGENT-SETUP.md:61,102,454` | `docs/AGENT-SETUP.md:50` still has stale "12 agent-facing tools" — see WARNING-1 |
| H.3 final regression `go test -cover ./...` | ✅ | n/a (gate) | All 20 packages GREEN; coverage llm 82.1%, store 79.2%, server 57.5%, mcp 93.5% |

**30/30 tasks marked complete in tasks.md and verified against code.** One stale doc string survived in `docs/AGENT-SETUP.md:50` despite the broader docs sweep.

---

## Section 3: Findings

### CRITICAL (must fix before archive)

None.

### WARNING (should fix soon, not blocking)

1. **WARNING-1 — Stale tool count in `docs/AGENT-SETUP.md:50`**
   - The file still reads `(12 agent-facing tools)` while the rest of the docs (and adjacent lines 61, 102, 454-458 in the same file) correctly use `18 tools`. This number was already stale from Phase 1 (it should have been 13, then 14 after Phase 4). The orchestrator briefing flagged this as a known stale string.
   - Misleading to plugin/setup readers but does not affect runtime behavior or contracts. Out of strict spec scope (REQ-016 covers MCP tool counts via mcp_test.go which IS green).
   - Suggested fix: change `12 agent-facing tools` → `14 agent-facing tools` in `docs/AGENT-SETUP.md:50`.

### SUGGESTION (nice-to-have)

1. **SUGGESTION-1 — Coverage on `internal/server` is 57.5%**
   - Below the other Phase 4 packages (llm 82.1%, store 79.2%, mcp 93.5%). Most uncovered branches are pre-existing (cloud/auth handlers, dashboards). Phase 4's semantic params block is fully covered by the 5 new server tests, so the gap is not a Phase 4 regression.
   - Consider adding HTTP-level coverage for `/conflicts/list`, `/conflicts/show`, `/conflicts/stats` happy paths in a future cleanup.

2. **SUGGESTION-2 — `agentRunnerFactory` indirection is non-obvious**
   - `cmd/engram/main.go` exposes `agentRunnerFactory = defaultAgentRunnerFactory` (a delegate) instead of `llm.NewRunner` directly. This is necessary because the factory must return `store.SemanticRunner` (not `llm.AgentRunner`) to keep the store package decoupled from llm — see `cmd/engram/llm.go:18-24`.
   - Already documented in `apply-progress` Deviations from Design. Not a defect; future contributors might benefit from a one-line code comment near `agentRunnerFactory` in main.go pointing to the adapter rationale.

---

## Section 4: Test execution evidence

### Full `go test ./...` (clean, no cache)

```
ok  	github.com/Gentleman-Programming/engram/cmd/engram	2.604s
ok  	github.com/Gentleman-Programming/engram/internal/cloud	0.006s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/auth	0.014s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/autosync	0.740s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec	0.011s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/cloudserver	0.054s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/cloudstore	0.017s
?   	github.com/Gentleman-Programming/engram/internal/cloud/constants	[no test files]
ok  	github.com/Gentleman-Programming/engram/internal/cloud/dashboard	0.041s
ok  	github.com/Gentleman-Programming/engram/internal/cloud/remote	0.039s
ok  	github.com/Gentleman-Programming/engram/internal/llm	0.010s
ok  	github.com/Gentleman-Programming/engram/internal/mcp	2.857s
ok  	github.com/Gentleman-Programming/engram/internal/obsidian	0.230s
ok  	github.com/Gentleman-Programming/engram/internal/project	0.814s
ok  	github.com/Gentleman-Programming/engram/internal/server	0.503s
ok  	github.com/Gentleman-Programming/engram/internal/setup	0.189s
ok  	github.com/Gentleman-Programming/engram/internal/store	1.902s
ok  	github.com/Gentleman-Programming/engram/internal/sync	0.509s
ok  	github.com/Gentleman-Programming/engram/internal/tui	0.186s
ok  	github.com/Gentleman-Programming/engram/internal/version	0.062s
```

Exit code: 0. All 20 packages GREEN. `internal/cloud/constants` has no test files (pre-existing).

### Coverage on Phase 4 packages

```
internal/llm     82.1%
internal/store   79.2%
internal/server  57.5%
internal/mcp     93.5%
cmd/engram       (no per-package %, large CLI surface)
```

### Build

```
go build ./...   → exit 0 (clean)
```

### Strict TDD compliance

All Phase 4 tasks marked RED in apply-progress wrote a failing test first (verified compile-time errors referencing undefined types, then GREEN after implementation). 73 tests added across 12 test files, 0 failures.

### Phase 1/2/3 regression

- Phase 1: `internal/store/relations_test.go` (mem_judge, FindCandidates) — GREEN.
- Phase 2: `internal/store/sync_apply_test.go`, `internal/cloud/autosync` — GREEN.
- Phase 3: `cmd/engram/conflicts_test.go` (list/show/stats/scan/deferred), `internal/server/server_test.go` (/conflicts/* routes) — GREEN. The Phase 4 Semantic=false branch is verified via `TestCmdConflictsScan_NoSemanticFlag` and `TestHandleScanConflicts_SemanticFalse_CountersZero`.

---

## Section 5: Recommendation

**READY_TO_ARCHIVE.**

- All 16 spec REQs COMPLIANT with PASSING tests.
- All 30 tasks marked complete and verified in code.
- Full `go test ./...` GREEN (20 packages, exit 0).
- Build clean.
- 1 WARNING (stale doc string in AGENT-SETUP.md:50, predates Phase 4) is non-blocking and out of strict Phase 4 scope.
- Strict TDD compliance verified (RED-before-GREEN per apply-progress).

Recommended next phase: **`sdd-archive`**. The lone WARNING (AGENT-SETUP.md:50) can be folded into a follow-up doc cleanup PR or fixed inline before archiving — orchestrator's call.

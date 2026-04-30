# Tasks: memory-conflict-semantic (Phase 4)

**Change**: memory-conflict-semantic
**Artifact store**: hybrid (engram + openspec)
**TDD mode**: strict (RED → GREEN per task)

---

## Phase A: internal/llm/ foundation — interfaces, types, errors (sequential first)

- [x] A.1 RED — Write `internal/llm/runner_test.go`: test `AgentRunner` interface via a fake impl; assert `Compare(ctx, prompt)` returns `Verdict` with all fields; assert error propagation returns zero-value Verdict. [REQ: AgentRunner interface + Verdict struct]
- [x] A.2 GREEN — Create `internal/llm/runner.go`: define `AgentRunner` interface, `Verdict` struct (Relation, Confidence, Reasoning, Model, DurationMS), and error sentinels (`ErrCLINotInstalled`, `ErrCLIAuthMissing`, `ErrTimeout`, `ErrInvalidJSON`, `ErrUnknownRelation`). [REQ: AgentRunner interface]
- [x] A.3 RED — Write `internal/llm/prompt_test.go`: golden snapshot test of `BuildPrompt` for a fixed observation pair. [REQ: locked prompt template]
- [x] A.4 GREEN — Create `internal/llm/prompt.go`: locked canonical prompt constant + `BuildPrompt(a, b) string`. [REQ: locked prompt template]
- [x] A.5 RED — Write `internal/llm/cost_test.go`: unit tests for `EstimateScanCost(pairCount int)` — math correctness, per-pair constants (300 in, 50 out). [REQ: cost warning]
- [x] A.6 GREEN — Create `internal/llm/cost.go`: `EstimatedInputTokensPerPair=300`, `EstimatedOutputTokensPerPair=50`, `EstimateScanCost`. [REQ: cost warning]

---

## Phase B: ClaudeRunner + OpenCodeRunner + factory (sequential after A)

- [x] B.1 RED — Write `internal/llm/claude_test.go`: table-driven tests via fake `runCLI` — clean JSON envelope, fence-wrapped JSON, malformed inner JSON, unknown relation → `ErrUnknownRelation`. [REQ: ClaudeRunner]
- [x] B.2 GREEN — Create `internal/llm/claude.go`: `ClaudeRunner` with injectable `runCLI`; invokes `claude -p --output-format json --model haiku --max-turns 1`; strips ```fences```; parses `.result`; captures `Model` + `DurationMS`. [REQ: ClaudeRunner]
- [x] B.3 RED — Write `internal/llm/opencode_test.go`: table-driven tests via fake `runCLI` — valid NDJSON with `type="text"` event, missing text event → error, malformed part.text → error. [REQ: OpenCodeRunner]
- [x] B.4 GREEN — Create `internal/llm/opencode.go`: `OpenCodeRunner` with injectable `runCLI`; invokes `opencode run --format json --pure`; scans NDJSON; picks `type=="text"` event; extracts `.part.text`; captures `Model` + `DurationMS`. [REQ: OpenCodeRunner]
- [x] B.5 RED — Write `internal/llm/factory_test.go`: `"claude"` → `*ClaudeRunner`; `"opencode"` → `*OpenCodeRunner`; `""` → error naming `ENGRAM_AGENT_CLI`. [REQ: Runner factory]
- [x] B.6 GREEN — Create `internal/llm/factory.go`: `NewRunner(name string) (AgentRunner, error)`. [REQ: Runner factory]

---

## Phase C: Store layer extension (parallel with D)

- [x] C.1 Create `internal/store/runner.go`: local duck-typed `AgentRunner` mirror interface (`Compare(ctx, prompt) (Verdict, error)`) — avoids `store→llm` import cycle. [Design: cross-package boundary]
- [x] C.2 RED — Write `internal/store/judge_by_semantic_test.go`: real SQLite in-memory tests — insert happy path with system provenance; UPSERT idempotency on same `(source_id, target_id)` pair; `not_conflict` is no-op (zero rows); validation error on zero-value SourceID. [REQ: JudgeBySemantic]
- [x] C.3 GREEN — Extend `internal/store/relations.go`: add `JudgeBySemanticParams` struct and `(s *Store) JudgeBySemantic(p JudgeBySemanticParams) (string, error)` — UPSERT into `memory_relations` with `marked_by_kind="system"`, `marked_by_actor="engram"`, `marked_by_model`. Extract shared cross-project guard helper reused from `JudgeRelation`. [REQ: JudgeBySemantic]
- [x] C.4 Extend `internal/store/relations.go`: add semantic fields to `ScanOptions` (`Semantic bool`, `Concurrency int`, `TimeoutPerCall time.Duration`, `MaxSemantic int`, `Runner AgentRunnerLike`) and to `ScanResult` (`SemanticJudged int`, `SemanticSkipped int`, `SemanticErrors int`). [REQ: ScanResult counters]
- [x] C.5 RED — Write `internal/store/scan_semantic_test.go`: fake-runner driven `ScanProject` with `Semantic=true` — counter accuracy (judged/skipped/errors); per-pair error isolation (one error does not abort scan); `--max-semantic` cap limits LLM calls; timeout-exceeded pair → `SemanticErrors++`. [REQ: per-pair failure isolation, max-semantic cap, timeout]
- [x] C.6 GREEN — Extend `ScanProject` in `internal/store/relations.go`: when `ScanOptions.Semantic=true`, after FTS5 collection spawn `errgroup.SetLimit(Concurrency)` worker pool; per-pair `context.WithTimeout(TimeoutPerCall)`; classify verdict into judged/skipped/errors; call `JudgeBySemantic` for non-`not_conflict` verdicts. [REQ: worker pool, per-pair isolation]

---

## Phase D: CLI factory wiring (parallel with C)

- [x] D.1 Modify `cmd/engram/main.go`: add package-level `agentRunnerFactory = llm.NewRunner` var alongside existing `storeNew`, `newHTTPServer` — injectable for tests. [Design: agentRunnerFactory injection]
- [x] D.2 RED — Extend `cmd/engram/conflicts_test.go`: `--semantic` absent → Phase 3 path unchanged (no LLM calls); `--semantic --yes` with fake runner factory → calls runner; `ENGRAM_AGENT_CLI` unset → fast error before any LLM calls; out-of-range `--concurrency 0` → error. [REQ: --semantic flag, ENGRAM_AGENT_CLI guard]
- [x] D.3 GREEN — Extend `cmd/engram/conflicts.go`: add `--semantic`, `--concurrency` (default 5, max 20), `--timeout-per-call` (default 60), `--yes`, `--max-semantic` (default 100) flags to `cmdConflictsScan`; validate concurrency range before any work; read `ENGRAM_AGENT_CLI` and call `agentRunnerFactory`; print cost estimate; prompt `Continue? [y/N]` (skipped on `--yes`); wire flags into `ScanOptions`. [REQ: semantic-scan-cli flags]

---

## Phase E: Integration gate (sequential — C+D must be GREEN)

- [x] E.1 Run `go test ./internal/store/... ./cmd/engram/...` — all new and existing tests GREEN. Fix any integration seams between C and D outputs. [REQ: backwards-compatibility]
- [x] E.2 Run `go test ./...` — full suite must be GREEN before proceeding to F/G. [REQ: Phase 3 surfaces unchanged]

---

## Phase F: HTTP API extension (parallel with G)

- [x] F.1 RED — Extend `internal/server/server_test.go`: `POST /conflicts/scan` with `semantic=false` → `semantic_judged=0`, `semantic_skipped=0`, `semantic_errors=0` in response; `semantic=true` with injected fake runner → non-zero judged counter and verdict rows in DB; omitted fields → defaults applied. [REQ: POST /conflicts/scan semantic params]
- [x] F.2 GREEN — Extend `internal/server/server.go`: extend `POST /conflicts/scan` body parser to accept `semantic`, `concurrency`, `timeout_per_call_seconds`, `max_semantic`; apply defaults matching CLI; forward to `ScanOptions`; add `semantic_judged`, `semantic_skipped`, `semantic_errors` to JSON response. [REQ: POST /conflicts/scan]

---

## Phase G: mem_compare MCP tool (parallel with F)

- [x] G.1 RED — Extend `internal/mcp/mcp_test.go`: `mem_compare` happy path inserts row with `marked_by_actor="engram"`; `not_conflict` returns success with no row; missing `memory_id_b` → MCP error; `memory_id_a=9999` not found → descriptive error. [REQ: mem_compare MCP tool]
- [x] G.2 GREEN — Extend `internal/mcp/mcp.go`: register `mem_compare` tool with input schema (memory_id_a int, memory_id_b int, relation enum, confidence float, reasoning ≤200, model optional); resolve observation IDs → sync_ids; call `Store.JudgeBySemantic`; return `{sync_id}`. [REQ: mem_compare MCP tool]

---

## Phase H: Documentation (sequential after F+G)

- [x] H.1 Update `docs/PLUGINS.md`: add `ENGRAM_AGENT_CLI` env var contract (`claude` | `opencode`); `--semantic` UX and cost warning format; `mem_compare` tool reference with schema and example. [Design: docs]
- [x] H.2 Update `DOCS.md`: reference `mem_compare` in the MCP tools table; add semantic scan sub-section under `conflicts scan` command reference. [Design: docs]
- [x] H.3 Final regression: `go test ./... -cover` — all packages GREEN; no Phase 3 regressions. [REQ: Phase 3 surfaces unchanged]

---

## Parallelism Map

```
A (serial) → B (serial) → C ‖ D → E (gate) → F ‖ G → H
```

| Phase | Files Touched | Parallel With |
|-------|--------------|---------------|
| A | internal/llm/ (new: runner.go, prompt.go, cost.go + tests) | none |
| B | internal/llm/ (new: claude.go, opencode.go, factory.go + tests) | none |
| C | internal/store/relations.go, runner.go (new), *_test.go | D |
| D | cmd/engram/conflicts.go, main.go, conflicts_test.go | C |
| E | integration gate — run tests only | none |
| F | internal/server/server.go, server_test.go | G |
| G | internal/mcp/mcp.go, mcp_test.go | F |
| H | docs/PLUGINS.md, DOCS.md | none |

**Total: 27 tasks across 8 phases.**

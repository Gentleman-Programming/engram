# Proposal: memory-conflict-semantic (Phase 4)

## Intent

Phase 1 shipped local conflict detection + agent-mediated judgment. Phase 2 shipped cloud sync of relations + the `sync_apply_deferred` queue. Phase 3 (`memory-conflict-audit`) shipped the maintainer-facing CLI/HTTP observability surface and the `engram conflicts scan` loop using **FTS5+BM25 lexical detection**.

Lexical detection misses the ~20% of conflicts that are **vocabulary-different but semantically equivalent or contradictory**. Verified examples:

- *"Use Hexagonal Architecture"* vs *"Use Ports and Adapters"* — same idea, different vocabulary. FTS5 sees no overlap.
- *"Use Clean Architecture"* vs *"Use Hexagonal Architecture"* — conceptually compatible. Verified live: LLM verdict `compatible` 0.95.
- *"Use Postgres"* vs *"We migrated to MongoDB"* — newer supersedes older. Verified live: LLM verdict `supersedes` 0.98.

Phase 4 closes that gap by adding an **LLM-judge layer** on top of the existing FTS5 candidate stream. Engram does not bundle a model, does not call any LLM API directly, and does not manage any API keys. Instead, engram shells out to an external agent CLI (Claude Code or OpenCode) that the user has already installed and authenticated. Two complementary transports ship together in this phase:

- **4a — CLI shell-out**: `engram conflicts scan --semantic` invokes `ENGRAM_AGENT_CLI` (claude | opencode) per FTS5 candidate, parses the verdict, and persists it as a judged relation.
- **4b — MCP `mem_compare` tool**: agents (running an active session) can compare two memories interactively via a new MCP tool that returns a verdict the agent can then judge.

Local-first stays intact. Engram remains the agent's memory; semantic detection is opt-in (`--semantic` flag, `mem_compare` invocation), shells out only when invoked, and never alters baseline scan behavior.

## Scope

### In Scope

1. **`internal/llm/` — new package** containing the runner abstraction:
   - `Verdict` struct: `{Relation string; Confidence float64; Reasoning string; Model string; DurationMS int64}`
   - `AgentRunner` interface: `Compare(ctx context.Context, prompt string) (Verdict, error)`
   - `ClaudeRunner` implementation: shells `claude -p "<prompt>" --output-format json --no-session-persistence --max-turns 1`, strips markdown fences, parses `envelope.result` then inner JSON.
   - `OpenCodeRunner` implementation: shells `opencode run "<prompt>" --format json`, parses NDJSON event stream, extracts `.part.text` from the `type:"text"` event.
   - Factory selecting runner via `ENGRAM_AGENT_CLI` env var (`claude` | `opencode`).

2. **CLI shell-out path (Phase 4a)** — `cmd/engram/conflicts.go`:
   - Extend `scan` sub-command with `--semantic`, `--concurrency N` (default 5), `--yes` (skip confirmation).
   - When `--semantic` is set: collect FTS5 candidates, print pre-scan estimate (request count + token estimate, subscription/API cost note), confirm via stdin (or `--yes`), then run `AgentRunner.Compare` per candidate using a worker pool.
   - Persist non-`not_conflict` verdicts via the new store method `JudgeBySemantic` (provenance: `marked_by_kind="system"`, `marked_by_actor="engram"`, `marked_by_model=<verdict.Model>`).
   - Per-pair failures skip + log + increment `SemanticErrors`; scan continues.

3. **Store layer extension** — `internal/store/relations.go`:
   - Extend `ScanOptions` with `Semantic bool`, `Concurrency int`.
   - Extend `ScanResult` with `SemanticJudged int`, `SemanticSkipped int`, `SemanticErrors int`.
   - Add new method `JudgeBySemantic(opts JudgeBySemanticOptions) error` that records the verdict on an existing pending relation row (or inserts a fresh judged row when no pending exists). Provenance fields populated as `system` / `engram` / `<model>`.
   - `ScanProject` keeps its existing FTS5 path and gains an internal branch when `opts.Semantic == true` that drives the runner pool through the injected `AgentRunner`.

4. **MCP `mem_compare` tool (Phase 4b)** — `internal/mcp/mcp.go`:
   - New tool `mem_compare(observation_a_id, observation_b_id)` that:
     1. Reads both observations from the local store.
     2. Returns a structured comparison payload (titles, types, contents, optional context) for the calling agent to judge.
     3. Returns the response in the same JSON shape as `Verdict` so the agent can directly call `mem_judge` with the verdict.
   - Unlike Phase 4a, this transport does NOT shell out — the agent IS the LLM and judges in-context.
   - The agent then uses the existing `mem_judge` to persist the verdict.

5. **Documentation** — extend or add doc(s) describing:
   - `ENGRAM_AGENT_CLI` env var contract (supported values, required CLI install + auth).
   - `engram conflicts scan --semantic` UX (pre-scan estimate, confirmation, output counters).
   - `mem_compare` MCP tool (when to use it vs `--semantic` scan).

### Out of Scope (explicit Phase 5+ deferrals)

- **Embeddings / pgvector / sqlite-vec** — Phase 4 uses LLM-judge only; vector candidate generation is Phase 5.
- **Bundled models** (ONNX, llama.cpp, etc.) — engram never ships a model.
- **Direct API calls from engram** — no Anthropic / OpenAI SDK, no API key management. Engram only shells out.
- **Other agent CLIs** beyond Claude + OpenCode (gemini, codex, cursor) — Phase 4 ships exactly two implementations; the `AgentRunner` interface is the extension point for later phases.
- **Async / job-based scan** — Phase 4 is synchronous shell-out. Background workers, job queues, status polling are deferred.
- **Resumable scan checkpoint table** — same single-shot model as Phase 3.
- **Cross-model verdict normalization** — the `Verdict` struct accepts whatever the runner returns; calibrating verdict semantics across models is not in scope.
- **Batched prompts** (N pairs in one LLM call) — Phase 4 is one-pair-per-call. Batching is a Phase 5 cost optimization.
- **HTTP `/conflicts/scan` semantic flag** — Phase 4 lands semantic only on the CLI path. Adding `?semantic=true` to the HTTP endpoint requires solving long-running request semantics and is deferred.

## Capabilities

### New Capabilities
- `agent-runner-abstraction`: pluggable shell-out interface in `internal/llm/` with two implementations (Claude, OpenCode).
- `semantic-scan-cli`: `engram conflicts scan --semantic` end-to-end flow with worker pool, cost warning, and verdict persistence.
- `mem_compare-mcp-tool`: agent-interactive MCP tool for in-session pairwise semantic comparison.
- `judge-by-semantic-store-method`: store-layer entry point for system-attributed judgments with model provenance.

### Modified Capabilities
- `conflict-scan` (Phase 3): gains `--semantic`, `--concurrency`, `--yes` flags. Default behavior unchanged (FTS5-only). Backwards compatible.
- `scan-options` / `scan-result`: extended with semantic counters and options. Existing fields and zero-value defaults preserved.

## Approach

**Pattern**: thin CLI handler, rich store layer, narrow injectable abstraction for shell-out — same shape used by Phase 3 (`ScanProject`, `cmdConflictsScan`) and the existing injectable function vars (`storeNew`, `exitFunc`, `detectProject`).

1. **`AgentRunner` first** (`internal/llm/runner.go`) — TDD the `Verdict` parser for both Claude and OpenCode using fixture strings. Real CLI invocation is gated behind `ENGRAM_AGENT_CLI`; tests use a fake `AgentRunner`.
2. **Store extension** — TDD `JudgeBySemantic` against real SQLite. Reuses `judgeRelationTx` provenance machinery from Phase 1. Add semantic counters to `ScanResult`.
3. **`ScanProject` semantic branch** — when `opts.Semantic == true`, replace the simple `InsertPending` step with: `runner.Compare(prompt)` → `JudgeBySemantic(verdict)`. Worker pool sized by `opts.Concurrency`. Per-pair errors increment `SemanticErrors` and continue.
4. **CLI** — `cmd/engram/conflicts.go` adds `--semantic`/`--concurrency`/`--yes` flag parsing to existing `cmdConflictsScan`. Pre-scan estimate uses `len(candidates)` × per-call token estimate constants. Confirmation reads stdin unless `--yes` or non-TTY.
5. **MCP tool** — register `mem_compare` matching the existing `mem_judge` pattern (declared via `mcp.NewTool`, handler closure). Returns structured comparison payload — does NOT call any model itself; the calling agent judges.
6. **Docs** — extend Phase 3 conflict docs (or `docs/PLUGINS.md`) with the agent-CLI contract and the two transport paths.

### Cost-warning UX (locked in propose context)

The pre-scan estimate prints request count + token estimate as the primary metric. Dollar amounts are NEVER the headline because subscription users pay quota, not USD. Example:

```
About to compare 487 pairs.
~487 LLM calls, ~30k input + ~5k output tokens.

Subscription (Pro/Max/Plus): consumes quota, no extra charge.
Per-token API: estimated ~$0.50-2.00 with Haiku.

Continue? [y/N]    (use --yes to skip)
```

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/llm/runner.go` | New package | `Verdict`, `AgentRunner`, `ClaudeRunner`, `OpenCodeRunner`, factory. |
| `internal/llm/runner_test.go` | New | Parser tests using fixture strings; fake runner for downstream consumers. |
| `internal/store/relations.go` | Modified | `ScanOptions`/`ScanResult` extension; semantic branch in `ScanProject`; new `JudgeBySemantic`. |
| `internal/store/relations_test.go` | Modified | Tests for `JudgeBySemantic` and `ScanProject` semantic branch (with fake runner). |
| `internal/mcp/mcp.go` | Modified | Register `mem_compare` tool + handler. |
| `internal/mcp/mcp_test.go` | Modified (or new file) | `mem_compare` handler tests. |
| `cmd/engram/conflicts.go` | Modified | `--semantic`, `--concurrency`, `--yes` flags; pre-scan estimate; runner factory wiring. |
| `cmd/engram/conflicts_test.go` | Modified | CLI tests for the new flags using a fake `AgentRunner`. |
| `cmd/engram/main.go` | Modified | Read `ENGRAM_AGENT_CLI` env var; inject runner into CLI context. |
| `docs/PLUGINS.md` (or new doc) | Modified | Document agent-CLI contract + semantic scan UX. |

## Test Strategy

Strict TDD is enabled (`go test ./...`). Every behavior arrives via RED → GREEN → REFACTOR.

- **`AgentRunner` parsers**: fixture-driven unit tests for both Claude (envelope.result + fence-stripping) and OpenCode (NDJSON event extraction). No real CLI invocation in unit tests.
- **`JudgeBySemantic`**: real SQLite tests via `newTestStore(t)` — verifies provenance fields (`marked_by_kind="system"`, `marked_by_actor="engram"`, `marked_by_model=<X>`) and verdict fields persisted correctly.
- **`ScanProject` semantic branch**: injects a fake `AgentRunner` that returns canned verdicts, asserts `SemanticJudged`/`SemanticSkipped`/`SemanticErrors` counters and persisted rows. Verifies per-pair error isolation (one bad pair does not abort the scan).
- **CLI tests**: existing `testConfig → seed → withArgs → captureOutput → assert` pattern from `conflicts_test.go`. The runner is injected via the existing fixture pattern (no real shell-out).
- **MCP `mem_compare` handler**: existing in-process MCP test pattern. Asserts response shape and store reads.
- **Regression**: full `go test ./...` MUST stay GREEN. Phase 3 scan path (without `--semantic`) MUST behave identically — covered by re-running existing scan tests.
- **Integration smoke** (manual, not gating): one optional manual run with real `claude` CLI and one with real `opencode` CLI documented in commit notes; not part of CI.

## Migration Safety

- **Schema**: NO new tables, NO new columns, NO new indexes. Phase 4 reuses Phase 1's `memory_relations` schema entirely. Provenance fields (`marked_by_*`, `marked_by_model`) already exist from Phase 1.
- **Data**: no backfill, no rewrites. New verdict rows simply append.
- **Migration order**: not applicable — there is no migration in this phase.

## Backwards Compatibility

- `--semantic` is **opt-in**. The existing `engram conflicts scan` with no flags behaves identically to Phase 3.
- `ScanOptions.Semantic` defaults to `false`; existing callers compile and run unchanged.
- `ScanResult` gains additive fields with zero-value defaults; existing JSON consumers (HTTP `POST /conflicts/scan`) see the new fields as `0` and tolerate them.
- `mem_compare` is a NEW MCP tool; no existing tool changes.
- `ENGRAM_AGENT_CLI` is read only when `--semantic` is invoked. Empty/unset env var with `--semantic` produces a clear error before any work begins.

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Latency at scale (10k pairs × ~1.5s ÷ 5 concurrency ≈ 50 min) | Med | `--concurrency N` flag tunable; pre-scan estimate sets expectations; progress output during scan. |
| Cost surprise for per-token API users | Med | Mandatory pre-scan confirmation; counts + token estimate as primary signal; `--yes` only for automation. |
| Claude CLI envelope change (markdown fences appear/disappear) | Med | Parser strips fences defensively; parser is fixture-tested; failure path skips pair + logs + counts as `SemanticErrors`. |
| OpenCode NDJSON schema drift (event names change) | Med | Same defensive parsing; per-pair errors do not abort scan; documented as a known maintenance surface. |
| Agent CLI not installed or not authenticated | High | Pre-flight check on first runner invocation; clear error message naming the env var and suggested install command. |
| Per-pair LLM hang | Med | Per-call context timeout (default 30s, configurable later); cancelled call counts as `SemanticErrors`. |
| `not_conflict` verdicts inflating `memory_relations` | Low | Decision: do NOT persist `not_conflict` verdicts (locked in propose context). Only positive verdicts (compatible/scoped/related/conflicts_with/supersedes) persist. |
| Mixing `--semantic` with `--apply` semantics from Phase 3 | Low | `--semantic` implies its own persistence path (via `JudgeBySemantic`); the existing `--apply` flag behavior is unchanged when `--semantic` is absent. Documented clearly. |
| Verdict model attribution stale across model upgrades | Low | `Verdict.Model` is captured per call; provenance reflects what was actually used at scan time. Model normalization is explicitly Phase 5+. |

## Rollback Plan

All changes are additive and feature-flagged behind the `--semantic` flag and a new MCP tool:

1. **CLI rollback** — remove `--semantic`/`--concurrency`/`--yes` flag handling and the `if opts.Semantic` branch from `cmd/engram/conflicts.go`. Phase 3 scan keeps working.
2. **Store rollback** — delete `JudgeBySemantic` and the semantic branch in `ScanProject`. Remove additive fields from `ScanOptions`/`ScanResult` (or leave them as unused fields; no DB impact).
3. **MCP rollback** — remove `mem_compare` registration in `internal/mcp/mcp.go` and delete the handler. Existing tools unaffected.
4. **`internal/llm/` rollback** — delete the package. No external callers outside the Phase 4 surface.
5. **Docs** — revert the documentation section.

No data migration. No schema changes. No persisted data depends on Phase 4 specifically (rows are valid Phase 1 relation rows with system provenance).

## Dependencies

- Phase 3 (`memory-conflict-audit`) ARCHIVED — confirmed in archive report #2730. `ScanProject`, `FindCandidates`, `JudgeRelation`, and the Phase 3 CLI/HTTP surface are the substrate Phase 4 extends.
- Phase 1 provenance machinery (`marked_by_kind`, `marked_by_actor`, `marked_by_model` on `memory_relations`) — present and tested.
- External tools: Claude Code CLI (`claude`) OR OpenCode CLI (`opencode`) — user-installed, user-authenticated. Engram does not install or manage them.
- No new Go module dependencies. Pure stdlib + existing SQLite + existing MCP server library.

## Open Questions

Most architectural forks were resolved in propose-phase context. The remaining minor questions for spec/design:

1. **`mem_compare` payload shape** — should the tool return only the two observations + minimal metadata, or pre-format a full prompt the agent can pass directly to its own model? (Lean: minimal payload — let the agent compose its own prompt; matches the "agent IS the LLM" model.)
2. **Pre-scan token estimate constants** — what fixed numbers should drive the estimate (e.g., ~60 input tokens + ~10 output tokens per pair)? (Lean: encode as named consts in `cmd/engram/conflicts.go`; revisit empirically in Phase 5.)
3. **Where does the runner factory live** — `internal/llm/factory.go`, or directly in `cmd/engram/main.go` as an injectable var (matching `storeNew`)? (Lean: `internal/llm/factory.go` for purity, plus an injectable `agentRunnerFactory` var in `cmd/engram/main.go` for testability.)
4. **Per-call timeout default** — 30s is the leaning default; should it be configurable in Phase 4 (`--timeout-per-call`) or hardcoded for now? (Lean: hardcoded for Phase 4; flag added only if real users complain.)
5. **MCP `mem_compare` model attribution** — the agent calling `mem_compare` does not know its own model name reliably. Should the tool ask, or should `mem_judge` capture model attribution as it already does? (Lean: rely on `mem_judge`'s existing `model` parameter; `mem_compare` itself does not need to record provenance.)

## Success Criteria

- [ ] `internal/llm/` package exists with `Verdict`, `AgentRunner`, `ClaudeRunner`, `OpenCodeRunner`, all unit-tested via fixtures.
- [ ] `engram conflicts scan --semantic --yes` against a seeded project produces correct verdicts and persists them via `JudgeBySemantic` with `marked_by_actor="engram"` provenance.
- [ ] `engram conflicts scan --semantic` (no `--yes`) prints the cost-warning estimate and respects stdin confirmation.
- [ ] `engram conflicts scan` (no `--semantic`) behaves identically to Phase 3 — all existing scan tests pass unchanged.
- [ ] `mem_compare` MCP tool is registered, returns structured comparison payload, and is callable end-to-end via the in-process MCP test harness.
- [ ] `ScanResult.SemanticJudged`, `SemanticSkipped`, `SemanticErrors` counters reflect actual scan outcomes; per-pair errors do not abort the scan.
- [ ] `ENGRAM_AGENT_CLI` unset + `--semantic` produces a clear pre-flight error.
- [ ] All existing 19+ packages remain GREEN (zero regressions).
- [ ] TDD followed throughout (RED → GREEN → REFACTOR per task).

## Phase 5 Hooks

Decisions in Phase 4 enable later work:

| Phase 5 Capability | Substrate from Phase 4 |
|--------------------|-----------------------|
| Embedding-based candidate generation | `FindCandidates` is still the single integration point — swap or augment with a vector index without touching the runner layer. |
| Additional agent CLIs (gemini, codex, cursor) | `AgentRunner` interface + factory — add a new implementation behind a new `ENGRAM_AGENT_CLI` value. |
| Async / job-based scan | `ScanProject` semantic branch is a single function — wrap it in a job runner without API change. |
| Batched prompts (N pairs per LLM call) | `AgentRunner.Compare` signature is `(ctx, prompt) Verdict` — add a `CompareBatch(ctx, prompts) []Verdict` method or a new interface. |
| Resumable scan checkpoint | Same hook as Phase 3 — wrap the scan loop with checkpoint state. |
| HTTP `/conflicts/scan?semantic=true` | Behind long-running-request handling (SSE or polling), the same `JudgeBySemantic` store method works. |
| Cross-model verdict normalization | `Verdict.Model` is captured per call — Phase 5 can add a normalizer layer without changing the runner contract. |
| Direct API calls (when/if user wants engram-managed key) | New `AgentRunner` implementation backed by an SDK; existing shell-out runners stay as the default. |

# Design: memory-conflict-semantic (Phase 4)

**Change**: memory-conflict-semantic
**Artifact store**: hybrid (engram + openspec)

---

## Technical Approach

Phase 4 layers a semantic LLM-judge step on top of the Phase 3 FTS5 candidate stream WITHOUT touching schema or existing code paths. A new `internal/llm/` package defines the `AgentRunner` abstraction and ships two shell-out implementations (Claude Code, OpenCode). When `engram conflicts scan --semantic` runs, the existing `ScanProject` loop collects candidates as today, then a worker pool pipes each pair through `runner.Compare(ctx, prompt)`, parses the verdict, and persists non-`not_conflict` outcomes via a new `Store.JudgeBySemantic` method that writes system-attributed rows into the existing `memory_relations` table. A parallel transport — the `mem_compare` MCP tool — lets an active agent perform the same judgment interactively. The `internal/llm/` package is a strict boundary: only `cmd/engram/conflicts.go` and `internal/store/relations.go` consume it; no other package imports it.

---

## Architecture Decisions

### Decision: package boundary `internal/llm/` (not `internal/agent/` or inside `store`)

| Option | Tradeoff | Decision |
|--------|----------|----------|
| `internal/llm/` (new) | Clean boundary; future direct-API runners fit | **Chosen** |
| Inside `internal/store/` | Mixes shell-out IO with SQL ownership | Rejected (violates engram-project-structure) |
| `internal/agent/` | Implies agent lifecycle, broader scope than runner | Rejected |

**Rationale**: name reflects what the package owns (LLM verdicts) and isolates external process IO from store/MCP code. Phase 5 direct-API runners drop in here without touching consumers.

### Decision: injectable `runCLI` function on each runner struct

| Option | Tradeoff | Decision |
|--------|----------|----------|
| Default `exec.CommandContext`, swap field for tests | Real shell-out never executes in unit tests | **Chosen** |
| Mock the `AgentRunner` interface only | Cannot test envelope/NDJSON parsing in isolation | Rejected (parsing IS the risk surface) |
| Build a CLI fixture binary | Heavyweight, slow, OS-dependent | Rejected |

**Rationale**: parsing Claude's outer envelope + fence stripping AND OpenCode's NDJSON event stream is the highest-risk code in this change. The injection point is the byte boundary (`func(ctx, name, args, stdin) ([]byte, error)`), so parser tests feed canned bytes and assert structured `Verdict` values.

### Decision: persist via `JudgeBySemantic` directly (NOT through `JudgeRelation`)

| Option | Tradeoff | Decision |
|--------|----------|----------|
| New `JudgeBySemantic` (separate insert/update path) | Independent of pre-existing pending row; simpler dedup on `(source_id, target_id)` | **Chosen** |
| Reuse `JudgeRelation` after `FindCandidates` inserts pending row | Forces double-write (insert pending → update); cross-project guard already gated by relation existence | Rejected |
| Direct INSERT in CLI handler | Scatters provenance logic outside store | Rejected (violates engram-architecture-guardrails: store owns SQL) |

**Rationale**: semantic scan calls `FindCandidates` with `SkipInsert=true` (already supported), so no pending row exists. `JudgeBySemantic` is one write per verdict. Dedup key `(source_id, target_id)` (canonicalised pair) UPSERTs an existing same-pair row; provenance always overwrites with `marked_by_kind="system"`, `marked_by_actor="engram"`, `marked_by_model=<verdict.Model>`. Cross-project guard reused from `JudgeRelation` (extracted helper).

### Decision: worker pool with bounded `chan pair` + per-pair `context.WithTimeout`

| Option | Tradeoff | Decision |
|--------|----------|----------|
| `errgroup.SetLimit(N)` + per-pair timeout | Standard Go idiom; cancel one pair without aborting scan | **Chosen** |
| Sequential loop | 10k pairs × 1.5s = ~4h; fails REQ-005 latency goal | Rejected |
| Unbounded goroutines | Hammers user's local CLI; OOM risk | Rejected |

**Rationale**: REQ on `--concurrency` defaults 5, max 20. `errgroup` with `SetLimit` matches the existing concurrency style in `internal/cloud/autosync`. Per-pair `context.WithTimeout(timeoutPerCall)` ensures one stuck CLI call never blocks the pool.

### Decision: locked canonical prompt template (verified against both CLIs)

| Option | Tradeoff | Decision |
|--------|----------|----------|
| Single shared prompt in `prompt.go` | Both runners produce identical Verdict shape; verified live | **Chosen** |
| Per-runner prompt | Drift risk; cross-model normalization headache | Rejected |
| Few-shot examples | Larger token cost; not needed (verified zero-shot works) | Rejected (Phase 5 hook) |

**Rationale**: the prompt was live-tested against `claude haiku` and `opencode` default — both return parseable single-line JSON with the six relation verbs. The prompt is locked as a constant in `internal/llm/prompt.go` with `BuildPrompt(a, b store.Observation) string` rendering placeholders.

### Decision: cost warning shows requests + tokens, NOT $$

| Option | Tradeoff | Decision |
|--------|----------|----------|
| Requests + token estimate + subscription note | Honest for both subscription and API users | **Chosen** |
| Show $$ range | Wrong for subscription users; pricing drifts | Rejected |
| No warning | Surprise cost on 10k-pair scans | Rejected (violates engram-business-rules: never silent) |

**Rationale**: token constants `EstimatedInputTokensPerPair=300`, `EstimatedOutputTokensPerPair=50` (revised up from proposal's 30/5 based on verified runs) live in `internal/llm/cost.go`. CLI prints `N requests, ~M input tokens, ~K output tokens. Subscription users: counts against your quota. Continue? [y/N]`.

---

## Data Flow

```
        ┌─ FTS5 candidates (SkipInsert=true) ─┐
        │                                      │
ScanProject ──→ for each obs ──→ FindCandidates ──→ chan pair
                                                       │
                                                       ▼
                                              ┌─ worker pool (N) ─┐
                                              │  AgentRunner      │
                                              │  .Compare(ctx,pmt)│
                                              │   ↓               │
                                              │  Verdict / err    │
                                              └────────┬──────────┘
                                                       ▼
                                              classify verdict
                                              ┌────────┼───────────┐
                                          error   not_conflict   judged
                                            │       │              │
                                       SemErrors  SemSkipped       ▼
                                            │       │     JudgeBySemantic
                                            └───────┴───────┐  (UPSERT system row)
                                                            ▼
                                                       ScanResult
```

`mem_compare` MCP tool path:

```
agent ──→ mem_compare(id_a, id_b, relation, confidence, reasoning, model)
              │
              ▼
      Store.JudgeBySemantic(...)  ──→ memory_relations row + sync_id
              │
              ▼
       MCP response { sync_id }
```

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/llm/runner.go` | Create | `AgentRunner` interface, `Verdict` struct, error sentinels (`ErrCLINotInstalled`, `ErrCLIAuthMissing`, `ErrTimeout`, `ErrInvalidJSON`, `ErrUnknownRelation`) |
| `internal/llm/claude.go` | Create | `ClaudeRunner` impl: `claude -p --output-format json --model haiku --max-turns 1`, parses outer envelope, strips ```fences```, extracts `Model` from `modelUsage` keys, `DurationMS` from `duration_ms` |
| `internal/llm/opencode.go` | Create | `OpenCodeRunner` impl: `opencode run --format json --pure`, scans NDJSON, picks `type=="text"` event, extracts `.part.text`, derives `Model` and `DurationMS` from event metadata |
| `internal/llm/factory.go` | Create | `NewRunner(name string) (AgentRunner, error)` — dispatches `claude` / `opencode`, returns descriptive error naming `ENGRAM_AGENT_CLI` for empty/unknown |
| `internal/llm/prompt.go` | Create | Locked canonical prompt constant + `BuildPrompt` |
| `internal/llm/cost.go` | Create | `EstimatedInputTokensPerPair`, `EstimatedOutputTokensPerPair`, `EstimateScanCost(pairCount int) (in, out int)` |
| `internal/llm/runner_test.go` `claude_test.go` `opencode_test.go` `factory_test.go` `prompt_test.go` | Create | Strict-TDD parser fixtures via injected `runCLI` |
| `internal/store/relations.go` | Modify | Extend `ScanOptions` with `Semantic bool`, `Concurrency int`, `TimeoutPerCall time.Duration`, `MaxSemantic int`, `Runner AgentRunnerLike` (interface duck-typed via local `internal/store/runner.go`); extend `ScanResult` with `SemanticJudged`, `SemanticSkipped`, `SemanticErrors`; add `JudgeBySemantic(JudgeBySemanticParams) (string, error)` |
| `internal/store/runner.go` | Create | Local interface mirror to avoid `store→llm` import cycle; `cmd/engram` wires concrete `*llm.ClaudeRunner` (Go structural compat) |
| `internal/store/judge_by_semantic_test.go` | Create | Real-SQLite tests: insert path, UPSERT idempotency, `not_conflict` skip, validation errors, system provenance assertions |
| `internal/store/scan_semantic_test.go` | Create | Fake-runner driven `ScanProject` tests: counter accuracy, per-pair error isolation, timeout handling, max-semantic cap |
| `cmd/engram/conflicts.go` | Modify | Add `--semantic`, `--concurrency`, `--timeout-per-call`, `--yes`, `--max-semantic` flags to `cmdConflictsScan`; pre-LLM cost prompt (skipped on `--yes`); resolve runner via `agentRunnerFactory(os.Getenv("ENGRAM_AGENT_CLI"))`; pass to `ScanOptions.Runner` |
| `cmd/engram/conflicts_test.go` | Modify | New cases: --semantic off = Phase 3 unchanged; --semantic + --yes uses fake runner; ENGRAM_AGENT_CLI unset fails fast; concurrency/timeout/max-semantic flag parsing |
| `cmd/engram/main.go` | Modify | Add injectable `agentRunnerFactory = llm.NewRunner` package-level var (alongside `storeNew`, `newHTTPServer`); enables test injection of fake runners |
| `internal/mcp/mcp.go` | Modify | Register `mem_compare` tool — input schema (memory_id_a, memory_id_b, relation enum, confidence float, reasoning ≤200 chars, model optional); resolves observation IDs (int → sync_id), calls `Store.JudgeBySemantic`, returns `{sync_id}` JSON |
| `internal/mcp/mcp_test.go` | Modify | New cases: persist verdict via mem_compare; `not_conflict` returns success no-row; missing required field error; non-existent observation error |
| `internal/server/server.go` | Modify | Extend `POST /conflicts/scan` body parser to accept `semantic`, `concurrency`, `timeout_per_call_seconds`, `max_semantic`; forward to `ScanOptions`; response includes `semantic_judged`, `semantic_skipped`, `semantic_errors` |
| `internal/server/server_test.go` | Modify | New cases: `semantic=false` returns zero counters; `semantic=true` with fake runner returns populated counters |
| `docs/PLUGINS.md` (or `docs/SEMANTIC_SCAN.md` new) | Modify/Create | `ENGRAM_AGENT_CLI` env var contract; `--semantic` UX; cost warning; `mem_compare` tool reference |

No file deletions. No schema migrations.

---

## Interfaces / Contracts

```go
// internal/llm/runner.go
package llm

type AgentRunner interface {
    Compare(ctx context.Context, prompt string) (Verdict, error)
}

type Verdict struct {
    Relation   string  // one of: conflicts_with|supersedes|scoped|related|compatible|not_conflict
    Confidence float64 // [0.0, 1.0]
    Reasoning  string  // ≤200 chars
    Model      string  // captured from CLI output (e.g. "claude-haiku-4-5")
    DurationMS int64   // wall-clock for the CLI call
}

var (
    ErrCLINotInstalled = errors.New("agent CLI binary not found in PATH")
    ErrCLIAuthMissing  = errors.New("agent CLI is not authenticated")
    ErrTimeout         = errors.New("agent CLI call exceeded timeout")
    ErrInvalidJSON     = errors.New("agent CLI returned malformed JSON")
    ErrUnknownRelation = errors.New("agent returned a relation outside the locked vocabulary")
)

// Concrete runners hold an injectable runCLI for tests:
type ClaudeRunner struct {
    runCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
}
type OpenCodeRunner struct {
    runCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
}

func NewRunner(name string) (AgentRunner, error) // factory
```

```go
// internal/store/relations.go (additions)
type ScanOptions struct {
    // existing: Project, Since, Apply, MaxInsert
    Semantic       bool
    Concurrency    int           // default 5, max 20
    TimeoutPerCall time.Duration // default 60s
    MaxSemantic    int           // default 100
    Runner         AgentRunner   // interface from internal/store/runner.go (duck-typed)
}

type ScanResult struct {
    // existing: Project, Inspected, CandidatesFound, AlreadyRelated, RelationsInserted, Capped, DryRun
    SemanticJudged  int `json:"semantic_judged"`
    SemanticSkipped int `json:"semantic_skipped"`
    SemanticErrors  int `json:"semantic_errors"`
}

type JudgeBySemanticParams struct {
    SourceID, TargetID string  // sync_ids
    Relation           string  // verb (must be in validRelationVerbs and != "not_conflict")
    Confidence         float64
    Reasoning          string
    Model              string  // -> marked_by_model
}

func (s *Store) JudgeBySemantic(p JudgeBySemanticParams) (syncID string, err error)
```

```go
// internal/mcp/mcp.go — mem_compare schema
{
  memory_id_a: int (required),
  memory_id_b: int (required),
  relation:    string (required, enum of 6 verbs),
  confidence:  float (required, [0,1]),
  reasoning:   string (required, ≤200),
  model:       string (optional)
}
// Returns: { "sync_id": "rel-...." } on persist, { "sync_id": "" } on not_conflict
```

CLI shell-out abstraction: every runner ctor accepts a default `runCLI = exec.CommandContext` based impl. Tests construct runners with a fake `runCLI` that returns canned bytes.

---

## Testing Strategy

| Layer | What to Test | Approach |
|-------|--------------|----------|
| Unit (parsers) | Claude envelope+fence stripping; OpenCode NDJSON extraction; missing/malformed JSON; unknown relation → `ErrUnknownRelation`; out-of-range confidence | Inject fake `runCLI`; table-driven fixtures from real CLI captures |
| Unit (factory) | `claude` / `opencode` dispatch; empty/unknown returns descriptive error naming env var | Direct `NewRunner` calls |
| Unit (prompt) | Snapshot of rendered prompt for fixed observation pair | String compare against golden |
| Unit (cost) | Token estimate math; per-pair constants stable | Pure function tests |
| Unit (store) | `JudgeBySemantic` insert; UPSERT idempotency on same pair; `not_conflict` no-op; validation errors; system provenance fields | Real SQLite `:memory:`, fixture observations |
| Integration (store) | `ScanProject` with `Semantic=true` and fake runner: counter accuracy across success/skip/error; `--max-semantic` cap; per-pair timeout isolation; concurrency bound observed | Real SQLite + canned-verdict fake runner channel-tap |
| Integration (CLI) | `--semantic` off = Phase 3 unchanged (snapshot output); `--semantic --yes` runs with injected fake; `ENGRAM_AGENT_CLI` unset fails fast with named error | `agentRunnerFactory` test override + existing `testConfig→seed→withArgs→captureOutput→assert` pattern |
| Integration (HTTP) | `POST /conflicts/scan` with `semantic=true/false`; counters in response; defaults applied when fields omitted | In-process server harness with injected fake runner |
| Integration (MCP) | `mem_compare` happy path persists row with system provenance; `not_conflict` no-row success; missing field rejected; non-existent obs id error | Existing in-process MCP test harness |
| Regression | All Phase 3 tests stay green; full `go test ./...` GREEN | CI |
| Out of scope | Real CLI invocation (no live `claude`/`opencode` calls in tests) | — |

---

## Migration / Rollout

No schema migration. No data backfill. No feature flag — the surface is opt-in via `--semantic` and `mem_compare`. Rollout order: ship `internal/llm/` package first (independent of consumers), then `JudgeBySemantic`, then CLI/HTTP/MCP wiring. Each step lands GREEN.

---

## Open Questions

- [ ] None blocking. `mem_compare` model attribution lands as an optional input field (agent-supplied); auto-capture deferred to Phase 5.
- [ ] Per-call timeout default locked at 60s (REQ); revisit if real-world p99 trips it.
- [ ] Token estimate constants (300/50) are placeholders refined post-launch with real telemetry.

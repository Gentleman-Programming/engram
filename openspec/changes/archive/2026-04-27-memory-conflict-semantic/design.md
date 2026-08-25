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

**Rationale**: parsing Claude's outer envelope + fence stripping AND OpenCode's NDJSON event stream is the highest-risk code in this change. The injection point is the byte boundary, so parser tests feed canned bytes and assert structured `Verdict` values.

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

**Rationale**: token constants `EstimatedInputTokensPerPair=300`, `EstimatedOutputTokensPerPair=50` live in `internal/llm/cost.go`. CLI prints `N requests, ~M input tokens, ~K output tokens. Subscription users: counts against your quota. Continue? [y/N]`.

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

---

## Interfaces / Contracts

Key new interfaces:

```go
// internal/llm/runner.go
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
    Runner         AgentRunnerLike
}

type ScanResult struct {
    // existing: Project, Inspected, CandidatesFound, AlreadyRelated, RelationsInserted, Capped, DryRun
    SemanticJudged  int `json:"semantic_judged"`
    SemanticSkipped int `json:"semantic_skipped"`
    SemanticErrors  int `json:"semantic_errors"`
}

func (s *Store) JudgeBySemantic(p JudgeBySemanticParams) (syncID string, err error)
```

---

## Testing Strategy

| Layer | Approach |
|-------|----------|
| Unit (parsers) | Inject fake `runCLI`; table-driven fixtures from real CLI captures |
| Unit (factory) | Direct `NewRunner` calls with various inputs |
| Unit (store) | Real SQLite `:memory:` with fixture observations |
| Integration (store) | Real SQLite + canned-verdict fake runner |
| Integration (CLI) | `agentRunnerFactory` test override + existing pattern |
| Integration (HTTP) | In-process server harness with injected fake runner |
| Integration (MCP) | Existing in-process MCP test harness |
| Regression | Full `go test ./...` GREEN |

---

## Migration / Rollout

No schema migration. No data backfill. The surface is opt-in via `--semantic` and `mem_compare`. Rollout order: ship `internal/llm/` package first (independent of consumers), then `JudgeBySemantic`, then CLI/HTTP/MCP wiring. Each step lands GREEN.

---

## Open Questions

- None blocking. Token estimate constants (300/50) are placeholders refined post-launch with real telemetry.
- Per-call timeout default locked at 60s (REQ); revisit if real-world p99 trips it.
- `mem_compare` model attribution lands as an optional input field (agent-supplied); auto-capture deferred to Phase 5.

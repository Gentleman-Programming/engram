# Exploration: memory-conflict-semantic (Phase 4)

> **Note**: This is a brief mirror. Full exploration content lives in engram at `topic_key: sdd/memory-conflict-semantic/explore` (observation #2733). For complete decision tradeoffs and verification notes, retrieve via `mem_search` + `mem_get_observation`.

## Context

Phase 3 (memory-conflict-audit) shipped `engram conflicts scan` using FTS5+BM25 lexical detection. That catches ~80% of conflicts where memories share vocabulary. The remaining ~20% are vocabulary-different but semantically equivalent or contradictory pairs:

- "Use Hexagonal Architecture" vs "Use Ports and Adapters" (compatible — same idea, different names)
- "Use Postgres" vs "We migrated to MongoDB" (supersedes — newer replaces older)
- "Use Clean Architecture" vs "Use Hexagonal Architecture" (compatible — conceptually equivalent)

FTS5 cannot see these because the tokens do not overlap. Phase 4 adds an LLM-judge layer on top of the FTS5 candidate stream.

## Verified facts

- Both `claude` and `opencode` CLIs are installed and authenticated for the user.
- **Claude CLI**: `claude -p "<prompt>" --output-format json` returns `{"type":"result","result":"<JSON-as-escaped-string>",...}`. Inner JSON is sometimes wrapped in markdown fences (` ```json ... ``` `) which must be stripped.
- **OpenCode CLI**: `opencode run "<prompt>" --format json` emits NDJSON event stream. The final assistant text is in the event with `type:"text"` at `.part.text`.
- **Live test results** (real CLI calls, not mocked):
  - "Use Clean Architecture" vs "Use Hexagonal Architecture" → `compatible` confidence 0.95 (CORRECT)
  - "Use Postgres" vs "We migrated to MongoDB" → `supersedes` confidence 0.98 (CORRECT)
- **Cost reality**: Pro/Max/Plus subscription users pay $0 out of pocket — quota only. The `total_cost_usd` reported by Claude CLI is *equivalent API cost*, NOT real billing. OpenCode reports `cost: 0` for sub users honestly.

## Pre-decided architecture (from exploration + user direction)

- **No embeddings, no bundled models, no API key management in engram** — engram shells out to an external agent CLI.
- **Two transports both ship in Phase 4**:
  - **4a — CLI shell-out**: `engram conflicts scan --semantic` invokes `ENGRAM_AGENT_CLI` (claude | opencode).
  - **4b — MCP `mem_compare` tool**: agent reads both memories, returns verdict via MCP.
- **`AgentRunner` interface** in `internal/llm/` (new package) with `Compare(ctx, prompt) (Verdict, error)`. Two implementations: `ClaudeRunner`, `OpenCodeRunner`.
- **Selected via env var**: `ENGRAM_AGENT_CLI=claude` or `ENGRAM_AGENT_CLI=opencode`.
- Plug point: after `FindCandidates` returns a candidate, before `InsertPending` — call `AgentRunner.Compare` and persist the verdict directly via new store method `JudgeBySemantic`.
- Concurrency: pool of 5 by default, configurable via `--concurrency N`.
- Cost warning: pre-scan prints request count + token estimate, NEVER $$ as primary metric. Subscription users see "consumes quota, no extra charge"; per-token API users see estimated range.
- Failure handling: per-pair skip+log, scan continues, `SemanticErrors` counter in `ScanResult`.
- Persistence: ALL non-`not_conflict` verdicts persist (scoped, related, conflicts_with, supersedes, compatible). Provenance: `marked_by_kind="system"`, `marked_by_actor="engram"`, `marked_by_model=<runner output model>`.

## Affected modules

- `internal/store/relations.go` — extend `ScanOptions` with `Semantic`, `Concurrency`. Extend `ScanResult` with `SemanticJudged`, `SemanticSkipped`, `SemanticErrors`. Add `JudgeBySemantic` method.
- `internal/llm/runner.go` — NEW package. `AgentRunner` interface + `ClaudeRunner` + `OpenCodeRunner` + `Verdict` type.
- `internal/mcp/mcp.go` — register `mem_compare` tool + handler.
- `cmd/engram/conflicts.go` — `--semantic`, `--concurrency`, `--yes` flags on `scan` sub-command.
- `cmd/engram/main.go` — wire `ENGRAM_AGENT_CLI` env var into the runner factory.
- `docs/PLUGINS.md` (or new doc) — document semantic mode + agent CLI requirement.

## Open questions resolved by user

The exploration listed 6 open questions; all 6 are pre-answered in the propose-phase context (see proposal.md). Remaining open questions are minor and listed in proposal.md § Open Questions.

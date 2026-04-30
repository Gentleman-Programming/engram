# Spec: memory-conflict-semantic (Phase 4)

**Change**: memory-conflict-semantic
**Artifact store**: hybrid (engram + openspec)

---

## Purpose

Define what MUST be true after Phase 4 ships. Covers the LLM-judge layer (CLI shell-out 4a + MCP tool 4b), the new `internal/llm/` package, store extensions, and backwards compatibility. All requirements are additive. Phase 3 surfaces MUST remain unchanged.

---

## Domain: agent-runner-abstraction (NEW)

### Requirement: AgentRunner interface and Verdict struct

The system MUST define `AgentRunner` and `Verdict` in `internal/llm/runner.go`. `AgentRunner` MUST have exactly one method: `Compare(ctx context.Context, prompt string) (Verdict, error)`. `Verdict` MUST carry `Relation`, `Confidence`, `Reasoning`, `Model`, and `DurationMS` fields.

#### Scenario: happy path — valid Compare call returns Verdict

- GIVEN a fake `AgentRunner` that returns `Verdict{Relation:"compatible", Confidence:0.95}`
- WHEN `runner.Compare(ctx, "compare these two")` is called
- THEN a non-error Verdict with the expected fields is returned

#### Scenario: Compare propagates error from runner

- GIVEN a fake `AgentRunner` that returns an error
- WHEN `runner.Compare(ctx, prompt)` is called
- THEN the error is returned and Verdict is zero-value

---

### Requirement: ClaudeRunner parses Claude CLI envelope

`ClaudeRunner` MUST invoke `claude -p --output-format json` and parse `envelope.result`. MUST strip ` ``` ` fences if present. MUST return a wrapped error on parse failure.

#### Scenario: parses clean JSON result

- GIVEN Claude CLI output with `{"result":"{\"Relation\":\"supersedes\",\"Confidence\":0.98,\"Reasoning\":\"...\",\"Model\":\"haiku\"}"}`
- WHEN `ClaudeRunner.Compare` parses the output
- THEN `Verdict.Relation == "supersedes"` and `Verdict.Confidence == 0.98`

#### Scenario: strips markdown fence and parses

- GIVEN Claude CLI output wraps inner JSON in ` ```json ... ``` ` fences
- WHEN `ClaudeRunner.Compare` parses the output
- THEN fences are stripped and Verdict is parsed successfully

#### Scenario: malformed inner JSON returns error

- GIVEN Claude CLI output with `{"result":"not valid json"}`
- WHEN `ClaudeRunner.Compare` parses the output
- THEN a wrapped parse error is returned

---

### Requirement: OpenCodeRunner parses NDJSON event stream

`OpenCodeRunner` MUST invoke `opencode run --format json --pure`, scan NDJSON lines, extract `.part.text` from the event with `type="text"`, and parse that text as a Verdict JSON.

#### Scenario: parses text event from NDJSON stream

- GIVEN NDJSON output with one `{"type":"text","part":{"text":"{\"Relation\":\"conflicts_with\",\"Confidence\":0.9,...}"}}` line
- WHEN `OpenCodeRunner.Compare` parses it
- THEN `Verdict.Relation == "conflicts_with"`

#### Scenario: missing text event returns error

- GIVEN NDJSON with no `type:"text"` line
- WHEN `OpenCodeRunner.Compare` processes the stream
- THEN a descriptive error is returned

---

### Requirement: Runner factory selects via ENGRAM_AGENT_CLI

`internal/llm/factory.go` MUST expose `NewRunner(name string) (AgentRunner, error)`. MUST return `ClaudeRunner` for `"claude"`, `OpenCodeRunner` for `"opencode"`. MUST return a descriptive error for any other value, including empty string.

#### Scenario: factory returns ClaudeRunner for "claude"

- GIVEN `ENGRAM_AGENT_CLI=claude`
- WHEN `NewRunner("claude")` is called
- THEN a `*ClaudeRunner` is returned without error

#### Scenario: factory errors on unknown value

- GIVEN `ENGRAM_AGENT_CLI=""` (empty)
- WHEN `NewRunner("")` is called
- THEN a non-nil error naming the env var and supported values is returned

---

## Domain: semantic-scan-cli (NEW)

### Requirement: --semantic flag on engram conflicts scan

`engram conflicts scan` MUST accept `--semantic` (bool, default false). When absent, behavior MUST be identical to Phase 3. When present, after FTS5 candidate collection the system MUST: print the cost-warning estimate, require confirmation (or `--yes`), then invoke `AgentRunner.Compare` per candidate via a worker pool, and persist non-`not_conflict` verdicts via `JudgeBySemantic`.

#### Scenario: --semantic off — Phase 3 behavior unchanged

- GIVEN `--semantic` is not passed
- WHEN `engram conflicts scan --apply` is run
- THEN behavior and output are identical to Phase 3; no LLM calls are made

#### Scenario: --semantic prints cost estimate and prompts

- GIVEN 10 FTS5 candidate pairs and `--semantic` set without `--yes`
- WHEN the command runs
- THEN output contains candidate count, estimated LLM calls, estimated tokens, subscription note, and `Continue? [y/N]` prompt

#### Scenario: --yes skips confirmation

- GIVEN `--semantic --yes` flags set
- WHEN the command runs
- THEN no stdin prompt appears; LLM calls proceed immediately

#### Scenario: ENGRAM_AGENT_CLI unset with --semantic fails fast

- GIVEN `ENGRAM_AGENT_CLI` is empty and `--semantic` is set
- WHEN the command is invoked
- THEN an error naming `ENGRAM_AGENT_CLI` and supported values is printed; exit code is non-zero; no LLM calls are made

---

### Requirement: --concurrency N flag (default 5, max 20)

MUST accept `--concurrency N` (int, default 5). Values outside 1-20 MUST be rejected with a clear error before any work begins.

#### Scenario: concurrency controls goroutine pool

- GIVEN `--semantic --concurrency 3`
- WHEN the scan runs with 10 candidate pairs
- THEN at most 3 pairs are in-flight simultaneously

#### Scenario: out-of-range concurrency rejected

- GIVEN `--concurrency 0`
- WHEN the command starts
- THEN an error is printed and no candidates are processed

---

### Requirement: --timeout-per-call N flag (default 60s)

MUST accept `--timeout-per-call N` (int, seconds, default 60). When a runner call exceeds the timeout: the pair MUST be skipped, a structured warn log MUST be emitted, and `SemanticErrors` MUST be incremented. The scan MUST continue.

#### Scenario: timed-out pair is skipped and counted

- GIVEN a fake runner that blocks beyond the timeout
- WHEN `--timeout-per-call 1` and the call exceeds 1s
- THEN `SemanticErrors` increments by 1 and the scan continues with remaining pairs

---

### Requirement: --max-semantic N cap (default 100)

MUST accept `--max-semantic N` (int, default 100). When the FTS5 candidate count exceeds N: a WARNING MUST be printed stating the cap; only the first N pairs are sent to the LLM; the scan MUST NOT abort.

#### Scenario: cap limits LLM calls

- GIVEN 200 FTS5 candidates and `--max-semantic 50`
- WHEN the scan runs
- THEN exactly 50 LLM calls are made; a WARNING message naming the cap is printed

---

## Domain: judge-by-semantic-store-method (NEW)

### Requirement: JudgeBySemantic store method

`*store.Store` MUST expose `JudgeBySemantic(opts JudgeBySemanticOptions) (syncID string, error)`. MUST insert a `memory_relations` row with `marked_by_kind="system"`, `marked_by_actor="engram"`, `marked_by_model=<opts.Model>`. MUST skip rows where `Relation == "not_conflict"`. MUST be idempotent on same pair+provenance (UPDATE if exists).

#### Scenario: inserts judged row with system provenance

- GIVEN two observations A and B with no existing relation
- WHEN `JudgeBySemantic({ObsA:A, ObsB:B, Relation:"compatible", Confidence:0.9, Model:"haiku"})` is called
- THEN a row is inserted with `marked_by_actor="engram"` and `marked_by_model="haiku"` and a non-empty sync_id is returned

#### Scenario: not_conflict verdict is not persisted

- GIVEN two observations with no existing relation
- WHEN `JudgeBySemantic({Relation:"not_conflict"})` is called
- THEN no row is inserted and no error is returned

#### Scenario: validates required fields

- GIVEN `JudgeBySemantic` called with zero-value ObsA
- WHEN the call executes
- THEN a validation error is returned

---

### Requirement: ScanResult extended with semantic counters

`ScanResult` MUST include `SemanticJudged int`, `SemanticSkipped int`, `SemanticErrors int`. CLI output and HTTP `/conflicts/scan` response MUST surface these fields. Zero-value MUST be tolerated by existing JSON consumers.

#### Scenario: counters reflect scan outcomes

- GIVEN a semantic scan where 8 pairs succeed, 1 is skipped (not_conflict), 1 errors
- WHEN the scan completes
- THEN `SemanticJudged=8`, `SemanticSkipped=1`, `SemanticErrors=1` are returned

#### Scenario: HTTP response includes semantic counters

- GIVEN a `POST /conflicts/scan` with `semantic:true`
- WHEN the response is read
- THEN `semantic_judged`, `semantic_skipped`, `semantic_errors` fields are present

---

### Requirement: per-pair failure isolation

Any error from `AgentRunner.Compare` (including timeouts) MUST: emit a structured warn log with `pair=<a,b>` and `error=<msg>`, increment `SemanticErrors`, and allow the scan to continue. The scan MUST NOT abort on individual pair failures.

#### Scenario: one failed pair does not abort scan

- GIVEN a fake runner that errors on pair (3,7) and succeeds on all others
- WHEN a semantic scan runs over 5 pairs including (3,7)
- THEN `SemanticErrors=1`, `SemanticJudged=4`, and scan exits with code 0

---

## Domain: mem-compare-mcp-tool (NEW)

### Requirement: mem_compare MCP tool

The system MUST register `mem_compare` in `internal/mcp/mcp.go`. Input schema: `memory_id_a` (int, required), `memory_id_b` (int, required), `relation` (string enum, required), `confidence` (float 0..1, required), `reasoning` (string ≤200 chars, required), `model` (string, optional). Behavior: persists a relation row via `JudgeBySemantic`. Returns the inserted row's `sync_id`. MUST be idempotent on existing same-pair relation.

#### Scenario: agent persists verdict via mem_compare

- GIVEN observations id=10 and id=20 exist in the local store
- WHEN `mem_compare(memory_id_a:10, memory_id_b:20, relation:"supersedes", confidence:0.98, reasoning:"newer post supersedes", model:"haiku")` is called
- THEN a relation row is inserted with system provenance and the sync_id is returned

#### Scenario: mem_compare with not_conflict does not insert

- GIVEN observations id=5 and id=6 exist
- WHEN `mem_compare(memory_id_a:5, memory_id_b:6, relation:"not_conflict", confidence:0.99, reasoning:"unrelated")` is called
- THEN no row is inserted and the tool returns a success response with no sync_id

#### Scenario: mem_compare rejects missing required field

- GIVEN a call omitting `memory_id_b`
- WHEN the MCP tool handler runs
- THEN a 400-equivalent MCP error is returned and no row is inserted

#### Scenario: mem_compare with non-existent observation id

- GIVEN `memory_id_a=9999` does not exist in the store
- WHEN `mem_compare` is called
- THEN a descriptive error is returned

---

## Domain: conflict-scan modified (MODIFIED)

### Requirement: engram conflicts scan

The system MUST expose `engram conflicts scan` that iterates observations for a project, runs `FindCandidates`, and either reports candidates (`--dry-run`, default) or inserts new pending relation rows (`--apply`). `--apply` MUST enforce a `--max-insert N` cap (default 100). When the cap is reached, the command MUST print a WARNING and stop without inserting further rows. Pairs with an existing relation row (any `judgment_status`) MUST be skipped without inserting.

Phase 4 additionally accepts `--semantic`, `--concurrency N` (default 5, max 20), `--timeout-per-call N` (default 60), `--yes`, and `--max-semantic N` (default 100) flags. All Phase 3 flag behavior MUST remain unchanged when `--semantic` is absent.

(Previously: no semantic flags; FTS5-only detection path.)

#### Scenario: dry-run reports candidates without writing

- GIVEN 5 observation pairs qualify as conflict candidates
- WHEN `engram conflicts scan --dry-run`
- THEN output reports 5 candidates found and 0 rows inserted
- AND the `memory_relations` table is unchanged

#### Scenario: apply inserts up to max-insert cap

- GIVEN 150 observation pairs qualify as candidates and no existing relation rows exist
- WHEN `engram conflicts scan --apply --max-insert 50`
- THEN exactly 50 rows are inserted, a WARNING is printed indicating the cap was reached, and the command stops

#### Scenario: apply skips already-related pairs

- GIVEN 3 candidate pairs exist, 2 of which already have a relation row with any status
- WHEN `engram conflicts scan --apply`
- THEN only 1 new row is inserted (the pair without an existing relation)

#### Scenario: --semantic + --apply persists LLM verdicts

- GIVEN 5 FTS5 candidates, a fake runner returning `compatible` for each, and `--semantic --apply --yes`
- WHEN `engram conflicts scan --semantic --apply --yes`
- THEN 5 `compatible` relation rows are inserted with `marked_by_actor="engram"` provenance and `SemanticJudged=5` is reported

---

### Requirement: POST /conflicts/scan accepts semantic params

`POST /conflicts/scan` body MUST accept optional fields: `semantic` (bool), `concurrency` (int), `timeout_per_call_seconds` (int), `max_semantic` (int). Defaults MUST match CLI defaults if omitted. Response MUST include `semantic_judged`, `semantic_skipped`, `semantic_errors` (all zero when `semantic=false`).

(Previously: body only accepted `project`, `since`, `apply`, `max_insert`.)

#### Scenario: scan with semantic=false omits new counters as zero

- GIVEN `POST /conflicts/scan` with `{"project":"alpha","apply":false}`
- WHEN response is read
- THEN `semantic_judged=0`, `semantic_skipped=0`, `semantic_errors=0` are present in the response

#### Scenario: scan with semantic=true returns populated counters

- GIVEN a seeded project with FTS5 candidates and a fake runner
- WHEN `POST /conflicts/scan` with `{"project":"alpha","semantic":true,"concurrency":2,"max_semantic":10}`
- THEN response includes non-zero `semantic_judged` and the verdict rows are in the DB

---

## Domain: backwards-compatibility

### Requirement: Phase 3 surfaces unchanged

All Phase 3 CLI commands, HTTP routes, MCP tools, and store methods MUST behave identically when `--semantic` is absent or `semantic=false`. `ScanOptions` zero-value (`Semantic=false`) MUST follow the Phase 3 code path without invoking any `AgentRunner`. All 19+ existing packages MUST remain GREEN.

#### Scenario: existing scan test suite passes unchanged

- GIVEN Phase 4 is deployed and `--semantic` is not used
- WHEN the full Phase 3 scan test suite runs
- THEN all tests pass with no behavioral changes

#### Scenario: existing MCP tools unaffected

- GIVEN Phase 4 is deployed
- WHEN all pre-Phase-4 MCP tools (mem_save, mem_search, mem_judge, etc.) are invoked
- THEN their signatures and behaviors are unchanged

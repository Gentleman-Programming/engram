# Feature: memory-protocol-unification

Locator: `odd/tasks/memory-protocol-unification.md` — Engram mirror topic `odd/memory-protocol-unification/tasks`
(project `engram`).
Branch: `feat/memory-protocol-unification`.

## Objective

Unify and strengthen Engram memory protocols across all agent instruction surfaces to eliminate Claude-specific
assumptions (ToolSearch, Core Tools, Deferred tools), document adapter-specific passive capture per host, and correct
contract mismatches in all embedded protocols (Go, TypeScript, Markdown, shell).

## Problem

1. Embedded protocols in Go (`setup.go`), TypeScript (`engram.ts`), and Markdown (`DOCS.md`, `SKILL.md`) contained
   Claude-specific assumptions: `ToolSearch` for tool discovery, a `Core Tools` vs `Deferred Tools` split, and "always
   available" language.
2. No documentation of which host owns which aspect of passive capture: Claude SubagentStop hooks, OpenCode delegated
   task outputs, Pi explicit capture, generic MCP `mem_capture_passive`.
3. `TestCodexPreToolUseManifestRegistersNativeBinder` had a mismatch between the proposed prose and Codex's actual
   PreToolUse matcher.
4. Three partial commits (Claude SKILL.md, Codex SKILL.md, OpenCode engram.ts) left gaps in the unification.

## Non-goals

- Do not modify runtime event implementation or hook behavior (only instruction prose).
- Do not impose Claude ToolSearch or MCP names on other hosts.
- Preserve pre-existing `odd/tasks/pi-cross-project-saves.md` (predates this work).

## Decision

Approach: replace every embedded protocol copy (Go `setup.go`, TypeScript `engram.ts`, Markdown `DOCS.md`, per-agent
`SKILL.md`, Codex `session-start.sh`) with a unified protocol that removes Claude-specific ToolSearch/deferred-tool
assumptions and replaces them with host-specific prose describing which MCP names and discovery mechanisms each adapter
exposes. Document passive capture as adapter-specific — Claude SubagentStop hooks, OpenCode delegated task outputs, Pi
explicit capture, generic MCP `mem_capture_passive` — rather than implying a universal automatic path. Keep instruction
corrections scoped to prose only; do not modify runtime event hooks, manifest parsing, or session lifecycle behavior.
Apply test-first for new behavioral assertions (T2), delegate full Pi suite to 17 independent per-file subagents (T3V),
run real-host probes against local Ollama with isolated Engram binary (T4), and close with seam checks across all 14
files plus semantic BCP14 assessment (T5).

## Tasks

- [x] T1 | inline | b49a66c | Four MUST NOT replacements committed; amend corrected odd/ constraint; source/embedded
      parity verified.
- [x] T2 | delegated → parent verification | ab7e5d5 | Worker corrected Codex native binder and SessionStart HEREDOC.
      Codex tests 48/48 PASS, PromptSubmit* 3/3 PASS, SSH-signed. Parent acceptance complete.
- [x] T3 | inline correction | HEAD | README:351 `must not` → `MUST NOT` authorized; BCP14/native contracts PASS
      (B1-B6).
- [x] T3V | 17 independent subagents | — | 366/366 tests PASS, 0 fail/skip/cancel, 17 exits 0. 30 Pi fingerprints
      unchanged.
- [x] T4 | isolated real-host probes | — | Pi probe: obs 5179 saved via Ollama qwen3.6. OpenCode HTTP: obs 5180 +
      session registration. Report at `.git/engram-memory-probes/t4-real-host/T4_VERIFICATION_REPORT.md`.
- [x] T5 | inline + independent verifier | — | Seam checks: 14/14 files, shared phrase consistent, no Claude ToolSearch
      in protocol prose, SSH-signed on all 9 commits, version bumps verified. All tests pass. Delivery slice ready.

## Acceptance criteria

- All embedded protocols (Go, TS, Markdown, shell) use the shared phrase: `The agent MUST follow this protocol without
waiting for activation`.
- Zero references to `ToolSearch`, `Core Tools`, `Deferred tools`, or `always available` in protocol prose (DOCS.md
  has one contextual note about Claude-specificity only).
- `TestCodexPreToolUseManifestRegistersNativeBinder` and `TestCodexRegisteredSessionHandoff` (with T2 assertions)
  PASS.
- Pi test suite 366/366 PASS (17 independent subagents).
- Real probes: at least one persisted observation in Pi and one in OpenCode via local Ollama.
- Commits: Signed-off-by, SSH-signed.

## Checks

- `go test ./plugin/... -run 'TestCodex'` — Codex tests
- `node --test plugin/pi/test/*.test.mjs` — Pi test suite
- `git log --oneline --name-only main..HEAD | grep '^\.codegraph/'` — no forbidden paths in commits

## Delivery

9 commits on `feat/memory-protocol-unification`:

```
17460ef fix(types): define EngramHooks type to replace unknown-valued hook record
7067ac8 fix(memory): enforce RFC 2119 normative keywords across all sources
021d129 fix(memory): unify Go memoryProtocolMarkdown with enriched protocol
fc83d13 fix(memory): unify Claude Code SKILL.md with enriched protocol
d5eedbb fix(memory): unify Codex SKILL.md with enriched protocol
c5a34da fix(memory): unify OpenCode engram.ts with enriched protocol
b49a66c fix(memory): record pending normative protocol corrections
ab7e5d5 fix(memory): unify host-contract corrections across all agents
a99d04f fix(memory): remove Claude ToolSearch from Claude SKILL.md
```

16 files changed (was 14; +2 for ENHANCE_TYPES `EngramHooks` type fix), ~520 insertions, ~130 deletions.

**Issue:** [Gentleman-Programming/engram#1786](https://github.com/Gentleman-Programming/engram/issues/1786)
**PR:** [Gentleman-Programming/engram#1787](https://github.com/Gentleman-Programming/engram/pull/1787) — `Fixes #1786`

## Progress

- 2026-10-11: exploration and baseline audit complete; 14 agents mapped.
- 2026-10-11: T3V 17 subagents, 366/366 PASS.
- 2026-10-11: T3 blocker README:351 corrected by user; semantic PASS.
- 2026-10-11: T4 real probes with ollama qwen3.6: Pi obs 5179, OpenCode obs 5180.
- 2026-10-11: T2 parent acceptance complete; commit ab7e5d5 + a99d04f; tests 48/48 PASS.
- 2026-10-11: T5 seam checks + semantic assessment PASS. Document complete, ready for final review.
- 2026-10-11: Post-completion cleanup — old doc `memory-protocol-remediation.md` removed, Decision rewritten to describe methodology (not session history), Checks baseline corrected from `d5eedbb..HEAD` → `main..HEAD`, T5 "4 commits" → "8 commits" → "9 commits".
- 2026-10-11: Issue #1786 created (docs_improvement, covers all 4 problems).
- 2026-10-11: PR #1787 created (`Fixes #1786`), branch pushed.
- 2026-10-11: ENHANCE_TYPES — CodeRabbit flagged `hooks: Record<string | symbol, unknown>` (V2 adapter calls them as callable); fixed with `EngramHooks` type (index signature callable + named properties) in both `plugin/opencode/engram.ts` and `internal/setup/plugins/opencode/engram.ts`. Commit `17460ef`.

## Implementation notes

- Shared phrase: `The agent MUST follow this protocol without waiting for activation` present in 6 sources (setup.go,
  engram.ts, DOCS.md, Claude SKILL.md, Codex SKILL.md, session-start.sh).
- Passive capture described as adapter-specific: Claude SubagentStop, OpenCode delegated outputs, Pi explicit capture,
  generic MCP `mem_capture_passive`.
- OpenCode qualified tool names (`engram_mem_save`), Codex MCP namespaces (`mcp__engram__mem_*`,
  `mcp__plugin_engram_engram__mem_*`), Pi native tool names.
- Version bumps: `.claude-plugin/marketplace.json` and `plugin/claude-code/.claude-plugin/plugin.json` from 0.1.7 to
  0.1.8.
- Spanish trigger `"listo"` in session close instructions is a known baseline issue pre-existing in d5eedbb (not
  introduced by this feature).

## Verification evidence

- **T1**: b49a66c — sign-off + SSH signature verified, 4 files.
- **T2**: CodexPreToolUse PASS, RegisteredSessionHandoff 48/48 PASS (T2 assertions: corrected prose present, forbidden
  phrases absent), PromptSubmit* 3/3 PASS.
- **T3**: RFC2119 checker semantic B1-B6 PASS; README:351 capitalization corrected.
- **T3V**: 17 TAP logs, 366/366 tests, 0 failures/skips/cancellations, 30 Pi fingerprints unchanged.
- **T4**: Pi → Ollama → mem_save → obs 5179 (GET /observations/5179 verifies fields). OpenCode HTTP POST /sessions +
  POST /observations → obs 5180. Plugin 57KB structure validated.
- **T5**: Seam checks 6/6 PASS (14 files, no forbidden paths, shared phrase consistent, no ToolSearch in protocols,
  SSH-signed, version bumps). All tests pass.

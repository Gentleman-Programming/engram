# Exploration — setup-protocol-flag

Add `engram setup --protocol=<slim|full>` so the SessionStart hook can emit a slim status block instead of the full ~3.6KB (~900 tokens) ACTIVE PROTOCOL text (session-start.sh ~2.0KB + post-compaction.sh ~1.6KB static heredocs, measured at HEAD — an earlier ~10KB figure came from an older installed plugin version). Downstream consumer contract (three guarantees): `/home/gentleman/work/gentle-ai/openspec/changes/archive/2026-07-08-engram-protocol-dedup/upstream-protocol-flag-contract.md`.

## Current State

**CLI framework**: no cobra/flag package. `cmd/engram/main.go` `main()` switches on `os.Args[1]` (main.go:626); each `cmdX()` hand-parses `os.Args[2:]`.

**`cmdSetup()`** (`cmd/engram/main.go:2334-2381`):
- `len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-")` → direct install (`setupInstallAgent`).
- ELSE (no arg or any `-`-prefixed arg, including `--help`) → interactive menu + `scanInputLine` (`fmt.Scanln`).
- Root cause of `setup --help` printing the menu (verified live on 1.18.0): no explicit `--help`/`-h` case, unlike `cmdSync` (main.go:1431). Guarded today by `TestCmdSetupHyphenArgFallsBackToInteractive` (`cmd/engram/main_extra_test.go:3749`).
- Fix direction: add `--help`/`-h` case before the hyphen branch with a `printSetupUsage()` whose Flags section contains the literal `--protocol` (Guarantee 1).

**Non-TTY stdin (Guarantee 2) — already safe**: with stdin detached, `fmt.Scanln` returns io.EOF immediately → "" → `strconv.Atoi` error → "Invalid choice." + exit(1). No hang. Must be preserved when `--help` gets real handling.

**SessionStart hook / ACTIVE PROTOCOL text — key architectural finding** (mechanism differs per adapter):
- **Claude Code**: `installClaudeCode()` (`internal/setup/setup.go:789`) only runs `claude plugin marketplace add/install` and writes `~/.claude/mcp/engram.json` — no protocol text. The static prose heredocs (~2.0KB in `session-start.sh:141-175`, ~1.6KB in `post-compaction.sh:35-69`, ~3.6KB total, measured at HEAD) live in `plugin/claude-code/scripts/session-start.sh` and `post-compaction.sh` — static files shipped via the plugin marketplace, version-pinned. NOT templated at setup time.
- **Codex**: `installCodex()` writes the `memoryProtocolMarkdown` const (`internal/setup/setup.go:132-223`) to `~/.codex/engram-instructions.md` at setup time; codex plugin script has its own heredoc.
- Other adapters (gemini-cli, antigravity, windsurf, qwen, kiro, cursor, vscode-copilot, kilocode): declarative registry entries (`internal/setup/agents.go`) write `memoryProtocolMarkdown` at setup time — trivially swappable per slug.
- Consequence: for Claude Code, `--protocol=slim` cannot "write different text" — it must persist a MODE the already-installed hook script reads at runtime, from a separate process.

## Persistence mechanism (design decision, options traced)

No user-level settings file exists today. `~/.engram/` (store.DefaultConfig, `internal/store/store.go:454-466`) is the SQLite data dir; project-scoped `.engram/config.json` is the git-synced chunks dir (different thing — do not conflate). Options:
1. New `~/.engram/protocol-mode.json`, read via `jq` in bash — no new binary call, but branching lands in untested bash.
2. Go-owned subcommand `engram protocol-mode <slug>` (same file underneath; hook shells out) — logic fully under `go test`, satisfies Strict TDD. **Explorer recommendation.**
3. Extend `~/.claude/mcp/engram.json` with `protocolMode` — no new file, but semantic mismatch and Claude-Code-only.

## What slim MUST preserve (orchestrator-clarified)

- The dynamic CONTEXT block (`store.FormatContext`, `internal/store/store.go:3298-3366`): Recent Sessions with `[%d observations]`, Prompts, Pinned, Recent Observations.
- Compaction-recovery steps: the unconditional "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN ORDER:" header (`post-compaction.sh:68`) and the numbered recovery steps (`post-compaction.sh:71-81`).
- What slim drops: the static protocol prose (save triggers, tool lists, session-close protocol) — delivered redundantly by the MCP server instructions on engram ≥ v1.4.0.
- There is NO "dynamic import status" for slim to preserve: at HEAD,
  `session-start.sh:124,126,133` redirects `engram sync --import` output
  entirely to `/dev/null` and backgrounds it, so that text never reaches hook
  stdout in any mode. (Earlier live-session evidence showing such output came
  from an older installed plugin version, not HEAD.)

## Test surface

- Go: `cmd/engram/main_extra_test.go` exercises `cmdSetup` branches via injectable vars + capture/exit-panic harness; `internal/setup/setup_test.go` asserts `memoryProtocolMarkdown` substrings per adapter.
- **Bash hooks: ZERO automated coverage** (no bats/shellspec; CI runs only `go test ./...` + one e2e tag) — real Strict-TDD gap if slim/full branching lands in bash.
- Existing `TestCmdSetupHyphenArgFallsBackToInteractive` needs coexistence handling once `--help`/`--protocol` get explicit cases.

## Versioning / follow-ups

- GoReleaser + Conventional Commits; ships as `feat(setup):` in the next minor after 1.18.0.
- `serverInstructions` (`internal/mcp/mcp.go:176`, `server.WithInstructions` at :241) is the always-sent MCP instructions string — out of scope; plausible slim/full follow-up.

## Constraints

- Default MUST remain full (backward compatible); unknown values → full + warning, never fail setup (no-op-safe at every layer: flag parse, file read, hook lookup).
- Strict TDD; repo-local 5-phase SDD flow (explore → propose → apply → verify → archive).
- Guarantee 3 (safest-wins per slug) is enforced upstream by gentle-ai; engram treats `--protocol` as scoped to the setup slug's hook output.

# Proposal: `engram setup --protocol=<slim|full>`

> Repo-local 5-phase flow (explore → propose → apply → verify → archive). No
> separate spec/design/tasks phases — this proposal carries the full approach
> decision detail. Relevant skills (AGENTS.md): `engram-plugin-thin`,
> `engram-testing-coverage`, `engram-architecture-guardrails`,
> `engram-project-structure`.

## Intent

`engram setup` always makes the SessionStart hook emit the full ~3.6KB (~900
tokens) ACTIVE PROTOCOL prose (session-start.sh ~2.0KB + post-compaction.sh
~1.6KB static heredocs, measured at HEAD). On engram ≥ v1.4.0 that prose is already delivered by the MCP
`serverInstructions`, so it is injected twice per session. gentle-ai wants a
side-effect-free way to opt a slug into a slim status block. This change adds
`engram setup <slug> --protocol=<slim|full>` and the runtime read path the
version-pinned Claude Code hook needs to honor it — satisfying the three
downstream guarantees (`upstream-protocol-flag-contract.md`).

## Scope

### In Scope
- `--help`/`-h` case in `cmdSetup`'s token-classification loop (replacing the
  old hyphen branch — see Approach) with `printSetupUsage()` whose Flags
  section contains the literal `--protocol` (**Guarantee 1**); preserve
  non-blocking detached-stdin behavior (**Guarantee 2**).
- `--protocol=<slim|full>` parsing in `cmdSetup`'s hand-rolled `os.Args[2:]`
  loop; strip the flag, treat remaining non-flag arg as the slug.
- Go-owned per-slug persistence in `~/.engram/protocol-mode.json`.
- `engram protocol-mode <slug>` subcommand: single-shell-out read path
  returning `slim`/`full` (default `full`).
- Claude Code `session-start.sh` + `post-compaction.sh`: one shell-out to the
  subcommand; on `slim`, drop the static protocol prose while keeping the
  CONTEXT block (`store.FormatContext`) and, in `post-compaction.sh`, the
  unconditional "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN
  ORDER:" header (`post-compaction.sh:68`) plus the numbered recovery steps
  (`post-compaction.sh:71-81`). At HEAD, `session-start.sh:124,126,133`
  redirects `engram sync --import` output entirely to `/dev/null` and
  backgrounds it — that text never reaches hook stdout in any mode, so there
  is no dynamic import status for slim to preserve. (Earlier live-session
  evidence of such output came from an older installed plugin version, not
  HEAD.) "FIRST ACTION REQUIRED" is Codex/declarative-adapter vocabulary from
  `memoryProtocolMarkdown` (`internal/setup/setup.go:217,229`), out of scope
  here — it does not appear in `post-compaction.sh`.
- Tests: `cmdSetup` flag/help cases (one per token-classification row above),
  mode round-trip, subcommand output, strengthened
  `internal/setup/registry_test.go:139-146` adapter assertions (not
  `setup_test.go`). Named cases: write/read path parity under
  `ENGRAM_DATA_DIR` override; mode=slim + `engram version` check failure →
  subcommand prints `full`; corrupted/malformed `protocol-mode.json` → full;
  missing file → full; missing slug key → full.

### Out of Scope
- **Slim `serverInstructions`** (`internal/mcp/mcp.go:176`) — always-full;
  separate follow-up.
- **Slim variants of `memoryProtocolMarkdown`** for Codex + declarative
  adapters (gemini-cli, antigravity, windsurf, qwen, kiro, cursor,
  vscode-copilot, kilocode). Justification: their protocol text is baked at
  setup time, not read at runtime, so they need no mode file to consume; and
  gentle-ai only ever verifies slim for `claude-code` (contract Guarantee 3).
  The flag is accepted and the mode is persisted uniformly per slug (forward
  compatible), but non-Claude setup-time text stays **full-only** this slice —
  avoids multiplying static text variants and keeps the diff minimal.

## Approach

- **Flag parsing** — extend `cmdSetup` (`main.go:2334-2381`): loop over
  `os.Args[2:]` and classify each token, REPLACING the old
  `os.Args[2]`-only hyphen branch entirely (not adding ahead of it):

  | Token | Action |
  |-------|--------|
  | `--help` / `-h` / `help` (any position) | `printSetupUsage()`, exit 0 |
  | `--protocol=<v>` | record mode from `<v>` |
  | `--protocol <v>` (space-separated) | consume next token as `<v>` |
  | Unknown `-`-prefixed token | preserve today's behavior: fall back to the interactive menu (keeps `TestCmdSetupHyphenArgFallsBackToInteractive` green) |
  | First bare token | slug |
  | Second bare token | error usage |
  | No slug, `--protocol` present | interactive menu; mode applies to whichever slug the user selects |

  Unknown/empty `<v>` → `full` + stderr warning, never fail setup. A
  slug-first invocation (`setup <slug> --protocol=slim`) and a flag-first
  invocation must both reach the same classification, which the loop form
  guarantees. `--help`/`-h` detection scans `os.Args[2:]` at any position,
  mirroring `cmdSync`'s loop (`main.go:1429-1433`) rather than a
  fixed-position check. Interactive fallback and `scanInputLine` EOF-fast
  behavior are unchanged.
- **Mode file** — `~/.engram/protocol-mode.json` (the SQLite data dir from
  `store.DefaultConfig`, `store.go:454-466`; NOT the project `.engram/config.json`
  chunks dir). Shape: `{"<slug>": "slim|full"}`. Setup upserts the slug's mode;
  missing file / missing key / bad JSON → `full`. The writer (in `cmdSetup`)
  and the reader (in `protocol-mode`) MUST consume the same resolved
  `cfg`/`DataDir` that `main()` computes (`main.go:604-624` —
  `ENGRAM_DATA_DIR` override and `resolveHomeFallback`), not a re-derived
  `store.DefaultConfig()`. `cmdSetup` is currently invoked bare at
  `main.go:662`; `cfg` will need to be threaded through.
- **Read path** — new `engram protocol-mode <slug>` subcommand (registered in
  `main()`'s `os.Args[1]` switch, `main.go:626`) prints the mode to stdout.
  Slim suppression is guarded AT HOOK RUNTIME: the subcommand returns `slim`
  only when BOTH (a) the persisted mode for the slug is `slim` AND (b)
  `engram version` (already invocable from the script; the binary is on PATH
  per existing calls) responds successfully with a version ≥ 1.4.0 at that
  moment; any failure, below-floor version, or unparseable output → the
  subcommand prints `full`. All slim/full branching, including the version
  check, lives in Go under `go test`, so the bash diff stays a single
  `$(...)` read + string compare.
- **Hook change** — `session-start.sh` and `post-compaction.sh` add one
  `mode=$(engram protocol-mode claude-code)`; gate ONLY the true static prose:
  `session-start.sh:141-175` and `post-compaction.sh:35-67` (NOT 35-69 — line
  68, the "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN
  ORDER:" header, stays OUT of the gated block, unconditional alongside the
  numbered recovery steps at 71-81, since it is their lead-in). Dynamic
  blocks stay unconditional. Invariant: `$mode` must NEVER be echoed or
  logged to the hook's own stdout — an old binary invoked with an
  unrecognized subcommand dumps `printUsage()` in full to stdout
  (`main.go:667-670,2441`), and hook stdout reaches the model's context;
  `$()` isolation keeps this harmless only as long as `$mode` itself is never
  re-printed.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `cmd/engram/main.go` (`cmdSetup` 2334-2381) | Modified | Token-classification loop replaces the old hyphen branch; `--help`/`-h` + `--protocol` parsing |
| `cmd/engram/main.go` (`main()` switch ~626) | Modified | Register `protocol-mode` subcommand |
| `internal/store/store.go` (or new `internal/setup`) | New | Per-slug mode read/write helper (`~/.engram/protocol-mode.json`), sharing `main()`'s resolved `cfg`/`DataDir` |
| `plugin/claude-code/scripts/session-start.sh` (141-175) | Modified | Gate static heredoc on slim mode |
| `plugin/claude-code/scripts/post-compaction.sh` (35-67) | Modified | Gate static prose only; keep the unconditional header (line 68) and numbered recovery steps (71-81) |
| `cmd/engram/main_extra_test.go` | New tests | Flag/help cases, mode round-trip, subcommand, version-gate |
| `internal/setup/registry_test.go` (139-146) | Modified | Strengthen existing generic adapter-text assertion (full-only) |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Zero bash test coverage for hook branching | High | Keep bash diff minimal — all logic in Go; bash only reads one string + one `[ ]` guard |
| Version-pinned plugin: old hook + new binary (or vice versa) | Med | Missing subcommand / missing mode → `full` (current behavior); no coupling |
| `--protocol` swallowed as slug | Med | Strip flag tokens before slug resolution; test `setup <slug> --protocol=slim` |
| Breaking `TestCmdSetupHyphenArgFallsBackToInteractive` | Med | The token-classification loop replaces the old `os.Args[2]`-only branch; unknown hyphen tokens still fall through to interactive, keeping the test's asserted behavior; extend the test for the new recognized-flag rows |
| Old binary + new hook: unrecognized `protocol-mode` subcommand dumps full `printUsage()` to stdout (`main.go:667-670,2441`), captured into `$mode` by `$()` | Med | Invariant: `$mode` is NEVER echoed/logged to the hook's own stdout (which reaches the model); the garbage value fails the `= slim` check and defaults to full |
| Slim relies on MCP `serverInstructions` for the prose, but MCP health is never verified at session time (`internal/mcp/mcp.go:176,241`) — the hook's only health probe checks the unrelated `engram serve` HTTP daemon, not the per-session `engram mcp` subprocess | Med | Do not gate slim on live MCP health; gate it purely on the local `engram version` check inside `protocol-mode` (≥ 1.4.0 required, decision made by the user). Any check failure emits `full`, so a broken MCP registration means duplicated-but-present prose, never zero prose |

## Rollback Plan

Pure additive. Revert the commit: flag parsing, subcommand, mode file, and hook
guards all vanish; hooks fall back to always-full. The `protocol-mode.json` file
is inert once unread — no migration or cleanup needed.

## Dependencies

- Downstream contract: `upstream-protocol-flag-contract.md` (Guarantees 1-3).
- Ships as `feat(setup):` in the next minor after 1.18.0 (GoReleaser).

## Success Criteria

- [ ] `engram setup --help` stdout contains literal `--protocol` and exits
      without reading stdin (no hang on detached stdin) — Guarantees 1 & 2.
- [ ] `engram setup <slug> --protocol=slim` persists `slim`; round-trips via
      `engram protocol-mode <slug>`.
- [ ] Unknown/absent `--protocol` value → `full` + warning; setup still succeeds.
- [ ] Slim `session-start` output measurably drops the ~3.6KB (~900 tokens) static prose
      (session-start.sh ~2.0KB + post-compaction.sh ~1.6KB) while keeping the
      CONTEXT block and, in post-compaction output, the unconditional
      "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN ORDER:"
      header and numbered recovery steps.
- [ ] `engram protocol-mode <slug>` returns `slim` only when the persisted
      mode is `slim` AND `engram version` reports ≥ 1.4.0 at that moment; any
      check failure returns `full`.
- [ ] `go test ./...` green; non-Claude adapter text unchanged.

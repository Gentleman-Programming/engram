# Verification Report

**Change**: setup-protocol-flag
**Version**: N/A (repo-local 5-phase flow; contract = judgment-day-approved `proposal.md`)
**Mode**: Strict TDD

## Completeness

| Metric | Value |
|--------|-------|
| Approach/Affected-Areas items (apply-progress "Completed Tasks") | 14 |
| Completed | 14 |
| Incomplete | 0 |
| Review-ledger constraints (JD-001..JD-011) | 11 |
| Constraints re-verified honored | 11 |

## Build & Tests Execution

**Build**: PASS — `go build ./...` clean, no output.

**Vet**: PASS — `go vet ./...` clean, no output.

**Tests**: PASS — `go test ./... -count=1` (fresh, no cache): 24 packages, 0 failures.
Also re-ran `go test ./... -race -count=1`: clean (adds confidence around the new
file-based mode read/write path).

```text
ok  cmd/engram              3.836s
ok  internal/cloud/*        (all pass)
ok  internal/mcp            3.992s
ok  internal/setup          0.200s
ok  internal/store          3.694s
ok  internal/sync           1.003s
ok  internal/tui            0.266s
ok  internal/version        0.076s
ok  plugin                  0.021s
(24 packages total, 0 FAIL)
```

Targeted re-run of every named test claimed in apply-progress (`-v`, fresh):
all 17 `cmd/engram` protocol tests + 6 `internal/setup` protocol tests +
`TestInstallDeclarativeAgentsRegisterMCPAndInstructions` (JD-008 registry
strengthening) — 24/24 PASS, including
`TestCmdSetupHyphenArgFallsBackToInteractive` (the guard test named at risk
in proposal.md Risks table) — confirmed still green, unmodified assertions,
only the `cmdSetup(cfg)` call-site signature migrated.

`bash -n` on both hook scripts: PASS (syntax OK), run directly by this
verify phase, not just claimed by apply-progress.

**Coverage**: not measured — Strict TDD module treats coverage as
informational only; no coverage regression signal from this change (all new
production code in `internal/setup/protocol.go` and the new `main.go`
functions is exercised by the 23 new unit tests).

## Live Runtime Evidence (beyond apply-progress's own claims)

Built the actual binary (`go build -o /tmp/.../engram-bin ./cmd/engram`) and
exercised it directly, independent of the Go test harness, to validate the
two literal downstream guarantees at the process boundary:

```text
$ timeout 5 ./engram-bin setup --help </dev/null
usage: engram setup [<agent>] [--protocol=slim|full]
...
Flags:
  --protocol=<slim|full>  ...
exit=0
```

- Guarantee 1 (literal `--protocol` in `--help` stdout): CONFIRMED live —
  `grep -c -- "--protocol"` on real stdout = 2.
- Guarantee 2 (non-blocking on detached stdin): CONFIRMED live — `</dev/null`
  + 5s `timeout` (mirroring gentle-ai's `exec.CommandContext` 5s deadline)
  exits 0 immediately, no hang.
- Gating logic dry-run (extracted `if [ "$mode" != "slim" ]` blocks from both
  hook scripts, executed standalone): `mode=slim` → static prose suppressed,
  header still emitted; `mode=full` / `mode=""` (missing binary) /
  `mode="usage: engram ..."` (old-binary garbage from an unrecognized
  subcommand) → static prose emitted in all three failure shapes, header
  always emitted. Confirms the "any failure path → full" invariant
  structurally, not just by code reading.

## Spec Compliance Matrix (proposal.md Success Criteria)

| Criterion | Test / Evidence | Result |
|-----------|------------------|--------|
| `setup --help` stdout contains literal `--protocol`, no stdin read | `TestCmdSetupHelpAnyPositionShowsProtocolFlagAndSkipsStdin` (4 positions, asserts `scanInputLine` never called) + live binary run above | COMPLIANT |
| `setup <slug> --protocol=slim` persists slim; round-trips | `TestCmdSetupProtocolEqualsFormPersistsSlim`, `TestCmdSetupProtocolSpaceFormPersistsSlim`, `TestCmdSetupProtocolFlagFirstThenSlug`, `TestCmdSetupWriteReadPathParityUnderEnvDataDir` (raw `ReadProtocolMode` confirms persisted=slim; `cmdProtocolMode` on the same cfg correctly returns full because the test binary's `version` is unparseable/dev — this is the version-floor guard working, not a persistence bug) | COMPLIANT |
| Unknown/absent `--protocol` value → full + warning; setup still succeeds | `TestCmdSetupUnknownProtocolValueDefaultsFullWithWarning` | COMPLIANT |
| Every token-classification row has a test | `main.go:2352-2384` 7-row loop vs proposal.md Approach table — verified 1:1, each row traced to a named passing test (--help/-h/help→Help test; `--protocol=`→Equals test; `--protocol <v>`→Space test; unknown `-` token→HyphenArgFallsBack test; first bare token→covered by all slug-based tests; second bare token→SecondBareTokenIsUsageError; no-slug+flag→NoSlugWithProtocolAppliesToSelectedAgent) | COMPLIANT |
| Writer/reader share resolved `cfg`/`DataDir` (ENGRAM_DATA_DIR parity) | `TestCmdSetupWriteReadPathParityUnderEnvDataDir` — mirrors `main()`'s exact `store.DefaultConfig()` + `ENGRAM_DATA_DIR` override resolution (main.go:604-624), writes via `cmdSetup(cfg)`, reads via both raw `ReadProtocolMode(dataDir,...)` and `cmdProtocolMode(cfg)` | COMPLIANT — verified `main.go:661-664` threads the SAME `cfg` from `main()`'s single resolution point into both `cmdSetup(cfg)` and `cmdProtocolMode(cfg)`; no re-derived config anywhere in the diff |
| `protocol-mode` returns slim only under persisted-slim AND in-process version ≥1.4.0, all failure paths → full | `TestCmdProtocolModeSlimAndVersionFloorMet`, `SlimButVersionBelowFloor`, `FullPersistedIgnoresVersion`, `MissingFileDefaultsFull`, `CorruptedJSONDefaultsFull`, `MissingSlugKeyDefaultsFull`, `TestMeetsProtocolVersionFloor` (12-case table incl. `dev`, `""`, `not-a-version`, `v1.4.0`, partial `1.4`) | COMPLIANT — version check confirmed in-process (`meetsProtocolVersionFloor(version)`, package var, no `exec.Command` shell-out to self) |
| Hook gating ranges exactly `session-start.sh:141-175` and `post-compaction.sh:35-67` with line-68 header + steps unconditional | Diffed pre-change HEAD blob directly: `session-start.sh` heredoc was exactly lines 141 (`cat <<'PROTOCOL'`) to 175 (closing `PROTOCOL`) — now wrapped `if [ "$mode" != "slim" ]; then ... fi` around exactly that span. `post-compaction.sh` heredoc was lines 35-69 in HEAD, with line 68 = the "CRITICAL INSTRUCTION..." header living INSIDE the original heredoc, immediately before the closing delimiter at 69. New code SPLIT the heredoc so it closes after the blank line following `---` (content = original 35-67) and moved the header to a separate unconditional `echo` statement outside the `if` block — exactly matching the proposal's "gate 35-67, line 68 header stays unconditional" requirement | COMPLIANT |
| `$mode` never echoed to hook stdout | `rg '\$mode'` on both scripts: every occurrence is inside `[ "$mode" != "slim" ]` comparisons or comments — zero `echo "$mode"` / `printf ... "$mode"` calls | COMPLIANT |
| `go test ./...` green; non-Claude adapter text unchanged | Fresh full-suite run (above) + `TestInstallDeclarativeAgentsRegisterMCPAndInstructions` re-run directly, PASS — strengthened assertions (`SESSION CLOSE PROTOCOL`, `AFTER COMPACTION`) pass against real `memoryProtocolMarkdown`-derived files, not mocked | COMPLIANT |

**Compliance summary**: 9/9 verifiable Success Criteria items COMPLIANT (all 6 proposal.md checkboxes plus 3 additional inline guarantees folded into the Approach section).

## Downstream Contract (upstream-protocol-flag-contract.md, 3 guarantees)

| Guarantee | Result |
|-----------|--------|
| 1 — `--protocol` discoverable via `--help` | HOLDS — confirmed by live binary run, not just unit test |
| 2 — probe runs with stdin detached, no TTY, no hang | HOLDS — confirmed live with `</dev/null` + 5s timeout matching gentle-ai's actual probe shape |
| 3 — per-slug forwarding is safest-wins | N/A at the engram layer by design — proposal.md Constraints explicitly delegates this to gentle-ai; engram's `protocol-mode.json` is keyed purely by slug (`internal/setup/protocol.go` `map[string]string`), carrying no adapter-level granularity, which is the only thing engram needs to guarantee for Guarantee 3 to be enforceable upstream. Verified structurally — no adapter identity anywhere in the mode-file schema. |

## Review Ledger (11 constraints, re-checked against final code)

| id | severity | re-verification |
|----|----------|------------------|
| JD-001 | CRITICAL | `session-start.sh:124,126,133` (pre-change) still redirects `sync --import` to `/dev/null`+background — untouched by this diff; no dynamic-import-status claim reintroduced. HOLDS |
| JD-002 | CRITICAL | Gating range confirmed exactly 35-67 with header split out (see compliance matrix row above). HOLDS |
| JD-003 | CRITICAL | No "FIRST ACTION REQUIRED" string anywhere in the two touched hook scripts' diffs; literal header text used instead. HOLDS |
| JD-004 | CRITICAL | Version-floor guard lives in-process Go (`meetsProtocolVersionFloor`), not shelled out; any failure → full, verified by 6 passing subcommand tests. HOLDS |
| JD-005 | CRITICAL | `main.go:604-624` single `cfg` resolution threaded into both `cmdSetup(cfg)` (line 662) and `cmdProtocolMode(cfg)` (line 664); `TestCmdSetupWriteReadPathParityUnderEnvDataDir` proves it end-to-end under `ENGRAM_DATA_DIR`. HOLDS |
| JD-006 | WARNING | 7-row token-classification table verified 1:1 against `main.go:2352-2384` loop, each row has a named passing test. HOLDS |
| JD-007 | WARNING | Byte-measured pre-change static prose = 2011 + 1491 = 3502 bytes (~3.4KB) vs proposal's "~3.6KB" — close, within measurement-method tolerance (line-range byte count vs an editor's KB rounding); not a material discrepancy. HOLDS (see Suggestion below) |
| JD-008 | WARNING | `registry_test.go:139-146` strengthened assertions re-run directly, PASS against real full-text files, locking non-Claude adapters to full-only. HOLDS |
| JD-009 | WARNING | `$mode` echo invariant confirmed via `rg` (zero stdout echoes) + gating dry-run showing old-binary-garbage failure path safely defaults to full. HOLDS |
| JD-010 | WARNING (theoretical) | `--help`/`-h`/`help` detection scans all of `os.Args[2:]` via the loop (mirrors `cmdSync`'s form), not a fixed position — confirmed in code and via the 4-position `TestCmdSetupHelpAnyPositionShowsProtocolFlagAndSkipsStdin` test. HOLDS |
| JD-011 | SUGGESTION | Corrupted/missing-file/missing-slug-key → full tests all present and passing (both `internal/setup` pure-function level and `cmd/engram` subcommand level). HOLDS |

**No constraint was reopened.**

## Correctness (Static + Runtime Evidence)

| Requirement | Status | Notes |
|------------|--------|-------|
| `cmdSetup` token loop matches Approach table | Implemented | 7/7 rows traced 1:1, no missing/extra branches |
| Mode-file helper (`internal/setup/protocol.go`) | Implemented | Upsert semantics, `normalizeProtocolMode` case-insensitive exact match only, all failure modes → full |
| `cmdProtocolMode` version gate | Implemented | In-process, package `version` var, 12-case table incl. `dev`/`""`/partial/`v`-prefixed |
| Hook scripts | Implemented | Both `bash -n` clean; gating verified structurally + dry-run |
| Out-of-scope items untouched | Confirmed | `git diff --stat` on `internal/mcp/mcp.go`, `internal/setup/setup.go`, `internal/setup/agents.go` = empty (no changes) |

## Coherence (Approach/Design)

| Decision | Followed? | Notes |
|----------|-----------|-------|
| Loop replaces old `os.Args[2]`-only hyphen branch entirely | Yes | Confirmed — no dual-branch leftover in `cmdSetup` |
| Go-owned subcommand, not `jq`-in-bash | Yes | `protocol-mode` subcommand fully in Go, single `$()` read in bash |
| Shared `cfg`/`DataDir`, not re-derived `store.DefaultConfig()` | Yes | Verified line-by-line at call sites |
| `$mode` never echoed to hook stdout | Yes | Verified via grep + dry-run |
| Bash diff kept minimal (all branching in Go) | Yes | Each hook script diff is a `mode=$(...)`/`[ ]` guard + one `if`/`fi` pair around a pre-existing heredoc |

## Issues Found

**CRITICAL**: None

**WARNING**: None

**SUGGESTION**:
- Proposal's "~3.6KB" static-prose figure measures ~3.4KB (3502 bytes) by
  raw byte count of the exact gated line ranges at HEAD. Non-blocking —
  the qualitative claim ("measurably drops the static prose") is
  structurally true regardless of the exact KB figure, and the range was
  already corrected once during judgment-day (JD-007). No action required
  unless the team wants byte-exact precision in future proposals.

## Verdict

**PASS**

All Success Criteria are implemented with passing, non-trivial covering
tests (verified via fresh `-count=1` runs, not cache); `go vet ./...` and
`bash -n` on both hook scripts are clean; all 11 review-ledger constraints
remain honored with no reopened findings; the two literal downstream
guarantees (`--help` discoverability, non-blocking detached-stdin probe)
were additionally confirmed against a freshly built live binary, not only
via the Go test harness; the claimed non-deviation from `proposal.md` is
real — `cmdSetup`'s token loop and the mode-file helper match the Approach
section exactly, including the split-heredoc line-68 handling that was the
subject of two CRITICAL judgment-day findings (JD-002/JD-003).

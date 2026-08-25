# Apply Progress — setup-protocol-flag

Mode: **Strict TDD**. Test runner: `go test ./...`. Contract: `proposal.md`
(Approach section — token-classification table, mode-file spec, runtime
version-guard, hook gating ranges). All items below implement that contract
as written; no deviations.

## Completed Tasks

- [x] `cmdSetup` (`main.go`) rewritten as a token-classification loop over
      `os.Args[2:]`, replacing the old `os.Args[2]`-only hyphen branch.
- [x] `--help` / `-h` / `help` recognized at ANY position → `printSetupUsage()`,
      exit cleanly (no `exitFunc`), Flags section contains literal `--protocol`.
- [x] `--protocol=<v>` and `--protocol <v>` (space form) both parsed; flag can
      appear before or after the slug.
- [x] Unknown hyphen-prefixed token → interactive fallback preserved
      (`TestCmdSetupHyphenArgFallsBackToInteractive` still green, unmodified
      assertions — only the call site now threads `cfg`).
- [x] Second bare token → usage error to stderr + `exitFunc(1)`.
- [x] No slug + `--protocol` present → interactive menu; mode applies to
      whichever slug the user selects (`cmdSetupInteractive`).
- [x] Unknown/empty `--protocol` value → normalizes to `full` + stderr
      warning; setup always still succeeds.
- [x] Mode file `~/.engram/protocol-mode.json` (`{"<slug>":"slim|full"}`) —
      new `internal/setup/protocol.go`: `ReadProtocolMode` / `WriteProtocolMode`.
      Missing file / missing key / corrupted JSON / unknown value → `full`.
      Write path is an upsert (other slugs' entries survive).
- [x] Writer (`cmdSetup`/`applyProtocolMode`) and reader (`cmdProtocolMode`)
      both consume the SAME `cfg store.Config` main() resolves (`ENGRAM_DATA_DIR`
      override included) — `cmdSetup` signature changed from `cmdSetup()` to
      `cmdSetup(cfg store.Config)`, threaded from `main()`'s switch (JD-005).
- [x] New `engram protocol-mode <slug>` subcommand (`main()` switch), wired at
      the same level as `setup`. Prints `slim` only when persisted mode is
      `slim` AND `meetsProtocolVersionFloor(version)` (>= 1.4.0) — both parts
      evaluated in-process in Go, no shelling out to self.
- [x] `meetsProtocolVersionFloor` pure function: parses `"1.4.0"`, `"v1.5.2"`,
      `"1.4"` (partial, treated as `.0`), rejects `"dev"`, `""`, unparseable
      strings → `false`.
- [x] `session-start.sh`: added `mode=$(engram protocol-mode claude-code
      2>/dev/null); [ "$mode" != slim ] && mode=full` guard; gated ONLY the
      static heredoc (was 141-175) behind `if [ "$mode" != "slim" ]; then ...
      fi`. `$mode` never echoed to hook stdout.
- [x] `post-compaction.sh`: same guard; gated ONLY the static heredoc content
      (was 35-67, i.e. through the blank line after `---`). The unconditional
      "CRITICAL INSTRUCTION POST-COMPACTION..." header (was line 68) and the
      numbered recovery steps (was 71-81) stay OUTSIDE the `if` — verified by
      manual dry-run (both `mode=slim` and `mode=full` paths).
- [x] `internal/setup/registry_test.go:139-146` strengthened: declarative
      (non-Claude) adapters now also assert `SESSION CLOSE PROTOCOL` and
      `AFTER COMPACTION` markers are present, locking them to full-only text
      (JD-008 — these adapters are out of scope for `--protocol=slim`).
- [x] Bash: zero new test coverage added (repo has no bats/shellspec, matches
      the accepted Risk row "Zero bash test coverage for hook branching" —
      mitigation is that all branching logic lives in Go under `go test`; bash
      only reads one `$()` string + one `[ ]` guard). Verified both scripts
      with `bash -n` (syntax) and a manual dry-run of the gating logic showing
      slim drops the static prose while full/error-paths default safely.

### Tests written (RED → GREEN, one file for pure mode-file logic, one for
CLI wiring)

`internal/setup/protocol_test.go` (6 tests, pure, `t.TempDir()`):
- `TestReadProtocolModeMissingFileDefaultsToFull`
- `TestReadProtocolModeMissingSlugKeyDefaultsToFull`
- `TestReadProtocolModeCorruptedJSONDefaultsToFull`
- `TestWriteProtocolModeRoundTrip`
- `TestWriteProtocolModeUnknownValueNormalizesToFull`
- `TestWriteProtocolModePreservesOtherSlugs`

`cmd/engram/setup_protocol_test.go` (17 tests, one per token-classification
row + subcommand + version-floor table):
- `TestCmdSetupHelpAnyPositionShowsProtocolFlagAndSkipsStdin` (4 arg
  permutations; asserts `scanInputLine` is never called — Guarantee 2)
- `TestCmdSetupProtocolEqualsFormPersistsSlim`
- `TestCmdSetupProtocolSpaceFormPersistsSlim`
- `TestCmdSetupProtocolFlagFirstThenSlug`
- `TestCmdSetupSecondBareTokenIsUsageError`
- `TestCmdSetupUnknownProtocolValueDefaultsFullWithWarning`
- `TestCmdSetupNoSlugWithProtocolAppliesToSelectedAgent`
- `TestCmdSetupWriteReadPathParityUnderEnvDataDir` (mirrors `main()`'s exact
  `store.DefaultConfig()` + `ENGRAM_DATA_DIR` override resolution)
- `TestCmdProtocolModeSlimAndVersionFloorMet`
- `TestCmdProtocolModeSlimButVersionBelowFloor`
- `TestCmdProtocolModeFullPersistedIgnoresVersion`
- `TestCmdProtocolModeMissingFileDefaultsFull`
- `TestCmdProtocolModeCorruptedJSONDefaultsFull`
- `TestCmdProtocolModeMissingSlugKeyDefaultsFull`
- `TestMeetsProtocolVersionFloor` (table-driven, 12 cases)

Plus approval-style updates (signature migration, no behavior change) to
existing tests: `TestCmdSetupHyphenArgFallsBackToInteractive`,
`TestCmdSetupDirectAndInteractive`, and one `cmdSetup()` call inside
`TestCommandErrorSeamsAndUncoveredBranches` — all now call `cmdSetup(cfg)`.

## TDD Cycle Evidence

| Task | Test File | Layer | Safety Net | RED | GREEN | TRIANGULATE | REFACTOR |
|------|-----------|-------|------------|-----|-------|-------------|----------|
| Mode file R/W | `internal/setup/protocol_test.go` | Unit | ✅ full repo `go test ./...` green before starting | ✅ Written (undefined symbols) | ✅ Passed | ✅ 6 cases (missing file/key/corrupted JSON/round-trip/unknown-value/multi-slug) | ➖ None needed |
| `cmdSetup` token classification | `cmd/engram/setup_protocol_test.go` | Unit (CLI, capture+panic harness) | ✅ N/A (existing `cmdSetup()` tests re-run as approval baseline before signature change) | ✅ Written (build fail on `cmdSetup(cfg)` / undefined `cmdProtocolMode`, `meetsProtocolVersionFloor`) | ✅ Passed | ✅ 8 cases across help/equals/space/flag-first/second-bare/unknown-value/no-slug+flag/env-parity | ✅ Extracted `cmdSetupInteractive`, `resolveProtocolModeFlag`, `applyProtocolMode` as named pure/thin helpers |
| `cmdProtocolMode` + version floor | `cmd/engram/setup_protocol_test.go` | Unit | N/A (new subcommand) | ✅ Written | ✅ Passed | ✅ 6 cases (slim+floor-met, slim+floor-not-met, full-ignores-version, missing file, corrupted JSON, missing slug) + 12-case version-floor table | ➖ None needed |
| `registry_test.go` strengthening | `internal/setup/registry_test.go` | Unit (approval) | ✅ 8/8 declarative-agent subtests passing before edit | ➖ N/A — strengthened existing passing assertion (approval-test style, no production change) | ✅ Passed unchanged (proves adapters already emit full text) | ➖ Single (assertion addition, not new behavior) | ➖ None needed |
| Hook scripts (bash) | none (repo has no bash test harness) | Manual | ✅ `bash -n` both scripts before/after | N/A (bash, no test runner) | ✅ Manual dry-run of gating logic (slim/full/missing-binary) | ✅ 3 cases (slim, full, missing binary) via isolated snippet run | ➖ Diff kept to minimum per Risk mitigation |

### Test Summary
- **Total tests written**: 23 new Go tests (6 in `internal/setup`, 17 in `cmd/engram`) + 2 new assertions in an existing test + 3 existing tests updated for signature migration.
- **Total tests passing**: full repo `go test ./...` green (24 packages, no failures) both before and after the change.
- **Layers used**: Unit (23 new + strengthened), Manual/bash dry-run (2 hook scripts, no automated harness exists in this repo).
- **Approval tests**: 1 (`registry_test.go` strengthening — behavior unchanged, assertions widened).
- **Pure functions created**: `meetsProtocolVersionFloor`, `resolveProtocolModeFlag`, `normalizeProtocolMode` (unexported in `internal/setup`).

## Files Changed

| File | Action | What Was Done |
|------|--------|----------------|
| `internal/setup/protocol.go` | Created | `ReadProtocolMode`/`WriteProtocolMode` + `ProtocolModeSlim`/`ProtocolModeFull` consts, `protocol-mode.json` upsert logic. |
| `internal/setup/protocol_test.go` | Created | 6 unit tests for the mode-file R/W helper. |
| `internal/setup/registry_test.go` | Modified | Strengthened the adapter-instruction assertion (JD-008) to lock declarative adapters to full-only text. |
| `cmd/engram/main.go` | Modified | `cmdSetup` rewritten as token-classification loop, now takes `cfg store.Config`; added `cmdSetupInteractive`, `printSetupUsage`, `resolveProtocolModeFlag`, `applyProtocolMode`, `cmdProtocolMode`, `meetsProtocolVersionFloor`, `protocolVersionFloor`; wired `case "setup": cmdSetup(cfg)` and new `case "protocol-mode": cmdProtocolMode(cfg)` in `main()`. |
| `cmd/engram/setup_protocol_test.go` | Created | 17 unit tests covering every token-classification row, the mode-file write/read parity under `ENGRAM_DATA_DIR`, the `protocol-mode` subcommand, and the version-floor table. |
| `cmd/engram/main_extra_test.go` | Modified | Migrated 3 existing `cmdSetup()` call sites to `cmdSetup(cfg)` (signature change only, no assertion changes). |
| `plugin/claude-code/scripts/session-start.sh` | Modified | Added `mode=$(engram protocol-mode claude-code ...)` guard; gated the static protocol heredoc (was 141-175) behind `if [ "$mode" != "slim" ]`. |
| `plugin/claude-code/scripts/post-compaction.sh` | Modified | Same guard; gated only the static heredoc content (was 35-67); the "CRITICAL INSTRUCTION..." header (was 68) and numbered recovery steps (was 71-81) remain unconditional. |

## Deviations from Design

None — implementation matches `proposal.md` Approach section exactly,
including the exact gating line ranges, the token-classification table, the
write/read `cfg.DataDir` parity requirement, and the version-floor guard
living entirely in Go.

One scope call made explicitly, not a deviation: `printUsage()` (top-level
`engram help`) was left unchanged — it already lists `setup [agent]` and the
new `protocol-mode` subcommand is a hook-internal implementation detail, not
meant for interactive discovery. `proposal.md`'s Affected Areas table does
not list `printUsage()` or `docs/`, so this was treated as out of scope
rather than expanded silently.

## Issues Found

None. One pre-existing, unrelated flake was investigated and ruled out: `go
test ./cmd/engram/... -cover` intermittently reports package-level `FAIL`
with zero `--- FAIL` lines (autosync port-binding noise in the log). Verified
via `git stash` that this reproduces identically on unmodified `main` HEAD —
not caused by this change. The mandated verification command, plain `go test
./...` (no `-cover`), is green both before and after.

## Remaining Tasks

None — all proposal Approach/Affected-Areas/Tests items implemented.

## Workload / PR Boundary

- Mode: single PR (repo-local 5-phase flow; delivery strategy from SDD init
  session was `exception-ok`, but the actual diff is well under budget).
- Current work unit: entire `setup-protocol-flag` change (Go CLI + mode file
  + hook scripts), matches proposal scope in full.
- Boundary: this apply batch starts from proposal approval (2 judgment-day
  rounds, both APPROVED) and ends with a fully green `go test ./...` + clean
  `go vet ./...`. No follow-up apply batch needed for this change.
- Estimated review budget impact: `git diff --stat` on tracked files = 5
  files changed, 236 insertions(+), 21 deletions(-); plus 3 new untracked Go
  files (~360 lines of new tests + ~95 lines of new production code in
  `internal/setup/protocol.go`). Comfortably under the 400-line review budget
  even counting new files.

## Status

All implementation items complete. `go test ./...` green (24 packages, 0
failures). `go vet ./...` clean. Both hook scripts pass `bash -n` and a
manual gating dry-run. Ready for `sdd-verify`.

## Correction — Implementation Round 1 judgment-day (JD-012..JD-016)

A second judgment-day round over the uncommitted apply diff found 5 issues
(`review-ledger.md`), all now fixed:

- **JD-012 (CRITICAL, missed in the original apply)**: `shouldCheckForUpdates`
  (`main.go`) was updated for `mcp`/`serve`/`cloud serve` in an unrelated
  earlier change, but this apply batch never added `protocol-mode` to that
  exclusion switch, even though it added the new `protocol-mode` subcommand
  and explicitly designed it as a hot hook-invocation path ("single `$()`
  read + string compare"). Root cause: the new subcommand's tests all call
  `cmdProtocolMode(cfg)` directly, bypassing `main()`'s update-check gate
  entirely, so the missing exclusion had zero test coverage and was not
  caught before this correction. Fixed by adding `"protocol-mode"` to the
  `case "mcp", "serve":` branch in `shouldCheckForUpdates`.
- **JD-013 (WARNING)**: `WriteProtocolMode` now writes via a temp file in
  the same directory + `os.Rename` (atomic on POSIX), and returns an error
  instead of silently overwriting a corrupted mode file (which would have
  wiped any recoverable data for other slugs).
- **JD-014 (WARNING)**: the unrecognized-flag interactive fallback now
  forwards the already-parsed `--protocol` mode instead of hardcoding `""`.
- **JD-015 (WARNING, theoretical)**: the space-form `--protocol` no longer
  swallows a following flag token as its value; `--protocol --help` now
  shows usage as expected.
- **JD-016 (SUGGESTION)**: added the three missing edge-case tests
  (dangling `--protocol` as last token, duplicate `--protocol` flags with
  last-wins, slug-then-unknown-flag interactive fallback).

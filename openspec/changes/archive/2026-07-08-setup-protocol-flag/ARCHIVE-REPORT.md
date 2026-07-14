# Archive Report — setup-protocol-flag

**Change**: setup-protocol-flag  
**Archived**: 2026-07-08  
**Status**: Complete (SDD cycle closed)  
**Commit**: 875890e on branch feat/setup-protocol-flag  

---

## Executive Summary

The `setup-protocol-flag` change has been fully planned, implemented, verified, and archived. The feature adds runtime protocol mode selection to engram's setup command, allowing downstream hook consumers (Claude Code's `session-start.sh` and `post-compaction.sh`) to opt into a slim status block that drops ~3.6KB (~900 tokens) of redundant static ACTIVE PROTOCOL prose, while preserving the dynamic CONTEXT block and critical compaction-recovery instructions. The implementation passes all verifications (Strict TDD, live binary probe validation, judgment-day approval across implementation rounds) with zero CRITICAL issues.

---

## What Shipped

### CLI and Subcommand
- **`engram setup --protocol=<slim|full>`**: Flag parsing via hand-rolled token-classification loop in `cmdSetup` (replaces old `os.Args[2]`-only hyphen branch)
  - Accepts `--protocol=slim`, `--protocol=full`, or space-form `--protocol slim`
  - Appears at any position in command-line arguments
  - Unknown/empty values normalize to `full` with stderr warning; setup always succeeds
  - `--help` / `-h` / `help` at any position prints `printSetupUsage()` (containing literal `--protocol` in Flags section) and exits cleanly without reading stdin

- **`engram protocol-mode <slug>`**: New subcommand that reads and returns the persisted protocol mode for a given slug
  - Returns `slim` only when BOTH (a) persisted mode is `slim` AND (b) `engram version` reports ≥ 1.4.0 at invocation time
  - Any failure (missing/corrupted mode file, unparseable version, version < 1.4.0) returns `full`
  - All version-floor logic lives in Go under test; bash only reads one `$()` string + one `[ ]` guard

### Persistence
- **Mode file**: `~/.engram/protocol-mode.json`
  - Shape: `{"<slug>": "slim|full"}`
  - Upsert semantics: new slug entries are added, existing entries updated, unmodified entries preserved
  - Missing file / missing key / corrupted JSON → defaults to `full`
  - Writer (`cmdSetup`) and reader (`cmdProtocolMode`) share main's resolved `cfg.DataDir` (including `ENGRAM_DATA_DIR` override) — no re-derived config

### Hook Integration (Claude Code Plugin)
- **`plugin/claude-code/scripts/session-start.sh`**:
  - Added `mode=$(engram protocol-mode claude-code 2>/dev/null)` to capture the mode
  - Gated the static protocol prose heredoc (lines 141–175 pre-change) behind `if [ "$mode" != "slim" ]`
  - CONTEXT block and dynamic content remain unconditional

- **`plugin/claude-code/scripts/post-compaction.sh`**:
  - Same guard pattern; gated only the static heredoc content (lines 35–67 pre-change)
  - The unconditional "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN ORDER:" header (line 68 pre-change) and numbered recovery steps (71–81) remain OUTSIDE the conditional, always emitted

### Test Coverage
- **6 unit tests** in `internal/setup/protocol_test.go` (pure mode-file R/W logic)
- **17 unit tests** in `cmd/engram/setup_protocol_test.go` (CLI token classification, subcommand, version floor)
- **1 existing test strengthened** (`registry_test.go:139–146` — declarative adapter assertions locked to full-only text)
- **3 existing tests updated** for `cmdSetup` signature migration (approval-style, no behavior change)

---

## Verification Outcome

### Test Results
- **`go test ./... -count=1`**: PASS (24 packages, 0 failures)
- **`go test ./... -race -count=1`**: PASS (extra confidence on file I/O path)
- **`go vet ./...`**: PASS (clean)
- **`bash -n`** on both hook scripts: PASS (syntax OK)

### Live Binary Validation
- **Guarantee 1 (--protocol discoverable)**: Confirmed live via `./engram-bin setup --help </dev/null` — stdout contains literal `--protocol`, exit 0
- **Guarantee 2 (non-blocking detached stdin)**: Confirmed live — `</dev/null` + 5s timeout exits immediately, no hang
- **Guarantee 3 (per-slug forwarding)**: Verified structurally — mode-file schema keyed only by slug, no adapter granularity; engram's responsibility limited to round-tripping the flag, enforcement upstream in gentle-ai

### Spec Compliance
All 9 Success Criteria from proposal.md implemented and verified:
1. `setup --help` contains `--protocol`, no stdin read → ✅ Live binary + 4 test positions
2. `setup <slug> --protocol=slim` persists, round-trips → ✅ Equality and space-form tests, env parity test
3. Unknown/absent → full + warning, setup succeeds → ✅ Test suite covers all null/empty/unrecognized paths
4. Slim drops ~3.6KB static prose, keeps CONTEXT + compaction header/steps → ✅ Gating ranges verified 1:1, dry-run confirmed
5. `protocol-mode` returns slim only under both conditions, any failure → full → ✅ 6 subcommand tests + 12-case version-floor table
6. Token-classification loop matches table → ✅ 7/7 rows traced to named tests
7. Writer/reader share resolved cfg/DataDir → ✅ `ENGRAM_DATA_DIR` parity test confirms no re-derivation
8. Hook gating ranges exactly 35–67 + line-68 unconditional → ✅ Diffed pre-change blob, confirmed exact ranges
9. `go test ./...` green, non-Claude text unchanged → ✅ Full suite green, adapter tests PASS

### Review Ledger
- **Proposal round**: 11 CRITICAL/WARNING/SUGGESTION constraints (all resolved by judges)
- **Implementation round 1 (judgment-day parallel with verify)**: 5 findings (JD-012 through JD-016)
  - JD-012 (CRITICAL): Missing `protocol-mode` in `shouldCheckForUpdates` exclusion — **fixed** (added to mcp/serve case)
  - JD-013 (WARNING): Atomic file write via temp + rename, error on corrupted file — **fixed**
  - JD-014 (WARNING): Order-dependent flag forwarding in interactive fallback — **refactored** to two-pass order-independent classification
  - JD-015 (WARNING theoretical): Space-form flag could swallow next flag — **fixed** (now checks for `-` prefix before consuming next token)
  - JD-016 (SUGGESTION): Missing edge-case tests — **added** (dangling flag, duplicate flags, slug-then-unknown-flag fallback)
- **Terminal state**: JUDGMENT APPROVED (implementation)
- **No CRITICAL issues reopened**

---

## Files Changed

| File | Action | Impact |
|------|--------|--------|
| `cmd/engram/main.go` | Modified | `cmdSetup` rewritten as 7-row token-classification loop; added `printSetupUsage()`, `cmdSetupInteractive()`, `resolveProtocolModeFlag()`, `applyProtocolMode()`, `cmdProtocolMode()`, `meetsProtocolVersionFloor()`, and related helpers; wired `case "protocol-mode"` in main switch; added `protocol-mode` to `shouldCheckForUpdates` exclusion (JD-012 fix) |
| `internal/setup/protocol.go` | Created | `ReadProtocolMode()` / `WriteProtocolMode()` helpers; mode-file shape, upsert logic, atomic write via temp+rename (JD-013 fix); default-to-full on any error path; `normalizeProtocolMode()` for case-insensitive matching |
| `internal/setup/protocol_test.go` | Created | 6 unit tests covering missing file, missing slug key, corrupted JSON, round-trip persistence, unknown-value normalization, multi-slug preservation |
| `cmd/engram/setup_protocol_test.go` | Created | 17 unit tests for CLI token classification (8 cases), subcommand (6 cases), version floor (12-case table); live env-parity test under `ENGRAM_DATA_DIR` |
| `cmd/engram/main_extra_test.go` | Modified | Approval-style signature migration: 3 existing `cmdSetup()` call sites updated to `cmdSetup(cfg)` (no assertion changes); added `TestUpdateChecksSkipCriticalStartupCommands` (JD-012 fix) |
| `internal/setup/registry_test.go` | Modified | Strengthened existing adapter assertions to verify `SESSION CLOSE PROTOCOL` and `AFTER COMPACTION` markers are present (locking non-Claude adapters to full-only text, JD-008 compliance) |
| `plugin/claude-code/scripts/session-start.sh` | Modified | Added `mode=$(engram protocol-mode claude-code 2>/dev/null)` guard; wrapped static heredoc (141–175) in `if [ "$mode" != "slim" ]` conditional |
| `plugin/claude-code/scripts/post-compaction.sh` | Modified | Added same guard; wrapped static heredoc (35–67) conditionally; split header ("CRITICAL INSTRUCTION...") and numbered steps (71–81) to stay unconditional |

---

## Effectiveness & Guarantees

### Downstream Contract (upstream-protocol-flag-contract.md)

| Guarantee | Status | Evidence |
|-----------|--------|----------|
| 1 — `--protocol` discoverable via `--help` | Holds | Live binary confirms `grep --protocol` returns 2 matches in stdout; exit 0, no hang |
| 2 — Probe runs with stdin detached, no TTY, no hang | Holds | Live binary with `</dev/null` + 5s timeout; immediate exit 0 |
| 3 — Per-slug forwarding with safest-wins policy | Holds | Mode file keyed by slug only; gentle-ai enforces adapter-level safest-wins upstream |

### Rollback Safety
- Pure additive: revert the commit to restore all-full behavior
- Mode file `~/.engram/protocol-mode.json` becomes inert once unread — no migration/cleanup required
- Hook scripts revert to unconditional full prose

---

## Release & Follow-ups

### Scheduled Release
- **Ship as**: `feat(setup):` tag in next minor version after 1.18.0 (via GoReleaser)
- **Downstream probe activation**: gentle-ai v1.19+ (or next minor after current) will detect engram ≥ 1.4.0 with protocol-mode support and activate the downstream protocol-dedup probe

### Future Work (Out of Scope)
- **Slim `serverInstructions`** (`internal/mcp/mcp.go:176`): Currently always-full; plausible follow-up to drop redundant MCP text in slim mode
- **Slim variants for declarative adapters** (Codex, gemini-cli, etc.): Currently full-only; forward-compatible (flag accepted, mode persisted uniformly) but baked at setup time rather than runtime-read, lower priority

---

## Artifacts Archived

- ✅ `proposal.md` — Complete change intent, scope, approach, risks, rollback
- ✅ `apply-progress.md` — All 14 Approach/Affected-Areas items completed; TDD cycle evidence; 23 new tests + 3 signature migrations + 1 assertion strengthening; zero deviations
- ✅ `verify-report.md` — All Success Criteria verified; build/test/vet/bash green; live binary probe confirms Guarantees 1 & 2; all 11 review constraints re-verified honored
- ✅ `review-ledger.md` — 16 entries across proposal (11) and implementation (5) rounds; no CRITICAL reopenings; terminal state JUDGMENT APPROVED
- ✅ `explore.md` — Architectural findings (hook runtime read vs setup-time templating), persistence mechanism trade-offs, test surface analysis

---

## Conclusion

The `setup-protocol-flag` change successfully closes the 5-phase SDD cycle: explored (architectural and mechanism decisions), proposed (token-classification loop, mode file, runtime hook guard, version floor), applied (strict TDD implementation with zero deviations), verified (spec compliance matrix + live binary validation + judgment-day re-judge), and archived (all artifacts collected, verified for quality, persisted for audit trail).

The feature is ready to ship in the next minor release and activate the downstream gentle-ai protocol-dedup probe. No follow-up work is required on engram side; slim `serverInstructions` and declarative adapter variants are noted as future optimization opportunities, not blockers.

**SDD cycle: CLOSED**

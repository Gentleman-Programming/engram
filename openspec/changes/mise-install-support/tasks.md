# Tasks: mise install support

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~459 total as single PR (~120 / ~290 / ~49 per unit) |
| 400-line budget risk | Medium |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 → PR 2 → PR 3, stacked to main |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: Medium

### Suggested Work Units

| Unit | Goal | Likely PR | Focused test command | Runtime harness | Rollback boundary |
|------|------|-----------|----------------------|-----------------|-------------------|
| 1 | `mise.toml` pins + `scripts/verify-mise-pins.sh` + ci.yml step | PR 1 | `./scripts/verify-mise-pins.sh` | Run on real repo tree (pass) + 4 mutated scratch copies (fail-closed) | Delete `mise.toml`, revert ci.yml step, delete script |
| 2 | `internal/version/mise.go` detector + `check.go` hint wiring | PR 2 | `go test ./internal/version/...` | N/A — pure env/path logic fully covered by unit tests | Revert `mise.go`, `mise_test.go`, `check.go` clause, `check_test.go` additions |
| 3 | `docs/INSTALLATION.md` mise section + version fix + `README.md` pointer | PR 3 | N/A (docs-only) | N/A — reviewer readback | Revert docs commit; version fix may stay |

All 3 units are independent — each PR targets `main` directly; order is a suggested review sequence, not a dependency. Use skill `chained-pr` for each PR's Chain Context section at apply time.

## Phase 1: Toolchain Pins + CI Drift Guard (PR 1)

- [ ] 1.1 Create `mise.toml` at repo root: `go = "1.25.10"`, `node = "24"` (REQ-MISE-001)
- [ ] 1.2 Write `scripts/verify-mise-pins.sh` (`extract_one` for single-occurrence sites + new `extract_agreed` for Go's cross-file agreement rule); `chmod +x` (REQ-MISE-002)
- [ ] 1.3 On scratch copies, verify the guard fails closed: go drift; deleted `go-version:` line; duplicate disagreeing `node-version:` (both well-formed, different values); a duplicated `node-version:`/`go-version:` line with unsupported syntax (unquoted, `${{ }}`) — previously invisible to the count, must now die explicitly; missing `mise.toml` (REQ-MISE-002 scenarios)
- [ ] 1.4 Run the guard on the real repo tree; confirm exit 0 with no drift output (REQ-MISE-002 clean-tree scenario)
- [ ] 1.5 Add a "Verify mise pins" step to `.github/workflows/ci.yml`'s `unit-tests` job, between "Set up Go" and "Run unit tests" (REQ-MISE-003)

## Phase 2: Mise-Managed Detection + Update-Hint Wiring (PR 2)

- [ ] 2.1 [RED] `internal/version/mise_test.go`: table-driven `miseInstallsRoot(goos)` precedence (`MISE_INSTALLS_DIR` > `MISE_DATA_DIR/installs` > `XDG_DATA_HOME/mise/installs` > platform default), whitespace fallthrough, Windows branch, empty-home case (REQ-MISE-004)
- [ ] 2.2 [RED] Add `pathContains` cases: under root, root itself, sibling false, symlinked ancestor, nonexistent root
- [ ] 2.3 [RED] Add `runningBinaryIsMiseManaged()` cases via `currentExecutableFn`/`userHomeDirFn` package-var swap
- [ ] 2.4 [GREEN] Implement `internal/version/mise.go` (vars, `miseInstallsRoot`, `pathContains`, `runningBinaryIsMiseManaged`) — pass 2.1-2.3
- [ ] 2.5 [RED] Extend `TestUpdateInstructions` in `check_test.go`: mise-managed output contains `mise upgrade engram`; non-mise output stays byte-identical (REQ-MISE-005)
- [ ] 2.6 [GREEN] Add guard clause atop `updateInstructions()` (`check.go:150`) returning the two-line mise hint before the unchanged GOOS switch — pass 2.5

## Phase 3: Documentation (PR 3)

- [ ] 3.1 Add a mise section to `docs/INSTALLATION.md` for macOS, Linux, and Windows using `mise use -g github:Gentleman-Programming/engram@latest`; note that `mise use -g` only registers the pin and does not by itself put the binary on `PATH` without `mise activate` (shell integration) or shims configured, and give `mise exec -- engram version` as the no-activation fallback (REQ-MISE-006)
- [ ] 3.2 Fix Requirements: `Go 1.24+` → `Go 1.25.10` at line 167 (REQ-MISE-006)
- [ ] 3.3 Add a one-line mise pointer to `README.md`'s install section (REQ-MISE-006)

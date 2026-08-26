# Apply Progress: Mise Install Support

## Current branch

`feat/mise-toolchain-pins`

## Chain context

- Strategy: stacked-to-main (all 3 PRs are independent; each targets `main` directly)
- Current slice: PR 1 — toolchain pins + CI drift guard
- Out of scope for this slice: `internal/version` mise-managed detector and update-hint wiring (PR 2), `docs/INSTALLATION.md` / `README.md` mise coverage (PR 3)

## Progress

### Completed (Phase 1, tasks 1.1-1.5)

- 1.1: Added `mise.toml` at repo root with a `[tools]` table pinning `go = "1.25.10"` and `node = "24"`, matching `go.mod`'s `go` directive and `publish-pi.yml`'s `node-version`.
- 1.2: Added `scripts/verify-mise-pins.sh` (`chmod +x`), implementing `extract_one` (single-occurrence pin sites: `go.mod`'s `go` directive, `mise.toml`'s `go`/`node` pins, `publish-pi.yml`'s `node-version`) and `extract_agreed` (cross-file agreement for every `go-version:` occurrence across `ci.yml` and `release.yml`). `extract_agreed`'s `key_pattern` matches the KEY only; a separate value-extraction step dies explicitly on an unsupported format instead of silently excluding it from the comparison, per design.md's documented bugfix.
- 1.3: Verified fail-closed behavior on 5 scratch-copy scenarios (not committed to the repo, built under the session scratchpad): go drift (`mise.toml` pin vs `go.mod`), a deleted `go-version:` line in `release.yml` (missing pin site), a duplicate disagreeing well-formed `node-version:` in `publish-pi.yml`, a duplicated `go-version:` line with an unsupported value format (`${{ env.GO_VERSION }}`) in `ci.yml`, and a missing `mise.toml`. All 5 exited non-zero with a message naming the disagreeing/missing/duplicate site.
- 1.4: Ran the guard on the real repo tree — exit 0, `mise pins: go=1.25.10 node=24 agree across go.mod, ci.yml, release.yml, publish-pi.yml and mise.toml`.
- 1.5: Added a "Verify mise pins" step to `.github/workflows/ci.yml`'s `unit-tests` job, between "Set up Go" and "Run unit tests".

### Work Unit Evidence

| Evidence | Value |
|---|---|
| Focused test command and exact result | `./scripts/verify-mise-pins.sh` on the real repo tree → exit 0, no drift output |
| Runtime harness command/scenario and exact result | 5 scratch-copy fail-closed scenarios (go drift, missing pin, well-formed duplicate, unsupported-format duplicate, missing `mise.toml`) — all exited non-zero with a descriptive message; built under the session scratchpad, never committed |
| Rollback boundary | Revert 3 commits: `mise.toml`, `scripts/verify-mise-pins.sh`, and the `ci.yml` step; no other files touched |

### Validation run

```bash
go build ./...
./scripts/verify-mise-pins.sh
```

Result: build OK; guard exit 0 on the real tree.

Strict TDD note: Phase 1 has no Go test runner target (shell script + config only). Task 1.3's scratch-copy fail-closed verification is the safety net/acceptance-proof equivalent for this unit, matching design.md's Testing Strategy table ("Guard — fail-closed: Manual verification on a scratch copy").

## Remaining work

- Phase 2 (PR 2): `internal/version/mise.go` mise-managed install detector (RED/GREEN per strict TDD) and `updateInstructions()` guard-clause wiring in `check.go`.
- Phase 3 (PR 3): `docs/INSTALLATION.md` mise section, Go version fix at line 167, `README.md` pointer.

## Risks

- None. Implementation matches design.md exactly, including the `extract_one`/`extract_agreed` key/value split fix.

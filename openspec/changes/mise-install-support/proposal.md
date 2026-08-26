# Proposal: mise install support

> Relevant repo-local skills (AGENTS.md): `engram-project-structure` (first
> `scripts/` dir, new `internal/version` file), `engram-docs-alignment`
> (INSTALLATION.md / README), `engram-testing-coverage` (detector tests).

## Intent

engram supports Homebrew, `go install`, and raw binaries, but has no
first-class mise path and no repo-declared toolchain. Worse, `updateInstructions()`
(`internal/version/check.go:150`) branches only on `runtime.GOOS`, so a
mise-managed install is told to `brew upgrade` / `go install` a binary mise
already owns. Add mise as a documented install method, pin the toolchain, and
make the update hint truthful per install method.

## Scope

### In Scope
- `mise.toml` at repo root: `go = "1.25.10"`, `node = "24"`.
- `scripts/verify-mise-pins.sh` (first script in the repo) + a step in ci.yml's
  `unit-tests` job — a required check per `CONTRIBUTING.md:59`.
- mise section in `docs/INSTALLATION.md` (Windows included) + pointer in
  `README.md`; fix the pre-existing `Go 1.24+` drift at `docs/INSTALLATION.md:167`.
- `internal/version/mise.go` detector + `updateInstructions()` emitting
  `mise upgrade engram`; `internal/version/mise_test.go`.

### Out of Scope
- External aqua-registry / jdx/mise registry PRs (non-code, handled elsewhere).
- Any self-upgrade preflight or skip logic: engram's notifier only writes to
  stderr (`cmd/engram/main.go:713`) and never replaces its own binary.
- `CONTRIBUTING.md` — no Prerequisites section exists to extend.

## Capabilities

### New Capabilities
- `mise-toolchain-support`: mise-managed-install detection driving update
  instructions, repo toolchain pins, and their CI drift guard.

### Modified Capabilities
- None.

## Decisions (previously open)

| Question | Decision |
|---|---|
| Windows in mise docs? | **Yes.** `.goreleaser.yaml` ships `windows/amd64`+`arm64`. gentle-ai's "Windows out of scope" precedent does not transfer, so the detector needs a Windows branch (`%LOCALAPPDATA%\mise\installs`, falling back to `~\AppData\Local\...`) that the sibling `internal/update/upgrade/mise.go` omits. |
| `INSTALLATION.md:167` "Go 1.24+" drift | **In scope.** One line, in a doc already being edited; `engram-docs-alignment` would flag it otherwise. Align to `go.mod`'s `go 1.25.10`. |
| `pi-v*` tags | Verified: `publish-pi.yml` runs only `npm publish` under `permissions: contents: read` — **no GitHub Release object at all**, not merely no assets. Release-API resolution ignores them; see Risks. |
| Node pin authority | `.github/workflows/publish-pi.yml:20` — the repo's only Node pin. **Not** `ci.yml`; a straight copy of the sibling script would check the wrong file. |
| Go pin authority | **All four**: `go.mod:3`, `ci.yml:23`, `ci.yml:38`, `release.yml:23`. |

## Approach

Import gentle-ai's `scripts/verify-mise-pins.sh` shape (`set -euo pipefail`,
`BASH_SOURCE` repo root, `die()`, fail-closed extraction, diff-style errors) but
replace its "exactly one match per file" rule for Go: engram repeats the version
literally in three workflow lines, so the guard asserts every `go-version:`
occurrence across ci.yml and release.yml is identical and equal to both `go.mod`'s
directive and mise.toml's pin, with zero occurrences a failure. Node keeps the
exactly-one rule, against `publish-pi.yml`.

The detector lives in `internal/version/mise.go` (same package as its only
consumer), following mise's own precedence: `$MISE_INSTALLS_DIR` →
`$MISE_DATA_DIR/installs` → `$XDG_DATA_HOME/mise/installs` → platform default
(Windows `%LOCALAPPDATA%\mise\installs`, else `~/.local/share/mise/installs`),
treating whitespace-only values as unset. engram has no `pathidentity` package;
design picks either a ported containment helper or a `filepath.Clean` prefix
check — lower stakes here than in gentle-ai because the only consumer is a
printed hint. Tests reuse `check_test.go`'s var-swap + `t.Setenv` idiom.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `mise.toml` | New | go/node pins mirroring go.mod + publish-pi.yml |
| `scripts/verify-mise-pins.sh` | New | Drift guard; first script in the repo |
| `.github/workflows/ci.yml` (`unit-tests`, 12-26) | Modified | Guard step |
| `internal/version/mise.go` | New | Installs-root resolution + containment check |
| `internal/version/check.go:150` | Modified | mise branch before the GOOS switch |
| `internal/version/mise_test.go` | New | Env-precedence + containment cases |
| `docs/INSTALLATION.md` | Modified | mise section; Requirements drift fix |
| `README.md` | Modified | One-line mise pointer |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Docs land before the external registry PRs, documenting a command that does not resolve yet | Med | Document a registry-independent form (e.g. mise's `ubi:` backend) alongside the short name; design confirms it resolves engram's goreleaser archives |
| A tag-based version source would surface `pi-v*` tags, which have no Release and no asset | Low | Registry entry must use a release-based version source; docs never promise `pi-v*` resolves |
| Windows detection untested on a real Windows host (CI is ubuntu-only) | Med | Pure env/path logic, table-driven with `t.Setenv`; no filesystem dependence beyond the containment check |
| Guard step added to a required check can block unrelated PRs on toolchain bumps | Low | Error message names both files and the exact expected value; fixing drift is a one-line edit |
| Third pin source: mise.toml adds a pin nothing else reads | Med | The guard is the only thing keeping it honest — that is its whole purpose |

## Rollback Plan

Per scope item, each independently revertible:
1. **mise.toml** — delete it. Nothing reads it; CI still provisions Go/Node via
   the `setup-go`/`setup-node` literals.
2. **Guard + CI step** — revert the ci.yml step and delete the script. No other
   job depends on it; `unit-tests` returns to its 3-step form.
3. **Docs** — revert the commit; purely additive text. The `Go 1.24+` → `1.25.10`
   correction is independently correct and may stay.
4. **Detector** — revert `mise.go` + the `check.go` branch. Restores today's
   GOOS-only hint. No persisted state, no schema, no binary mutation to undo.

## Dependencies

- External aqua-registry and jdx/mise registry PRs for the short-name install
  form (`mise use -g engram`) to resolve. Tracked outside this repo.
- Existing `.goreleaser.yaml` release assets (all six OS/arch archives).

## Success Criteria

- [ ] `mise.toml` pins match `go.mod:3` and `publish-pi.yml:20`.
- [ ] `scripts/verify-mise-pins.sh` passes on a clean tree and fails with an
      actionable message when any of the five pin sites drifts.
- [ ] The guard runs in the `unit-tests` job on every PR.
- [ ] A mise-managed binary prints `mise upgrade engram`; Homebrew/`go install`
      installs print today's message unchanged.
- [ ] Detector honors `MISE_INSTALLS_DIR` → `MISE_DATA_DIR` → `XDG_DATA_HOME` →
      platform default (Windows branch covered), treating blank values as unset.
- [ ] `docs/INSTALLATION.md` documents mise for macOS, Linux, and Windows, and
      the Requirements section matches `go.mod`.
- [ ] `go test ./...` green.

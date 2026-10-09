# feat(dashboard): show running server version in chrome

Issue: [#1657](https://github.com/Gentleman-Programming/engram/issues/1657)
Branch: `feat/dashboard-show-server-version`
Route: delegated direct (writer for ≥2 non-trivial files)

## Problem

The Engram Cloud web dashboard does not display the running server version
anywhere. Operators must SSH into the host and run `engram --version` to know
which build is serving a given deployment. The TUI already prints the version
(`tui.New(s, version)` in `cmd/engram/main.go:114`); the dashboard has no
equivalent wiring today.

## Scope

Add a `Version` field to `dashboard.MountConfig`, plumb it through
`cloudserver` via a new `WithVersion(string)` Option, and render it in the
dashboard `Layout` footer plus `LoginPage`. No behavior change outside the
cloud dashboard UI. No public API renames.

## Constraints

- One PR per the issue-first workflow — link with `Closes #1657`.
- One `type:feature` label on the PR.
- `MountConfig.Version` is optional (zero value = omit rendering).
- The change must compile without modifying any of the 22 existing
  `MountConfig{...}` test fixtures.
- Generated templ files (`*_templ.go`) must be regenerated, not hand-edited.
- `make deadcode-check` must pass.
- Conventional commit: `feat(dashboard): show running server version in chrome`.

## Tasks

- [ ] **T1 — MountConfig.Version field**
  Add `Version string` to `dashboard.MountConfig` in
  `internal/cloud/dashboard/dashboard.go`. Documented as optional.

- [ ] **T2 — Layout renders version (RED-GREEN)**
  Add a failing test in `internal/cloud/dashboard/` that asserts the
  rendered Layout HTML contains the version when `MountConfig.Version` is
  set, and omits the version block when empty. Then implement the
  conditional render in `layout.templ` and regenerate `layout_templ.go`.

- [ ] **T3 — LoginPage renders version (RED-GREEN)**
  Same RED-GREEN cycle in `login.templ` / `login_templ.go` so unauthenticated
  operators also see the build. Empty version: omit.

- [ ] **T4 — cloudserver.WithVersion option**
  Add `version string` field on `*CloudServer`, a `WithVersion(string) Option`
  (follow the `WithMaxPushBodyBytes` pattern at `cloudserver.go:174`), and
  pass `s.version` into `dashboard.MountConfig{Version: ...}` at
  `cloudserver.go:268`.

- [ ] **T5 — cmd/engram/cloud.go wires version**
  Add `cloudserver.WithVersion(version)` to the slice returned by
  `cloudRuntimeServerOptions` in `cmd/engram/cloud.go`. The local `version`
  variable is already in scope.

- [ ] **T6 — Verification**
  Run focused tests + deadcode check, record outcomes:
  - `go test ./internal/cloud/dashboard/...` — must pass
  - `go test ./internal/cloud/cloudserver/...` — must pass
  - `make deadcode-check` — must pass (no new unreachable symbols)

- [ ] **T7 — Work-unit commit**
  Single conventional commit on `feat/dashboard-show-server-version`:
  `feat(dashboard): show running server version in chrome`. No
  `Co-Authored-By` trailer.

- [x] **T8 — Local Docker smoke + v-prefix normalization fix (discovered after T7)**
  Discovered during a local `docker compose -f docker-compose.cloud.yml`
  smoke test with `VERSION=v3.0.0-test`:
  - ldflags injects `main.version` with a literal `v` prefix (`v3.0.0-test`).
  - The Layout / LoginPage templates render `· v{ version }`, which
    produces a double `v` (`vv3.0.0-test`).
  - Fix: keep `v{ version }` in the templates (matches the established test
    fixture convention `Version: "1.2.3-test"` → renders as `v1.2.3-test`)
    and normalize inside `WithVersion` with `strings.TrimPrefix(..., "v")`.
  - Both `v1.20.3` (ldflags shape) and `1.20.3` (post-debug.ReadBuildInfo
    fallback shape) are now accepted by `WithVersion`.
  - Added `TestWithVersionNormalizesLeadingV` in
    `internal/cloud/cloudserver/cloudserver_test.go` with 5 sub-cases
    (with-v / without-v / with-whitespace / empty / only-whitespace).
  - Local smoke: rebuilt docker image with `VERSION=v3.0.0-test`, dashboard
    footer now renders `<small>... LIVE SYNC READY · v3.0.0-test</small>`
    (single v, correct).
  - All checks green: 4 layout tests + 5 WithVersion sub-tests pass;
    `make deadcode-check` clean; full dashboard + cloudserver suites green.
  - Pre-existing inconsistency in `cmd/engram/main.go`: ldflags leaves the
    `v` prefix on `main.version`, while `debug.ReadBuildInfo` strips it.
    Out of scope for this change — flagged for a follow-up if Alán wants
    the CLI / TUI / dashboard paths to converge.

- [ ] **T9 — Amend T7 commit with T8 fix**
  Amend `5a078ec` with the v-prefix normalization fix + new test.
  Single atomic work-unit commit remains.

## Out of scope

- Per-page version display (only chrome: Layout footer + LoginPage).
- Version injection via `/dashboard/health` JSON — already considered and
  rejected in the issue.
- Any non-dashboard consumer of `version`.
- Documentation updates beyond the issue itself (the change is self-evident
  from the diff).

## Acceptance

- Issue #1657 is approved (`status:`).
- All tasks above complete.
- Verification commands observed and recorded.
- Single work-unit commit, ready for PR.
- 22 existing `MountConfig{...}` test fixtures compile unchanged.
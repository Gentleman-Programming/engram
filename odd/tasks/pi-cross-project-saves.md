# Feature: pi-cross-project-saves

Locator: `odd/tasks/pi-cross-project-saves.md` — Engram mirror topic `odd/pi-cross-project-saves/tasks` (project `engram`).
Branch: `fix/pi-cross-project-saves`.

## Objective

A Pi session bound to project A can save memories into an explicitly targeted project B (another repo/worktree) without a `session_project_conflict`, while every session keeps single-project ownership.

## Problem

`mem_save` with `project: "engram"` from a session owned by `gentle-pi` calls `registeredSessionForWrite(activeProject)` (`plugin/pi/index.ts` ~1606), which re-registers the SAME runtime session id as `project_owned` under the new project (~1097). The client guard `sessionProjectConflict` (~1011) or the server (409 `session_project_conflict`, `internal/server/server.go` ~548) rejects it, so the memory ends up in the wrong project.

## Why

`observations.project` is already independent of `sessions.project`. The `project_owned` invariant is intentional (prevented gentle-shell/gentle-pi contamination) and must stay.

## Decision (user, 2026-10-01)

Option 1 — explicit routing only. Target project comes from an explicit `project` or `cwd` argument. NO automatic inference from edited files: working in gentle-ai while patching gentle-pi must not make gentle-pi the default; only explicitly targeted saves go there.

## Scope

- Pi plugin: when the write target project differs from the runtime session's project, register and use a derived, stable, `project_owned` satellite session (`<runtimeId>@<project>`, no directory) and write with it.
- `cwd` argument on Pi write tools (at least `mem_save`), resolved through the same project resolution as `/project/current`.
- Tests and docs for the above.

## Non-goals

- No server ownership-rule relaxation; no switching Pi sessions to `shared`.
- No auto-inference from touched paths.

## Tasks

- [x] T1 — Satellite session routing for explicit foreign `project` on Pi write tools (mem_save, mem_save_prompt, mem_session_summary; mem_capture_passive takes no project and is unchanged), with RED→GREEN tests. Route: delegated (writer; index.ts + tests). Commit: `0f5fdf2` (shared coherent T1/T2 work unit).
- [x] T2 — `cwd` argument resolving the target project via `/project/current`, tests, and README/DOCS update. Route: delegated (writer). Commit: `0f5fdf2` (shared coherent T1/T2 work unit).

- [x] T3 — After T1/T2 closure, fix Unix-socket test fixtures for HTTP serving, idempotent close, and stale-socket replacement without weakening directory trust validation. Route: delegated exploration/writer/verification (security-sensitive filesystem contract). User authorized this follow-up. Independent and native verification approved; committed as `5aa207e`.

- [x] T4 — Resolve full-suite blockers in CLI socket fixtures and autosync `TestManagerStopForUpgradeHaltsCycle`. User explicitly authorized investigation and corrections after full suite reported 27 passing packages, 2 failures and 3 packages without tests. Route: delegated exploration, bounded writer, independent verification. Independent verification and native review `review-fbee4168a9483776` approved/acknowledged; race x20 and Pi272/272 pass. Commit/overall closure pending. Preserve socket trust protection and concurrency semantics. Commit: pending.

- [ ] T5 — Investigate and correct the remaining Claude hook capture failure: `TestSubagentStopExplicitEmptyMessageSelection/primary_wins` at `plugin/claude_code_hook_behavior_test.go:598` receives an empty capture body. User authorized this follow-up. Route: delegated read-only diagnosis, bounded writer and verification. No causality established yet; preserve empty-message selection semantics. If Claude plugin source changes, apply the required synchronized version bumps. Commit: pending.

## Acceptance criteria

- Session owned by A, `mem_save(project: B)` → observation stored in B under a B-owned satellite session; no 409; runtime session still owned by A.
- Same-project saves unchanged (same session id).
- Satellite id is stable/idempotent across repeated saves.
- `mem_save(cwd: <repo B path>)` routes to B.

## Checks

- `cd plugin/pi && npm test`
- `go test ./internal/server/... ./internal/store/...` if server touched.

## Delivery

Forecast ~250–400 authored lines. Strategy: exception-ok (user selected one PR). Push/PR are user decisions (issue-first per CONTRIBUTING.md).

## Progress

- 2026-10-01: exploration done, decision recorded (engram obs 21106), branch created.
- 2026-10-01: T1+T2 implemented by delegated writer; commits left to the parent (writer role forbids git add/commit).
- 2026-10-01: Corrected verifier P2/P3 with user-authorized server scope: POST /sessions preserves omitted/empty/whitespace directories as empty, while non-empty normalization is unchanged. Pi satellites omit directory; cwd only resolves the target project. Replaced the old omitted-directory normalization expectation and added runtime-candidate checks for both create/resume, plus project/cwd satellite wire assertions. Previously persisted nonblank directories are not cleared by renewal.

## Implementation notes

- Server only rejects blank session IDs (`internal/store/store.go` `validateSessionID`), so `@` is valid. The final isolated-registration contract preserves empty satellite directories; ordinary runtime registration retains its original server-cwd/worktree normalization. This supersedes the earlier blanket blank-directory handler correction.
- Runtime owner = local registered/in-flight owner, else persisted pending owner (ownerless stays unresolved → fail closed), else resolved detected project. Unknown owner keeps the legacy path (explicit project claims the runtime session).
- Satellites: `<runtimeID>@<project>`, `project_owned`, `resume: true` (ended satellites adopt `:resume:N`), renewed on every write (30-minute local lease), concurrent writes coalesce, ended on quit (not on reload).
- Only explicit `project`/`cwd` routes to satellites; implicit tool writes and prompt/passive hooks keep the runtime session.
- `cwd` + `project` disagreement (case-insensitive) and unresolved/ambiguous `cwd` fail before any registration.
- Existing tests that encoded "explicit foreign project fails" were rewritten to the new contract; two continuation-race tests now use ambiguous detection to keep exercising the legacy runtime-claim path.

## Verification evidence

- RED T1: 7/8 new tests failed with `Pi runtime session runtime-a belongs to Engram project project-a, not project-b` (same-project test passed).
- GREEN T1: `npm test` 248/248 after rewriting 8 legacy expectations.
- RED T2: 4/5 new cwd tests failed (cwd ignored). GREEN T2: `npm test` 253/253.
- Risk tier: medium-high (session ownership contract); independent verification recommended.
- P2/P3 correction RED: Go omitted/empty/whitespace cases failed in both create/resume modes (stored server cwd and appeared as runtime candidates); Pi project/cwd satellite wire assertions failed (251/253 passed).
- P2/P3 correction GREEN: focused Go directory regression passed (10 subcases); Pi npm test passed 253/253; go vet passed. Required Go package run still fails two unrelated Unix-socket tests (`socket parent hierarchy is writable by an untrusted user`); store passes. `gofmt -l internal` lists only untouched `internal/diagnostic/checks_test.go`, `internal/store/session_identity_repair_test.go`, and `internal/store/sync_apply_test.go`.

## Compatibility correction (implementation ready; closure pending)

- User authorized additional server/plugin/store/tests correction after independent verification failed on old-server normalization and existing nonblank satellite renewal.
- `GET /health` now advertises `capabilities.isolated_session_registration: true`. Pi requires the literal boolean before each satellite registration flight, including renewal, then sends `isolated: true`. Missing, false, and malformed capability values fail closed with upgrade guidance; no version floor or automatic project inference was added.
- `Store.RegisterIsolatedSession` uses the existing registration transaction to validate the requested root and selected continuation before ownership repair, sync mutations, or lease renewal. Any existing nonblank directory returns `409 session_isolation_conflict`; directories are never silently cleared. New/blank satellites retain empty directory. Ordinary runtime registrations keep their original normalization and ownership rules.
- RED: `cd plugin/pi && npm test` failed 7 new assertions (252/259 passed): absent isolated wire flag and missing/false/malformed capability allowed registration. `go test ./internal/server/... ./internal/store/...` failed the new health capability, runtime-bound root/continuation, ended root, and invalid isolated request regressions, in addition to the two known socket environment failures.
- GREEN: final `cd plugin/pi && npm test` passed 260/260. Final Go package run passed store and all isolation/directory regressions; server still fails only `TestUnixSocketServesHTTPWithRestrictivePermissions` and `TestUnixSocketCloseIsIdempotent`, both with `engram server: socket parent hierarchy is writable by an untrusted user` (known on HEAD 4cb56ac). `go vet ./internal/server/... ./internal/store/...` and `git diff --check` passed. Changed Go files were normalized with targeted gofmt.
- TRIANGULATE: coverage includes capability recheck on renewal, same-project success without capability, rejected registration preserving session/lease/sync journal, ownerless legacy roots not repaired, new and existing empty satellites, server-selected continuations, whitespace directory inputs, and runtime candidate exclusion.
- Intermediate validation found two satellite fixtures missing the newly required capability (fixed without weakening assertions), and one transient failure in `a later ownership conflict on an uncertain replacement revokes shutdown end across reload`; that unchanged test passed on both subsequent full Pi runs. Root cause of that transient result is not established.
- No staging or commits performed. Existing unrelated dirty/untracked work preserved. Compatibility implementation is ready for parent review, but overall task closure remains pending.

## Independent-gate correction (closure pending)

- P1: Satellite registrations now supply a transport-only pre-dispatch capability guard. It runs before every POST attempt, including refusal recovery/reconnect, outside transport-error classification. A guarded request permits only one recovery replay; ordinary runtime registration and other POST semantics remain unchanged. Corrected core still enforces `isolated: true` atomically.
- P2: Satellite acknowledgements require the exact root or `:resume:` followed only by one or more ASCII decimal digits. This matches core's arbitrary-precision allocator, including existing zero/leading-zero numeric identities. Empty, nonnumeric, negative, and trailing suffixes cannot authorize observation writes or shutdown cleanup.
- Lease evidence: Server rejection compares full session structs rather than JSON (which omits `RuntimeLeaseExpiresAt`). Direct store regression snapshots every sessions and sync_mutations field before/after a rejected runtime-bound continuation, including ownerless continuation; lease, ownership, and journal stay unchanged.
- Real-server evidence: Extended the existing native persistence harness (its Go bridge builds successfully inside the test sandbox, no skip) to persist a B observation under the B-owned satellite, assert empty satellite directory, and compare principal runtime A before/after unchanged. Core handler tests directly assert `ActiveRuntimeSessions` excludes isolated satellites; the Pi bridge has no runtime-candidate HTTP endpoint, so integration proves the empty-directory premise rather than invoking that store method through a new test framework.
- RED: `cd plugin/pi && npm test` observed seven intended new failures (260/267 passed): five malformed acknowledgements wrote successfully and both changing-server transport fixtures bypassed capability validation. Recovery fixture was then narrowed to the actual two-attempt registration policy so the replacement is reached immediately after recovery.
- GREEN: Final `cd plugin/pi && npm test` passed 272/272, including real-server build/persistence, changing-server retry/recovery refusals, capable-server recovery, bounded persistent refusal, numeric acknowledgement edge cases, and malformed-ack cleanup denial.
- Added Go evidence regressions pass without core changes (test-only evidence expansion, not an invented RED). `go test ./internal/server/... ./internal/store/...` passes store and isolation regressions, but still fails only the two known Unix-socket hierarchy tests. Socket sources/tests remain untouched; T3 is not started.
- Intermediate checks: one Pi run timed out after an unconditional optional-callback await changed unguarded dispatch scheduling; guard now awaits only when supplied, preserving ordinary runtime scheduling. The next run caught an extracted-source test helper still stripping the old recursive TypeScript call; helper updated to the new signature, then full suite passed.
- Final normalization: `gofmt -w internal/store/isolated_session_test.go internal/server/isolated_session_test.go`. `go vet ./internal/server/... ./internal/store/...` and `git diff --check` passed.
- T1/T2 remain unchecked for parent closure. Dirty work preserved; no staging/commits.

## Verified boundary

T1/T2 committed together as `0f5fdf2` to preserve the tested coherent contract. Pi 272/272 and real-server integration passed; store, vet and diff-check passed. Server has only two proven baseline failures assigned to T3. Independent targeted verification PASS. Native review `review-bb33ea57146a0faf` approved; acknowledgement burned authority. Nonblocking advisory: `R3-satellite-key-collision` at `plugin/pi/index.ts:1174`, separate later work. Authored commit lines: 1224; single-PR exception retained.

T3 read-only diagnosis: `muplmbtp-a-az69`; no socket changes yet.

## Remaining full-suite gate

T3 independent socket tests PASS (6 top-level + 6 policy subcases), server package PASS, Pi 272/272, vet and diff-check PASS. Native T3 review `review-38aa747720e5f3c1` approved and acknowledged. Full `go test -count=1 ./...` fails in `cmd/engram` (socket hierarchy trust error; matching fixture `cmd/engram/serve_socket_test.go:69`) and `internal/cloud/autosync` (`TestManagerStopForUpgradeHaltsCycle`: expected disabled, got healthy). Baseline/candidate causality of these remaining failures is not yet established. T3 commit remains pending. User authorized both follow-up fixes; diagnosis task `mupmgxug-e-dp3x` is running. Windows checks not run.

## Latest gate and authorized follow-up

Independent T4 PASS: autosync race x20, CLI/autosync packages, Pi272/272, vet and diff-check pass. Full Go suite:28 packages passed,1 plugin failure,3 no tests. Remaining failure is Claude `TestSubagentStopExplicitEmptyMessageSelection/primary_wins`, empty body causing JSON decode failure. No rerun or causal assertion. User authorized T5; scout `mupn6n91-h-u6g0` running. T4 bytes remain reviewed and uncommitted; T3 commit `5aa207e`. Windows not run.

## Delivery disposition

User authorized closing verified work and creating/merging a PR. T4 committed as `66ed312` after independent and native approval. T1/T2: `0f5fdf2`; T3: `5aa207e`. T5 is deferred, NOT fixed: focused test, plugin package and diagnostic overlay each passed once; overlay captured only expected GET project/current and valid passive POST JSON. Original intermittent failure remains unexplained. Last independent full Go suite had 28 passing packages, 1 plugin failure, 3 packages without tests; writer full run passed earlier. No claim of final overall green. Pi272/272 and autosync race x20 passed. Windows local checks not run. Merge requires fresh CI and ordinary repository policy; no gate bypass.

Repository issue-first policy requires a matching approved issue before PR creation; searched related issues are broader or already closed, so do not close them falsely. Single-PR size exception was explicitly selected by user. Known `.codegraph/` remains excluded.

## Next step

Parent: independently verify and natively review T4, then commit T3 (server fixtures only) separately from T4 (CLI fixture and autosync pause precedence). Record commit hashes before closing T4; its checkbox remains pending. No rollback quiescence is claimed.

## T3 — Private Unix-socket fixtures (implementation ready)

- Parent diagnosis found rejected `t.TempDir()` leaves with mode `0775`, UID/EUID 1000, and no sticky bit; inspected ancestors and `*syscall.Stat_t` assertions passed. The reason for the leaf mode is not established. Production correctly rejects non-sticky group-writable directories.
- RED: `go test -count=1 -v ./internal/server -run '^TestUnixSocket(ServesHTTPWithRestrictivePermissions|ReplacesOnlyStaleSockets|CloseIsIdempotent)$'` observed 2 failures (HTTP serving and idempotent close: untrusted writable parent hierarchy) and 1 pass (stale replacement). Parent's earlier full uncached run also failed stale replacement; this run does not establish a deterministic failure for that fixture.
- Test-only fix: reusable `os.MkdirTemp(os.TempDir(), "engram-uds-")` fixture with cleanup, observed `0700` permission assertion, and full existing hierarchy trust validation. It fails rather than repairs an unsafe directory. No shared ancestor chmod, production exemption, or skipped negative case.
- The three affected success fixtures use the helper. Active-listener rejection and regular-file preservation also use a trusted parent so their intended negative contracts are not masked by hierarchy rejection. The six-case trust-policy table remains unchanged, including foreign sticky ownership and non-sticky group/world write rejection. Added dedicated fixture trust-compatibility and no-premature-socket coverage; retained the Unix build tag.
- GREEN: `go test -count=1 -v ./internal/server -run '^TestUnixSocket'` passed 6 top-level tests and all 6 policy subcases (0 failures). `go test -count=1 ./internal/server/...` and `go test -count=1 ./internal/store/...` each passed their package. `go vet ./internal/server/...` and `git diff --check` passed; targeted `gofmt -w internal/server/unix_socket_test.go` applied.
- T1/T2 sources untouched. Existing task-document changes and untracked `.codegraph/` preserved. No staging/commits; parent owns final review and commit. Fixture intentionally fails if the host temp hierarchy is unsafe; no Windows validation claimed for this Unix-only test change.

## T4 — CLI fixture and autosync pause precedence (implementation ready)

- Pre-edit CLI RED: `go test -count=1 -v ./cmd/engram -run '^TestCmdServeSignalClosesUnixSocket$'` exited 1 with `engram: engram server: socket parent hierarchy is writable by an untrusted user`. This exercised the unchanged CLI fixture at HEAD `0f5fdf2` with dirty T3 server fixtures preserved; no isolated clean-baseline run is claimed.
- CLI fix: private `os.MkdirTemp` parent with cleanup and explicit `0700` assertion. SIGTERM and socket removal expectations remain. No production trust changes or shared-directory chmod.
- Autosync deterministic RED: `go test -count=1 -v ./internal/cloud/autosync -run '^TestManager(StopForUpgrade|Resume)'` exited 1. An admitted cycle blocked at PullMutations, then StopForUpgrade ran before release/join. Success overwrote disabled with healthy; failure with pull_failed/transport_failed; blocked-after-success with push_failed/non_enrolled_pending_mutations; panic with backoff/internal_error. Pushing, pulling, backoff and blocked transition cases also overwrote the pause.
- Fix: one mutex-held helper protects phase and pause reason while disabled across intermediate phase writes, outcome recorders and panic recovery. Outcome metadata and existing store persistence remain active. StopForUpgrade does not cancel/drain admitted work, invoke Stop, or release the acquired lease; this is status precedence, not rollback quiescence.
- Tests: entered/release/done barriers with watchdog-only timeouts and cleanup release/join; assert exact pause phase/reason/message after admitted work, zero transport calls on another disabled cycle, resumed work, persisted success/failure/blocked outcomes, panic failure count and previously acquired lease retention. Replaced sleep-based stop/resume evidence.
- GREEN: both exact focused commands above passed; `go test -race -count=20 ./internal/cloud/autosync -run '^TestManager(StopForUpgrade|Resume)'` passed. `go test -count=1 ./cmd/engram/... ./internal/cloud/autosync/... ./internal/server/... ./internal/sync/...` and `go test -count=1 ./...` passed. `go vet ./cmd/engram/... ./internal/cloud/autosync/...` and `git diff --check` passed. Only changed Go files normalized with targeted gofmt.
- T3 server fixture bytes and unrelated `.codegraph/` preserved. No staging/commits, clean-baseline archive, Windows validation or independent parent proof claimed. T4 stays unchecked until parent verification/review/commit. Scope remains below the advisory 400-line budget.

## Key Learnings

1. A cycle admitted before StopForUpgrade can finish afterward; guarding admission alone cannot preserve the disabled status.
2. Upgrade pause precedence protects phase and reason under the same mutex without discarding legitimate sync outcome persistence.
3. Private socket fixtures must satisfy real directory trust checks; creating and asserting a private parent avoids weakening production validation.

# Apply Progress: doctor-diagnostic

## Mode

Standard (no `openspec/config.yaml`; focused Go test runner available, but strict TDD mode was not configured).

## Completed Tasks

- [x] 1.1 Diagnostic shared types and runner.
- [x] 1.2 Deterministic registry and invalid-check error.
- [x] 1.3 Store read-only session evidence helper.
- [x] 1.4 Pending mutation helper and pure required-field validator.
- [x] 1.5 SQLite lock snapshot helper.
- [x] 2.1 `session_project_directory_mismatch` check.
- [x] 2.2 `manual_session_name_project_mismatch` check.
- [x] 2.3 `sync_mutation_required_fields` check.
- [x] 2.4 `sqlite_lock_contention` check.
- [x] 2.5 Default registry wiring for all MVP check IDs.
- [x] 3.1 CLI `engram doctor` with text, `--json`, `--project`, and `--check`.
- [x] 3.2 Main command dispatch/help entry.
- [x] 3.3 MCP `mem_doctor` registration and handler.
- [x] 4.1 Store helper tests.
- [x] 4.2 Diagnostic package tests.
- [x] 4.3 CLI contract tests.
- [x] 4.4 MCP contract tests.
- [x] 4.5 Explicit CLI JSON vs MCP `mem_doctor` normalized envelope parity test.
- [x] 4.6 Explicit `mem_doctor` omitted-project auto-detection test.
- [x] 4.7 Deterministic `sqlite_lock_contention` warning/error branch tests via a test seam.
- [x] 4.8 Broader healthy `RunAll` scenario covering all four MVP checks.
- [x] 4.9 Deterministic adapter-level blocked envelope test for `sync_mutation_required_fields` using a pending malformed mutation fixture.
- [x] 5.1 Doctor CLI/MCP docs.
- [x] 5.2 Manual verification matrix.
- [x] 5.2a Root `spec.md` reconciled as a canonical-spec pointer to avoid stale HTTP/check-ID ambiguity.

## Remaining

- None.

## Validation

- `go test ./internal/diagnostic ./internal/store ./internal/mcp ./cmd/engram` — passed.
- `git diff --check` — passed.

## Verification Gap Fixes

- Added `cmd/engram/doctor_test.go::TestCmdDoctorJSONMatchesMemDoctorEnvelope`, which invokes `cmdDoctor` and MCP `DoctorToolHandler` against the same temp store/project/check and deep-compares normalized JSON envelopes.
- Added `internal/mcp/mcp_test.go::TestMemDoctorOmittedProjectUsesAutoDetectedScope`, which uses a temp git repo and omitted `project` argument to prove auto-detected scope.
- Added `internal/diagnostic/diagnostic_test.go::TestSQLiteLockContentionBranches`, which injects deterministic healthy, contention, and probe-failure snapshots without relying on flaky real DB locking.
- Added `internal/diagnostic/diagnostic_test.go::TestRunnerRunAllHealthyEvaluatesEveryMVPCheck` for broader all-check coverage.
- Added `cmd/engram/doctor_test.go::TestCmdDoctorSyncMutationRequiredFieldsBlockedEnvelope`, which seeds a pending malformed mutation in a temp store and asserts the CLI JSON diagnostic envelope is `blocked` with stable finding evidence.
- Executed manual clone walkthroughs with SQLite `.backup` clones only:
  - full local clone surfaced false positives that drove stricter `git_remote`/`git_root`-only directory evidence;
  - synthetic `sias-app` clone confirmed `manual_session_name_project_mismatch` and `session_project_directory_mismatch` findings with `requires_confirmation=true`;
  - conceptual reclassification SQL on the clone cleared the `sias-app` doctor report back to `ok`.

## Deviations

- Check implementation files are consolidated into `internal/diagnostic/checks.go` rather than one file per check to keep the MVP compact. The package boundary and check IDs match the design.

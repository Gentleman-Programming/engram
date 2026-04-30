# Apply Progress: doctor-repair-reclassify-project

## Status

MVP implemented in standard mode with TDD-style red/green coverage for planner, store boundary, and CLI JSON behavior.

## Completed Tasks

- [x] 1.1 Add planner tests for `session_project_directory_mismatch` trusted `git_remote`/`git_root` evidence.
- [x] 1.2 Create `internal/diagnostic/repair.go` with `BuildRepairPlan` for directory mismatch findings.
- [x] 1.3 Add planner tests for `manual_session_name_project_mismatch` exact/manual/contradiction cases.
- [x] 1.4 Implement manual-session planning and deterministic skipped reasons.
- [x] 1.5 Keep evidence parsing local to diagnostic planner and expose typed repair actions.
- [x] 2.1 Add store test proving estimate/dry-run counts without mutation.
- [x] 2.2 Add store estimate method for selected session IDs grouped by source/target project.
- [x] 2.3 Add apply test requiring backup before mutation and transactionally updating sessions/observations/user_prompts.
- [x] 2.4 Implement store backup helper and transactional session-scoped reclassification SQL.
- [x] 2.5 Add forbidden-mutation regression for no deletes and unchanged sync state/mutations.
- [x] 2.6 Lock apply SQL to the three allowed project-column updates only.
- [x] 3.1 Add CLI validation tests for missing/multiple modes, missing project, and unsupported check.
- [x] 3.2 Extend `cmdDoctor` to route `engram doctor repair ...` and print safe usage/errors.
- [x] 3.3 Add CLI JSON tests for `--plan`, `--dry-run`, and `--apply`, including backup path.
- [x] 3.4 Wire CLI to run doctor check, build plan, estimate/apply store actions, and render JSON.
- [x] 4.1 Update `docs/DOCTOR.md` with command forms, JSON output, safety boundaries, and restore guidance.
- [x] 4.2 Add synthetic clone verification guidance for plan → dry-run → apply → verify allowed tables.
- [x] 4.3 Document local SQLite source-of-truth and cloud/sync repair as out of scope.
- [x] 5.1 Run focused tests: `go test ./internal/diagnostic ./internal/store ./internal/mcp ./cmd/engram`.
- [x] 5.2 Run full validation: `go test ./...`.
- [x] 5.3 Record manual/synthetic clone verification notes before merge.

## Verification

- ✅ RED gate observed: `go test ./internal/diagnostic ./internal/store` failed before repair types/store methods existed.
- ✅ Focused validation passed: `go test ./internal/diagnostic ./internal/store ./internal/mcp ./cmd/engram`.
- ✅ Full validation passed: `go test ./...`.
- ✅ Synthetic temp-DB CLI verification is covered by `TestCmdDoctorRepairPlanDryRunApplyJSON`, using a temporary `DataDir`, a git-backed directory mismatch, and plan → dry-run → apply assertions.
- ✅ Manual cloned-DB verification ran against `/tmp/engram-doctor-repair-clone-rvYGNY`, created with SQLite `.backup` from `~/.engram/engram.db`; the production database was not mutated.
- ✅ Manual-session repair clone flow: `--plan` found `manual-save-engram` (`sias-app` → `engram`) with 1 observation and 1 prompt, `--dry-run` did not mutate, `--apply` created backup `/tmp/engram-doctor-repair-clone-rvYGNY/backups/engram-repair-20260429T220435.834327000Z.db` and updated exactly 1 session, 1 observation, and 1 prompt.
- ✅ Directory-mismatch repair clone flow: `--plan` found `manual-save-sdd-engram-plugin` (`sias-app` → `engram`) with 1 observation, `--apply` created a backup and updated exactly 1 session and 1 observation.
- ✅ After both clone repairs, `engram doctor --json --project sias-app` returned `ok` with 4/4 checks OK.

## Notes

- `--plan` and `--dry-run` estimate row counts and do not mutate.
- `--apply` estimates planned counts, creates a SQLite backup, then updates only `sessions.project`, `observations.project`, and `user_prompts.project` in one store-owned transaction.
- The implementation deliberately does not call `MergeProjects`, enqueue sync mutations, edit sync cursors, delete rows, dedupe rows, or mutate cloud state.

# Tasks: Doctor repair reclassifies project-contaminated rows

## Phase 1: Contracts and planner (TDD-first)

- [x] 1.1 **RED** Add `internal/diagnostic/repair_test.go` for `session_project_directory_mismatch` planning from trusted `git_remote`/`git_root` evidence.
- [x] 1.2 **GREEN** Create `internal/diagnostic/repair.go` with `BuildRepairPlan` for directory mismatch findings.
- [x] 1.3 **RED** Add planner tests for `manual_session_name_project_mismatch`: exact `manual-save-{known_project}`, unknown project skip, trusted directory contradiction skip.
- [x] 1.4 **GREEN** Implement manual-session planning and deterministic skipped reasons.
- [x] 1.5 **REFACTOR** Keep evidence parsing local to diagnostic planner; expose typed repair actions to callers.

## Phase 2: Store backup and reclassification boundary

- [x] 2.1 **RED** Add `internal/store/diagnostic_repair_test.go` proving estimate/dry-run returns row counts without mutation.
- [x] 2.2 **GREEN** Add store estimate method for selected session IDs grouped by source/target project.
- [x] 2.3 **RED** Add apply test requiring a backup file before mutation and one transaction updating sessions/observations/user_prompts.
- [x] 2.4 **GREEN** Implement store backup helper and transactional session-scoped reclassification SQL.
- [x] 2.5 **RED** Add forbidden-mutation regression asserting no deletes and no changes to `sync_state`, `sync_mutations`, last-acked/cursor fields, or cloud mutation tables.
- [x] 2.6 **GREEN** Lock apply SQL to the three allowed project-column updates only.

## Phase 3: CLI repair command

- [x] 3.1 **RED** Add CLI validation tests in `cmd/engram/doctor_test.go` for missing mode, multiple modes, missing project, missing/unsupported check.
- [x] 3.2 **GREEN** Extend `cmdDoctor` to route `engram doctor repair ...` and print safe usage/errors.
- [x] 3.3 **RED** Add CLI JSON tests for `--plan`, `--dry-run`, and `--apply` including `status`, `actions`, `counts`, and `backup_path` on apply.
- [x] 3.4 **GREEN** Wire CLI to run doctor check, build plan, estimate/apply store actions, and render JSON.

## Phase 4: Docs and clone verification

- [x] 4.1 Update `DOCS.md` or create `docs/DOCTOR.md` with command forms, JSON output, safety boundaries, and restore-from-backup guidance.
- [x] 4.2 Add manual verification notes for synthetic `sias-app` clone: plan → dry-run → apply → verify only allowed tables changed.
- [x] 4.3 Ensure docs state local SQLite remains source of truth and cloud/sync repair is out of scope.

## Phase 5: Verification gates

- [x] 5.1 Run focused tests: `go test ./internal/diagnostic ./internal/store ./cmd/engram`.
- [x] 5.2 Run full validation: `go test ./...`.
- [x] 5.3 Record manual clone verification result in the change notes or PR description before merge.

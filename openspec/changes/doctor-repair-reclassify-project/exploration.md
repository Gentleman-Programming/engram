# Exploration: doctor-repair-reclassify-project

## Context

`engram doctor` currently reports read-only operational diagnostics from `cmd/engram/doctor.go`, with check logic in `internal/diagnostic` and SQL evidence owned by `internal/store`. The follow-up repair MVP should reuse the existing doctor evidence for two project-contamination shapes without expanding into cloud repair, dedupe, or merge behavior.

Observed clone issue shape:
- `manual-save-engram` rows persisted under `project=sias-app`.
- Sessions whose `directory` points at `/home/j0k3r/engram` or `/Users/alanbuscaglia/work/engram` were stored under `sias-app`.
- Manual SQL on a clone worked by updating only `sessions.project`, `observations.project`, and `user_prompts.project` for selected `session_id`s.

## Relevant Existing Patterns

- `cmd/engram/doctor.go` has minimal flag parsing and delegates diagnostics via injectable `runDiagnostics` in `main.go`.
- `internal/diagnostic/checks.go` already emits findings with `requires_confirmation=true` for risky session/project mismatch checks.
- `internal/store/diagnostic.go` exposes read-only diagnostic projections.
- `internal/store/store.go` already uses `withTx` for atomic writes and has project rewrite precedent in `MergeProjects`, but `MergeProjects` is too broad because it rewrites whole project aliases and backfills sync mutations.
- `internal/project.DetectProjectFull` identifies trusted directory evidence with `git_remote` and `git_root`; doctor already ignores lower-trust sources for `session_project_directory_mismatch`.

## Design Implications

- Repair planning should live in core packages, not in CLI string processing.
- Store should own the transaction and backup boundary.
- The repair command must not call `MergeProjects`, because this MVP is session-scoped and must not touch sync journals.
- Plans should be deterministic JSON so clone verification and future tooling can diff intended changes before apply.

## Output Decision

`engram doctor repair --project X --check CODE --plan|--dry-run|--apply` should emit JSON to stdout for all three modes. Help and argument errors remain safe text on stderr/stdout. JSON is required because a repair plan is an auditable artifact, not prose.

## Manual Verification Target

Use a synthetic clone equivalent to prior `sias-app` testing:
1. Copy an affected `engram.db` into a temporary `ENGRAM_DATA_DIR`.
2. Run `engram doctor --json --project sias-app --check session_project_directory_mismatch`.
3. Run `engram doctor repair --project sias-app --check session_project_directory_mismatch --plan` and confirm only trusted `engram` directory rows are planned.
4. Run `--dry-run`; verify row counts are unchanged.
5. Run `--apply`; verify a backup exists and target sessions/prompts/observations moved to `engram` with no deletes, sync cursor updates, or `sync_mutations` changes.

# Proposal: Doctor repair reclassifies project-contaminated rows

## Intent

Add a CLI-only `engram doctor repair` MVP that safely reclassifies contaminated sessions and their associated memories/prompts between projects using existing doctor findings. The goal is to turn a verified clone-only SQL fix into an auditable, dry-run-first command with strict safety boundaries.

## Scope

### In Scope
- `engram doctor repair --project X --check CODE --plan|--dry-run|--apply`.
- Support `session_project_directory_mismatch` by moving selected sessions and associated observations/user prompts from the current project to the project inferred from trusted directory evidence (`git_remote` or `git_root`).
- Support `manual_session_name_project_mismatch` only for exact `manual-save-{known_project}` sessions when trusted directory evidence does not contradict the manual-name target.
- JSON stdout for plan/dry-run/apply.
- SQLite backup before apply and one transaction for all selected updates.

### Out of Scope
- Dashboard, MCP, or server repair surfaces.
- Deletes, dedupe, pruning, project-wide merge, sync cursor edits, last-acked changes, cloud mutations, or remote/cloud writes.
- Auto-repair from plain `engram doctor`.
- Repairs for `sync_mutation_required_fields` or `sqlite_lock_contention`.

## Approach

Add a repair subcommand under `doctor`. The CLI validates `--project`, `--check`, and exactly one mode flag. It opens the local store, reruns the requested doctor check, builds a deterministic reclassification plan from the findings, and either returns the plan (`--plan`), validates non-mutation (`--dry-run`), or creates a backup then applies the selected session updates in a single transaction (`--apply`).

Core logic belongs below the CLI:
- `internal/diagnostic` interprets supported findings into repair actions.
- `internal/store` owns backup and project reclassification SQL.
- `cmd/engram` only parses flags and renders JSON/errors.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `cmd/engram/doctor.go` | Modified | Add `repair` subcommand parsing, usage, JSON render path. |
| `internal/diagnostic` | Modified/Create | Build supported repair plans from doctor reports and trusted evidence. |
| `internal/store/diagnostic.go` | Modified | Add session-scoped reclassification plan/apply methods. |
| `cmd/engram/doctor_test.go`, `internal/diagnostic/*_test.go`, `internal/store/*_test.go` | Modified/Create | TDD coverage for plan, dry-run, apply, boundaries. |
| `DOCS.md` or `docs/` | Modified | Document repair workflow and clone verification guidance. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Moving rows to the wrong project | Medium | Only supported checks, trusted git evidence, exact manual names, explicit apply. |
| Backup gives false confidence | Low | Create backup before transaction and include backup path in apply JSON. |
| Sync state inconsistency | Medium | Do not edit sync cursors/mutations; document cloud reconciliation as out of scope. |

## Rollback Plan

Every `--apply` creates a SQLite backup before the write transaction. If the repair is wrong, stop Engram processes and restore the backup file manually. No remote/cloud state is changed by this command.

## Success Criteria

- [ ] `--plan` and `--dry-run` are deterministic and non-mutating.
- [ ] `--apply` creates a backup before one transaction and updates only the three allowed project columns.
- [ ] Unsupported checks and missing/ambiguous mode flags fail loudly.
- [ ] Synthetic clone verification reproduces the `sias-app` → `engram` repair safely.

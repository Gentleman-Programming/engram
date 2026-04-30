# Tasks: doctor-diagnostic

## Phase 1: Foundation — diagnostic domain + store read helpers

- [x] 1.1 Create `internal/diagnostic/diagnostic.go` with shared types (`Scope`, `Finding`, `CheckResult`, `Report`, `DiagnosticCheck`) and runner entry points (`RunAll`, `RunOne`) matching `design.md` + REQ-OD-001.
- [x] 1.2 Create `internal/diagnostic/registry.go` with deterministic registration/lookup and invalid-check error used by CLI/MCP (`--check` and `mem_doctor`).
- [x] 1.3 In `internal/store/store.go`, add read-only helper returning session evidence (`id`, `project`, `directory`, `name`) scoped by project for mismatch checks.
- [x] 1.4 In `internal/store/store.go`, add `ListPendingProjectMutations(...)` read helper and extract/reuse pure payload validator for required fields (doctor + cloud-upgrade path).
- [x] 1.5 In `internal/store/store.go`, add `ReadSQLiteLockSnapshot(ctx)` helper for journal mode, busy timeout, and `wal_checkpoint(PASSIVE)` indicators without write transactions.

## Phase 2: Core checks — MVP diagnostic implementations

- [x] 2.1 Create `internal/diagnostic/checks/session_project_directory_mismatch.go` using store session evidence + directory inference heuristic; emit stable warning reason code/evidence.
- [x] 2.2 Create `internal/diagnostic/checks/manual_session_name_project_mismatch.go` parsing `manual-save-{suffix}` and comparing to `sessions.project`; emit stable warning reason code/evidence.
- [x] 2.3 Create `internal/diagnostic/checks/sync_mutation_required_fields.go` evaluating pending mutations via shared validator; emit blocked findings for non-repairable missing fields.
- [x] 2.4 Create `internal/diagnostic/checks/sqlite_lock_contention.go` using lock snapshot helper; emit warning for contention drift and error when probe cannot evaluate.
- [x] 2.5 Wire all four checks in `internal/diagnostic/registry.go` with stable IDs exactly as spec: `session_project_directory_mismatch`, `manual_session_name_project_mismatch`, `sync_mutation_required_fields`, `sqlite_lock_contention`.

## Phase 3: Adapter wiring — CLI + MCP using one runner

- [x] 3.1 Create `cmd/engram/doctor.go` to implement `engram doctor`, `--json`, `--project`, `--check`; call only diagnostic runner and render text/JSON.
- [x] 3.2 Update `cmd/engram/main.go` command dispatch/help text to include `doctor`.
- [x] 3.3 Update `internal/mcp/mcp.go` to register `mem_doctor` schema (`project?`, `check?`) and handler calling same runner + existing read-project resolution.

## Phase 4: Tests — checks, contracts, and parity

- [x] 4.1 Add store helper tests in `internal/store/store_test.go` for session evidence query, pending mutation filtering, shared validator semantics, and lock snapshot read behavior.
- [x] 4.2 Add diagnostic package tests in `internal/diagnostic/*_test.go` for registry ordering/invalid-check path, report rollup status, and each MVP check (healthy, finding, error path).
- [x] 4.3 Add CLI contract tests in `cmd/engram/doctor_test.go` for text mode, JSON envelope, `--project` scoping, valid single-check, and invalid-check loud failure.
- [x] 4.4 Extend `internal/mcp/mcp_test.go` for `mem_doctor` registration, optional project override validation, and JSON envelope parity against CLI `--json` (normalized comparison).

Verification gap follow-up:

- [x] 4.5 Add explicit CLI `engram doctor --json` vs MCP `mem_doctor` normalized envelope parity test.
- [x] 4.6 Add explicit `mem_doctor` omitted-project auto-detection test.
- [x] 4.7 Add deterministic `sqlite_lock_contention` warning/error branch tests using a diagnostic test seam.
- [x] 4.8 Add broader healthy `RunAll` scenario covering all four MVP checks.
- [x] 4.9 Add deterministic adapter-level blocked envelope test for `sync_mutation_required_fields` with a pending mutation missing required payload fields.

## Phase 5: Docs + manual verification

- [x] 5.1 Update docs (CLI + MCP references) to include `engram doctor` flags, `mem_doctor` schema, check catalog, severity semantics, and explicit non-repair/read-only behavior.
- [x] 5.2 Add manual verification matrix under `openspec/changes/doctor-diagnostic/` covering healthy baseline + one synthetic fixture per check (including `sias-app`-style mismatch/legacy-mutation evidence snapshots).
- [x] 5.2a Reconcile stale root `spec.md` by marking canonical specs under `specs/` and removing conflicting early contracts.
- [x] 5.3 Execute manual walkthrough: run `engram doctor` and `engram doctor --json` on cloned/synthetic DB data; record expected vs actual outputs and attach evidence paths in the matrix.

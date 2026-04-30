# Design: doctor-diagnostic

## Technical Approach

Ship `engram doctor` as a read-only core diagnostic runner in `internal/diagnostic`. CLI and MCP are thin adapters: they resolve scope, call the same runner, and render the same JSON envelope for agents. Checks never repair; they detect, explain, and suggest safe next steps.

## Architecture Decisions

| Decision | Choice | Alternatives considered | Rationale |
|---|---|---|---|
| Domain placement | `internal/diagnostic` owns registry, runner, shared types, and check implementations; `internal/store` owns SQL/read helpers. | Put logic in `cmd/engram` or `internal/mcp`; put all SQL in diagnostic checks. | Keeps adapters thin and preserves store boundary: operational behavior is reusable, persistence remains store-owned. |
| Shared contract | One `diagnostic.Report` envelope for CLI `--json` and MCP `mem_doctor`: `status`, `summary`, `checks[]`. | Separate CLI/MCP DTOs. | Avoids JSON drift and lets parity tests compare normalized reports. |
| No repair in MVP | Findings include `safe_next_step`; no `--apply`, no write transaction, all `requires_confirmation=false` unless suggesting an external repair command. | Add repair/dry-run now. | The MVP must be safe in agent contexts; repair needs backups, confirmation UX, and separate specs. |
| Directory inference | Batch-read sessions, infer directory project with pure Go basename/known-directory heuristics plus an in-memory cache per run. | Call `project.DetectProjectFull` for every session directory. | Avoids shelling out per row and keeps tests deterministic. CLI/MCP may still use existing project resolution once for scope. |

## Data Flow

```text
CLI flags / MCP args
        │
        ▼
scope: {project, selected_check, store, now}
        │
        ▼
internal/diagnostic Runner ── registry ── checks
        │                         │
        │                         └── store read helpers / PRAGMA snapshots
        ▼
diagnostic.Report ── text formatter (CLI) / JSON (CLI,MCP)
```

## File Changes

| File | Action | Description |
|---|---|---|
| `internal/diagnostic/diagnostic.go` | Create | Types: `Severity`, `Finding`, `CheckResult`, `Report`, `Scope`, `DiagnosticCheck`, runner. |
| `internal/diagnostic/registry.go` | Create | Deterministic check registry, lookup, invalid-check error. |
| `internal/diagnostic/checks/*.go` | Create | Four MVP checks: session/project-directory mismatch, manual-session-name mismatch, sync mutation required fields, sqlite lock contention. |
| `internal/store/store.go` | Modify | Add read helpers for diagnostic session evidence, pending mutations, SQLite lock snapshot; extract pure mutation validation helper reused by cloud upgrade diagnosis. |
| `cmd/engram/doctor.go` | Create | Parse `--json`, `--project`, `--check`; render text/JSON; exit loudly on invalid checks. |
| `cmd/engram/main.go` | Modify | Add `doctor` dispatch and usage entry. |
| `internal/mcp/mcp.go` | Modify | Add agent-profile `mem_doctor` read tool with optional project override via existing `resolveReadProject`. |

## Interfaces / Contracts

```go
type DiagnosticCheck interface {
    Code() string
    Run(context.Context, Scope) (CheckResult, error)
}

type Report struct {
    Status string `json:"status"` // ok|warning|blocked|error
    Summary Summary `json:"summary"`
    Checks []CheckResult `json:"checks"`
}

type Finding struct {
    CheckID string `json:"check_id"`
    Severity string `json:"severity"`
    ReasonCode string `json:"reason_code"`
    Why string `json:"why"`
    Evidence json.RawMessage `json:"evidence"`
    SafeNextStep string `json:"safe_next_step"`
    RequiresConfirmation bool `json:"requires_confirmation"`
}
```

## Check Design

- `session_project_directory_mismatch`: store returns `{id, project, directory}` in one query scoped by project. Diagnostic derives `directory_project` by normalizing `filepath.Base(directory)` and by matching directories already known in `ListProjectsWithStats`; no per-row shell execution.
- `manual_session_name_project_mismatch`: same session batch; parse `manual-save-{suffix}` and compare suffix to normalized `sessions.project`.
- `sync_mutation_required_fields`: store exposes pending unacked mutations read-only. Extract `ValidateSyncMutationPayload(entity, op, payload, entityKey)` from cloud upgrade legacy evaluation and make both doctor and `DiagnoseCloudUpgradeLegacyMutations` call it to prevent drift.
- `sqlite_lock_contention`: store exposes `ReadSQLiteLockSnapshot(ctx)` using `QueryRow` PRAGMA reads (`journal_mode`, `busy_timeout`, `wal_checkpoint(PASSIVE)`) without a write transaction. Tests use injected/temp SQLite handles and assert row counts unchanged.

## Testing Strategy

| Layer | What to Test | Approach |
|---|---|---|
| Store | Read helpers, pure mutation validation, no writes | TDD fixtures in `internal/store/store_test.go`; before/after row counts. |
| Diagnostic | Registry, rollup, all four checks | Table tests with seeded stores and deterministic clock. |
| CLI | text, JSON, invalid check, project scope | `cmd/engram/doctor_test.go` with injected `storeNew`/`exitFunc`. |
| MCP | `mem_doctor` registration, project override, JSON parity | `internal/mcp/mcp_test.go`; compare report shape with CLI JSON. |

## Migration / Rollout

No migration required. The feature is additive, local-first, and read-only.

## Risks

- Directory heuristics can false-positive renamed/nested repos; emit warnings with evidence, not blocking results.
- `wal_checkpoint(PASSIVE)` is observational but can perform passive checkpoint work; document it and assert no application-table mutation.
- Repair remains intentionally out of scope; adding it later needs separate backup/confirmation design.

## Open Questions

None.

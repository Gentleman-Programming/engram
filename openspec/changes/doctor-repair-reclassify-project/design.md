# Design: Doctor repair reclassifies project-contaminated rows

## Technical Approach

Add a CLI-only `doctor repair` subcommand that reuses doctor findings to build an auditable session-scoped reclassification plan. Planning lives in `internal/diagnostic`; SQL and backup boundaries live in `internal/store`; `cmd/engram` stays a thin adapter for args and JSON rendering. `--plan` and `--dry-run` never mutate. `--apply` creates a SQLite backup via `VACUUM INTO` (or equivalent store-owned backup helper) before one transaction updates only the allowed `project` columns.

## Architecture Decisions

| Decision | Choice | Alternatives considered | Rationale |
|----------|--------|-------------------------|-----------|
| Output contract | JSON stdout for `--plan`, `--dry-run`, `--apply` | Human text by default | Repair plans need stable audit/diff output; text remains for help/errors. |
| Repair ownership | `internal/diagnostic` plans, `internal/store` applies | CLI parses evidence and runs SQL | Keeps adapters thin and preserves store ownership of persistence. |
| Scope of writes | Session-ID reclassification only | Reuse `MergeProjects` | `MergeProjects` is project-wide and backfills sync mutations; this MVP must not touch sync journals. |
| Backup mechanism | Store helper creates SQLite backup before tx | Caller copies files | Store knows `DataDir`/DB lifecycle and can guarantee backup-before-transaction. |

## Data Flow

```
engram doctor repair flags
  └─ cmd/engram validates --project/--check/mode
      └─ diagnostic.RunOne(check)
          └─ diagnostic.BuildRepairPlan(report, store, project, check)
              └─ store.EstimateProjectReclassification(plan)
                  ├─ --plan/--dry-run: JSON only
                  └─ --apply: store.BackupSQLite() → tx UPDATEs → JSON
```

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `cmd/engram/doctor.go` | Modify | Parse `repair`, enforce explicit mode/check/project, render repair JSON. |
| `cmd/engram/doctor_test.go` | Modify | CLI mode validation, JSON output, dry-run non-mutation, apply backup path. |
| `internal/diagnostic/repair.go` | Create | Plan supported reclassification actions from findings/evidence. |
| `internal/diagnostic/repair_test.go` | Create | Directory/manual planning, trusted evidence, contradiction skips. |
| `internal/store/diagnostic.go` | Modify | Add session-scoped estimate/apply reclassification and backup helper. |
| `internal/store/diagnostic_repair_test.go` | Create | Transactional updates and forbidden-table regression tests. |
| `DOCS.md` or `docs/DOCTOR.md` | Modify/Create | Document repair workflow and clone verification. |

## Interfaces / Contracts

```go
type RepairMode string // plan|dry_run|apply

type ProjectReclassifyAction struct {
  SessionID string `json:"session_id"`
  FromProject string `json:"from_project"`
  ToProject string `json:"to_project"`
  ReasonCode string `json:"reason_code"`
  EvidenceSource string `json:"evidence_source,omitempty"`
}

type RepairPlan struct {
  Project string `json:"project"`
  Check string `json:"check"`
  Mode RepairMode `json:"mode"`
  Status string `json:"status"` // planned|dry_run|applied|blocked|noop
  Actions []ProjectReclassifyAction `json:"actions"`
  Skipped []RepairSkip `json:"skipped,omitempty"`
  Counts RepairCounts `json:"counts"`
  BackupPath string `json:"backup_path,omitempty"`
}
```

Store apply SQL is constrained to:
- `UPDATE sessions SET project=? WHERE id IN (...) AND project=?`
- `UPDATE observations SET project=? WHERE session_id IN (...) AND project=?`
- `UPDATE user_prompts SET project=? WHERE session_id IN (...) AND project=?`

## Testing Strategy

| Layer | What to Test | Approach |
|-------|-------------|----------|
| Diagnostic unit | Supported findings become actions; untrusted/contradicted evidence skipped | Fake reports + temp git dirs |
| Store integration | Plan counts, dry-run no mutation, apply transaction, backup exists | Real SQLite test store |
| Boundary regression | No deletes; no `sync_state`, `sync_mutations`, cursor changes | Before/after snapshots |
| CLI integration | Required args/modes, unsupported check, JSON shape | Existing `withArgs`/`captureOutput` |
| Manual verification | `sias-app` synthetic clone repair | Documented checklist |

## Migration / Rollout

No schema migration. Roll out as explicit CLI command only. Existing `engram doctor` remains read-only.

## Open Questions

- [ ] None blocking.

# Proposal: doctor-diagnostic

## Intent

Ship `engram doctor` as a read-only operational diagnostic surface so humans and agents can detect store health issues, understand impact, and get safe next steps without touching data.

## Scope

### In Scope
- Registry-based diagnostic checks with stable check IDs and reason codes.
- Shared JSON envelope for CLI `--json` and MCP `mem_doctor`.
- CLI forms: `engram doctor`, `engram doctor --json`, `engram doctor --project X`, `engram doctor --check CODE`.
- MVP checks: `session_project_directory_mismatch`, `manual_session_name_project_mismatch`, `sync_mutation_required_fields`, `sqlite_lock_contention`.

### Out of Scope
- Auto-repair, `--apply`, interactive fixes, or write transactions.
- Dashboard/HTTP diagnostic routes.
- Cloud-side diagnostics against `cloudstore`/`cloudserver`.
- Semantic memory conflicts integration.

## Capabilities

### New Capabilities
- `operational-diagnostics`: Read-only doctor checks, CLI/MCP surfaces, structured findings, and healthy/error envelopes.

### Modified Capabilities
- `mcp-project-resolution`: Add `mem_doctor` as an agent read tool that follows existing optional project override and standardized response envelope rules.

## Approach

Create `internal/diagnostic/` as the single owner of check registration, execution, and result types. Adapters stay thin: CLI parses flags and renders text/JSON; MCP calls the same runner and returns the same JSON shape. Checks read local SQLite/store state only and emit findings shaped like `check_id`, `severity`, `reason_code`, `why`, `evidence`, and `safe_next_step`.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/diagnostic/` | New | Registry, runner, envelope, check implementations. |
| `internal/store/store.go` | Modified | Read helpers for session/project drift, mutation validation, SQLite pragma/checkpoint status. |
| `cmd/engram/main.go` | Modified | Top-level `doctor` command and flags. |
| `internal/mcp/mcp.go` | Modified | Register `mem_doctor` under agent profile. |
| `*_test.go` | Modified/New | Deterministic coverage for each check, CLI JSON/text, MCP parity. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Project/directory heuristics false-positive on nested or renamed repos | Med | Conservative matching; evidence includes path/project values; warnings not errors. |
| Mutation validation drift from cloud-upgrade doctor | Med | Extract/reuse one pure validation helper. |
| `wal_checkpoint(PASSIVE)` side effects misunderstood | Low | Document as read-only diagnostic probe and assert no repair/write path. |
| CLI/MCP JSON divergence | Low | One envelope type and parity tests. |

## Rollback Plan

Remove `internal/diagnostic/`, CLI dispatch, MCP registration, and related tests. No migration or data rollback is needed because MVP performs no writes and changes no schema.

## Dependencies

- Existing local SQLite schema and store helpers.
- Existing MCP project resolution and agent profile patterns.

## Success Criteria

- [ ] `engram doctor` prints human-readable results and exits successfully on healthy stores.
- [ ] `engram doctor --json` and `mem_doctor` return the same structured envelope.
- [ ] `--project` scopes diagnostics and `--check CODE` runs only one registered check.
- [ ] All four MVP checks produce deterministic findings with explain/suggest fields.
- [ ] Tests cover healthy, finding, invalid check, project override, and MCP parity paths.

## Exploration: doctor-diagnostic

### Current State
Engram currently has no dedicated operational doctor command. The closest logic is `engram cloud upgrade doctor` (`cmd/engram/cloud.go`) plus store-level legacy mutation diagnosis (`DiagnoseCloudUpgradeLegacyMutations` / `evaluateCloudUpgradeLegacyMutationTx` in `internal/store/store.go`).

For project/session consistency, schema-level FK integrity exists, but semantic drift is still possible: `sessions.project` can be internally consistent yet wrong for the actual directory or naming convention. MCP write tools auto-detect project from cwd (`resolveWriteProject` in `internal/mcp/mcp.go`) and generate default IDs like `manual-save-{project}` (`defaultSessionID`), which provides enough signal for semantic diagnostics but no existing doctor surface checks it.

SQLite locking behavior is configured (`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`) and write retries exist (`withSQLiteWriteRetry` + `isRetryableSQLiteLockError`), but there is no user-facing diagnostic that reports contention status.

### Affected Areas
- `internal/store/store.go` — source of truth for schema (`sessions`, `sync_mutations`), legacy mutation validation, SQLite pragma setup, and read helpers where doctor checks should read from.
- `cmd/engram/main.go` — top-level CLI dispatch point where `doctor` command would be wired.
- `cmd/engram/conflicts.go` — reference pattern for subcommand dispatch and usage formatting.
- `internal/mcp/mcp.go` — MCP tool registry/profile; `mem_doctor` should return same JSON shape as CLI/HTTP.
- `internal/server/server.go` — HTTP route registration pattern; optional `GET /diagnostic` can reuse identical doctor payload.
- `internal/store/store_test.go` + `internal/mcp/mcp_test.go` + `cmd/engram/*_test.go` — expected locations for deterministic diagnostics coverage and tool/profile assertions.

### Approaches
1. **Registry-based operational doctor package** — create `internal/diagnostic/` with check registry + normalized finding schema shared by CLI/MCP(/HTTP).
   - Pros: clean separation from `conflicts`; easy to add checks; guarantees stable reason codes and JSON contract for agents.
   - Cons: introduces new package + adapter wiring; requires careful boundary design to keep store authoritative.
   - Effort: Medium

2. **Inline doctor logic in CLI/MCP handlers** — implement each check directly in command/tool handlers with ad-hoc structs.
   - Pros: faster initial coding for MVP.
   - Cons: duplicated logic across CLI/MCP/HTTP, higher drift risk, weaker long-term maintainability, harder deterministic testing.
   - Effort: Low (short-term) / High (maintenance)

### Recommendation
Use **Approach 1 (registry-based doctor)** for MVP. It best matches the requested scope (structured reason codes, same JSON for CLI and `mem_doctor`) and preserves local-first boundaries: checks read local SQLite/store state, explain issues, and suggest safe next steps without repair writes.

Recommended MVP checks (operational/semantic, read-only):
- `session_project_directory_mismatch` — detect sessions where `project` semantics conflict with `directory` evidence (e.g., directory path indicates another repo/project).
- `manual_session_name_project_mismatch` — detect `manual-save-*` session IDs whose suffix disagrees with `sessions.project`.
- `sync_mutation_required_fields` — reuse legacy payload validation logic from cloud-upgrade diagnostics for pending mutation payloads.
- `sqlite_lock_contention` — report journal/busy_timeout drift and live checkpoint contention signal.

Reason-code shape should be stable and explicit for agents, e.g.:
`check_id`, `severity`, `reason_code`, `why`, `evidence`, `safe_next_step`.

### Risks
- **False positives in project/directory mismatch** if heuristics are too strict (nested repos, renamed folders, ambiguous basenames).
- **Validation logic duplication drift** if `sync_mutation_required_fields` does not share a single source with cloud-upgrade legacy checks.
- **Checkpoint probe side effects** (`wal_checkpoint(PASSIVE)` attempts housekeeping) must be documented as diagnostic-safe.
- **Scope confusion with `conflicts`** if semantic contradiction diagnostics are mixed into doctor MVP; keep them separate but linkable.
- **Agent contract drift** if CLI and `mem_doctor` diverge in JSON envelope.

### Ready for Proposal
Yes — proceed to `sdd-propose` with explicit MVP contract: detect/explain/suggest only, no auto-repair, and future repair only as explicit dry-run/apply + backup-gated workflow.

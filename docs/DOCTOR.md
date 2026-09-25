# Engram Doctor

`engram doctor` runs read-only operational diagnostics against the local SQLite store and, for unfiltered CLI runs, generic MCP client registrations. It detects, explains, and suggests safe next steps; the base diagnostic command does **not** repair data, apply migrations, delete rows, or mutate sync cursors.

## CLI

```bash
engram doctor
engram doctor --json
engram doctor --project engram
engram doctor --check sync_mutation_required_fields
engram doctor repair --project sias-app --check session_project_directory_mismatch --plan
engram doctor repair --project sias-app --check session_project_directory_mismatch --dry-run
engram doctor repair --project sias-app --check session_project_directory_mismatch --apply
```

Flags:

- `--json` prints the stable diagnostic envelope for agents.
- `--project PROJECT` scopes checks to a normalized project name.
- `--check CODE` runs one registered check and fails loudly for unknown codes.
- `doctor repair` supports exactly `invalid_session_identity`, `manual_session_name_project_mismatch`, `orphaned_observation_session`, `session_project_directory_mismatch`, `sync_mutation_required_fields`, and `sync_target_closed_space`. It requires `--project`, `--check`, and exactly one mode: `--plan`, `--dry-run`, or `--apply`. `sync_mutation_required_fields` may omit `--project` and the mode; an omitted mode defaults to `--dry-run`. Its optional project scopes title repair, supersession, quarantine, and source-title repair. The diagnostic-only checks are `ambiguous_active_runtime_sessions`, `sqlite_lock_contention`, and `unowned_session_project`; a rejected repair names the corresponding `engram doctor --check <code>` continuation.

An unfiltered CLI `engram doctor` also reports generic adapter Engram MCP entries whose absolute executable command no longer exists and whose parsed fields match the entry shape written by setup (apart from the executable path). Each finding names the client and recommends `engram setup <slug>` to refresh its registration; doctor never runs the configured executable or edits the config. Missing configs/entries, bare commands, and entries with different or extra fields are skipped. Matching the setup shape cannot prove who created an entry: check for custom launchers before rerunning setup, which replaces the `engram` registration. This CLI-only inspection is not a registered `--check` or MCP `mem_doctor` check.

For `invalid_session_identity`, supply an unused canonical `--replacement-id`. Without it repair remains a nonmutating `noop`. Plan and dry-run show `identity_repair` with exact source, replacement, reference and retired journal counts; `blockers` explains collisions and unsafe evidence. For multiple whitespace-only sources specify the exact `--source-id SOURCE` (use `--source-id ''` for the empty string). The repair JSON `counts.corrected_mutations_planned` estimates corrected current-state publications as one session plus each observation and prompt for an enrolled project; local-only projects report zero. `counts.corrected_mutations_applied` is zero for plan, dry-run, blocked and noop results, and on apply reports the number actually published by the store. Both count fields are always present (zero outside applicable work); neither counts retired historical journal rows, which remain in `identity_repair.retired_mutations`. Apply revalidates under the SQLite writer lock, backs up the database, atomically remaps references and retires legacy journal evidence; corrected state is published only for enrolled projects. Quarantined pulled identities are not locally repairable. Unrepaired findings remain in `skipped`; an apply that repairs one source while others remain reports `partial`. Re-run doctor after apply; other malformed sources may remain. To roll back, stop Engram and manually restore the reported backup.

```bash
engram doctor repair --project engram --check invalid_session_identity --replacement-id canonical-session --plan
engram doctor repair --project engram --check invalid_session_identity --replacement-id canonical-session --apply
```

## MCP

Agents can call `mem_doctor` with the same contract as `engram doctor --json`:

```json
{
  "project": "engram",
  "check": "sqlite_lock_contention"
}
```

Both fields are optional. When `project` is omitted, MCP uses the existing read-tool project detection. Unknown explicit projects return the standard structured `unknown_project` error.

## JSON envelope

The CLI `--json` and MCP tool return:

```json
{
  "status": "ok|warning|blocked|error",
  "project": "engram",
  "summary": { "total": 4, "ok": 4, "warnings": 0, "blocked": 0, "errors": 0 },
  "checks": [
    {
      "check_id": "sqlite_lock_contention",
      "result": "ok|warning|blocked|error",
      "severity": "info|warning|blocking|error",
      "reason_code": "stable_reason_code",
      "evidence": {},
      "safe_next_step": "No action required.",
      "requires_confirmation": false
    }
  ]
}
```

## MVP check catalog

- `session_project_directory_mismatch` — warns when `sessions.project` disagrees with the project inferred from trusted repository evidence for the session directory. The MVP trusts `git_remote` and `git_root` only; it ignores basename fallback, ambiguous workspaces, missing directories, and child-repo auto-promotion to avoid noisy false positives.
- `manual_session_name_project_mismatch` — warns when a known `manual-save-{suffix}` session name disagrees with its persisted project. Trusted Git directory evidence precedes manual-name inference for repair: manual-name repair applies only when that evidence does not establish ownership. The suffix must normalize to a project already evidenced by a local session; a name alone never establishes `project_owned` ownership. Basename evidence can corroborate persisted ownership only and cannot authorize a move that conflicts with trusted directory evidence.
- `ambiguous_active_runtime_sessions` — warns once per project when two or more active runtime candidates match the same directory. Evidence contains the active-candidate count, involved directories, and session IDs. It uses the same lease-aware selection as omitted-session resolution: valid unexpired local leases take precedence in their own directory, expired or malformed nonblank leases are excluded, and the legacy seven-day effective-activity window applies only when that directory has no live lease. Multiple live leases remain ambiguous. Doctor is diagnostic-only: it never selects, ends, or modifies sessions. End only confirmed stale IDs with `mem_session_end`; otherwise keep explicit runtime attribution with `session_id` on writes.
- `sync_mutation_required_fields` — blocks when a pending `sync_mutations.payload` is missing required fields. On a device that uses cloud sync (at least one project enrolled), it also blocks when pending cloud mutations belong to a project that is not enrolled; the finding identifies the project and backlog count, so enroll intended projects with `engram cloud enroll <project>` or review enrollment before retrying. A local-only install with no enrolled project never reports that finding: any pending non-enrolled row there is legacy or otherwise pre-existing backlog, because new unenrolled local writes are not journaled.
- `orphaned_observation_session` — warns when active or soft-deleted observations reference a missing session. Findings are grouped by the stored observation project and session ID. After reviewing a plan, `doctor repair` can create an immediately-ended, local-only, project-owned placeholder for a group with complete evidence; it preserves observations and never emits sync state. Apply revalidates the current observations inside the same transaction and derives the placeholder's start time and observation count from them, so a stale plan or a concurrent change cannot persist outdated placeholder metadata; a planned orphan that resolves before apply reports `noop` with zero applied rows.
- `unowned_session_project` — warns for each session with an unclassified or invalid ownership mode, including blank persisted projects and contradictory legacy manual-save identities. Doctor never guesses a rescue. Use `engram projects rescue-ownership --project <name> --session <id>` only after review; its apply path creates a SQLite backup that can be restored for rollback. The listing is deliberately unscoped.
- Session modes are `shared` and `project_owned`. Runtime and HTTP-created sessions default to `shared`; deterministic CLI and MCP manual-save sessions are `project_owned`. Shared sync can use old peers. Project-owned sync requires a mode-capable manifest (version 2); returning to an older manifest after project-owned sessions exist is unsupported and fails loudly.
- `sqlite_lock_contention` — warns on conservative SQLite contention signals; returns an error if lock state cannot be evaluated.

## Safety

Plain `engram doctor` remains diagnostic-only. Findings that imply data movement set `requires_confirmation=true` so agents know a human must review evidence before repair.

### Network filesystem startup rejection

Persistent SQLite WAL is unsafe on known NFS and SMB/CIFS data directories. When startup rejects one, stop **all** Engram processes; copy the complete `engram.db`, `engram.db-wal`, and `engram.db-shm` triplet to local storage; set `ENGRAM_DATA_DIR` to the absolute path of that local directory (relative paths are rejected); start Engram; then run `engram doctor`. Run the integrity check for your shell:

```bash
# POSIX shell or Git Bash
sqlite3 "$ENGRAM_DATA_DIR/engram.db" "PRAGMA integrity_check;"
```

```powershell
# PowerShell
sqlite3 (Join-Path $env:ENGRAM_DATA_DIR 'engram.db') 'PRAGMA integrity_check;'
```

This condition has no automatic repair, quarantine, checkpoint, or rollback-journal fallback.

`engram doctor repair` is intentionally narrow and local-first: local SQLite remains the source of truth. Project reclassification supports:

- `session_project_directory_mismatch`, using trusted `git_remote` or `git_root` evidence from doctor findings.
- `manual_session_name_project_mismatch`, only for exact `manual-save-{known_project}` sessions when trusted Git directory evidence does not establish ownership. Unknown suffixes remain unrepaired; basename evidence can corroborate persisted ownership but cannot authorize a conflicting move.

Title restoration supports `sync_mutation_required_fields` only when a pending observation upsert has a blank title as its sole missing field and the matching local titleless observation has non-empty content. Run `engram doctor repair --check sync_mutation_required_fields --dry-run` first (add `--project <project>` to scope it); cloud-upgrade tooling instead requires configured cloud sync. The repair derives a sanitized, bounded title from local content and updates `observations.title` and `sync_mutations.payload` in place; all other invalid mutations remain quarantined on `--apply`.

The same repair also supersedes a pending local upsert when a local session/observation delete tombstone or prompt tombstone proves the entity was deleted while its project was unenrolled. `superseded` is auditable local evidence, not a cloud acknowledgement: it is excluded from transport and allows re-enrollment backfill to reconstruct the current local delete state. Superseded evidence missing its reason, evidence, or timestamp remains blocking until manually repaired; complete terminal quarantined and superseded rows remain informational without keeping doctor in warning or blocked status.

Project reclassification never deletes or deduplicates rows. Identity repair replaces the malformed source session row after remapping its references; it retains old journal rows as auditable retired evidence rather than deleting their payload history. Repair never edits sync cursors, acknowledges undelivered mutations, or writes cloud state. `--plan` and `--dry-run` are non-mutating. `--apply` creates a SQLite backup under `<ENGRAM_DATA_DIR>/backups/` before a project reclassification transaction updates only:

- `sessions.project`
- `sessions.ownership_mode` (`project_owned` for a session named `manual-save-{target_project}`, otherwise `shared`)
- `observations.project`
- `user_prompts.project`

Title restoration does not create a SQLite backup.

`ambiguous_active_runtime_sessions`, `sqlite_lock_contention`, and `unowned_session_project` are diagnostic-only and are not supported by `engram doctor repair`. SQLite lock contention has no repair.

A strict `project_owned` registration of an ended legacy session with no project may establish its owner only when no live observation or prompt belongs to another project. The session remains ended and the registration still returns `409 session_already_ended`; a later strict `project_owned` registration from a different project returns `409 session_project_conflict`. This is not a general repair for ambiguous legacy ownership. Use `engram projects rescue-ownership` when existing records require an explicit operator decision.

### Repair JSON envelope

All repair modes print stable JSON to stdout:

For `sync_mutation_required_fields`, `repairs` lists title-only observation upserts that can be restored in place; `actions` continues to list residual rows quarantined on `--apply`; `superseded` lists obsolete local upserts retired by durable local delete evidence.

```json
{
  "project": "sias-app",
  "check": "session_project_directory_mismatch",
  "mode": "plan|dry_run|apply",
  "status": "planned|dry_run|applied|partial|blocked|noop",
  "actions": [
    {
      "session_id": "session-id",
      "from_project": "sias-app",
      "to_project": "engram",
      "reason_code": "session_project_directory_mismatch",
      "evidence_source": "git_remote"
    }
  ],
  "skipped": [],
  "counts": {
    "sessions_planned": 1,
    "observations_planned": 2,
    "prompts_planned": 1,
    "sessions_applied": 0,
    "observations_applied": 0,
    "prompts_applied": 0,
    "corrected_mutations_planned": 0,
    "corrected_mutations_applied": 0
  },
  "backup_path": ""
}
```

On `--apply`, `backup_path` contains the backup database path and `*_applied` counts report the rows updated.

### Clone-safe verification workflow

Never experiment on production `~/.engram/engram.db`. Use a SQLite backup clone or a temporary `ENGRAM_DATA_DIR`:

```bash
mkdir -p /tmp/engram-repair-clone
sqlite3 ~/.engram/engram.db ".backup '/tmp/engram-repair-clone/engram.db'"
ENGRAM_DATA_DIR=/tmp/engram-repair-clone engram doctor --json --project sias-app --check session_project_directory_mismatch
ENGRAM_DATA_DIR=/tmp/engram-repair-clone engram doctor repair --project sias-app --check session_project_directory_mismatch --plan
ENGRAM_DATA_DIR=/tmp/engram-repair-clone engram doctor repair --project sias-app --check session_project_directory_mismatch --dry-run
ENGRAM_DATA_DIR=/tmp/engram-repair-clone engram doctor repair --project sias-app --check session_project_directory_mismatch --apply
```

After a project reclassification apply, verify each planned session's `project` and `ownership_mode` classification, the related observation and prompt projects, and that `backup_path` exists. If the repair is wrong, stop Engram processes and restore the `backup_path` database file manually.

# Engram Doctor

`engram doctor` runs read-only operational diagnostics against the local SQLite store. It detects, explains, and suggests safe next steps; the base diagnostic command does **not** repair data, apply migrations, delete rows, or mutate sync cursors.

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
- `doctor repair` requires `--project`, `--check`, and exactly one mode: `--plan`, `--dry-run`, or `--apply`.

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
- `manual_session_name_project_mismatch` — warns when a `manual-save-{suffix}` session name disagrees with `sessions.project`.
- `sync_mutation_required_fields` — blocks when a pending `sync_mutations.payload` is missing required fields.
- `sqlite_lock_contention` — warns on conservative SQLite contention signals; returns an error if lock state cannot be evaluated.

## Safety

Plain `engram doctor` remains diagnostic-only. Findings that imply data movement set `requires_confirmation=true` so agents know a human must review evidence before repair.

`engram doctor repair` is intentionally narrow and local-first: local SQLite remains the source of truth, and cloud/sync repair is out of scope. The repair MVP only supports project reclassification for:

- `session_project_directory_mismatch`, using trusted `git_remote` or `git_root` evidence from doctor findings.
- `manual_session_name_project_mismatch`, only for exact `manual-save-{known_project}` sessions, and only when trusted directory evidence does not contradict the manual-name target.

Repair never deletes or deduplicates rows, never edits sync cursors, never mutates `sync_state`/`sync_mutations`, and never writes cloud state. `--plan` and `--dry-run` are non-mutating. `--apply` creates a SQLite backup under `<ENGRAM_DATA_DIR>/backups/` before a transaction updates only:

- `sessions.project`
- `observations.project`
- `user_prompts.project`

### Repair JSON envelope

All repair modes print stable JSON to stdout:

```json
{
  "project": "sias-app",
  "check": "session_project_directory_mismatch",
  "mode": "plan|dry_run|apply",
  "status": "planned|dry_run|applied|noop",
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
    "prompts_applied": 0
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

After apply, verify that only the three allowed project columns changed for the planned session IDs and that a backup exists. If the repair is wrong, stop Engram processes and restore the `backup_path` database file manually.

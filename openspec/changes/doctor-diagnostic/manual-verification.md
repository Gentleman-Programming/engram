# Manual Verification Matrix: doctor-diagnostic

Use only temporary data directories. Do not point these commands at a production `~/.engram` database.

## Setup

```bash
export ENGRAM_DATA_DIR="$(mktemp -d)"
```

## Matrix

| Scenario | Fixture | Command | Expected |
|---|---|---|---|
| Healthy baseline | Empty/synthetic store with matching session project and directory | `engram doctor --json --project engram` | Envelope status is `ok`; checks include stable IDs and read-only next steps. |
| Session directory mismatch | Session project `api`, directory basename `web` | `engram doctor --check session_project_directory_mismatch --project api` | `warning` with session id, project, directory, and directory-derived project evidence. |
| Manual session mismatch | Session id `manual-save-old`, project `new` | `engram doctor --check manual_session_name_project_mismatch --project new` | `warning` with session id/name/project evidence. |
| Required mutation fields missing | Pending observation upsert payload missing `session_id`, `type`, `title`, `content`, or `scope` | `engram doctor --check sync_mutation_required_fields --project engram --json` | `blocked` with `sync_mutation_payload_missing_required_fields` and mutation seq evidence. |
| SQLite contention probe | Temp SQLite store under normal conditions | `engram doctor --check sqlite_lock_contention --json` | `ok` with journal mode, busy timeout, and passive checkpoint evidence. |

## Execution Notes

- Focused automated tests cover synthetic fixtures for helper behavior, CLI output, and MCP contract.
- Manual execution should capture stdout/stderr only from the temporary data directory above.
- No repair/apply command exists in MVP.

## 2026-04-29 Manual Clone Walkthrough

All commands below used SQLite `.backup` clones under `/tmp`. The production `~/.engram/engram.db` was not mutated.

### Clone integrity

- Clone: `/tmp/engram-doctor-clone-XHfzil`
- `PRAGMA integrity_check` returned `ok`.

### Full doctor on cloned local database

Initial run surfaced too many `session_project_directory_mismatch` false positives because the detector inferred projects from directory basenames and `git_child` auto-promotion. The implementation was tightened to:

- use real project detection for existing directories;
- trust only `git_remote` and `git_root` for this check;
- ignore `dir_basename`, `ambiguous`, missing directories, and `git_child` evidence.

After refinement, a fresh clone `/tmp/engram-doctor-clone-oeqAzc` returned:

```text
status warning summary {'total': 4, 'ok': 3, 'warnings': 1, 'blocked': 0, 'errors': 0}
manual_session_name_project_mismatch ok findings 0
session_project_directory_mismatch warning findings 9
sqlite_lock_contention ok findings 0
sync_mutation_required_fields ok findings 0
```

Remaining findings were high-signal project/remote mismatches, for example:

```text
opencode -> sub-agent-statusline | /tmp/sub-agent-statusline | git_remote
Gentleman.Dots2 -> gentleman.dots | /Users/alanbuscaglia/Gentleman.Dots2 | git_remote
```

### Synthetic `sias-app` contamination clone

Clone: `/tmp/engram-doctor-sias-clone-P0Frly`

Fixture inserted into the clone only:

- `manual-save-engram`, `project=sias-app`, empty directory, one observation.
- `manual-save-sdd-engram-plugin`, `project=sias-app`, directory pointing to a temp git repo with remote `engram`, one observation.

Doctor result before conceptual repair:

```text
before warning {'total': 4, 'ok': 2, 'warnings': 2, 'blocked': 0, 'errors': 0}
manual_session_name_project_mismatch warning 1 confirm True
session_project_directory_mismatch warning 1 confirm True
sqlite_lock_contention ok 0
sync_mutation_required_fields ok 0
```

Expected: PASS. This matches the real `sias-app` failure pattern where sessions are FK/project-consistent but semantically assigned to the wrong project.

### Conceptual repair validation on clone only

Applied SQL to the clone only:

```sql
BEGIN;
UPDATE sessions SET project='engram'
WHERE id IN ('manual-save-engram','manual-save-sdd-engram-plugin') AND project='sias-app';
UPDATE observations SET project='engram'
WHERE session_id IN ('manual-save-engram','manual-save-sdd-engram-plugin') AND project='sias-app';
UPDATE user_prompts SET project='engram'
WHERE session_id IN ('manual-save-engram','manual-save-sdd-engram-plugin') AND project='sias-app';
COMMIT;
```

Doctor result after conceptual repair:

```text
after sias ok {'total': 4, 'ok': 4, 'warnings': 0, 'blocked': 0, 'errors': 0}
manual_session_name_project_mismatch ok 0
session_project_directory_mismatch ok 0
sqlite_lock_contention ok 0
sync_mutation_required_fields ok 0
```

Expected: PASS. This validates the future repair direction but does not add repair behavior to the MVP.

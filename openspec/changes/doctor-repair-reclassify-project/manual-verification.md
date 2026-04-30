# Manual Verification: doctor-repair-reclassify-project

All verification below used SQLite `.backup` clones under `/tmp`. The production `~/.engram/engram.db` was not mutated.

## Clone setup

```bash
tmpdir=$(mktemp -d /tmp/engram-doctor-repair-clone-XXXXXX)
sqlite3 "$HOME/.engram/engram.db" ".backup '$tmpdir/engram.db'"
```

Clone used for this run:

```text
/tmp/engram-doctor-repair-clone-rvYGNY
```

Synthetic contamination inserted into the clone only:

- `manual-save-engram`, `project=sias-app`, empty directory, 1 observation, 1 prompt.
- `manual-save-sdd-engram-plugin`, `project=sias-app`, directory pointing at a temp git repo whose remote resolves to `engram`, 1 observation.

## Manual-session repair flow

Command:

```bash
ENGRAM_DATA_DIR=/tmp/engram-doctor-repair-clone-rvYGNY \
  go run ./cmd/engram doctor repair \
  --project sias-app \
  --check manual_session_name_project_mismatch \
  --plan
```

Observed:

```json
{
  "status": "planned",
  "counts": {
    "sessions_planned": 1,
    "observations_planned": 1,
    "prompts_planned": 1,
    "sessions_applied": 0,
    "observations_applied": 0,
    "prompts_applied": 0
  }
}
```

Dry-run was then executed and confirmed no database rows moved.

Apply command:

```bash
ENGRAM_DATA_DIR=/tmp/engram-doctor-repair-clone-rvYGNY \
  go run ./cmd/engram doctor repair \
  --project sias-app \
  --check manual_session_name_project_mismatch \
  --apply
```

Observed:

```text
status: applied
sessions_applied: 1
observations_applied: 1
prompts_applied: 1
backup exists: /tmp/engram-doctor-repair-clone-rvYGNY/backups/engram-repair-20260429T220435.834327000Z.db
```

## Directory-mismatch repair flow

Command:

```bash
ENGRAM_DATA_DIR=/tmp/engram-doctor-repair-clone-rvYGNY \
  go run ./cmd/engram doctor repair \
  --project sias-app \
  --check session_project_directory_mismatch \
  --plan
```

Observed action:

```json
{
  "session_id": "manual-save-sdd-engram-plugin",
  "from_project": "sias-app",
  "to_project": "engram",
  "reason_code": "session_project_directory_mismatch",
  "evidence_source": "git_remote"
}
```

Apply observed:

```text
status: applied
sessions_applied: 1
observations_applied: 1
prompts_applied: 0
backup_exists: true
```

## Final doctor check

Command:

```bash
ENGRAM_DATA_DIR=/tmp/engram-doctor-repair-clone-rvYGNY \
  go run ./cmd/engram doctor --json --project sias-app
```

Observed:

```text
ok {'total': 4, 'ok': 4, 'warnings': 0, 'blocked': 0, 'errors': 0}
manual_session_name_project_mismatch ok 0
session_project_directory_mismatch ok 0
sqlite_lock_contention ok 0
sync_mutation_required_fields ok 0
```

Expected: PASS.

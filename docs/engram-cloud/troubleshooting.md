[← Back to Engram Cloud](./README.md)

# Engram Cloud Troubleshooting

Use this guide when local Engram saves work but cloud sync does not advance. The safest rule is simple: **local SQLite is the source of truth; cloud is replication**. Back up the local database before repairing anything.

---

## Quick Triage

Run these commands first:

```bash
engram version
engram cloud status
engram cloud upgrade doctor --project <project>
engram sync --cloud --status --project <project>
```

Check three things:

| Signal | Healthy value | What it means |
|---|---|---|
| `engram version` | Latest release on client and server | Client/server version skew can block old chunk sync |
| `cloud status` | configured + auth ready | CLI has a server URL and runtime token |
| `doctor` | `ready` or actionable repair | Local metadata is safe to upload |
| `last_acked_seq` | Advances after sync | Cloud accepted the pending journal |

If the dashboard shows `0` observations but local saves exist, the cloud server has not accepted the client's pending sync yet. Do not delete local data.

---

## Command Map

Use the supported commands exactly:

```bash
engram cloud config --server https://your-cloud-host
engram cloud status
engram cloud enroll <project>
engram sync --cloud --project <project>
```

`engram cloud config --status` is not the documented status command. Use `engram cloud status`.

Cloud auth token is runtime config:

```bash
export ENGRAM_CLOUD_TOKEN="your-token"
```

The local `~/.engram/cloud.json` stores the server URL. The token is intentionally read from the environment.

---

## Error: `chunk_id does not match payload content hash`

This error means the legacy chunk upload endpoint rejected a payload because the client-provided `chunk_id` did not match the server-computed canonical hash.

### Fix

Upgrade both sides to `v1.14.8` or newer:

```bash
brew update
brew upgrade engram
engram version
```

Redeploy or restart the cloud server so the server binary also runs `v1.14.8` or newer.

Then retry:

```bash
engram sync --cloud --project <project>
engram sync --cloud --status --project <project>
```

### Why this works

In `v1.14.8`, the server treats the client `chunk_id` as advisory. The server validates and canonicalizes the payload, computes its own chunk ID, stores using the server-computed ID, and returns that ID. Valid payloads no longer get blocked by client/server canonicalization drift.

---

## Error: `session payload directory is required` or observation payload fields are missing

This is the common legacy manual-save blocker:

```text
session payload directory is required and cannot be inferred from local state (seq=N entity=session op=upsert)
```

Another legacy blocker can appear for observation upserts:

```text
observation payload missing required upsert fields: title (seq=N entity=observation op=upsert)
canonicalize cloud chunk: mutations[768]: observation payload title is required for upsert
```

You may also see the failure only during the final server push, even after `doctor` says the project is ready:

```text
write chunk: cloud: push chunk ...: status 400: invalid push payload: sessions[N].directory is required
write chunk: cloud: push chunk ...: status 400: invalid push payload: observations[N].title is required
```

It means a historical `session` mutation in `sync_mutations` is missing `directory`, a local `sessions` row included in the project export still has an empty/null `directory`, a local `observations` row included in the project export is missing a cloud-push required field, or a historical `observation` mutation is missing one of the required upsert fields: `sync_id`, `session_id`, `type`, `title`, `content`, or `scope`. Newer Engram versions write these fields correctly, but old journal rows, local session rows, or exported local observation rows may still need repair before first cloud upload.

### Safe path: helper script

Engram includes a temporary rescue helper. Despite the historical file name, it repairs missing session directories in both `sync_mutations` payloads and local `sessions.directory` rows, missing observation payload fields, and exported local `observations` rows whose required cloud-push fields are empty:

```bash
tools/repair-missing-session-directory.sh <project>
```

Run it from inside the real project directory for session directory repairs. Observation repairs do not require a directory argument. Dry-run is the default.

```bash
cd /absolute/path/to/project
tools/repair-missing-session-directory.sh <project>
```

Review the preview. For session repairs, confirm the detected `Directory:` is correct. For observation repairs, confirm the `Local observation row` is the authoritative local data. If an observation field such as `title` is still missing and the local row cannot fully infer it, preview an interactive repair first:

```bash
tools/repair-missing-session-directory.sh --interactive --seq 1677 sias-app
```

The interactive mode shows the mutation payload, matching local observation row when available, and a short content excerpt so you can provide only the missing required observation fields. Dry-run is still the default, so review the patched payload first. When it looks correct, rerun with `--apply --interactive`:

```bash
tools/repair-missing-session-directory.sh --apply --interactive --seq 1677 sias-app
```

For non-interactive repairs that can be inferred completely from local state, apply directly:

```bash
tools/repair-missing-session-directory.sh --apply <project>
```

If doctor reveals another legacy blocker after each repair, use loop mode after you have reviewed a one-shot dry-run:

```bash
tools/repair-missing-session-directory.sh --apply --interactive --all <project>
```

Loop mode repairs exactly one supported blocker (`entity=session|observation op=upsert`), reruns `engram cloud upgrade doctor --project <project>`, then repeats until doctor no longer reports a supported blocker. If doctor reports ready but local `sessions` rows included in the project export still have empty/null `directory`, loop mode applies that fallback repair and reruns doctor once more. If exported local `observations` rows are missing required fields such as `title`, loop mode applies that fallback repair next and reruns doctor again. It still stops on unsupported blockers, project mismatches, or observation data that cannot be fully inferred. In non-interactive loop mode, rerun with `--interactive` when the script asks for human-provided observation fields.

If one-shot mode finds no doctor blocker but reports local sessions with empty/null directory, preview and apply the fallback explicitly:

```bash
tools/repair-missing-session-directory.sh --fix-empty-sessions <project>
tools/repair-missing-session-directory.sh --apply --fix-empty-sessions <project>
```

If final push fails with `observations[N].title is required` even though doctor is ready, preview and apply the exported observation fallback explicitly:

```bash
tools/repair-missing-session-directory.sh --fix-empty-observations <project>
tools/repair-missing-session-directory.sh --apply --fix-empty-observations <project>
```

`--fix-exported` runs both exported-row fallbacks in one shot:

```bash
tools/repair-missing-session-directory.sh --fix-exported <project>
tools/repair-missing-session-directory.sh --apply --fix-exported <project>
```

Use `--max` to cap the number of repairs and avoid accidental infinite loops. The default is `20`:

```bash
tools/repair-missing-session-directory.sh --apply --interactive --all --max 5 <project>
```

Then rerun the normal flow:

```bash
engram cloud upgrade doctor --project <project>
engram cloud upgrade repair --project <project> --dry-run
engram cloud upgrade repair --project <project> --apply
engram sync --cloud --project <project>
```

### What the script changes

For session repairs, the script patches one legacy row in `sync_mutations` by adding a JSON field:

```json
"directory": "/absolute/path/to/project"
```

It also updates `sessions.directory` only when the matching session row exists and its directory is empty. In the fallback path for `sessions[N].directory is required`, it updates only local `sessions` rows included in the requested project export scope where `directory IS NULL OR directory = ''`; it does not modify `sync_mutations`.

For observation repairs, the script reads the authoritative local row from `observations` using `payload.sync_id` or `entity_key`, then fills only missing or empty fields in the mutation payload:

```text
sync_id, session_id, type, title, content, scope
```

It does not invent values in non-interactive mode. If the local observation row is missing, or the required payload fields would still be empty after patching, the script exits non-zero without modifying the database. Use `--interactive` when you need to provide missing observation values manually after reviewing the payload and content excerpt.

For exported local observation repairs, the script changes only `observations` rows that belong to the requested project export scope:

- rows where `ifnull(project, '') = '<project>'`
- rows with empty `project` whose `session_id` belongs to a `sessions.project = '<project>'` session

It does not touch `sync_mutations`. Safe inferred fields are filled as follows: empty `sync_id` becomes the row `id`, empty `type` becomes `manual`, empty `title` is inferred from non-empty `content`, and empty `scope` becomes `project`. Empty `session_id` is always blocked. Empty `content` is blocked unless you run with `--interactive` and provide it yourself.

It never changes `last_acked_seq`, never deletes mutations, and creates a timestamped database backup before each applied blocker. Loop mode can be noisy because it intentionally keeps one backup per repair.

### How the script finds `seq`

If you do not pass `--seq`, the script runs:

```bash
engram cloud upgrade doctor --project <project>
```

and extracts the first matching blocker:

```text
seq=N entity=session op=upsert
seq=N entity=observation op=upsert
```

If you already know the sequence:

```bash
tools/repair-missing-session-directory.sh --seq 873 <project>
tools/repair-missing-session-directory.sh --apply --seq 873 <project>
```

Loop mode does not accept `--seq`; it always asks doctor for the next supported blocker each iteration.

### How the script chooses `directory`

Precedence:

1. Explicit directory argument.
2. `git rev-parse --show-toplevel` from the current directory.
3. `pwd`.

The directory must be absolute. Good examples:

```text
/home/user/work/sias-app
/Users/user/work/sias-app
C:/Users/user/work/sias-app
```

Bad example:

```text
sias-app
```

On Windows/Git Bash, prefer forward slashes (`C:/Users/user/work/sias-app`) to avoid JSON and SQL escaping problems.

### Explicit directory mode

Use this when you are not currently inside the project directory:

```bash
tools/repair-missing-session-directory.sh --apply --seq 873 sias-app C:/Users/user/work/sias-app
```

### Manual inspection

If you want to inspect before using the helper:

```bash
sqlite3 ~/.engram/engram.db "select seq, entity, op, entity_key, payload from sync_mutations where seq = 873;"
sqlite3 ~/.engram/engram.db "select id, project, directory from sessions where id = 'manual-save-current';"
sqlite3 ~/.engram/engram.db "select id, project, started_at, directory from sessions where project = '<project>' and (directory is null or directory = '');"
sqlite3 ~/.engram/engram.db "select s.id, s.project, s.started_at, s.directory from sessions s where (s.directory is null or s.directory = '') and (s.project = '<project>' or s.id in (select session_id from observations where ifnull(project, '') = '<project>' union select session_id from user_prompts where ifnull(project, '') = '<project>'));"
sqlite3 ~/.engram/engram.db "select o.id, ifnull(o.sync_id, '') as sync_id, ifnull(o.session_id, '') as session_id, ifnull(o.project, '') as project, ifnull(o.type, '') as type, ifnull(o.title, '') as title, ifnull(o.scope, '') as scope from observations o where (ifnull(o.project, '') = '<project>' or (ifnull(o.project, '') = '' and o.session_id in (select id from sessions where ifnull(project, '') = '<project>'))) and (ifnull(o.sync_id, '') = '' or ifnull(o.session_id, '') = '' or ifnull(o.type, '') = '' or ifnull(o.title, '') = '' or ifnull(o.content, '') = '' or ifnull(o.scope, '') = '');"
```

Do not manually edit SQLite without a backup.

---

## Error: `transport_failed`

`transport_failed` is a wrapper around network, auth, server, or payload errors. Look for the concrete error message below it.

| Concrete error | Next step |
|---|---|
| `chunk_id does not match payload content hash` | Upgrade client and server to `v1.14.8` or newer |
| `session payload directory is required` | Run the missing session directory helper |
| `sessions[N].directory is required` | Run the missing session directory helper with `--fix-empty-sessions` or `--all` |
| `observation payload title is required for upsert` | Run the missing session directory helper; it also repairs missing observation payload fields from local `observations` |
| `observations[N].title is required` | Run the missing session directory helper with `--fix-empty-observations` or `--all` |
| `401` or `auth_required` | Check `ENGRAM_CLOUD_TOKEN` on the client and server |
| `403` or `policy_forbidden` | Check `ENGRAM_CLOUD_ALLOWED_PROJECTS` on the server |
| `server_unsupported` | Redeploy a cloud server with mutation endpoints |

---

## Verification Checklist

After any repair, verify in this order:

```bash
engram cloud status
engram cloud upgrade doctor --project <project>
engram cloud upgrade repair --project <project> --dry-run
engram cloud upgrade repair --project <project> --apply
engram sync --cloud --project <project>
engram sync --cloud --status --project <project>
```

Expected result:

- `doctor` no longer reports the same blocker.
- `sync --cloud` completes without canonicalization errors.
- `last_acked_seq` advances.
- Dashboard stats stop showing `0` once data has been accepted by cloud.

---

## What Not To Do

- Do not delete `sync_mutations` rows to make the error disappear.
- Do not edit `last_acked_seq` manually.
- Do not invent a relative `directory` like `sias-app`.
- Do not assume dashboard `0` means local data is gone.
- Do not run repair without a database backup.

---

## Next Steps

- Cloud setup: [Quickstart](./quickstart.md)
- Full command reference: [DOCS.md — Cloud CLI](../../DOCS.md#cloud-cli-opt-in)
- Autosync details: [DOCS.md — Cloud Autosync](../../DOCS.md#cloud-autosync)

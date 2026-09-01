# obsidian-export Specification

## Purpose

CLI command that reads the Engram SQLite store and writes a structured Obsidian-compatible markdown vault with YAML frontmatter, wikilinks, hub notes, and incremental sync state. Supports graph config bootstrap and a built-in watch-mode daemon for automatic periodic sync.

---

## Requirements

### Requirement: REQ-EXPORT-01 — Command Parsing

The `engram obsidian-export` subcommand MUST accept the following flags:
- `--vault <path>` (required): absolute or relative path to the Obsidian vault root
- `--project <name>` (optional): select a known project for export
- `--all` (optional): explicitly export every project
- `--limit <n>` (optional): cap exported observations at n (default: unlimited)
- `--since <date>` (optional): export only observations created after this ISO-8601 date

Missing `--vault` MUST produce a descriptive error and exit code 1.

#### Scenario: Happy path — all flags provided

- GIVEN the user runs `engram obsidian-export --vault ~/vault --project eng --limit 100 --since 2026-01-01`
- WHEN the command is parsed
- THEN export runs scoped to project "eng", max 100 observations, created after 2026-01-01

#### Scenario: Omitted project uses current project

- GIVEN the current project resolves to "eng"
- WHEN the user runs `engram obsidian-export --vault ~/vault`
- THEN export runs scoped to "eng"

#### Scenario: Explicit all exports every project

- GIVEN the store contains records from multiple projects
- WHEN the user runs `engram obsidian-export --vault ~/vault --all`
- THEN export includes every project

#### Scenario: Missing required --vault flag

- GIVEN the user runs `engram obsidian-export --project eng`
- WHEN the command is parsed
- THEN an error message "flag --vault is required" is printed to stderr
- AND the process exits with code 1

#### Scenario: Unknown flag

- GIVEN the user runs `engram obsidian-export --unknown`
- WHEN the command is parsed
- THEN an error is returned and the process exits with code 1

---

### Requirement: REQ-EXPORT-02 — Vault Directory Structure

The exporter MUST create the following directory hierarchy inside `{vault}/engram/`:

```
{vault}/engram/
├── {project}/{type}/{slug}-{id}.md   ← observation notes
├── _sessions/{session-id}.md          ← session hub notes
├── _topics/{topic-prefix}.md          ← topic cluster hub notes
└── .engram-sync-state.json            ← incremental sync state
```

All directories MUST be created if they do not exist. The exporter MUST NOT write outside the `{vault}/engram/` subdirectory.

#### Scenario: Fresh vault — directories don't exist

- GIVEN `--vault ~/vault` points to a directory without an `engram/` subfolder
- WHEN export runs
- THEN `~/vault/engram/` and all needed subdirectories are created
- AND observation files are written inside them

#### Scenario: Vault directory not writable

- GIVEN the vault path exists but the user has no write permission
- WHEN export runs
- THEN an error is printed to stderr and the process exits with code 1

---

### Requirement: REQ-EXPORT-03 — Observation → Markdown Conversion

Each observation MUST be written as a markdown file with:
- A YAML frontmatter block containing: `type`, `project`, `scope`, `topic_key`, `session_id`, `created_at`, `tags`
- The observation `content` field as the markdown body
- A `## Wikilinks` section listing all generated `[[links]]`

`topic_key` and `session_id` in frontmatter MAY be empty strings when the observation has no value.

#### Scenario: Observation with all fields populated

- GIVEN an observation with type="bugfix", project="eng", scope="project", topic_key="auth/jwt", session_id="abc123", created_at="2026-01-01T10:00:00Z", content="Fixed the bug"
- WHEN the markdown file is generated
- THEN the file starts with a YAML frontmatter block containing all six fields
- AND the body contains "Fixed the bug"
- AND a `## Wikilinks` section lists `[[session-abc123]]` and `[[topic-auth]]`

#### Scenario: Observation with no topic_key

- GIVEN an observation with no topic_key set
- WHEN the markdown file is generated
- THEN `topic_key: ""` appears in frontmatter
- AND no `[[topic-*]]` wikilink is emitted

---

### Requirement: REQ-EXPORT-04 — Session Hub Notes

For each unique `session_id` among exported observations, the exporter MUST generate a hub note at `_sessions/{session-id}.md` listing all observations in that session as wikilinks.

#### Scenario: Session with multiple observations

- GIVEN observations obs-1 and obs-2 both have session_id="sess-42"
- WHEN export runs
- THEN `_sessions/sess-42.md` is created
- AND it contains `[[obs-1-slug-1]]` and `[[obs-2-slug-2]]` as wikilinks

#### Scenario: Observation with no session_id

- GIVEN an observation with no session_id
- WHEN export runs
- THEN no session hub note is generated for that observation
- AND the observation note is still exported normally

---

### Requirement: REQ-EXPORT-05 — Topic Cluster Hub Notes

A topic hub note at `_topics/{prefix}.md` MUST be generated **only when ≥2 exported observations share the same topic_key prefix** (the first segment of a `/`-delimited topic_key).

Observations with an empty or absent topic_key MUST be excluded from prefix grouping.

#### Scenario: Two observations share a prefix → hub created

- GIVEN obs-A has topic_key="sdd/spec" and obs-B has topic_key="sdd/design"
- WHEN export runs
- THEN `_topics/sdd.md` is created listing both observations as wikilinks

#### Scenario: Only one observation with a given prefix → hub NOT created

- GIVEN only obs-A has topic_key="auth/jwt" and no other observation shares prefix "auth"
- WHEN export runs
- THEN `_topics/auth.md` is NOT created

#### Scenario: Observation with no topic_key → excluded from hub grouping

- GIVEN obs-C has no topic_key
- WHEN export runs
- THEN obs-C does not contribute to any topic hub prefix count

---

### Requirement: REQ-EXPORT-06 — Incremental Sync

The exporter MUST persist the last-export timestamp in `{vault}/engram/.engram-sync-state.json`. On subsequent runs, it MUST only write observations created or updated after that timestamp.

#### Scenario: First export — no state file

- GIVEN `.engram-sync-state.json` does not exist
- WHEN export runs
- THEN all matching observations are exported
- AND `.engram-sync-state.json` is created with `last_export_at` set to the current time

#### Scenario: Incremental export — state file exists

- GIVEN `.engram-sync-state.json` has `last_export_at: "2026-03-01T00:00:00Z"`
- AND a new observation was created at "2026-04-01T00:00:00Z"
- WHEN export runs without `--since`
- THEN only the new observation is written
- AND `.engram-sync-state.json` is updated to the new timestamp

#### Scenario: --since flag overrides state file

- GIVEN `.engram-sync-state.json` has `last_export_at: "2026-03-01T00:00:00Z"`
- AND the user passes `--since 2026-01-01`
- WHEN export runs
- THEN observations since 2026-01-01 are exported (overriding the state file timestamp)

---

### Requirement: REQ-EXPORT-07 — Collision-Safe File Naming

Observation files MUST be named `{slug}-{id}.md` where `{slug}` is a lowercase, hyphen-separated truncation of the observation title or first 40 chars of content, and `{id}` is the numeric observation ID.

#### Scenario: Two observations with identical content previews

- GIVEN obs-1 (id=1) and obs-2 (id=2) both start with "Fixed authentication"
- WHEN filenames are generated
- THEN obs-1 → `fixed-authentication-1.md` and obs-2 → `fixed-authentication-2.md`

---

### Requirement: REQ-EXPORT-08 — Idempotency

Re-running export MUST NOT create duplicate files. If a file already exists and the observation has not changed, the file MUST be left unchanged. If the observation content has changed, the file MUST be overwritten.

#### Scenario: Re-export with no changes

- GIVEN a vault with previously exported files
- AND no observations have changed
- WHEN export runs again
- THEN no files are written (no disk I/O for unchanged observations)

#### Scenario: Re-export after observation updated

- GIVEN obs-5 was exported previously
- AND obs-5's content has since been updated in the store
- WHEN export runs again
- THEN obs-5's markdown file is overwritten with the new content

---

### Requirement: REQ-EXPORT-09 — Deleted Observations Handling

Observations that are soft-deleted in the store (deleted_at IS NOT NULL) MUST NOT be exported. If a previously exported file corresponds to a now-deleted observation, it MUST be removed from the vault.

#### Scenario: Observation deleted after first export

- GIVEN obs-3 was exported to `eng/bugfix/some-fix-3.md`
- AND obs-3 is later soft-deleted in the store
- WHEN export runs again
- THEN `eng/bugfix/some-fix-3.md` is deleted from the vault

---

### Requirement: REQ-GRAPH-01 — `--graph-config` Flag

The `engram obsidian-export` command MUST accept a `--graph-config` flag with accepted values: `preserve` (default), `force`, `skip`.
Any other value MUST cause the process to print a descriptive error to stderr and exit with code 1.

#### Scenario: Valid flag value accepted

- GIVEN the user runs `engram obsidian-export --vault ./v --graph-config=force`
- WHEN the flag is parsed
- THEN export proceeds with force mode

#### Scenario: Invalid flag value rejected

- GIVEN the user runs `engram obsidian-export --vault ./v --graph-config=invalid`
- WHEN the flag is parsed
- THEN an error "invalid --graph-config value: invalid (accepted: preserve, force, skip)" is printed to stderr
- AND the process exits with code 1

---

### Requirement: REQ-GRAPH-02 — `preserve` Mode

When `--graph-config=preserve` (default):
- If `{vault}/.obsidian/graph.json` does NOT exist, the system MUST write the embedded default template.
- If `{vault}/.obsidian/graph.json` DOES exist, the system MUST leave it unchanged and log "graph.json exists, preserving user config".

#### Scenario: First run — graph.json absent

- GIVEN `{vault}/.obsidian/graph.json` does not exist
- WHEN export runs with `--graph-config=preserve` (or default)
- THEN `{vault}/.obsidian/graph.json` is created with the embedded default
- AND no warning is logged

#### Scenario: Subsequent run — graph.json present

- GIVEN `{vault}/.obsidian/graph.json` already exists with custom content
- WHEN export runs with `--graph-config=preserve`
- THEN the file is unchanged
- AND "graph.json exists, preserving user config" is logged

---

### Requirement: REQ-GRAPH-03 — `force` Mode

When `--graph-config=force`, the system MUST always overwrite `{vault}/.obsidian/graph.json` with the embedded default and log "graph.json overwritten with engram default".

#### Scenario: Force overwrites existing file

- GIVEN `{vault}/.obsidian/graph.json` exists with custom content
- WHEN export runs with `--graph-config=force`
- THEN the file is overwritten with the embedded default
- AND "graph.json overwritten with engram default" is logged

---

### Requirement: REQ-GRAPH-04 — `skip` Mode

When `--graph-config=skip`, the system MUST NOT read, write, or create `{vault}/.obsidian/graph.json`. No log is emitted.

#### Scenario: Skip never touches graph.json

- GIVEN `{vault}/.obsidian/graph.json` does not exist
- WHEN export runs with `--graph-config=skip`
- THEN `{vault}/.obsidian/graph.json` is NOT created

---

### Requirement: REQ-GRAPH-05 — Embedded Default Template

The embedded default `graph.json` MUST be stored as a file in `internal/obsidian/` and included via `//go:embed`.
It MUST contain EXACTLY the following values (no deviation):

| Key | Value |
|-----|-------|
| `collapse-filter` | `false` |
| `collapse-color-groups` | `true` |
| `collapse-display` | `true` |
| `collapse-forces` | `false` |
| `showArrow` | `false` |
| `textFadeMultiplier` | `0` |
| `nodeSizeMultiplier` | `1` |
| `lineSizeMultiplier` | `1` |
| `centerStrength` | `0.515147569444444` |
| `repelStrength` | `12.7118055555556` |
| `linkStrength` | `0.729210069444444` |
| `linkDistance` | `207` |
| `scale` | `0.1` |
| `showOrphans` | `true` |

Color groups (MUST be EXACTLY these 6, in this order):

| # | query | rgb |
|---|-------|-----|
| 1 | `path:engram/_sessions` | 14736466 |
| 2 | `path:engram/_topics` | 13893887 |
| 3 | `tag:#architecture` | 7935 |
| 4 | `tag:#bugfix` | 16711680 |
| 5 | `tag:#decision` | 65322 |
| 6 | `tag:#pattern` | 16741120 |

All color group entries MUST have `"a": 1`. No discovery group. No additional groups.

#### Scenario: Embedded default validates expected structure

- GIVEN the embedded `graph.json` is parsed
- WHEN a unit test validates its contents
- THEN all keys and values from the table above match exactly
- AND `colorGroups` has exactly 6 entries with the queries and rgb values listed

---

### Requirement: REQ-GRAPH-06 — `.obsidian/` Directory Creation

If `{vault}/.obsidian/` does not exist when graph config needs to be written (modes `preserve` or `force`), the system MUST create it before writing `graph.json`.
This MUST NOT error if the directory already exists.

#### Scenario: .obsidian dir missing — created automatically

- GIVEN `{vault}/.obsidian/` does not exist
- AND `--graph-config=preserve` or `--graph-config=force`
- WHEN export runs
- THEN `{vault}/.obsidian/` is created
- AND `graph.json` is written inside it

#### Scenario: .obsidian dir already exists — no error

- GIVEN `{vault}/.obsidian/` already exists
- WHEN export runs
- THEN no error is produced related to the directory

---

### Requirement: REQ-WATCH-01 — `--watch` Flag (Daemon Mode)

The `engram obsidian-export` command MUST accept a `--watch` flag.
Without `--watch`: command runs once and exits (unchanged behavior).
With `--watch`: command loops indefinitely until SIGINT or SIGTERM is received.

#### Scenario: Without --watch — single run

- GIVEN the user runs `engram obsidian-export --vault ./v`
- WHEN export completes
- THEN the process exits with code 0

#### Scenario: With --watch — loops until signal

- GIVEN the user runs `engram obsidian-export --vault ./v --watch`
- WHEN the first export completes
- THEN the process does NOT exit; it waits for the next interval
- AND continues looping until SIGINT or SIGTERM

---

### Requirement: REQ-WATCH-02 — `--interval` Flag

The `--interval` flag MUST accept Go duration strings (e.g. `30s`, `5m`, `1h`).
It is only active when `--watch` is set. Default value: `10m`.
Values below `1m` MUST cause the process to exit with code 1 and a descriptive error.
Unparseable values MUST cause the process to exit with code 1.

#### Scenario: Valid interval accepted

- GIVEN the user runs `engram obsidian-export --vault ./v --watch --interval 5m`
- WHEN the flag is parsed
- THEN the daemon runs with a 5-minute interval

#### Scenario: Interval below minimum rejected

- GIVEN the user runs with `--watch --interval 30s`
- WHEN the flag is parsed
- THEN an error "--interval must be >= 1m; got 30s" is printed to stderr
- AND the process exits with code 1

#### Scenario: Unparseable interval rejected

- GIVEN the user runs with `--watch --interval banana`
- WHEN the flag is parsed
- THEN a parse error is printed to stderr
- AND the process exits with code 1

---

### Requirement: REQ-WATCH-03 — First-Run Semantics

When `--watch` is enabled, the FIRST export cycle MUST run IMMEDIATELY on startup without waiting for the interval.
Subsequent cycles run every `interval` after the previous cycle completes.

#### Scenario: First export is immediate

- GIVEN the user runs `engram obsidian-export --vault ./v --watch --interval 10m`
- WHEN the process starts
- THEN an export cycle begins immediately (within milliseconds, not after 10m)
- AND the next cycle starts approximately 10m after the first completes

---

### Requirement: REQ-WATCH-04 — Per-Cycle Logging

Each completed watch cycle MUST log a single line in this exact format:

```
[{RFC3339-timestamp}] sync: created={n} updated={n} deleted={n} skipped={n} hubs={n}
```

On cycle error: the error MUST be logged and the loop MUST continue. The process MUST NOT exit on transient failures.

#### Scenario: Successful cycle log line

- GIVEN a watch cycle completes with 5 created, 0 updated, 0 deleted, 1725 skipped, 277 hubs
- WHEN the cycle ends
- THEN a log line matching `[2026-04-06T21:00:00Z] sync: created=5 updated=0 deleted=0 skipped=1725 hubs=277` is printed (timestamp varies)

#### Scenario: Cycle error — loop continues

- GIVEN a watch cycle fails with a transient error (e.g. locked DB)
- WHEN the error occurs
- THEN the error is logged
- AND the next cycle runs at the next interval tick

---

### Requirement: REQ-WATCH-05 — Graceful Shutdown

On SIGINT (Ctrl+C) or SIGTERM, the system MUST:
1. Finish the current cycle if one is in progress.
2. Log "shutting down watch mode".
3. Exit with code 0.
4. NOT leave a corrupted state file.

#### Scenario: SIGINT during idle interval

- GIVEN the daemon is waiting between cycles
- WHEN SIGINT is received
- THEN "shutting down watch mode" is logged
- AND the process exits with code 0

#### Scenario: SIGTERM during active export

- GIVEN a cycle is actively writing observation files
- WHEN SIGTERM is received
- THEN the current cycle completes
- AND "shutting down watch mode" is logged
- AND the process exits with code 0
- AND `.engram-sync-state.json` is valid (not corrupted)

---

### Requirement: REQ-WATCH-06 — Flag Interaction

`--watch` MUST be compatible with `--project`, `--graph-config`, `--since`, `--force`, and `--limit`. All flags apply to every cycle with these exceptions:
- `--since` is respected ONLY on the first cycle; subsequent cycles use the state file cutoff.
- `--graph-config` is applied ONLY on the first cycle; subsequent cycles skip the graph config step.

#### Scenario: --project scopes every cycle

- GIVEN `--watch --project engram`
- WHEN the daemon runs multiple cycles
- THEN each cycle exports only observations from project "engram"

#### Scenario: --since only on first cycle

- GIVEN `--watch --since 2026-01-01`
- WHEN the first cycle runs
- THEN observations since 2026-01-01 are exported
- WHEN the second cycle runs
- THEN the state file cutoff is used (not --since)

#### Scenario: --graph-config=force only on first cycle

- GIVEN `--watch --graph-config=force`
- WHEN the first cycle runs
- THEN `graph.json` is overwritten
- WHEN the second cycle runs
- THEN `graph.json` is NOT touched (graph-config step is skipped for subsequent cycles)

---

### Requirement: REQ-WATCH-07 — Mutual Exclusion

`--interval` without `--watch` MUST cause an error exit.
`--watch` with an invalid `--interval` MUST cause an error exit.

#### Scenario: --interval without --watch is rejected

- GIVEN the user runs `engram obsidian-export --vault ./v --interval 5m`
- WHEN the flags are parsed
- THEN an error "--interval requires --watch" is printed to stderr
- AND the process exits with code 1

#### Scenario: --watch with invalid interval is rejected

- GIVEN the user runs `engram obsidian-export --vault ./v --watch --interval 10s`
- WHEN the flags are parsed
- THEN an error "--interval must be >= 1m; got 10s" is printed to stderr
- AND the process exits with code 1

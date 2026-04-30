# operational-diagnostics Specification Delta

## Requirements

### Requirement: REQ-DOCTOR-REPAIR-01 — Explicit CLI Repair Modes

`engram doctor repair` MUST require `--project`, `--check`, and exactly one of `--plan`, `--dry-run`, or `--apply`. It MUST fail loudly and provide usage when no mode is supplied, multiple modes are supplied, the check is unsupported, or required arguments are missing.

#### Scenario: Default repair does not mutate
- GIVEN a user runs `engram doctor repair --project sias-app --check session_project_directory_mismatch`
- WHEN argument validation runs
- THEN the command exits non-zero
- AND stderr explains that `--plan`, `--dry-run`, or `--apply` is required
- AND no database rows are changed

#### Scenario: Supported repair modes emit JSON
- GIVEN a valid supported repair request
- WHEN the user runs with `--plan`, `--dry-run`, or `--apply`
- THEN stdout is valid JSON
- AND it includes `project`, `check`, `mode`, `status`, `actions`, and row-count fields

### Requirement: REQ-DOCTOR-REPAIR-02 — Directory Mismatch Reclassification

For `session_project_directory_mismatch`, repair MUST reclassify each selected session from the requested project to the project inferred from trusted directory evidence. Trusted evidence is limited to `git_remote` and `git_root`.

#### Scenario: Trusted directory evidence plans reclassification
- GIVEN a session under project `sias-app` whose directory detects project `engram` from `git_remote`
- WHEN `engram doctor repair --project sias-app --check session_project_directory_mismatch --plan` runs
- THEN the JSON plan includes that session ID
- AND the action target project is `engram`
- AND the action says no mutation was applied

#### Scenario: Untrusted evidence is skipped
- GIVEN a mismatch finding without trusted `git_remote` or `git_root` evidence
- WHEN repair planning runs
- THEN no apply action is created for that session
- AND JSON includes a skipped reason

### Requirement: REQ-DOCTOR-REPAIR-03 — Manual Session Name Reclassification

For `manual_session_name_project_mismatch`, repair MUST only reclassify exact `manual-save-{known_project}` sessions. It MUST skip when trusted directory evidence contradicts the manual-name target.

#### Scenario: Exact manual-save known project is repairable
- GIVEN session `manual-save-engram` is stored under `sias-app`
- AND `engram` is a known project in the local store
- AND no trusted directory evidence contradicts `engram`
- WHEN repair planning runs for `manual_session_name_project_mismatch`
- THEN the plan targets only that session for project `engram`

#### Scenario: Trusted directory contradiction blocks manual repair
- GIVEN session `manual-save-engram` is stored under `sias-app`
- AND its directory detects project `other` from `git_root`
- WHEN repair planning runs
- THEN the session is skipped
- AND JSON includes reason `trusted_directory_contradicts_manual_name`

### Requirement: REQ-DOCTOR-REPAIR-04 — Apply Safety Boundary

`--apply` MUST create a SQLite backup before opening the write transaction. The transaction MUST update only `sessions.project`, `observations.project`, and `user_prompts.project` for selected session IDs. It MUST NOT delete rows, deduplicate rows, change sync cursors, edit last-acked state, or mutate `sync_mutations`/cloud tables.

#### Scenario: Apply updates only allowed project columns
- GIVEN a repair plan with selected session IDs
- WHEN `--apply` executes
- THEN a backup file is created before the transaction
- AND `sessions.project` is updated for selected sessions
- AND `observations.project` and `user_prompts.project` are updated for those session IDs
- AND `sync_state` and `sync_mutations` remain unchanged

#### Scenario: Dry run is non-mutating
- GIVEN a repairable mismatch exists
- WHEN `--dry-run` executes
- THEN JSON reports planned row counts
- AND subsequent SQL counts/projects are unchanged

### Requirement: REQ-DOCTOR-REPAIR-05 — Clone Verification

The implementation MUST include manual clone verification for the `sias-app` contamination shape before release notes/docs claim the repair is validated.

#### Scenario: Synthetic clone verifies real issue shape
- GIVEN a cloned database with `manual-save-engram` and Engram directory sessions stored under `sias-app`
- WHEN plan, dry-run, and apply are executed in order
- THEN plan and dry-run agree
- AND apply moves only targeted rows to `engram`
- AND a backup is available for restore

# Operational Diagnostics Specification

## Purpose

Define read-only operational diagnostics reusable by CLI and MCP with one shared contract and deterministic check semantics.

## Requirements

### Requirement: REQ-OD-001 Shared Diagnostic Envelope

The system MUST return one diagnostic envelope schema for CLI `--json` and MCP `mem_doctor`: `{status, summary, checks[]}`. Each check result MUST include `check_id`, `result` (`ok|warning|blocked|error`), `severity`, `reason_code`, `evidence`, `safe_next_step`, and `requires_confirmation`.

#### Scenario: CLI JSON and MCP parity

- GIVEN the same project scope and selected checks
- WHEN `engram doctor --json` and `mem_doctor` are executed
- THEN both responses use the same envelope keys and check result keys

#### Scenario: Human-readable output remains adapter-specific

- GIVEN `engram doctor` without `--json`
- WHEN findings are present
- THEN plain text MAY differ in formatting but MUST preserve the same check semantics

### Requirement: REQ-OD-002 CLI Behavior and Exit Semantics

`engram doctor` MUST support default text output and `--json`. `--project` MUST scope diagnostics to that project, and `--check CODE` MUST run only the named registered check. Unknown check codes MUST return an error envelope in JSON mode and a clear failure message in text mode.

#### Scenario: Single-check execution

- GIVEN `--check sync_mutation_required_fields`
- WHEN the command runs
- THEN only that check appears in output

#### Scenario: Invalid check code

- GIVEN `--check not_real`
- WHEN the command runs
- THEN execution fails loudly with invalid-check diagnostics and no silent fallback

### Requirement: REQ-OD-003 MCP mem_doctor Contract

`mem_doctor` MUST call the same domain runner as CLI and return the same JSON envelope. It MUST accept optional project override following existing read-tool validation rules.

#### Scenario: Optional project override

- GIVEN `mem_doctor` input with `project: "engram"`
- WHEN the project exists
- THEN diagnostics run for `engram` and return the standard envelope

#### Scenario: Unknown override fails deterministically

- GIVEN `mem_doctor` input with an unknown project
- WHEN validation runs
- THEN response returns a structured unknown-project error and no findings payload

### Requirement: REQ-OD-004 MVP Check Semantics

The system MUST implement these read-only checks with stable IDs and reason codes:
- `session_project_directory_mismatch`: emit `warning` when session `project` and directory-derived project disagree.
- `manual_session_name_project_mismatch`: emit `warning` when `manual-save-{suffix}` disagrees with `sessions.project`.
- `sync_mutation_required_fields`: emit `blocked` when pending mutation payload misses required fields.
- `sqlite_lock_contention`: emit `warning` for contention/drift signals and `error` when probe cannot evaluate lock state.

#### Scenario: Healthy store

- GIVEN data with no mismatch, no invalid mutations, and normal lock signals
- WHEN all checks run
- THEN each check returns `ok` with evidence of what was evaluated

#### Scenario: Required fields missing in pending mutation

- GIVEN a pending sync mutation missing required payload fields
- WHEN `sync_mutation_required_fields` runs
- THEN result is `blocked` with deterministic `reason_code`, failing evidence, and safe next step

#### Scenario: SQLite contention detected

- GIVEN lock probe shows contention indicators
- WHEN `sqlite_lock_contention` runs
- THEN result is `warning` with probe evidence and conservative safe next step

### Requirement: REQ-OD-005 MVP Non-Repair Constraint

The doctor MVP MUST NOT perform repair, apply, or write transactions. `requires_confirmation` MUST be false for all MVP checks because no apply path exists.

#### Scenario: Findings do not mutate state

- GIVEN any failing check
- WHEN doctor execution completes
- THEN no repair action is executed and output only explains impact plus safe next step

### Requirement: REQ-OD-006 Testing and Manual Verification

Tests MUST cover happy, finding, invalid-check, and error paths for CLI text, CLI JSON, and MCP parity. Manual verification SHOULD include one scenario per MVP check plus a healthy-store baseline.

#### Scenario: Automated parity test

- GIVEN seeded deterministic fixtures
- WHEN CLI `--json` and `mem_doctor` are invoked
- THEN normalized envelopes are equivalent

#### Scenario: Manual walkthrough matrix

- GIVEN local fixtures for each MVP check condition
- WHEN an operator runs `engram doctor` and `engram doctor --json`
- THEN outputs are understandable for humans and complete for agents

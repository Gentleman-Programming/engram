# Spec: memory-conflict-audit (Phase 3)

**Change**: memory-conflict-audit
**Artifact store**: hybrid (engram + openspec)
**Scope**: All new capabilities — full spec (no existing spec to delta against)

---

## Purpose

Define what MUST be true after Phase 3 of the memory-conflict-surfacing track ships.
Covers CLI sub-commands, HTTP endpoints, store contracts, index migration, and Phase 2 fold-ins.
All requirements are additive. Phase 1 and Phase 2 surfaces (relation lifecycle, sync, MCP tools) MUST remain unchanged.

---

## Domain: conflict-audit-cli

### Requirement: engram conflicts list

The system MUST expose `engram conflicts list` that returns memory_relations rows filtered by optional project, status, since, and limit parameters. When `--project` is omitted, the cwd-detected project MUST be used. Output format MUST match `cmdStats` label-colon style.

#### Scenario: list with explicit project and status filter

- GIVEN a database with 5 `memory_relations` rows for project "alpha", 3 with `judgment_status='pending'` and 2 with `judgment_status='accepted'`
- WHEN `engram conflicts list --project alpha --status pending` is run
- THEN exactly 3 rows are printed, each showing `relation_id`, `relation_type`, `judgment_status`, and `created_at`
- AND rows for project "beta" or status "accepted" are NOT printed

#### Scenario: list falls back to cwd-detected project when --project omitted

- GIVEN cwd resolves to project "myproject" and 2 pending relations exist for it
- WHEN `engram conflicts list` is run without `--project`
- THEN the 2 rows for "myproject" are printed (not all rows in the DB)

#### Scenario: list with --limit cap

- GIVEN 200 pending relations exist for the project
- WHEN `engram conflicts list --limit 10`
- THEN exactly 10 rows are printed and no error is raised

#### Scenario: list returns empty when no matching rows

- GIVEN no relations exist with `judgment_status='accepted'` for the project
- WHEN `engram conflicts list --status accepted`
- THEN output states zero results and exits with code 0

---

### Requirement: engram conflicts show

The system MUST expose `engram conflicts show <relation_id>` that prints the full detail of one relation row, including the source observation content snippet and the target observation content snippet.

#### Scenario: show existing relation

- GIVEN a relation with `relation_id=42` exists, with source observation id=10 and target observation id=20
- WHEN `engram conflicts show 42`
- THEN the output includes `relation_id`, `relation_type`, `judgment_status`, `sync_id`, `created_at`, a snippet from observation 10, and a snippet from observation 20

#### Scenario: show unknown relation_id

- GIVEN no relation with `relation_id=999` exists
- WHEN `engram conflicts show 999`
- THEN a human-readable "not found" message is printed and the process exits with a non-zero code

---

### Requirement: engram conflicts stats

The system MUST expose `engram conflicts stats` that prints aggregate counts of relations grouped by `relation_type` and `judgment_status`, plus total deferred and dead queue sizes. `--project` scopes to one project; omitted means cwd-detected project.

#### Scenario: stats for a project with mixed statuses

- GIVEN project "alpha" has 3 pending, 1 accepted, 2 rejected relations, and 4 deferred + 1 dead queue rows
- WHEN `engram conflicts stats --project alpha`
- THEN output shows per-status counts and deferred=4, dead=1

#### Scenario: stats on empty project

- GIVEN project "new" has no relation rows
- WHEN `engram conflicts stats --project new`
- THEN all counts are 0 and process exits with code 0

---

### Requirement: engram conflicts scan

The system MUST expose `engram conflicts scan` that iterates observations for a project, runs `FindCandidates`, and either reports candidates (`--dry-run`, default) or inserts new pending relation rows (`--apply`). `--apply` MUST enforce a `--max-insert N` cap (default 100). When the cap is reached, the command MUST print a WARNING and stop without inserting further rows. Pairs with an existing relation row (any `judgment_status`) MUST be skipped without inserting.

#### Scenario: dry-run reports candidates without writing

- GIVEN 5 observation pairs qualify as conflict candidates
- WHEN `engram conflicts scan --dry-run`
- THEN output reports 5 candidates found and 0 rows inserted
- AND the `memory_relations` table is unchanged

#### Scenario: apply inserts up to max-insert cap

- GIVEN 150 observation pairs qualify as candidates and no existing relation rows exist
- WHEN `engram conflicts scan --apply --max-insert 50`
- THEN exactly 50 rows are inserted, a WARNING is printed indicating the cap was reached, and the command stops

#### Scenario: apply skips already-related pairs

- GIVEN 3 candidate pairs exist, 2 of which already have a relation row with any status
- WHEN `engram conflicts scan --apply`
- THEN only 1 new row is inserted (the pair without an existing relation)

#### Scenario: apply with no candidates

- GIVEN no observation pairs qualify as conflict candidates
- WHEN `engram conflicts scan --apply`
- THEN 0 rows are inserted and process exits with code 0

---

### Requirement: engram conflicts deferred

The system MUST expose `engram conflicts deferred` that lists rows from `sync_apply_deferred` with optional `--status` and `--limit` filters. `--replay` MUST invoke the existing `ReplayDeferred()` path (same as autosync cycle) and print counts of retried, succeeded, and dead rows. `--inspect <sync_id>` MUST print the full decoded payload of that single deferred row.

#### Scenario: list deferred queue

- GIVEN 3 deferred rows with status='deferred' and 1 with status='dead' exist
- WHEN `engram conflicts deferred`
- THEN all 4 rows are listed showing sync_id, status, retry_count, created_at

#### Scenario: replay triggers ReplayDeferred path

- GIVEN 2 deferred rows are eligible for retry
- WHEN `engram conflicts deferred --replay`
- THEN `ReplayDeferred()` is called, and output prints retried=2, succeeded=N, dead=M

#### Scenario: inspect a single deferred row

- GIVEN a deferred row with sync_id="abc-123" exists with a valid JSON payload
- WHEN `engram conflicts deferred --inspect abc-123`
- THEN the decoded payload is printed in human-readable form and process exits with code 0

#### Scenario: inspect unknown sync_id

- GIVEN no deferred row with sync_id="no-such" exists
- WHEN `engram conflicts deferred --inspect no-such`
- THEN a "not found" message is printed and process exits non-zero

---

## Domain: conflict-audit-http

### Requirement: GET /conflicts

The system MUST expose `GET /conflicts` on `engram serve` returning a paginated JSON list of relation rows. MUST support query params: `project` (string), `status` (string), `since` (RFC3339), `limit` (integer, default 50, max 500). Response shape MUST include a `total` field and a `relations` array.

#### Scenario: paginated list with project filter

- GIVEN 80 relations exist for project "alpha"
- WHEN `GET /conflicts?project=alpha&limit=50`
- THEN response is 200 with `relations` array of 50 items and `total=80`

#### Scenario: limit exceeds max cap

- GIVEN any DB state
- WHEN `GET /conflicts?limit=1000`
- THEN the server MUST clamp to 500 and return at most 500 rows (no 4xx)

#### Scenario: since filter restricts by created_at

- GIVEN 3 relations created before 2026-01-01T00:00:00Z and 2 after
- WHEN `GET /conflicts?since=2026-01-01T00:00:00Z`
- THEN response contains exactly the 2 newer relations

---

### Requirement: GET /conflicts/{relation_id}

The system MUST expose `GET /conflicts/{relation_id}` returning the full detail of one relation including source and target observation snippets. Returns 404 when relation_id does not exist.

#### Scenario: fetch existing relation

- GIVEN relation_id=42 exists
- WHEN `GET /conflicts/42`
- THEN response is 200 with `relation_id`, `relation_type`, `judgment_status`, `sync_id`, `source_snippet`, `target_snippet`

#### Scenario: fetch missing relation

- GIVEN relation_id=9999 does not exist
- WHEN `GET /conflicts/9999`
- THEN response is 404 with a JSON error body

---

### Requirement: GET /conflicts/stats

The system MUST expose `GET /conflicts/stats` returning aggregate counts. MUST support `project` query param. Response MUST include per-status counts and deferred/dead totals.

#### Scenario: stats for project

- GIVEN project "alpha" has known counts
- WHEN `GET /conflicts/stats?project=alpha`
- THEN response is 200 with JSON: `{pending, accepted, rejected, deferred, dead}` fields

#### Scenario: stats without project param returns global counts

- GIVEN relations exist across multiple projects
- WHEN `GET /conflicts/stats` (no project param)
- THEN response includes counts across all projects

---

### Requirement: POST /conflicts/scan

The system MUST expose `POST /conflicts/scan` with JSON body `{"project": "X", "since": "...", "apply": true|false, "max_insert": 100}`. The endpoint MUST run the scan synchronously and return counts of candidates found and rows inserted. When `apply=false` (or omitted), zero rows are inserted. When `apply=true` and the cap is reached, a `warning` field MUST be included in the response.

#### Scenario: dry-run scan via HTTP

- GIVEN 5 candidate pairs exist for project "alpha"
- WHEN `POST /conflicts/scan` with `{"project":"alpha","apply":false}`
- THEN response is 200 with `{"candidates_found":5,"inserted":0}`

#### Scenario: apply scan with cap reached

- GIVEN 150 candidate pairs exist and `max_insert=50` is specified
- WHEN `POST /conflicts/scan` with `{"project":"alpha","apply":true,"max_insert":50}`
- THEN response is 200 with `{"candidates_found":150,"inserted":50,"warning":"cap reached"}`

#### Scenario: missing project field

- GIVEN a request body with no `project` field
- WHEN `POST /conflicts/scan`
- THEN response is 400 with a JSON error body

---

### Requirement: GET /conflicts/deferred

The system MUST expose `GET /conflicts/deferred` returning a paginated list of `sync_apply_deferred` rows. MUST support `status` and `limit` (default 50, max 500) query params.

#### Scenario: list deferred rows

- GIVEN 10 deferred rows exist
- WHEN `GET /conflicts/deferred?limit=5`
- THEN response is 200 with 5 rows and a `total` field

#### Scenario: status filter

- GIVEN 3 rows with status='dead' and 7 with status='deferred'
- WHEN `GET /conflicts/deferred?status=dead`
- THEN response contains exactly 3 rows

---

### Requirement: POST /conflicts/deferred/replay

The system MUST expose `POST /conflicts/deferred/replay` that calls `ReplayDeferred()` synchronously and returns counts of retried, succeeded, and dead rows.

#### Scenario: replay with eligible rows

- GIVEN 4 deferred rows are eligible for retry
- WHEN `POST /conflicts/deferred/replay`
- THEN response is 200 with `{"retried":4,"succeeded":N,"dead":M}`

#### Scenario: replay when queue is empty

- GIVEN no deferred rows exist
- WHEN `POST /conflicts/deferred/replay`
- THEN response is 200 with `{"retried":0,"succeeded":0,"dead":0}`

---

## Domain: conflict-scan (store layer)

### Requirement: ListRelations and CountRelations store methods

The system MUST implement `ListRelations(opts)` and `CountRelations(opts)` on `*store.Store` in `internal/store/relations.go`. Project filtering MUST use a JOIN to the `observations` table at query time (no schema change). Both methods MUST use `idx_memrel_status_created` for efficient pagination.

#### Scenario: ListRelations filters by project via JOIN

- GIVEN observations for two projects and relations linking them
- WHEN `ListRelations({project: "alpha", status: "pending", limit: 10})` is called
- THEN only relations whose source OR target observation belongs to project "alpha" are returned

#### Scenario: CountRelations returns accurate total

- GIVEN 7 pending relations for project "alpha"
- WHEN `CountRelations({project: "alpha", status: "pending"})` is called
- THEN the return value is 7

---

### Requirement: ListDeferred and GetDeferred store methods

The system MUST implement `ListDeferred(status, limit)` and `GetDeferred(syncID)` on `*store.Store`. `GetDeferred` MUST return an error when the row does not exist.

#### Scenario: GetDeferred returns row

- GIVEN a deferred row with sync_id="xyz" exists
- WHEN `GetDeferred("xyz")` is called
- THEN a `DeferredRow` with the correct fields is returned

#### Scenario: GetDeferred returns not-found error

- GIVEN no row with sync_id="missing"
- WHEN `GetDeferred("missing")` is called
- THEN an error wrapping "not found" is returned

---

## Domain: index-migration

### Requirement: idx_memrel_status_created migration

The system MUST add `CREATE INDEX IF NOT EXISTS idx_memrel_status_created ON memory_relations(judgment_status, created_at DESC)` via the existing N→N+1 migration pattern. The migration MUST be idempotent and MUST NOT modify existing rows.

#### Scenario: migration applies on legacy DB

- GIVEN a DB without `idx_memrel_status_created`
- WHEN the store is opened and migrations run
- THEN the index exists and existing rows are unchanged

#### Scenario: migration is idempotent

- GIVEN a DB that already has `idx_memrel_status_created`
- WHEN migrations run again
- THEN no error is raised and no duplicate index is created

---

## Domain: phase2-fold-ins

### Requirement: Seq in FK miss log line

The system MUST include the mutation `Seq` in the `[store] ApplyPulledMutation: relation FK miss` log line. Format: `"relation FK miss seq=%d source=%s target=%s"`.

#### Scenario: FK miss log includes Seq

- GIVEN a pulled mutation with Seq=42 references a non-existent FK
- WHEN `applyRelationUpsertTx` encounters the miss
- THEN the log line contains `seq=42`

#### Scenario: FK miss log still fires on missing source observation

- GIVEN the source observation ID does not exist
- WHEN the mutation is applied
- THEN the existing "straight to dead" behavior is unchanged and the log line contains the Seq

---

### Requirement: TestApplyPulledRelation_MalformedPayload_StraightToDead

The system MUST have a test in `internal/store/sync_apply_test.go` that verifies a pulled relation mutation with a malformed JSON payload (or missing required fields) goes directly to `apply_status='dead'` without retries.

#### Scenario: malformed JSON goes straight to dead

- GIVEN a pulled relation mutation with payload `"not valid json"`
- WHEN `ApplyPulledMutation` is called
- THEN the row in `sync_apply_deferred` has `apply_status='dead'` and `retry_count=0`

#### Scenario: missing required field goes straight to dead

- GIVEN a pulled relation mutation with payload `{"relation_type":"conflicts"}` (missing source_id, target_id)
- WHEN `ApplyPulledMutation` is called
- THEN the row has `apply_status='dead'` and `retry_count=0`

---

### Requirement: multi-actor sync_id documentation

The system MUST include a section in `docs/PLUGINS.md` documenting that multiple agents can produce distinct relation rows for the same (source_id, target_id) pair, each with its own `sync_id`. Annotation parsers MUST be documented as potentially seeing duplicate `conflicts:` prefix lines.

#### Scenario: docs section exists

- GIVEN `docs/PLUGINS.md` is opened
- WHEN the multi-actor sync_id section is searched
- THEN a section describing the multi-actor namespace behavior and duplicate prefix implications is present

---

## Domain: backwards-compatibility

### Requirement: additive-only surface

All new CLI commands, HTTP endpoints, and store methods MUST be purely additive. No existing CLI commands, HTTP routes, MCP tool signatures, or database schema columns MUST change shape. Existing 19 packages MUST remain GREEN after Phase 3 lands.

#### Scenario: existing commands unaffected

- GIVEN Phase 3 is deployed
- WHEN `engram add`, `engram search`, `engram sync`, `engram stats` are run with existing arguments
- THEN behavior is identical to pre-Phase-3

#### Scenario: existing HTTP routes unaffected

- GIVEN Phase 3 is deployed
- WHEN `GET /sync/status`, `GET /sessions/recent`, and all pre-existing routes are called
- THEN responses are identical in shape to pre-Phase-3

#### Scenario: no existing MCP tool changes

- GIVEN Phase 3 is deployed
- WHEN all 17 MCP tools (mem_save, mem_search, etc.) are invoked
- THEN their signatures and behavior are unchanged

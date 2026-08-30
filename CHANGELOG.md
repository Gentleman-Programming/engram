# Changelog

All notable changes to Engram are documented here.

This project follows [Conventional Commits](https://www.conventionalcommits.org/) and uses [GoReleaser](https://goreleaser.com/) to auto-generate GitHub Release notes from commit history on each tag push.

## Where to Find Release Notes

Full release notes with changelogs per version live on the **[GitHub Releases page](https://github.com/Gentleman-Programming/engram/releases)**.

GoReleaser generates them automatically from commits, filtering by type:

- `feat:` / `fix:` / `refactor:` / `chore:` commits appear in the release notes
- `docs:` / `test:` / `ci:` commits are excluded from the generated changelog

## Breaking Changes

Breaking changes are always marked with a `type:breaking-change` label and documented in the release notes with a migration path. The `fix!:` and `feat!:` commit format triggers a major version bump.

## Unreleased

<!-- Changes that are merged but not yet released are tracked here until the next tag. -->

### Memory core

- **fix(project):** make project-scoped reads consistent across CLI and local HTTP. Omitted selectors now resolve the canonical current project; use `--all` or `all_projects=true` for an intentional global read. Search, timeline, stats, export, Obsidian export, conflict inspection, recent lists, review, prompts, and sync status now validate explicit selectors through the shared resolver. Sync status rejects `all_projects=true` because it has no aggregate provider. This is a compatibility change for callers that relied on omitted reads being global.
- **fix(project):** scan child repositories until EOF, the 200ms deadline, or the second repository. Large noisy directories can no longer auto-promote an early repository while a later repository makes the cwd ambiguous.
- **fix(store):** establish project ownership forward on legacy sessions instead of rejecting their writes. A database upgraded from the schema where `sessions.project` was nullable still holds sessions that identify no project; those sessions now adopt the project of the write landing on them, in the same transaction and journaled like any other ownership move, so the record and its session agree rather than the write failing permanently. Adoption is refused only when the unowned session already parents a record owned by a different project, which would split that record from its session. Both ownership errors answer `409` with a `code` and a `remedy` naming the exact repair.
- **feat(cli):** add `engram projects rescue-ownership --project <name> [--session <id>] [--observation <id>] [--prompt <id>]`. It performs the same ownership repair as `POST /projects/rescue-ownership` against the local store, so recovery no longer requires `ENGRAM_HTTP_TOKEN` to be configured.
- **fix(store):** resolve the whole rescue plan before mutating anything. The session pass previously claimed dependent parent sessions before any record's ownership was examined, so an unowned session parenting a record owned by another project was moved while the record was left behind — a split in the mirror direction of the case the rescue guards. Sessions and records are now decided together and applied afterwards.
- **fix(server):** report rescue outcomes unambiguously. The response carries `complete` and a `blocked` list naming every item left behind with its reason, and `status` is `partially_rescued` rather than `rescued` when anything was; a partial outcome is no longer indistinguishable from a clean one.
- **fix(store):** read `sessions.project` as nullable in `GetSession`, so a legacy NULL row no longer fails every caller that inspects the session with an opaque scan error.
- **fix(doctor):** read `sessions.project` through `ifnull()` in the diagnostic queries, so `engram doctor` no longer dies with `converting NULL to string is unsupported` on a database upgraded from the schema where the column was nullable. That population is precisely the one the `project_ownership_required` remedy sends to doctor, and it previously got no report at all.
- **fix(store):** read `sessions.project` through `ifnull()` at every remaining site that scanned it raw. An audit of the whole store found the same crash in `RecentSessions` and `AllSessions` (so a single legacy row denied the user `engram context`, `mem_context`, and the TUI session list), in `Export` (the way data leaves the store before a repair), and in `DeleteSession`, `EndSession`, and the `CreateSession` readback (so a legacy session could not be deleted, ended, or re-registered). `sessions.project` is the only column in the schema that is nullable in practice but declared `NOT NULL`: the table is only ever created with `CREATE TABLE IF NOT EXISTS`, so the declaration never reaches rows written before it, and no migration rewrites or backfills the column.
- **feat(doctor):** add the `unowned_session_project` check. It reports every session that identifies no project — NULL or blank — and carries the exact `engram projects rescue-ownership --project <name> --session <id>` repair, so the legacy ownership state doctor can now read is also surfaced instead of silently passing. The listing is unscoped by design: an unowned session belongs to no project, so `--project` cannot filter it away.
- **fix(pi):** surface background capture failures on stderr instead of discarding them, so a user whose passive memories stopped being saved gets a signal.
- **fix(store):** reject empty or whitespace-only observation titles consistently on create and update, before any side effect. `engram save` and `mem_save` now refuse a titleless write before opening the store or creating a session, `POST /observations` validates the title before the session lookup so a bad session or project can no longer mask the documented `400`, and `PATCH /observations/{id}` answers `400` rather than `404`. Persisting a titleless observation also enqueues a cloud upsert that the sync validators reject, which blocks every later mutation for the project.

### Cloud sync

- **fix(cloud):** make chunk and mutation push payload limits configurable with `ENGRAM_CLOUD_MAX_PUSH_BYTES` while preserving the 8 MiB default.

### Cloud user token management (`cloud-user-token-management`)

- **feat(cloud):** add principal/human-user/token/project-grant/audit storage foundation (`cloud_principals`, `cloud_human_users`, `cloud_principal_tokens`, `cloud_project_grants`, `cloud_auth_audit_log`) alongside existing sync tables.
- **feat(cloud):** enforce managed-principal project grants for sync chunk/mutation push and pull while preserving legacy `ENGRAM_CLOUD_ALLOWED_PROJECTS` behavior.
- **feat(cloud):** add managed admin API handlers (`/admin/users`, `/admin/users/{id}/tokens`, `/admin/users/{id}/grants`, and related enable/disable/revoke routes) and dashboard managed-user UX, with audit-backed mutations.
- **feat(cloud):** add dashboard managed-principal sessions and first-admin dashboard bootstrap (`/dashboard/bootstrap`), including audit coverage for admin login and legacy-recovery actions.
- **feat(cloud):** add `engram cloud bootstrap admin --username <name> [--email <email>] [--grant-project <project>]... [--issue-token [name]]` CLI command to create the first managed admin headlessly, with duplicate-bootstrap refusal and `bootstrap.cli` audit events.
- **feat(cloud):** add `ENGRAM_CLOUD_TOKEN_PEPPER` for dedicated managed-token hashing, distinct from `ENGRAM_JWT_SECRET`.
- **feat(cloud):** wire managed-token authentication into `engram cloud serve` — the runtime principal resolver now checks managed token storage first, then falls back to the legacy `ENGRAM_CLOUD_TOKEN`/`ENGRAM_CLOUD_ADMIN` credentials, on every `/sync/*`, `/admin/*`, and dashboard-login request. Requires `ENGRAM_CLOUD_TOKEN_PEPPER` to be set; without it, managed-token auth is disabled and the server starts in legacy-only mode exactly as before. See [DOCS.md — Managed users, tokens, and CLI bootstrap](DOCS.md#managed-users-tokens-and-cli-bootstrap).
- **fix(cloud):** dashboard login/bootstrap audit events no longer send the legacy admin/sync principal's synthetic ID (e.g. `legacy:admin`) as `ActorPrincipalID`, which the real Postgres-backed audit table rejects (a non-numeric value against a `BIGINT` foreign key); this previously would have made every legacy admin dashboard login fail with a 500 once an admin identity store was configured.

### Pi package (`pi-engram`)

- **fix(plugin):** allow `mem_session_summary` to accept an explicit `project` fallback when automatic project detection is unavailable.
- **fix(plugin):** fall back to local `.engram/config.json` and surface a clearer version-mismatch diagnostic when the running Engram server lacks `/project/current`.
- **feat(plugin):** add `gentle-engram` package for Pi marketplace installs, with HTTP event capture, Memory Protocol prompt injection, safe `engram mcp` launcher config, and `pi-engram init` setup helper.

### Cloud dashboard visual parity (`cloud-dashboard-visual-parity`)

New and updated routes registered in `internal/cloud/dashboard/dashboard.go`:

- **feat(dashboard):** add `/dashboard/projects/list` HTMX partial with paginated project list and "Paused" badge when sync is disabled
- **feat(dashboard):** add `/dashboard/projects/{name}/observations|sessions|prompts` HTMX partials for project detail tabs
- **feat(dashboard):** add `/dashboard/contributors/list` HTMX partial with paginated contributor list
- **feat(dashboard):** add `/dashboard/contributors/{contributor}` detail page showing recent sessions, observations, and prompts
- **feat(dashboard):** add `/dashboard/admin/users` and `/dashboard/admin/users/list` (admin-gated)
- **feat(dashboard):** add `/dashboard/admin/health` (admin-gated)
- **feat(dashboard):** add `POST /dashboard/admin/projects/{name}/sync` toggle for per-project sync pause (admin-gated; HTTP 409 on paused push)
- **feat(dashboard):** add `/dashboard/sessions/{project}/{sessionID}`, `/dashboard/observations/{project}/{sessionID}/{syncID}`, `/dashboard/prompts/{project}/{sessionID}/{syncID}` composite-ID detail pages
- **fix(dashboard):** removed dead route `/dashboard/admin/contributors`; user/contributor management consolidated under `/dashboard/admin/users`
- **feat(dashboard):** type pills on browser page sourced from `ListDistinctTypes` DB query
- **feat(dashboard):** principal display name bridged via `MountConfig.GetDisplayName`; falls back to `"OPERATOR"` when nil or empty
- **feat(dashboard):** detail page URL scheme uses `{syncID}` (not `{chunkID}`) as the tertiary path segment

### Cloud autosync restoration (`cloud-autosync-restoration`)

Background mutation-based replication for `engram serve` and `engram mcp`:

- **feat(autosync):** `internal/cloud/autosync.Manager` — lease-guarded background push/pull goroutine enabled by `ENGRAM_CLOUD_AUTOSYNC=1` + `ENGRAM_CLOUD_TOKEN` + `ENGRAM_CLOUD_SERVER`
- **feat(cloudserver):** add `POST /sync/mutations/push` (batch up to 100 mutations, configurable body cap defaulting to 8 MiB, per-project auth + pause gate returning HTTP 409 `sync-paused`)
- **feat(cloudserver):** add `GET /sync/mutations/pull?since_seq=N&limit=M` (server-side filtered by enrolled projects; fail-closed when `EnrolledProjectsProvider` not implemented)
- **feat(autosync):** phases: `idle`, `pushing`, `pulling`, `healthy`, `push_failed`, `pull_failed`, `backoff`, `disabled`
- **feat(autosync):** reason codes: `transport_failed`, `auth_required`, `policy_forbidden`, `server_unsupported`, `internal_error`, `sync-paused`
- **feat(autosync):** exponential backoff — base 1s, max 5min, ×2 per failure, ±25% jitter, ceiling at 10 consecutive failures
- **feat(autosync):** `StopForUpgrade` / `ResumeAfterUpgrade` for upgrade-window pause without releasing the sync lease
- **fix(autosync):** SIGTERM cancels context → `releaseLease()` deferred in `Run()` for graceful shutdown

### BREAKING CHANGE: MCP write tools no longer accept a `project` field

The `project` argument has been removed from the JSON schemas of 7 MCP write tools:
`mem_save`, `mem_save_prompt`, `mem_session_start`, `mem_session_end`, `mem_session_summary`, `mem_capture_passive`, `mem_update`.

**Before:** agents could pass `project: "my-project"` to write tools.
**After:** the project is auto-detected from the server's working directory (cwd). Any `project` argument sent by the LLM is silently discarded.

**Migration:**

- Remove `project` from write tool calls in your agent's memory protocol.
- Use `mem_current_project` (new tool) to inspect which project Engram will use before writing.
- If the cwd is ambiguous (multiple git repos), Engram returns a structured error with `available_projects`. Navigate to one of the repos before writing.
- Read tools (`mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation`, `mem_stats`) still accept an optional `project` override — validated against the store.

### New tool: `mem_current_project`

Returns detection result including `project`, `project_source`, `project_path`, `cwd`, `available_projects`, and `warning`. Never errors — returns success even when the cwd is ambiguous. Recommended as the first call when starting a session to confirm which project will receive writes.

- **feat(project):** add project name auto-detection via git remote and normalization (lowercase + trim + collapse) on all read/write paths
- **feat(cli):** add `engram projects list|consolidate|prune` commands for project hygiene
- **feat(mcp):** add `mem_merge_projects` tool for agent-driven project consolidation
- **feat(mcp):** auto-detect project at MCP startup via `--project` flag, `ENGRAM_PROJECT` env, or git remote
- **feat(mcp):** similar-project warnings when saving to a new project that resembles an existing one
- **fix(sync):** use git remote detection instead of `filepath.Base(cwd)` for project name

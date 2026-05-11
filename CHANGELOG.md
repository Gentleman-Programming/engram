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

### Pi package (`pi-engram`)

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
- **feat(cloudserver):** add `POST /sync/mutations/push` (batch up to 100 mutations, 8 MiB body cap, per-project auth + pause gate returning HTTP 409 `sync-paused`)
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

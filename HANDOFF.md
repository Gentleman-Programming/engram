# Maintainer Handoff

This document transfers the current local-fork context to another agent or
machine. It describes the state of this fork, not the upstream project's
release state.

## Authoritative checkout

Work from this Git repository only. The Codex/Claude plugin caches and Go
module cache are generated installation copies; do not edit them as a source
of truth.

- Fork remote: `https://github.com/Faturrachman-dev/engram.git`
- Upstream remote: `https://github.com/Gentleman-Programming/engram.git`
- Active branch: `feat/mem-update-project-reassign`
- Latest local-and-pushed commit: `465e81a feat(provenance): record author across agent integrations`

The previous `engram-src` duplicate worktree was intentionally removed. Do not
recreate or use it for development.

## What this branch adds

The branch contains the following local work on top of upstream:

1. `mem_update` supports a validated `project` argument, allowing one
   observation to be reassigned without raw SQLite edits.
2. `/stats` includes `total_created` and `max_observation_id`.
3. The HTTP server serves a dashboard at `/dashboard`, redirects `/` there,
   enables browser CORS, and exposes `GET /projects/stats` for project counts.
4. Observations support author provenance end-to-end:
   - `mem_save.author` is accepted by MCP.
   - When omitted, `ENGRAM_AUTHOR` is used.
   - SQLite, imports, API responses, and Obsidian export retain `author`.
   - The dashboard shows author information.
5. Pi integration sets authors to `pi/<model-id>` when the active model is
   available, otherwise it uses `ENGRAM_AUTHOR`.
6. `ENGRAM_AGENT_CLI=pi` is supported for semantic conflict scanning. The Pi
   runner shells out to the local `pi` CLI and uses that machine's configured
   provider/model.

## Architecture landmarks

- `cmd/engram/main.go` — CLI command wiring and environment help.
- `internal/mcp/mcp.go` — MCP schemas and tool handlers.
- `internal/store/store.go` — SQLite schema, migrations, and observation
  persistence.
- `internal/llm/` — semantic conflict-scan runners; `pi.go` is the local Pi
  runner.
- `internal/server/server.go` — HTTP routes, dashboard delivery, and CORS.
- `internal/server/dashboard/index.html` — embedded dashboard UI.
- `plugin/pi/index.ts` — Pi integration and memory tool requests.
- `plugin/codex/` and `plugin/claude-code/` — thin host-agent hooks. Keep
  behavior and persistence policy in the Go server where possible.

## Build and verification

Run focused verification from the repository root:

```powershell
go test ./internal/llm ./internal/mcp ./internal/store ./internal/server
```

Rebuild the local executable after changing Go code, then restart MCP clients
or their sessions. The installed binary is deliberately separate from the
source checkout; never modify its generated plugin-cache files as a substitute
for rebuilding from this repository.

## Working conventions

- Use Conventional Commits and keep the branch name in `type/description`
  format. The repository ruleset rejects invalid messages.
- Do not commit generated binaries, databases, credentials, or agent caches.
- Preserve author provenance as an agent/model label, never as a credential or
  captured request payload.
- Before changing a plugin hook, read `skills/plugin-thin/SKILL.md`; before
  changing persistence or project resolution, read
  `skills/business-rules/SKILL.md`.

## Follow-up work worth checking

- `DOCS.md` still describes `ENGRAM_AGENT_CLI` as accepting only `claude` and
  `opencode`; update it when preparing this branch for broader review.
- Add focused MCP/store tests for author migration and persistence if this
  branch will be proposed upstream.
- Rebuild and smoke-test the installed executable on each machine after pulling
  this branch; the executable itself is intentionally not committed.

[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Memory Core](memory-core.md) | [Next: Sync and Cloud →](sync-and-cloud.md)

# Interfaces

**Engram exposes one local memory core through several interfaces: CLI, MCP, local HTTP API, and TUI.** Keep interface code thin: parse input, call the right package, return a clear response.

## CLI: `cmd/engram`

`cmd/engram/main.go` and neighboring files are the binary entry point. They connect store, HTTP, MCP, TUI, sync, autosync, setup, doctor, conflicts, cloud, and Obsidian.

Do not put core behavior in the command if it can live in a testable package. The command should coordinate, parse flags, and adapt errors for humans.

## MCP: `internal/mcp`

`internal/mcp/mcp.go` exposes Engram to agents over stdio. It has tool profiles:

| Profile | Use |
|---|---|
| `all` | Default for `engram mcp`; registers all tools. |
| `agent` | Normal agent tools, such as save/search/context/session summary/current project/judge/doctor. |
| `admin` | Manual curation: stats, delete, timeline, merge. |

Important points:

- `mem_current_project` is the recommended first call to confirm detection.
- Normal writes should not pass `project` as an arbitrary override.
- `ambiguous_project` recovery requires the user to choose an exact project.
- If `mem_save` returns conflict candidates, the agent must judge with `mem_judge` or ask when the relationship is sensitive.

For tool parameters and envelopes, use [DOCS.md — MCP Tools](../../DOCS.md#mcp-tools-19-tools).

## Local API: `internal/server`

`internal/server/server.go` is a simple JSON API over the local store. It also exposes `GET /sync/status` for autosync/degraded-state visibility.

Use it for plugins, hooks, or local external clients. Do not confuse it with cloud: the cloud runtime has its own server.

For exact local routes, use [DOCS.md — HTTP API Endpoints](../../DOCS.md#http-api-endpoints).

## TUI: `internal/tui`

The TUI uses Bubbletea and reads from the local store. The separation is classic:

| File | Role |
|---|---|
| `internal/tui/model.go` | State, screens, initialization. |
| `internal/tui/update.go` | Input/transitions handling. |
| `internal/tui/view.go` | Screen rendering. |
| `internal/tui/styles.go` | Lipgloss styles. |

## Interface change checklists

### MCP tools

- [ ] The tool uses store as the source of truth.
- [ ] Project resolution respects `.engram/config.json`, cwd, and the `ambiguous_project` flow.
- [ ] The `agent`/`admin` profile remains intentional.
- [ ] Errors return useful envelopes for agents.
- [ ] Tests in `internal/mcp/*_test.go` cover the contract.
- [ ] `docs/AGENT-SETUP.md`, `docs/ARCHITECTURE.md`, or `DOCS.md` are updated if visible behavior changes.

### Local API

- [ ] The route belongs to `engram serve`, not cloud.
- [ ] `internal/server/server.go` only orchestrates request/response and calls store/services.
- [ ] Status codes and JSON errors are deterministic.
- [ ] Tests in `internal/server/*_test.go` cover errors and success.
- [ ] `DOCS.md#http-api-endpoints` is updated if there is a new/modified public endpoint.

---

[← Previous: Memory Core](memory-core.md) | [Next: Sync and Cloud →](sync-and-cloud.md)

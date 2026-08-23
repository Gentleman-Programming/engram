# Droid Setup Implementation Notes

## Overview

Implemented `engram setup droid` to integrate Engram with Factory's Droid CLI.
The installer registers the Engram MCP server, installs the Engram plugin via
Droid's marketplace translation, and writes a user-level `UserPromptSubmit` hook
to work around a Droid plugin-hook limitation.

## What `engram setup droid` does

1. **MCP registration** — writes `mcpServers.engram` to `~/.factory/mcp.json`
   using the absolute path to the `engram` binary.
2. **Hook script extraction** — copies embedded hook scripts to
   `~/.factory/hooks/engram/` so they live at a stable path.
3. **User-level UserPromptSubmit hook** — writes the hook to
   `~/.factory/hooks.json` in Droid's standalone format (event names as
   top-level keys).
4. **Plugin installation** — runs `droid plugin marketplace add` and
   `droid plugin install engram@engram --scope user` so Droid gets the
   `SessionStart`, `Stop`, `PreCompact`, `SubagentStop` hooks and the
   `engram-memory` skill.

## Key findings from validation

### Plugin translation works, but UserPromptSubmit plugin hooks do not fire

Droid translates the existing Claude Code plugin (`.claude-plugin/`) into a
native Droid plugin (`.factory-plugin/`) and loads it. Lifecycle hooks such as
`SessionStart`, `Stop`, `PreCompact`, and `SubagentStop` execute correctly.

However, `UserPromptSubmit` hooks declared **inside** a plugin are registered
and matched but never executed. This matches the known Claude Code issue
[anthropics/claude-code#10225](https://github.com/anthropics/claude-code/issues/10225).
The workaround is to declare the `UserPromptSubmit` hook at user scope in
`~/.factory/hooks.json`.

### Droid MCP tool naming

Droid exposes Engram MCP tools as `engram___<tool>` (server name + triple
underscore + tool name), not `mcp__engram__<tool>`. The first-message
`ToolSearch` instruction emitted by the user-level hook uses the correct Droid
pattern:

```text
select:engram___mem_save,engram___mem_search,engram___mem_context,...
```

### Multi-repo cwd handling

When Droid starts in a directory that contains multiple git repositories (e.g.
`/Users/aj/scratch`, which holds both `engram-droid` and `iqair-airvisual-pro`),
cwd-based project detection returns `ambiguous_project` and read tools fail
until the caller retries with an explicit `project=`.

The `UserPromptSubmit` hook scans immediate child git repos on the first
message of each session. If it finds more than one, it injects the candidate
list and a hard rule into the first-message system prompt:

```text
IMPORTANT — multi-repo cwd detected: [engram-droid, iqair-airvisual-pro].
When calling ANY engram read tool (mem_search, mem_context, ...), ALWAYS pass
project=<the repo matching the current user task> explicitly. Never omit the
project parameter from read tools — cwd auto-detection will fail with
ambiguous_project. Only use a project name from the list above.
```

This is a prompt-side fix: it eliminates the `ambiguous_project` round-trip by
telling the agent to always pass `project=` on read tools, while still allowing
the agent to pick the correct project for the user's task. If cwd is a single
repo or not a git parent, the first-message prompt is unchanged.

### `droid exec` vs interactive `droid`

`UserPromptSubmit` hooks fire in interactive Droid sessions. They do **not**
fire in `droid exec` sessions. In exec mode the agent still receives the Memory
Protocol from the `SessionStart` hook and sees Engram tools in the deferred
list, but it must choose to load them itself.

## Files added/changed

- `internal/setup/droid.go` — installer implementation
- `internal/setup/droid_test.go` — installer tests
- `internal/setup/plugins/droid/scripts/_helpers.sh` — shared hook helpers,
  including `list_child_projects()` for multi-repo cwd detection
- `internal/setup/plugins/droid/scripts/user-prompt-submit.sh` — first-message
  tool loader, save nudge, and multi-repo `project=` instruction injection
- `internal/setup/agents.go` — registry entry for `droid`
- `internal/setup/setup.go` — seam variables for testing
- `internal/setup/setup_test.go` — reset seams for Droid
- `internal/setup/registry_test.go` — include `droid` in expected agents
- `README.md` — add Droid to the setup table
- `docs/AGENT-SETUP.md` — Droid setup section

## Current user configuration (this machine)

- Binary: `/Users/aj/.local/bin/engram` (development build from this branch)
- MCP config: `~/.factory/mcp.json` → `mcpServers.engram`
- Hook scripts: `~/.factory/hooks/engram/`
- User hooks: `~/.factory/hooks.json` → `UserPromptSubmit`
- Plugin: `engram@engram` installed at user scope

## How to verify

1. Restart Droid (or the Droid daemon) so it reloads `~/.factory/hooks.json`.
2. Start an interactive Droid session in any project.
3. Check the session transcript for a `UserPromptSubmit` hook result.
4. Confirm the assistant calls `ToolSearch` with the Engram tools and then
   loads them.

## Testing

```bash
# Run only the Droid installer tests
go test ./internal/setup/ -run Droid -v

# Run the full setup package tests
go test ./internal/setup/

# Run the entire repository test suite
go test ./...
```

All tests pass.

## Fork

Changes are pushed to `main` on https://github.com/ahjota/engram.

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

### `droid exec` vs interactive `droid`

`UserPromptSubmit` hooks fire in interactive Droid sessions. They do **not**
fire in `droid exec` sessions. In exec mode the agent still receives the Memory
Protocol from the `SessionStart` hook and sees Engram tools in the deferred
list, but it must choose to load them itself.

## Files added/changed

- `internal/setup/droid.go` — installer implementation
- `internal/setup/droid_test.go` — installer tests
- `internal/setup/plugins/droid/scripts/_helpers.sh` — shared hook helpers
- `internal/setup/plugins/droid/scripts/user-prompt-submit.sh` — first-message
  tool loader and save nudge
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

# Claude Code Memory Sync — Feature Documentation

## Summary

This feature adds **lazy bidirectional sync** between Engram and Claude Code's native memory folder (`~/.claude/projects/{slug}/memory/`).

## Problem It Solves

Users who work with both Claude Code (native) and other AI agents (OpenCode, VS Code, etc.) have **two disconnected memory systems**:

- **Claude Code native**: Stores memories as `.md` files in `~/.claude/projects/{slug}/memory/`
- **Engram**: Stores memories in `~/.engram/engram.db`

Previously, memories saved in one system were invisible to the other.

## Solution: Lazy Import

Instead of auto-syncing on startup (which goes against Engram's philosophy of "no auto-capture"), we use **lazy import**:

1. User calls `mem_search` to look for a memory
2. Before searching, a background goroutine imports any new Claude Code memories
3. Search returns unified results from both sources

```go
// In handleSearch (internal/mcp/mcp.go)
go func() {
    syncer := claudecode.NewSyncer(s, claudecode.SyncConfig{
        ClaudeProjectsDir: claudeProjectsDir,
        Project:          project,
    })
    result, err := syncer.ImportOnly()
    if err != nil {
        log.Printf("[engram] lazy claude-code import: %v", err)
        return
    }
    if result.Imported > 0 {
        log.Printf("[engram] lazy import: %d memories from Claude Code", result.Imported)
    }
}()
```

## Benefits

- **Consistent with Engram philosophy**: No auto-capture. Import only happens when user explicitly requests a search.
- **No startup cost**: Import only runs when memories are actually needed.
- **No background daemon**: Uses goroutine in existing request flow.
- **Transparent to user**: Search results automatically include memories from both sources.

## CLI Commands Added

| Command | Description |
|---------|-------------|
| `engram claude-code-export` | Export Engram memories → Claude Code memory folder |
| `engram claude-code-import` | Import Claude Code memories → Engram |
| `engram claude-code-sync` | Bidirectional sync (export + import) |

All commands support `--dry-run` for preview mode.

## Claude Code Memory File Format

Claude Code stores memories as markdown files with YAML frontmatter:

```markdown
---
name: BuilderBot QR endpoint — getQrImage() no existe
description: Bug crítico: el endpoint /v1/qr falla porque...
type: project
originSessionId: f80df214-5fd3-402c-bda8-417e5655b543
project: AnitaChatBot-DrJorgeHara
---
## Bug

`adapterProvider.getQrImage()` no existe en `@builderbot/provider-baileys`...
```

## Test Coverage

Tests verify:

1. **Import works**: Claude Code `.md` files are correctly parsed and stored in Engram
2. **Idempotent**: Re-importing doesn't create duplicates
3. **Multiple projects**: Memories from different Claude Code projects are imported separately
4. **Metadata preserved**: Type, session_id, project, and topic_key from frontmatter are preserved
5. **MEMORY.md skipped**: The index file is not imported as a memory
6. **Graceful handling**: Malformed files don't crash the import

```bash
# Run tests
go test ./internal/claudecode/... -v
go test ./internal/mcp/... -run "TestClaudeCode" -v
```

## Files Changed

- `internal/claudecode/` — New package for Claude Code sync
  - `claudecode.go` — Package docs
  - `markdown.go` — Memory file format generation
  - `exporter.go` — Engram → Claude Code export
  - `importer.go` — Claude Code → Engram import
  - `sync.go` — Bidirectional sync orchestration
  - `claudecode_test.go` — Unit tests
- `internal/mcp/mcp.go` — Added lazy import in `handleSearch`
- `cmd/engram/main.go` — Added CLI commands

## How It Works (Technical)

### Import Flow

1. `handleSearch` is called via MCP
2. A goroutine spawns `syncer.ImportOnly()`
3. `Importer` scans `~/.claude/projects/*/memory/` directories
4. For each `.md` file (except `MEMORY.md`):
   - Parse frontmatter (name, type, description, originSessionId, project, topic_key)
   - Extract content (everything after the `## Title` H2)
   - Create session if needed
   - Add observation to store
5. Original search proceeds with now-up-to-date store

### Export Flow

1. `claude-code-export` CLI command is called
2. `Exporter` reads all observations from Engram store
3. For each observation:
   - Generate Claude Code memory format (frontmatter + content)
   - Write to `~/.claude/projects/{slug}/memory/project_{slug}.md`
4. Update `MEMORY.md` index

### Memory File Naming

Claude Code uses URL-style slugs for project names:
- `C--Users-JorgeHaraDevs-Desktop-AnitaChatBot-DrJorgeHara`
- `C--Users-JorgeHaraDevs-Desktop-CitaMedicaBeta`

Our importer correctly parses these slugs and maps them to Engram project names.

## Example Usage

```bash
# Manual import (before using search)
engram claude-code-import

# Dry-run to see what would be imported
engram claude-code-import --dry-run

# Export Engram memories to Claude Code folder
engram claude-code-export

# Full bidirectional sync
engram claude-code-sync
```

## Alignment with Engram Philosophy

This implementation follows Engram's core principles:

- **"No auto-capture"**: Import only happens when user explicitly calls `mem_search`
- **Agent-driven**: The agent decides when to search, triggering the import
- **Lazy loading**: Import happens on-demand, not at startup
- **Transparent**: User doesn't need to know about the sync — it just works

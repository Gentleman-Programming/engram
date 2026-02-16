<p align="center">
  <h1 align="center">engram</h1>
  <p align="center"><strong>Persistent memory for AI coding agents</strong></p>
  <p align="center">
    <em>Agent-agnostic. Single binary. Zero dependencies.</em>
  </p>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="#how-it-works">How It Works</a> &bull;
  <a href="#why-not-claude-mem">Why Not claude-mem?</a> &bull;
  <a href="#tui">Terminal UI</a> &bull;
  <a href="#mcp-tools">MCP Tools</a> &bull;
  <a href="DOCS.md">Full Docs</a>
</p>

---

> **engram** `/ˈen.ɡræm/` — *neuroscience*: the physical trace of a memory in the brain.

Your AI coding agent forgets everything when the session ends. Engram gives it a brain.

A **Go binary** with SQLite + FTS5 full-text search, exposed via CLI, HTTP API, MCP server, and an interactive TUI. Works with **any agent** that supports MCP — OpenCode, Claude Code, Cursor, Windsurf, or anything else.

```
Agent (OpenCode / Claude Code / Cursor / Windsurf / ...)
    ↓ MCP stdio or HTTP
Engram (single Go binary)
    ↓
SQLite + FTS5 (~/.engram/engram.db)
```

## Quick Start

```bash
# Install from source
git clone https://github.com/alanbuscaglia/engram.git
cd engram
go install ./cmd/engram

# Add to any MCP-compatible agent
# (add this to your agent's MCP config)
{
  "engram": {
    "type": "stdio",
    "command": "engram",
    "args": ["mcp"]
  }
}

# Or start the HTTP server for plugin integrations
engram serve

# Browse your memories interactively
engram tui
```

That's it. No Node.js, no Python, no Bun, no Docker, no ChromaDB, no vector database, no worker processes, no web server to keep running. **One binary, one SQLite file.**

## How It Works

Engram trusts the **agent** to decide what's worth remembering — not a firehose of raw tool calls.

### The Agent Saves, Engram Stores

```
1. Agent completes significant work (bugfix, architecture decision, etc.)
2. Agent calls mem_save with a structured summary:
   - title: "Fixed N+1 query in user list"
   - type: "bugfix"
   - content: What/Why/Where/Learned format
3. Engram persists to SQLite with FTS5 indexing
4. Next session: agent searches memory, gets relevant context
```

### Session Lifecycle

```
Session starts → Agent works → Agent saves memories proactively
                                    ↓
Session ends → Agent writes session summary (Goal/Discoveries/Accomplished/Files)
                                    ↓
Next session starts → Previous session context is injected automatically
```

### 10 MCP Tools

| Tool | Purpose |
|------|---------|
| `mem_save` | Save a structured observation (decision, bugfix, pattern, etc.) |
| `mem_search` | Full-text search across all memories |
| `mem_session_summary` | Save end-of-session summary |
| `mem_context` | Get recent context from previous sessions |
| `mem_timeline` | Chronological context around a specific observation |
| `mem_get_observation` | Get full content of a specific memory |
| `mem_save_prompt` | Save a user prompt for future context |
| `mem_stats` | Memory system statistics |
| `mem_session_start` | Register a session start |
| `mem_session_end` | Mark a session as completed |

### Progressive Disclosure (3-Layer Pattern)

Token-efficient memory retrieval — don't dump everything, drill in:

```
1. mem_search "auth middleware"     → compact results with IDs (~100 tokens each)
2. mem_timeline observation_id=42  → what happened before/after in that session
3. mem_get_observation id=42       → full untruncated content
```

## Why Not claude-mem?

[claude-mem](https://github.com/thedotmack/claude-mem) is a great project (28K+ stars!) that inspired Engram. But we made fundamentally different design decisions:

| | **Engram** | **claude-mem** |
|---|---|---|
| **Language** | Go (single binary, zero runtime deps) | TypeScript + Python (needs Node.js, Bun, uv) |
| **Agent lock-in** | None. Works with any MCP agent | Claude Code only (uses Claude plugin hooks) |
| **Search** | SQLite FTS5 (built-in, zero setup) | ChromaDB vector database (separate process) |
| **What gets stored** | Agent-curated summaries only | Raw tool calls + AI compression |
| **Compression** | Agent does it inline (it already has the LLM) | Separate Claude API calls via agent-sdk |
| **Dependencies** | `go install` and done | Node.js 18+, Bun, uv, Python, ChromaDB |
| **Processes** | One binary (or none — MCP stdio) | Worker service on port 37777 + ChromaDB |
| **Database** | Single `~/.engram/engram.db` file | SQLite + ChromaDB (two storage systems) |
| **Web UI** | Terminal TUI (`engram tui`) | Web viewer on localhost:37777 |
| **Privacy** | `<private>` tags stripped at 2 layers | `<private>` tags stripped |
| **Auto-capture** | No. Agent decides what matters | Yes. Captures all tool calls then compresses |
| **License** | MIT | AGPL-3.0 |

### The Core Philosophy Difference

**claude-mem** captures *everything* and then compresses it with AI. This means:

- Extra API calls for compression (costs money, adds latency)
- Raw tool calls pollute search results until compressed
- Requires a worker process, ChromaDB, and multiple runtimes
- Locked to Claude Code's plugin system

**Engram** lets the agent decide what's worth remembering. The agent already has the LLM, the context, and understands what just happened. Why run a separate compression pipeline?

- `mem_save` after a bugfix: *"Fixed N+1 query — added eager loading in UserList"*
- `mem_session_summary` at session end: structured Goal/Discoveries/Accomplished/Files
- No noise, no compression step, no extra API calls
- Works with ANY agent via standard MCP

**The result**: cleaner data, faster search, no infrastructure overhead, agent-agnostic.

## TUI

Interactive terminal UI for browsing your memory. Built with [Bubbletea](https://github.com/charmbracelet/bubbletea).

```bash
engram tui
```

**Screens**: Dashboard, Search, Recent Observations, Observation Detail, Timeline, Sessions, Session Detail

**Navigation**: `j/k` vim keys, `Enter` to drill in, `t` for timeline, `/` to search, `Esc` to go back

**Features**:
- Catppuccin Mocha color palette
- `(active)` badges on live sessions, sorted to the top
- Scroll indicators for long lists
- Full FTS5 search from the TUI

## CLI

```
engram serve [port]       Start HTTP API server (default: 7437)
engram mcp                Start MCP server (stdio transport)
engram tui                Launch interactive terminal UI
engram search <query>     Search memories
engram save <title> <msg> Save a memory
engram timeline <obs_id>  Chronological context around an observation
engram context [project]  Recent context from previous sessions
engram stats              Memory statistics
engram export [file]      Export all memories to JSON
engram import <file>      Import memories from JSON
```

## OpenCode Plugin

For [OpenCode](https://opencode.ai) users, a thin TypeScript plugin handles session management and context injection:

```bash
# Copy the plugin
cp plugin/opencode/engram.ts ~/.config/opencode/plugins/

# Install plugin dependency
cd ~/.config/opencode && npm install @opencode-ai/plugin
```

The plugin:
- Auto-starts the engram server if not running
- Creates sessions on-demand (resilient to restarts/reconnects)
- Captures user prompts
- Injects `MEMORY_INSTRUCTIONS` + previous session context during compaction
- Strips `<private>` tags before sending data

**No raw tool call recording** — the agent handles all memory via `mem_save` and `mem_session_summary`.

## Privacy

Wrap sensitive content in `<private>` tags — it gets stripped at TWO levels:

```
Set up API with <private>sk-abc123</private> key
→ Set up API with [REDACTED] key
```

1. **Plugin layer** — stripped before data leaves the process
2. **Store layer** — `stripPrivateTags()` in Go before any DB write

## Project Structure

```
engram/
├── cmd/engram/main.go              # CLI entrypoint
├── internal/
│   ├── store/store.go              # Core: SQLite + FTS5 + all data ops
│   ├── server/server.go            # HTTP REST API (port 7437)
│   ├── mcp/mcp.go                  # MCP stdio server (10 tools)
│   └── tui/                        # Bubbletea terminal UI
│       ├── model.go                # Screen constants, Model, Init()
│       ├── styles.go               # Lipgloss styles (Catppuccin Mocha)
│       ├── update.go               # Input handling, per-screen handlers
│       └── view.go                 # Rendering, per-screen views
├── plugin/
│   └── opencode/engram.ts          # OpenCode adapter plugin
├── DOCS.md                         # Full technical documentation
├── go.mod
└── go.sum
```

## Requirements

- **Go 1.21+** to build
- That's it. No runtime dependencies.

The binary includes SQLite (via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no CGO).

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ENGRAM_DATA_DIR` | Data directory | `~/.engram` |
| `ENGRAM_PORT` | HTTP server port | `7437` |

## License

MIT

---

**Inspired by [claude-mem](https://github.com/thedotmack/claude-mem)** — but agent-agnostic, simpler, and built different.

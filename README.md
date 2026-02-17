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
  <a href="#agent-setup">Agent Setup</a> &bull;
  <a href="#why-not-claude-mem">Why Not claude-mem?</a> &bull;
  <a href="#tui">Terminal UI</a> &bull;
  <a href="DOCS.md">Full Docs</a>
</p>

---

> **engram** `/ˈen.ɡræm/` — *neuroscience*: the physical trace of a memory in the brain.

Your AI coding agent forgets everything when the session ends. Engram gives it a brain.

A **Go binary** with SQLite + FTS5 full-text search, exposed via CLI, HTTP API, MCP server, and an interactive TUI. Works with **any agent** that supports MCP — OpenCode, Claude Code, Cursor, Windsurf, or anything else.

```
Agent (OpenCode / Claude Code / Cursor / Windsurf / ...)
    ↓ MCP stdio
Engram (single Go binary)
    ↓
SQLite + FTS5 (~/.engram/engram.db)
```

## Quick Start

### Install via Homebrew (recommended)

```bash
brew install gentleman-programming/tap/engram
```

### Install from source

```bash
git clone https://github.com/Gentleman-Programming/engram.git
cd engram
go install ./cmd/engram
```

### Download binary

Grab the latest release for your platform from [GitHub Releases](https://github.com/Gentleman-Programming/engram/releases).

Then set up your agent's plugin:

```bash
engram setup
```

This will ask which agent you use (OpenCode or Claude Code) and install the plugin automatically. Or specify directly: `engram setup opencode` / `engram setup claude-code`.

See [Agent Setup](#agent-setup) for manual configuration or other agents (Cursor, Windsurf, Gemini).

That's it. No Node.js, no Python, no Bun, no Docker, no ChromaDB, no vector database, no worker processes. **One binary, one SQLite file.**

## How It Works

<p align="center">
  <img src="assets/agent-save.png" alt="Agent saving a memory via mem_save" width="800" />
  <br />
  <em>The agent proactively calls <code>mem_save</code> after significant work — structured, searchable, no noise.</em>
</p>

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

## Agent Setup

Engram works with **any MCP-compatible agent**. Add it to your agent's MCP config:

### OpenCode

Add to your `opencode.json` (global: `~/.config/opencode/opencode.json` or project-level):

```json
{
  "mcp": {
    "engram": {
      "type": "local",
      "command": ["engram", "mcp"],
      "enabled": true
    }
  }
}
```

**Optional: OpenCode plugin** for enhanced session management (auto-session tracking, compaction memory persistence, system prompt injection):

```bash
engram setup opencode
# or manually: cp plugin/opencode/engram.ts ~/.config/opencode/plugins/
```

The plugin is auto-loaded from `~/.config/opencode/plugins/` — no config changes needed. It also needs the HTTP server running for session tracking:

```bash
engram serve &
```

See [OpenCode Plugin](#opencode-plugin) for details.

### Claude Code

**Option A: Plugin (recommended)** — full session management, auto-import, compaction recovery, and Memory Protocol skill:

```bash
claude plugin add ./plugin/claude-code
```

For local development/testing:

```bash
claude --plugin-dir ./plugin/claude-code
```

The plugin bundles the MCP server, hooks, and a Memory Protocol skill — no additional config needed. The HTTP server must be running for session tracking:

```bash
engram serve &
```

See [Claude Code Plugin](#claude-code-plugin) for details on what the plugin provides.

**Option B: Bare MCP** — just the 10 memory tools, no session management:

Add to your `.claude/settings.json` (project) or `~/.claude/settings.json` (global):

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

With bare MCP, add a [Surviving Compaction](#surviving-compaction-recommended) prompt to your `CLAUDE.md` so the agent remembers to use Engram after context resets.

### Gemini CLI

Add to your `~/.gemini/settings.json` (global) or `.gemini/settings.json` (project):

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

Or via the CLI:

```bash
gemini mcp add engram engram mcp
```

### Cursor

Add to your `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

### Windsurf

Add to your `~/.windsurf/mcp.json`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

### Any other MCP agent

The pattern is always the same — point your agent's MCP config to `engram mcp` via stdio transport.

### Surviving Compaction (Recommended)

When your agent compacts (summarizes long conversations to free context), it starts fresh — and might forget about Engram. To make memory truly resilient, add this to your agent's system prompt or config file:

**For Claude Code** (`CLAUDE.md`):
```markdown
## Memory
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
- Save proactively after significant work — don't wait to be asked.
- After any compaction or context reset, call `mem_context` to recover session state before continuing.
```

**For OpenCode** (agent prompt in `opencode.json`):
```
After any compaction or context reset, call mem_context to recover session state before continuing.
Save memories proactively with mem_save after significant work.
```

**For Gemini CLI** (`GEMINI.md`):
```markdown
## Memory
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
- Save proactively after significant work — don't wait to be asked.
- After any compaction or context reset, call `mem_context` to recover session state before continuing.
```

**For Cursor/Windsurf** (`.cursorrules` or `.windsurfrules`):
```
You have access to Engram persistent memory (mem_save, mem_search, mem_context).
Save proactively after significant work. After context resets, call mem_context to recover state.
```

This is the **nuclear option** — system prompts survive everything, including compaction.

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
<p align="center">
<img src="assets/tui-dashboard.png" alt="TUI Dashboard" width="400" />
  <img width="400" alt="image" src="https://github.com/user-attachments/assets/0308991a-58bb-4ad8-9aa2-201c059f8b64" />
  <img src="assets/tui-detail.png" alt="TUI Observation Detail" width="400" />
  <img src="assets/tui-search.png" alt="TUI Search Results" width="400" />
</p>

**Screens**: Dashboard, Search, Recent Observations, Observation Detail, Timeline, Sessions, Session Detail

**Navigation**: `j/k` vim keys, `Enter` to drill in, `t` for timeline, `/` to search, `Esc` to go back

**Features**:
- Catppuccin Mocha color palette
- Scroll indicators for long lists
- Full FTS5 search from the TUI
- Live data refresh on back-navigation

## Git Sync

Share memories across machines and team members by committing them to your repo. Uses compressed chunks with a manifest index — no merge conflicts, no huge files.

```bash
# Export new memories as a compressed chunk
# (automatically filters by current directory name as project)
engram sync

# Commit to git
git add .engram/ && git commit -m "sync engram memories"

# On another machine / clone: import new chunks
engram sync --import

# Check sync status
engram sync --status

# Override project detection if needed
engram sync --project other-name
```

**How it works:**

```
.engram/
├── manifest.json          ← small index (git diffs this)
├── chunks/
│   ├── a3f8c1d2.jsonl.gz ← chunk by Alan (compressed, ~2KB)
│   ├── b7d2e4f1.jsonl.gz ← chunk by Juan
│   └── c9f1a2b3.jsonl.gz ← chunk by Alan (next day)
└── engram.db              ← gitignored (local working DB)
```

- Each `engram sync` creates a **new chunk** — never modifies old ones
- Chunks are **gzipped JSONL** — small files, git treats as binary (no diff noise)
- The **manifest** is the only file git diffs — it's small and append-only
- Each chunk has a **content hash ID** — imported only once, no duplicates
- **No merge conflicts** on data — each dev creates independent chunks

**Auto-import**: The OpenCode plugin automatically runs `engram sync --import` when it detects `.engram/manifest.json` in the project directory. Clone a repo, open OpenCode, and the team's memories are loaded.

## CLI

```
engram setup [agent]      Install agent plugin (interactive or: engram setup opencode)
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
engram sync               Export new memories as compressed chunk to .engram/
engram version            Show version
```

## OpenCode Plugin

For [OpenCode](https://opencode.ai) users, a thin TypeScript plugin adds enhanced session management on top of the MCP tools:

```bash
# Install via engram (recommended — works from Homebrew or binary install)
engram setup opencode

# Or manually: cp plugin/opencode/engram.ts ~/.config/opencode/plugins/
```

The plugin auto-starts the HTTP server if it's not already running — no manual `engram serve` needed.

The plugin:
- **Auto-starts** the engram server if not running
- **Auto-imports** git-synced memories from `.engram/manifest.json` if present in the project
- **Creates sessions** on-demand via `ensureSession()` (resilient to restarts/reconnects)
- **Injects the Memory Protocol** into the agent's system prompt via `chat.system.transform` — strict rules for when to save, when to search, and a mandatory session close protocol
- **Injects previous session context** into the compaction prompt
- **Instructs the compressor** to tell the new agent to persist the compacted summary via `mem_session_summary`
- **Strips `<private>` tags** before sending data

**No raw tool call recording** — the agent handles all memory via `mem_save` and `mem_session_summary`.

### Memory Protocol (injected via system prompt)

The plugin injects a strict protocol into every agent message:

- **WHEN TO SAVE**: Mandatory after bugfixes, decisions, discoveries, config changes, patterns, preferences
- **WHEN TO SEARCH**: Reactive (user says "remember"/"recordar") + proactive (starting work that might overlap past sessions)
- **SESSION CLOSE**: Mandatory `mem_session_summary` before ending — "This is NOT optional. If you skip this, the next session starts blind."
- **AFTER COMPACTION**: Immediately call `mem_context` to recover state

### Three Layers of Memory Resilience

The OpenCode plugin uses a defense-in-depth strategy to ensure memories survive compaction:

| Layer | Mechanism | Survives Compaction? |
|-------|-----------|---------------------|
| **System Prompt** | `MEMORY_INSTRUCTIONS` injected via `chat.system.transform` | Always present |
| **Compaction Hook** | Auto-saves checkpoint + injects context + reminds compressor | Fires during compaction |
| **Agent Config** | "After compaction, call `mem_context`" in agent prompt | Always present |

## Claude Code Plugin

For [Claude Code](https://docs.anthropic.com/en/docs/claude-code) users, a plugin adds enhanced session management using Claude's native hook and skill system:

```bash
# Install via engram (recommended — works from Homebrew or binary install)
engram setup claude-code

# Or from the repo: claude plugin add ./plugin/claude-code
# Or for local development/testing: claude --plugin-dir ./plugin/claude-code
```

The plugin requires the HTTP server running for session tracking:

```bash
engram serve &
```

### What the Plugin Provides (vs bare MCP)

| Feature | Bare MCP | Plugin |
|---------|----------|--------|
| 10 memory tools | ✓ | ✓ |
| Session tracking (auto-start) | ✗ | ✓ |
| Auto-import git-synced memories | ✗ | ✓ |
| Compaction recovery | ✗ | ✓ |
| Memory Protocol skill | ✗ | ✓ |
| Previous session context injection | ✗ | ✓ |

### Plugin Structure

```
plugin/claude-code/
├── .claude-plugin/plugin.json     # Plugin manifest
├── .mcp.json                      # Registers engram MCP server
├── hooks/hooks.json               # SessionStart + Stop lifecycle hooks
├── scripts/
│   ├── session-start.sh           # Ensures server, creates session, imports chunks, injects context
│   ├── post-compaction.sh         # Injects previous context + recovery instructions
│   └── session-stop.sh            # Placeholder for future heartbeat
└── skills/memory/SKILL.md         # Memory Protocol (when to save, search, close, recover)
```

### How It Works

**On session start** (`startup`):
1. Ensures the engram HTTP server is running
2. Creates a new session via the API
3. Auto-imports git-synced chunks from `.engram/manifest.json` (if present)
4. Injects previous session context into Claude's initial context

**On compaction** (`compact`):
1. Injects the previous session context + compacted summary
2. Tells the agent: "FIRST ACTION REQUIRED — call `mem_session_summary` with this content before doing anything else"
3. This ensures no work is lost when context is compressed

**Memory Protocol skill** (always available):
- Strict rules for **when to save** (mandatory after bugfixes, decisions, discoveries)
- **When to search** memory (reactive + proactive)
- **Session close protocol** — mandatory `mem_session_summary` before ending
- **After compaction** — 3-step recovery: persist summary → load context → continue

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
│   ├── setup/setup.go              # Agent plugin installer (go:embed)
│   ├── sync/sync.go                # Git sync: manifest + compressed chunks
│   └── tui/                        # Bubbletea terminal UI
│       ├── model.go                # Screen constants, Model, Init()
│       ├── styles.go               # Lipgloss styles (Catppuccin Mocha)
│       ├── update.go               # Input handling, per-screen handlers
│       └── view.go                 # Rendering, per-screen views
├── plugin/
│   ├── opencode/engram.ts          # OpenCode adapter plugin
│   └── claude-code/                # Claude Code plugin (hooks + skill)
│       ├── .claude-plugin/plugin.json
│       ├── .mcp.json
│       ├── hooks/hooks.json
│       ├── scripts/                # session-start, post-compaction, session-stop
│       └── skills/memory/SKILL.md
├── assets/                         # Screenshots and media
├── DOCS.md                         # Full technical documentation
├── go.mod
└── go.sum
```

## Requirements

- **Go 1.25+** to build from source (not needed if installing via Homebrew or downloading a binary)
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

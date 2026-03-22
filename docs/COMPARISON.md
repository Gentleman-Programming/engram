[← Back to README](../README.md)

# Why Not claude-mem?

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

## The Core Philosophy Difference

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

---

**Inspired by [claude-mem](https://github.com/thedotmack/claude-mem)** — but agent-agnostic, simpler, and built different.

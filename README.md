<p align="center">
  <img width="1024" height="340" alt="image" src="https://github.com/user-attachments/assets/32ed8985-841d-49c3-81f7-2aabc7c7c564" />
</p>

<p align="center">
  <strong>Persistent memory for AI coding agents</strong><br>
  <em>Agent-agnostic. Single binary. Zero dependencies.</em>
</p>

<p align="center">
  <a href="docs/INSTALLATION.md">Installation</a> &bull;
  <a href="docs/AGENT-SETUP.md">Agent Setup</a> &bull;
  <a href="docs/ARCHITECTURE.md">Architecture</a> &bull;
  <a href="docs/PLUGINS.md">Plugins</a> &bull;
  <a href="CONTRIBUTING.md">Contributing</a> &bull;
  <a href="DOCS.md">Full Docs</a>
</p>

---

> **engram** `/ˈen.ɡræm/` — *neuroscience*: the physical trace of a memory in the brain.

Your AI coding agent forgets everything when the session ends. Engram gives it a brain.

A **Go binary** with SQLite + FTS5 full-text search, exposed via CLI, HTTP API, MCP server, and an interactive TUI. Works with **any agent** that supports MCP — Claude Code, OpenCode, Gemini CLI, Codex, VS Code (Copilot), Antigravity, Cursor, Windsurf, or anything else.

```
Agent (Claude Code / OpenCode / Gemini CLI / Codex / VS Code / Antigravity / ...)
    ↓ MCP stdio
Engram (single Go binary)
    ↓
SQLite + FTS5 (~/.engram/engram.db)
```

## Quick Start

### Install

```bash
brew install gentleman-programming/tap/engram
```

Windows, Linux, and other install methods → [docs/INSTALLATION.md](docs/INSTALLATION.md)

### Setup Your Agent

| Agent | One-liner |
|-------|-----------|
| Claude Code | `claude plugin marketplace add Gentleman-Programming/engram && claude plugin install engram` |
| OpenCode | `engram setup opencode` |
| Gemini CLI | `engram setup gemini-cli` |
| Codex | `engram setup codex` |
| VS Code | `code --add-mcp '{"name":"engram","command":"engram","args":["mcp"]}'` |
| Cursor / Windsurf / Any MCP | See [docs/AGENT-SETUP.md](docs/AGENT-SETUP.md) |

Full per-agent config, Memory Protocol, and compaction survival → [docs/AGENT-SETUP.md](docs/AGENT-SETUP.md)

That's it. No Node.js, no Python, no Docker. **One binary, one SQLite file.**

## How It Works

```
1. Agent completes significant work (bugfix, architecture decision, etc.)
2. Agent calls mem_save → title, type, What/Why/Where/Learned
3. Engram persists to SQLite with FTS5 indexing
4. Next session: agent searches memory, gets relevant context
```

Full details on session lifecycle, topic keys, and memory hygiene → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## MCP Tools (15)

| Category | Tools |
|----------|-------|
| **Save & Update** | `mem_save`, `mem_update`, `mem_delete`, `mem_suggest_topic_key` |
| **Search & Retrieve** | `mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation` |
| **Session Lifecycle** | `mem_session_start`, `mem_session_end`, `mem_session_summary` |
| **Utilities** | `mem_save_prompt`, `mem_stats`, `mem_capture_passive`, `mem_merge_projects` |

Full tool reference with parameters → [DOCS.md#mcp-tools-15-tools](DOCS.md#mcp-tools-15-tools)

## Terminal UI

```bash
engram tui
```

<p align="center">
<img src="assets/tui-dashboard.png" alt="TUI Dashboard" width="400" />
  <img width="400" alt="image" src="https://github.com/user-attachments/assets/0308991a-58bb-4ad8-9aa2-201c059f8b64" />
  <img src="assets/tui-detail.png" alt="TUI Observation Detail" width="400" />
  <img src="assets/tui-search.png" alt="TUI Search Results" width="400" />
</p>

**Navigation**: `j/k` vim keys, `Enter` to drill in, `/` to search, `Esc` back. Catppuccin Mocha theme.

## Git Sync

Share memories across machines. Uses compressed chunks — no merge conflicts, no huge files.

```bash
engram sync                    # Export new memories as compressed chunk
git add .engram/ && git commit -m "sync engram memories"
engram sync --import           # On another machine: import new chunks
engram sync --status           # Check sync status
```

Full sync documentation → [DOCS.md](DOCS.md)

## Cloud Server (Multi-User Sync)

For teams sharing memories across multiple developers. Uses PostgreSQL for conflict resolution and real-time sync.

```
Clients (engram local instances)
    ↓ HTTP push/pull
engram-cloud (Go binary + PostgreSQL)
    ↓
PostgreSQL + tsvector search
```

### Setup

```bash
# 1. Start PostgreSQL
docker run -d --name engram-pg -p 5432:5432 \
  -e POSTGRES_USER=engram -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=engram_cloud \
  postgres:16-alpine

# 2. Run the cloud server
export ENGRAM_CLOUD_DB="postgres://engram:secret@localhost:5432/engram_cloud?sslmode=disable"
engram-cloud serve
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_CLOUD_DB` | `postgres://localhost:5432/engram_cloud?sslmode=disable` | PostgreSQL connection string |
| `ENGRAM_CLOUD_PORT` | `7438` | HTTP listen port |

### API Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/health` | No | Health check + PostgreSQL status |
| POST | `/api/v1/sync/push` | Yes | Push mutations (observations, sessions, prompts) |
| GET | `/api/v1/sync/pull` | Yes | Pull changes since cursor |
| POST | `/api/v1/observations` | Yes | Create observation directly |
| GET | `/api/v1/observations/{id}` | Yes | Get observation by ID |
| GET | `/api/v1/search` | Yes | Full-text search (tsvector) |
| GET | `/api/v1/context` | Yes | Get formatted context |
| GET | `/api/v1/stats` | Yes | Project statistics |
| GET | `/api/v1/projects` | Yes | List user's projects |
| POST | `/api/v1/auth/rotate-key` | Yes | Rotate API key |
| POST | `/api/v1/batch` | Yes | Execute multiple operations in one round trip |

Authentication: `Authorization: Bearer <api_key>` + `X-Engram-Protocol: 1` headers required for protected endpoints.

### Running Tests

```bash
# Start the test database
docker run -d --name engram-test-pg -p 5433:5432 \
  -e POSTGRES_USER=engram -e POSTGRES_PASSWORD=testpass -e POSTGRES_DB=engram_test \
  postgres:16-alpine

# Run integration tests (30 tests: 15 cloudstore + 15 cloudserver)
go test -tags integration -v ./internal/cloudstore/ ./internal/cloudserver/
```

## CLI Reference

| Command | Description |
|---------|-------------|
| `engram setup [agent]` | Install agent integration |
| `engram serve [port]` | Start HTTP API (default: 7437) |
| `engram mcp` | Start MCP server (stdio) |
| `engram tui` | Launch terminal UI |
| `engram search <query>` | Search memories |
| `engram save <title> <msg>` | Save a memory |
| `engram timeline <obs_id>` | Chronological context |
| `engram context [project]` | Recent session context |
| `engram stats` | Memory statistics |
| `engram export [file]` | Export to JSON |
| `engram import <file>` | Import from JSON |
| `engram sync` | Git sync export/import |
| `engram projects list\|consolidate\|prune` | Manage project names |
| `engram obsidian-export` | Export to Obsidian vault (beta) |
| `engram version` | Show version |
| `engram-cloud serve` | Start cloud sync server |
| `engram-cloud version` | Show cloud server version |

Full CLI with all flags → [docs/ARCHITECTURE.md#cli-reference](docs/ARCHITECTURE.md#cli-reference)

## Documentation

| Doc | Description |
|-----|-------------|
| [Installation](docs/INSTALLATION.md) | All install methods + platform support |
| [Agent Setup](docs/AGENT-SETUP.md) | Per-agent configuration + Memory Protocol |
| [Architecture](docs/ARCHITECTURE.md) | How it works + MCP tools + project structure |
| [Plugins](docs/PLUGINS.md) | OpenCode & Claude Code plugin details |
| [Comparison](docs/COMPARISON.md) | Why Engram vs claude-mem |
| [Intended Usage](docs/intended-usage.md) | Mental model — how Engram is meant to be used |
| [Obsidian Brain](docs/beta/obsidian-brain.md) | Export memories as Obsidian knowledge graph (beta) |
| [Contributing](CONTRIBUTING.md) | Contribution workflow + standards |
| [Full Docs](DOCS.md) | Complete technical reference |
| Cloud Server | Multi-user sync setup + API (see [Cloud Server](#cloud-server-multi-user-sync) above) |

## License

MIT

---

**Inspired by [claude-mem](https://github.com/thedotmack/claude-mem)** — but agent-agnostic, simpler, and built different.

## Contributors

<a href="https://github.com/Gentleman-Programming/engram/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=Gentleman-Programming/engram&max=100" />
</a>

<p align="center">
  <img width="1024" alt="Engram Cloud" src="assets/branding/engram-cloud-logo.png" />
</p>

<p align="center">
  <strong>Persistent memory for AI coding agents</strong><br>
  <em>Agent-agnostic. Single binary. Zero dependencies.</em>
</p>

<p align="center">
  <a href="docs/INSTALLATION.md">Installation</a> &bull;
  <a href="docs/engram-cloud/README.md">Engram Cloud</a> &bull;
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

## MCP Tools (17)

| Category | Tools |
|----------|-------|
| **Save & Update** | `mem_save`, `mem_update`, `mem_delete`, `mem_suggest_topic_key` |
| **Search & Retrieve** | `mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation` |
| **Session Lifecycle** | `mem_session_start`, `mem_session_end`, `mem_session_summary` |
| **Conflict Surfacing** | `mem_judge` |
| **Utilities** | `mem_save_prompt`, `mem_stats`, `mem_capture_passive`, `mem_merge_projects`, `mem_current_project` |

Full tool reference with parameters → [DOCS.md#mcp-tools-17-tools](DOCS.md#mcp-tools-17-tools)

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

Local SQLite remains the source of truth. Cloud integration is opt-in replication.

```bash
engram sync                    # Export new memories as compressed chunk
git add .engram/ && git commit -m "sync engram memories"
engram sync --import           # On another machine: import new chunks
engram sync --status           # Check sync status
```

Full sync documentation → [DOCS.md](DOCS.md)

## Cloud Integration (Opt-In Replication)

Cloud is optional. Local SQLite stays authoritative; cloud is replication/shared access only.

**Recommended first path (local smoke):**

```bash
docker compose -f docker-compose.cloud.yml up -d
engram cloud config --server http://127.0.0.1:18080
engram cloud enroll smoke-project
engram sync --cloud --project smoke-project
```

Cloud mode is always project-scoped (`--project` is required; `engram sync --cloud --all` is intentionally blocked).

**Upgrade flow for existing local databases** (diagnose → repair → bootstrap → status):

```bash
engram cloud upgrade doctor --project smoke-project        # read-only readiness check
engram cloud upgrade repair --project smoke-project --dry-run
engram cloud upgrade repair --project smoke-project --apply
engram cloud upgrade bootstrap --project smoke-project     # resumable enroll + push + verify
engram cloud upgrade status --project smoke-project        # stage/class/reason
```

See [DOCS.md — Cloud upgrade flow](DOCS.md#cloud-upgrade-flow) for the full state machine.

For authenticated mode, upgrade flow, dashboard behavior, reason codes, and full runtime/env details:

- [Engram Cloud docs landing](docs/engram-cloud/README.md)
- [Engram Cloud quickstart](docs/engram-cloud/quickstart.md)
- [DOCS.md — Cloud CLI reference](DOCS.md#cloud-cli-opt-in)
- [DOCS.md — Cloud Autosync](DOCS.md#cloud-autosync)

## CLI Reference

| Command | Description |
|---------|-------------|
| `engram setup [agent]` | Install agent integration |
| `engram serve [port]` | Start HTTP API (default: 7437) |
| `engram mcp [--tools=PROFILE]` | Start MCP server (stdio transport) |
| `engram tui` | Launch terminal UI |
| `engram search <query>` | Search memories |
| `engram save <title> <msg>` | Save a memory |
| `engram timeline <obs_id>` | Chronological context |
| `engram context [project]` | Recent session context |
| `engram stats` | Memory statistics |
| `engram export [file]` | Export to JSON |
| `engram import <file>` | Import from JSON |
| `engram sync` | Git sync export/import |
| `engram cloud <subcommand>` | Opt-in cloud config/status/enrollment + cloud runtime (`serve`) |
| `engram projects list\|consolidate\|prune` | Manage project names |
| `engram obsidian-export` | Export to Obsidian vault (beta) |
| `engram version` | Show version |

Full CLI with all flags → [docs/ARCHITECTURE.md#cli-reference](docs/ARCHITECTURE.md#cli-reference)

## Documentation

| Doc | Description |
|-----|-------------|
| [Installation](docs/INSTALLATION.md) | All install methods + platform support |
| [Engram Cloud](docs/engram-cloud/README.md) | Cloud landing page, quickstart, branding, and deep links |
| [Agent Setup](docs/AGENT-SETUP.md) | Per-agent configuration + Memory Protocol |
| [Architecture](docs/ARCHITECTURE.md) | How it works + MCP tools + project structure |
| [Plugins](docs/PLUGINS.md) | OpenCode & Claude Code plugin details |
| [Comparison](docs/COMPARISON.md) | Why Engram vs claude-mem |
| [Intended Usage](docs/intended-usage.md) | Mental model — how Engram is meant to be used |
| [Obsidian Brain](docs/beta/obsidian-brain.md) | Export memories as Obsidian knowledge graph (beta) |
| [Contributing](CONTRIBUTING.md) | Contribution workflow + standards |
| [Full Docs](DOCS.md) | Complete technical reference |

> **Dashboard contributors**: if you modify `.templ` files in `internal/cloud/dashboard/`, run `make templ` to regenerate before committing. See [DOCS.md — Dashboard templ regeneration](DOCS.md#dashboard-templ-regeneration).

## License

MIT

---

**Inspired by [claude-mem](https://github.com/thedotmack/claude-mem)** — but agent-agnostic, simpler, and built different.

## Contributors

<a href="https://github.com/Gentleman-Programming/engram/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=Gentleman-Programming/engram&max=100" />
</a>

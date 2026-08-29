<p align="center">
  <img width="1024" alt="Engram — One Brain. Local or Cloud." src="assets/branding/engram-banner.png" />
</p>

<p align="center">
  <strong>Persistent memory for AI coding agents</strong><br>
  <em>One brain. Local or cloud. Agent-agnostic, single binary, zero dependencies.</em>
</p>

<p align="center">
  <a href="docs/INSTALLATION.md">Installation</a> &bull;
  <a href="docs/engram-cloud/README.md">Engram Cloud</a> &bull;
  <a href="docs/AGENT-SETUP.md">Agent Setup</a> &bull;
  <a href="docs/CODEBASE-GUIDE.md">Codebase Guide</a> &bull;
  <a href="docs/ARCHITECTURE.md">Architecture</a> &bull;
  <a href="docs/PLUGINS.md">Plugins</a> &bull;
  <a href="docs/TEAM-USAGE.md">Team Usage</a> &bull;
  <a href="CONTRIBUTING.md">Contributing</a> &bull;
  <a href="DOCS.md">Full Docs</a>
</p>

---

> **engram** `/ˈen.ɡræm/` — _neuroscience_: the physical trace of a memory in the brain.

Your AI coding agent forgets everything when the session ends. Engram gives it a brain.

A **Go binary** with SQLite + FTS5 full-text search, exposed through CLI, HTTP API, MCP, and an interactive TUI. It works with any MCP-compatible agent, including Claude Code, OpenCode, Gemini CLI, Codex, VS Code (Copilot), Antigravity, Cursor, and Windsurf.

No Node.js, Python, or Docker is required: one binary, one SQLite file.

```
Agent (Claude Code / OpenCode / Gemini CLI / Codex / VS Code / Antigravity / ...)
    ↓ MCP stdio
Engram (single Go binary)
    ↓
SQLite + FTS5 (~/.engram/engram.db)
```

## For agents

Treat Engram as a curated project memory, not a transcript sink. Use this operating contract throughout the session.

1. **Orient before writing.** Start with `mem_current_project` to confirm the resolved project and its source. At the start of related work, use `mem_context` and `mem_search` to recover the relevant history.
2. **Search before repeating.** Before revisiting a decision, bug, convention, or request that may already be known, search with focused terms. Search results are previews, not the complete record.
3. **Retrieve progressively.** Use `mem_search` for candidates, `mem_timeline` when surrounding session context matters, and `mem_get_observation` before relying on a full observation.
4. **Save significant knowledge deliberately.** Save completed bug fixes, decisions, discoveries, configuration changes, patterns, and durable user constraints with `mem_save`. Do not capture raw tool output or every conversational turn.
5. **Keep evolving knowledge stable.** Give an evolving topic a stable `topic_key` such as `architecture/auth-model`; reuse it to update that topic rather than creating competing memories. Use `mem_suggest_topic_key` when the key is unclear.
6. **Leave a handoff.** Before ending a session, save a `mem_session_summary` with the goal, instructions, discoveries, accomplished work, next steps, and relevant files.
7. **Recover after compaction.** Persist the compacted handoff with `mem_session_summary` first. Then call `mem_context` to recover recent session history before continuing.

### A useful memory is structured

```markdown
**What**: Added retry-safe upload handling.
**Why**: Retries could create duplicate records.
**Where**: internal/upload/handler.go
**Learned**: Reuse the request id as the idempotency key.
```

Use a short, searchable title and a fitting type with that content. The full [Memory Protocol](DOCS.md#memory-protocol) defines the durable-save rules and session-summary shape.

### Choose MCP tools by intent

Tool availability can vary by MCP profile. Start with the intent, then use your client's tool discovery mechanism (such as `ToolSearch`) only when a deferred tool is needed.

| Intent | Start with |
| --- | --- |
| Confirm the project and recover recent work | `mem_current_project`, `mem_context` |
| Find prior knowledge without repeating work | `mem_search` |
| Inspect a result in enough detail | `mem_timeline`, `mem_get_observation` |
| Save or refine durable knowledge | `mem_save`, `mem_update`, `mem_suggest_topic_key` |
| Preserve the user's request | `mem_save_prompt` |
| Hand off or close a session | `mem_session_summary`, `mem_session_start`, `mem_session_end` |
| Review stale knowledge or memory relationships | `mem_review`, `mem_judge`, `mem_compare` |
| Diagnose project or store state | `mem_doctor` |

For parameters and the complete, current tool reference, see [the full documentation](DOCS.md).

## Quick start

### Install

```bash
brew install gentleman-programming/tap/engram
```

For Windows, Linux, source builds, and downloads, see [Installation](docs/INSTALLATION.md).

### Set up your agent

Run the setup command for the agent you use, then restart that agent. `engram setup` writes the applicable MCP and integration configuration; it does not require you to start a server for the usual stdio-only setup.

| Agent | Setup |
| --- | --- |
| Claude Code | `claude plugin marketplace add Gentleman-Programming/engram && claude plugin install engram` |
| Pi | `engram setup pi` |
| OpenCode | `engram setup opencode` |
| Gemini CLI | `engram setup gemini-cli` |
| Codex | `engram setup codex` |
| Antigravity CLI | `engram setup antigravity-cli` |
| Windsurf | `engram setup windsurf` |
| Qwen Code | `engram setup qwen` |
| Kiro | `engram setup kiro` |
| Cursor | `engram setup cursor` |
| VS Code (Copilot) | `engram setup vscode-copilot` |
| Kilo Code | `engram setup kilocode` |
| Another MCP-compatible agent | [Manual MCP setup](docs/AGENT-SETUP.md#any-other-mcp-agent) |

See [Agent Setup](docs/AGENT-SETUP.md) for per-agent configuration, plugin behavior, manual MCP setup, compaction resilience, and troubleshooting. Pi users can also find the package at [`gentle-engram`](plugin/pi/README.md).

## Local first, portable when needed

Engram keeps memory local by default. The local SQLite database is authoritative; Git Sync exports portable compressed chunks for sharing across machines, and Engram Cloud is optional, project-scoped replication/shared access with browser visibility.

| Need | Start here |
| --- | --- |
| Local memory and the runtime model | [Architecture](docs/ARCHITECTURE.md) |
| Share memory with Git | [Git Sync reference](DOCS.md#git-sync-chunked) |
| Use optional Cloud replication | [Engram Cloud](docs/engram-cloud/README.md) |
| Diagnose or recover Cloud operations | [Cloud troubleshooting](docs/engram-cloud/troubleshooting.md) |

For an existing local database, use the guided upgrade sequence. If the dry run reports changes, apply them before bootstrap; otherwise continue directly to bootstrap.

```bash
engram cloud upgrade doctor --project <project>
engram cloud upgrade repair --project <project> --dry-run
engram cloud upgrade repair --project <project> --apply # only when the dry run reports changes
engram cloud upgrade bootstrap --project <project>
engram cloud upgrade status --project <project>
```

See the [Cloud upgrade reference](DOCS.md#cloud-upgrade-flow) for apply, rollback, and recovery details.

## Terminal UI

```bash
engram tui
```

<p align="center">
  <img src="assets/tui-dashboard.png" alt="TUI Dashboard" width="400" />
  <img width="400" alt="TUI recent observations" src="assets/tui-recent.png" />
  <img src="assets/tui-detail.png" alt="TUI Observation Detail" width="400" />
  <img src="assets/tui-search.png" alt="TUI Search Results" width="400" />
</p>

Navigate with `j`/`k`, use `Enter` to drill in, `c` to copy content to the clipboard, `/` to search, and `Esc` to go back. The TUI uses the Catppuccin Mocha theme.

## Documentation

| Doc | Description |
| --- | --- |
| [Installation](docs/INSTALLATION.md) | Platform support and all installation methods |
| [Agent Setup](docs/AGENT-SETUP.md) | Per-agent configuration and compaction resilience |
| [Intended Usage](docs/intended-usage.md) | The human mental model for using Engram |
| [Architecture](docs/ARCHITECTURE.md) | Memory model, tool behavior, and project structure |
| [Codebase Guide](docs/CODEBASE-GUIDE.md) | Repository structure, flows, and implementation landmarks |
| [Plugins](docs/PLUGINS.md) | OpenCode and Claude Code plugin details |
| [Team Usage](docs/TEAM-USAGE.md) | Shared-memory conventions |
| [Engram Cloud](docs/engram-cloud/README.md) | Cloud quickstart, deployment, and technical links |
| [Doctor](docs/DOCTOR.md) | Operational diagnosis and repair workflows |
| [Binary self-testing](docs/SELF-TESTING.md) | Isolated reliability and performance checks for released binaries |
| [Beta Testing](docs/BETA_TESTING.md) | Isolated beta testing flows and cleanup guidance |
| [Comparison](docs/COMPARISON.md) | Engram compared with claude-mem |
| [Obsidian Brain](docs/beta/obsidian-brain.md) | Export memories as an Obsidian knowledge graph (beta) |
| [Full Docs](DOCS.md) | Complete CLI, environment, API, and operational reference |

> **Dashboard contributors:** if you modify `.templ` files in `internal/cloud/dashboard/`, run `make templ` to regenerate before committing. See [Dashboard templ regeneration](DOCS.md#dashboard-templ-regeneration).

## Contributing

Every change starts with an approved issue. See [Contributing](CONTRIBUTING.md) for the issue-first workflow, labels, review requirements, and contributor standards.

> **Trademark notice:** The Engram names and logos are trademarks of Alan Buscaglia. The MIT License applies to the code; it does not permit implying endorsement or official affiliation. See [TRADEMARKS.md](TRADEMARKS.md).

## License

MIT

---

**Inspired by [claude-mem](https://github.com/thedotmack/claude-mem)** — but agent-agnostic, simpler, and built different.

## Contributors

<a href="https://github.com/Gentleman-Programming/engram/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=Gentleman-Programming/engram&max=100" />
</a>

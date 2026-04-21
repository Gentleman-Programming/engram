[← Back to README](README.md)

# Engram — Technical Reference

**Persistent memory for AI coding agents**

This is the complete technical reference for Engram. For getting started, see the [README](README.md). For per-agent setup, see [Agent Setup](docs/AGENT-SETUP.md).

---

## Quick Navigation

| Section | What you'll find |
|---------|-----------------|
| [Database Schema](#database-schema) | Tables, FTS5, SQLite config |
| [HTTP API](#http-api-endpoints) | All REST endpoints with request/response details |
| [MCP Tools](#mcp-tools-16-tools) | Detailed reference for all 16 memory tools |
| [Memory Protocol](#memory-protocol) | When/how agents should use the tools |
| [Project Name Normalization](#project-name-normalization) | Auto-detection, normalization, similar-project warnings |
| [Features](#features) | FTS5 search, timeline, privacy, git sync, compression |
| [TUI](#terminal-ui-tui) | Screens, navigation, architecture |
| [Running as a Service](#running-as-a-service) | systemd setup |
| [Design Decisions](#design-decisions) | Why Go, why SQLite, why no auto-capture |

For other docs:

| Doc | Description |
|-----|-------------|
| [Installation](docs/INSTALLATION.md) | All install methods + platform support |
| [Agent Setup](docs/AGENT-SETUP.md) | Per-agent configuration + compaction survival |
| [Architecture](docs/ARCHITECTURE.md) | How it works, session lifecycle, CLI reference, project structure |
| [Plugins](docs/PLUGINS.md) | OpenCode & Claude Code plugin details |
| [Comparison](docs/COMPARISON.md) | Why Engram vs claude-mem |

---

## Database Schema

### Tables

- **sessions** — `id` (TEXT PK), `project`, `directory`, `started_at`, `ended_at`, `summary`, `status`
- **observations** — `id` (INTEGER PK AUTOINCREMENT), `session_id` (FK), `type`, `title`, `content`, `tool_name`, `project`, `scope`, `topic_key`, `normalized_hash`, `revision_count`, `duplicate_count`, `last_seen_at`, `created_at`, `updated_at`, `deleted_at`
- **observations_fts** — FTS5 virtual table synced via triggers (`title`, `content`, `tool_name`, `type`, `project`)
- **user_prompts** — `id` (INTEGER PK AUTOINCREMENT), `session_id` (FK), `content`, `project`, `created_at`
- **prompts_fts** — FTS5 virtual table synced via triggers (`content`, `project`)
- **sync_chunks** — `chunk_id` (TEXT PK), `imported_at` — tracks which chunks have been imported to prevent duplicates

### SQLite Configuration

- WAL mode for concurrent reads
- Busy timeout 5000ms
- Synchronous NORMAL
- Foreign keys ON

---

## HTTP API Endpoints

All endpoints return JSON. Server listens on `127.0.0.1:7437`.

### Health

- `GET /health` — Returns `{"status": "ok", "service": "engram", "version": "<current>"}`

### Sessions

- `POST /sessions` — Create session. Body: `{id, project, directory}`
- `POST /sessions/{id}/end` — End session. Body: `{summary}`
- `GET /sessions/recent` — Recent sessions. Query: `?project=X&limit=N`
- `DELETE /sessions/{id}` — Delete empty session. Returns 409 if session has observations.

### Observations

- `POST /observations` — Add observation. Body: `{session_id, type, title, content, tool_name?, project?, scope?, topic_key?}`
- `GET /observations/recent` — Recent observations. Query: `?project=X&scope=project|personal&limit=N`
- `GET /observations/{id}` — Get single observation by ID
- `PATCH /observations/{id}` — Update fields. Body: `{title?, content?, type?, project?, scope?, topic_key?}`
- `DELETE /observations/{id}` — Delete observation (`?hard=true` for hard delete, soft delete by default)

### Search

- `GET /search` — FTS5 search. Query: `?q=QUERY&type=TYPE&project=PROJECT&scope=SCOPE&limit=N&since=DATE&until=DATE`

### Timeline

- `GET /timeline` — Chronological context. Query: `?observation_id=N&before=5&after=5`

### Prompts

- `POST /prompts` — Save user prompt. Body: `{session_id, content, project?}`
- `GET /prompts/recent` — Recent prompts. Query: `?project=X&limit=N`
- `GET /prompts/search` — Search prompts. Query: `?q=QUERY&project=X&limit=N`
- `DELETE /prompts/{id}` — Delete a prompt by ID. Returns 404 if not found.

### Context

- `GET /context` — Formatted context. Query: `?project=X&scope=project|personal`

### Passive Capture

- `POST /observations/passive` — Extract structured learnings from text. Body: `{content, session_id?, project?}`

### Export / Import

- `GET /export` — Export all data as JSON
- `POST /import` — Import data from JSON. Body: ExportData JSON

### Stats

- `GET /stats` — Memory statistics

### Project Migration

- `POST /projects/migrate` — Migrate observations between project names. Body: `{source, target}`

### Sync Status

- `GET /sync/status` — Chunk sync status (local vs remote counts, pending imports)

### Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ENGRAM_DATA_DIR` | Override data directory | `~/.engram` |
| `ENGRAM_PORT` | Override HTTP server port | `7437` |
| `ENGRAM_PROJECT` | Override project name for MCP server | auto-detected via git |

---

## MCP Tools (16 tools)

### mem_search

Search persistent memory across all sessions. Supports FTS5 full-text search with type/project/scope/limit filters.

- **query** (required): Search query — natural language or keywords. Empty queries are rejected.
- **since**: Filter observations created on or after this date (YYYY-MM-DD or RFC3339).
- **until**: Filter observations created on or before this date (YYYY-MM-DD or RFC3339).

Invalid date formats return an explicit error (SQLite's `datetime()` silently ignores bad input, so validation happens in Go).

### mem_save

Save structured observations. The tool description teaches agents the format:

- **title**: Short, searchable (e.g. "JWT auth middleware")
- **type**: `decision` | `architecture` | `bugfix` | `pattern` | `config` | `discovery` | `learning`
- **scope**: `project` (default) | `personal`
- **topic_key**: optional canonical topic id (e.g. `architecture/auth-model`) used to upsert evolving memories
- **content**: Structured with `**What**`, `**Why**`, `**Where**`, `**Learned**`

Exact duplicate saves are deduplicated in a rolling time window using a normalized content hash + project + scope + type + title.
When `topic_key` is provided, `mem_save` upserts the latest observation in the same `project + scope + topic_key`, incrementing `revision_count`.

### mem_update

Update an observation by ID. Supports partial updates for `title`, `content`, `type`, `project`, `scope`, and `topic_key`.

### mem_suggest_topic_key

Suggest a stable `topic_key` from `type + title` (or content fallback). Uses family heuristics like `architecture/*`, `bug/*`, `decision/*`, etc. Use before `mem_save` when you want evolving topics to upsert into a single observation.

### mem_delete

Delete an observation by ID. Uses soft-delete by default (`deleted_at`); optional hard-delete for permanent removal.

### mem_save_prompt

Save user prompts — records what the user asked so future sessions have context about user goals.

### mem_context

Get recent memory context from previous sessions — shows sessions, prompts, and observations, with optional scope filtering for observations.

- **since**: Filter to sessions/observations on or after this date (YYYY-MM-DD or RFC3339).
- **until**: Filter to sessions/observations on or before this date (YYYY-MM-DD or RFC3339).

### mem_stats

Show memory system statistics — sessions, observations, prompts, projects.

### mem_timeline

Progressive disclosure: after searching, drill into chronological context around a specific observation. Shows N observations before and after within the same session.

### mem_get_observation

Get full untruncated content of a specific observation by ID.

### mem_session_summary

Save comprehensive end-of-session summary:

```
## Goal
## Instructions
## Discoveries
## Accomplished
## Relevant Files
```

### mem_session_start

Register the start of a new coding session.

### mem_session_end

Mark a session as completed with optional summary.

### mem_capture_passive

Extract structured learnings from text output. Looks for `## Key Learnings:` sections and saves each numbered/bulleted item as a separate observation. Duplicates are automatically skipped.

### mem_sessions

List sessions within a date range. Shows session date, project, and observation count.

- **since**: Filter sessions started on or after this date (YYYY-MM-DD or RFC3339).
- **until**: Filter sessions started on or before this date (YYYY-MM-DD or RFC3339).
- **project**: Filter by project name (optional).
- **limit**: Max results (default: 20, max: 50).

### mem_merge_projects

**Admin tool.** Merge multiple project name variants into a single canonical name. Accepts an array of source project names and a target canonical name. All observations, sessions, and prompts from the source projects are reassigned to the canonical project.

---

## Memory Protocol

The Memory Protocol teaches agents **when** and **how** to use Engram's MCP tools. Without it, the agent has the tools but no behavioral guidance. Add this to your agent's prompt file (see [Agent Setup](docs/AGENT-SETUP.md) for per-agent locations).

### WHEN TO SAVE (mandatory)

Call `mem_save` IMMEDIATELY after any of these:
- Bug fix completed
- Architecture or design decision made
- Non-obvious discovery about the codebase
- Configuration change or environment setup
- Pattern established (naming, structure, convention)
- User preference or constraint learned

Format for `mem_save`:
- **title**: Verb + what — short, searchable (e.g. "Fixed N+1 query in UserList", "Chose Zustand over Redux")
- **type**: `bugfix` | `decision` | `architecture` | `discovery` | `pattern` | `config` | `preference`
- **scope**: `project` (default) | `personal`
- **topic_key** (optional, recommended for evolving decisions): stable key like `architecture/auth-model`
- **content**:
  ```
  **What**: One sentence — what was done
  **Why**: What motivated it (user request, bug, performance, etc.)
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you (omit if none)
  ```

### Topic update rules (mandatory)

- Different topics must not overwrite each other (e.g. architecture vs bugfix)
- Reuse the same `topic_key` to update an evolving topic instead of creating new observations
- If unsure about the key, call `mem_suggest_topic_key` first and then reuse it
- Use `mem_update` when you have an exact observation ID to correct

### WHEN TO SEARCH MEMORY

When the user asks to recall something — any variation of "remember", "recall", "what did we do", "how did we solve", "recordar", "acordate", or references to past work:
1. First call `mem_context` — checks recent session history (fast, cheap)
2. If not found, call `mem_search` with relevant keywords (FTS5 full-text search)
3. If you find a match, use `mem_get_observation` for full untruncated content

Also search memory PROACTIVELY when:
- Starting work on something that might have been done before
- The user mentions a topic you have no context on — check if past sessions covered it

### SESSION CLOSE PROTOCOL (mandatory)

Before ending a session or saying "done" / "listo" / "that's it", you MUST call `mem_session_summary` with this structure:

```
## Goal
[What we were working on this session]

## Instructions
[User preferences or constraints discovered — skip if none]

## Discoveries
- [Technical findings, gotchas, non-obvious learnings]

## Accomplished
- [Completed items with key details]

## Next Steps
- [What remains to be done — for the next session]

## Relevant Files
- path/to/file — [what it does or what changed]
```

This is NOT optional. If you skip this, the next session starts blind.

### PASSIVE CAPTURE

When completing a task, include a `## Key Learnings:` section at the end of your response with numbered items. Engram will automatically extract and save these as observations.

Example:
```
## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
```

You can also call `mem_capture_passive(content)` directly with any text that contains a learning section.

### AFTER COMPACTION

If you see a message about compaction or context reset:
1. IMMEDIATELY call `mem_session_summary` with the compacted summary content
2. Then call `mem_context` to recover additional context from previous sessions
3. Only THEN continue working

Do not skip step 1. Without it, everything done before compaction is lost from memory.

---

## Project Name Normalization

Engram automatically prevents project name drift — the same project saved under different names (`"engram"` vs `"Engram"` vs `"engram-memory"`) by different clients or users.

### Automatic normalization

All project names are normalized on write and read: **lowercase**, **trimmed**, **collapsed hyphens/underscores**. If a name is changed during normalization, a warning is included in the response.

### Auto-detection

The MCP server auto-detects the project name at startup using a priority chain:
1. `--project` flag
2. `ENGRAM_PROJECT` environment variable
3. Git remote origin URL (extracts repo name)
4. Git repository root directory name
5. Current working directory basename

### Similar-project warnings

When saving to a project that doesn't exist yet, Engram checks for similar existing project names (Levenshtein distance, substring, case-insensitive matching) and warns the agent if a likely variant already exists.

### Retroactive cleanup

Use `engram projects consolidate` to interactively merge variant project names, or `mem_merge_projects` for agent-driven consolidation.

---

## Features

### Input Validation

- **Empty queries**: `Search()` rejects blank/whitespace-only queries before they reach FTS5 (which would crash on `MATCH ""`).
- **Date filters**: `since` and `until` accept only `YYYY-MM-DD` or RFC3339 formats. Invalid strings (e.g. `"yesterday"`, `"hace 3 dias"`) return an explicit error instead of being silently ignored by SQLite's `datetime()`.
- **Required fields**: `mem_save` validates that `title` and `content` are non-empty after trimming whitespace.
- **Import normalization**: Project names from imported JSON/sync data are normalized (lowercase, trimmed, collapsed hyphens) to match local conventions.

### Full-Text Search (FTS5)

- Searches across title, content, tool_name, type, project, and topic_key
- Query sanitization: wraps each word in quotes to avoid FTS5 syntax errors on special characters (`()`, `*`, `+`, `"`)
- Supports type, project, scope, since, and until filters

### Timeline (Progressive Disclosure)

Three-layer pattern for token-efficient memory retrieval:

1. `mem_search` — Find relevant observations
2. `mem_timeline` — Drill into chronological neighborhood of a result
3. `mem_get_observation` — Get full untruncated content

### Privacy Tags

`<private>...</private>` content is stripped at TWO levels:

1. **Plugin layer** (TypeScript) — Strips before data leaves the process
2. **Store layer** (Go) — `stripPrivateTags()` runs inside `AddObservation()` and `AddPrompt()`

Example: `Set up API with <private>sk-abc123</private>` becomes `Set up API with [REDACTED]`

### User Prompt Storage

Separate table captures what the USER asked (not just tool calls). Gives future sessions the "why" behind the "what". Full FTS5 search support.

### Export / Import

Share memories across machines, backup, or migrate:

- `engram export` — JSON dump of all sessions, observations, prompts
- `engram import <file>` — Load from JSON, sessions use INSERT OR IGNORE (skip duplicates), atomic transaction

### Git Sync (Chunked)

Share memories through git repositories using compressed chunks with a manifest index.

- `engram sync` — Exports new memories as a gzipped JSONL chunk to `.engram/chunks/`
- `engram sync --all` — Exports ALL memories from every project
- `engram sync --import` — Imports chunks listed in the manifest that haven't been imported yet
- `engram sync --status` — Shows how many chunks exist locally vs remotely
- `engram sync --project NAME` — Filters export to a specific project

```
.engram/
├── manifest.json          <- index of all chunks (small, git-mergeable)
├── chunks/
│   ├── a3f8c1d2.jsonl.gz <- chunk 1 (gzipped JSONL)
│   ├── b7d2e4f1.jsonl.gz <- chunk 2
│   └── ...
└── engram.db              <- local working DB (gitignored)
```

**Why chunks?**
- Each `engram sync` creates a NEW chunk — old chunks are never modified
- No merge conflicts: each dev creates independent chunks, git just adds files
- Chunks are content-hashed (SHA-256 prefix) — each chunk is imported only once
- The manifest is the only file git diffs — it's small and append-only
- Compressed: a chunk with 8 sessions + 10 observations = ~2KB

### Agent-Driven Compression

Instead of a separate LLM service, the agent itself compresses observations. The agent already has the model, context, and API key.

**Two levels:**

- **Per-action** (`mem_save`): Structured summaries (What/Why/Where/Learned)
- **Session summary** (`mem_session_summary`): Comprehensive end-of-session summary (Goal/Instructions/Discoveries/Accomplished/Files)

### No Raw Auto-Capture

All memory comes from the agent itself — no firehose of raw tool calls. Why? Raw tool calls (`edit: {file: "foo.go"}`, `bash: {command: "go build"}`) are noisy and pollute FTS5 search. The agent's curated summaries are higher signal, more searchable, and don't bloat the database. Shell history and git provide the raw audit trail.

---

## Terminal UI (TUI)

Interactive Bubbletea-based terminal UI. Launch with `engram tui`.

### Screens

| Screen | Description |
|---|---|
| **Dashboard** | Stats overview (sessions, observations, prompts, projects) + menu |
| **Search** | FTS5 text search with text input |
| **Search Results** | Browsable results list from search |
| **Recent Observations** | Browse all observations, newest first |
| **Observation Detail** | Full content of a single observation, scrollable |
| **Timeline** | Chronological context around an observation (before/after) |
| **Sessions** | Browse all sessions |
| **Session Detail** | Observations within a specific session |

### Navigation

- `j/k` or arrow keys — Navigate lists
- `Enter` — Select / drill into detail
- `t` — View timeline for selected observation
- `s` or `/` — Quick search from any screen
- `Esc` or `q` — Go back / quit
- `Ctrl+C` — Force quit

### Visual Features

- **Catppuccin Mocha** color palette
- **`(active)` badge** — shown next to sessions and observations from active sessions, sorted to top
- **Scroll indicators** — position in long lists (e.g. "showing 1-20 of 50")
- **2-line items** — each observation shows title + content preview

---

## Running as a Service

### Using systemd

1. Move binary to `~/.local/bin` (ensure it's in your `$PATH`)
2. Create directories: `mkdir -p ~/.engram ~/.config/systemd/user`
3. Create `~/.config/systemd/user/engram.service` (see below)
4. `systemctl --user daemon-reload`
5. `systemctl --user enable engram`
6. `systemctl --user start engram`
7. `journalctl --user -u engram -f`

```ini
[Unit]
Description=Engram Memory Server
After=network.target

[Service]
WorkingDirectory=%h
ExecStart=%h/.local/bin/engram serve
Restart=always
RestartSec=3
Environment=ENGRAM_DATA_DIR=%h/.engram

[Install]
WantedBy=default.target
```

---

## Design Decisions

1. **Go over TypeScript** — Single binary, cross-platform, no runtime. The initial prototype was TS but was rewritten.
2. **SQLite + FTS5 over vector DB** — FTS5 covers 95% of use cases. No ChromaDB/Pinecone complexity.
3. **Agent-agnostic core** — Go binary is the brain, thin plugins per-agent. Not locked to any agent.
4. **Agent-driven compression** — The agent already has an LLM. No separate compression service.
5. **Privacy at two layers** — Strip in plugin AND store. Defense in depth.
6. **Pure Go SQLite (modernc.org/sqlite)** — No CGO means true cross-platform binary distribution.
7. **No raw auto-capture** — The agent saves curated summaries. Shell history and git provide the raw audit trail.
8. **TUI with Bubbletea** — Interactive terminal UI following Gentleman Bubbletea patterns.

---

## Dependencies

### Go

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/mark3labs/mcp-go` | v0.44.0 | MCP protocol implementation |
| `modernc.org/sqlite` | v1.45.0 | Pure Go SQLite driver (no CGO) |
| `github.com/charmbracelet/bubbletea` | v1.3.10 | Terminal UI framework |
| `github.com/charmbracelet/lipgloss` | v1.1.0 | Terminal styling |
| `github.com/charmbracelet/bubbles` | v1.0.0 | TUI components |

### OpenCode Plugin

- `@opencode-ai/plugin` — OpenCode plugin types and helpers
- Runtime: Bun (built into OpenCode)

---

## Next Steps

- [Agent Setup](docs/AGENT-SETUP.md) — connect your agent to Engram
- [Plugins](docs/PLUGINS.md) — what the OpenCode and Claude Code plugins add beyond bare MCP
- [Obsidian Brain](docs/beta/obsidian-brain.md) — visualize memories as a knowledge graph (beta)
- [Contributing](CONTRIBUTING.md) — how to contribute

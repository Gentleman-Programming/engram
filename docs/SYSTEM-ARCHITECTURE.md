[← Back to README](../README.md)

# Engram — System Architecture

Technical reference for the internal architecture, module structure, and cloud sync design.

---

## Table of Contents

- [What is Engram?](#what-is-engram)
- [Design Principles](#design-principles)
- [System Overview](#system-overview)
- [Package Map](#package-map)
- [Data Flow](#data-flow)
- [Database Schema](#database-schema)
- [MCP Tools](#mcp-tools)
- [Cloud Sync Architecture](#cloud-sync-architecture)
- [Testing Strategy](#testing-strategy)
- [External Dependencies](#external-dependencies)
- [File Layout](#file-layout)

---

## What is Engram?

Engram is a **persistent memory system for AI coding agents**. When a coding agent (Claude Code, OpenCode, Gemini CLI, Codex, etc.) completes a session, everything it learned — decisions, bug fixes, conventions, discoveries — disappears. Engram gives it a brain.

A single Go binary with SQLite + FTS5 full-text search, exposed via CLI, HTTP API, MCP server, and an interactive TUI. Works with **any agent** that supports MCP.

> **engram** `/ˈen.ɡræm/` — *neuroscience*: the physical trace of a memory in the brain.

---

## Design Principles

1. **Local-first**: SQLite is always the source of truth. Cloud is replication and shared access.
2. **Agent-agnostic**: Works with any MCP-compatible agent. No vendor lock-in.
3. **Zero dependencies**: Single binary, no Node.js, no Python, no Docker for local use.
4. **Fail loudly**: When sync blocks or policy prevents an operation, report it — never silently drop data.

---

## System Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        cmd/engram (CLI)                             │
│                                                                     │
│  engram mcp          engram serve       engram tui                  │
│  (MCP stdio)         (HTTP REST)        (Bubbletea)                 │
│                                                                     │
│  engram cloud setup/sync/status         engram sync                 │
│  (Cloud sync CLI)                       (Git file sync)             │
└───────────┬──────────────┬───────────────┬──────────────────────────┘
            │              │               │
     ┌──────▼──────┐ ┌────▼─────┐  ┌──────▼──────┐
     │ internal/mcp│ │ internal/│  │ internal/tui│
     │ (15 tools)  │ │ server   │  │ (Bubbletea) │
     └──────┬──────┘ └────┬─────┘  └──────┬──────┘
            │              │               │
            └──────────────┼───────────────┘
                           │
                    ┌──────▼──────┐
                    │StoreInterface│  ← contract (internal/types)
                    └──────┬──────┘
                           │
              ┌────────────┼─────────────┐
              │            │             │
       ┌──────▼──────┐    │      ┌──────▼──────┐
       │ store.Store  │    │      │ RemoteStore │ (planned)
       │ (SQLite+FTS5)│    │      │ (HTTP proxy)│
       └──────┬──────┘    │      └─────────────┘
              │            │
       ┌──────▼──────┐    │
       │ SyncClient   │    │
       │ (push/pull)  │    │
       └──────┬──────┘    │
              │            │
              ▼            ▼
       ┌────────────────────────┐
       │   engram-cloud server  │  (cmd/engram-cloud)
       │   cloudserver (chi)    │
       │   cloudstore (pgx/v5) │
       │   PostgreSQL + tsvector│
       └────────────────────────┘
```

---

## Package Map

### Core

| Package | Purpose | Key Exports |
|---------|---------|-------------|
| `internal/types` | Shared domain model. All structs and interfaces live here. No internal dependencies. | `Observation`, `Session`, `Prompt`, `Stats`, `SyncState`, `SyncMutation`, `StoreInterface`, `StoreSyncer` |
| `internal/store` | SQLite persistence engine. FTS5 full-text search, sync mutation journaling, project enrollment, cloud config storage. | `Store`, `New()`, `Config` |
| `internal/format` | Formats observations, sessions, and prompts into context strings for MCP tools. | `Context()`, `Truncate()` |

### Access Layers

| Package | Purpose | Key Exports |
|---------|---------|-------------|
| `internal/mcp` | MCP stdio server. Exposes 15 tools in two profiles (agent + admin). Agents connect via stdio transport. | `NewServerWithConfig()` |
| `internal/server` | HTTP REST API for local use. Powers `engram serve`. | `Server`, `New()` |
| `internal/tui` | Interactive terminal UI (Bubbletea + Lipgloss). Dashboard, search, observation detail screens. Catppuccin Mocha theme. | `Model`, `New()` |

### Cloud Sync

| Package | Purpose | Key Exports |
|---------|---------|-------------|
| `internal/cloudserver` | HTTP API for the cloud sync server (chi router). Auth middleware, protocol version, rate limiting, batch operations. | `New(store)` → `http.Handler` |
| `internal/cloudstore` | PostgreSQL backend for cloud. Users, projects, membership, observations with tsvector search, push/pull protocol, idempotency, maintenance. | `Store`, `New()`, `ProcessPush()`, `Pull()` |
| `internal/remote` | Cloud sync client. HTTP client wrapper with retries, config management, SyncClient for local-first push/pull. | `Client`, `CloudConfig`, `SyncClient` |
| `internal/sync` | Git-friendly file-based sync (manifest + compressed chunks). For sharing memories via git repos without cloud. | `Syncer`, `Manifest`, `ChunkEntry` |

### Utilities

| Package | Purpose | Key Exports |
|---------|---------|-------------|
| `internal/project` | Detects project name from filesystem path. | `DetectProject()`, `Similar()` |
| `internal/setup` | Agent plugin installer. Configures Claude Code, OpenCode, Gemini CLI, Codex, VS Code. | `Install()`, `SupportedAgents()` |
| `internal/version` | Checks for newer releases on GitHub. | `CheckLatest()` |
| `internal/obsidian` | Exports memories as an Obsidian knowledge graph (beta). | `Exporter`, `Hub` |

### Binaries

| Binary | Entry Point | Purpose |
|--------|-------------|---------|
| `engram` | `cmd/engram/main.go` | Primary CLI — MCP server, HTTP server, TUI, sync, setup, search, cloud commands |
| `engram-cloud` | `cmd/engram-cloud/main.go` | Cloud sync server — PostgreSQL backend, HTTP API |

### Dependency Graph

```
cmd/engram
  ├── internal/mcp        (MCP stdio server)  → store, project
  ├── internal/server     (HTTP REST API)     → store
  ├── internal/tui        (Bubbletea TUI)     → store
  ├── internal/setup      (agent installer)
  ├── internal/sync       (file-based sync)   → store
  ├── internal/remote     (cloud sync client) → types
  ├── internal/obsidian   (vault export)      → store, types
  └── internal/version    (update check)

cmd/engram-cloud
  └── internal/cloudserver (HTTP API)         → cloudstore

internal/store            → internal/types, internal/format
internal/cloudstore       → (PostgreSQL, standalone)
internal/remote           → internal/types (uses SyncStore interface, not *store.Store)
internal/types            → (no internal deps, shared by all)
internal/format           → internal/types
```

---

## Data Flow

### Local Mode (default)

```
Agent writes → MCP tool (mem_save) → store.Store → SQLite
Agent reads  → MCP tool (mem_search) → store.Store → SQLite FTS5
```

### Local-First with Cloud Sync (`--backend local-sync`)

```
Agent writes → store.Store → SQLite
                   ↓ (trigger)
              sync_mutations table
                   ↓ (background, debounced 10s)
              SyncClient.PushOnce()
                   ↓ (HTTP POST, batches of 100)
              engram-cloud /api/v1/sync/push
                   ↓
              PostgreSQL (cloudstore)

Other team member's writes:
              PostgreSQL → engram-cloud /api/v1/sync/pull
                   ↓ (background, every 120s, pages of 500)
              SyncClient.PullOnce()
                   ↓
              store.ApplyPulledMutation() → SQLite
```

### Cloud-Only Mode (`--backend cloud`, planned)

```
Agent writes → MCP tool → RemoteStore → HTTP POST → engram-cloud → PostgreSQL
Agent reads  → MCP tool → RemoteStore → HTTP GET  → engram-cloud → PostgreSQL
```

---

## Database Schema

### SQLite (Local Store)

| Table | Purpose |
|-------|---------|
| `sessions` | Coding sessions with directory, timestamps, summary |
| `observations` | Memories: decisions, bugs, patterns, discoveries |
| `observations_fts` | FTS5 virtual table for full-text search |
| `user_prompts` | Saved prompt templates |
| `prompts_fts` | FTS5 virtual table for prompt search |
| `sync_state` | Sync cursor, lease, backoff state per target |
| `sync_mutations` | Journal of local changes pending push (auto-populated by triggers) |
| `sync_enrolled_projects` | Which projects participate in cloud sync |
| `sync_cloud_config` | Cloud connection config (key-value pairs) |
| `sync_chunks` | Tracks git-synced chunks to avoid re-import |

### PostgreSQL (Cloud Store)

| Table | Purpose |
|-------|---------|
| `users` | User accounts with hashed API keys (bcrypt) |
| `projects` | Project definitions |
| `project_members` | User-project membership with roles |
| `observations` | Cloud observations with tsvector search |
| `observation_revisions` | LWW conflict revisions (topic_key collisions) |
| `sessions` | Cloud sessions |
| `prompts` | Cloud prompts |
| `server_seq_counter` | Per-project monotonic sequence (advisory lock) |
| `sync_cursors` | Per-user per-project pull cursor |
| `idempotency_keys` | Push request deduplication (24h TTL) |
| `rate_limits` | Per-user per-endpoint sliding window counters |

---

## MCP Tools

15 tools across two profiles:

| Category | Tools | Profile |
|----------|-------|---------|
| **Save & Update** | `mem_save`, `mem_update`, `mem_delete`, `mem_suggest_topic_key` | agent/admin |
| **Search & Retrieve** | `mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation` | agent |
| **Session Lifecycle** | `mem_session_start`, `mem_session_end`, `mem_session_summary` | agent |
| **Utilities** | `mem_save_prompt`, `mem_stats`, `mem_capture_passive`, `mem_merge_projects` | agent/admin |

---

## Cloud Sync Architecture

### Implementation Phases

| Phase | Status | Description |
|-------|--------|-------------|
| **Phase 1** | Complete | Schema migrations, StoreInterface extraction, sync_mutations triggers, import idempotency |
| **Phase 2** | Complete | Cloud server MVP — PostgreSQL schema, auth, push/pull protocol, CRUD, tsvector search, batch endpoint, rate limiting, maintenance. 32 integration tests. |
| **Phase 3** | In Progress | Client integration — HTTP client wrapper, config, SyncClient (push/pull/debounce/backoff). 32 unit tests. Remaining: RemoteStore, CLI commands, --backend flag. |
| **Phase 4** | Planned | Auto-sync polish, monitoring, production hardening |

### Sync Protocol

**Push**: Client reads `sync_mutations` → batches (100/push) → POST `/api/v1/sync/push` → server assigns monotonic `server_seq` per mutation → client ACKs.

**Pull**: Client sends `since_seq` cursor → GET `/api/v1/sync/pull` → server returns entities with `server_seq > cursor` (500/page) → client applies to local store → advances cursor.

**Conflict Resolution**: Last-Writer-Wins (LWW) by `server_seq`. When two observations share a `topic_key`, the server saves the older version as a revision and overwrites with the newer.

**Guarantees**:
- Monotonic ordering via per-project advisory lock + per-project sequence counter
- Gap-free sequences (advisory lock inside transaction, rollback reclaims)
- Idempotent push (idempotency keys, 24h TTL)
- Scope isolation (personal observations visible only to creator)

### SyncClient Design

```
┌──────────────── SyncClient ─────────────────┐
│                                              │
│  Push Goroutine          Pull Goroutine      │
│  ┌─────────────┐        ┌─────────────┐     │
│  │ Debounce    │        │ Ticker      │     │
│  │ (10s after  │        │ (every 120s)│     │
│  │  last write)│        │             │     │
│  └──────┬──────┘        └──────┬──────┘     │
│         │                      │             │
│         ▼                      ▼             │
│  ┌─────────────┐        ┌─────────────┐     │
│  │ PushOnce()  │        │ PullOnce()  │     │
│  │ - Lease     │        │ - Cursor    │     │
│  │ - Batch 100 │        │ - Page 500  │     │
│  │ - POST      │        │ - Apply     │     │
│  │ - ACK       │        │ - Advance   │     │
│  └─────────────┘        └─────────────┘     │
│                                              │
│  Backoff: 30s → 60s → 120s → 300s (max)    │
│  Enrollment guard: only syncs enrolled       │
│  Graceful shutdown: flush + lease release    │
└──────────────────────────────────────────────┘
```

### Large Sync Mitigation: Chunked Background Pull

Sync is an **async enrichment**, not a prerequisite. The local store works 100% while sync runs in background.

**Initial sync (seq=0)**:
- Pull loop runs in background goroutine
- Pages through entities: `GET /pull?since_seq={cursor}&limit=500`
- Each page: apply to local store → update cursor → sleep 100ms → next page
- If interrupted (app closes), resumes from last cursor on next start

**Reconnection after long offline**:
- Push first: drain pending `sync_mutations` (local changes while offline) — fast, only delta
- Pull after: resume from last cursor, page through accumulated remote changes
- Developer keeps working — reads/writes hit local SQLite, never blocked

**Key invariant**: MCP/serve commands NEVER wait for sync. SyncClient.Start() launches goroutines and returns immediately.

### Cloud Server Endpoints

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

Authentication: `Authorization: Bearer <api_key>` + `X-Engram-Protocol: 1`.

### Planned: RemoteStore

`RemoteStore` will implement `StoreInterface` by proxying every operation to the cloud server via HTTP. Stateless — no local SQLite, no caching. For teams that want a single cloud source of truth without local storage.

Write operations route through the push protocol (`POST /sync/push`) as single-mutation pushes, returning the server-assigned numeric ID. Read operations call dedicated endpoints.

### Planned: CLI Cloud Commands

```bash
engram cloud setup              # Configure cloud connection
engram cloud sync               # Manual push + pull
engram cloud status             # Show sync health, pending mutations, cursor
engram cloud enroll <project>   # Enable sync for a project
engram cloud unenroll <project> # Disable sync for a project
engram mcp --backend cloud      # Use RemoteStore (cloud-only)
engram mcp --backend local-sync # Use local store + SyncClient
```

---

## Project Skills (Planned)

Skills are **role-controlled, versioned project knowledge** stored as observations with `type: "skill"`. They represent architecture decisions, coding conventions, technology choices, and patterns that define how a project should be built.

### Why Skills?

Regular observations (bug fixes, session notes) are ephemeral — any team member can create and modify them. Skills are **governed knowledge**: only authorized roles can edit them, every change is audited, and rollback is supported.

### How They Work

Skills are observations with special policy enforcement:

```
Observation {
    type:      "skill"
    scope:     "project"
    topic_key: "skill/architecture"    ← hierarchical organization
    content:   "## Modules\n..."       ← free-form markdown
    project:   "my-api"
}
```

- **Zero schema changes for storage** — skills use the existing observations table
- **FTS5/tsvector search** — works automatically
- **Sync via push/pull** — no changes to the sync protocol
- **LWW conflict resolution** — same as any observation with topic_key

### Role Hierarchy

| Role | Level | Edit Skills | Delete Skills | Manage Members |
|------|-------|-------------|---------------|----------------|
| `viewer` | 1 | No | No | No |
| `member` | 2 | No | No | No |
| `senior` | 3 | Yes (if policy allows) | No | No |
| `lead` | 4 | Yes | Yes (if policy allows) | No |
| `owner` | 5 | Yes | Yes | Yes |

Each project can configure the minimum role required for editing and deleting skills via `project_skill_policies`.

### Mandatory Versioning

Every skill edit creates a revision before overwriting — not just on LWW conflicts. The full version history is preserved and queryable.

### Audit Log

Append-only `skill_audit_log` table records every create, edit, delete, and rollback with user, timestamp, and revision numbers.

### Rollback

Restoring a previous version creates a NEW version (history is never rewritten):

```
v1 (Juan) → v2 (Maria) → v3 (Pedro, bad edit) → v4 (rollback to v2)
```

### Server-Side Enforcement

Permission checks happen in cloudserver, not in the client. Offline developers can edit skills locally — the server accepts or rejects (403) on push. This preserves the local-first model.

Full proposal: [openspec/changes/project-skills/proposal.md](../openspec/changes/project-skills/proposal.md)

---

## Dashboard (Planned)

An embedded web UI served by the same `engram-cloud` binary. No separate frontend deployment — HTML templates and static assets are compiled into the binary via `go:embed`.

### Technology

| Component | Choice | Reason |
|-----------|--------|--------|
| Interactivity | htmx 2.x (14kb) | Partial page updates, no JS framework, no build step |
| Templates | Go `html/template` | Zero dependency, embedded in binary |
| Styling | Custom CSS (~5kb) | No Tailwind build pipeline |
| Markdown | goldmark (pure Go) | Server-side rendering for skill preview |
| Auth | Session cookie + API key | Reuses existing auth infrastructure |

### Pages

```
/dashboard/
├── /login              ← API key authentication
├── /memories           ← Search, filter, view observations
├── /skills             ← List by category, edit, revision history, rollback
├── /skills/{topic_key} ← Detail view with rendered markdown
├── /members            ← User management, role assignment per project
├── /audit              ← Skill change timeline with filters
└── /projects           ← Stats, sync health, enrolled projects
```

### Architecture

```
engram-cloud serve
    │
    ├── /api/v1/...        ← JSON API (existing)
    ├── /dashboard/...     ← HTML server-rendered (new)
    └── /static/...        ← Embedded assets via go:embed (new)
```

All dashboard handlers call the same cloudstore methods as the API — no separate data layer. The dashboard is purely a presentation layer.

### Skill Ingestion from Filesystem

```bash
engram cloud skills import ./docs/conventions/ [--project my-api]
```

Recursively scans a folder for `*.md` files, derives `topic_key` from the file path (`conventions/testing.md` → `skill/conventions/testing`), and creates/updates skills via the push protocol. Idempotent — unchanged files are skipped, changes create revisions.

Full proposal: [openspec/changes/dashboard/proposal.md](../openspec/changes/dashboard/proposal.md)

---

## Testing Strategy

| Layer | Approach | Count |
|-------|----------|-------|
| `internal/store` | Unit tests with in-memory SQLite | ~30 |
| `internal/cloudstore` | Integration tests with real PostgreSQL (docker) | 16 |
| `internal/cloudserver` | Integration tests with httptest + real PostgreSQL | 17 |
| `internal/remote` | Unit tests with httptest mock servers | 32 |
| `internal/tui` | Bubbletea teatest for key transitions and rendering | existing |

```bash
# Unit tests (fast, no external deps)
go test ./...

# Integration tests (requires PostgreSQL)
docker run -d --name engram-test-pg -p 5433:5432 \
  -e POSTGRES_USER=engram -e POSTGRES_PASSWORD=testpass \
  -e POSTGRES_DB=engram_test postgres:16-alpine

go test -tags integration -v ./internal/cloudstore/ ./internal/cloudserver/

# Coverage
go test -cover ./internal/remote/...
```

---

## External Dependencies

| Dependency | Purpose |
|------------|---------|
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO required) |
| `jackc/pgx/v5` | PostgreSQL driver (cloud store) |
| `mark3labs/mcp-go` | MCP protocol implementation (stdio transport) |
| `charmbracelet/bubbletea` | TUI framework |
| `charmbracelet/bubbles` | TUI components (textinput, viewport, table) |
| `charmbracelet/lipgloss` | TUI styling (Catppuccin Mocha theme) |
| `go-chi/chi/v5` | HTTP router (cloud server) |
| `google/uuid` | UUID generation |

---

## File Layout

```
engram/
├── cmd/
│   ├── engram/              # Primary CLI binary
│   └── engram-cloud/        # Cloud server binary
├── internal/
│   ├── types/               # Shared domain model + interfaces
│   ├── store/               # SQLite persistence (local)
│   ├── format/              # Context string formatting
│   ├── mcp/                 # MCP stdio server (15 tools)
│   ├── server/              # HTTP REST API (local)
│   ├── tui/                 # Terminal UI (Bubbletea)
│   ├── cloudserver/         # Cloud HTTP API (chi)
│   ├── cloudstore/          # Cloud PostgreSQL backend
│   ├── remote/              # Cloud sync client
│   ├── sync/                # Git file-based sync
│   ├── project/             # Project name detection
│   ├── setup/               # Agent plugin installer
│   ├── version/             # GitHub release checker
│   └── obsidian/            # Obsidian vault export (beta)
├── plugin/
│   ├── claude-code/         # Claude Code plugin config
│   ├── opencode/            # OpenCode plugin config
│   └── obsidian/            # Obsidian integration assets
├── openspec/                # SDD planning artifacts
│   └── changes/
│       └── cloud-sync-phase3/
│           ├── proposal.md
│           ├── spec.md
│           ├── design.md
│           └── tasks.md
├── docs/                    # Documentation
│   ├── ARCHITECTURE.md      # User-facing architecture guide
│   ├── SYSTEM-ARCHITECTURE.md  # This document
│   ├── INSTALLATION.md
│   ├── AGENT-SETUP.md
│   ├── PLUGINS.md
│   └── COMPARISON.md
└── assets/                  # Images and static files
```

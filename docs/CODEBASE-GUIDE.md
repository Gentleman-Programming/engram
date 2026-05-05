[← Back to README](../README.md)

# Engram Codebase Guide

**This guide is for maintainers and contributors who need to understand where Engram responsibilities live, which invariants are non-negotiable, and which file to open when something needs to change.**

Engram is a local-first persistent memory system for coding agents. The center of the product is a Go binary that writes to SQLite + FTS5; the CLI, MCP, HTTP API, TUI, plugins, sync, cloud, and dashboard are interfaces around that core.

## 90-second mental model

```text
Coding agent
  Claude Code / OpenCode / Gemini CLI / Codex / VS Code / Antigravity / Cursor / Windsurf
        │
        │ MCP stdio, plugin hooks, or local API
        ▼
cmd/engram
  CLI + local runtime + cloud runtime
        │
        ├── internal/mcp        mem_* tools for agents
        ├── internal/server     local JSON API: engram serve
        ├── internal/tui        Bubbletea terminal UI
        ├── internal/setup      automated integration installation
        │
        ▼
internal/store
  SQLite + FTS5 + sessions + observations + prompts + relations + sync mutations
        │
        ├── internal/sync                   git-friendly chunks / transport
        └── internal/cloud/autosync         optional background push/pull
                │
                ▼
        internal/cloud/remote ── HTTP ── engram cloud serve
                                      │
                                      ├── internal/cloud/cloudserver
                                      ├── internal/cloud/cloudstore  Postgres
                                      ├── internal/cloud/dashboard   HTML/HTMX
                                      └── internal/cloud/auth        bearer + dashboard session
```

The sentence that organizes the whole repo:

> **Local SQLite is the source of truth. Cloud is opt-in replication and shared access, not the owner of the data.**

## Recommended reading path

| Step | Page | Read this when... |
|---|---|---|
| 1 | [Mental Model](codebase/mental-model.md) | You need the product shape before opening code. |
| 2 | [Repository Map](codebase/repository-map.md) | You need to decide which package owns a change. |
| 3 | [Memory Core](codebase/memory-core.md) | You are touching SQLite, FTS5, prompts, observations, sessions, relations, or sync mutations. |
| 4 | [Interfaces](codebase/interfaces.md) | You are changing CLI, MCP, local API, or TUI behavior. |
| 5 | [Sync and Cloud](codebase/sync-and-cloud.md) | You are changing chunks, remote sync, autosync, transport, or cloud persistence. |
| 6 | [Dashboard](codebase/dashboard.md) | You are changing browser UI, HTMX partials, cloud dashboard routes, or dashboard policy controls. |
| 7 | [Integrations](codebase/integrations.md) | You are changing agent setup, plugins, or MCP configuration docs. |
| 8 | [Maintainer Playbook](codebase/maintainer-playbook.md) | You are reviewing a PR or planning a large change. |
| 9 | [Reference Map](codebase/reference-map.md) | You need a traceable appendix from docs/source files to purpose. |

## Quick map: if you need X, read Y

| If you need to... | Open first | Then check |
|---|---|---|
| Understand the product in 5 minutes | `README.md` | [Mental Model](codebase/mental-model.md) |
| See the full technical reference | `DOCS.md` | This guide for ownership and guardrails |
| Change MCP tools | `internal/mcp/mcp.go` | [Interfaces](codebase/interfaces.md), `internal/mcp/*_test.go`, `docs/AGENT-SETUP.md` |
| Change local persistence | `internal/store/store.go` | [Memory Core](codebase/memory-core.md), `internal/store/*_test.go`, `DOCS.md#database-schema` |
| Change the local API | `internal/server/server.go` | [Interfaces](codebase/interfaces.md), `internal/server/*_test.go`, `DOCS.md#http-api-endpoints` |
| Change chunk sync | `internal/sync/sync.go` | [Sync and Cloud](codebase/sync-and-cloud.md), `internal/sync/*_test.go`, `README.md#git-sync` |
| Change cloud autosync | `internal/cloud/autosync/manager.go` | [Sync and Cloud](codebase/sync-and-cloud.md), `internal/cloud/remote/transport.go`, `DOCS.md#cloud-autosync` |
| Change the dashboard | `internal/cloud/dashboard/dashboard.go` | [Dashboard](codebase/dashboard.md), `internal/cloud/dashboard/*_templ.go`, `internal/cloud/dashboard/static/styles.css` |
| Change agent setup | `internal/setup/setup.go` | [Integrations](codebase/integrations.md), `plugin/`, `docs/AGENT-SETUP.md`, `docs/PLUGINS.md` |
| Prepare or review a large feature | `openspec/changes/*` | [Maintainer Playbook](codebase/maintainer-playbook.md), `openspec/specs/*`, `CONTRIBUTING.md` |

## Full technical reference stays in DOCS.md

This guide explains ownership, flows, and guardrails. It intentionally does **not** duplicate the complete API reference. For endpoints, schemas, MCP parameters, and CLI flags, use [DOCS.md](../DOCS.md).

---

[Next: Mental Model →](codebase/mental-model.md)

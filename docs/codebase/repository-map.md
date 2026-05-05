[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Mental Model](mental-model.md) | [Next: Memory Core →](memory-core.md)

# Repository Map

**Use this page to place behavior in the right owner before writing code.** Most Engram mistakes start when SQL, HTTP, UI, sync, and plugin responsibilities drift into the same file.

## Quick map

| If you need to... | Open first | Then check |
|---|---|---|
| Understand the product in 5 minutes | `README.md` | `docs/ARCHITECTURE.md` |
| See the full technical reference | `DOCS.md` | This guide for ownership and guardrails |
| Change MCP tools | `internal/mcp/mcp.go` | `internal/mcp/*_test.go`, `docs/AGENT-SETUP.md` |
| Change local persistence | `internal/store/store.go` | `internal/store/*_test.go`, `DOCS.md#database-schema` |
| Change the local API | `internal/server/server.go` | `internal/server/*_test.go`, `DOCS.md#http-api-endpoints` |
| Change chunk sync | `internal/sync/sync.go` | `internal/sync/*_test.go`, `README.md#git-sync` |
| Change cloud autosync | `internal/cloud/autosync/manager.go` | `internal/cloud/remote/transport.go`, `DOCS.md#cloud-autosync` |
| Change cloud transport | `internal/cloud/cloudserver/cloudserver.go` | `internal/cloud/cloudstore/cloudstore.go`, `docs/engram-cloud/README.md` |
| Change the dashboard | `internal/cloud/dashboard/dashboard.go` | `internal/cloud/dashboard/*_templ.go`, `internal/cloud/dashboard/static/styles.css` |
| Change cloud/dashboard auth | `internal/cloud/auth/auth.go` | `cmd/engram/cloud.go`, `internal/cloud/cloudserver/cloudserver.go` |
| Change agent setup | `internal/setup/setup.go` | `plugin/`, `docs/AGENT-SETUP.md`, `docs/PLUGINS.md` |
| Change project detection | `internal/project/detect.go` | `internal/project/similar.go`, `docs/AGENT-SETUP.md` |
| Change the TUI | `internal/tui/model.go` | `internal/tui/update.go`, `internal/tui/view.go`, `internal/tui/styles.go` |
| Change Obsidian | `internal/obsidian/` | `plugin/obsidian/`, `docs/beta/obsidian-brain.md` |
| Prepare a large feature | `openspec/changes/*` | `openspec/specs/*`, `CONTRIBUTING.md` |

## Package ownership

| Area | Responsibility | Should not do |
|---|---|---|
| `cmd/engram` | CLI parsing, runtime composition, package wiring, flags, user commands. | Put SQL or deep business rules here. |
| `internal/store` | Local source of truth: SQLite, FTS5, sessions, observations, prompts, relations, mutations, dedupe, topic upserts, local diagnostics. | Render HTML, talk directly to cloud HTTP, decide UX. |
| `internal/mcp` | Expose MCP tools, resolve project by cwd, profile tools (`agent`, `admin`, `all`), translate agent calls into store operations. | Persist outside the store or duplicate store logic. |
| `internal/server` | Local JSON API (`engram serve`), local endpoints, autosync notification after writes. | Expose cloud/dashboard routes or use Postgres. |
| `internal/sync` | Chunk export/import, manifest, abstract transport, sync bootstrap/upgrade. | Decide cloud auth or render the dashboard. |
| `internal/cloud/chunkcodec` | Shared canonicalization of chunks, IDs, and mutation payload decoding used by sync/cloud. | Decide transport, persistence, or sync policies. |
| `internal/cloud/remote` | HTTP client to cloud for chunks/mutations. | Store local state directly except through expected interfaces. |
| `internal/cloud/autosync` | Orchestrate background push/pull with leases, backoff, cursors, and degraded state. | Implement HTTP transport or concrete SQL queries. |
| `internal/cloud/cloudserver` | Cloud HTTP runtime: `/sync/*`, auth boundary, dashboard mount, server-side enforcement. | Store local-first data or mix HTML/SQL in handlers. |
| `internal/cloud/cloudstore` | Cloud persistence in Postgres, chunks, mutations, dashboard read model, controls, audit. | Decide HTTP routes or browser interaction. |
| `internal/cloud/dashboard` | Server-rendered browser UI, `/dashboard/*` routes, HTMX, templ components, navigation. | Enforce policies only visually; if it is a real rule, it must reach server/cloudstore. |
| `internal/cloud/auth` | Bearer token, project-scope authorizer, signed dashboard sessions. | Create product rules outside its auth contract. |
| `internal/project` | Project detection and normalization. | Access the store to correct data. |
| `internal/setup` | Idempotent installation of agent integrations. | Orchestrate automatic cloud enrollment/login if it is not implemented and documented. |
| `plugin/` | Thin adapters per agent/host. | Contain core behavior that should live in Go. |
| `skills/` | Guardrails for contributor agents. | Replace specs, tests, or code. |
| `docs/` | Usage, architecture, cloud, plugin, installation, and doctor guides. | Document unimplemented aspirations. |
| `openspec/` | Per-change proposals, specs, designs, and tasks. | Act as end-user documentation. |

## Golden placement rule

```text
Is it local persistence?         → internal/store
Is it a local HTTP contract?     → internal/server
Is it a tool for agents?         → internal/mcp
Is it chunk/export/import?       → internal/sync
Is it chunk canonicalization?    → internal/cloud/chunkcodec
Is it a cloud client?            → internal/cloud/remote
Is it background orchestration?  → internal/cloud/autosync
Is it cloud/server transport?    → internal/cloud/cloudserver
Is it Postgres/cloud read model? → internal/cloud/cloudstore
Is it a browser screen?          → internal/cloud/dashboard
Is it auth/session?              → internal/cloud/auth
Is it agent installation?        → internal/setup
```

If a change needs two areas, separate the contract: for example, a dashboard toggle that pauses sync should have UI in `dashboard`, enforcement in `cloudserver`, state in `cloudstore`, and tests crossing that boundary.

---

[← Previous: Mental Model](mental-model.md) | [Next: Memory Core →](memory-core.md)

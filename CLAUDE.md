# Engram — Persistent Memory System
> Gentleman Living Architecture | Canvas: inline below

## Methodology: Gentleman Living Architecture
Workflow: Canvas → Orient → Plan → Delegate → Synthesize → Judge → PCD Loop → Hand Off

## Canvas
- **Problema**: AI agents lose ALL context between sessions. Every conversation starts from zero.
- **Usuario**: Allan Guerrero (primary) + any developer using Claude Code, OpenCode, Gemini, or Codex.
- **Solución**: CLI tool + MCP server that gives AI agents persistent memory across sessions via SQLite + FTS5.
- **Fuera de scope**: Not a database, not a knowledge graph, not a RAG system. It's a MEMORY system — save, search, recall.

## Rules
1. **Go idioms**: table-driven tests, error wrapping, no globals
2. **Plugin boundary**: plugins are THIN wrappers — business logic stays in `internal/`
3. **Never break MCP protocol**: tool schemas are a public contract
4. **Topic key semantics**: same topic evolving → same key (upsert). Different topics → different keys
5. **FTS5 safety**: ALWAYS sanitize user input before MATCH queries (wrap terms in quotes)
6. **PCD Loop (mandatory)**: After every task → Prevent (neurona?), Codify (skill?), Delegate (engram saved?)

## Stack
| Component | Technology | Purpose |
|-----------|-----------|---------|
| Language | Go 1.22+ | Core CLI + server |
| Database | SQLite + FTS5 | Local persistent storage |
| TUI | Bubbletea + Lipgloss | Terminal UI |
| Dashboard | HTMX + server-rendered HTML | Browser UI |
| MCP | JSON-RPC over stdio | AI agent integration |
| Plugins | Claude, OpenCode, Gemini, Codex | Per-agent wrappers |
| Tests | Go testing + teatest | Unit + TUI tests |

## Scopes
| Scope | Path | Responsibility |
|-------|------|----------------|
| CLI | `cmd/engram/` | Entry point, commands |
| Store | `internal/store/` | SQLite, FTS5, CRUD |
| Server | `internal/server/` | MCP JSON-RPC server |
| TUI | `internal/tui/` | Bubbletea screens |
| Dashboard | `internal/dashboard/` | HTMX browser UI |
| Plugins | `plugin/` | Thin agent wrappers |

## Skills (20 project skills in AGENTS.md)
See `AGENTS.md` for full skill routing table.

## Current State
- **Version**: latest commits show project name drift fixes, MCP tool deferral, FTS5 topic_key indexing
- **Health**: Active development, Go tests exist (`go test ./...`)
- **Deploy**: Local CLI tool installed via `go install` or homebrew
- **Open source**: Apache-2.0 license, CONTRIBUTING.md, CODEOWNERS present

## Vault Reference (Biblioteca de Conocimiento)

Cuando algo falle con una herramienta del ecosistema, PRIMERO consultar:
`C:\Users\iUser\repos\claude-workspace\vault\{herramienta}\AGENT.md`

Bibliotecas disponibles: vercel, supabase, n8n, google-sheets, docker-swarm,
claude-code, windows, nextjs, engram-memory, powershell, inmoautos, villas,
ios-apple, telegram, traefik, wordpress.

Si el error es nuevo, agregarlo al catalogo despues de resolverlo.

## Gotchas
- FTS5 MATCH syntax ≠ LIKE — special chars crash queries if not sanitized
- `mem_search` results are TRUNCATED — always follow with `mem_get_observation` for full content
- Project name fragmentation: 28 different names in Engram for 1 ecosystem — use canonical names only
- Plugin startup hook injects protocol into every session — keep it LEAN


## Vault Reference (Biblioteca de Conocimiento)

Cuando algo falle con una herramienta del ecosistema, PRIMERO consultar:
`C:\Users\iUser\repos\claude-workspace\vault\{herramienta}\AGENT.md`

Bibliotecas disponibles: vercel, supabase, n8n, google-sheets, docker-swarm,
claude-code, windows, nextjs, engram-memory, powershell, inmoautos, villas,
ios-apple, telegram, traefik, wordpress.

Si el error es nuevo, agregarlo al catalogo despues de resolverlo.


# BATON — Engram Session Handoff
> Read this FIRST before any work.

## Last Update: 2026-04-02

### Current State
- Active development, recent commits: project name drift auto-detection, MCP tool deferral, FTS5 topic_key indexing
- 1,469+ observations across 44 sessions in ag-workspace alone
- 28 project name fragmentation issue KNOWN — canonical names list defined in GLA methodology
- MCP server working: Claude Code, OpenCode, Gemini, Codex plugins
- Go tests exist — this is one of the FEW projects with actual test infrastructure

### What's Working
- CLI: save, search, context, session_summary, get_observation — all functional
- MCP server: all tools operational, 4 deferred for startup performance
- FTS5 search with topic_key indexing
- Session summaries and handoff protocols
- Plugin system (Claude, OpenCode, Gemini, Codex)

### Pending
- P1: Project name normalization migration (auto-detection built, migration pending)
- P1: Dashboard HTMX improvements
- P2: Neurona auto-injection at session start (protocol defined in session-handoff skill)
- P2: mem_delete tool (currently no way to delete observations)

### Gotchas
| Gotcha | Severity | Notes |
|--------|----------|-------|
| FTS5 special chars crash MATCH | HIGH | Wrap terms in quotes before query |
| Search results truncated at 300 chars | MEDIUM | Always call mem_get_observation for full content |
| 28 project names for 1 ecosystem | HIGH | Use canonical names from GLA methodology |
| Plugin hook inflates session context | MEDIUM | Keep startup hook LEAN |

### For Next Session
1. Read this BATON.md
2. Check AGENTS.md for skill routing
3. Run `go test ./...` to verify nothing is broken
4. Check GitHub issues for open work

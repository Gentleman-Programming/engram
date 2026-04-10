# Roadmap — engram

> **Last updated**: 2026-04-09 (Session 9 — initial creation by RoadmapBuilder)
> **Source of truth**: this file. Everything else is derived.
> **Language exception**: U-18 does not apply — public GitHub project, English OK

## 1. Vision in one sentence

Persistent memory for AI agents. Go binary with SQLite + FTS5 backend. Open source under Gentleman Programming org.

## 2. Current status

**Status**: Producción
**Since**: 2025-12
**Reason**: Core infrastructure for the GLA methodology — memory across sessions.

## 3. Completed milestones

- [x] Go binary with MCP server
- [x] SQLite + FTS5 search backend
- [x] Topic key semantics (upsert same topic, new topic = new key)
- [x] Plugin boundary (thin wrappers)
- [x] Table-driven tests
- [x] Session 9: applied GLA Canonical U1-U18 (English exception, U-18 NOT applicable for public lib)

## 4. In progress

- [ ] **FTS5 safety improvements** (ongoing)
  - Sanitize user input before MATCH queries

## 5. Upcoming milestones (P0/P1/P2)

### P0 (critical)
- (none right now)

### P1 (important)
- [ ] Better embedding support for Spanish (Rioplatense keyword mismatch detected in Session 9 — Judge A + B reported 9-11 queries with 0 hits due to fuzzy matching)
- [ ] MCP protocol stability (public contract — never break)

### P2 (nice to have)
- [ ] Multi-language embeddings support
- [ ] Export/import tools

## 6. Out of scope

- NO break MCP protocol compatibility
- NO introduce business logic to plugins (stay thin)

## 7. Cross-project dependencies

| Project | What this provides | What the other provides | Status |
|---------|--------------------|--------------------------|--------|
| claude-workspace | Primary consumer | All methodology relies on Engram | ✓ |
| gentle-ai | Shared infrastructure | Both are core Gentleman tools | ✓ |

## 8. Last update

- **Date**: 2026-04-09
- **Session**: claude-workspace Session 9
- **Chat**: RoadmapBuilder (manual generation via script after agent crashed with 529 overloaded)
- **Changes**: Initial roadmap creation based on CLAUDE.md + BATON.md + git log + Session 9 GLA Canonical propagation

---
name: engram-knowledge-recall
description: >
  Multi-source knowledge retrieval chain. Ensures the agent never claims
  "no context" without searching both Engram (live memory) and an external
  knowledge base such as Obsidian. Trigger: any context retrieval, recall
  request, session start, compaction recovery, or starting work on a topic
  the agent has no context on.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

- Any time the agent needs context about past work
- User says "remember", "recall", "what did we do", "how did we solve"
- Starting work on a topic with no prior context
- After compaction — recovering what was lost
- First message of a session references a project, feature, or problem

---

## Prerequisites

This skill requires TWO MCP tool sources in the agent's environment:

1. **Engram** (live memory) — provides:
   - `mem_context` — recent session history
   - `mem_search` — FTS5 full-text search across all sessions
   - `mem_get_observation` — full content of a specific memory

2. **A knowledge-base MCP** (curated library) — any server that provides:
   - A tool to **search notes** by content, tags, or metadata
   - A tool to **read a note's** full content

   Known compatible implementations:

   | MCP Server | Search tool | Read tool |
   |------------|-------------|-----------|
   | obsidian-mcp | `obsidian_search_notes` | `obsidian_read_note` |
   | obsidian-local-rest-api | `search` | `read` |
   | notion-mcp | `search_pages` | `get_page` |
   | Any file-based MCP | `search_files` | `read_file` |

If only Engram is available, the chain stops at Level 2. The agent must still
state explicitly that no external knowledge base was searched.

---

## Retrieval Chain (mandatory order)

When the agent needs context, follow this chain **in order**. Do NOT skip levels.

### Level 1 — Engram live memory (fast, cheap)

```
mem_context(project)
```

If this returns useful context → **use it, stop here**.

### Level 2 — Engram deep search (FTS5, broader)

```
mem_search(query, project?, since?, until?)
```

If results found → use `mem_get_observation(id)` for full content → **stop here**.

### Level 3 — External knowledge base (curated library)

Only reach this level if Levels 1 and 2 returned nothing useful.

```
<search_tool>(query)           → find relevant notes
<read_tool>(filename)          → read full content
```

Where `<search_tool>` and `<read_tool>` are the tools from your knowledge-base
MCP (see Prerequisites table).

If the knowledge base returns useful context:
1. **Use it** to answer the user
2. **Rehidrate**: call `mem_save` with a summary of what was found, so future
   searches at Level 1-2 will find it without needing Level 3 again

```
mem_save(
  title: "Recalled: <topic>",
  type: "discovery",
  content: "<summary of what was found in the knowledge base>",
  topic_key: "<appropriate key>"
)
```

### Level 4 — No context found

Only after exhausting all 3 levels, the agent may state:

> "No prior context found in live memory or knowledge base."

**NEVER say "I don't have context" after only checking Levels 1-2.**

---

## Save Classification

When the agent saves a memory, it should consider the memory's lifecycle:

| Type | Where it lives | Example |
|------|---------------|---------|
| Session context | Engram only | "User prefers tabs over spaces" |
| Bug fix | Engram only | "Fixed N+1 in user list query" |
| Architecture decision | Engram + knowledge base | "Chose SQLite over Postgres for local storage" |
| Reference documentation | Engram + knowledge base | "API rate limits: 100 req/min per token" |

Memories that belong in both places get there automatically:
- `mem_save` writes to Engram immediately
- `engram obsidian-export` (or equivalent sync) mirrors to the knowledge base

The agent does NOT need to write to the knowledge base directly.

---

## Rules

1. **Never skip Level 3.** If Engram returns nothing, search the knowledge base before giving up.
2. **Always rehidrate.** When Level 3 finds something, save a summary back to Engram.
3. **Engram first.** Always search Engram before the knowledge base — it is faster and has the freshest data.
4. **No silent failures.** If the knowledge-base MCP is not available, state it explicitly: "Knowledge base MCP not available, searched Engram only."
5. **Date-scoped searches.** When the user references a specific time ("yesterday", "last week"), use `since`/`until` parameters in `mem_search` and `mem_sessions`.
6. **Agent-agnostic.** This chain works identically across Claude Code, OpenCode, Gemini CLI, Codex, or any MCP-compatible agent.

---

## Compaction Recovery (extends memory-protocol)

After compaction, the retrieval chain becomes critical:

1. `mem_session_summary` — persist what was done before compaction
2. `mem_context` — Level 1 recovery
3. If context is insufficient → `mem_search` — Level 2 recovery
4. If still insufficient → knowledge-base search — Level 3 recovery
5. Only then continue working

This ensures compaction never causes permanent context loss.

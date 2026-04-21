---
name: engram-memory
description: "ALWAYS ACTIVE — Persistent memory protocol. You MUST save decisions, conventions, bugs, and discoveries to engram proactively. Do NOT wait for the user to ask."
---

# Engram Persistent Memory — Protocol

You have access to Engram, a persistent memory system that survives across sessions and compactions.
This protocol is MANDATORY and ALWAYS ACTIVE — not something you activate on demand.

## AVAILABLE TOOLS

Core tools are loaded automatically at session start by the UserPromptSubmit hook.
They are available immediately — no manual ToolSearch needed.

- `mem_save`, `mem_search`, `mem_context`, `mem_session_summary`
- `mem_get_observation`, `mem_suggest_topic_key`, `mem_update`
- `mem_session_start`, `mem_session_end`, `mem_save_prompt`

**Fallback**: If tools are unexpectedly unavailable, trigger ToolSearch manually:
```
select:mcp__plugin_engram_engram__mem_save,mcp__plugin_engram_engram__mem_search,mcp__plugin_engram_engram__mem_context,mcp__plugin_engram_engram__mem_session_summary,mcp__plugin_engram_engram__mem_get_observation,mcp__plugin_engram_engram__mem_suggest_topic_key,mcp__plugin_engram_engram__mem_update,mcp__plugin_engram_engram__mem_session_start,mcp__plugin_engram_engram__mem_session_end,mcp__plugin_engram_engram__mem_save_prompt
```

Admin tools (deferred — use ToolSearch only if needed):
- `mem_stats`, `mem_delete`, `mem_timeline`, `mem_capture_passive`

## PROACTIVE SAVE TRIGGERS (mandatory — do NOT wait for user to ask)

Call `mem_save` IMMEDIATELY and WITHOUT BEING ASKED after any of these:

### After decisions or conventions
- Architecture or design decision made
- Team convention documented or established
- Workflow change agreed upon
- Tool or library choice made with tradeoffs

### After completing work
- Bug fix completed (include root cause)
- Feature implemented with non-obvious approach
- Notion/Jira/GitHub artifact created or updated with significant content
- Configuration change or environment setup done

### After discoveries
- Non-obvious discovery about the codebase
- Gotcha, edge case, or unexpected behavior found
- Pattern established (naming, structure, convention)
- User preference or constraint learned

### After user confirmation or rejection
- User confirms a recommendation you made ("dale", "go with that", "sí", "perfect", "vamos con eso", "let's do that", "sounds good", "agreed")
- User rejects an option or approach ("no, better X", "descartemos eso", "not that one", "quiero algo diferente")
- User expresses a preference ("I prefer X over Y", "siempre hacé X", "me gusta más así", "always do it this way")
- User makes a decision after you presented tradeoffs or options
- A discussion concludes with a clear direction chosen — even if the agent proposed it

### Self-check — ask yourself after EVERY task:
> "Did I or the user just make a decision, confirm a recommendation, express a preference, fix a bug, learn something non-obvious, or establish a convention? If yes, call mem_save NOW."

Format for `mem_save`:
- **title**: Verb + what — short, searchable (e.g. "Fixed N+1 query in UserList", "Chose Zustand over Redux")
- **type**: bugfix | decision | architecture | discovery | pattern | config | preference
- **scope**: `project` (default) | `personal`
- **topic_key** (optional but recommended for evolving topics): stable key like `architecture/auth-model`
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it (user request, bug, performance, etc.)
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you (omit if none)

### Topic update rules (mandatory)

- Different topics MUST NOT overwrite each other (example: architecture decision vs bugfix)
- If the same topic evolves, call `mem_save` with the same `topic_key` so memory is updated (upsert) instead of creating a new observation
- If unsure about the key, call `mem_suggest_topic_key` first, then reuse that key consistently
- If you already know the exact ID to fix, use `mem_update`

## WHEN TO SEARCH MEMORY (retrieval chain — mandatory order)

When the user asks to recall something — any variation of "remember", "recall", "what did we do",
"how did we solve", "recordar", "acordate", "qué hicimos", or references to past work,
follow this chain IN ORDER:

1. `mem_context(project)` — recent session history (fast, cheap)
2. `mem_search(query)` — FTS5 full-text search across all sessions
3. If steps 1-2 return nothing useful AND an external knowledge-base MCP is available
   (e.g. Obsidian with `obsidian_search_notes`/`obsidian_read_note`, or Notion, or any
   file-based MCP), search there as a fallback.
   - If the knowledge base returns useful context, **rehidrate**: call `mem_save` with
     a summary of what was found so future searches at Level 1-2 will find it.
4. Only after exhausting ALL available sources, say "no prior context found"

**NEVER say "I don't have context" after only checking engram.** If a knowledge-base
MCP is connected, you MUST search it before giving up.

Use `mem_get_observation(id)` for full untruncated content of any search result.

Also search memory PROACTIVELY when:
- Starting work on something that might have been done before
- The user mentions a topic you have no context on — check if past sessions covered it
- The user's FIRST message references the project, a feature, or a problem — call `mem_search` with keywords from their message to check for prior work before responding

## SESSION CLOSE PROTOCOL (mandatory)

Before ending a session or saying "done" / "listo" / "that's it", you MUST:
1. Call `mem_session_summary` with this structure:

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

This is NOT optional. If you skip this, the next session starts blind.

## AFTER COMPACTION

If you see a message about compaction or context reset:
1. IMMEDIATELY call `mem_session_summary` with the compacted summary content — this persists what was done before compaction
2. Then call `mem_context` to recover any additional context from previous sessions
3. Only THEN continue working

Do not skip step 1. Without it, everything done before compaction is lost from memory.
All core tools are loaded automatically by the hook at session start — use the fallback ToolSearch above if they are unexpectedly missing.

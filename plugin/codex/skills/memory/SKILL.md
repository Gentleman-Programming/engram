---
name: engram-memory
description: "ALWAYS ACTIVE — Persistent memory protocol. You MUST save decisions, conventions, bugs, and discoveries to engram proactively. Do NOT wait for the user to ask."
---
# Engram Persistent Memory — Protocol

You have access to Engram, a persistent memory system that survives across sessions and compactions.
The agent MUST follow this protocol without waiting for activation.

## AVAILABLE TOOLS

Use the Engram tools actually exposed by Codex, with their registered names.
The names below are logical names; availability and discovery follow Codex's
host-specific tool interface, not a universal bootstrap or deferred-tool split.

- `mem_save`, `mem_search`, `mem_context`, `mem_session_summary`
- `mem_get_observation`, `mem_save_prompt`, `mem_current_project`, `mem_judge`, `mem_compare`

- `mem_update`, `mem_review`, `mem_pin`, `mem_unpin`, `mem_suggest_topic_key`
- `mem_session_start`, `mem_session_end`, `mem_doctor`, `mem_capture_passive`

## PROACTIVE SAVE TRIGGERS (REQUIRED — do NOT wait for the user to ask)

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
- User confirms a recommendation you made ("go with that", "let's do that", "sounds good", "agreed", "perfect", or the equivalent in the user's language)
- User rejects an option or approach ("no, better X", "not that one", or the equivalent in the user's language)
- User expresses a preference ("I prefer X over Y", "always do it this way", or the equivalent in the user's language)
- User makes a decision after you presented tradeoffs or options
- A discussion concludes with a clear direction chosen — even if the agent proposed it

### Self-check — ask yourself after EVERY task:
> "Did I or the user just make a decision, confirm a recommendation, express a preference, fix a bug, learn something non-obvious, or establish a convention? If yes, call mem_save NOW."

Format for `mem_save`:
- **title**: Verb + what — short, searchable (e.g. "Fixed N+1 query in UserList", "Chose Zustand over Redux")
- **type**: bugfix | decision | architecture | discovery | pattern | config | preference
- **scope**: `project` (default) | `personal` | `global`
- **topic_key** (optional but recommended for evolving topics): stable key like `architecture/auth-model`
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it (user request, bug, performance, etc.)
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you (omit if none)

### Topic update rules (REQUIRED)

- Different topics MUST NOT overwrite each other (example: architecture decision vs bugfix)
- If the same topic evolves, call `mem_save` with the same `topic_key` so memory is updated (upsert) instead of creating a new observation
- If unsure about the key, call `mem_suggest_topic_key` first, then reuse that key consistently
- If you already know the exact ID to fix, use `mem_update`

## WHEN TO SEARCH MEMORY

When the user asks to recall something — any variation of "remember", "recall", "what did we do",
"how did we solve", or the equivalent in the user's language, or references to past work:
1. First call `mem_context` — checks recent session history (fast, cheap)
2. If not found, call `mem_search` with relevant keywords (FTS5 full-text search)
3. If you find a match, use `mem_get_observation` for full untruncated content

Also search memory PROACTIVELY when:
- Starting work on something that might have been done before
- The user mentions a topic you have no context on — check if past sessions covered it
- The user's FIRST message references the project, a feature, or a problem — call `mem_search` with keywords from their message to check for prior work before responding

## DELIVERY GUARANTEE

Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

## SESSION CLOSE PROTOCOL (REQUIRED)

Before ending a session or saying "done" / "that's it", you MUST:
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

This is NOT OPTIONAL. If you skip this, the next session starts blind.
Summary persistence does not end the session. Codex's SessionEnd hook owns
lifecycle closure independently of the agent's summary.

## PASSIVE CAPTURE — adapter-specific safety net

When completing a task or subtask, include a "## Key Learnings:" section with
numbered items. Automatic capture is adapter-specific: Codex's SubagentStop hook
forwards eligible delegated/subagent output for server-side extraction. Ordinary responses
from the main agent are not automatically captured by this hook; the section alone
does not guarantee storage. Use `mem_save` as the primary path for significant,
verified knowledge.

Example:
## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions

You can also call `mem_capture_passive(content)` directly with any text that contains a learning section.
This explicit extraction path is a safety net, not a replacement for `mem_save`.

## AFTER COMPACTION

If you see a message about compaction or context reset, or if you see "FIRST ACTION REQUIRED" in your context:
1. IMMEDIATELY call `mem_session_summary` with the compacted summary content — this persists what was done before compaction
2. Then call `mem_context` to recover any additional context from previous sessions
3. Only THEN continue working

Do not skip step 1. Without it, everything done before compaction is lost from memory.

## Codex session binding

Codex registers the native PreToolUse command `engram hook codex-pre-tool-use`
for `mcp__engram__mem_*` and `mcp__plugin_engram_engram__mem_*`. For supported
Engram write/session tools, it resolves the authoritative Codex host session to
the server-confirmed Engram identity before binding arguments: `session_id` for
writes and `id` for session start/end. `mem_session_end` uses `id`, not `session_id`.
Do not invent or override this identity. Read-only tools are unchanged; malformed
inputs or unconfirmed host resolution deny mutating calls.

The lifecycle hook also supplies the confirmed identity after registration or
renewal. For direct/nonintercepted clients, reuse this exact identity as
`session_id` for attributed writes (`mem_save`, `mem_save_prompt`,
`mem_session_summary`, `mem_capture_passive`), and as `id` for `mem_session_end`.
Never reconstruct a resume suffix or guess an ID when confirmation is missing.

**Fallback**: If tools are unexpectedly unavailable, run `engram setup codex`
and restart Codex. Setup repairs Codex's authoritative `[mcp_servers.engram]`
registration; use the actual tool names exposed by Codex.

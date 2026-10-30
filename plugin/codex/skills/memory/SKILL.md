---
name: engram-memory
description: "ALWAYS ACTIVE — Selective persistent memory: preserve verified, durable knowledge worth reconstructing, with safe topic updates and host-owned lifecycle."
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

## SELECTIVE MEMORY

Persist verified knowledge that is durable and costly to reconstruct: decisions with rationale,
non-obvious root causes, real constraints, and resumption-relevant pending work.
Group related conclusions deliberately, not every task, response, or event.
Exclude secrets, private data, speculation, raw dumps, and routine progress.

Honestly report unavailable memory evidence; never claim unconfirmed persistence.
Do not blindly repeat writes. Complete unrelated work even if bookkeeping fails.
Do not add per-event learning sections or ceremonial delivery blocks.

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

- Use mem_search on the bounded topic in the intended project and scope before writing.
- Read candidates in full with mem_get_observation; snippets are not identity evidence.
- For the same bounded topic use mem_update by ID: keep topic_key, preserve every still-valid fact, and record superseded facts as history.
- For a genuinely new topic use mem_save with a deliberate, distinct topic_key. If already covered, write nothing.
- On ambiguous identity or failed lookup, do not overwrite or fall back to a duplicate save. A failed read is not evidence of absence.
- A suggested topic_key is a hint, not proof of identity. Never merge across projects or scopes.

## WHEN TO SEARCH MEMORY

When the user asks to recall something — any variation of "remember", "recall", "what did we do",
"how did we solve", or the equivalent in the user's language, or references to past work:
1. First call `mem_context` — checks recent session history (fast, cheap)
2. If not found, call `mem_search` with relevant keywords (FTS5 full-text search)
3. If you find a match, use `mem_get_observation` for full untruncated content

Search when materially useful for recall or related work; mere project mentions do not require a search.
Narrow by project/type after broad recall as needed.

## DELIVERY GUARANTEE

Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

## SESSION CLOSE PROTOCOL (REQUIRED)

At significant explicit closure or handoff, call mem_session_summary with Goal, Instructions,
Discoveries, Accomplished, Next Steps, Relevant Files. Ordinary task completion does not require a summary.
Summary persistence does not end the session. Codex's SessionEnd hook owns
lifecycle closure independently of the agent's summary.

## PASSIVE CAPTURE — adapter-specific safety net

Automatic capture is adapter-specific: Codex's SubagentStop hook
forwards eligible delegated/subagent output for server-side extraction. Ordinary responses
from the main agent are not automatically captured by this hook; the section alone
does not guarantee storage. Use `mem_save` as the primary path for significant,
verified knowledge.

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

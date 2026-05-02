"""
Engram — Hermes Agent Plugin

Thin layer that connects Hermes Agent's event hooks to the Engram Go binary.
The Go binary runs as a local HTTP server and handles all persistence.

Flow:
    Hermes events → this plugin → HTTP calls → engram serve → SQLite

Hooks:
    pre_llm_call     → inject memory instructions into system prompt
    post_tool_call   → passive capture after tool execution
"""

import logging
import os

from . import schemas, tools

logger = logging.getLogger(__name__)

# Memory instructions injected into the system prompt so the agent
# always knows about the Engram protocol, even after compaction.
MEMORY_INSTRUCTIONS = """

## Engram Persistent Memory — Protocol

You have access to Engram, a persistent memory system that survives across sessions and compactions.
This is NOT optional — use it proactively.

### WHEN TO SAVE (mandatory — call IMMEDIATELY after these events)

Call `mem_save` after:
- Bug fix completed
- Architecture or design decision made
- Non-obvious discovery about the codebase
- Configuration change or environment setup
- Pattern established (naming, structure, convention)
- User preference or constraint learned

Format for `mem_save`:
- **title**: Verb + what — short, searchable (e.g. "Fixed N+1 query", "Chose Zustand over Redux")
- **type**: bugfix | decision | architecture | discovery | pattern | config | preference
- **scope**: `project` (default) | `personal`
- **topic_key**: stable key like `architecture/auth-model` for evolving decisions
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases (omit if none)

Topic rules:
- Different topics must not overwrite each other
- Reuse same `topic_key` to update an evolving topic
- If unsure about the key, pick a descriptive kebab-case name (e.g. `arch-auth-model`)

### WHEN TO SEARCH MEMORY

When user asks to recall something — any variation of "remember", "recall",
"what did we do", "how did we solve", "recordar", "qué hicimos":
1. Call `mem_context` — recent session history (fast, cheap)
2. If not found, call `mem_search` with keywords
3. If found, call `mem_get_observation` for full content

Also search PROACTIVELY when:
- Starting work that might have been done before
- User mentions a topic you have no context on
- User's first message references a project, feature, or problem

### SESSION CLOSE PROTOCOL (mandatory)

Before ending a session or saying "done" / "listo" / "that's it":
Call `mem_session_summary` with this structure:

## Goal
[What we were working on]

## Instructions
[User preferences or constraints — skip if none]

## Discoveries
- [Technical findings, gotchas]

## Accomplished
- [Completed items with key details]

## Next Steps
- [What remains — for the next session]

## Relevant Files
- path/to/file — [what it does]

### AFTER COMPACTION

If you see "FIRST ACTION REQUIRED" or context reset message:
1. Call `mem_session_summary` with the compacted content FIRST
2. Then call `mem_context` to recover context
3. Only THEN continue working

Do not skip step 1 — without it, everything before compaction is lost.
"""


# ─── Hook Callbacks ───────────────────────────────────────────────────────────


def _on_pre_llm_call(
    session_id: str,
    user_message: str,
    conversation_history: list,
    is_first_turn: bool,
    model: str,
    platform: str,
    **kwargs,
) -> dict | None:
    """
    Inject memory instructions into the system prompt context.
    This is the ONLY hook that can return a value (context injection).
    """
    # Try to start engram server on first turn
    if is_first_turn:
        tools._ensure_server()

    # Inject context from previous sessions on first turn
    injected = {}
    if is_first_turn and session_id:
        context_data = tools._engram_fetch(
            "/context", params={"project": tools._current_project}
        )
        if context_data and context_data.get("context"):
            injected["context"] = context_data["context"]

    # Always inject memory instructions so the agent knows the protocol
    injected["system_instruction"] = MEMORY_INSTRUCTIONS

    return injected


def _on_session_start(session_id: str, model: str, platform: str, **kwargs) -> None:
    """Register the session in engram when Hermes starts a new session."""
    if not session_id:
        return

    # Get working directory and extract project from git remote
    cwd = os.getcwd()
    tools.set_session_context(cwd)

    # Detect sub-agents: Hermes may pass parent info in kwargs
    parent_id = kwargs.get("parent_session_id") or kwargs.get("parent_id")
    if parent_id:
        tools.mark_subagent(session_id)
        logger.debug(
            "Sub-agent session detected: %s (parent: %s)", session_id, parent_id
        )
        return

    tools.ensure_session(session_id)
    logger.debug(
        "Session registered in engram: %s (project: %s)",
        session_id,
        tools._current_project,
    )


def _on_session_end(
    session_id: str,
    completed: bool,
    interrupted: bool,
    model: str,
    platform: str,
    **kwargs,
) -> None:
    """Clean up session state when Hermes ends a session."""
    tools.clear_session(session_id)
    logger.debug(
        "Session ended in engram: %s (completed=%s, interrupted=%s)",
        session_id,
        completed,
        interrupted,
    )


def _on_post_tool_call(
    tool_name: str,
    args: dict,
    result: str,
    task_id: str,
    duration_ms: int,
    **kwargs,
) -> None:
    """Track tool calls for session stats. Skip Engram's own tools."""
    if task_id:
        tools.capture_tool_call(task_id, tool_name)


# ─── Plugin Registration ──────────────────────────────────────────────────────


def register(ctx) -> None:
    """
    Wire schemas to handlers and register lifecycle hooks.
    Called exactly once at Hermes startup.
    """
    # ── Tools ──────────────────────────────────────────────────────────────
    ctx.register_tool(
        name="mem_search",
        toolset="engram",
        schema=schemas.MEM_SEARCH,
        handler=tools.mem_search,
    )
    ctx.register_tool(
        name="mem_save",
        toolset="engram",
        schema=schemas.MEM_SAVE,
        handler=tools.mem_save,
    )
    ctx.register_tool(
        name="mem_update",
        toolset="engram",
        schema=schemas.MEM_UPDATE,
        handler=tools.mem_update,
    )
    ctx.register_tool(
        name="mem_delete",
        toolset="engram",
        schema=schemas.MEM_DELETE,
        handler=tools.mem_delete,
    )
    ctx.register_tool(
        name="mem_context",
        toolset="engram",
        schema=schemas.MEM_CONTEXT,
        handler=tools.mem_context,
    )
    ctx.register_tool(
        name="mem_session_summary",
        toolset="engram",
        schema=schemas.MEM_SESSION_SUMMARY,
        handler=tools.mem_session_summary,
    )
    ctx.register_tool(
        name="mem_get_observation",
        toolset="engram",
        schema=schemas.MEM_GET_OBSERVATION,
        handler=tools.mem_get_observation,
    )
    ctx.register_tool(
        name="mem_save_prompt",
        toolset="engram",
        schema=schemas.MEM_SAVE_PROMPT,
        handler=tools.mem_save_prompt,
    )
    ctx.register_tool(
        name="mem_session_start",
        toolset="engram",
        schema=schemas.MEM_SESSION_START,
        handler=tools.mem_session_start,
    )
    ctx.register_tool(
        name="mem_session_end",
        toolset="engram",
        schema=schemas.MEM_SESSION_END,
        handler=tools.mem_session_end,
    )
    ctx.register_tool(
        name="mem_timeline",
        toolset="engram",
        schema=schemas.MEM_TIMELINE,
        handler=tools.mem_timeline,
    )
    ctx.register_tool(
        name="mem_judge",
        toolset="engram",
        schema=schemas.MEM_JUDGE,
        handler=tools.mem_judge,
    )
    ctx.register_tool(
        name="mem_doctor",
        toolset="engram",
        schema=schemas.MEM_DOCTOR,
        handler=tools.mem_doctor,
    )
    ctx.register_tool(
        name="mem_current_project",
        toolset="engram",
        schema=schemas.MEM_CURRENT_PROJECT,
        handler=tools.mem_current_project,
    )
    ctx.register_tool(
        name="mem_capture_passive",
        toolset="engram",
        schema=schemas.MEM_CAPTURE_PASSIVE,
        handler=tools.mem_capture_passive,
    )

    ctx.register_tool(
        name="mem_stats",
        toolset="engram",
        schema=schemas.MEM_STATS,
        handler=tools.mem_stats,
    )

    # ── Lifecycle Hooks ────────────────────────────────────────────────────
    ctx.register_hook("pre_llm_call", _on_pre_llm_call)
    ctx.register_hook("on_session_start", _on_session_start)
    ctx.register_hook("on_session_end", _on_session_end)
    ctx.register_hook("post_tool_call", _on_post_tool_call)

    # ── Server bootstrap ───────────────────────────────────────────────────
    # Try to start engram serve if not running
    tools._ensure_server()

    logger.info("Engram plugin loaded — memory protocol active")

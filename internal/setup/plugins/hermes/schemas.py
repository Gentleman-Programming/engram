"""Tool schemas — what the LLM sees when deciding to call Engram tools."""

MEM_SEARCH = {
    "name": "mem_search",
    "description": (
        "Search persistent memory across all past sessions. Use this when the user asks "
        "to recall, remember, or references past work. Also use proactively when starting "
        "work on something that might have been done before, or when the user's first "
        "message references a project, feature, or problem — check memory before responding. "
        "Returns FTS5 full-text search results with session context."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Search query — keywords, phrases, or boolean expressions (e.g. 'jwt OR session', 'fix auth bug').",
            },
            "project": {
                "type": "string",
                "description": "Filter by project name (e.g. 'conta', 'engram'). Omit for all projects.",
            },
            "scope": {
                "type": "string",
                "enum": ["project", "personal"],
                "description": "Filter by scope: project (default) or personal.",
            },
            "type": {
                "type": "string",
                "description": "Filter by observation type: bugfix, decision, architecture, discovery, pattern, config.",
            },
            "limit": {
                "type": "integer",
                "description": "Max results (default: 10, max: 20).",
            },
        },
        "required": ["query"],
    },
}

MEM_SAVE = {
    "name": "mem_save",
    "description": (
        "Save an important observation to persistent memory. Call this PROACTIVELY after: "
        "bug fix completed, architecture/design decision made, non-obvious discovery, "
        "configuration change, pattern established, user preference learned. "
        "Format uses What/Why/Where/Learned structure. Different topics don't overwrite each other. "
        "Skip trivial/obvious info and temporary task state."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "title": {
                "type": "string",
                "description": "Short searchable title (e.g. 'Fixed N+1 query', 'JWT auth middleware').",
            },
            "content": {
                "type": "string",
                "description": (
                    "Structured content using **What**, **Why**, **Where**, **Learned** format. "
                    "Example: '**What**: Replaced sessions with JWT\\n**Why**: Session storage doesn't scale\\n"
                    "**Where**: src/auth/middleware.ts\\n**Learned**: Must set httpOnly and secure flags'"
                ),
            },
            "type": {
                "type": "string",
                "enum": [
                    "decision",
                    "architecture",
                    "bugfix",
                    "discovery",
                    "pattern",
                    "config",
                    "learning",
                ],
                "description": "Category of the observation (default: 'learning').",
            },
            "scope": {
                "type": "string",
                "enum": ["project", "personal"],
                "description": "Scope: project (default) or personal.",
            },
            "topic_key": {
                "type": "string",
                "description": "Optional stable topic key for upserts (e.g. 'architecture/auth-model'). Reuses same observation.",
            },
        },
        "required": ["title", "content"],
    },
}

MEM_UPDATE = {
    "name": "mem_update",
    "description": "Update an existing observation by ID. Only provided fields are changed.",
    "parameters": {
        "type": "object",
        "properties": {
            "id": {
                "type": "integer",
                "description": "The observation ID to update (required).",
            },
            "title": {"type": "string", "description": "New title."},
            "content": {"type": "string", "description": "New content."},
            "type": {
                "type": "string",
                "enum": [
                    "decision",
                    "architecture",
                    "bugfix",
                    "discovery",
                    "pattern",
                    "config",
                    "learning",
                ],
            },
            "scope": {"type": "string", "enum": ["project", "personal"]},
            "topic_key": {"type": "string", "description": "New topic key."},
        },
        "required": ["id"],
    },
}

MEM_DELETE = {
    "name": "mem_delete",
    "description": "Delete an observation by ID. Use when an observation is wrong or no longer relevant.",
    "parameters": {
        "type": "object",
        "properties": {
            "id": {"type": "integer", "description": "The observation ID to delete."},
        },
        "required": ["id"],
    },
}

MEM_CONTEXT = {
    "name": "mem_context",
    "description": (
        "Get recent memory context from previous sessions. Checks session history (fast, cheap) "
        "for understanding what was done before. Call this at the start of a session or after "
        "compaction to recover context."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "project": {
                "type": "string",
                "description": "Filter by project (omit for all projects).",
            },
            "scope": {
                "type": "string",
                "enum": ["project", "personal"],
                "description": "Filter observations by scope.",
            },
        },
    },
}

MEM_SESSION_SUMMARY = {
    "name": "mem_session_summary",
    "description": (
        "Save a comprehensive end-of-session summary. MUST be called before ending a session "
        "or saying 'done'/'listo'. Also call this after compaction with the compacted content. "
        "Format: Goal/Instructions/Discoveries/Accomplished/Relevant Files. "
        "The Discoveries section is most valuable — captures gotchas and learnings."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "content": {
                "type": "string",
                "description": (
                    "Full session summary using Goal/Instructions/Discoveries/Accomplished/Files format. "
                    "Example: '## Goal\\n[What we were building]\\n## Instructions\\n[User preferences]\\n"
                    "## Discoveries\\n- [Technical finding]\\n## Accomplished\\n- [Completed task]\\n## Relevant Files\\n- path/to/file.ts'"
                ),
            },
            "session_id": {
                "type": "string",
                "description": "Session ID (default: auto-detected from project).",
            },
        },
        "required": ["content"],
    },
}

MEM_GET_OBSERVATION = {
    "name": "mem_get_observation",
    "description": "Get the full untruncated content of a specific observation by ID.",
    "parameters": {
        "type": "object",
        "properties": {
            "id": {"type": "integer", "description": "The observation ID to retrieve."},
        },
        "required": ["id"],
    },
}

MEM_SAVE_PROMPT = {
    "name": "mem_save_prompt",
    "description": "Save a user prompt for context. Use this to record what the user asked — their intent, questions, and requests.",
    "parameters": {
        "type": "object",
        "properties": {
            "content": {"type": "string", "description": "The user's prompt text."},
            "session_id": {"type": "string", "description": "Optional session ID."},
        },
        "required": ["content"],
    },
}

MEM_SESSION_START = {
    "name": "mem_session_start",
    "description": "Register the start of a new coding session. Call this at the beginning of a session to track activity.",
    "parameters": {
        "type": "object",
        "properties": {
            "id": {"type": "string", "description": "Unique session identifier."},
            "directory": {"type": "string", "description": "Working directory."},
        },
        "required": ["id"],
    },
}

MEM_SESSION_END = {
    "name": "mem_session_end",
    "description": "Mark a coding session as completed with an optional summary.",
    "parameters": {
        "type": "object",
        "properties": {
            "id": {"type": "string", "description": "Session identifier to close."},
            "summary": {"type": "string", "description": "Optional summary of what was accomplished."},
        },
        "required": ["id"],
    },
}

MEM_TIMELINE = {
    "name": "mem_timeline",
    "description": "Get timeline of observations for a project (recent activity).",
    "parameters": {
        "type": "object",
        "properties": {
            "project": {"type": "string", "description": "Filter by project name."},
            "scope": {"type": "string", "description": "Filter by scope (project/personal)."},
            "limit": {"type": "integer", "description": "Max results (default: 20)."},
        },
    },
}

MEM_STATS = {
    "name": "mem_stats",
    "description": "Get memory statistics: total observations, breakdown by type/scope, recent activity.",
    "parameters": {
        "type": "object",
        "properties": {
            "project": {"type": "string", "description": "Filter by project name."},
        },
    },
}

MEM_JUDGE = {
    "name": "mem_judge",
    "description": "Record a verdict on a pending memory conflict surfaced by mem_save.",
    "parameters": {
        "type": "object",
        "properties": {
            "judgment_id": {
                "type": "string",
                "description": "The judgment_id from candidates[] in the mem_save response.",
            },
            "relation": {
                "type": "string",
                "enum": ["related", "compatible", "scoped", "conflicts_with", "supersedes", "not_conflict"],
                "description": "The verdict.",
            },
            "reason": {
                "type": "string",
                "description": "Brief explanation of the verdict (max 200 chars).",
            },
            "confidence": {
                "type": "number",
                "description": "Confidence score 0.0..1.0 (default: 1.0).",
            },
        },
        "required": ["judgment_id", "relation"],
    },
}

MEM_DOCTOR = {
    "name": "mem_doctor",
    "description": "Run read-only operational diagnostics. Returns project state, recent sessions, and any detected issues.",
    "parameters": {
        "type": "object",
        "properties": {
            "project": {"type": "string", "description": "Project to diagnose."},
            "check": {"type": "string", "description": "Optional specific diagnostic check code."},
        },
    },
}

MEM_CURRENT_PROJECT = {
    "name": "mem_current_project",
    "description": (
        "Detect current project from working directory. Recommended first call at session start "
        "or after compaction. Returns project name, detected path, and detection method."
    ),
    "parameters": {
        "type": "object",
        "properties": {},
    },
}

MEM_CAPTURE_PASSIVE = {
    "name": "mem_capture_passive",
    "description": (
        "Extract and save structured learnings from text output. Automatically parses "
        "'## Key Learnings:' or '## Aprendizajes Clave:' sections from tool output."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "content": {"type": "string", "description": "Text output containing learnings to extract."},
            "session_id": {"type": "string", "description": "Optional session ID."},
            "source": {
                "type": "string",
                "description": "Source identifier (e.g. 'subagent-stop', 'session-end').",
            },
        },
        "required": ["content"],
    },
}

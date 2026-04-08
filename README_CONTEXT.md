# Context Bus Manual for AI Agents

This document defines how AI agents should write and recall shared context in Engram.

## Purpose

Engram Context Bus enables multiple agents to share short, high-signal context logs through a local REST API backed by SQLite.

Goals:
- Preserve work progress across sessions.
- Keep memory entries compact and searchable.
- Allow agent-aware handoff between Codex, Gemini, and system processes.

## API Endpoints

### POST /remember

Stores one context pill in `context_logs`.

Required fields:
- `agent_source`: must be one of `codex`, `gemini`, `system`.
- `content`: the context pill text.

Optional fields:
- `metadata`: JSON object with tags like `file_path`, `status`, `priority`, `task_id`.

Example request body:
{
  "agent_source": "codex",
  "content": "Refactored recall query to return chronological timeline around focus.",
  "metadata": {
    "file_path": "internal/store/store.go",
    "status": "in_progress",
    "priority": "high"
  }
}

Success response:
- HTTP 201 with created context log object.

Validation behavior:
- HTTP 400 for missing/invalid `agent_source`, missing `content`, or invalid `metadata` JSON object.

### GET /recall

Returns enriched recall payload with:
- `focused_observation`: single focus context log.
- `timeline`: chronological surrounding entries (up to 5 before + 5 after).

Query params:
- `q` optional: partial match search over `content`.
- `agent_source` optional: filter to one source (`codex`, `gemini`, `system`).
- `mode` optional: `compact` (default) or `full`.
- `recall_profile` optional: `lean`, `balanced`, `deep` (native presets).
- `timeline_limit` optional: number of items before/after focus (default 5, max 20).
- `max_chars` optional: compact preview char budget (default 180, min 60, max 500).

Native profile behavior:
- `lean`: `mode=compact`, `timeline_limit=2`, `max_chars=100`
- `balanced` (default): `mode=compact`, `timeline_limit=5`, `max_chars=180`
- `deep`: `mode=full`, `timeline_limit=8`, `max_chars=320`

Notes:
- Explicit `mode`, `timeline_limit`, or `max_chars` query params override profile defaults.
- Use `deep` when context completeness matters more than token cost.

Selection rules:
1. If `q` is present: pick the newest matching entry as focus.
2. If `q` is not present: pick the newest entry for `agent_source` (or globally if source omitted).
3. Return surrounding timeline ordered oldest-to-newest.

Response mode behavior:
- Focused observation always includes full content (`content_full`).
- Timeline is compressed by default (`mode=compact`) and optimized for token-efficient UI rendering.
- Use `mode=full` only for deep inspection workflows.
- For lowest token usage in UI, prefer `mode=compact&timeline_limit=2&max_chars=100`.

Error behavior:
- HTTP 404 when no focus entry exists for provided filters.
- HTTP 400 for invalid `agent_source`.

### GET /

Returns an embedded dark-mode monitoring dashboard (HTML) rendered directly by the Go server.

Behavior:
- Shows the latest 20 rows from `context_logs`.
- Colors `agent_source` badges (`codex` in blue, `gemini` in purple, `system` in green).
- Auto-refreshes every 5 seconds using a lightweight fetch call.

### GET /dashboard/logs

Returns JSON rows for dashboard live-refresh.

Query params:
- `limit` optional: defaults to 20, capped at 200.

Response behavior:
- Ordered newest-to-oldest.
- Includes `timestamp_human` for display formatting.

## Deterministic Compression Rules (`mode=compact`)

No LLM is used for compression to keep RAM/CPU overhead minimal.

| Content Type | Compression Rule |
|---|---|
| Go/Python-like code | Keep declaration line (`func ...`, `class ...`, or first structural line). |
| System logs | Keep last non-empty line; if HTTP/server error code exists, emit `Error <code>`. |
| Markdown | Keep only heading lines (`#`, `##`) and join compactly. |
| Plain text / notes | Normalize repeated newlines and truncate to 180 chars with ellipsis. |

## Agent Signature Protocol

Every agent must sign each context entry with a stable `agent_source` and role-specific content style.

### codex

Use for implementation and repository-level technical changes.

Recommended metadata:
- `file_path`
- `status`: `todo`, `in_progress`, `done`, `blocked`
- `priority`: `low`, `medium`, `high`
- `test`: optional test name or command

Content style:
- 1 to 3 sentences
- mention what changed and why
- include technical trade-off if relevant

### gemini

Use for UI/UX context, product behavior, interaction flow, and visual decisions.

Recommended metadata:
- `screen`
- `component`
- `status`
- `priority`

Content style:
- concise UX rationale
- expected user impact
- unresolved design question if any

### system

Use for daemon, runtime, infra, build, deploy, migrations, and operational logs.

Recommended metadata:
- `subsystem`
- `environment`
- `status`
- `incident_id` optional

Content style:
- factual operational status
- include failure mode or mitigation when needed

## Context Pill Guidelines

Keep each entry lightweight:
- 1 thought per entry.
- No large dumps.
- Avoid stack traces unless summarized.
- Prefer references in metadata over verbose content.

Bad:
- long multi-paragraph transcript.

Good:
- short decision, state, next action.

## Recommended Handoff Flow

1. Agent writes progress with `POST /remember` after meaningful change.
2. Next agent calls `GET /recall?agent_source=<its-source>` for latest same-lane focus.
3. If cross-lane context is needed, call `GET /recall?q=<topic>` without source filter.
4. Continue work and append next pill.

## Runtime and Storage Notes

- SQLite runs in WAL mode for concurrent reads/writes.
- `context_logs` has indexes by source/time and time for fast recall.
- Store lifecycle is managed by CLI command handlers and closed with defer in `cmd/engram/main.go`.

## Troubleshooting MCP Transport

Symptom:
- Codex tool calls fail with `Transport closed` (for example on `mem_save`).

Most likely cause:
- Codex keeps a stale MCP stdio session after binary replacement or MCP-related config/instruction changes.

Recommended recovery:
1. Open a new Codex chat (or reload VS Code window).
2. If the issue persists, restart VS Code.
3. Validate with one Engram MCP call (`mem_context` or `mem_save`).

Preventive rule:
- After replacing `engram.exe` or editing Codex MCP/instruction files, restart the Codex session before further tool calls.

## SSD Sidecar (.engram-ssd.md)

The server keeps a small sidecar file in the project root: `.engram-ssd.md`.

Behavior:
- On `engram serve` startup: if the file does not exist, bootstrap it from the latest 15 context logs.
- On every successful `POST /remember`: trigger async SSD refresh (non-blocking HTTP response).
- File write is atomic (temp file + rename) to avoid partial reads by agents.

What it contains:
- `# PROYECTO: <name>`
- `## ESTADO ACTUAL (SSD)`
- grouped milestones by `agent_source` with timestamps and deterministic compact previews.

How agents use it:
- Codex can read `.engram-ssd.md` at session start to get immediate context without querying full DB history.
- Browser UIs (Gemini/Antigravity) can fetch `/recall?mode=compact` and optionally mirror context from `.engram-ssd.md` for low-token startup.

Important:
- `/ssd` is not a native slash command in Codex by default.
- The sidecar is a file contract. Any agent that can read workspace files can consume it.

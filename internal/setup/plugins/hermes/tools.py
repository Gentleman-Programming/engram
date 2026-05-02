"""Tool handlers — the code that runs when the LLM calls an Engram tool."""

import json
import logging
import os
import subprocess
from typing import Optional

import requests

logger = logging.getLogger(__name__)

# ─── Configuration ───────────────────────────────────────────────────────────

ENGRAM_PORT = int(os.environ.get("ENGRAM_PORT", "7437"))
ENGRAM_URL = f"http://127.0.0.1:{ENGRAM_PORT}"
ENGRAM_BIN = os.environ.get("ENGRAM_BIN", "engram")

# Engram's own MCP tools — don't count these in session stats
ENGRAM_TOOLS = frozenset(
    [
        "mem_search",
        "mem_save",
        "mem_update",
        "mem_delete",
        "mem_context",
        "mem_session_summary",
        "mem_get_observation",
        "mem_save_prompt",
        "mem_timeline",
        "mem_session_start",
        "mem_session_end",
        "mem_judge",
        "mem_doctor",
        "mem_current_project",
    ]
)

# ─── HTTP Client ───────────────────────────────────────────────────────────────

_session: Optional[requests.Session] = None


def _get_session() -> requests.Session:
    global _session
    if _session is None:
        _session = requests.Session()
        _session.headers.update({"Content-Type": "application/json"})
    return _session


def _engram_fetch(
    path: str,
    method: str = "GET",
    body: Optional[dict] = None,
    params: Optional[dict] = None,
    timeout: float = 5.0,
) -> Optional[dict]:
    """Call engram serve HTTP API.

    Returns None only when the server is completely unreachable (ConnectionError).
    For HTTP 4xx/5xx responses, returns a dict describing the error so callers can
    surface the real failure instead of a misleading "server not reachable" message.
    """
    try:
        url = f"{ENGRAM_URL}{path}"
        resp = _get_session().request(
            method, url, json=body, params=params, timeout=timeout
        )
        if resp.status_code >= 400:
            try:
                payload = resp.json()
                return {
                    "error": payload.get("error")
                    or payload.get("message")
                    or str(resp.status_code),
                    "status": resp.status_code,
                }
            except Exception:
                return {
                    "error": resp.text or str(resp.status_code),
                    "status": resp.status_code,
                }
        resp.raise_for_status()
        return resp.json()
    except requests.exceptions.ConnectionError:
        return None
    except Exception as e:
        logger.warning("engram fetch error %s %s: %s", method, path, e)
        return None


def _is_engram_running() -> bool:
    """Check if engram serve is running."""
    try:
        resp = _get_session().get(f"{ENGRAM_URL}/health", timeout=1.0)
        return resp.status_code == 200
    except Exception:
        return False


def _ensure_server() -> bool:
    """Try to start engram serve if not running. Returns True if running."""
    if _is_engram_running():
        return True

    try:
        subprocess.Popen(
            [ENGRAM_BIN, "serve"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            stdin=subprocess.DEVNULL,
        )
        # Give it time to start
        import time

        for _ in range(10):
            time.sleep(0.3)
            if _is_engram_running():
                return True
    except Exception as e:
        logger.debug("Could not start engram serve: %s", e)

    return False


# ─── Session helpers ──────────────────────────────────────────────────────────


def _extract_project(directory: str) -> str:
    """Extract project name from git remote origin URL."""
    try:
        result = subprocess.run(
            ["git", "-C", directory, "remote", "get-url", "origin"],
            capture_output=True,
            text=True,
            timeout=3,
        )
        if result.returncode == 0:
            url = result.stdout.strip()
            if url:
                name = os.path.basename(url).replace(".git", "")
                if name:
                    return name
    except Exception:
        pass

    # Fallback: git root directory name
    try:
        result = subprocess.run(
            ["git", "-C", directory, "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            timeout=3,
        )
        if result.returncode == 0:
            root = result.stdout.strip()
            if root:
                return os.path.basename(os.path.normpath(root))
    except Exception:
        pass

    return os.path.basename(os.path.normpath(directory)) or "unknown"


def _truncate(s: str, max_chars: int) -> str:
    return s[:max_chars] + "..." if len(s) > max_chars else s


def default_session_id(project: str) -> str:
    """Generate a default session ID from project name for Hermes context."""
    safe = "".join(c if c.isalnum() else "_" for c in project)
    return f"hermes-{safe}" if safe else "hermes-default"


def _strip_private(s: str) -> str:
    """Strip <private>...</private> tags before sending to engram."""
    import re

    return re.sub(r"<private>[\s\S]*?</private>", "[REDACTED]", s).strip()


# ─── State tracked in-memory ─────────────────────────────────────────────────

# Sessions we've already ensured exist in engram (idempotent)
_known_sessions: set = set()

# Tool call counts per session
_tool_counts: dict = {}

# Sub-agent sessions — tracked so we don't register them
_subagent_sessions: set = set()

# Current project/directory (set by on_session_start hook)
_current_project: str = "unknown"
_current_directory: str = "unknown"


def set_session_context(directory: str) -> None:
    """Set project/directory from the current working directory (os.getcwd())."""
    global _current_project, _current_directory
    _current_project = _extract_project(directory)
    _current_directory = directory


def mark_subagent(session_id: str) -> None:
    _subagent_sessions.add(session_id)


def ensure_session(session_id: str) -> None:
    """Ensure a session exists in engram. Idempotent."""
    if not session_id or session_id in _known_sessions:
        return
    if session_id in _subagent_sessions:
        return  # Don't register sub-agents

    response = _engram_fetch(
        "/sessions",
        method="POST",
        body={
            "id": session_id,
            "project": _current_project,
            "directory": _current_directory,
        },
    )
    if response is not None and response.get("status", 200) < 400:
        _known_sessions.add(session_id)


def capture_prompt(session_id: str, content: str) -> None:
    """Capture a user prompt into engram."""
    if not session_id or session_id in _subagent_sessions:
        return

    content = _strip_private(_truncate(content, 2000))
    if len(content) <= 10:
        return

    ensure_session(session_id)
    _engram_fetch(
        "/prompts",
        method="POST",
        body={
            "session_id": session_id,
            "content": content,
            "project": _current_project,
        },
    )


def capture_tool_call(session_id: str, tool_name: str) -> None:
    """Track a tool call in engram. Skip Engram's own tools."""
    if not session_id or session_id in _subagent_sessions:
        return
    if tool_name.lower() in ENGRAM_TOOLS:
        return

    ensure_session(session_id)
    _tool_counts[session_id] = _tool_counts.get(session_id, 0) + 1


def clear_session(session_id: Optional[str]) -> None:
    """Clean up session state on deletion."""
    if session_id:
        _known_sessions.discard(session_id)
        _tool_counts.pop(session_id, None)
        _subagent_sessions.discard(session_id)


# ─── Tool Handlers ────────────────────────────────────────────────────────────
# Each receives (args: dict, **kwargs) → JSON string


def mem_search(args: dict, **kwargs) -> str:
    """Search persistent memory. Endpoint: GET /search?q=...&project=...&type=...&scope=...&limit=..."""
    try:
        params = {
            "q": args.get("query", ""),
        }
        if args.get("project"):
            params["project"] = args["project"]
        if args.get("scope"):
            params["scope"] = args["scope"]
        if args.get("type"):
            params["type"] = args["type"]
        if args.get("limit"):
            params["limit"] = str(args["limit"])

        result = _engram_fetch("/search", method="GET", body=None, params=params)
        if result is None:
            return json.dumps(
                {"error": "Engram server not reachable. Is 'engram serve' running?"}
            )
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_save(args: dict, **kwargs) -> str:
    try:
        result = _engram_fetch(
            "/observations",
            method="POST",
            body={
                "title": args.get("title", ""),
                "content": args.get("content", ""),
                "type": args.get("type", "learning"),
                "scope": args.get("scope", "project"),
                "topic_key": args.get("topic_key"),
                "project": _current_project,
            },
        )
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_update(args: dict, **kwargs) -> str:
    """Update an existing observation. Endpoint: PATCH /observations/{id}"""
    try:
        obs_id = args.get("id")
        if not obs_id:
            return json.dumps({"error": "id is required"})

        # Build PATCH body (only non-None fields)
        patch_body = {}
        for field in ("title", "content", "type", "scope", "topic_key"):
            if field in args and args[field] is not None:
                patch_body[field] = args[field]

        result = _engram_fetch(
            f"/observations/{obs_id}", method="PATCH", body=patch_body
        )
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_delete(args: dict, **kwargs) -> str:
    try:
        obs_id = args.get("id")
        if not obs_id:
            return json.dumps({"error": "id is required"})
        result = _engram_fetch(f"/observations/{obs_id}", method="DELETE")
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_context(args: dict, **kwargs) -> str:
    """Get recent memory context. Endpoint: GET /context?project=...&scope=..."""
    try:
        params = {}
        if args.get("project"):
            params["project"] = args["project"]
        if args.get("scope"):
            params["scope"] = args["scope"]
        result = _engram_fetch("/context", method="GET", params=params)
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_session_summary(args: dict, **kwargs) -> str:
    """Save end-of-session summary. Uses POST /observations with type=session_summary."""
    try:
        # No /sessions/summary endpoint in HTTP API — use mem_save pattern instead.
        # The MCP handler (mcp.go:1541-1547) does the same: AddObservation with
        # type="session_summary".
        project = _current_project or "unknown"
        session_id = args.get("session_id") or default_session_id(project)
        content = args.get("content", "")

        result = _engram_fetch(
            "/observations",
            method="POST",
            body={
                "session_id": session_id,
                "type": "session_summary",
                "title": f"Session summary: {project}",
                "content": content,
                "project": project,
            },
        )
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_get_observation(args: dict, **kwargs) -> str:
    try:
        obs_id = args.get("id")
        if not obs_id:
            return json.dumps({"error": "id is required"})
        result = _engram_fetch(f"/observations/{obs_id}", method="GET")
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_save_prompt(args: dict, **kwargs) -> str:
    """Save a user prompt for context. Endpoint: POST /observations (type=prompt)."""
    try:
        project = _current_project or "unknown"
        session_id = args.get("session_id") or default_session_id(project)
        result = _engram_fetch(
            "/observations",
            method="POST",
            body={
                "session_id": session_id,
                "type": "prompt",
                "title": "User prompt",
                "content": args.get("content", ""),
                "project": project,
            },
        )
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_session_start(args: dict, **kwargs) -> str:
    """Register session start. Endpoint: POST /sessions (creates session record).

    Note: HTTP API doesn't have a dedicated /sessions endpoint — this uses the
    passive-capture flow to register the session with a "session_start" pseudo-type.
    The Hermes on_session_start hook handles the real session registration.
    """
    try:
        project = _current_project or "unknown"
        session_id = args.get("id") or default_session_id(project)
        directory = args.get("directory", "")

        # Use passive capture to register the session
        result = _engram_fetch(
            "/observations/passive",
            method="POST",
            body={
                "session_id": session_id,
                "content": f"Session started: {project}"
                + (f" | {directory}" if directory else ""),
                "project": project,
                "source": "session_start",
            },
        )
        # Passive capture is best-effort — return success even if server unreachable
        if result is None:
            return json.dumps(
                {
                    "id": None,
                    "message": f"Session registered locally (engram server may be offline). "
                    f"ID={session_id}, project={project}",
                }
            )
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_session_end(args: dict, **kwargs) -> str:
    """Mark session as completed. Best-effort via passive capture."""
    try:
        project = _current_project or "unknown"
        session_id = args.get("id") or default_session_id(project)

        result = _engram_fetch(
            "/observations/passive",
            method="POST",
            body={
                "session_id": session_id,
                "content": f"Session ended: {project}",
                "project": project,
                "source": "session_end",
            },
        )
        if result is None:
            return json.dumps(
                {"message": f"Session end noted locally. ID={session_id}"}
            )
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_timeline(args: dict, **kwargs) -> str:
    """Get timeline of observations for a project. Endpoint: GET /timeline?project=...&scope=...&limit=..."""
    try:
        params = {"project": args.get("project", _current_project or "")}
        if args.get("scope"):
            params["scope"] = args["scope"]
        if args.get("limit"):
            params["limit"] = str(args["limit"])
        result = _engram_fetch("/timeline", method="GET", params=params)
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def mem_stats(args: dict, **kwargs) -> str:
    """Get memory statistics.

    The HTTP server does not expose a /stats endpoint. Use the MCP server
    (engram mcp --tools=agent) to access this capability."""
    return json.dumps(
        {
            "error": "mem_stats is not available via the Engram HTTP API.",
            "details": "The Hermes plugin previously called GET /stats, but that route is not exposed by the Engram HTTP server.",
        }
    )


def mem_judge(args: dict, **kwargs) -> str:
    """Record a verdict on a pending memory conflict.

    The HTTP server does not expose a /judge endpoint. Use the MCP server
    (engram mcp --tools=agent) to access this tool.
    """
    return json.dumps(
        {
            "error": "mem_judge is not available via the Engram HTTP API.",
            "details": (
                "The Hermes plugin uses direct HTTP requests, but the /judge route "
                "is not exposed. Route this tool through the MCP server instead: "
                "ensure mcp_servers.engram is configured in ~/.hermes/config.yaml."
            ),
        }
    )


def mem_doctor(args: dict, **kwargs) -> str:
    """Run operational diagnostics.

    The HTTP server does not expose a /doctor endpoint. Use the MCP server
    (engram mcp --tools=agent) to access this tool.
    """
    return json.dumps(
        {
            "error": "mem_doctor is not available via the Engram HTTP API.",
            "details": (
                "The Hermes plugin uses direct HTTP requests, but the /doctor route "
                "is not exposed. Route this tool through the MCP server instead: "
                "ensure mcp_servers.engram is configured in ~/.hermes/config.yaml."
            ),
        }
    )


def mem_current_project(args: dict, **kwargs) -> str:
    """Detect current project from working directory. Uses _current_project set by hook."""
    project = _current_project or "unknown"
    return json.dumps(
        {
            "project": project,
            "directory": _current_directory or os.getcwd(),
            "source": "hermes_plugin",
        }
    )


def mem_capture_passive(args: dict, **kwargs) -> str:
    """Extract and save structured learnings from text output. Endpoint: POST /passive."""
    try:
        project = _current_project or "unknown"
        session_id = args.get("session_id") or default_session_id(project)
        result = _engram_fetch(
            "/observations/passive",
            method="POST",
            body={
                "session_id": session_id,
                "content": args.get("content", ""),
                "project": project,
                "source": args.get("source", "passive_capture"),
            },
        )
        if result is None:
            return json.dumps({"error": "Engram server not reachable."})
        return json.dumps(result, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


# ─── Tool Registry ────────────────────────────────────────────────────────────
# Maps schema name → handler function.
# Each receives (args: dict, **kwargs) → JSON string

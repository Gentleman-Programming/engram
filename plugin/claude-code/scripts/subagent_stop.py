#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "requests>=2.31",
# ]
# ///
"""
Engram — SubagentStop hook for Claude Code (Python port of subagent-stop.sh).

Passive memory capture: extracts structured learnings from subagent transcripts
and saves them to Engram automatically.

Fires when: Task(subagent_type="...") completes
Input (stdin JSON): session_id, agent_transcript_path, transcript_path, cwd

Supported learning formats:
  ## Aprendizajes Clave:      (Spanish — CCT format)
  ## Key Learnings:           (English)
  ### Learnings:              (alternative header)
  Numbered lists: 1. text
  Bullet lists: - text

Design: NEVER blocks the agent workflow (always exits 0).
If Engram is not running, logs a warning and exits cleanly.
"""

import json
import logging
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

import requests


# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

ENGRAM_PORT = int(os.environ.get("ENGRAM_PORT", 7437))
ENGRAM_URL = f"http://127.0.0.1:{ENGRAM_PORT}"
ENGRAM_OBSERVATIONS_PATH = "/observations"
ENGRAM_HEALTH_PATH = "/health"

LOG_DIR = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "engram" / "logs"
LOG_FILE = LOG_DIR / "subagent_stop.log"

# Request timeouts (seconds)
HEALTH_TIMEOUT = 2
SAVE_TIMEOUT = 5

# Minimum length for a learning to be valid
MIN_LEARNING_LENGTH = 20


# ---------------------------------------------------------------------------
# Logging setup
# ---------------------------------------------------------------------------

def setup_logging() -> logging.Logger:
    """Configure logging to file and stderr."""
    LOG_DIR.mkdir(parents=True, exist_ok=True)

    logger = logging.getLogger("engram.subagent_stop")
    logger.setLevel(logging.INFO)

    if not logger.handlers:
        fmt = logging.Formatter(
            "[%(asctime)s] [subagent-stop] %(message)s",
            datefmt="%Y-%m-%dT%H:%M:%S%z",
        )
        file_handler = logging.FileHandler(LOG_FILE, mode="a", encoding="utf-8")
        file_handler.setFormatter(fmt)
        logger.addHandler(file_handler)

    return logger


# ---------------------------------------------------------------------------
# Engram connectivity
# ---------------------------------------------------------------------------

def check_engram_health(engram_url: str, logger: logging.Logger) -> bool:
    """Check if the Engram server is reachable.

    Returns True if the server responds, False otherwise.
    Never raises — failures are logged as warnings.
    """
    try:
        resp = requests.get(
            f"{engram_url}{ENGRAM_HEALTH_PATH}",
            timeout=HEALTH_TIMEOUT,
        )
        return resp.ok
    except Exception as exc:
        logger.warning(f"Engram server not reachable at {engram_url}: {exc}")
        return False


def save_to_engram(
    observation: dict,
    engram_url: str,
    logger: logging.Logger,
) -> str | None:
    """POST an observation to the Engram API.

    Args:
        observation: Payload dict with title, content, type, project, metadata.
        engram_url: Base URL for the Engram server.
        logger: Logger instance.

    Returns:
        The observation ID string on success, None on failure.
    """
    try:
        resp = requests.post(
            f"{engram_url}{ENGRAM_OBSERVATIONS_PATH}",
            json=observation,
            timeout=SAVE_TIMEOUT,
        )
        if resp.ok:
            data = resp.json()
            return data.get("id")
        logger.warning(f"Engram returned {resp.status_code}: {resp.text[:120]}")
        return None
    except Exception as exc:
        logger.warning(f"Failed to save observation to Engram: {exc}")
        return None


# ---------------------------------------------------------------------------
# Agent detection from parent transcript
# ---------------------------------------------------------------------------

def detect_agent_name(parent_transcript_path: str, logger: logging.Logger) -> str:
    """Detect the last used subagent_type from the parent transcript.

    The parent transcript is a JSONL file. Each line is a JSON object.
    Task tool invocations appear as tool_use items with name "Task" and
    an input dict containing "subagent_type".

    Args:
        parent_transcript_path: Absolute path to the parent JSONL transcript.
        logger: Logger instance.

    Returns:
        The last detected subagent_type, or "unknown" if not found.
    """
    if not parent_transcript_path:
        logger.warning("No parent transcript path provided.")
        return "unknown"

    path = Path(parent_transcript_path)
    if not path.exists():
        logger.warning(f"Parent transcript not found: {parent_transcript_path}")
        return "unknown"

    found: list[str] = []

    try:
        with path.open(encoding="utf-8") as fh:
            for raw_line in fh:
                line = raw_line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    continue

                # Format 1: entry.message.content[] (standard Claude Code JSONL)
                content = None
                if "message" in entry:
                    content = entry["message"].get("content", [])
                elif "content" in entry:
                    content = entry.get("content", [])

                if isinstance(content, list):
                    for item in content:
                        if (
                            isinstance(item, dict)
                            and item.get("type") == "tool_use"
                            and item.get("name") == "Task"
                        ):
                            subagent_type = item.get("input", {}).get("subagent_type")
                            if subagent_type:
                                found.append(subagent_type)

                # Format 2: entry.tool_input (PreToolUse format)
                if "tool_input" in entry:
                    subagent_type = entry["tool_input"].get("subagent_type")
                    if subagent_type:
                        found.append(subagent_type)

    except Exception as exc:
        logger.warning(f"Error reading parent transcript: {exc}")

    if found:
        agent_name = found[-1]  # Last one is most recent
        logger.info(f"Detected agent_name: {agent_name} (from {len(found)} task(s))")
        return agent_name

    logger.warning("Could not detect subagent_type from parent transcript.")
    return "unknown"


# ---------------------------------------------------------------------------
# Learnings extraction from agent transcript
# ---------------------------------------------------------------------------

def _extract_text_from_jsonl(transcript_path: Path, logger: logging.Logger) -> str:
    """Extract all assistant text output from a JSONL transcript file.

    Handles:
    - assistant messages with text content blocks
    - tool_result items with list content (Task tool output)
    - legacy tool_result entries at top level

    Args:
        transcript_path: Path to the JSONL file.
        logger: Logger instance.

    Returns:
        Combined text from the transcript.
    """
    parts: list[str] = []

    try:
        with transcript_path.open(encoding="utf-8") as fh:
            for raw_line in fh:
                line = raw_line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    continue

                # Assistant messages
                if entry.get("type") == "assistant":
                    content = entry.get("message", {}).get("content", [])
                    if isinstance(content, list):
                        for item in content:
                            if isinstance(item, dict) and item.get("type") == "text":
                                text = item.get("text", "")
                                if text:
                                    parts.append(text)

                # User messages — Task tool results arrive as list content
                if entry.get("type") == "user":
                    content = entry.get("message", {}).get("content", [])
                    if isinstance(content, list):
                        for item in content:
                            if isinstance(item, dict) and item.get("type") == "tool_result":
                                result_content = item.get("content", "")
                                # Only list content = Task tool (not Bash/Read strings)
                                if isinstance(result_content, list):
                                    for block in result_content:
                                        if isinstance(block, dict) and block.get("type") == "text":
                                            text = block.get("text", "")
                                            if text:
                                                parts.append(text.replace("\\n", "\n"))

                # Legacy top-level tool_result
                if entry.get("type") == "tool_result":
                    content = entry.get("content", "")
                    if isinstance(content, str) and content:
                        parts.append(content.replace("\\n", "\n"))

    except Exception as exc:
        logger.warning(f"Error extracting text from transcript: {exc}")

    return "\n".join(parts)


def extract_learnings(agent_transcript_path: str, logger: logging.Logger) -> list[str]:
    """Extract individual learning items from an agent transcript.

    Reads the JSONL transcript, reconstructs the text output, then searches
    for sections matching:
      ## Aprendizajes Clave:
      ## Key Learnings:
      ### Learnings:

    Returns the learnings from the LAST valid section found (most recent output).

    Args:
        agent_transcript_path: Absolute path to the subagent JSONL transcript.
        logger: Logger instance.

    Returns:
        List of learning strings (each >= MIN_LEARNING_LENGTH chars).
    """
    if not agent_transcript_path:
        return []

    path = Path(agent_transcript_path)
    if not path.exists():
        logger.warning(f"Agent transcript not found: {agent_transcript_path}")
        return []

    text = _extract_text_from_jsonl(path, logger)
    if not text.strip():
        logger.info("Agent transcript is empty. Nothing to capture.")
        return []

    # Match all learning section headers (Spanish and English)
    header_pattern = re.compile(
        r"^#{2,3}\s+(?:Aprendizajes(?:\s+Clave)?|Key\s+Learnings?|Learnings?):?\s*$",
        re.IGNORECASE | re.MULTILINE,
    )

    matches = list(header_pattern.finditer(text))
    if not matches:
        logger.info(f"No learning section found in transcript for this agent.")
        return []

    # Process sections in REVERSE order — use first valid one (most recent)
    for match in reversed(matches):
        section_text = text[match.end():]

        # Cut off at next major section header (## or ### )
        next_section = re.search(r"\n#{1,3} ", section_text)
        if next_section:
            section_text = section_text[: next_section.start()]

        learnings: list[str] = []

        # Try numbered items: "1. text" or "1) text"
        numbered = re.findall(
            r"^\s*\d+[.)]\s+(.+?)(?=\n\s*\d+[.)]|\n\n|\Z)",
            section_text,
            re.MULTILINE | re.DOTALL,
        )
        if numbered:
            for item in numbered:
                cleaned = _clean_markdown(item)
                if len(cleaned) >= MIN_LEARNING_LENGTH and not cleaned.startswith("["):
                    learnings.append(cleaned)

        # Fall back to bullet items: "- text" or "* text"
        if not learnings:
            bullets = re.findall(
                r"^\s*[-*]\s+(.+?)(?=\n\s*[-*]|\n\n|\Z)",
                section_text,
                re.MULTILINE | re.DOTALL,
            )
            for item in bullets:
                cleaned = _clean_markdown(item)
                if len(cleaned) >= MIN_LEARNING_LENGTH and not cleaned.startswith("["):
                    learnings.append(cleaned)

        if learnings:
            logger.info(f"Found learnings block. Extracted {len(learnings)} item(s).")
            return learnings

    return []


def _clean_markdown(text: str) -> str:
    """Strip basic Markdown formatting and collapse whitespace."""
    text = re.sub(r"\*\*([^*]+)\*\*", r"\1", text)   # bold
    text = re.sub(r"`([^`]+)`", r"\1", text)           # inline code
    text = re.sub(r"\*([^*]+)\*", r"\1", text)         # italic
    return " ".join(text.split())


# ---------------------------------------------------------------------------
# Main orchestration
# ---------------------------------------------------------------------------

def main() -> None:
    """Entry point — always exits 0 to avoid blocking Claude Code."""
    logger = setup_logging()

    try:
        # Read hook input from stdin
        raw_input = sys.stdin.read()
        try:
            data = json.loads(raw_input) if raw_input.strip() else {}
        except json.JSONDecodeError as exc:
            logger.warning(f"Invalid JSON from stdin: {exc}")
            data = {}

        session_id = data.get("session_id", "")
        agent_transcript = data.get("agent_transcript_path", "")
        parent_transcript = data.get("transcript_path", "")
        cwd = data.get("cwd", "")
        project = Path(cwd).name if cwd else "unknown"

        logger.info(f"SubagentStop fired. session={session_id} project={project}")

        # 1. Verify Engram is running
        if not check_engram_health(ENGRAM_URL, logger):
            logger.warning("Engram server not running. Skipping passive capture.")
            sys.exit(0)

        # 2. Verify we have a transcript to read
        if not agent_transcript or not Path(agent_transcript).exists():
            logger.warning(f"No agent transcript available. Path: '{agent_transcript}'")
            sys.exit(0)

        # 3. Detect agent name from parent transcript
        agent_name = detect_agent_name(parent_transcript, logger)

        # 4. Extract learnings from agent transcript
        learnings = extract_learnings(agent_transcript, logger)

        if not learnings:
            logger.info(f"No learning section found in transcript for agent={agent_name}")
            sys.exit(0)

        # 5. Save each learning to Engram
        saved_count = 0
        for learning in learnings:
            title = f"[{agent_name}] {learning[:60]}..."
            now_iso = datetime.now(timezone.utc).isoformat()

            payload = {
                "title": title,
                "content": learning,
                "type": "learning",
                "project": project,
                "metadata": {
                    "session_id": session_id,
                    "agent_name": agent_name,
                    "source": "subagent-stop-hook",
                    "captured_at": now_iso,
                },
            }

            obs_id = save_to_engram(payload, ENGRAM_URL, logger)
            if obs_id:
                logger.info(f"Saved learning #{saved_count + 1} obs_id={obs_id} agent={agent_name}")
                saved_count += 1
            else:
                logger.warning(f"Failed to save learning: {learning[:60]}...")

        logger.info(
            f"Passive capture complete. Saved {saved_count} learnings "
            f"for agent={agent_name} session={session_id}"
        )

    except Exception as exc:
        logger.warning(f"Unexpected error in subagent_stop: {exc}")

    sys.exit(0)


if __name__ == "__main__":
    main()

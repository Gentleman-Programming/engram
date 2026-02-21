#!/bin/bash
# Engram — SubagentStop hook for Claude Code
#
# Passive memory capture: extracts structured learnings from subagent transcripts
# and saves them to Engram automatically.
#
# Fires when: Task(subagent_type="...") completes
# Input (stdin JSON): session_id, agent_transcript_path, transcript_path, cwd
#
# Supported learning formats:
#   ## Aprendizajes Clave:      (Spanish — CCT format)
#   ## Key Learnings:           (English)
#   ### Learnings:              (alternative header)
#   Numbered lists: 1. text
#   Bullet lists: - text
#
# Design: NEVER blocks the agent workflow (always exits 0).
# If Engram is not running, logs a warning and exits cleanly.

set -euo pipefail

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
LOG_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/engram/logs"
LOG_FILE="${LOG_DIR}/subagent-stop.log"

# Setup logging
mkdir -p "$LOG_DIR"
log() { echo "[$(date -Iseconds)] [subagent-stop] $*" >> "$LOG_FILE"; }

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
AGENT_TRANSCRIPT=$(echo "$INPUT" | jq -r '.agent_transcript_path // empty')
PARENT_TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(basename "$CWD")

log "SubagentStop fired. session=$SESSION_ID project=$PROJECT"

# Verify Engram is running
if ! curl -sf "${ENGRAM_URL}/health" --max-time 2 > /dev/null 2>&1; then
  log "Engram server not running. Skipping passive capture."
  exit 0
fi

# Verify we have a transcript to read
if [ -z "$AGENT_TRANSCRIPT" ] || [ ! -f "$AGENT_TRANSCRIPT" ]; then
  log "No agent transcript available. Path: '$AGENT_TRANSCRIPT'"
  exit 0
fi

# -------------------------------------------------------------------
# Detect agent_name from parent transcript (last used subagent_type)
# -------------------------------------------------------------------
AGENT_NAME="unknown"
if [ -n "$PARENT_TRANSCRIPT" ] && [ -f "$PARENT_TRANSCRIPT" ]; then
  # Extract the last subagent_type from parent transcript
  # Claude Code records Task(subagent_type="name") calls in transcript
  AGENT_NAME=$(grep -oP '(?<=subagent_type=")[^"]+' "$PARENT_TRANSCRIPT" 2>/dev/null | tail -1 || true)
  if [ -z "$AGENT_NAME" ]; then
    # Fallback: look for subagent_type in JSON format
    AGENT_NAME=$(grep -oP '"subagent_type"\s*:\s*"\K[^"]+' "$PARENT_TRANSCRIPT" 2>/dev/null | tail -1 || true)
  fi
fi
[ -z "$AGENT_NAME" ] && AGENT_NAME="unknown"
log "Detected agent_name: $AGENT_NAME"

# -------------------------------------------------------------------
# Extract learnings from agent transcript
# -------------------------------------------------------------------
# Read the transcript content
TRANSCRIPT_CONTENT=$(cat "$AGENT_TRANSCRIPT" 2>/dev/null || true)

if [ -z "$TRANSCRIPT_CONTENT" ]; then
  log "Agent transcript is empty. Nothing to capture."
  exit 0
fi

# Extract text between learning header and next ## header (or end of file)
# Supports: ## Aprendizajes Clave:, ## Key Learnings:, ### Learnings:
LEARNINGS_BLOCK=$(echo "$TRANSCRIPT_CONTENT" | \
  awk '/^#{2,3} (Aprendizajes Clave|Key Learnings|Learnings):?/{found=1; next} found && /^#{1,2} /{found=0} found{print}' \
  2>/dev/null || true)

if [ -z "$LEARNINGS_BLOCK" ]; then
  log "No learning section found in transcript for agent=$AGENT_NAME"
  exit 0
fi

log "Found learnings block. Extracting individual items..."

# Parse individual learning items (numbered or bullet list)
SAVED_COUNT=0
while IFS= read -r line; do
  # Match: "1. text" or "- text" or "* text" — strip leading marker
  LEARNING=$(echo "$line" | sed -E 's/^[0-9]+\.\s+//' | sed -E 's/^[-*]\s+//' | xargs 2>/dev/null || true)

  # Skip empty lines or very short text (< 20 chars — likely headers or whitespace)
  if [ ${#LEARNING} -lt 20 ]; then
    continue
  fi

  # Build observation payload
  TITLE="[${AGENT_NAME}] $(echo "$LEARNING" | cut -c1-60)..."
  PAYLOAD=$(jq -n \
    --arg title "$TITLE" \
    --arg content "$LEARNING" \
    --arg type "learning" \
    --arg project "$PROJECT" \
    --arg session_id "$SESSION_ID" \
    --arg agent_name "$AGENT_NAME" \
    '{
      title: $title,
      content: $content,
      type: $type,
      project: $project,
      metadata: {
        session_id: $session_id,
        agent_name: $agent_name,
        source: "subagent-stop-hook",
        captured_at: (now | todate)
      }
    }')

  RESPONSE=$(curl -sf "${ENGRAM_URL}/observations" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" \
    --max-time 5 \
    2>/dev/null || true)

  if [ -n "$RESPONSE" ]; then
    OBS_ID=$(echo "$RESPONSE" | jq -r '.id // empty' 2>/dev/null || true)
    log "Saved learning #$((SAVED_COUNT+1)) obs_id=$OBS_ID agent=$AGENT_NAME"
    SAVED_COUNT=$((SAVED_COUNT + 1))
  else
    log "Failed to save learning: ${LEARNING:0:60}..."
  fi
done < <(echo "$LEARNINGS_BLOCK" | grep -E '^[0-9]+\.|^[-*]')

log "Passive capture complete. Saved $SAVED_COUNT learnings for agent=$AGENT_NAME session=$SESSION_ID"
exit 0

#!/bin/bash
# Engram — PostToolUse hook (file read tracking)
#
# Tracks which files have been read this session into a temp JSON file.
# The UserPromptSubmit hook reads this file to inject a manifest,
# preventing re-reads and saving tokens.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Save stdin to temp file — JSON may contain newlines in string values
# which break echo "$VAR" | jq
INPUT_FILE=$(mktemp)
trap "rm -f '$INPUT_FILE'" EXIT
cat > "$INPUT_FILE"

SESSION_ID=$(jq -r '.session_id // empty' "$INPUT_FILE")
TOOL_NAME=$(jq -r '.tool_name // empty' "$INPUT_FILE")

# No session ID = can't track
if [ -z "$SESSION_ID" ]; then
  echo "{}"
  exit 0
fi

TRACK_FILE="/tmp/engram-claude-${SESSION_ID}-filereads.json"

# Extract file_path based on tool type
FILE_PATH=""
case "$TOOL_NAME" in
  Read)
    FILE_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE")
    ;;
  Bash)
    # Conservative: only match "rtk read <path>" pattern
    CMD=$(jq -r '.tool_input.command // empty' "$INPUT_FILE")
    FILE_PATH=$(echo "$CMD" | grep -oP '^rtk read\s+(?:-[a-zA-Z]\s+)*\K\S+' 2>/dev/null || true)
    ;;
  *)
    # Skip all other tools
    echo "{}"
    exit 0
    ;;
esac

# No file path extracted = nothing to track
if [ -z "$FILE_PATH" ]; then
  echo "{}"
  exit 0
fi

# Estimate output bytes from tool_response
BYTES=$(jq -r '
  .tool_response //
  .tool_result //
  "" |
  if type == "string" then length
  elif type == "object" then (tostring | length)
  else 0
  end
' "$INPUT_FILE" 2>/dev/null || echo "0")

# Read existing tracking file (or start empty)
if [ -f "$TRACK_FILE" ]; then
  READS=$(cat "$TRACK_FILE")
else
  READS="[]"
fi

TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Upsert: remove existing entry for this file, add new one at end
NEW_READS=$(echo "$READS" | jq --arg fp "$FILE_PATH" --arg ts "$TIMESTAMP" --argjson bytes "${BYTES:-0}" '
  [.[] | select(.file_path != $fp)] + [{"file_path": $fp, "timestamp": $ts, "bytes": $bytes}] |
  if length > 15 then sort_by(.timestamp) | .[-15:] else . end
' 2>/dev/null)

# Only write if jq succeeded — don't truncate on parse error
if [ $? -eq 0 ] && [ -n "$NEW_READS" ]; then
  echo "$NEW_READS" > "$TRACK_FILE"
fi

# Return empty — tracking is a side effect only
echo "{}"
exit 0

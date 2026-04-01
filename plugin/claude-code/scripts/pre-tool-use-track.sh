#!/bin/bash
# Engram — PreToolUse hook (file read tracking)
#
# Tracks which files are about to be read into a temp JSON file.
# The UserPromptSubmit hook reads this file to inject a manifest,
# preventing re-reads and saving tokens.
#
# Fires on Read and Bash (rtk read) tools. Never blocks — always returns {}.
exec 2>/dev/null

INPUT=$(cat)

SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty') || exit 0
TOOL_NAME=$(printf '%s' "$INPUT" | jq -r '.tool_name // empty') || exit 0

[ -z "$SESSION_ID" ] && exit 0

TRACK_FILE="/tmp/engram-claude-${SESSION_ID}-filereads.json"

# Extract file_path based on tool type
FILE_PATH=""
case "$TOOL_NAME" in
  Read)
    FILE_PATH=$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // empty')
    ;;
  Bash)
    CMD=$(printf '%s' "$INPUT" | jq -r '.tool_input.command // empty')
    FILE_PATH=$(printf '%s' "$CMD" | grep -oP '^rtk read\s+(?:-[a-zA-Z]\s+)*\K\S+' || true)
    ;;
  *)
    exit 0
    ;;
esac

[ -z "$FILE_PATH" ] && exit 0

# Expand ~ to home dir
FILE_PATH="${FILE_PATH/#\~/$HOME}"

# Get file size from disk (pre-tool, so we use stat)
BYTES=0
if [ -f "$FILE_PATH" ]; then
  BYTES=$(stat -c '%s' "$FILE_PATH" 2>/dev/null || stat -f '%z' "$FILE_PATH" 2>/dev/null || echo "0")
fi

# Read existing tracking file (or start empty)
[ -f "$TRACK_FILE" ] && READS=$(cat "$TRACK_FILE") || READS="[]"

TS=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Upsert: remove existing entry for this file, add new one at end
NEW_READS=$(printf '%s' "$READS" | jq --arg fp "$FILE_PATH" --arg ts "$TS" --argjson bytes "${BYTES:-0}" '
  [.[] | select(.file_path != $fp)] + [{"file_path": $fp, "timestamp": $ts, "bytes": $bytes}] |
  if length > 15 then sort_by(.timestamp) | .[-15:] else . end
') && [ -n "$NEW_READS" ] && printf '%s' "$NEW_READS" > "$TRACK_FILE"

exit 0

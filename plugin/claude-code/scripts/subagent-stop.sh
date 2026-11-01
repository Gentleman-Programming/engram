#!/usr/bin/env bash
# Engram — SubagentStop/SubagentHandback capture hook for Claude Code (async)
#
# Thin hook: reads the subagent output from stdin, POSTs it to
# the passive capture endpoint. All extraction logic lives in the
# Go server — this script is intentionally minimal.

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
# Successful handbacks carry the report in tool_input.message. Legacy stop
# events use last_assistant_message/stdout. Keep content inside JSON so shell
# and platform text-mode conversions cannot alter its newlines.
PROJECT=$(resolve_project "$CWD") || exit 0
BODY=$(printf '%s' "$INPUT" | jq -c --arg project "$PROJECT" '
  (if .hook_event_name == "PostToolUse" then
     select(.tool_name == "SubagentHandback") | .tool_input | select(type == "object") | .message | select(type == "string")
   elif .hook_event_name == null or .hook_event_name == "SubagentStop" then
     if .last_assistant_message == "" then (.stdout // "") else (.last_assistant_message // .stdout // "") end
   else empty end) as $content |
  {session_id: (.session_id // ""), content: $content,
   project: $project, source: "subagent-stop"} | select(.content != "")')

# Nothing to capture if no output
[ -z "$BODY" ] && exit 0

# Fire and forget — server handles extraction, dedup, and storage
# Stream the JSON bytes: native Windows curl may transcode non-ASCII argv.
printf '%s' "$BODY" | engram_curl -sf "${ENGRAM_URL}/observations/passive" \
  -X POST \
  -H "Content-Type: application/json" \
  --data-binary @- \
  > /dev/null

exit 0

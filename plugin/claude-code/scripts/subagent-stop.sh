#!/usr/bin/env bash
# Engram — SubagentStop hook for Claude Code (async)
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
# Claude Code uses last_assistant_message; retain stdout as a fallback for
# other harnesses. Keep content inside JSON so shell and platform text-mode
# conversions cannot alter its newlines.
PROJECT=$(resolve_project "$CWD") || exit 0
BODY=$(printf '%s' "$INPUT" | jq -c --arg project "$PROJECT" '
  {session_id: (.session_id // ""), content: (if .last_assistant_message == "" then (.stdout // "") else (.last_assistant_message // .stdout // "") end),
   project: $project, source: "subagent-stop"} | select(.content != "")')

# Nothing to capture if no output
[ -z "$BODY" ] && exit 0

# Fire and forget — server handles extraction, dedup, and storage
engram_curl -sf "${ENGRAM_URL}/observations/passive" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$BODY" \
  > /dev/null

exit 0

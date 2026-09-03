#!/usr/bin/env bash
# Engram — SubagentStop hook for Claude Code (async)
#
# Thin hook: reads the subagent output from stdin, POSTs it to
# the passive capture endpoint. All extraction logic lives in the
# Go server — this script is intentionally minimal.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
# Claude Code's SubagentStop payload carries the subagent's final text in
# last_assistant_message; there is no .stdout field, so reading .stdout captured
# nothing and every subagent run no-op'd. Keep .stdout as a fallback for other
# harnesses that reuse this script (parity with plugin/codex/scripts).
OUTPUT=$(echo "$INPUT" | jq -r '.last_assistant_message // .stdout // empty')

# Nothing to capture if no output
[ -z "$OUTPUT" ] && exit 0
PROJECT=$(resolve_project "$CWD") || exit 0

# Fire and forget — server handles extraction, dedup, and storage
curl -sf "${ENGRAM_URL}/observations/passive" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$(jq -n \
    --arg sid "$SESSION_ID" \
    --arg content "$OUTPUT" \
    --arg project "$PROJECT" \
    --arg source "subagent-stop" \
    '{session_id: $sid, content: $content, project: $project, source: $source}')" \
  > /dev/null 2>&1

exit 0

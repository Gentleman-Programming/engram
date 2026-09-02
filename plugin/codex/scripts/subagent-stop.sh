#!/usr/bin/env bash
# Engram — SubagentStop hook for Codex (synchronous)
#
# Reads the subagent output from stdin and POSTs it to the passive capture
# endpoint. All extraction logic lives in the Go server — this script is
# intentionally minimal.
#
# Runs synchronously (Codex does not support async: true). The HTTP call
# completes well within the 10s timeout on a local engram server.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Codex requires a JSON hook result on every stdout path. Keep diagnostics on
# stderr so a failed best-effort capture cannot corrupt the hook protocol.
emit_hook_result() {
  printf '%s\n' '{}'
}

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
OUTPUT=$(echo "$INPUT" | jq -r '.last_assistant_message // .stdout // empty')

# Nothing to capture if no output
[ -z "$OUTPUT" ] && { emit_hook_result; exit 0; }
PROJECT=$(resolve_project "$CWD") || {
  printf 'engram: unable to resolve project for cwd %q; skipping passive capture\n' "$CWD" >&2
  emit_hook_result
  exit 0
}

# Post to passive capture — server handles extraction, dedup, and storage
curl -sf "${ENGRAM_URL}/observations/passive" \
  -X POST \
  -H "Content-Type: application/json" \
  --max-time 9 \
  -d "$(jq -n \
    --arg sid "$SESSION_ID" \
    --arg content "$OUTPUT" \
    --arg project "$PROJECT" \
    --arg source "subagent-stop" \
    '{session_id: $sid, content: $content, project: $project, source: $source}')" \
  > /dev/null 2>&1

emit_hook_result
exit 0

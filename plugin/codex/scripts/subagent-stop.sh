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
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
# Keep the original extraction for other cases. In the explicit-empty case,
# transport stdout as JSON so jq text-mode newline conversion cannot alter it.
if [ "$(echo "$INPUT" | jq -r '.last_assistant_message == ""')" = true ]; then
  OUTPUT_JSON=$(echo "$INPUT" | jq -c '.stdout // empty')
  [ -z "$OUTPUT_JSON" ] || [ "$OUTPUT_JSON" = '""' ] && { emit_hook_result; exit 0; }
else
  OUTPUT=$(echo "$INPUT" | jq -r '.last_assistant_message // .stdout // empty')
  [ -z "$OUTPUT" ] && { emit_hook_result; exit 0; }
  OUTPUT_JSON=$(jq -n --arg content "$OUTPUT" '$content')
fi
PROJECT=$(resolve_project "$CWD") || {
  printf 'engram: unable to resolve project for cwd %q; skipping passive capture\n' "$CWD" >&2
  emit_hook_result
  exit 0
}

# Resolve, never register: Go owns the live binding and project policy. Keep
# identity in JSON throughout so shell command substitution cannot trim ID bytes.
IDENTITY=$(printf '%s' "$INPUT" | engram_native_hook codex-resolve 2>/dev/null) || {
  emit_hook_result
  exit 0
}
BODY=$(printf '%s' "$IDENTITY" | jq -esc --argjson input "$INPUT" \
  --argjson content "$OUTPUT_JSON" --arg project "$PROJECT" '
  ($input.session_id // null) as $host |
  if length == 1 and ($host | type) == "string" and ($host | length) > 0
    and (.[0].session_id | type) == "string" and (.[0].session_id | length) > 0
    and (.[0].session_id as $id | $id == $host or
      ($id | startswith($host + ":resume:")) and
      ($id | ltrimstr($host + ":resume:") | test("^[0-9]+\\z")))
  then {session_id: .[0].session_id, content: $content, project: $project, source: "subagent-stop"}
  else error("unconfirmed runtime identity") end
' 2>/dev/null) || {
  emit_hook_result
  exit 0
}

# Native resolution has a 1.5s aggregate deadline; project lookup takes at most
# 2s and capture 3s. Network budgets fit together within Codex's outer 10s bound.
# The server handles extraction, dedup, and storage.
curl -sf "${ENGRAM_URL}/observations/passive" \
  -X POST \
  -H "Content-Type: application/json" \
  --max-time 3 \
  -d "$BODY" \
  > /dev/null 2>&1

emit_hook_result
exit 0

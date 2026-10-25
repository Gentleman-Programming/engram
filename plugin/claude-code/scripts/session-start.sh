#!/usr/bin/env bash
# Engram — SessionStart hook for Claude Code
#
# 1. Ensures the engram server is running
# 2. Creates a session in engram
# 3. Auto-imports git-synced chunks if .engram/manifest.json exists
# 4. Injects Memory Protocol instructions + memory context

IMPORT_TIMEOUT_SECS=8
LOCK_TTL_SECS=$((IMPORT_TIMEOUT_SECS + 4))
LOCK_METADATA_STALE_SECS=$((LOCK_TTL_SECS * 5))

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')

# An explicit URL is an external-server opt-in. Only the default local endpoint
# is owned by this data directory, so reachability alone is never sufficient.
if [ "${ENGRAM_MANAGED_LOCAL:-0}" = 1 ]; then
  ENGRAM_INSTANCE_ID=$(engram instance-id 2>/dev/null) || {
    printf '%s\n' "warning: Engram could not resolve its local server identity." >&2
    exit 0
  }
if ! engram_health_matches_instance "$ENGRAM_INSTANCE_ID"; then
  ENGRAM_SERVE_DATA_DIR="${ENGRAM_DATA_DIR:-$HOME/.engram}"
  if mkdir -p "$ENGRAM_SERVE_DATA_DIR" 2>/dev/null && : >> "$ENGRAM_SERVE_DATA_DIR/serve.err.log" 2>/dev/null; then
    ENGRAM_SERVE_ERR_LOG="$ENGRAM_SERVE_DATA_DIR/serve.err.log"
  else
    ENGRAM_SERVE_ERR_LOG="${TMPDIR:-/tmp}/engram-serve.err.log"
  fi
  ENGRAM_CLOUD_AUTOSYNC=1 engram serve > /dev/null 2>> "$ENGRAM_SERVE_ERR_LOG" &
  sleep 0.5
fi
if ! engram_health_matches_instance "$ENGRAM_INSTANCE_ID"; then
  printf '%s\n' "warning: Engram server ownership mismatch; use ENGRAM_URL, ENGRAM_PORT, or ENGRAM_SOCKET to isolate it." >&2
  exit 0
fi
fi

PROJECT=$(resolve_project "$CWD") || PROJECT=""

# Create session. Claude keeps the session ID on --resume, so resume=true lets
# the server register a live continuation when the original session already ended.
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  # Roots created before Claude registered project_owned sessions are shared;
  # resuming one keeps that mode so its continuation stays resolvable.
  OWNERSHIP_MODE="project_owned"
  ENCODED_SESSION_ID=$(printf '%s' "$SESSION_ID" | jq -sRr @uri 2>/dev/null)
  SESSION=""
  if [ -n "$ENCODED_SESSION_ID" ]; then
    SESSION=$(engram_curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION_ID}" --max-time 1 2>/dev/null)
  fi
  if [ -n "$SESSION" ] && printf '%s' "$SESSION" \
    | jq -e --arg id "$SESSION_ID" '.id == $id and .ended_at != null and .ownership_mode == "shared"' >/dev/null 2>&1; then
    OWNERSHIP_MODE="shared"
  fi
  engram_curl -sf "${ENGRAM_URL}/sessions" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg id "$SESSION_ID" --arg project "$PROJECT" --arg dir "$CWD" --arg mode "$OWNERSHIP_MODE" \
      '{id: $id, project: $project, directory: $dir, ownership_mode: $mode, resume: true}')" \
    > /dev/null
fi

# Auto-import git-synced chunks
if [ -f "${CWD}/.engram/manifest.json" ]; then
  (
    cd "$CWD" 2>/dev/null || exit 0
    IMPORT_LOCK="/tmp/engram-sync-import-$(printf '%s' "$CWD" | cksum | cut -d ' ' -f 1).lock"
    write_import_lock_info() {
      LOCK_INFO_TMP="$IMPORT_LOCK/info.$$"
      printf '%s %s\n' "$LOCK_PID" "$LOCK_NOW" > "$LOCK_INFO_TMP" 2>/dev/null \
        && mv "$LOCK_INFO_TMP" "$IMPORT_LOCK/info" 2>/dev/null
    }
    lock_path_age_secs() {
      LOCK_PATH=$1
      LOCK_MTIME=""
      if LOCK_MTIME=$(stat -f %m "$LOCK_PATH" 2>/dev/null); then
        :
      elif LOCK_MTIME=$(stat -c %Y "$LOCK_PATH" 2>/dev/null); then
        :
      else
        return 1
      fi
      case "$LOCK_MTIME" in
        ''|*[!0-9]*) return 1 ;;
      esac
      printf '%s\n' $(( LOCK_NOW - LOCK_MTIME ))
    }
    lock_metadata_is_stale() {
      LOCK_METADATA_PATH=$1
      LOCK_METADATA_AGE=$(lock_path_age_secs "$LOCK_METADATA_PATH") || return 1
      [ "$LOCK_METADATA_AGE" -gt "$LOCK_METADATA_STALE_SECS" ]
    }
    acquire_import_lock() {
      LOCK_PID="${BASHPID:-$$}"
      LOCK_NOW=$(date +%s)
      if mkdir "$IMPORT_LOCK" 2>/dev/null; then
        write_import_lock_info || true
        return 0
      fi

      LOCK_INFO=""
      if [ -f "$IMPORT_LOCK/info" ]; then
        LOCK_INFO=$(cat "$IMPORT_LOCK/info" 2>/dev/null || true)
      fi
      read -r OLD_PID OLD_EPOCH LOCK_INFO_EXTRA <<< "$LOCK_INFO"
      STALE_LOCK=0
      if [ -z "$LOCK_INFO" ] || [ -z "$OLD_PID" ] || [ -z "$OLD_EPOCH" ] || [ -n "$LOCK_INFO_EXTRA" ]; then
        lock_metadata_is_stale "$IMPORT_LOCK" || return 1
        STALE_LOCK=1
      elif [[ "$OLD_PID" == *[!0-9]* ]] || [[ "$OLD_EPOCH" == *[!0-9]* ]]; then
        lock_metadata_is_stale "$IMPORT_LOCK/info" || return 1
        STALE_LOCK=1
      elif kill -0 "$OLD_PID" 2>/dev/null; then
        return 1
      elif [ $(( LOCK_NOW - OLD_EPOCH )) -gt "$LOCK_TTL_SECS" ]; then
        STALE_LOCK=1
      fi

      if [ "$STALE_LOCK" -ne 1 ]; then
        return 1
      fi
      rm -f "$IMPORT_LOCK/info" 2>/dev/null || true
      rmdir "$IMPORT_LOCK" 2>/dev/null || return 1
      if mkdir "$IMPORT_LOCK" 2>/dev/null; then
        write_import_lock_info || true
        return 0
      fi
      return 1
    }
    if ! acquire_import_lock; then
      exit 0
    fi
    trap 'rm -f "$IMPORT_LOCK/info" 2>/dev/null || true; rmdir "$IMPORT_LOCK" 2>/dev/null || true' EXIT
    if command -v timeout >/dev/null 2>&1; then
      timeout "${IMPORT_TIMEOUT_SECS}s" engram sync --import >/dev/null 2>&1 || true
    else
      engram sync --import >/dev/null 2>&1 &
      IMPORT_PID=$!
      (sleep "$IMPORT_TIMEOUT_SECS"; kill "$IMPORT_PID" 2>/dev/null || true) &
      WAITER_PID=$!
      wait "$IMPORT_PID" 2>/dev/null || true
      kill "$WAITER_PID" 2>/dev/null || true
    fi
  ) >/dev/null 2>&1 &
fi

# Fetch memory context.
#
# compact=1 renders observation bullets as `- [type] **title**` instead of
# appending 300 chars of body: no row disappears, only the inline preview,
# and the body is one mem_get_observation away when the agent actually wants
# it. pinned=20 puts a ceiling on the one section that never had one.
# max_bytes=16384 caps the final injected context without changing its defaults.
CONTEXT=""
if [ -n "$PROJECT" ]; then
  ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
  CONTEXT=$(engram_curl -sf "${ENGRAM_URL}/context?project=${ENCODED_PROJECT}&compact=1&pinned=20&max_bytes=16384" --max-time 3 | jq -r '.context // empty' 2>/dev/null)
fi

# Resolve protocol verbosity mode for this slug. All slim/full branching
# (including the engram-version floor check) lives in Go — see `engram
# protocol-mode`. A missing/old engram binary or an unrecognized subcommand
# never yields "slim" here, so this always defaults safely to full. $mode is
# NEVER echoed/logged to this hook's own stdout.
mode=$(engram protocol-mode claude-code 2>/dev/null)
if [ "$mode" != "slim" ]; then
  mode="full"
fi

# Inject Memory Protocol + context — stdout goes to Claude as additionalContext
if [ "$mode" != "slim" ]; then
cat <<'PROTOCOL'
## Engram Persistent Memory — ACTIVE PROTOCOL

You have engram memory tools. The agent MUST follow this protocol without waiting for activation.

### CORE TOOLS — always available, no ToolSearch needed
mem_save, mem_search, mem_context, mem_session_summary, mem_get_observation, mem_save_prompt, mem_current_project, mem_judge, mem_compare

Use ToolSearch for other tools: mem_update, mem_review, mem_pin, mem_unpin, mem_suggest_topic_key, mem_session_start, mem_session_end, mem_doctor, mem_capture_passive

### SELECTIVE MEMORY
Persist verified knowledge that is durable and costly to reconstruct: decisions with rationale,
non-obvious root causes, real constraints, and resumption-relevant pending work.
Group related conclusions deliberately, not every task, response, or event.
Exclude secrets, private data, speculation, raw dumps, and routine progress.

For selected writes, use mem_search on the bounded topic in the intended project and scope before writing.
Read candidates in full with mem_get_observation; snippets are not identity evidence.
For the same bounded topic use mem_update by ID: keep topic_key, preserve every still-valid fact,
and record superseded facts as history. For a genuinely new topic use mem_save with a deliberate,
distinct topic_key. If already covered, write nothing.
On ambiguous identity or failed lookup, do not overwrite or fall back to a duplicate save.
A failed read is not evidence of absence. A suggested topic_key is a hint, not proof of identity.
Never merge across projects or scopes. Structure content as What, Why, Where, Learned.

Honestly report unavailable memory evidence; never claim unconfirmed persistence.
Do not blindly repeat writes. Complete unrelated work even if bookkeeping fails.
Do not add per-event learning sections or ceremonial delivery blocks.

### DELIVERY GUARANTEE
Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

### SEARCH AND SESSION CLOSE
Search when materially useful for recall or related work; mere project mentions do not require a search.
For recall: mem_context, then mem_search with distinctive keywords, then mem_get_observation for full content.
Narrow by project/type after broad recall as needed.
At significant explicit closure or handoff, call mem_session_summary with Goal, Instructions,
Discoveries, Accomplished, Next Steps, Relevant Files. Ordinary task completion does not require a summary.
Summary persistence does not end the session; Claude's SessionEnd hook owns lifecycle closure.

### PASSIVE CAPTURE — adapter-specific safety net
Claude's SubagentStop hook forwards eligible delegated/subagent output for server-side extraction.
Ordinary main-agent responses are not automatically captured; the section alone does not guarantee storage.
Use `mem_save` as the primary path for significant, verified knowledge. Explicit
`mem_capture_passive(content)` extraction is a safety net, not a replacement for `mem_save`.
PROTOCOL
fi

# Inject memory context if available
if [ -n "$CONTEXT" ]; then
  printf "\n%s\n" "$CONTEXT"
fi

exit 0

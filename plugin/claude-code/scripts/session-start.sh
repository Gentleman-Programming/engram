#!/bin/bash
# Engram — Unified Memory SessionStart hook
#
# 1. Ensures engram server is running
# 2. Creates session, migrates project name if needed
# 3. Runs hot cache garbage collection (staleness)
# 4. Injects unified protocol + hot cache as additionalContext

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
OLD_PROJECT=$(basename "$CWD")
PROJECT=$(detect_project "$CWD")

# Ensure engram server is running
if ! curl -sf "${ENGRAM_URL}/health" --max-time 1 > /dev/null 2>&1; then
  engram serve &>/dev/null &
  sleep 0.5
fi

# Migrate project name if changed
if [ "$OLD_PROJECT" != "$PROJECT" ] && [ -n "$OLD_PROJECT" ] && [ -n "$PROJECT" ]; then
  curl -sf "${ENGRAM_URL}/projects/migrate" \
    -X POST -H "Content-Type: application/json" \
    -d "$(jq -n --arg old "$OLD_PROJECT" --arg new "$PROJECT" \
      '{old_project: $old, new_project: $new}')" \
    > /dev/null 2>&1
fi

# Create session
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  curl -sf "${ENGRAM_URL}/sessions" \
    -X POST -H "Content-Type: application/json" \
    -d "$(jq -n --arg id "$SESSION_ID" --arg project "$PROJECT" --arg dir "$CWD" \
      '{id: $id, project: $project, directory: $dir}')" \
    > /dev/null 2>&1
fi

# Auto-import git-synced chunks
if [ -f "${CWD}/.engram/manifest.json" ]; then
  engram sync --import 2>/dev/null
fi

# Run staleness garbage collection
curl -sf -X POST "${ENGRAM_URL}/observations/hot/gc" --max-time 2 > /dev/null 2>&1

# Fetch hot observations
HOT_JSON=$(curl -sf "${ENGRAM_URL}/observations/hot" --max-time 3 2>/dev/null)

# Get total observation count for footer
STATS_JSON=$(curl -sf "${ENGRAM_URL}/stats" --max-time 1 2>/dev/null)
TOTAL_OBS=$(echo "$STATS_JSON" | jq -r '.total_observations // 0' 2>/dev/null)

# Render hot cache as markdown grouped by type
HOT_MARKDOWN=""
HOT_COUNT=0
if [ -n "$HOT_JSON" ] && [ "$HOT_JSON" != "null" ] && [ "$HOT_JSON" != "[]" ]; then
  HOT_COUNT=$(echo "$HOT_JSON" | jq 'length' 2>/dev/null || echo 0)

  # Group by type and render
  for TYPE in user feedback reference project decision architecture bugfix pattern discovery config session_summary; do
    TYPE_OBS=$(echo "$HOT_JSON" | jq -r --arg t "$TYPE" \
      '[.[] | select(.type == $t)] | if length > 0 then . else empty end' 2>/dev/null)

    if [ -n "$TYPE_OBS" ] && [ "$TYPE_OBS" != "null" ]; then
      # Title-case lookup (avoids GNU sed-specific \b\u extensions)
      case "$TYPE" in
        user) HEADER="User" ;; feedback) HEADER="Feedback" ;; reference) HEADER="Reference" ;;
        project) HEADER="Project" ;; decision) HEADER="Decision" ;; architecture) HEADER="Architecture" ;;
        bugfix) HEADER="Bugfix" ;; pattern) HEADER="Pattern" ;; discovery) HEADER="Discovery" ;;
        config) HEADER="Config" ;; session_summary) HEADER="Session Summary" ;; *) HEADER="$TYPE" ;;
      esac
      HOT_MARKDOWN="${HOT_MARKDOWN}
### ${HEADER}"
      ITEMS=$(echo "$TYPE_OBS" | jq -r '.[] | "- **\(.title)** (id:\(.id)): \(.content | split("\n")[0])"' 2>/dev/null)
      HOT_MARKDOWN="${HOT_MARKDOWN}
${ITEMS}
"
    fi
  done
fi

# Apply 15KB size cap per spec (soft limit)
HOT_SIZE=$(printf '%s' "$HOT_MARKDOWN" | wc -c)
if [ "$HOT_SIZE" -gt 15360 ]; then
  # Truncate to ~15KB and append note
  HOT_MARKDOWN=$(printf '%s' "$HOT_MARKDOWN" | head -c 15360)
  TRUNCATED_COUNT=$((HOT_COUNT - $(printf '%s' "$HOT_MARKDOWN" | grep -c '^- \*\*')))
  HOT_MARKDOWN="${HOT_MARKDOWN}
...
(${TRUNCATED_COUNT} observations omitted — size cap. Use mem_hot() to see all, mem_demote(id) to free space.)"
fi

# Output unified protocol + hot cache
cat <<PROTOCOL
## Persistent Memory

You have a persistent memory system backed by engram. Observations survive across sessions.

### Saving
Call \`mem_save\` IMMEDIATELY after:
- Decisions (architecture, convention, tool choice)
- Bug fixes (include root cause)
- Discoveries, gotchas, edge cases
- User preferences or constraints
- Confirmed recommendations

Format: **What** / **Why** / **Where** / **Learned**
For feedback: Rule, then **Why:** and **How to apply:**

Types: user, feedback, reference, project, decision, architecture,
       bugfix, pattern, config, discovery, session_summary

user/feedback/reference auto-promote to hot cache (always in context).
Everything else is searchable via mem_search / mem_vector_search.

### Recall
- Hot cache is already in your context (see below)
- Use mem_search for anything not hot: prior project work, old decisions
- Search on first message if user references prior work

### Hot Cache Management
- mem_promote(id) — add to always-in-context cache
- mem_demote(id) — move to search-only
- mem_hot() — list current hot observations

### Session End
Call mem_session_summary before saying "done".

### What NOT to save
- Code patterns derivable from reading the codebase
- Git history (use git log)
- Debugging solutions (the fix is in the code)
- Ephemeral task details for the current conversation

---

## Persistent Memory (Hot Cache)
${HOT_MARKDOWN}
---
Hot: ${HOT_COUNT} observations | Total: ${TOTAL_OBS} | Search: mem_search("query") | Promote: mem_promote(id) | Demote: mem_demote(id)
PROTOCOL

exit 0

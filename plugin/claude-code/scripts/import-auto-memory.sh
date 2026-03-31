#!/bin/bash
# One-time migration: import auto memory files into engram with hot=true
#
# Usage: bash import-auto-memory.sh [memory-dir]
# Default memory-dir: ~/.claude/projects/-home-zach/memory/

set -euo pipefail

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
MEMORY_DIR="${1:-$HOME/.claude/projects/-home-zach/memory}"

if [ ! -d "$MEMORY_DIR" ]; then
  echo "Error: memory directory not found: $MEMORY_DIR" >&2
  exit 1
fi

# Ensure server is running
if ! curl -sf "${ENGRAM_URL}/health" --max-time 1 > /dev/null 2>&1; then
  echo "Error: engram server not running on port $ENGRAM_PORT" >&2
  exit 1
fi

# Create synthetic session
echo "Creating import session..."
curl -sf "${ENGRAM_URL}/sessions" \
  -X POST -H "Content-Type: application/json" \
  -d '{"id":"auto-memory-import","project":"global","directory":"'"$MEMORY_DIR"'"}' \
  > /dev/null 2>&1 || true  # OK if session already exists

IMPORTED=0
SKIPPED=0
ERRORS=0

for FILE in "$MEMORY_DIR"/*.md; do
  BASENAME=$(basename "$FILE")
  # Skip MEMORY.md index file
  if [ "$BASENAME" = "MEMORY.md" ]; then
    continue
  fi

  # Parse frontmatter
  CONTENT=$(cat "$FILE")
  if ! echo "$CONTENT" | head -1 | grep -q '^---'; then
    echo "SKIP (no frontmatter): $BASENAME"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  # Extract frontmatter fields
  FRONTMATTER=$(echo "$CONTENT" | sed -n '2,/^---$/p' | head -n -1)
  NAME=$(echo "$FRONTMATTER" | grep '^name:' | sed 's/^name: *//')
  DESCRIPTION=$(echo "$FRONTMATTER" | grep '^description:' | sed 's/^description: *//')
  TYPE=$(echo "$FRONTMATTER" | grep '^type:' | sed 's/^type: *//')

  # Use description as title (more readable than slug name)
  TITLE="${DESCRIPTION:-$NAME}"
  if [ -z "$TITLE" ]; then
    echo "SKIP (no title): $BASENAME"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  # Extract body (everything after second ---)
  BODY=$(echo "$CONTENT" | sed '1,/^---$/d' | sed '1,/^---$/d')
  if [ -z "$BODY" ]; then
    echo "SKIP (empty body): $BASENAME"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  # Dedup check: search for similar observations (spec requires >80% word overlap)
  ENCODED_TITLE=$(printf '%s' "$TITLE" | jq -sRr @uri)
  EXISTING=$(curl -sf "${ENGRAM_URL}/search?q=${ENCODED_TITLE}&limit=3" --max-time 1 2>/dev/null)
  if [ -n "$EXISTING" ] && [ "$EXISTING" != "[]" ] && [ "$EXISTING" != "null" ]; then
    # Check word overlap between import title and existing titles
    IMPORT_WORDS=$(echo "$TITLE" | tr '[:upper:]' '[:lower:]' | tr -cs '[:alnum:]' '\n' | sort -u)
    IMPORT_COUNT=$(echo "$IMPORT_WORDS" | wc -l)
    MATCH_FOUND=false
    for i in $(seq 0 2); do
      EXISTING_TITLE=$(echo "$EXISTING" | jq -r ".[$i].title // empty" 2>/dev/null)
      [ -z "$EXISTING_TITLE" ] && continue
      EXISTING_WORDS=$(echo "$EXISTING_TITLE" | tr '[:upper:]' '[:lower:]' | tr -cs '[:alnum:]' '\n' | sort -u)
      OVERLAP=$(comm -12 <(echo "$IMPORT_WORDS") <(echo "$EXISTING_WORDS") | wc -l)
      if [ "$IMPORT_COUNT" -gt 0 ] && [ "$(( OVERLAP * 100 / IMPORT_COUNT ))" -ge 80 ]; then
        echo "SKIP (>80% overlap with: '$EXISTING_TITLE'): $BASENAME"
        SKIPPED=$((SKIPPED + 1))
        MATCH_FOUND=true
        break
      fi
    done
    [ "$MATCH_FOUND" = true ] && continue
  fi

  # Create observation (project=global to match the synthetic session)
  RESULT=$(curl -sf "${ENGRAM_URL}/observations" \
    -X POST -H "Content-Type: application/json" \
    -d "$(jq -n \
      --arg sid "auto-memory-import" \
      --arg type "${TYPE:-manual}" \
      --arg title "$TITLE" \
      --arg content "$BODY" \
      '{session_id: $sid, type: $type, title: $title, content: $content, project: "global"}')" \
    2>/dev/null)

  if [ -z "$RESULT" ]; then
    echo "ERROR (save failed): $BASENAME"
    ERRORS=$((ERRORS + 1))
    continue
  fi

  # Extract ID and promote to hot
  OBS_ID=$(echo "$RESULT" | jq -r '.id // empty')
  if [ -n "$OBS_ID" ]; then
    curl -sf "${ENGRAM_URL}/observations/${OBS_ID}/hot" \
      -X PUT -H "Content-Type: application/json" \
      -d '{"hot": true}' \
      > /dev/null 2>&1
    echo "OK (id:${OBS_ID}, hot): $BASENAME — $TITLE"
    IMPORTED=$((IMPORTED + 1))
  else
    echo "ERROR (no id returned): $BASENAME"
    ERRORS=$((ERRORS + 1))
  fi
done

echo ""
echo "Migration complete: imported=$IMPORTED skipped=$SKIPPED errors=$ERRORS"

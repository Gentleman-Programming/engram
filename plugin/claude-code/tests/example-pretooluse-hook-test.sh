#!/bin/bash
# Tests for ~/.claude/hooks/engram/pretooluse-project-inject.sh
#
# Each test pipes synthetic Claude Code hook input to the hook script and
# asserts the output JSON matches expectations. No live Engram server needed —
# this is a pure unit test of the hook's decision logic.
#
# Run: bash ~/.claude/hooks/engram/tests/test-pretooluse-hook.sh
# Exit 0 = all pass, exit 1 = some failed.

set -u
HOOK="$(cd "$(dirname "$0")/.." && pwd)/pretooluse-project-inject.sh"

if [ ! -x "$HOOK" ]; then
  echo "FAIL: hook not executable at $HOOK"
  exit 1
fi

PASS=0
FAIL=0
FAILED_TESTS=()

# Run hook with input; assert output via jq query
# Args: name, input_json, jq_query, expected_value
run_test() {
  local name="$1"
  local input="$2"
  local query="$3"
  local expected="$4"

  local output
  output=$(printf '%s' "$input" | bash "$HOOK" 2>/dev/null)
  local actual
  actual=$(printf '%s' "$output" | jq -r "$query" 2>/dev/null)

  if [ "$actual" = "$expected" ]; then
    echo "  PASS: $name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $name"
    echo "    query:    $query"
    echo "    expected: $expected"
    echo "    actual:   $actual"
    echo "    output:   $output"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("$name")
  fi
}

echo "=== PreToolUse hook unit tests ==="

# ─── pass-through: not an Engram tool ──────────────────────────────────────
run_test "non-engram tool passes through" \
  '{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"abc"}' \
  '.hookSpecificOutput.permissionDecision' \
  'allow'

run_test "non-engram tool has no updatedInput" \
  '{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

# ─── pass-through: read tools (preserve cross-project search) ──────────────
run_test "mem_search passes through (no inject)" \
  '{"tool_name":"mcp__engram-onprem__mem_search","tool_input":{"query":"x"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_context passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_context","tool_input":{},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_get_observation passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_get_observation","tool_input":{"id":1},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_timeline passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_timeline","tool_input":{"obs_id":1},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_suggest_topic_key passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_suggest_topic_key","tool_input":{"title":"x"},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

# ─── pass-through: stateless / explicit tools ──────────────────────────────
run_test "mem_stats passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_stats","tool_input":{},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_judge passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_judge","tool_input":{"judgment_id":"x","relation":"not_conflict"},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_delete passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_delete","tool_input":{"id":1},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

run_test "mem_current_project passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_current_project","tool_input":{},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

# ─── inject: write tools ───────────────────────────────────────────────────
run_test "mem_save injects session_id" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"claude-uuid-1"}' \
  '.hookSpecificOutput.updatedInput.session_id' \
  'claude-uuid-1'

run_test "mem_save injects directory (CWD)" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"claude-uuid-1"}' \
  '.hookSpecificOutput.updatedInput.directory' \
  '/Users/johnrandall/dev/johnrandall.com'

run_test "mem_save preserves original args" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"my title","content":"my content","type":"discovery"},"cwd":"/Users/johnrandall/admin-technical","session_id":"claude-uuid-2"}' \
  '.hookSpecificOutput.updatedInput.title' \
  'my title'

run_test "mem_save preserves type field" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c","type":"discovery"},"cwd":"/Users/johnrandall/admin-technical","session_id":"claude-uuid-2"}' \
  '.hookSpecificOutput.updatedInput.type' \
  'discovery'

run_test "mem_save preserves scope=personal" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c","scope":"personal"},"cwd":"/Users/johnrandall/admin-technical","session_id":"claude-uuid-3"}' \
  '.hookSpecificOutput.updatedInput.scope' \
  'personal'

run_test "mem_save with personal scope still gets session_id injected" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c","scope":"personal"},"cwd":"/Users/johnrandall/admin-technical","session_id":"claude-uuid-3"}' \
  '.hookSpecificOutput.updatedInput.session_id' \
  'claude-uuid-3'

run_test "mem_save_prompt injects directory" \
  '{"tool_name":"mcp__engram-onprem__mem_save_prompt","tool_input":{"content":"x"},"cwd":"/Users/johnrandall/dev/jkre-bookkeeping","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput.directory' \
  '/Users/johnrandall/dev/jkre-bookkeeping'

run_test "mem_session_summary injects directory" \
  '{"tool_name":"mcp__engram-onprem__mem_session_summary","tool_input":{"summary":"x"},"cwd":"/Users/johnrandall/admin-technical","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput.directory' \
  '/Users/johnrandall/admin-technical'

run_test "mem_session_start injects session_id" \
  '{"tool_name":"mcp__engram-onprem__mem_session_start","tool_input":{},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput.session_id' \
  'abc'

run_test "mem_capture_passive injects directory" \
  '{"tool_name":"mcp__engram-onprem__mem_capture_passive","tool_input":{"content":"x"},"cwd":"/Users/johnrandall/dev/jkre-bookkeeping","session_id":"abc"}' \
  '.hookSpecificOutput.updatedInput.directory' \
  '/Users/johnrandall/dev/jkre-bookkeeping'

run_test "mem_update injects session_id" \
  '{"tool_name":"mcp__engram-onprem__mem_update","tool_input":{"id":1,"title":"new"},"cwd":"/Users/johnrandall/admin-technical","session_id":"upd-abc"}' \
  '.hookSpecificOutput.updatedInput.session_id' \
  'upd-abc'

# ─── pass-through: explicit session_id provided by agent ──────────────────
run_test "mem_save with explicit session_id passes through" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c","session_id":"agent-set"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"hook-input"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

# ─── edge case: missing claude_session_id ─────────────────────────────────
run_test "mem_save without claude session_id falls through" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c"},"cwd":"/Users/johnrandall/dev/johnrandall.com"}' \
  '.hookSpecificOutput.updatedInput' \
  'null'

# ─── all decisions are "allow" (hook never blocks) ────────────────────────
run_test "all permissionDecisions are allow (write tool)" \
  '{"tool_name":"mcp__engram-onprem__mem_save","tool_input":{"title":"t","content":"c"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"abc"}' \
  '.hookSpecificOutput.permissionDecision' \
  'allow'

run_test "all permissionDecisions are allow (read tool)" \
  '{"tool_name":"mcp__engram-onprem__mem_search","tool_input":{"query":"x"},"cwd":"/Users/johnrandall/dev/johnrandall.com","session_id":"abc"}' \
  '.hookSpecificOutput.permissionDecision' \
  'allow'

# ─── summary ──────────────────────────────────────────────────────────────
echo
echo "Results: $PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
  echo "Failed tests:"
  for t in "${FAILED_TESTS[@]}"; do
    echo "  - $t"
  done
  exit 1
fi
exit 0

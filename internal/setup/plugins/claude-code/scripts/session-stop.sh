#!/bin/bash
# Engram — Stop hook for Claude Code (async)
#
# Tracks tool call count per session. Runs async so it doesn't
# block Claude's response.

# Nothing heavy to do here — the Memory Protocol in the skill
# instructs the agent to call mem_session_summary before ending.
# This hook exists as a placeholder for future heartbeat/tracking.

exit 0

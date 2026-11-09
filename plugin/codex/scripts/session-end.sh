#!/usr/bin/env bash
# Close only the prior server-confirmed binding; Go owns validation and deadlines.
engram hook codex-session-end >/dev/null 2>&1 || true
exit 0

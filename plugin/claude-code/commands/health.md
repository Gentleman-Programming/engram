---
description: Check engram server status
allowed-tools: [Bash]
---

# Engram Health Check

Check if the engram server is running and healthy.

## Steps

1. Run: `curl -sf http://127.0.0.1:${ENGRAM_PORT:-7437}/health --max-time 2`
2. If success: show status, version
3. If fails: inform server is not running, suggest `engram serve`

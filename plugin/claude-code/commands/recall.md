---
description: Smart memory recall with progressive search
argument-hint: <query>
allowed-tools: [mcp__plugin_engram_engram__mem_context, mcp__plugin_engram_engram__mem_search, mcp__plugin_engram_engram__mem_get_observation]
---

# Engram Recall

Recall memories about: $ARGUMENTS

Progressive disclosure search:
1. First call mem_context to check recent session history
2. If not found in context, call mem_search with: $ARGUMENTS
3. If a result matches, call mem_get_observation for full untruncated content
4. Present findings clearly with source and date

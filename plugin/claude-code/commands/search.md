---
description: Search engram memories
argument-hint: <query>
allowed-tools: [mcp__plugin_engram_engram__mem_search, mcp__plugin_engram_engram__mem_get_observation]
---

# Search Engram Memories

Search for: $ARGUMENTS

## Steps

1. Call mem_search with query: $ARGUMENTS
2. For each result, show: title, type, date, content preview
3. If a result looks relevant, fetch full content with mem_get_observation
4. If no results found, say so and suggest alternative search terms

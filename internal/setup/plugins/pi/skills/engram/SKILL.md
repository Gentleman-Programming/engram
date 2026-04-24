---
name: engram
description: Memory protocol guidance for Engram's Pi integration.
---

## Engram Persistent Memory — Protocol (Pi)

You have access to Engram persistent memory tools (`mem_*`) as native Pi tools from the extension.

### Save immediately after
- Bug fixes
- Architecture/design decisions
- Non-obvious discoveries
- Config changes
- Established patterns
- User preferences

### Search rules
- On recall requests, call `mem_context` first, then `mem_search`.
- Proactive lookup is allowed only when continuity likelihood is high.
- Startup policy is conservative: notify memory availability, do not auto-inject full context.

### Session close
Before ending a session, call `mem_session_summary` with goal, discoveries, accomplished work, next steps, and relevant files.

### After compaction
If a compacted summary is present, first call `mem_session_summary` with that content, then call `mem_context`.

### Compaction hook behavior
- `session_before_compact`: extension injects `FIRST ACTION REQUIRED` into compaction instructions when supported by event shape.
- `session_compact`: extension attempts to persist the compacted summary through Engram session summary endpoint and notifies whether persistence succeeded.
- If summary extraction/persistence is unavailable, run `/engram-recovery` and continue with manual `mem_context`.

### Pi adapter limitations (validated v0.70.0 contract)
- Engram does not auto-inject full previous context after compaction; `mem_context` stays manual.
- Recovery guidance remains available through `/engram-recovery` for runtimes with partial hook payloads.

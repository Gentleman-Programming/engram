---
name: engram-memory-protocol
description: >
  Persistent memory discipline for Engram contributors.
  Trigger: Decisions, bugfixes, discoveries, preferences, or session closure.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Making architecture or implementation decisions
- Fixing bugs with non-obvious root causes
- Discovering patterns, gotchas, or user preferences
- Closing a session or after compaction

---

## Save Rules

Call `mem_save` immediately after:
- decision
- bugfix
- pattern/discovery
- config/preference changes

Use structured content:
- What
- Why
- Where
- Learned

Use stable `topic_key` for evolving topics.

---

## Search Rules

- On recall requests: `mem_context` first, then `mem_search`.
- Before similar work: run proactive `mem_search`.
- On first message: if user references the project, a feature, or a problem, call `mem_search` with their keywords before responding.

---

## Session Close Rules

Before saying done/listo:
1. Call `mem_session_summary`.
2. Include goal, discoveries, accomplished, next steps, relevant files.

After compaction:
1. Save summary first.
2. Recover context.
3. Continue work.

---

## Delivery Guarantee

Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

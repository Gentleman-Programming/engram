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

The agent MUST follow this protocol without waiting for activation.
Canonical prose lives in DOCS.md; this contributor skill is manually maintained.

## Selective Memory

Persist verified knowledge that is durable and costly to reconstruct: decisions with rationale,
non-obvious root causes, real constraints, and resumption-relevant pending work.
Group related conclusions deliberately, not every task, response, or event.
Exclude secrets, private data, speculation, raw dumps, and routine progress.

Search when materially useful for recall or related work; mere project mentions do not require a search.
For recall: mem_context, then mem_search with distinctive keywords, then mem_get_observation for full content.
Narrow by project/type after broad recall as needed.

For selected writes, use mem_search on the bounded topic in the intended project and scope before writing.
Read candidates in full with mem_get_observation; snippets are not identity evidence.
For the same bounded topic use mem_update by ID: keep topic_key, preserve every still-valid fact,
and record superseded facts as history. For a genuinely new topic use mem_save with a deliberate,
distinct topic_key. If already covered, write nothing.
On ambiguous identity or failed lookup, do not overwrite or fall back to a duplicate save.
A failed read is not evidence of absence. A suggested topic_key is a hint, not proof of identity.
Never merge across projects or scopes. Structure content as What, Why, Where, Learned.

## Session Close Rules

At significant explicit closure or handoff, call mem_session_summary with Goal, Instructions,
Discoveries, Accomplished, Next Steps, Relevant Files. Ordinary task completion does not require a summary.
After relevant compaction, follow the host's recovery instructions; otherwise persist the compacted
summary first, recover context, then continue. Summary persistence does not itself end a session;
retain the host's attribution, registration, privacy, and lifecycle obligations.

Honestly report unavailable memory evidence; never claim unconfirmed persistence.
Do not blindly repeat writes. Complete unrelated work even if bookkeeping fails.
Do not add per-event learning sections or ceremonial delivery blocks.

---

## Delivery Guarantee

Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

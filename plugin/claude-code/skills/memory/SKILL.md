---
name: engram-memory
description: "ALWAYS ACTIVE — Persistent memory protocol. Triggers: save decisions/bugs/patterns/discoveries. Search: before repeat work. Summary: before done/listo."
triggers:
  - decision made
  - bug fixed
  - pattern discovered
  - session ending
  - context compacted
  - starting similar work
---

## Purpose

Manage persistent memory across sessions using Engram tools. Save non-obvious knowledge
automatically. Search before repeating work. Always close sessions with a summary.

## Variables

- **mem_save**: Save an observation (decision, bugfix, pattern, discovery, config, preference)
- **mem_search**: Full-text search across all observations (FTS5)
- **mem_context**: Fetch context from recent sessions for this project
- **mem_session_summary**: Structured summary of current session
- **mem_update**: Update an existing observation by ID
- **mem_suggest_topic_key**: Get a canonical key for upsert-safe topic tracking
- **agent_name**: Your agent identifier (e.g., "backend", "frontend") — use in scope field

## Instructions

1. **Save proactively** — do not wait for the user to ask
2. **Use topic_key for evolving topics** — same key = upsert (no duplicates)
3. **Search before starting** — check if this was done before
4. **Always close with summary** — mem_session_summary before "done"
5. **After compaction** — mem_session_summary first, then mem_context, then continue

## Workflow

1. Check: Does this trigger match a Cookbook case? → find matching If/Then/Example
2. Execute: Call the appropriate tool with the specified fields
3. Verify: Confirm the response includes an ID (save) or content (search)

## Cookbook

<If : You just made an architecture or design decision>
<Then : Call mem_save with type=decision, include topic_key like "architecture/auth-model">
<Example : mem_save(title="Chose SQLAlchemy over raw SQL", type="decision", topic_key="architecture/orm-choice", content="What: Selected SQLAlchemy\nWhy: Team familiarity + migration support\nWhere: models/\nLearned: async session requires AsyncSession, not Session")>

<If : You just fixed a bug>
<Then : Call mem_save with type=bugfix, include root cause and affected files>
<Example : mem_save(title="Fixed N+1 query in UserList", type="bugfix", content="What: Added .joinedload(User.roles)\nWhy: ORM was issuing per-row queries\nWhere: api/users.py:list_users\nLearned: SQLAlchemy lazy loads by default — always check explain()")>

<If : You discovered a non-obvious pattern or gotcha>
<Then : Call mem_save with type=pattern or type=discovery, short searchable title>
<Example : mem_save(title="Pydantic v2 model_validate replaces parse_obj", type="pattern", content="What: parse_obj() removed in Pydantic v2\nWhy: API breaking change\nWhere: all schemas\nLearned: Use model_validate() everywhere")>

<If : You learned a user preference or constraint>
<Then : Call mem_save with type=preference, scope=personal>
<Example : mem_save(title="User prefers Spanish in code comments", type="preference", scope="personal", content="Always write inline comments in Spanish, docstrings in English")>

<If : You completed a significant feature or config setup>
<Then : Call mem_save with type=decision or type=config, list affected files>
<Example : mem_save(title="Added Celery beat for async email queue", type="config", content="What: Configured Celery + Redis\nWhere: docker-compose.yml, workers/email.py\nLearned: CELERY_TASK_SERIALIZER must be json not pickle for Redis 7+")>

<If : A topic was already saved and you have an update>
<Then : Call mem_save with the SAME topic_key to upsert (update in place)>
<Example : mem_save(title="Auth model update: added refresh tokens", topic_key="architecture/auth-model", type="decision", content="Updated: Added JWT refresh token rotation\nPrevious: stateless JWT only")>

<If : The user asks "what did we do", "remember", "acordate", "recall", or references past work>
<Then : First call mem_context (fast), then mem_search if not found>
<Example : mem_context(project="my-project") → if empty → mem_search(query="auth JWT tokens")>

<If : You are starting work on something that might have been done before>
<Then : Call mem_search proactively before starting>
<Example : Starting auth work → mem_search(query="authentication JWT session") → if found, read existing decisions first>

<If : You are saying "done", "listo", "that's it", or ending a session>
<Then : Call mem_session_summary FIRST before any closing message>
<Example : mem_session_summary(goal="Implement payment flow", discoveries=["Stripe requires idempotency keys"], accomplished=["Created checkout session endpoint", "Added webhook handler"], next_steps=["Add retry logic"], relevant_files=["api/payments.py", "webhooks/stripe.py"])>

<If : Context compaction just happened (you see a compaction notice or FIRST ACTION REQUIRED)>
<Then : Call mem_session_summary with compacted content FIRST, then mem_context, then resume>
<Example : mem_session_summary(goal="[from compacted summary]", accomplished=["[from compacted summary]"], next_steps=["[from compacted summary]"]) → then mem_context() → then continue work>

<If : You are a subagent (invoked via Task) and finishing your task>
<Then : Include "## Key Learnings:" section in your response with numbered list>
<Example : ## Key Learnings:\n\n1. [Specific technical insight from this task]\n2. [Pattern discovered or best practice applied]\n3. [Reusable knowledge for future tasks]>

<If : You are unsure what topic_key to use for an evolving topic>
<Then : Call mem_suggest_topic_key with a description, use the suggested key consistently>
<Example : mem_suggest_topic_key(description="our choice of ORM and query patterns") → returns "architecture/orm-strategy" → use this key every time>

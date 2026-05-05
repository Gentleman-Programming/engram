[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Repository Map](repository-map.md) | [Next: Interfaces →](interfaces.md)

# Memory Core

**The memory core is `internal/store`: local SQLite + FTS5 is Engram's source of truth.** Interfaces should translate user/agent intent into store operations instead of reimplementing persistence rules.

## Save and retrieve flow

The memory flow does not start in the database. It starts with the agent deciding something is worth remembering.

```text
1. The agent finishes significant work
   bugfix, decision, discovery, config, convention, session summary

2. The agent calls an MCP tool
   mem_save / mem_session_summary / mem_save_prompt / mem_capture_passive

3. internal/mcp resolves the project and validates the contract
   cwd → .engram/config.json → git remote/root → child repo → basename

4. internal/store persists
   sessions / observations / user_prompts / memory_relations / sync_mutations
   FTS5 indexes for search

5. Next session
   mem_context → mem_search → mem_get_observation when full detail is needed
```

## Store mental entities

| Entity | Purpose | Relevant files |
|---|---|---|
| `sessions` | Groups work from one agent session. | `internal/store/store.go`, `internal/mcp/activity.go` |
| `observations` | Curated memories: decisions, bugs, patterns, discoveries, summaries. | `internal/store/store.go`, `internal/store/store_test.go` |
| `observations_fts` | FTS5 search index. | `internal/store/store.go`, `DOCS.md#database-schema` |
| `user_prompts` / `prompts_fts` | User prompt as retrievable context. | `internal/store/store.go`, `internal/server/server.go` |
| `memory_relations` | Relationships/judgments between memories for semantic conflict surfacing. | `internal/store/relations.go`, `internal/mcp/mcp_judge_test.go` |
| `sync_mutations` | Queue of changes for sync/autosync. | `internal/store/store.go`, `internal/sync/sync.go`, `internal/cloud/autosync/manager.go` |
| `sync_apply_deferred` | Pull mutations deferred because dependencies are missing. | `internal/store/sync_apply_test.go`, `internal/server/server.go` |

For schema details, use [DOCS.md — Database Schema](../../DOCS.md#database-schema).

## Memory invariants

- Agent protocol and tool guides expect structured `mem_save` content: **What / Why / Where / Learned**. The persistence layer does not automatically reject poorly formed prose; discipline lives in agent instructions and review.
- `topic_key` is for evolving topics; distinct decisions are not mixed under the same key.
- `scope=project` is the default; `scope=personal` exists for non-shared memory.
- Soft delete (`deleted_at`) hides data without physically deleting it unless explicit hard delete is used.
- Write tools resolve the project from cwd/config; do not invent a project when there is ambiguity.
- Search is progressive: compact results first, `mem_get_observation` only when full content is needed.

## Local store change checklist

- [ ] The rule really belongs in `internal/store`.
- [ ] Migration/schema is covered by existing or new tests.
- [ ] FTS/dedupe/topic/scope/soft delete remain coherent.
- [ ] If it touches sync, mutations are queued or applied correctly.
- [ ] `internal/store/*_test.go` covers the expected flow and edge cases.
- [ ] `DOCS.md#database-schema` is updated if schema or public semantics change.

---

[← Previous: Repository Map](repository-map.md) | [Next: Interfaces →](interfaces.md)

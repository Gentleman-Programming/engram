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
   cwd → .engram/config.json → Git shared-metadata binding (initial remote/root label) → child repo → basename

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
| `sync_apply_deferred` | Pull mutations deferred because dependencies are missing or relation endpoint effective projects do not match. | `internal/store/sync_apply_test.go`, `internal/server/server.go` |

For schema details, use [DOCS.md — Database Schema](../../DOCS.md#database-schema).

## Memory invariants

- Agent protocol and tool guides expect structured `mem_save` content: **What / Why / Where / Learned**. The persistence layer does not automatically reject poorly formed prose; discipline lives in agent instructions and review.
- `topic_key` is for evolving topics; distinct decisions are not mixed under the same key.
- `scope=project` is the default; `scope=personal` exists for non-shared memory; `scope=global` exists for machine-wide, cross-project observations.
- Soft delete (`deleted_at`) hides data without physically deleting it unless explicit hard delete is used.
- Public observation updates/deletes require caller-supplied `expected_project`. `UpdateObservationForProject` and `DeleteObservationForProject` validate the assertion and compare normalized stored ownership inside the same transaction as the mutation, before revision/sync/tombstone changes. Personal/global scope still has an owner. Shared transactional helpers preserve unguarded CLI/internal maintenance entry points; project metadata remains immutable.
- Native MCP's stored-owner fallback is read-only (`mem_get_observation`); an owner assertion never replaces current-project checks or ambiguous-project recovery.
- Write tools resolve the project from cwd/config; do not invent a project when there is ambiguity.
- Search is progressive: compact results first, `mem_get_observation` only when full content is needed.

## Deferred relation diagnostics

`referenced observation missing` means at least one endpoint identity is absent locally. `referenced observation effective project mismatch` means both identities exist, but the relation's project-scoped endpoint check fails. An observation's explicit project overrides its session project; blank observation projects inherit the session project. Check the endpoint and relation ownership rather than assuming another pull will supply a missing observation.

Both diagnostics retain the same deferred retry lifecycle: the fifth failed replay marks the row dead. Eligible retry-cap dead relations can rearm only when the original scoped endpoint predicate is satisfied; legacy missing-error rows remain compatible. No project rewriting or automatic ownership repair occurs.

## Explicit session identity replacement diagnostics

Without `--replacement-id`, identity repair retains guidance to supply a canonical replacement. Once an explicit replacement attempt selects one source, a blocked plan reports the actual safety or ID-validation blocker instead of repeating that guidance for the selected source. Unselected sources retain their guidance; selection failures and all store repair guards remain unchanged.

## Local store change checklist

- [ ] The rule really belongs in `internal/store`.
- [ ] Migration/schema is covered by existing or new tests.
- [ ] FTS/dedupe/topic/scope/soft delete remain coherent.
- [ ] If it touches sync, mutations are queued or applied correctly.
- [ ] `internal/store/*_test.go` covers the expected flow and edge cases.
- [ ] `DOCS.md#database-schema` is updated if schema or public semantics change.

---

[← Previous: Repository Map](repository-map.md) | [Next: Interfaces →](interfaces.md)

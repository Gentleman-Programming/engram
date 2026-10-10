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
- Native MCP's stored-owner fallback is read-only (`mem_get_observation`). ID-addressed `mem_update`/`mem_delete` use the caller's mandatory owner assertion rather than the server's current project; this is an ownership consistency check, not authentication or authorization.
- New-record write tools resolve the project from cwd/config or a bound session; do not invent a project when there is ambiguity. Update/delete assertions do not change save/session recovery rules.
- Search is progressive: compact results first, `mem_get_observation` only when full content is needed.

## Save conflict candidate relevance

`mem_save` keeps the original OR-based FTS query and raw BM25 scores, then applies
an opt-in title/topic relevance gate in `internal/store` inside the ranked SQL
query, before materialization, the positive candidate limit, and pending relation
writes. A deterministic SQLite function uses the same Go relevance rule; rejected
matches never become result rows read by Go. `CandidateTimings.RowsRead` reports
consumed result rows, not the amount of work performed by the FTS index. BM25 still orders eligible candidates; its default maximum
rank of `0.0` is not a semantic relevance threshold.

The gate accepts a retrieved candidate when it shares a nonempty `topic_key`,
shares at least two distinct significant title terms, or both titles reduce to
the same single significant term. Terms are case-insensitive letter/digit words;
punctuation separates words except that `+` and `#` are preserved in technical
identifiers (`C++` and `C#` remain distinct). Bare punctuation does not count as a
term. Duplicates do not count twice, and common English
connectives and generic change verbs (such as `updated` and `fixed`) are ignored.
Matches only in content do not establish title relevance. Missing or different
topic keys do not veto otherwise relevant titles. Topic-key saves continue to
revise the existing observation rather than creating a new same-topic record.

This is a lexical heuristic, not semantic conflict detection: synonyms or titles
sharing just one term can still miss a real conflict, and the ignored-word list
is English-specific. A matching topic key is a signal among retrieved candidates,
not a separate topic-only search. Title normalization applies only to the gate;
retrieval still follows the original FTS trigram matching rules, including its
short-query limitations. `ScanProject` and ordinary `FindCandidates`
callers retain their existing broad recall unless they explicitly opt into the
save gate. No new user setting or schema migration is required.

## Replay-safe store saves

`AddObservationParams.OperationID` optionally binds a save to a durable local result in `observation_save_operations`. The ledger uses a `v1:` SHA-256 fingerprint of length-prefixed, normalized request fields (including redacted/truncated content), computed before session ownership resolution. An identical replay returns the committed ID before ownership, dedupe, topic revisions, or sync mutations can run again; a changed payload returns `ErrObservationOperationConflict`.

The ledger uses a plain insert in the same transaction as the observation and its sync mutation. Soft-deleted observations are not replayable or recoverable by lookup, but retain their ledger binding and fingerprint so changed payloads still conflict. Hard deletion preserves the operation as a tombstone through nullable `observation_id` and `ON DELETE SET NULL`; identical tombstone replays and unsupported fingerprint versions return `ErrObservationOperationExpired`. Committed-result lookup returns that same error for a soft-deleted or tombstoned operation, while a never-recorded operation remains a distinct zero-result lookup. Saves without an operation ID retain existing behavior and write no ledger row. The HTTP and Pi recovery contract built on this store behavior is described in [Replay-Safe Observation Saves](replay-safe-observation-saves.md).

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

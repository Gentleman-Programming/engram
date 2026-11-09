# Compound agent workflows over HTTP

These local Go HTTP bridges use the existing store operations. They do not invoke MCP or implement memory algorithms in clients. All three routes use the existing optional Bearer-token mutation policy: when `ENGRAM_HTTP_TOKEN` is configured, supply `Authorization: Bearer <token>`. Session-owned servers also retain their whole-handler authentication.

Send exactly one JSON object. Each workflow request is limited to **1 MiB (1,048,576 bytes)**, including trailing JSON or whitespace. The decoder enforces this limit before workflow writes, after the existing authentication checks. Overflow detected while reading either the object or its trailing data returns HTTP 413 `{ "error": "body exceeds 1048576-byte limit", "code": "request_too_large" }`; malformed, empty, null, array or additional JSON values within the limit return HTTP 400 `invalid_arguments`. This is a transport resource limit, not a change to Store content rules.

Never automatically replay any of these POSTs after an uncertain network result or HTTP 500. The store's finalization retry journal is not a network retry policy.

## Consolidate observations

`POST /observations/consolidate`

```json
{"ids_to_delete":[1,2],"new_title":"Combined finding","new_content":"Caller-authored summary","expected_project":"my-project"}
```

Required: at least two positive exact JSON integer IDs (maximum 9007199254740991), nonempty `new_title`, `new_content`, and `expected_project`. Optional: string `type`, `topic_key`, `fingerprint`, boolean `confirm` (default false). Other fields are refused; project/session/scope overrides are not supported.

Preview returns HTTP 200 `{ "status": "preview", "plan": ... }`, without writes or write notifications. The plan contains `source_ids`, `sources`, `project`, `fingerprint`, and the resolved `survivor` projection. Repeat the exact reviewed arguments with `confirm:true` and its opaque `fingerprint` only after explicit confirmation. HTTP 201 returns `status:"consolidated"`, `plan`, persisted `survivor`, and `soft_deleted_ids`.

Refusals are `{ "error": "message", "code": "code", "plan": ... }` (plan optional). A stale fingerprint returns HTTP 409 `fingerprint_mismatch` with a replacement preview. Wrong expected owner returns HTTP 403 `project_mismatch`; invalid arguments return HTTP 400 `invalid_arguments`. Other known store refusals (duplicate/missing sources, cross-project inputs, provenance cycles or unresolved project ownership) return HTTP 400 `consolidation_refused`. Unexpected database or internal failures return HTTP 500 `{ "error": "consolidation failed", "code": "consolidation_failed" }`, without exposing internal error details. Never auto-confirm a replacement plan. Ownership, source drift, provenance, survivor construction, soft deletion, and local atomicity remain store-owned; local success does not promise remote visibility.

## Judge a batch

`POST /conflicts/judge-many`

```json
{"judgments":[{"judgment_id":"rel-existing","relation":"not_conflict","reason":"Different contexts","confidence":0.9}],"session_id":"existing-session"}
```

`judgments` must contain 1..32 raw items; optional shared `session_id` must be a string. Invalid batch envelope/size/shared-session type returns HTTP 400 `{ "error": "message" }`, without a `code` field, before any writes. Individual nonobjects, invalid field types, missing required fields, invalid verbs, or store failures are per-item errors. This distinction preserves MCP batch behavior: the batch is not atomic, and valid later items still run.

Each object uses `judgment_id`, `relation`, optional `reason`, `evidence`, and numeric `confidence` in [0,1]. Shared session provenance replaces per-item session provenance, including when omitted (empty provenance). Existing store verb validation applies.

HTTP 200 returns `{ "outcomes": [...] }` in input order. Every outcome has `index`, `judgment_id` (null when unavailable), and `status:"success"|"error"`. Success includes `relation` and `judgment_status`; errors include `error`. Known Store refusals (missing relation, invalid verb and cross-project relation) retain their existing messages; unexpected database or internal failures use `error:"judgment failed"`, without exposing internal details. These remain ordered per-item errors under HTTP 200, and later items still run. Successful items retain their individual transactions; no rollback or exactly-once guarantee is added.

## Finalize an existing session

`POST /sessions/{id}/finalize`

```json
{"project":"my-project","summary":"Goal: ...\nAccomplished: ...\nNext Steps: ..."}
```

Both fields must be nonempty strings. `project` asserts the existing session's canonical owner; it is not a reassignment. The path ID is exact, never trimmed or implicitly registered. Other fields, including MCP recovery tokens, are refused.

HTTP 200 returns `session_id`, `state:"finalized"|"already_finalized"`, `summary_id`, `summary_sync_id`, `already_finalized`, and `judgment_required:false`. A valid local retry retains the original summary identity even if the new summary text differs, without writes or notification. The store atomically saves the session-summary observation, ends the session, queues normal sync mutations, and journals the local identity; legacy session free-text summary is preserved.

Unknown session: HTTP 404 `unknown_session`. Foreign owner: HTTP 403 `session_project_mismatch`. A session ended separately: HTTP 409 `session_already_ended`. Empty/invalid content: HTTP 400 `invalid_arguments`. Blank session identity returns HTTP 400 `session_finalize_failed`. Unexpected database or internal failures return HTTP 500 with generic `error:"session finalization failed"`, `code:"session_finalize_failed"`, and `session_id`, without exposing internal error details; they must not be treated as a confirmed client refusal or a reason to replay automatically. Refusals contain string `error` and `code`, and store refusals and internal failures also carry `session_id`. The journal is local-only, not replicated/exported.

## Transport differences

Consolidation arguments and plan projections are shared pure Go helpers; batch argument validation and single-judge parameter projection are also shared with MCP. Store behavior is authoritative. HTTP uses status codes and a flat string `error`; decoding, consolidation and finalization errors also include `code`, while batch-envelope validation errors omit it and per-item errors remain within `outcomes`. MCP retains its existing tool-result/error envelopes and queue. HTTP finalization requires an explicit project owner instead of copying MCP project-resolution/recovery-token semantics. No claim of identical transport envelopes or automatic network replay is made.

# Delta for Cloud Autosync

Scope: #503; excludes #814/#892.

## ADDED Requirements

### REQ-215: Canonical validation before mutation storage

`POST /sync/mutations/push` MUST validate every supported `upsert` against the canonical payload contract before mutation storage. Required fields (non-blank strings) are:

| Entity | Required upsert payload fields |
|---|---|
| `session` | `id`, `directory` |
| `observation` | `sync_id`, `session_id`, `type`, `title`, `content`, `scope` |
| `prompt` | `sync_id`, `session_id`, `content` |
| `relation` | `sync_id`, `source_id`, `target_id`, `relation`, `judgment_status`, `marked_by_actor`, `marked_by_kind` |

The mutation payload MUST be either a native JSON object or a JSON string containing an encoded JSON object. Both forms MUST pass the same canonical validation. Encoded arrays, scalars, malformed strings, and decoded invalid objects MUST be rejected. If `entity_key` is supplied, it MUST match the canonical identity in the payload; an omitted key MAY be derived from that identity.

#### Scenario: Blank observation content is repairably rejected

- GIVEN an authorized, active-project observation upsert whose `content` is `""`, `"   "`, or whitespace-only
- WHEN the push is submitted
- THEN the server responds HTTP 400 with `error_class=repairable`, `error_code=payload_invalid`, and `reason_code=validation_error`, before mutation storage

#### Scenario: Other canonical fields are enforced

- GIVEN an active-project upsert missing or blank in turn `session.directory`, `observation.scope`, `prompt.session_id`, or `relation.marked_by_kind`
- WHEN the push is submitted
- THEN the server responds HTTP 400 identifying the corresponding canonical field

#### Scenario: Complete relation remains compatible

- GIVEN a relation upsert containing all seven required fields
- WHEN the push is submitted
- THEN the server responds HTTP 200 and accepts the relation compatibly

### REQ-216: Delete compatibility and whole-batch admission

Session, observation, and prompt `delete` mutations MUST remain supported and require their delete identity (`session.id`, `observation.sync_id`, or `prompt.sync_id`); upsert-only fields MUST NOT be required for deletes. Relation deletes remain unsupported. Any invalid entry MUST reject the complete batch before mutation storage; the server MUST NOT store a subset, return accepted sequences, or partially persist the batch.

#### Scenario: Valid upserts and deletes are preserved

- GIVEN a batch containing valid session, observation, prompt, and supported delete mutations
- WHEN the push is submitted
- THEN the server responds HTTP 200 with an acknowledgement for every stored sequence

#### Scenario: Mixed valid and invalid entries are atomic

- GIVEN a batch with valid entries and one observation upsert with blank `content`
- WHEN the push is submitted
- THEN the server responds HTTP 400, makes no mutation-storage call, returns no partial acknowledgement, and persists no entry

### REQ-217: Structured repairable errors and policy ordering

Validation failures MUST use the JSON error envelope: HTTP 400, `error_class=repairable`, `error_code=payload_invalid`, and `error`. The response MUST include `invalid`, with each offending entry represented by zero-based `index`, `entity`, and canonical `field`; all invalid entries SHOULD be reported. Authentication and project authorization MUST occur before payload-validation details, and pause policy MUST be checked after authorization but before payload validation. A paused authorized project MUST retain HTTP 409 without payload details.

#### Scenario: Structured details identify the offender

- GIVEN invalid entries at indexes 1 and 3 for entities `observation` and `prompt`
- WHEN the push is submitted to an active project
- THEN the 400 response includes `invalid` details identifying indexes 1 and 3 and their missing fields

#### Scenario: Authorization and pause precede payload validation

- GIVEN a malformed payload for an unauthorized project, or an invalid payload for an authorized paused project
- WHEN the push is submitted
- THEN the first case returns the existing authorization error and the second returns HTTP 409 `sync-paused`; neither exposes payload-validation details

#### Scenario: Valid requests retain existing acknowledgement behavior

- GIVEN an authorized active project and a batch in which every upsert and delete satisfies this contract
- WHEN the push is submitted
- THEN the server responds HTTP 200 and acknowledges all entries exactly as before

## Non-goals

- No quarantine, dead-letter, cursor, or acknowledgement redesign.
- No historical reconstruction, pull-path changes, or autosync recovery-policy changes.
- No authorization or pause-policy behavior change beyond preserving their ordering.
- No #814/PR #892 title-repair work, issue reopening, or duplicate tracking.

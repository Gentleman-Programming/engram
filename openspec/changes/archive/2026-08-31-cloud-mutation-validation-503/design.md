# Design: Cloud Mutation Validation Before Storage

## Technical Approach

Expose one `internal/cloud/chunkcodec` validation seam and call it from `CloudServer.handleMutationPush` after authentication, authorization, and pause checks, but before `InsertMutationBatch`. The seam reuses canonical normalizers; the handler owns indexing and HTTP presentation. `CloudStore` remains defense in depth, and autosync acknowledgements are unchanged. `withAuth` authenticates first. A bounded minimal decode extracts only each entry's project, without nested payload decoding, then authorizes every distinct project and checks pause policy. Full validation runs only afterward, so malformed unauthorized data returns the existing authorization error, not validation details. Scope remains #503; #814/#892 stay excluded.

## Architecture Decisions

| Decision | Choice | Alternatives rejected | Rationale |
|---|---|---|---|
| Validation ownership | Add exported `chunkcodec.ValidateMutationEntry` under the existing normalizer. | Duplicate handler switches; content-only check. | Prevents drift across canonical entities. |
| Admission boundary | Validate all entries, collect input-order issues, then make one store call. | Validate during insertion; accept a prefix. | Guarantees zero storage/acknowledgement for invalid batches. |
| Wire compatibility | Return `{error_class:"repairable", error_code:"payload_invalid", error, invalid:[...]}` and retain additive `reason_code`. Keep chunk code unchanged. | Reuse `upgrade_repairable_payload_invalid`. | REQ-217 fixes the mutation code without unrelated chunk breakage. |
| Policy ordering | Extract project metadata, authorize all, pause-gate all, then validate payloads. | Validate before authorization. | Preserves 401/403/409 and prevents cross-project disclosure. |

## Data Flow

    withAuth
      → bounded body + minimal project extraction (no nested payload decode)
      → authorize every project → pause gate for every authorized project
      → full decode/chunkcodec.ValidateMutationEntry (all entries)
      → structured 400 OR InsertMutationBatch → accepted_seqs 200

The preflight retains raw bytes and extracts only `entries[*].project`; nested fields are not inspected before authorization. Unauthorized projects receive the existing structured 403 without validation details. Pause checks remain after authorization and before validation; paused projects receive 409 `sync-paused` without `invalid`. `ValidateMutationEntry` accepts native objects and encoded JSON-string objects, then enforces supported operations/entities, non-blank upsert fields, delete identities, and `entity_key` consistency through the canonical decoder. Arrays, scalars, malformed strings, and decoded invalid objects remain rejected. The normalizer still derives keys and rewrites ownership.

## File Changes

| File | Action | Description |
|---|---|---|
| `internal/cloud/chunkcodec/chunkcodec.go` | Modify | Centralize typed required-field/identity checks and expose the seam. |
| `internal/cloud/cloudserver/mutations.go` | Modify | Apply shared validation and emit indexed issues before storage. |
| `internal/cloud/constants/constants.go` | Modify | Add mutation wire code `payload_invalid`; preserve chunk code. |
| `internal/cloud/cloudserver/mutations_test.go` | Modify | Cover entities, operations, ordering, atomicity, policy, and responses; revise obsolete fixtures. |
| `internal/cloud/chunkcodec/chunkcodec_test.go` | Modify | Cover canonical rules, object/delete rules, and identity mismatch. |
| `internal/cloud/remote/transport_mutations_test.go`, `internal/cloud/autosync/manager_test.go` | Modify | Cover 400 parsing and no local ack on failed/short pushes; no production changes. |
| `internal/cloud/cloudserver/mutations_e2e_test.go` | Create | Exercise the HTTP route through `httptest.Server` with status/body assertions. |

## Interfaces / Contracts

    type MutationValidationIssue struct {
        Field   string
        Message string
    }

    func ValidateMutationEntry(entity, op, entityKey string,
        payload json.RawMessage) (MutationValidationIssue, bool)

The handler serializes zero-based `{index, entity, field}` details. `error` remains human-readable; `invalid` is machine-repair detail. Relation-delete rejection reports `op`; payload failures report the canonical field.

## Testing Strategy and Traceability

| Requirement | Design element | RED tests |
|---|---|---|
| REQ-215 | Shared seam and pre-store gate | Session, observation, prompt, and relation canonical fields; complete relation, object, and identity mismatch. |
| REQ-216 | Whole-batch, operation-aware admission | Valid upserts and identity-only session/observation/prompt deletes, relation-delete rejection, mixed-batch zero-store/no-ack. |
| REQ-217 | Typed errors and gate ordering | Indexed 400 envelope, unauthorized/paused detail hiding, remote parsing, and no autosync ack. |

Use `t.Run` table-driven cases and a fake store insert counter. API/E2E tests through `httptest.Server` assert 200/`accepted_seqs`; 400 with exact envelope and indexed `invalid`; existing 403 fields without validation details for malformed unauthorized payloads; and 409 `sync-paused` without details for paused projects. Keep cloudstore transaction tests as defense in depth.

## Threat Matrix

| Boundary | Applicability | Expected behavior / RED test |
|---|---|---|
| Documentation-like paths | N/A — no file classification or execution | None. |
| Git repository selection | N/A — no Git command or repository selection | None. |
| Commit state | N/A — no commit automation | None. |
| Push state | N/A — no VCS push automation; HTTP mutation push is not this matrix boundary | None. |
| PR commands | N/A — no PR automation | None. |

## Migration / Rollout

No migration or flag. Roll out server-side; valid clients retain 200/ack semantics. Revert restores prior admission behavior and the existing transaction. Historical poison rows remain pending; quarantine, dead-letter, cursor, acknowledgement, and recovery-policy changes stay separate.

## Open Questions

No product choices are unresolved. Tasks must preserve canonical field ordering, input-order reporting, and the typed-error seam; these are execution constraints, not new scope.

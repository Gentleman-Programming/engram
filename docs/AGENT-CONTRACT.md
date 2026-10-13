# Generate the Go-owned agent contract foundation

The checked-in [agent contract](contracts/agent-contract-v1.json) describes the
current 22-tool MCP `agent` profile and its semantic/projection metadata. Its
status is **`mcp-foundation`**, not a claim that Pi already implements the same
contract. Runtime negotiation and Pi alignment are not enabled by this artifact.

## Regenerate and check

Run from the repository root:

```sh
go generate ./internal/mcp
go run ./tools/agent-contract -check
go test ./internal/mcp ./tools/agent-contract
```

The generator uses Go registration metadata only. It does not open SQLite,
invoke tool handlers, or start write workers. Successful generation/checking
emits no stdout or stderr. `-check` never writes: a missing or stale artifact
returns exit code 1 with a diagnostic. `go run` also reports a failing child exit
status. The generator binary returns 2 for invalid arguments.

```sh
go run ./tools/agent-contract -h
go run ./tools/agent-contract -output docs/contracts/agent-contract-v1.json
```

`-output` changes the destination. Help and argument rejection do not generate
an artifact or open a data directory.

## Authority and digest

| Field | Authority / meaning |
| --- | --- |
| `contract_id` | `engram.agent-contract/v1`, the contract format identity |
| `revision` | Explicit semantic revision, currently `1`; not binary/npm SemVer |
| `profile` | `agent`, drawn from Go `ProfileAgent`, not the MCP `all` default |
| `operations` | Names, descriptions, schemas, and annotations from the existing Go MCP registrations; semantics from `internal/mcp/agent_contract.go` |
| `status` | `mcp-foundation`; no runtime compatibility decision is implied |
| `digest` | Lowercase SHA-256 of the complete unsigned canonical document |

Canonical encoding uses Go `encoding/json`: object keys are sorted, arrays
retain their declared order, JSON numbers are preserved with `json.Number`,
and there is no insignificant whitespace. The digest covers descriptions,
schemas, annotations, semantics, status, and revision. Only `digest` itself is
omitted. No build timestamps, machine paths, release versions, or caller facts
are added. The checked-in representation is indented JSON with one final newline.

Change the revision deliberately when the semantic contract changes, regenerate,
and review the diff. The generation-drift test does not automatically grant
compatibility to a different revision/digest.

## Semantic metadata is not implemented parity

Each operation records effects/risk, project/session policy, success guarantees,
error effects, and any known Pi projection differences. `pi_runtime_fields`
describes host-owned context separately from MCP model-visible parameters.
Observation IDs, explicit session IDs, host runtime IDs, continuation IDs, and
provenance are not interchangeable.

`target_rule` is a proposed alignment rule, not existing behavior. In particular,
the agreed `mem_review` target admits an explicit project for `mark_reviewed`,
uses the resolved current project when omitted, and never marks globally. The
current MCP branch still ignores that argument for this action; Pi accepts it.
No review handler behavior is changed by exporting this foundation.

Known differences also include save conflict-candidate output, topic-key
computation, history/cursors, literal `find/replace`, session ownership, and
project/personal-scope selection. Resolve them and add shared behavioral vectors
before asserting MCP/Pi semantic parity. Schema/digest equality alone cannot
prove effects, error handling, or transport acknowledgement behavior.

An error is not universally proof of rollback: reloads may fail after pin/review
mutations, and lost write acknowledgements can leave outcomes unknown.

## Compatibility and review boundary

The existing 26-tool MCP `all` behavior and the historical
`internal/mcp/testdata/tool-contract-v1.json` compatibility ratchet remain intact.
`TestAgentContractArtifactCurrent` checks this new current artifact separately.
The generator tests exercise its public binary against isolated filesystem state.

The current baseline includes upstream `mem_consolidate`. Metadata registration
preserves its required schema, destructive annotations, preview/confirmation
rules, and existing runtime queue; it does not invoke the handler or open a store.
This operation remains Go/MCP-only: no Pi recipe or dispatch is added.

The agent and admin profiles share exactly `mem_judge_many` and
`mem_session_finalize`. The four admin-only tools (`mem_delete`, `mem_stats`,
`mem_timeline`, and `mem_merge_projects`) remain excluded from this artifact.
The two shared operations are also Go/MCP-only; this foundation adds no Pi
recipes or dispatch and does not change existing Pi summary/end behavior.

`mem_judge_many` processes 1..32 caller-authored judgments in submitted order.
Per-item failures do not stop later items or roll back successful items; retries
can refresh sync mutations and are not exactly-once. `mem_session_finalize`
atomically saves a structured summary and ends an existing exact session while
preserving its legacy free-text summary. Successful local retries retain the
original summary identity without writes, even when valid retry text differs.
Its retry journal is not synced/exported; separately ended or imported ended
sessions without that journal reject. Neither operation promises remote
visibility or treats a lost acknowledgement as proof of rollback.

Review the small registration seam and Go metadata first, then the tests and
generated JSON. No Pi tool inventory or HTTP route is changed. Follow-up work is
tracked in issues #1348 (Pi alignment) and #1350 (runtime negotiation).

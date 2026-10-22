# Generate the Go-owned agent contract foundation

The checked-in [agent contract](contracts/agent-contract-v1.json) describes the
current 23-tool MCP `agent` profile and its semantic/projection metadata. Its
status is **`mcp-foundation`**, not a claim that Pi already implements the same
contract. Runtime negotiation and Pi alignment are not enabled by this artifact.

The [behavioral evidence matrix](AGENT-BEHAVIOR-EVIDENCE.md) separates current
operation/rule tests from execution results and remaining fault/host/release proof.
It does not upgrade this artifact's status.

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

## Verify a Pi model-input capture

The existing command also supports an independent, read-only mode:

```sh
go run ./tools/agent-contract -verify-pi /path/to/capture.json
node --test plugin/pi/test/pi-schema-projections.test.mjs
```

The input file is a nonempty JSON array of actual serialized registrations:
`[{"name":"mem_...","parameters":{...JSON schema...}}]`. The Node regression
captures registrations with the existing disposable sandbox and real pinned
TypeBox 1.3.34 (Node.js 24 and existing plugin test dependencies required).
It builds the existing Go command once in temporary storage; no server is needed.
The repository-only Node test skips when the Go executable or repository contract
artifact is genuinely absent (for example in a packaged copy). Failed toolchain
commands, builds, artifact reads other than absence, and verification errors fail
rather than skip; a complete checkout still runs every real-capture case.

`-verify-pi` calls the [Go-owned projection verifier](PI-TOOL-PROJECTIONS.md#go-owned-input-projection-verification)
against live Go authority, not a second inventory or the checked-in artifact.
It never writes an artifact/capture or opens a store. The binary exits:

- **0**, with empty stdout/stderr: scope-only projection match.
- **1**, with empty stdout and the exact Go verifier error plus newline on stderr:
  refused capture, including invalid JSON, duplicates, inventory/schema differences
  or unsupported source projections.
- **2**, with empty stdout: usage error or unavailable input file. Read failures
  emit only `pi input: capture unavailable`, without private paths.

Explicit blank/whitespace paths reject with `verify-pi path must not be blank`.
Combining verification with explicitly supplied `-output` or `-check` rejects with
`verify-pi cannot be combined with output or check`, even for `-check=false` or
an output equal to its default. Diagnostics end with a newline. `-h` describes
both modes; legacy generation and freshness checking remain independent.
`go run` additionally reports a failing child's exit status.

Success is not semantic parity, installed-host compatibility or release approval.
The raw diagnostic, artifact digest/status/freshness and MCP ratchet are unchanged.

## Authority and digest

| Field | Authority / meaning |
| --- | --- |
| `contract_id` | `engram.agent-contract/v1`, the contract format identity |
| `revision` | Explicit semantic revision, currently `2`; not binary/npm SemVer |
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

### Reproduce the existing Go encoding

This foundation uses **Go `encoding/json`, not RFC 8785/JCS**. Reproducing
its digest requires the same bytes, not merely an equivalent decoded JSON value:

1. Omit only the root `digest` field. Sort every object's decoded string keys
   lexicographically by their UTF-8 bytes (not locale collation or JavaScript's
   default UTF-16 ordering). Preserve array order and do not normalize Unicode.
2. Preserve the original valid JSON number tokens with `json.Number`, including
   exponent spelling, negative zero, and integers beyond JavaScript's exact
   range. Do not round-trip them through floating-point numbers: `1e+02`, `100`,
   and `100.0` are equivalent values but different digest inputs.
3. Encode strings with Go's default escaping: quote and backslash become `\"`
   and `\\`; backspace, form feed, newline, carriage return, and tab use `\b`,
   `\f`, `\n`, `\r`, and `\t`. Other U+0000–U+001F controls use lowercase
   `\u00xx`. `<`, `>`, `&`, U+2028, and U+2029 become `\u003c`, `\u003e`,
   `\u0026`, `\u2028`, and `\u2029`. Slash and other valid Unicode remain
   unescaped. Go replaces invalid UTF-8 in strings and unpaired surrogate
   escapes during decoding with U+FFFD; consumers must match Go's handling.
4. Hash the compact unsigned UTF-8 bytes with SHA-256: no BOM, indentation,
   extra spaces, or trailing newline. Encode the result as lowercase hex.
   The pretty artifact's final newline is not part of this hash input.

Plain `JSON.stringify`, even with sorted keys, does not implement these rules.
Disabling HTML escaping alone would neither implement JCS nor preserve this
contract's current digest. This clarification changes no encoder, revision,
artifact, or runtime compatibility policy.

### Cross-language test vectors

[The versioned vectors](../internal/mcp/testdata/agent-contract-canonical-v1.json)
pin input JSON, exact unsigned `canonical_utf8` text, and its SHA-256. The Go
unit test checks both vectors against the existing encoder. The string vector
covers HTML/line-separator escaping, all C0 controls, non-ASCII keys whose UTF-8
and UTF-16 order differs, and deliberately unsorted arrays. The number vector
pins `1e+02`, `-0`, and `9007199254740993` without converting them to floats.
These synthetic documents test encoding; they are not live MCP inventories.

From the repository root, verify with existing Go and Node installations:

```sh
go test ./internal/mcp -run TestAgentContractCanonicalEncodingVectors -count=1
node --test tools/agent-contract/canonical-vector.test.mjs
```

The Node **test-only** reference reconstructs the string vector and the current
checked-in artifact's digest without executing Go. Its reconstruction domain is
valid Unicode and canonical decimal safe-integer tokens, as used by the current
artifact. It rejects unsafe/non-integer numeric values and negative zero; it is
not a general lossless JSON parser. For the number vector it verifies SHA-256
from the supplied exact canonical UTF-8 text, not by reserializing `JSON.parse`
output. A general non-Go verifier would also need lossless number-token handling.
No Node/Pi runtime verifier, parity assertion, or negotiation is introduced.

Change the revision deliberately when the semantic contract changes, regenerate,
and review the diff. The generation-drift test does not automatically grant
compatibility to a different revision/digest.

### Revision 2: explicit semantic search

Issue #233 adds optional `mem_search.search_mode: "lexical"|"semantic"`.
Lexical search remains the default, with `match_mode: "all"|"any"`; semantic
mode rejects nonempty `match_mode`, requires explicit local configuration and a
fresh manually built index, and never falls back to lexical results. Similarity
describes potentially related memories, not answer confidence.

Revision 2 records this schema and semantic change deliberately. The historical
v1 tool fixture and comparator remain unchanged: the compatibility test asserts
only this approved optional string enum before removing that one property from
the observed schema. Missing, malformed, required, or unrelated schema changes
still fail. Regeneration does not assert Pi support or runtime parity.

## Semantic metadata is not implemented parity

Each operation records effects/risk, project/session policy, success guarantees,
error effects, and any known Pi projection differences. `pi_runtime_fields`
describes host-owned context separately from MCP model-visible parameters.
Observation IDs, explicit session IDs, host runtime IDs, continuation IDs, and
provenance are not interchangeable.

`target_rule` records the review alignment rule now enforced by Go:
`mem_review` accepts an explicit project for `mark_reviewed`, uses the resolved
current project when omitted, and never marks globally. Explicit project outranks
process/default context; unresolved write context is refused. List behavior
remains global when project is omitted; the `id` alias remains supported.

Known differences include save conflict-candidate output, native HTTP result/error
envelopes, host-owned session attribution, and project/personal-scope selection.
Pi already forwards topic-key suggestions, history/cursors, `older_history`, and
literal `find/replace` to Go; their presence is not proof of identical transports.
Resolve remaining differences and add shared behavioral vectors before asserting
MCP/Pi semantic parity. Schema/digest equality alone cannot
prove effects, error handling, or transport acknowledgement behavior.

An error is not universally proof of rollback: reloads may fail after pin/review
mutations, and lost write acknowledgements can leave outcomes unknown.

## Compatibility and review boundary

The existing 27-tool MCP `all` behavior and the historical
`internal/mcp/testdata/tool-contract-v1.json` compatibility ratchet remain intact.
`TestAgentContractArtifactCurrent` checks this new current artifact separately.
The generator tests exercise its public binary against isolated filesystem state.

The current baseline includes upstream `mem_consolidate`. Metadata registration
preserves its required schema, destructive annotations, preview/confirmation
rules, and existing runtime queue; it does not invoke the handler or open a store.
Pi already dispatches this operation over HTTP, preserving error codes and
replacement plans in native error metadata. Go owns the opaque fingerprint;
Pi never auto-confirms a replacement preview or replays an uncertain POST.

The agent and admin profiles share exactly `mem_judge_many` and
`mem_session_finalize`. The four admin-only tools (`mem_delete`, `mem_stats`,
`mem_timeline`, and `mem_merge_projects`) remain excluded from this artifact.
Pi already dispatches both shared operations over HTTP. Batch judgment sends one
request, preserves ordered partial outcomes, and leaves omitted shared provenance
empty rather than injecting the host ID; it adds no client loop or uncertain replay.
Finalization requires confirmed current host/effective-session ownership, injects
the exact identity and owner, and accepts no model session ID. Unknown delivery
pins that identity and blocks lifecycle reopening; retries must be explicit with
the same host identity, never automatic replay or substitution of legacy end.

`mem_judge_many` processes 1..32 caller-authored judgments in submitted order.
Per-item failures do not stop later items or roll back successful items; retries
can refresh sync mutations and are not exactly-once. `mem_session_finalize`
atomically saves a structured summary and ends an existing exact session while
preserving its legacy free-text summary. Successful local retries retain the
original summary identity without writes, even when valid retry text differs.
Its retry journal is not synced/exported; separately ended or imported ended
sessions without that journal reject. Neither operation promises remote
visibility or treats a lost acknowledgement as proof of rollback.

These corrections describe already-landed adapters, not changed Go guarantees or
new compatibility permissions; revision stays `1` and status stays `mcp-foundation`.
Only the six corrected `pi_differences` arrays and the derived digest change.
See [the projection audit](PI-TOOL-PROJECTIONS.md) for source/test evidence and the
intentional canonical-`cwd` decision (MCP retains its `directory` alias).

Review the small registration seam and Go metadata first, then the tests and
generated JSON. No Pi tool inventory or HTTP route is changed. Follow-up work is
tracked in issues #1348 (Pi alignment) and #1350 (runtime negotiation).

# Pi HTTP runtime contract — Go endpoint

This unit adds `GET /runtime-contract`, a read-only Go-owned **server handler
projection** for the default Pi HTTP adapter. It does not implement a Pi
consumer or a generation command. Those remain separate #1350 units.

## Authority and response

`internal/server/runtime_contract.go` uses one typed registry for the 31
selected existing HTTP handler bindings and their configured-token wrappers.
No Store algorithm, existing route path, request limit or error policy changes.

The response contains `contract_id`, `revision`, `status`, `digest`,
`operations`, `supporting_routes`, and dynamic `instance_id`. The initial
identifier is `engram.pi-http-runtime/v1`, revision `1`, status
`pi-http-server-contract`.

The 22 operation names describe the current default Pi surface.
`mem_suggest_topic_key` binds to Go's `POST /topic-keys/suggest`; consolidation,
batch judgments and session finalization declare their actual HTTP routes.
The three compound mutation bindings retain configured-token authentication.
Administrative handlers remain registered with their existing policy, but
`mem_delete`, `mem_stats` and `mem_timeline` are not default Pi operations.
Supporting bindings describe runtime-session resolution/end, prompt capture
decisions and compaction context. Composite operations list their real bindings.

The digest is SHA-256 over Go `encoding/json.Marshal` of the stable typed
projection with `digest` omitted. Struct fields use declared Go order, maps use
Go JSON key order, and arrays retain their order. Dynamic `instance_id` and
release version are not hashed. This is not general JSON canonicalization.
Changes to handler contracts require deliberate declaration/version changes and
public regressions; matching metadata alone does not certify behavior.

## Authentication and identity

Shared/local read-only metadata follows the existing health policy.
Session-owned mode requires the existing bearer token for the entire handler,
including this endpoint; no new token or model-supplied authority is introduced.
The existing configured-token wrappers for destructive and compound mutations
are preserved. `/health` is unchanged.

An instance ID identifies the configured server/store, not remote attestation
or proof of local ownership. Metadata requests do not create observations,
register sessions or emit write notifications. Unsupported endpoint methods
retain the normal method refusal.

## Boundaries and follow-up

The [MCP foundation](AGENT-CONTRACT.md) remains separate. Names, route descriptors
and digests are not TypeBox/MCP schema equivalence, semantic parity or release
approval. Successful pending-batch writes remain outside this unit's proof.

The existing generator and Pi negotiation child PRs must be adapted and verified
against this projection separately. No generated Pi JSON, package contents,
adapter readiness, lifecycle behavior or compatibility policy is changed here.
There is no generation command in this endpoint-only slice. A future consumer
must use Go-generated expectations, reject unsupported contracts explicitly and
preserve existing transport and instance-identity protections.

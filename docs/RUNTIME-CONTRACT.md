# Pi HTTP runtime contract — Go endpoint and generation

`GET /runtime-contract` exposes a read-only Go-owned **server handler
projection** for the default Pi HTTP adapter. The generator produces the stable
projection for a future consumer. Pi negotiation remains a separate #1350 unit.

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

## Generate and check the expected projection

From the repository root, using a POSIX shell (including Git Bash on Windows):

```sh
go run ./tools/runtime-contract > plugin/pi/runtime-contract.json
go run ./tools/runtime-contract -check
go run ./tools/runtime-contract -help
```

Default mode prints indented UTF-8 JSON with a final LF to stdout; only the
shell redirection writes the artifact. The generator calls
`server.RuntimeContractProjection()` without opening a store or starting a
server. It does not duplicate operation lists or independently compute a digest.
The artifact excludes the HTTP-only `instance_id` and release version.

`-check` compares the artifact's exact bytes without writing it. It exits 0 and
prints `Pi HTTP runtime projection is current` on a match. A missing or stale
artifact exits 1 with a diagnostic on stderr. Invalid options or positional
arguments exit 2; `-help` exits 0 and prints usage on stderr. The artifact alone
is pinned to LF in `.gitattributes` so checkout conversion cannot change it.
Do not edit generated JSON by hand or treat this output as Pi negotiation.

## Boundaries and follow-up

The [MCP foundation](AGENT-CONTRACT.md) remains separate. Names, route descriptors
and digests are not TypeBox/MCP schema equivalence, semantic parity or release
approval. Successful pending-batch writes remain outside this unit's proof.

`plugin/pi/runtime-contract.json` is a generated artifact, not an active Pi
consumer. This slice changes no package manifest, adapter readiness, lifecycle
behavior or compatibility policy. The existing Pi negotiation child PR still
needs separate adaptation and verification against this projection. A future
consumer must reject unsupported contracts explicitly and preserve existing
transport and instance-identity protections.

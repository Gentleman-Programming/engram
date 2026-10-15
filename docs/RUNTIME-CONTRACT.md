# Pi HTTP runtime contract — Go endpoint and generation

`GET /runtime-contract` exposes a read-only Go-owned **server handler
projection** for the default Pi HTTP adapter. The generator produces the stable
projection consumed by Pi's exact-only runtime comparison.

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

Pi packages the generated `plugin/pi/runtime-contract.json` beside its comparison
helper. The consumer requalifies before readiness, each memory dispatch and retry,
and observation result lookup/replay. It rejects missing, malformed, older,
unknown or incompatible metadata without a SemVer fallback or JS digest authority.
A private binding retains the configured URL, effective shared/private credential
and dynamic instance. Metadata cannot replace local ownership checks; explicit
URLs still skip local binary ownership and cannot authorize local recovery.

Concurrent callers share a bounded 64 KiB, three-second whole-probe flight, not a
caller's cancellation signal. Qualification is refreshed rather than indefinitely
cached. Only definite connection refusal may enter the existing generation-bounded
implicit recovery; HTTP, parsing and compatibility failures cannot restart a
server. Uncertain observation saves retain frozen destinations and cannot replay
onto a new binding. These source changes do not prove installed SDK execution,
semantic parity, hosted CI acceptance or publication readiness.

## Focused integration evidence

`plugin/pi/test/real-agent-workflows.test.mjs` includes an actual-handler metadata
case alongside its workflow cases. `ENGRAM_PI_TEST_SERVER_BINARY` selects the
prebuilt production-handler helper, avoiding the checkout suite's build path.
The package-only availability tests require eight explicit skips without a Go
checkout or override; an invalid supplied binary fails rather than skips.

The separate `public PATH runtime negotiation` cases require the test-only
`ENGRAM_PI_TEST_PATH_BINARY` prebuilt override and never compile implicitly. The
stdlib fixture consumes the complete generated projection supplied by the test.
This is synthetic PATH/startup/recovery evidence, not real-handler or SDK proof.
Owned-mode preload and authenticated instance-mismatch checks remain separate.
Source preparation and successful builds do not establish that these tests passed.

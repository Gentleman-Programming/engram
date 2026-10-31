# Explicit semantic retrieval (store contract)

Lexical search remains the default. No provider configuration, network calls or
indexing occurs on lexical search or observation writes. Semantic similarity is
relatedness, **not truth or confidence that a memory answers a question**.
There is no score threshold, hybrid ranking, FTS rescue or topic-key shortcut.

## Adapter wire points

Inject `Config.EmbeddingProvider SemanticProvider` before `New`. The optional
interface is `Resolve(context.Context) (embedding.Identity, error)` and
`Embed(context.Context, embedding.Identity, []string) ([][]float64, error)`.
A nil provider leaves lexical operation unchanged. The store reads no environment
variables and creates no provider. Provider/config selection belongs to adapters.

Manual indexing is:

```go
result, err := s.BuildSemanticIndex(ctx, store.SearchOptions{Project: "engram"})
// result.Indexed counts every visible matching observation; Limit is ignored.
```

`BuildSemanticIndex(context.Context, SearchOptions) (SemanticIndexResult, error)`
rebuilds the complete filtered corpus. It currently regenerates all matching
vectors rather than skipping valid cached rows. Inputs are raw title + newline +
content. Requests contain at most 32 observations and normally at most 128 KiB of
text; an individual source remains subject to existing store content limits.
There is no fixed dimensionality or model download.

Pass `SearchOptions.SearchMode` (`json:"search_mode,omitempty"`) to existing
`SearchContext`, `Search`, and `SearchPreviewsContext`. Empty or `lexical` retains
existing behavior, including empty/all/any match modes and their existing errors.
`semantic` rejects **any nonempty MatchMode**: adapters must not inject `all` as a
default in semantic requests. Unknown search modes fail before database work.
Indexing also rejects nonempty MatchMode; SearchMode may be empty, lexical or
semantic for the explicit indexing method.

Semantic results and previews add `Similarity *float64` (`similarity,omitempty`)
and `RetrievalMode string` (`retrieval_mode,omitempty`, value `semantic`). These
are absent from lexical JSON. Zero and negative similarities are valid. Existing
Rank remains present but is not the semantic score (it is zero). Compact projection
adds only similarity/retrieval_mode for semantic hits and retains its conditional
project/topic/review/relation fields. Unicode previews match the existing SQLite
300-code-point preview behavior, including stopping at NUL, with no ellipsis.

All live observations matching the existing normalized project, type and scope
predicates are candidates; soft-deleted records are excluded. No other visibility
rules are invented. Every eligible cache must be valid before embedding the raw
query. Rank all candidates by descending cosine similarity, tie by ascending local
ID, then apply the existing default (10) and configured maximum result limits.
An empty eligible corpus returns an empty slice without embedding the query;
an unconfigured provider still errors. Filtered operations disclose no cache
metadata about other projects.

## Local cache format and freshness

No schema or sync payload changes. The reserved `embedding` BLOB contains JSON:

- `version`: 1
- `identity`: the provider's `embedding.Identity` (Model and full Digest)
- `dimensions`: vector length
- `source`: lowercase hex SHA-256 source fingerprint
- `vector`: raw finite, nonzero float64 components

`embedding_model` repeats Identity.Model and `embedding_created_at` records the
local cache write time. These three fields are derived local state only. Writes
use the existing guarded SQLite transaction path; they do not change source
columns, timestamps, revisions, duplicate counters, topic identity or sync outbox.
Existing FTS update triggers run but the indexed source values do not change.

The source fingerprint is SHA-256 over Go encoding/json's deterministic sorted-key
object of **all observations columns except those three cache fields**. SQL NULL
is JSON null; text/byte values are encoded as JSON base64 byte slices (preserving
even invalid UTF-8 exactly), and SQLite numeric values retain their numeric
representation. This includes ID, sync_id, content, source metadata,
provenance, timestamps and counters. Thus delete/recreate cannot reuse an old cache
merely because title/content match. Schema additions to source metadata naturally
invalidate existing envelopes. This is an internal derived format, not a sync or
user-data format; unknown versions require an explicit rebuild.

Malformed JSON, absent coverage, unknown version, source/model/digest/dimension
mismatch or invalid cached vectors fail closed. Null vectors and null components
are rejected; numeric zero and negative components remain valid when the vector
has nonzero norm. Vectors are scaled by their
maximum absolute component before Hypot normalization to avoid overflow, then
ranked by the normalized dot product (clamped to [-1,1] for rounding only).

## Concurrency and errors

Source and cache snapshots are materialized and their rows closed before provider
work. Resolve surrounds embedding work; the observed model identity must remain
unchanged. After provider work the entire eligible source/cache set is compared
again. Build revalidation and persistence occur atomically inside `withTx`, using
only transaction-scoped database operations. No provider call holds a database
connection or transaction, even with a one-connection pool. Comparing cache bytes
and timestamps as well as source fingerprints prevents overlapping builds from
silently overwriting another derived result. Query revalidation precedes return;
changes/deletions during provider work do not return an outdated snapshot.

As with ordinary reads, a source can change after final read/commit. The daemon is
trusted to report its identity honestly: checks detect observed replacement, not
unobservable change-and-change-back or immutable per-response digest attestation.

Use `errors.Is` with:

- `ErrSemanticUnconfigured`: configure an injected local provider.
- `ErrSemanticIndex`: missing/stale/invalid cache; run BuildSemanticIndex.
- `ErrSemanticChanged`: source/cache/model drift; retry explicit build or search.
- `ErrSemanticVector`: invalid injected vectors; fix provider and rebuild.

Provider errors retain their original errors.Is classifications and include local
provider guidance; cancellation retains context.Canceled/DeadlineExceeded,
including when a provider also reports another failure. Invalid mode/match-mode
inputs return descriptive validation errors. No failure falls back to lexical or
persists a partial new index. External intentional source edits remain intact.

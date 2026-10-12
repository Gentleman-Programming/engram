# Observation consolidation (store API)

`Store.ConsolidateObservations` merges at least two live observations from one
resolved canonical project. It is a store API, not yet an MCP tool in this slice
of issue #242.

## Preview and confirmation

1. Call with `Confirm: false` (the default), source `IDs` and optional `Overrides`.
   The result contains sources, a resolved survivor and a `Fingerprint`; nothing
   is written.
2. Call with `Confirm: true`, the same selection and resolved overrides, and that
   fingerprint. Source ID input order does not matter.
3. If source state or the resolved survivor changed, execution returns
   `ErrConsolidateFingerprintMismatch` and a fresh plan without writing. Inspect
   that replacement preview and explicitly confirm its fingerprint to proceed.

Core callers may optionally set `ExpectedProject` to an explicit owner assertion.
A nil assertion preserves the existing API. A supplied assertion is validated and
compared with the resolved canonical source project inside both the preview and
confirmation transactions, before any writes or fresh-plan disclosure. This is
an assertion, not a target-project override.

The versioned fingerprint covers complete source snapshots, including resolved
project ownership and pinned state, and the resolved survivor. Revision counters
alone are insufficient: project migration and diagnostic title repair can change
source fields without incrementing them. Tokens from older fingerprint versions
require a new preview.

A source whose project cannot be resolved from its observation or session is
rejected with `ErrProjectRequired`. Missing/deleted sources, duplicate IDs,
cross-project selections and provenance cycles are also refused before writes.
The survivor inherits the newest source's session, type and scope unless the
supported fields are overridden; `topic_key` is explicit-only. Content includes
source sync IDs in a `consolidated_from` footer.

## Atomicity and sync limits

One local transaction inserts the survivor, sets each source's
`superseded_by_sync_id` to the survivor's sync ID, soft-deletes the sources and
applies the associated sync journal/lifecycle changes. Any failure rolls back
all of those writes, including failures after survivor journaling or a source
deletion.

For enrolled projects, the survivor upsert precedes source deletes in the local
journal; deletes are emitted in source ID order. This is **not distributed
atomicity or a guarantee of remote visibility**: a receiver may defer the
survivor upsert when its parent session is missing while later mutations proceed.
Existing last-write-wins sync can also resurrect a source after a later peer edit.
Unenrolled projects create no cloud journal entries and supersede pending source
mutations using the existing deletion lifecycle.

## Structured provenance

Sources expose nullable `superseded_by_sync_id` through observation reads,
full-row and deletion sync payloads, cloud transport, and backup export/import,
including soft-deleted rows. It is an optional JSON field and has no foreign key:
replication can deliver a reference before its survivor. Migration leaves old
rows null and does not infer links from the `consolidated_from` footer.

A delete received before its source retains the link in the existing delete
tombstone lifecycle without creating a placeholder observation. Missing, null
or empty incoming links preserve known provenance; clearing it is not supported
in this slice. Conflicting nonempty links follow existing last-applied ordering,
not a new timestamp conflict resolver.

A later admitted upsert can restore a source while retaining its known link;
provenance does not silently re-delete it or bypass existing tombstone admission
rules. Older clients can strip unknown fields when relaying payloads, so
mixed-version preservation is not unconditional. Structured provenance changes
also invalidate a previously issued consolidation preview fingerprint.

This slice does not add an MCP tool, dashboard/TUI display, relation-model
changes, automatic resurrection cleanup or a new sync engine.

Regression checks:

```sh
go test ./internal/store -run 'Test(ConsolidateObservations|ConsolidationStructuredProvenance|Provenance)' -count=1
go test ./internal/cloud/chunkcodec ./internal/cloud/cloudstore -run 'Test(Canonicalize.*ObservationProvenance|MaterializedObservationProvenance)' -count=1
go test ./internal/sync -run '^TestSyncStructuredProvenance' -count=1
```

[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Interfaces](interfaces.md) | [Next: Dashboard →](dashboard.md)

# Sync and Cloud

**Sync moves local-first memory without changing ownership: local SQLite stays authoritative.** Engram has git-friendly chunk sync and opt-in cloud sync for explicit projects.

## Local sync and cloud sync

Engram has two related but distinct ideas:

1. **Git sync through chunks**: export/import to `.engram/manifest.json` and `.engram/chunks/*.jsonl.gz`.
2. **Opt-in cloud sync**: push/pull against `engram cloud serve` for an explicit project.

```text
Local SQLite
   │
   ├── engram sync
   │     └── .engram/manifest.json + gzip JSONL chunks
   │
   └── engram sync --cloud --project <name>
         └── internal/cloud/remote
                └── HTTP /sync/*
                       └── internal/cloud/cloudserver
                              └── internal/cloud/cloudstore Postgres
```

## Git-friendly chunks: `internal/sync`

`internal/sync/sync.go` avoids one large shared JSON file. Each sync creates new chunks and a small manifest. That reduces merge conflicts and lets multiple machines generate memory in parallel.

Project-scoped chunks carry sessions, observations, prompts, and the non-orphaned `memory_relations` graph for observations in that project. Relation rows travel as existing `relation` sync mutations inside the chunk so imports reuse the same idempotent relation apply path as cloud sync.

During incremental local project exports, an observation absent from available chunk history bypasses the manifest watermark. A direct historical row or an observation mutation, including a tombstone, establishes historical presence; sessions and prompts otherwise retain timestamp-only filtering, while sessions needed by emitted observations are included for dependency closure.

Cloud exports are size-bounded: `engram sync --cloud` splits the pending mutation replay into deterministic, dependency-complete chunks of at most 4 MiB each, so a large initial replay stays within the server's `ENGRAM_CLOUD_MAX_PUSH_BYTES` limit (default 8 MiB). Each chunk acknowledges only its own mutation sequences after a successful push, so an interrupted sync resumes from the first unacknowledged mutation instead of replaying acknowledged work.

Guardrails:

- Do not modify old chunks to “update” them.
- Do not assume every project is exported unless the command says so.
- Keep imported-chunk tracking to avoid duplicates.

## Cloud autosync: `internal/cloud/autosync`

`internal/cloud/autosync/manager.go` runs in long-lived processes and coordinates:

- SQLite lease to avoid duplicate workers,
- pending mutation push,
- cursor-based pull,
- deferred replay,
- backoff with jitter,
- degraded state with `reason_code` and message.

Business rule: **if sync is blocked, fail loudly and visibly**. No silent drops.

### Blank session identities

A session identity is blank when `strings.TrimSpace` reduces it to the empty
string. That is the only definition. SQL predicates express it through the
shared whitespace trim set in `internal/store/store.go` (`sqlSessionIDBlank` /
`sqlSessionIDNotBlank`), because SQLite's bare `trim()` strips only `U+0020` and
would otherwise let a tab- or newline-only legacy ID be blank to Go and
non-blank to SQL.

Local writes fail closed. `enqueueSyncMutationTx` rejects a blank session
identity for every caller, so no blank-identity mutation can enter the journal
regardless of which Store method enqueues it.

Pulled mutations are **skip-plus-evidence**, not fail-closed. Servers enrolled
before the identity rule existed still hold historical chunks whose session
identity is blank. No local action can make those valid, so halting the pull
would pin the cursor forever and block every later mutation behind it. Instead
the mutation is quarantined in `sync_apply_deferred` with reason code
`sync_session_identity_invalid`, the rest of the chunk applies, and the cursor
advances. A payload that does not decode at all stays fail-closed — that is a
transport fault, not known-corrupt historical data.

Quarantined mutations are never silent, so the "no silent drops" rule holds:
`engram doctor --check invalid_session_identity` reports each one as a warning
finding with reason code `quarantined_pulled_session_identity`, and `engram
conflicts deferred` lists the raw row. The same doctor check reports blank
identities found in the local `sessions` table as blocking findings. Neither is
auto-repaired, because inventing a canonical session ID would fabricate identity
data.

### Session directory admission: local-partial, cloud-strict

Session `directory` follows a two-domain contract (engram#1287).

**Local pulled chunks admit a partial directory.** The local validator accepts
only two states: the `directory` key is absent, or its value is a JSON string
(blank/whitespace included). JSON `null` and every non-string token are rejected
with `ErrPulledSessionDirectoryInvalid` — `null` is not a blank directory, it is
a non-string value and never normalizes into one.

**Cloud pulled chunks are strict.** A directory is required and must be
non-blank. A cloud chunk carrying a session mutation with a blank or missing
directory fails the entire chunk atomically: no session is persisted, the chunk
is not recorded as imported, and the corrected chunk can be redelivered. This
mirrors `Store.ApplyPulledMutation`, the strict single-mutation path used by
cloud autosync.

The domain is carried explicitly, never inferred from the target-key string:
the syncer's import mode selects it, and it flows through
`Store.ApplyPulledChunkForDomain(targetKey, chunkID, mutations, cloud)`.
`Store.ApplyPulledChunk` remains the local-domain wrapper;
`chunkTrackingTargetKey` still controls scoping and dedup, but it is not a
domain signal.

**Completion, not overwrite.** Every path that can see an existing session —
`createSessionTx`, `startSessionTx`, and the pulled-payload upsert — uses the
same SQL CASE: an existing concrete directory is preserved, and a blank one
adopts the incoming concrete value. A later blank payload never erases a
concrete directory, and replayed chunks are idempotent for directory.

**Snapshot import is identity-establishing, not completing.** `Store.Import`
uses `INSERT OR IGNORE`: a later concrete duplicate does not complete an
existing blank session — completion belongs to the create/start/pulled-chunk
paths. At the JSON decode boundary (`ExportData.UnmarshalJSON`) an absent
directory key is treated as blank and accepted, present strings are preserved
verbatim, and `null` or any non-string token fails the whole unmarshal with an
error naming the offending session.

## Cloud transport: `internal/cloud/remote` + `internal/cloud/cloudserver`

`internal/cloud/remote/transport.go` is the client. `internal/cloud/cloudserver/cloudserver.go` is the server. The server mounts:

- `GET /health`
- `GET /sync/pull`
- `GET /sync/pull/{chunkID}`
- `POST /sync/push`
- `POST /sync/mutations/push`
- `GET /sync/mutations/pull`
- `/dashboard/*`

`POST /sync/push` and `POST /sync/mutations/push` enforce the server-side push request body limit from `ENGRAM_CLOUD_MAX_PUSH_BYTES` (default 8 MiB).

For complete route details, use [DOCS.md — HTTP API Endpoints](../../DOCS.md#http-api-endpoints).

## Cloud store: `internal/cloud/cloudstore`

`internal/cloud/cloudstore/cloudstore.go` persists to Postgres, materializes chunks/mutations, and feeds dashboard read models. If an organizational policy matters, state lives here or is enforced from `cloudserver` against data from here.

## Sync/cloud guardrails

- Local SQLite remains the source of truth.
- Cloud sync is project-scoped.
- Push and pull are covered if the sync contract changes.
- Blocks/policies fail loudly with reason code.
- Cloud docs (`docs/engram-cloud/*`, `DOCS.md#cloud-cli-opt-in`, `DOCS.md#cloud-autosync`) stay aligned.

## Sync/cloud change checklist

- [ ] Local SQLite remains the source of truth.
- [ ] Cloud sync is project-scoped.
- [ ] Push and pull are covered if the sync contract changes.
- [ ] Blocks/policies fail loudly with reason code.
- [ ] `internal/cloud/autosync/*_test.go`, `internal/cloud/remote/*_test.go`, `internal/cloud/cloudserver/*_test.go`, or `internal/cloud/cloudstore/*_test.go` cover the affected boundary.
- [ ] Cloud docs (`docs/engram-cloud/*`, `DOCS.md#cloud-cli-opt-in`, `DOCS.md#cloud-autosync`) stay aligned.

---

[← Previous: Interfaces](interfaces.md) | [Next: Dashboard →](dashboard.md)

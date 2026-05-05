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

## Cloud transport: `internal/cloud/remote` + `internal/cloud/cloudserver`

`internal/cloud/remote/transport.go` is the client. `internal/cloud/cloudserver/cloudserver.go` is the server. The server mounts:

- `GET /health`
- `GET /sync/pull`
- `GET /sync/pull/{chunkID}`
- `POST /sync/push`
- `POST /sync/mutations/push`
- `GET /sync/mutations/pull`
- `/dashboard/*`

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

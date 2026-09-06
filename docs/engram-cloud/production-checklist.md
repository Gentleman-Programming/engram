[← Back to Engram Cloud](./README.md)

# Engram Cloud Production Checklist

Use this checklist before operating a self-hosted Engram Cloud endpoint for users. Engram provides the Cloud runtime; the operator owns the surrounding production platform, its security boundaries, and its recovery process. The Compose examples are starting points, not a complete production platform.

## Before public operation

### Transport and ingress

- [ ] Terminate TLS at a reverse proxy or equivalent boundary that you operate, then configure clients with that HTTPS endpoint using `engram cloud config --server https://<cloud-host>`.
- [ ] Keep bearer-token traffic on HTTPS. Authenticated clients require HTTPS, including after redirects; `ENGRAM_CLOUD_INSECURE_NO_AUTH=1` is local/development smoke mode only and must not be used in production.
- [ ] Expose only the intended Cloud endpoint to public ingress. Restrict administrative access to the proxy, host, and deployment controls according to your environment.
- [ ] Keep PostgreSQL and other private dependencies off public ingress. A Compose port mapping or `ENGRAM_CLOUD_HOST` bind address controls where a process listens or is published; it is not a firewall policy.

### Configuration and secrets

- [ ] Configure the required Cloud runtime values: `ENGRAM_DATABASE_URL`, `ENGRAM_CLOUD_ALLOWED_PROJECTS`, `ENGRAM_CLOUD_TOKEN`, and a non-default `ENGRAM_JWT_SECRET` for authenticated mode.
- [ ] Store secret values outside source control and restrict access to the operators and deployment processes that need them. The reference Compose flow keeps its `.env` file on the server and out of Git.
- [ ] Treat `ENGRAM_DATABASE_URL`, `ENGRAM_CLOUD_TOKEN`, `ENGRAM_JWT_SECRET`, and, when used, `ENGRAM_CLOUD_ADMIN` and `ENGRAM_CLOUD_TOKEN_PEPPER` as deployment secrets. The reference Compose example also uses `POSTGRES_PASSWORD`.
- [ ] If managed-token authentication is enabled, keep `ENGRAM_CLOUD_TOKEN_PEPPER` distinct from `ENGRAM_JWT_SECRET`. It is required both when issuing managed tokens and when the running Cloud runtime authenticates them.
- [ ] Define who may change each secret, where its current value is stored, and how rotation triggers a safe redeployment. Operators own rotation and redeployment; this guide does not provide a secret manager or rotation automation.

### Data protection and recovery

- [ ] Use durable storage for Cloud PostgreSQL data. The reference Compose file mounts `engram-cloud-pg` at `/var/lib/postgresql/data`; choose equivalent durable storage for your platform.
- [ ] Own backups for the Cloud database, including retention, access protection, and an off-host or otherwise independent copy where appropriate for your environment. Engram does not provide backup tooling or recovery objectives.
- [ ] Preserve local SQLite data as well. Engram is local-first: local SQLite remains authoritative and Cloud is optional replication and browser visibility.
- [ ] Document a restore procedure that does not overwrite a live service during rehearsal.

## Rehearse and verify

### Restore rehearsal

- [ ] Restore a backup into an isolated environment, using a separate endpoint and database from production.
- [ ] Start the restored Cloud runtime with its own protected configuration; do not reuse production credentials merely to perform the rehearsal.
- [ ] Verify the restored endpoint responds at `GET /health` and that an authorized test client can complete `engram sync --cloud --project <project>`.
- [ ] Confirm observable success: `engram sync --cloud --status --project <project>` reports the expected restored remote chunks and a pending-import state you understand, and the expected replicated data is visible in the isolated dashboard. Record the result and any gaps before relying on the backup process.

### Health and version checks

- [ ] Probe the documented Cloud health endpoint through the production ingress: `GET /health`. A healthy Cloud runtime returns `{"status": "ok", "service": "engram-cloud"}`.
- [ ] Use `engram cloud status` to verify client configuration and auth readiness, then use `engram sync --cloud --status --project <project>` to inspect replication progress.
- [ ] Use `engram version` to record the client binary version. For the Cloud runtime, record the image reference configured by your deployment; the documented Cloud `/health` response does not expose a server version.

## Responsibility boundary and non-goals

| Engram runtime | Operator and platform |
|---|---|
| Cloud sync API, dashboard, authenticated-mode configuration requirements, and the documented health endpoint | TLS termination, reverse proxy, ingress/firewall rules, secret storage and rotation, durable storage, backups, restore rehearsal, and deployment version selection |

This checklist does not configure infrastructure, select a provider, implement high availability, supply backup or monitoring products, define recovery objectives, or change PostgreSQL configuration. Use the [Quickstart](./quickstart.md) for the supported setup and runtime variables, and the [Technical Cloud Reference](../../DOCS.md#cloud-cli-opt-in) for the full command and endpoint contract.

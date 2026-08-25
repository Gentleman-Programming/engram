[← Back to Engram Cloud](./README.md)

# Engram Cloud Quickstart

**Fastest working setup:** run the compose smoke profile, enroll one project, and sync explicitly.

This page gives one recommended path first. Advanced/authenticated mode follows after.

---

## Recommended Path: Local Smoke (Docker Compose)

### 1) Start cloud runtime + Postgres

```bash
docker compose -f docker-compose.cloud.yml up -d
```

`docker-compose.cloud.yml` defaults on this branch:
- `ENGRAM_CLOUD_INSECURE_NO_AUTH=1`
- `ENGRAM_CLOUD_ALLOWED_PROJECTS=smoke-project`
- cloud endpoint published at `http://127.0.0.1:18080`

### 2) Configure CLI cloud endpoint

```bash
engram cloud config --server http://127.0.0.1:18080
```

### 3) Enroll explicit project

```bash
engram cloud enroll smoke-project
```

### 4) Sync explicitly in cloud mode

```bash
engram sync --cloud --project smoke-project
engram sync --cloud --status --project smoke-project
```

### 5) Verify browser dashboard

Open:
- `http://127.0.0.1:18080/dashboard`

In compose smoke mode, `/dashboard/login` redirects to `/dashboard/` (no bearer login needed).

---

## Existing Project Upgrade Path (recommended)

Use this sequence before first bootstrap for established local projects:

```bash
engram cloud upgrade doctor --project smoke-project
engram cloud upgrade repair --project smoke-project --dry-run
engram cloud upgrade repair --project smoke-project --apply
engram cloud upgrade bootstrap --project smoke-project --resume
engram cloud upgrade status --project smoke-project
```

`rollback` is only available before bootstrap reaches `bootstrap_verified`.

---

## Deploy with Official GHCR Image (Dokploy/Coolify/Portainer/VPS)

Do not build from source for production deploys. Use the published image:

- `ghcr.io/gentleman-programming/engram:latest`

Reference compose file:
- [docker-compose.ghcr.yml](./docker-compose.ghcr.yml)

Required runtime env vars:
- `ENGRAM_DATABASE_URL`
- `ENGRAM_CLOUD_TOKEN`
- `ENGRAM_CLOUD_ADMIN`
- `ENGRAM_JWT_SECRET`
- `ENGRAM_CLOUD_ALLOWED_PROJECTS`
- `ENGRAM_CLOUD_HOST=0.0.0.0`
- `ENGRAM_PORT=18080`

Optional runtime env vars:
- `ENGRAM_CLOUD_MAX_PUSH_BYTES` (defaults to `8388608`)
- `ENGRAM_CLOUD_TOKEN_PEPPER` (required to enable managed-token authentication on `engram cloud serve`, and to run `engram cloud bootstrap admin --issue-token`; see [Managed Users and CLI Bootstrap](#managed-users-and-cli-bootstrap) below)

Dokploy guidance:
1. Create a managed Postgres service.
2. Create an app from image `ghcr.io/gentleman-programming/engram:latest`.
3. Configure the env vars above (with strong secrets).
4. Expose container port `18080`.
5. Avoid build-from-source mode unless you are actively developing Engram itself.

### VPS / self-hosted Compose

For a plain VPS, put secrets in a `.env` file next to your compose file instead of
hardcoding them into YAML.

Directory layout:

```text
/opt/engram/
  docker-compose.yml
  .env
```

Example `.env`:

```dotenv
POSTGRES_USER=engram
POSTGRES_PASSWORD=replace-with-strong-postgres-password
POSTGRES_DB=engram_cloud

ENGRAM_DATABASE_URL=postgres://engram:replace-with-strong-postgres-password@postgres:5432/engram_cloud?sslmode=disable
ENGRAM_CLOUD_TOKEN=replace-with-long-random-bearer-token
ENGRAM_CLOUD_ADMIN=replace-with-separate-admin-token
ENGRAM_JWT_SECRET=replace-with-32+-byte-random-secret
ENGRAM_CLOUD_ALLOWED_PROJECTS=engram,gentle-ai
ENGRAM_CLOUD_HOST=0.0.0.0
ENGRAM_CLOUD_MAX_PUSH_BYTES=8388608
ENGRAM_PORT=18080
```

Notes:
- Keep `.env` on the server only. Do not commit it.
- `ENGRAM_CLOUD_TOKEN` is the bearer token clients use for authenticated sync.
- `ENGRAM_CLOUD_ADMIN` is the dashboard admin token. Use a different secret from `ENGRAM_CLOUD_TOKEN`.
- `ENGRAM_JWT_SECRET` must be an explicit, non-default strong secret in authenticated mode.
- `ENGRAM_CLOUD_ALLOWED_PROJECTS` is required server-side and should be a comma-separated allowlist.
- `ENGRAM_CLOUD_MAX_PUSH_BYTES` optionally raises or lowers the server-side limit for chunk and mutation push request bodies. Omit it to keep the default 8 MiB limit.

## Managed Users and CLI Bootstrap

`engram cloud bootstrap admin` creates the first **managed admin** (a `cloud_principals` row, distinct from the legacy `ENGRAM_CLOUD_TOKEN`/`ENGRAM_CLOUD_ADMIN` env-token model):

```bash
engram cloud bootstrap admin --username alice \
  --grant-project my-project \
  --issue-token first-token
```

- `--grant-project` may repeat; managed principals are deny-by-default (no grants means no sync access for that principal).
- `--issue-token [name]` prints the raw managed token exactly once and requires `ENGRAM_CLOUD_TOKEN_PEPPER` to be set to a dedicated secret, separate from `ENGRAM_JWT_SECRET`.
- Running bootstrap again once a managed admin exists is refused (no silent duplicate admin), and every attempt — accepted or refused — is recorded as a `bootstrap.cli` audit event.

**Runtime authentication:** set `ENGRAM_CLOUD_TOKEN_PEPPER` (the same secret used at token-issuance time) on the `engram cloud serve` process to enable managed-token authentication — `engram cloud serve` then resolves managed tokens first, then falls back to the legacy `ENGRAM_CLOUD_TOKEN`/`ENGRAM_CLOUD_ADMIN` credentials, on every `/sync/*`, `/admin/*`, and dashboard-login request. If `ENGRAM_CLOUD_TOKEN_PEPPER` is not set, the server still starts normally and continues to authenticate only via the legacy env-token credentials. Full details: [DOCS.md — Managed users, tokens, and CLI bootstrap](../../DOCS.md#managed-users-tokens-and-cli-bootstrap).

---

Reference compose:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    env_file:
      - .env
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB}
    volumes:
      - engram-cloud-pg:/var/lib/postgresql/data

  cloud:
    image: ghcr.io/gentleman-programming/engram:latest
    restart: unless-stopped
    depends_on:
      postgres:
        condition: service_healthy
    env_file:
      - .env
    ports:
      - "18080:18080"
```

Start or restart after editing `.env`:

```bash
docker compose up -d
docker compose restart cloud
```

If you upgrade the `engram` image tag, redeploy or restart the container so the
running server picks up the new binary.

### Client-side token setup

On the machine that runs the Engram CLI, set the client token in the shell before
cloud sync:

```bash
engram cloud config --server https://your-host:18080
export ENGRAM_CLOUD_TOKEN=replace-with-long-random-bearer-token
engram cloud enroll my-project
engram sync --cloud --project my-project
```

> `ENGRAM_CLOUD_INSECURE_NO_AUTH=1` is for local/dev smoke only. Never use it in production.

---

## Common Failure Reasons

| Reason code | Meaning |
|---|---|
| `blocked_unenrolled` | Project is not enrolled for cloud replication |
| `auth_required` | Authenticated runtime requires valid token/session |
| `cloud_config_error` | Cloud endpoint config is missing/invalid |
| `policy_forbidden` | Project blocked by cloud policy |
| `paused` | Project sync paused in cloud control plane |
| `transport_failed` | Cloud transport/network operation failed |

For concrete recovery steps, see [Engram Cloud Troubleshooting](./troubleshooting.md).

---

<details>
<summary><strong>Advanced: Authenticated Source-Run Mode</strong></summary>

Use this when you are running `engram cloud serve` directly (no insecure compose smoke mode):

```bash
ENGRAM_DATABASE_URL="postgres://engram:engram_dev@127.0.0.1:5433/engram_cloud?sslmode=disable" \
ENGRAM_JWT_SECRET="replace-with-32+-byte-random-secret" \
ENGRAM_CLOUD_TOKEN="your-token" \
ENGRAM_CLOUD_ALLOWED_PROJECTS="my-project" \
engram cloud serve
```

Then configure client endpoint + token:

```bash
engram cloud config --server http://127.0.0.1:8080
export ENGRAM_CLOUD_TOKEN="your-token"
engram cloud enroll my-project
engram sync --cloud --project my-project
```

Rules that matter:
- `ENGRAM_CLOUD_INSECURE_NO_AUTH=1` cannot be combined with `ENGRAM_CLOUD_TOKEN`
- `ENGRAM_CLOUD_ALLOWED_PROJECTS` is required server-side in both modes
- authenticated mode requires explicit non-default `ENGRAM_JWT_SECRET`
- `ENGRAM_CLOUD_INSECURE_NO_AUTH=1` remains local/dev only (never production)

</details>

---

## Next Steps

- Deep runtime/env reference: [DOCS.md — Cloud CLI](../../DOCS.md#cloud-cli-opt-in)
- Background sync mode: [DOCS.md — Cloud Autosync](../../DOCS.md#cloud-autosync)
- Cloud sync failures: [Troubleshooting](./troubleshooting.md)
- Branding assets and usage: [Branding](./branding.md)

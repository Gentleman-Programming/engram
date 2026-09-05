[← Back to README](../../README.md)

# Engram Cloud

**Use Engram Cloud when you want shared, project-scoped memory across machines without giving up local-first ownership.**

Local SQLite remains authoritative. Engram Cloud is optional replication + browser visibility for teams and operators.

<p align="center">
  <img src="../../assets/branding/engram-cloud-logo.png" alt="Engram Cloud" width="960" />
</p>

---

## Recommended Path (first)

If you want a working cloud setup fast, use the local smoke path first:

1. Start cloud + Postgres with compose
2. Configure local CLI server URL
3. Enroll one explicit project
4. Run explicit cloud sync

```bash
docker compose -f docker-compose.cloud.yml up -d
engram cloud config --server http://127.0.0.1:18080
engram cloud enroll smoke-project
engram sync --cloud --project smoke-project
```

Continue with full verification and expected outputs in [Quickstart](./quickstart.md).

---

## Public Container Image (GHCR)

Engram Cloud publishes an official image at:

- `ghcr.io/gentleman-programming/engram`

Supported platforms:
- `linux/amd64`
- `linux/arm64`

Why Linux only? Docker containers run Linux in production platforms (and also inside Docker Desktop VMs on macOS/Windows), so these are the portable targets that matter for Dokploy/Coolify/Portainer/VPS deployments.

For a direct registry-based deploy example, use:
- [docker-compose.ghcr.yml](./docker-compose.ghcr.yml)

---

## What Engram Cloud Is

- **Project-scoped replication**: each sync call is tied to one explicit `--project`
- **Self-hosted cloud runtime**: `engram cloud serve` on infrastructure you control
- **Browser dashboard**: `/dashboard/*` visibility for humans/operators
- **Deterministic status/failure signals**: clear reason codes instead of silent drift

## What Engram Cloud Is Not

- Not cloud-only
- Not implicit “sync everything” mode
- Not a replacement for local SQLite

---

## Runtime Split (important)

### Local runtime: `engram serve`
- Local memory API
- Local sync status endpoint: `GET /sync/status`

### Cloud runtime: `engram cloud serve`
- `GET /health`
- `GET /sync/pull`, `POST /sync/push`
- `GET /dashboard/*` (browser surfaces)

### Chunk transport compatibility and security

Current chunk-sync clients send `POST /sync/push` using the versioned
`application/vnd.engram.sync+gzip; version=1` envelope. The server decompresses
it before its existing validation and storage steps. Chunk pulls use the same
format only when the client explicitly advertises it with `Accept`; otherwise
they remain legacy JSON. The server continues to accept legacy JSON pushes, and
current clients accept both compressed and legacy JSON pull responses.

This envelope is compression/obfuscation intended to reduce false-positive WAF
inspection. It is **not encryption** and does not provide confidentiality; use
HTTPS and appropriate deployment controls for transport security.

---

## Cloud Docs Map

| Doc | Purpose |
|---|---|
| [Quickstart](./quickstart.md) | One recommended path first, then authenticated mode |
| [GHCR Compose Example](./docker-compose.ghcr.yml) | Pull-and-run deployment sample for Dokploy/Coolify/Portainer/VPS |
| [Branding](./branding.md) | Engram Cloud visual identity, asset usage, previews |
| [Technical Cloud Reference](../../DOCS.md#cloud-cli-opt-in) | Full CLI + env/runtime details |
| [Cloud Autosync](../../DOCS.md#cloud-autosync) | Background replication behavior + phase table |

---

## Next Steps

- Need a first successful run? Go to [Quickstart](./quickstart.md)
- Need exact env/runtime contracts? See [DOCS.md Cloud sections](../../DOCS.md#cloud-cli-opt-in)
- Need visual usage rules? Open [Branding](./branding.md)

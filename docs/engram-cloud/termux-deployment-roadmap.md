# Engram Cloud on Termux — Complete Deployment Roadmap

> **Device:** Android 8 GB RAM, 256 GB ROM  
> **Termux:** aarch64 (arm64)  
> **Postgres:** 18 local  
> **Engram:** v1.16 compiled from source  
> **Tunnel:** Cloudflare Tunnel with custom domain  
> **Client:** Windows with PowerShell

---

## Index

- [Prerequisites](#prerequisites)
1. [Architecture Decision](#1-architecture-decision)
2. [Phase 1 — Prepare Termux](#2-phase-1--prepare-termux)
3. [Phase 2 — Compile Engram](#3-phase-2--compile-engram)
4. [Phase 3 — Local Postgres](#4-phase-3--local-postgres)
5. [Phase 4 — Configure Engram Cloud](#5-phase-4--configure-engram-cloud)
6. [Phase 5 — Test Local Sync](#6-phase-5--test-local-sync)
7. [Phase 6 — Cloudflare Tunnel](#7-phase-6--cloudflare-tunnel)
8. [Phase 7 — Persistence (Auto-start Services)](#8-phase-7--persistence-auto-start-services)
9. [Phase 8 — Autosync from Windows Client](#9-phase-8--autosync-from-windows-client)
10. [Issues Encountered and Solutions](#10-issues-encountered-and-solutions)
11. [Quick Status Commands](#11-quick-status-commands)
12. [Final Stack Diagram](#12-final-stack-diagram)
13. [Next Steps](#13-next-steps)

---

## Prerequisites

- Termux installed from **[F-Droid](https://f-droid.org/packages/com.termux/)** (the Play Store version does not receive updates and causes known issues)
- Storage enabled: `termux-setup-storage`
- Device with at least **4 GB RAM** (recommended: 8 GB)
- Android 12+ (tested on Android 14)
- Own domain on Cloudflare (for the tunnel)
- OpenSSL available on a separate machine to generate secrets (Linux, WSL, or macOS)

---

## 1. Architecture Decision

### Analysis: Native binary vs Proot + Alpine

| Aspect | Native (Termux) | Proot + Alpine |
|--------|----------------|----------------|
| Overhead | Zero | ~15-20% on syscalls |
| Postgres shm | Workaround (mmap) | Same problem |
| Binary | Compiles directly | Same compilation |
| Cloudflared | Native in Termux | Same host |
| Maintenance | `pkg upgrade` | `apk update` + 2 distros |

**Decision:** Native compiled binary. Engram uses `CGO_ENABLED=0` (pure Go SQLite via `modernc.org/sqlite`), static binary, no external dependencies. Proot provides no real benefit.

### Postgres: external or local?

| Aspect | External (Neon/Supabase) | Local (Termux) |
|--------|-------------------------|----------------|
| Setup | 5 min | 15 min + tuning |
| RAM | ~30 MB (engram only) | ~150 MB (engram + postgres) |
| Storage | 500 MB free | Unlimited (256 GB ROM) |
| Backup | Automatic | Manual |
| Reliability | 99.9% | Android can kill the process |

**Decision:** Local. The 500 MB from free-tier providers are insufficient for real team usage. With 8 GB RAM and 256 GB ROM locally, there's plenty of headroom.

---

## 2. Phase 1 — Prepare Termux

### Install dependencies

```bash
pkg update && pkg upgrade -y
pkg install \
  golang \
  git \
  cloudflared \
  postgresql \
  termux-services \
  termux-api \
  nano \
  htop
```

### Verify installed versions

```bash
go version
# go version go1.26.3 android/arm64

psql --version
# psql (PostgreSQL) 18.2

cloudflared version
# cloudflared version 2026.6.0 (built 2026.06.09-13:13 UTC)
```

### Wake lock (prevent Android from killing the process)

```bash
termux-wake-lock
```

---

## 3. Phase 2 — Compile Engram

### Create working directory (semantic equivalent to `/opt/engram/`)

```bash
mkdir -p ~/engram-cloud
```

### Clone Engram repository

```bash
cd ~/engram-cloud
git clone --depth=1 https://github.com/Gentleman-Programming/engram .
```

> `--depth=1` avoids downloading the full git history (only the latest commit). Downloads ~23 MB.

### Build the binary

```bash
cd ~/engram-cloud
VERSION=$(git describe --tags --always 2>/dev/null || echo "dev")
go build -ldflags="-X main.version=termux-$VERSION" \
  -o $PREFIX/bin/engram \
  ./cmd/engram
```

### Verify

```bash
engram version
# engram termux-v1.16.1

# Confirm it's a static ELF binary for aarch64
pkg install file
file $PREFIX/bin/engram
# ELF 64-bit LSB executable, ARM aarch64, statically linked
```

---

## 4. Phase 3 — Local Postgres

### Initialize the cluster

```bash
mkdir -p ~/engram-cloud/pgdata
initdb -D ~/engram-cloud/pgdata
# → "Success. You can now start the database server"
```

### ⚠️ Issue: Shared Memory on Android

Android does not support `sysv` shared memory. Without configuration, Postgres fails to start.

**Solution:** Set `dynamic_shared_memory_type = mmap`:

```bash
nano ~/engram-cloud/pgdata/postgresql.conf
```

Modify these lines in `postgresql.conf`:

```ini
dynamic_shared_memory_type = mmap
listen_addresses = 'localhost'
port = 5432
log_line_prefix = '%t [%p] '
```

### Configure pg_hba.conf (local trust access)

```bash
nano ~/engram-cloud/pgdata/pg_hba.conf
```

Ensure:

```
local   all             all                                     trust
host    all             all             127.0.0.1/32            trust
host    all             all             ::1/128                 trust
```

> `trust` is acceptable because Postgres only listens on localhost. Postgres is never exposed to the outside world — Cloudflare tunnels port 18080 (Engram), not 5432 (Postgres).

### Start Postgres

```bash
pg_ctl -D ~/engram-cloud/pgdata -l ~/engram-cloud/pgdata/pg.log start

# Verify
pg_isready
# /data/data/com.termux/files/usr/tmp:5432 - accepting connections
```

### Create user and database

```bash
createuser engram
createdb engram_cloud -O engram

# Verify
psql -U engram -d engram_cloud -c "SELECT current_database(), version();"
```

---

## 5. Phase 4 — Configure Engram Cloud

### Generate secrets

> **Important:** Do NOT generate on Termux. `openssl rand` on Android has limited entropy and may hang. Generate on any Linux machine:

```bash
openssl rand -base64 32
# Repeat 3 times to get ENGRAM_CLOUD_TOKEN, ENGRAM_CLOUD_ADMIN, ENGRAM_JWT_SECRET
```

### Create `.env` file

```bash
cat > ~/engram-cloud/.env << 'ENVEOF'
# --- Postgres ---
ENGRAM_DATABASE_URL=postgres://engram@localhost:5432/engram_cloud?sslmode=disable

# --- Engram Cloud Secrets (generated with openssl rand -base64 32) ---
ENGRAM_CLOUD_TOKEN=your-cloud-token-here
ENGRAM_CLOUD_ADMIN=your-admin-token-here
ENGRAM_JWT_SECRET=your-jwt-secret-32-or-more-chars

# --- Project allowlist ---
ENGRAM_CLOUD_ALLOWED_PROJECTS=*

# --- Network ---
ENGRAM_CLOUD_HOST=0.0.0.0
ENGRAM_PORT=18080
ENVEOF

chmod 600 ~/engram-cloud/.env
```

> ⚠️ **Security:** `ENGRAM_CLOUD_ALLOWED_PROJECTS=*` allows any project to sync with this cloud server. For multi-tenant or team use, replace `*` with an explicit comma-separated list:
>
> ```ini
> ENGRAM_CLOUD_ALLOWED_PROJECTS=my-project,other-project
> ```

### ⚠️ Issue: `source .env` without `export`

By default, `source .env` loads variables as shell locals — they are not inherited by the `engram` process.

**Solution:** Add `export` to every line in `.env`:

```bash
sed -i 's/^ENGRAM_/export ENGRAM_/' ~/engram-cloud/.env
```

Now load and run:

```bash
cd ~/engram-cloud
source .env
engram cloud serve
```

### ⚠️ Issue: Connection refused to Postgres

```
engram: cloudstore: ping postgres: failed to connect to 127.0.0.1:5432: connection refused
```

**Solution:** Postgres was not running. Start it:

```bash
pg_ctl -D ~/engram-cloud/pgdata -l ~/engram-cloud/pgdata/pg.log start
pg_isready
```

### Verify cloud server health

```bash
# In another Termux session
curl http://127.0.0.1:18080/health
# → {"service":"engram-cloud","status":"ok"}

# Dashboard in Android browser
# http://127.0.0.1:18080/dashboard
```

---

## 6. Phase 5 — Test Local Sync

### Configure CLI as client

```bash
engram cloud config --server http://127.0.0.1:18080
export ENGRAM_CLOUD_TOKEN="your-cloud-token-here"
```

> Note: engram may print `Update available: termux-v1.16.1 -> 1.16.1`. This is a false positive caused by the `termux-` version prefix. Ignore it.

### Enroll test project

```bash
engram cloud enroll smoke-test
```

### ⚠️ Issue: `blocked_unenrolled`

```
engram: cloud sync blocked_unenrolled: project "smoke-test" is not enrolled for cloud sync
```

**Solution:** First enroll the project, then sync:

```bash
engram cloud enroll smoke-test
engram save "Test from Termux" "My first local Termux sync" --project smoke-test
engram sync --cloud --project smoke-test
```

### Verify in dashboard

```
http://127.0.0.1:18080/dashboard/projects/smoke-test
```

---

## 7. Phase 6 — Cloudflare Tunnel

### Authenticate Cloudflared

```bash
cloudflared tunnel login
# → Opens a link in the browser to authorize with Cloudflare
```

### ⚠️ Issue: `--force` flag does not exist

```bash
cloudflared tunnel login --force
# flag provided but not defined: -force
```

There is no `--force` flag. Simply run `cloudflared tunnel login` again if it fails.

### Create the tunnel

```bash
cloudflared tunnel create engram-cloud
# Created tunnel engram-cloud with id abcd1234-ab12-cd34-ef56-abcdef123456
```

This creates the credentials file at `~/.cloudflared/abcd1234-ab12-cd34-ef56-abcdef123456.json`.

### ⚠️ Issue: DNS route to apex domain fails

```bash
cloudflared tunnel route dns engram-cloud ejemplo.com
# Failed to add route: code: 1003 - A, AAAA, or CNAME record with that host already exists
```

**Solution:** Use a subdomain:

```bash
cloudflared tunnel route dns engram-cloud engram.ejemplo.com
# Added CNAME engram.ejemplo.com → tunnel-id.cfargotunnel.com
```

### Create tunnel configuration

```bash
cat > ~/.cloudflared/config.yml << EOF
tunnel: abcd1234-ab12-cd34-ef56-abcdef123456
credentials-file: ~/.cloudflared/abcd1234-ab12-cd34-ef56-abcdef123456.json

ingress:
  - hostname: engram.ejemplo.com
    service: http://localhost:18080
  - service: http_status:404
EOF
```

### Test the tunnel

```bash
cloudflared tunnel run engram-cloud
```

Verify from any browser:
```
https://engram.ejemplo.com/health
```

### Alternative: SSH tunnel (for local testing without Cloudflare)

```bash
# From the client machine
ssh -p 8022 -L 18080:127.0.0.1:18080 user@192.168.x.x

# Then open in local browser
# http://127.0.0.1:18080/dashboard
```

Or if connecting from the same network:
```
http://termux-local-ip:18080/dashboard
```

---

## 8. Phase 7 — Persistence (Auto-start Services)

### Postgres service

```bash
mkdir -p $PREFIX/var/service/postgresql
cat > $PREFIX/var/service/postgresql/run << 'RUNEOF'
#!/data/data/com.termux/files/usr/bin/bash
export PGDATA=${HOME}/engram-cloud/pgdata
exec pg_ctl -D ${PGDATA} -l ${PGDATA}/pg.log start 2>&1
RUNEOF
chmod +x $PREFIX/var/service/postgresql/run
```

### Engram Cloud service

```bash
mkdir -p $PREFIX/var/service/engram-cloud
cat > $PREFIX/var/service/engram-cloud/run << 'RUNEOF'
#!/data/data/com.termux/files/usr/bin/bash
source ${HOME}/engram-cloud/.env
for i in $(seq 1 15); do
  pg_isready -q && break
  sleep 1
done
exec engram cloud serve 2>&1
RUNEOF
chmod +x $PREFIX/var/service/engram-cloud/run
```

### Cloudflare Tunnel service

```bash
mkdir -p $PREFIX/var/service/cloudflared
cat > $PREFIX/var/service/cloudflared/run << 'RUNEOF'
#!/data/data/com.termux/files/usr/bin/bash
exec cloudflared tunnel run engram-cloud 2>&1
RUNEOF
chmod +x $PREFIX/var/service/cloudflared/run
```

### Enable and start services

```bash
# Stop manual tunnel if running
kill $(pgrep -f "cloudflared tunnel run") 2>/dev/null

# Start services
sv start postgresql
sv start engram-cloud
sv start cloudflared

# Should show:
# ok: run: postgresql: (pid XXXX) 0s
# ok: run: engram-cloud: (pid XXXX) 19s
# ok: run: cloudflared: (pid XXXX) 3s
```

### Verify everything

```bash
pg_isready
curl -sf http://127.0.0.1:18080/health
curl -sf https://engram.ejemplo.com/health
```

All 3 must respond OK.

### Auto wake lock on Termux open

```bash
cat >> ~/.bashrc << 'BASHEOF'
if [ -z "$ENGRAM_STACK_STARTED" ]; then
  export ENGRAM_STACK_STARTED=1
  termux-wake-lock 2>/dev/null
fi
BASHEOF
```

### Auto-start on Android boot (optional)

Requires the **Termux:Boot** app from F-Droid.

```bash
mkdir -p ~/.termux/boot/
cat > ~/.termux/boot/start-engram << 'BOOTEOF'
#!/data/data/com.termux/files/usr/bin/bash
pg_ctl -D ~/engram-cloud/pgdata -l ~/engram-cloud/pgdata/pg.log start
sleep 3
cd ~/engram-cloud && source .env && nohup engram cloud serve > ~/engram-cloud/cloud.log 2>&1 &
sleep 2
nohup cloudflared tunnel run engram-cloud > ~/engram-cloud/tunnel.log 2>&1 &
termux-wake-lock
BOOTEOF
chmod +x ~/.termux/boot/start-engram
```

---

## 9. Phase 8 — Autosync from Windows Client

### Set environment variables in PowerShell

```powershell
# Temporary (current session only)
$env:ENGRAM_CLOUD_SERVER="https://engram.ejemplo.com"
$env:ENGRAM_CLOUD_AUTOSYNC="1"
$env:ENGRAM_CLOUD_TOKEN="your-cloud-token-here"
```

### Configure cloud server

```powershell
engram cloud config --server https://engram.ejemplo.com
engram cloud enroll your-project
```

### Permanent variables (survive PowerShell restart)

```powershell
[Environment]::SetEnvironmentVariable("ENGRAM_CLOUD_SERVER", "https://engram.ejemplo.com", "User")
[Environment]::SetEnvironmentVariable("ENGRAM_CLOUD_AUTOSYNC", "1", "User")
[Environment]::SetEnvironmentVariable("ENGRAM_CLOUD_TOKEN", "your-cloud-token-here", "User")
```

### ⚠️ Issue: Autosync degraded due to unenrolled projects

```
Sync diagnostic: degraded
reason_code: non_enrolled_pending_mutations
reason_message: pending cloud sync mutations are blocked because
project(s) are not enrolled: antigravity=303, antigravity ide=10, ...
```

**Solution:** The local daemon has pending mutations from unenrolled projects blocking autosync. Stop the serve and restart:

```powershell
# 1. Stop daemon
taskkill /IM engram.exe /F

# 2. Enroll the projects you care about
engram cloud enroll your-project

# 3. Force manual sync
engram sync --cloud --project your-project

# 4. Start serve with autosync
$env:ENGRAM_CLOUD_AUTOSYNC="1"
$env:ENGRAM_CLOUD_TOKEN="your-cloud-token-here"
$env:ENGRAM_CLOUD_SERVER="https://engram.ejemplo.com"
engram serve
```

Verify in the log:

```
2026/06/10 11:33:16 [autosync] started (server=https://engram.ejemplo.com)
```

### Test autosync

```powershell
engram save "Test autosync" "Appears automatically in dashboard" --project your-project
engram cloud status
# Should say: Cloud autosync: enabled
```

The memory appears automatically at:
`https://engram.ejemplo.com/dashboard/projects/your-project`

---

## 10. Issues Encountered and Solutions

| # | Issue | Symptom | Cause | Solution |
|---|-------|---------|-------|----------|
| 1 | `go.mod not found` | `go build` fails | `git clone` without `.` creates a subdirectory | `git clone --depth=1 https://github.com/... .` |
| 2 | Postgres won't start | `connection refused` | Android lacks `sysv` shared memory | `dynamic_shared_memory_type = mmap` in postgresql.conf |
| 3 | `source .env` doesn't work | Variables not visible to `engram` | `source` loads without `export` | `sed -i 's/^ENGRAM_/export ENGRAM_/' .env` |
| 4 | `blocked_unenrolled` | Sync fails | Project not enrolled | `engram cloud enroll <project>` before syncing |
| 5 | DNS route fails | `code: 1003` | Apex domain already has an A/AAAA/CNAME record | Use a subdomain: `engram.ejemplo.com` |
| 6 | `cloudflared tunnel login --force` doesn't exist | `flag provided but not defined: -force` | No `--force` flag exists | Run `cloudflared tunnel login` without flags |
| 7 | Autosync degraded | `non_enrolled_pending_mutations` | Many unenrolled local projects block the daemon | `taskkill /F engram.exe`, enroll projects, restart serve |
| 8 | False positive update notice | `Update available: termux-v1.16.1 -> 1.16.1` | `termux-` prefix in version string | Ignore, cosmetic only |
| 9 | `export` doesn't work in PowerShell | `export: The term is not recognized` | Running PowerShell, not bash | Use `$env:VAR="value"` in PowerShell |

---

## 11. Quick Status Commands

### On Termux

```bash
# Verify services
pg_isready
curl -sf http://127.0.0.1:18080/health
curl -sf https://engram.ejemplo.com/health

# Logs
tail -f ~/engram-cloud/pgdata/pg.log
tail -f ~/engram-cloud/cloud.log
tail -f ~/engram-cloud/tunnel.log

# Service status via termux-services
sv status postgresql
sv status engram-cloud
sv status cloudflared

# Graceful shutdown
kill $(pgrep -f "cloudflared tunnel") 2>/dev/null
kill $(pgrep -f "engram cloud serve") 2>/dev/null
pg_ctl -D ~/engram-cloud/pgdata stop
```

### On Windows (PowerShell)

```powershell
engram cloud status
engram save "Message" "Content" --project your-project
```

### On any client machine

```bash
engram cloud config --server https://engram.ejemplo.com
export ENGRAM_CLOUD_TOKEN="your-cloud-token-here"
engram cloud enroll your-project
engram sync --cloud --project your-project
```

---

## 12. Final Stack Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                         ANDROID / TERMUX                            │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                     ~/engram-cloud/                         │    │
│  │                                                             │    │
│  │  📁 engram/           ← source code (git clone)            │    │
│  │  📁 pgdata/           ← Postgres data (persistent)         │    │
│  │  📄 .env              ← secrets + configuration            │    │
│  │  📄 cloud.log         ← Engram Cloud stdout                │    │
│  │  📄 tunnel.log        ← Cloudflare Tunnel stdout           │    │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                    │
│  ┌─────────────────────┐    ┌──────────────────────────┐           │
│  │  Postgres 18        │    │  Engram Cloud            │           │
│  │  localhost:5432     │◄──►│  localhost:18080         │           │
│  │  engram_cloud DB    │    │  /dashboard, /sync/*     │           │
│  │  trust auth (local) │    │  ENGRAM_CLOUD_ALLOWED=*  │           │
│  └─────────────────────┘    └──────────┬───────────────┘           │
│                                        │                           │
│                               ┌────────┴───────────────┐           │
│                               │  Cloudflare Tunnel     │           │
│                               │  cloudflared → :18080  │           │
│                               │  engram.ejemplo.com    │           │
│                               └────────────────────────┘           │
│                                                                    │
│  🔒 termux-wake-lock         → Android keeps process alive         │
│  🔄 termux-services (runsv)  → auto-start services                 │
│  📋 ~/.termux/boot/         → auto-start on boot (optional)        │
└─────────────────────────────────────────────────────────────────────┘
                               │
                               ▼  HTTPS
┌─────────────────────────────────────────────────────────────────────┐
│  WINDOWS CLIENT                                                     │
│                                                                     │
│  engram serve (autosync=1)  ───────► engram.ejemplo.com             │
│  port 7437                              │                           │
│  environment variables:                 ▼                           │
│  ENGRAM_CLOUD_AUTOSYNC=1        Cloudflare Tunnel                   │
│  ENGRAM_CLOUD_SERVER=...              │                             │
│  ENGRAM_CLOUD_TOKEN=...               ▼                             │
│                                  Engram Cloud (Termux)              │
│                                        │                            │
│                                        ▼                            │
│                                  Postgres 18                        │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 13. Next Steps

- [Engram Cloud Quickstart](./quickstart.md) — official Docker Compose and GHCR setup
- [Engram Cloud Troubleshooting](./troubleshooting.md) — common cloud error resolution
- [Engram Architecture](../../docs/ARCHITECTURE.md) — understand sync model, sessions, and topics
- [Cloud Autosync](../../DOCS.md#cloud-autosync) — technical background autosync documentation
- [Agent Setup](../../docs/AGENT-SETUP.md) — configure clients (OpenCode, Claude Code, Gemini CLI, etc.)
- [Team Usage](../../docs/TEAM-USAGE.md) — scope conventions and sync behavior for teams

---

> Originally documented on June 10, 2026.  
> Stack tested end-to-end: Windows (save + autosync) → Cloudflare Tunnel → Engram Cloud (Termux) → Local Postgres.

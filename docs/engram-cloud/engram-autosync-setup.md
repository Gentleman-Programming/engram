# Engram from zero: install, cloud sync, and always-on autosync (macOS)

This guide takes you from "never used Engram" to a setup where:

1. Your AI agents (Claude Code, etc.) save persistent memories locally.
2. Those memories sync to the Nauta Engram cloud.
3. The sync keeps working even if you close every shell, log out, or reboot — via a `launchd` agent, macOS's native service manager.

Everything below was verified on Engram **v1.19.0** installed via Homebrew (binary at `/opt/homebrew/bin/engram`, data at `~/.engram/`).

---

## How Engram works (30-second mental model)

- **Saving is local and always safe.** Agent tools (`mem_save`, etc.) write straight to a SQLite database at `~/.engram/engram.db`. No daemon or network needed. You never lose a memory by closing a shell.
- **Every save also queues a "sync mutation"** in that same database, marked unacked until it reaches the cloud.
- **Cloud upload is a separate step.** The queue is flushed either by an explicit `engram sync --cloud --project <name>`, or automatically by the `engram serve` daemon **when autosync is enabled** (`ENGRAM_CLOUD_AUTOSYNC=1`). If neither is in place, memories pile up locally and the cloud copy goes stale — that's the problem this guide fixes.

---

## Step 1 — Install Engram

```bash
brew install gentleman-programming/tap/engram
engram version   # expect v1.19.0 or newer
```

## Step 2 — Hook it into your agent (optional but recommended)

For Claude Code:

```bash
engram setup claude-code
```

This registers Engram as an MCP server so tools like `mem_save` / `mem_search` are available inside your agent sessions. (Other supported agents: `engram setup <agent>` — cursor, codex, gemini-cli, etc.)

## Step 3 — Point Engram at the cloud server

```bash
engram cloud config --server https://ingress.internal.getnauta.dev/api/nauta-engram/
```

This writes `~/.engram/cloud.json`. Verify with:

```bash
engram cloud status
```

You want to see `Cloud status: configured` and `Auth status: ready`.

## Step 4 — Enroll your project

Cloud sync is opt-in **per project**. The project name is auto-detected from the git remote of the directory you work in. This guide uses a disposable test project, `smoke-project`, so you can practice safely — swap in your real project name when you're done testing.

```bash
engram cloud enroll smoke-project
```

Repeat for any other project you want synced.

## Step 5 — Smoke-test the whole pipeline once

```bash
# save something
engram save "cloud-test" "verifying cloud sync end-to-end" --project smoke-project

# push the queue to the cloud
engram sync --cloud --project smoke-project
```

You should see `Cloud sync complete for project "smoke-project"` with a chunk id and mutation count. If this works manually, automation will work too.

---

## Why memories weren't reaching the cloud (root cause, verified 2026-07-10)

Diagnosed in practice with `engram cloud status`, `engram doctor`, and the [official engram-cloud docs](https://github.com/Gentleman-Programming/engram/blob/main/docs/engram-cloud/quickstart.md):

- **Autosync lives inside the `engram serve` daemon.** The MCP tools (`mem_save`, etc.) only write to local SQLite and enqueue mutations — they never push to the cloud themselves.
- **The daemon wasn't running** (no process, no launchd agent), so nothing ever flushed the queue. `engram cloud status` says it directly: *"Local daemon: not running on port 7437 — run `engram serve` to resume autosync"*.
- **`ENGRAM_CLOUD_AUTOSYNC=1` was never set.** Autosync requires three env vars — `ENGRAM_CLOUD_AUTOSYNC`, `ENGRAM_CLOUD_TOKEN`, `ENGRAM_CLOUD_SERVER` — and only the token was exported. Even a running daemon would have stayed idle.
- The one stale chunk on the server came from the single manual `engram sync --cloud` run during Step 5.

Symptom to recognize it by: `engram sync --status --cloud --project <name>` shows more local chunks than remote (e.g. `Local chunks: 5, Remote chunks: 1`).

The steps below fix all three conditions permanently.

## Step 6 — Export the cloud env vars for ALL sessions

Two different kinds of "session" need the variables, and they do **not** share configuration:

**1. Interactive shells** (terminals, and any agent session spawned from them) — add all three exports to `~/.zshrc`:

```bash
# ~/.zshrc — engram cloud sync
export ENGRAM_CLOUD_TOKEN=<your-bearer-token>
export ENGRAM_CLOUD_SERVER=https://ingress.internal.getnauta.dev/api/nauta-engram/
export ENGRAM_CLOUD_AUTOSYNC=1
```

Apply and confirm:

```bash
source ~/.zshrc
env | grep ENGRAM   # all three must appear
```

**2. launchd services** — launchd does **not** read `~/.zshrc`. The daemon plist must carry its own `EnvironmentVariables` block (included in Step 7). This is the trap that silently disables autosync if you only export in the shell.

Note: `ENGRAM_CLOUD_SERVER` overlaps with `~/.engram/cloud.json` (written in Step 3), but exporting it explicitly keeps the CLI, the daemon, and future machine bootstraps consistent.

## Step 7 — Run the autosync daemon, alive across reboots (launchd + KeepAlive)

Running `engram serve` in a terminal works, but it dies the moment that terminal (or the agent session that spawned it) closes. The fix is a **launchd agent**: macOS starts it at login and restarts it if it ever crashes. With the autosync env vars embedded, the daemon flushes the cloud queue in the background for every enrolled project — no separate timer needed.

Run this single command — it generates the plist (injecting the real token and server from your Step 6 exports, so make sure the current shell has them: `env | grep ENGRAM`) and (re)loads the agent:

```bash
cat > ~/Library/LaunchAgents/dev.engram.serve.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.engram.serve</string>

    <key>ProgramArguments</key>
    <array>
        <string>/opt/homebrew/bin/engram</string>
        <string>serve</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
        <key>ENGRAM_CLOUD_AUTOSYNC</key>
        <string>1</string>
        <key>ENGRAM_CLOUD_TOKEN</key>
        <string>${ENGRAM_CLOUD_TOKEN}</string>
        <key>ENGRAM_CLOUD_SERVER</key>
        <string>${ENGRAM_CLOUD_SERVER}</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>/tmp/engram-serve.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/engram-serve.err.log</string>
</dict>
</plist>
EOF
launchctl bootout gui/$(id -u)/dev.engram.serve 2>/dev/null; \
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.engram.serve.plist
```

The heredoc is unquoted on purpose: `${ENGRAM_CLOUD_TOKEN}` and `${ENGRAM_CLOUD_SERVER}` expand from your shell, so the written plist contains the literal values (launchd can't expand variables itself). The `bootout` before `bootstrap` makes the command idempotent — safe to re-run after editing.

Verify:

```bash
launchctl list | grep engram          # should show dev.engram.serve
curl -s http://127.0.0.1:7437/health  # {"service":"engram","status":"ok",...}
engram cloud status                   # "Local daemon: running on port 7437"
```

`KeepAlive` means launchd restarts the daemon if it dies; `RunAtLoad` means it starts at every login. No shell required, survives reboots.

## Step 8 — Verify autosync end-to-end

```bash
# 1. save a memory without any daemon interaction
engram save "autosync-test" "saved after automation setup" --project smoke-project

# 2. give autosync a moment, then compare local vs remote chunks
engram sync --status --cloud --project smoke-project
# healthy: Local chunks == Remote chunks

# 3. confirm nothing is left unacked
sqlite3 ~/.engram/engram.db \
  "SELECT count(*) FROM sync_mutations WHERE acked_at IS NULL AND project='smoke-project';"
# expect: 0
```

If the queue isn't draining, check `/tmp/engram-serve.err.log` and the Troubleshooting section. You can always force a flush manually:

```bash
engram sync --cloud --project smoke-project
```

---

## Day-to-day operations cheat sheet

| Task | Command |
|------|---------|
| Is the daemon up? | `engram cloud status` or `curl -s 127.0.0.1:7437/health` |
| What's pending upload? | `sqlite3 ~/.engram/engram.db "SELECT seq, entity, project FROM sync_mutations WHERE acked_at IS NULL;"` |
| Local vs remote chunks | `engram sync --status --cloud --project <name>` |
| Force a sync now | `engram sync --cloud --project <name>` |
| Restart the daemon | `launchctl kickstart -k gui/$(id -u)/dev.engram.serve` |
| Stop the daemon | `launchctl bootout gui/$(id -u)/dev.engram.serve` |
| Health diagnostics | `engram doctor --json` |
| See all projects | `engram projects list` |

## Troubleshooting

- **`launchctl bootstrap` says "Bootstrap failed: 5: Input/output error"** — the agent is already loaded. Unload first: `launchctl bootout gui/$(id -u)/dev.engram.serve`, then bootstrap again.
- **Daemon runs but the queue never drains** — almost always a missing `ENGRAM_CLOUD_AUTOSYNC=1` in the plist's `EnvironmentVariables` (launchd ignores `~/.zshrc`). Confirm what the daemon actually sees: `launchctl print gui/$(id -u)/dev.engram.serve | grep -A5 environment`.
- **Sync log shows auth errors** — re-check `engram cloud status`; the token comes from runtime cloud config, and the project must be enrolled (`engram cloud enroll <project>`).
- **Sync fails with `blocked_unenrolled` / `paused` / `policy_forbidden`** — server-side states from the cloud control plane: enroll the project, or ask an admin to resume sync from the dashboard.
- **After a brew upgrade the daemon disappears** — launchd points at `/opt/homebrew/bin/engram`, which is a stable symlink, so upgrades are normally fine; if the daemon is flapping, check `/tmp/engram-serve.err.log`.
- **Memories saved but never reach the cloud** — remember: saving is local-only by design. Check the pending-queue query above, then confirm the daemon is loaded (`launchctl list | grep engram`) and autosync is enabled.
- **Fallback if autosync misbehaves** — a timer-style launchd agent running `engram sync --cloud --project <name>` on a `StartInterval` (e.g. 900 s) also works; one agent per project, distinct `Label`s. Autosync via the daemon supersedes this approach.

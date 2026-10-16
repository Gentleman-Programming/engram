[← Back to README](../README.md)

# Installation

- [Choose a release channel before installing](#choose-a-release-channel-before-installing)
- [Homebrew (macOS / Linux)](#homebrew-macos--linux)
- [Windows](#windows)
- [Install from source (macOS / Linux)](#install-from-source-macos--linux)
- [Download binary (all platforms)](#download-binary-all-platforms)
- [Requirements](#requirements)
- [Data-directory filesystem safety](#data-directory-filesystem-safety)
- [Environment Variables](#environment-variables)
- [Windows Config Paths](#windows-config-paths)

---

## Choose a release channel before installing

The latest stable release is the production and security-supported line. Release candidates are prerelease validation and feedback builds, not a universal production recommendation or a guaranteed security-supported channel. See the [Release Policy](./RELEASE-POLICY.md) for channel, upgrade, and rollback guidance.

---

## Homebrew (macOS / Linux)

```bash
brew install gentleman-programming/tap/engram
```

Upgrade to latest:

```bash
brew update && brew upgrade engram
```

> **Migrating from Cask?** If you installed engram before v1.0.1, it was distributed as a Cask. Uninstall first, then reinstall:
> ```bash
> brew uninstall --cask engram 2>/dev/null; brew install gentleman-programming/tap/engram
> ```

> **Keep `engram serve` running across `brew upgrade`?** On macOS, `brew upgrade engram` replaces the binary and kills any running `engram serve` process — autosync stops silently until you relaunch it. To make autosync survive upgrades and reboots, use the launchd template in [Running as a Service → Using launchd (macOS)](../DOCS.md#using-launchd-macos). Run `engram cloud status` afterwards: the `Local daemon:` line should report `running`.

---

## Windows

**Option A: Install via `go install` (recommended for technical users)**

If you have Go installed, you can compile the binary locally from source, but this does not guarantee protection from antivirus alerts or Windows security-policy blocking.

Go's [Semantic Import Versioning](https://go.dev/ref/mod#major-version-suffixes) rule puts the major version in the module path starting at major version 2, so each major line has its own `go install` path. Pick the line that matches the major version you want:

```powershell
# Current line (v3.x)
go install github.com/Gentleman-Programming/engram/v3/cmd/engram@latest

# Previous lines, only if you must stay on an older major version
go install github.com/Gentleman-Programming/engram/v2/cmd/engram@latest
go install github.com/Gentleman-Programming/engram/cmd/engram@latest
# Binary goes to %GOPATH%\bin\engram.exe (typically %USERPROFILE%\go\bin\)
```

The `/v3` command requires `v3.0.0` or a later v3 release. `@latest` selects the latest released version for the requested module path only; it never moves you to another major line.

Ensure `%GOPATH%\bin` (or `%USERPROFILE%\go\bin`) is on your `PATH`.

**Option B: Build from source**

```powershell
git clone https://github.com/Gentleman-Programming/engram.git
cd engram
go install ./cmd/engram
# Binary goes to %GOPATH%\bin\engram.exe (typically %USERPROFILE%\go\bin\)
```

> **Want a real version string instead of `dev`?**
>
> `go install` always stamps the binary as `dev`. To get a meaningful version, pick one of these — not both. Running them both leaves two binaries on disk and `engram version` keeps reporting `dev` because PATH still resolves to the `go install` build.
>
> **Option B1 — version-stamped `go install` (binary stays on PATH):**
>
> ```powershell
> $v = git describe --tags --always
> go install -ldflags="-X main.version=local-$v" ./cmd/engram
> ```
>
> **Option B2 — `go build` and move the result onto PATH:**
>
> ```powershell
> $v = git describe --tags --always
> go build -ldflags="-X main.version=local-$v" -o engram.exe ./cmd/engram
> Move-Item -Force engram.exe "$env:USERPROFILE\go\bin\engram.exe"
> ```
>
> After either option, `engram version` should print `local-<git-describe>` instead of `dev`.

**Option C: Download the prebuilt binary**

1. Go to [GitHub Releases](https://github.com/Gentleman-Programming/engram/releases)
2. Download `engram_<version>_windows_amd64.zip` (or `arm64` for ARM devices)
3. Extract `engram.exe` to a folder in your `PATH` (e.g. `C:\Users\<you>\bin\`)

```powershell
# Example: extract and add to PATH (PowerShell)
Expand-Archive engram_*_windows_amd64.zip -DestinationPath "$env:USERPROFILE\bin"
# Add to PATH permanently (run once):
[Environment]::SetEnvironmentVariable("Path", "$env:USERPROFILE\bin;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

> **Antivirus false positives on prebuilt binaries**
>
> Windows Defender and other antivirus tools (ESET, Brave's built-in scanner) have flagged some
> engram prebuilt releases as malware (`Trojan:Script/Wacatac.H!ml` or similar). This is a
> **heuristic false positive**. The binary is built reproducibly from the public source code
> via GoReleaser and contains no malicious code.
>
> **Why does this happen?** Prebuilt binaries from small open-source projects are unsigned (code
> signing certificates cost hundreds of dollars per year). Many AV engines automatically flag
> unsigned executables from unknown publishers, especially recently compiled Go binaries. The
> same alert has been observed on Claude Code's own MSIX installer, which confirms this is an
> AV heuristic issue, not a code problem.
>
> **Maintainer stance:** We will not pay for a code signing certificate at this time. This is a
> distribution trust problem, not a security problem. The source code is fully auditable.
>
> **Recommended workaround:** Technical Windows users should prefer **Option A (`go install`)** or
> **Option B (build from source)**. Building locally is an option, not a guarantee against
> antivirus alerts or Windows security-policy blocking.

> **Other Windows notes:**
> - Data is stored in `%USERPROFILE%\.engram\engram.db`
> - Override with `ENGRAM_DATA_DIR` environment variable
> - All core features work natively: CLI, MCP server, TUI, HTTP API, Git Sync
> - No WSL required for the core binary — it's a native Windows executable

---

## Windows troubleshooting: executable blocked before MCP starts

**Distinguish a missing executable from Windows rejecting an existing one.**
Smart App Control or another security policy can prevent `engram.exe` from
starting, including a locally compiled `go install` binary. Building locally is
not a guarantee against security-policy blocking, despite the antivirus guidance
above.

| Observation | What to check |
|-------------|---------------|
| The configured executable does not exist, or the client resolves a different file | Check the exact MCP executable path and the installation location; this is a path-resolution problem. |
| Windows explicitly reports that security policy blocked the existing executable | Treat this as an OS launch rejection, not an MCP protocol or argument error. |
| Git Bash reports `Permission denied` / exit 126, or an MCP client reports `EUNKNOWN: uv_spawn` | These errors alone do not identify Smart App Control; correlate them with the Windows launch error and security-policy evidence. |
| Authenticode status is `NotSigned` | This describes the file's signature, but does not by itself prove which policy blocked it. A signature status alone is not a policy diagnosis. |

Issue #1703 reports Windows 11 Smart App Control enabled, an unsigned
`engram.exe`, a PowerShell policy rejection, Git Bash exit 126, and a Claude Code
MCP `EUNKNOWN: uv_spawn` error. These are reported observations, not an
independently reproduced diagnosis for every similar startup failure.

If Windows rejects the executable before it starts, the MCP server cannot begin
its protocol exchange. Reinstalling or changing MCP arguments cannot be assumed
to resolve that rejection. Locally compiled binaries are separate from release
artifacts: signing a distributed release would not sign a local `go install`
build. This guidance does not establish the signing status of current downloads
or identify a signed workaround.

**For a support report, collect only the relevant evidence:**

- Installation route: `go install`, local source build, downloaded release, or installer.
- Exact executable path configured in the MCP client, and whether that file exists.
- Exact error from an already attempted direct launch of that same file, including the shell used; do not substitute only the MCP client's generic error.
- Authenticode signature status for that same file, plus any explicit Windows security-policy message or known Smart App Control state.

Redact personal path components when sharing evidence. Do not disable Smart App
Control, antivirus, or other security controls to troubleshoot this issue. On a
managed device, ask the security administrator to review the rejection under the
organization's policy rather than attempting a bypass.

---

## Install from source (macOS / Linux)

Pick the line that matches the major version you want — the `/vN` suffix exists starting at major version 2 because of Go's [Semantic Import Versioning](https://go.dev/ref/mod#major-version-suffixes) rule:

```bash
# Current line (v3.x)
go install github.com/Gentleman-Programming/engram/v3/cmd/engram@latest

# Previous lines, only if you must stay on an older major version
go install github.com/Gentleman-Programming/engram/v2/cmd/engram@latest
go install github.com/Gentleman-Programming/engram/cmd/engram@latest
# Binary goes to $GOPATH/bin (typically ~/go/bin/)
```

The `/v3` command requires `v3.0.0` or a later v3 release. `@latest` selects the latest released version for the requested module path only; it never moves you to another major line.

Or build from a local clone:

```bash
git clone https://github.com/Gentleman-Programming/engram.git
cd engram
go install ./cmd/engram
```

> **Want a real version string instead of `dev`?**
>
> `go install` always stamps the binary as `dev`. To get a meaningful version, pick one of these — not both. Running them both leaves two binaries on disk and `engram version` keeps reporting `dev` because PATH still resolves to the `go install` build.
>
> **Option 1 — version-stamped `go install` (binary stays on PATH):**
>
> ```bash
> go install -ldflags="-X main.version=local-$(git describe --tags --always)" ./cmd/engram
> ```
>
> **Option 2 — `go build` and move the result onto PATH:**
>
> ```bash
> go build -ldflags="-X main.version=local-$(git describe --tags --always)" -o engram ./cmd/engram
> mv engram "$(go env GOPATH)/bin/engram"
> ```
>
> After either option, `engram version` should print `local-<git-describe>` instead of `dev`.

---

## Download binary (all platforms)

Grab the latest release for your platform from [GitHub Releases](https://github.com/Gentleman-Programming/engram/releases).

| Platform | File |
|----------|------|
| macOS (Apple Silicon) | `engram_<version>_darwin_arm64.tar.gz` |
| macOS (Intel) | `engram_<version>_darwin_amd64.tar.gz` |
| Linux (x86_64) | `engram_<version>_linux_amd64.tar.gz` |
| Linux (ARM64) | `engram_<version>_linux_arm64.tar.gz` |
| Windows (x86_64) | `engram_<version>_windows_amd64.zip` |
| Windows (ARM64) | `engram_<version>_windows_arm64.zip` |

---

## Requirements

- **Go 1.24+** to build from source (not needed if installing via Homebrew or downloading a binary)
- That's it. No runtime dependencies.

The binary includes SQLite (via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no CGO). Works natively on **macOS**, **Linux**, and **Windows** (x86_64 and ARM64).

---

## Data-directory filesystem safety

Engram uses persistent SQLite WAL and rejects known NFS and SMB/CIFS data directories before changing the SQLite files. Use a local disk for `ENGRAM_DATA_DIR`; an unknown filesystem remains compatible but is not proven local.

If startup rejects a network data directory, stop **all** Engram processes. Copy the complete `engram.db`, `engram.db-wal`, and `engram.db-shm` triplet to local storage, set `ENGRAM_DATA_DIR` to the absolute path of that local directory (relative paths are rejected), then start Engram and run `engram doctor`. Check SQLite integrity with the command for your shell:

```bash
# POSIX shell or Git Bash
sqlite3 "$ENGRAM_DATA_DIR/engram.db" "PRAGMA integrity_check;"
```

```powershell
# PowerShell
sqlite3 (Join-Path $env:ENGRAM_DATA_DIR 'engram.db') 'PRAGMA integrity_check;'
```

Engram does not auto-repair, quarantine, checkpoint, or use rollback journaling as a fallback.

---

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ENGRAM_DATA_DIR` | Engram CLI data directory. Empty or whitespace-only values use the platform default; nonblank values are used as provided. | `~/.engram` (Windows: `%USERPROFILE%\.engram`) |
| `ENGRAM_PORT` | HTTP server port. Use an unsigned decimal value from `1` through `65535`; invalid values fall back to `7437` in `engram serve` and Claude Bash hooks. | `7437` |
| `ENGRAM_SOCKET` | POSIX-only Unix-domain socket path. Run `engram serve --socket /path/to/engram.sock` (or set `ENGRAM_SOCKET`) to listen exclusively on the socket; do not combine it with an explicit TCP port. Claude Bash hooks use the same socket when the variable is exported and warn on stderr if socket transport cannot preserve memory capture. PowerShell remains TCP-only. | (unset) |

---

## Windows Config Paths

When using `engram setup`, config files are written to platform-appropriate locations:

| Agent | macOS / Linux | Windows |
|-------|---------------|---------|
| OpenCode | `~/.config/opencode/` | `%APPDATA%\opencode\` |
| Gemini CLI | `~/.gemini/` | `%APPDATA%\gemini\` |
| Codex | `$CODEX_HOME/` when absolute, else `~/.codex/` | `%CODEX_HOME%\` when absolute, else `%USERPROFILE%\.codex\` |
| Claude Code | Managed by `claude` CLI | Managed by `claude` CLI |
| Antigravity CLI | `~/.gemini/config/mcp_config.json` + `~/.gemini/GEMINI.md` | `%APPDATA%\gemini\config\mcp_config.json` + `%APPDATA%\gemini\GEMINI.md` |
| Windsurf | `~/.codeium/windsurf/mcp_config.json` + `.../memories/global_rules.md` | `%USERPROFILE%\.codeium\windsurf\...` |
| Qwen Code | `~/.qwen/settings.json` + `~/.qwen/QWEN.md` | `%USERPROFILE%\.qwen\...` |
| Kiro | `~/.kiro/settings/mcp.json` + `~/.kiro/steering/engram.md` | `%USERPROFILE%\.kiro\...` |
| Cursor | `~/.cursor/mcp.json` + `~/.cursor/rules/engram.mdc` | `%USERPROFILE%\.cursor\...` |
| VS Code Copilot | `~/.config/Code/User/mcp.json` + `.../prompts/engram.instructions.md` (macOS: `~/Library/Application Support/Code/User/`) | `%APPDATA%\Code\User\...` |
| Kilo Code | `~/.config/kilo/opencode.json` + `~/.config/kilo/AGENTS.md` | `%USERPROFILE%\.config\kilo\...` |
| Kimi Code | `~/.kimi-code/mcp.json` + `~/.kimi-code/AGENTS.md` | `%USERPROFILE%\.kimi-code\...` |
| CommandCode | `~/.commandcode/mcp.json` + `~/.commandcode/AGENTS.md` | `%USERPROFILE%\.commandcode\mcp.json` + `%USERPROFILE%\.commandcode\AGENTS.md` |
| Data directory | `~/.engram/` | `%USERPROFILE%\.engram\` |

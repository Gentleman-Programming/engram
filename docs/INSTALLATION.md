[← Back to README](../README.md)

# Installation

- [Homebrew (macOS / Linux)](#homebrew-macos--linux)
- [Windows](#windows)
- [Install from source (macOS / Linux)](#install-from-source-macos--linux)
- [Download binary (all platforms)](#download-binary-all-platforms)
- [Requirements](#requirements)
- [Environment Variables](#environment-variables)
- [Windows Config Paths](#windows-config-paths)

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

If you have Go installed, this is the cleanest and most trustworthy path — the binary is compiled on your machine from source, so no antivirus will flag it:

```powershell
go install github.com/Gentleman-Programming/engram/cmd/engram@latest
# Binary goes to %GOPATH%\bin\engram.exe (typically %USERPROFILE%\go\bin\)
```

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
> **Option B (build from source)**. Binaries you compile locally will not trigger AV alerts because
> they originate from your own machine.

> **Other Windows notes:**
> - Data is stored in `%USERPROFILE%\.engram\engram.db`
> - Override with `ENGRAM_DATA_DIR` environment variable
> - All core features work natively: CLI, MCP server, TUI, HTTP API, Git Sync
> - No WSL required for the core binary — it's a native Windows executable

---

## Install from source (macOS / Linux)

```bash
git clone https://github.com/Gentleman-Programming/engram.git
cd engram
go install ./cmd/engram
# Binary goes to $GOPATH/bin (typically ~/go/bin/)
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

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ENGRAM_DATA_DIR` | Data directory | `~/.engram` (Windows: `%USERPROFILE%\.engram`) |
| `ENGRAM_PORT` | HTTP server port | `7437` |

---

## Windows Config Paths

When using `engram setup`, config files are written to platform-appropriate locations:

| Agent | macOS / Linux | Windows |
|-------|---------------|---------|
| OpenCode | `~/.config/opencode/` | `%APPDATA%\opencode\` |
| Gemini CLI | `~/.gemini/` | `%APPDATA%\gemini\` |
| Codex | `~/.codex/` | `%APPDATA%\codex\` |
| Claude Code | Managed by `claude` CLI | Managed by `claude` CLI |
| VS Code | `.vscode/mcp.json` (workspace) or `~/Library/Application Support/Code/User/mcp.json` (user) | `.vscode\mcp.json` (workspace) or `%APPDATA%\Code\User\mcp.json` (user) |
| Antigravity | `~/.gemini/antigravity/mcp_config.json` | `%USERPROFILE%\.gemini\antigravity\mcp_config.json` |
| Data directory | `~/.engram/` | `%USERPROFILE%\.engram\` |

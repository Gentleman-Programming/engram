[← Back to README](../README.md)

# Agent Setup

Engram works with **any MCP-compatible agent**. Pick your agent below.

## Quick Reference

| Agent | One-liner | Manual Config |
|-------|-----------|---------------|
| Claude Code | `claude plugin marketplace add Gentleman-Programming/engram && claude plugin install engram` | [Details](#claude-code) |
| OpenCode | `engram setup opencode` | [Details](#opencode) |
| Gemini CLI | `engram setup gemini-cli` | [Details](#gemini-cli) |
| Codex | `engram setup codex` | [Details](#codex) |
| Pi | `engram setup pi` | [Details](#pi) |
| VS Code | `code --add-mcp '{"name":"engram","command":"engram","args":["mcp"]}'` | [Details](#vs-code-copilot--claude-code-extension) |
| Antigravity | Manual JSON config | [Details](#antigravity) |
| Cursor | Manual JSON config | [Details](#cursor) |
| Windsurf | Manual JSON config | [Details](#windsurf) |
| Any MCP agent | `engram mcp` (stdio) | [Details](#any-other-mcp-agent) |

---

## OpenCode

> **Prerequisite**: Install the `engram` binary first (via [Homebrew](INSTALLATION.md#homebrew-macos--linux), [Windows binary](INSTALLATION.md#windows), [binary download](INSTALLATION.md#download-binary-all-platforms), or [source](INSTALLATION.md#install-from-source-macos--linux)). The plugin needs it for the MCP server and session tracking.

**Recommended: Full setup with one command** — installs the plugin AND registers the MCP server in `opencode.json` automatically:

```bash
engram setup opencode
```

This does two things:
1. Copies the plugin to `~/.config/opencode/plugins/engram.ts` (session tracking, Memory Protocol, compaction recovery)
2. Adds the `engram` MCP server entry to your `opencode.json` (the 13 memory tools)

The plugin also needs the HTTP server running for session tracking:

```bash
engram serve &
```

> **Windows**: On Windows, `engram setup opencode` writes to `%APPDATA%\opencode\plugins\` and `%APPDATA%\opencode\opencode.json` automatically. To run the server in the background: `Start-Process engram -ArgumentList "serve" -WindowStyle Hidden` (PowerShell) or just run `engram serve` in a separate terminal.

**Alternative: Manual MCP-only setup** (no plugin, just the 13 memory tools):

Add to your `opencode.json` (global: `~/.config/opencode/opencode.json` or project-level; on Windows: `%APPDATA%\opencode\opencode.json`):

```json
{
  "mcp": {
    "engram": {
      "type": "local",
      "command": ["engram", "mcp"],
      "enabled": true
    }
  }
}
```

See [Plugins → OpenCode Plugin](PLUGINS.md#opencode-plugin) for details on what the plugin provides beyond bare MCP.

---

## Claude Code

> **Prerequisite**: Install the `engram` binary first (via [Homebrew](INSTALLATION.md#homebrew-macos--linux), [Windows binary](INSTALLATION.md#windows), [binary download](INSTALLATION.md#download-binary-all-platforms), or [source](INSTALLATION.md#install-from-source-macos--linux)). The plugin needs it for the MCP server and session tracking scripts.

**Option A: Plugin via marketplace (recommended)** — full session management, auto-import, compaction recovery, and Memory Protocol skill:

```bash
claude plugin marketplace add Gentleman-Programming/engram
claude plugin install engram
```

That's it. The plugin registers the MCP server, hooks, and Memory Protocol skill automatically.

**Option B: Plugin via `engram setup`** — same plugin, installed from the embedded binary:

```bash
engram setup claude-code
```

During setup, you'll be asked whether to add engram tools to `~/.claude/settings.json` permissions allowlist — this prevents Claude Code from prompting for confirmation on every memory operation.

**Option C: Bare MCP** — just the 13 memory tools, no session management:

Add to your `.claude/settings.json` (project) or `~/.claude/settings.json` (global):

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

With bare MCP, add a [Surviving Compaction](#surviving-compaction-recommended) prompt to your `CLAUDE.md` so the agent remembers to use Engram after context resets.

> **Windows note:** The Claude Code plugin hooks use bash scripts. On Windows, Claude Code runs hooks through Git Bash (bundled with [Git for Windows](https://gitforwindows.org/)) or WSL. If hooks don't fire, ensure `bash` is available in your `PATH`. Alternatively, use **Option C (Bare MCP)** which works natively on Windows without any shell dependency.

See [Plugins → Claude Code Plugin](PLUGINS.md#claude-code-plugin) for details on what the plugin provides.

---

## Gemini CLI

Recommended: one command to set up MCP + compaction recovery instructions:

```bash
engram setup gemini-cli
```

`engram setup gemini-cli` now does three things:
- Registers `mcpServers.engram` in `~/.gemini/settings.json` (Windows: `%APPDATA%\gemini\settings.json`)
- Writes `~/.gemini/system.md` with the Engram Memory Protocol (includes post-compaction recovery)
- Ensures `~/.gemini/.env` contains `GEMINI_SYSTEM_MD=1` so Gemini actually loads that system prompt

> `engram setup gemini-cli` automatically writes the full Memory Protocol to `~/.gemini/system.md`, so the agent knows exactly when to save, search, and close sessions. No additional configuration needed.

Manual alternative: add to your `~/.gemini/settings.json` (global) or `.gemini/settings.json` (project); on Windows: `%APPDATA%\gemini\settings.json`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

Or via the CLI:

```bash
gemini mcp add engram engram mcp
```

---

## Codex

Recommended: one command to set up MCP + compaction recovery instructions:

```bash
engram setup codex
```

`engram setup codex` now does three things:
- Registers `[mcp_servers.engram]` in `~/.codex/config.toml` (Windows: `%APPDATA%\codex\config.toml`)
- Writes `~/.codex/engram-instructions.md` with the Engram Memory Protocol
- Writes `~/.codex/engram-compact-prompt.md` and points `experimental_compact_prompt_file` to it, so compaction output includes a required memory-save instruction

> `engram setup codex` automatically writes the full Memory Protocol to `~/.codex/engram-instructions.md` and a compaction recovery prompt to `~/.codex/engram-compact-prompt.md`. No additional configuration needed.

Manual alternative: add to your `~/.codex/config.toml` (Windows: `%APPDATA%\codex\config.toml`):

```toml
model_instructions_file = "~/.codex/engram-instructions.md"
experimental_compact_prompt_file = "~/.codex/engram-compact-prompt.md"

[mcp_servers.engram]
command = "engram"
args = ["mcp"]
```

---

## Pi

Recommended: one command for global, offline setup:

```bash
engram setup pi
```

`engram setup pi` materializes a local embedded package and runs Pi's official installer:
- Local package path: `~/.config/pi-coding-agent/packages/engram`
- Package assets:
  - `package.json` (with top-level `pi` manifest)
  - `extensions/engram.ts`
  - `skills/engram/SKILL.md`
- Installer invocation: `pi install ~/.config/pi-coding-agent/packages/engram`

No guessed edits to `~/.config/pi-coding-agent/config.json` are performed by Engram.

Policy defaults for Pi support:
- notify-only at session start when relevant memory exists
- no automatic full-context injection
- extension lifecycle uses validated events: `session_start`, `session_shutdown`, `input`, `session_before_compact`, `session_compact`
- native Pi tools are registered via `pi.registerTool()` with canonical names: `mem_search`, `mem_context`, `mem_save`, `mem_session_summary`, `mem_get_observation`, `mem_save_prompt`
- compaction recovery guidance is available via extension command `/engram-recovery`

After install, run `/reload` in Pi so the extension and tools are reloaded in the current client process.

> Pi contract fields (hook names, manifest schema, notification API) are tracked in `internal/setup/testdata/pi-contract.json` and exercised by deterministic runtime harness tests (`go test ./internal/setup -run TestPiExtensionRuntimeHarnessDeterministic`). Update both if upstream behavior differs.

### Pi Manual E2E Checklist

Use this when validating a real Pi installation outside CI:

1. **Global install + offline path**
   - Disconnect network (or block outbound) and run `engram setup pi`.
   - Confirm local package exists at `~/.config/pi-coding-agent/packages/engram`.
   - Confirm `engram setup pi` invokes `pi install <local-package-dir>`.
2. **Extension load**
   - Start Pi and verify startup banner lists Engram extension/skill.
3. **Auto-start parity**
   - Stop Engram backend, then call native mem_search from Pi in a fresh session (`stop Engram backend, then call native mem_search`).
   - Confirm extension checks `/health` and attempts `engram serve` before tool/lifecycle calls.
4. **Session lifecycle hooks**
   - Start Pi, run `/reload`, then begin and quit a session.
   - Verify `session_start` emits `POST /sessions` and `session_shutdown` emits `POST /sessions/{id}/end` (shutdown metadata must not overwrite an existing non-empty/high-signal summary).
   - Search for session start/end evidence in Engram storage/logs if those traces are available in your environment.
5. **Native memory tools + prompt capture**
   - Confirm Pi lists native tools `mem_search`, `mem_context`, `mem_save`, `mem_session_summary`, `mem_get_observation`, `mem_save_prompt`.
   - Trigger an input turn and verify `input` hook emits `POST /prompts`.
   - Verify extension-generated input (`event.source === "extension"`) is not double-captured.
6. **Notify-only startup policy**
   - Seed memory for the project, start a new Pi session.
   - Confirm extension announces memory availability through `ctx.ui.notify(...)` but does **not** auto-fetch/inject `/context`.
7. **Recovery guidance + compaction hooks**
   - Run `/engram-recovery` and verify message starts with `FIRST ACTION REQUIRED`.
   - Run `/compact`, then verify compaction summary persistence (`run `/compact`, then verify compaction summary persistence`) via `POST /sessions/{id}/end` or recovery notification fallback.
   - After compaction, call `mem_context` manually (no automatic context injection).
8. **Privacy sanitization**
   - Start/end session values containing `<private>...</private>` and verify outbound payloads are `[REDACTED]`.

### Pi Troubleshooting: Duplicate package installs

If Pi reports shortcut/skill conflicts that reference unrelated packages (for example stale `pi-btw` temp installs), clean those duplicates separately from Engram:

1. List installed Pi packages and identify duplicate or malformed temporary paths.
2. Remove only the stale duplicate package directories (do **not** remove `~/.config/pi-coding-agent/packages/engram`).
3. Re-run `pi install ~/.config/pi-coding-agent/packages/engram` or `engram setup pi`.

These conflicts are environment cleanup issues, not Engram runtime behavior.

### Pi Troubleshooting: Auto-start cannot find Engram binary

If native `mem_*` calls report auto-start failures, Pi could be resolving an old/invalid `engram` binary from `PATH`.

1. Verify `engram` in your shell resolves to the expected binary (`which engram` / `command -v engram`).
2. Ensure that binary is available in the environment used to launch Pi.
3. If needed, launch Pi with an explicit override: `ENGRAM_BIN=/path/to/engram pi`

`ENGRAM_BIN` takes precedence over the default binary lookup inside the Pi extension.

If any checkpoint fails due to Pi API differences, update:
- `internal/setup/testdata/pi-contract.json` (assumptions)
- `plugin/pi/extensions/engram.ts` + embedded copy via `go generate ./internal/setup/`
- this checklist section

---

## VS Code (Copilot / Claude Code Extension)

VS Code supports MCP servers natively in its chat panel (Copilot agent mode). This works with **any** AI agent running inside VS Code — Copilot, Claude Code extension, or any other MCP-compatible chat provider.

**Option A: Workspace config** (recommended for teams — commit to source control):

Add to `.vscode/mcp.json` in your project:

```json
{
  "servers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

**Option B: User profile** (global, available across all workspaces):

1. Open Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`)
2. Run **MCP: Open User Configuration**
3. Add the same `engram` server entry above to VS Code User `mcp.json`:
   - macOS: `~/Library/Application Support/Code/User/mcp.json`
   - Linux: `~/.config/Code/User/mcp.json`
   - Windows: `%APPDATA%\Code\User\mcp.json`

**Option C: CLI one-liner:**

```bash
code --add-mcp "{\"name\":\"engram\",\"command\":\"engram\",\"args\":[\"mcp\"]}"
```

> **Using Claude Code extension in VS Code?** The Claude Code extension runs inside VS Code but uses its own MCP config. Follow the [Claude Code](#claude-code) instructions above — the `.claude/settings.json` config works whether you use Claude Code as a CLI or as a VS Code extension.

> **Windows**: Make sure `engram.exe` is in your `PATH`. VS Code resolves MCP commands from the system PATH.

**Adding the Memory Protocol** (recommended — teaches the agent when to save and search memories):

Without the Memory Protocol, the agent has the tools but doesn't know WHEN to use them. Add these instructions to your agent's prompt:

**For Copilot:** Create a `.instructions.md` file in the VS Code User `prompts/` folder and paste the Memory Protocol from [DOCS.md](../DOCS.md#memory-protocol-full-text).

Recommended file path:
- macOS: `~/Library/Application Support/Code/User/prompts/engram-memory.instructions.md`
- Linux: `~/.config/Code/User/prompts/engram-memory.instructions.md`
- Windows: `%APPDATA%\Code\User\prompts\engram-memory.instructions.md`

**For any VS Code chat extension:** Add the Memory Protocol text to your extension's custom instructions or system prompt configuration.

The Memory Protocol tells the agent:
- **When to save** — after bugfixes, decisions, discoveries, config changes, patterns
- **When to search** — reactive ("remember", "recall") + proactive (overlapping past work)
- **Session close** — mandatory `mem_session_summary` before ending
- **After compaction** — recover state with `mem_context`

See [Surviving Compaction](#surviving-compaction-recommended) for the minimal version, or [DOCS.md](../DOCS.md#memory-protocol-full-text) for the full Memory Protocol text you can copy-paste.

---

## Antigravity

[Antigravity](https://antigravity.google) is Google's AI-first IDE with native MCP and skill support.

**Add the MCP server** — open the MCP Store (`...` dropdown in the agent panel) → **Manage MCP Servers** → **View raw config**, and add to `~/.gemini/antigravity/mcp_config.json`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

**Adding the Memory Protocol** (recommended):

Add the Memory Protocol as a global rule in `~/.gemini/GEMINI.md`, or as a workspace rule in `.agent/rules/`. See [DOCS.md](../DOCS.md#memory-protocol-full-text) for the full text, or use the minimal version from [Surviving Compaction](#surviving-compaction-recommended).

> **Note:** Antigravity has its own skill, rule, and MCP systems separate from VS Code. Do not use `.vscode/mcp.json`.

---

## Cursor

Add to your `.cursor/mcp.json` (same path on all platforms — it's project-relative):

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

> **Windows**: Make sure `engram.exe` is in your `PATH`. Cursor resolves MCP commands from the system PATH.

> **Memory Protocol:** Cursor uses `.mdc` rule files stored in `.cursor/rules/` (Cursor 0.43+). Create an `engram.mdc` file (any name works — the `.mdc` extension is what matters) and place it in one of:
> - **Project-specific:** `.cursor/rules/engram.mdc` — commit to git so your whole team gets it
> - **Global (all projects):** `~/.cursor/rules/engram.mdc` (Windows: `%USERPROFILE%\.cursor\rules\engram.mdc`) — create the directory if it doesn't exist
>
> See [DOCS.md](../DOCS.md#memory-protocol-full-text) for the full text, or use the minimal version from [Surviving Compaction](#surviving-compaction-recommended).
>
> **Note:** The legacy `.cursorrules` file at the project root is still recognized by Cursor but is deprecated. Prefer `.cursor/rules/` for all new setups.

---

## Windsurf

Add to your `~/.windsurf/mcp.json` (Windows: `%USERPROFILE%\.windsurf\mcp.json`):

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "args": ["mcp"]
    }
  }
}
```

> **Memory Protocol:** Add the Memory Protocol instructions to your `.windsurfrules` file. See [DOCS.md](../DOCS.md#memory-protocol-full-text) for the full text.

---

## Any other MCP agent

The pattern is always the same — point your agent's MCP config to `engram mcp` via stdio transport.

---

## Surviving Compaction (Recommended)

When your agent compacts (summarizes long conversations to free context), it starts fresh — and might forget about Engram. To make memory truly resilient, add this to your agent's system prompt or config file:

**For Claude Code** (`CLAUDE.md`):
```markdown
## Memory
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
- Save proactively after significant work — don't wait to be asked.
- After any compaction or context reset, call `mem_context` to recover session state before continuing.
```

**For OpenCode** (agent prompt in `opencode.json`):
```
After any compaction or context reset, call mem_context to recover session state before continuing.
Save memories proactively with mem_save after significant work.
```

**For Gemini CLI** (`GEMINI.md`):
```markdown
## Memory
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
- Save proactively after significant work — don't wait to be asked.
- After any compaction or context reset, call `mem_context` to recover session state before continuing.
```

**For VS Code** (`Code/User/prompts/*.instructions.md` or custom instructions):
```markdown
## Memory
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
- Save proactively after significant work — don't wait to be asked.
- After any compaction or context reset, call `mem_context` to recover session state before continuing.
```

**For Antigravity** (`~/.gemini/GEMINI.md` or `.agent/rules/`):
```markdown
## Memory
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
- Save proactively after significant work — don't wait to be asked.
- After any compaction or context reset, call `mem_context` to recover session state before continuing.
```

**For Cursor** (`.cursor/rules/engram.mdc` or `~/.cursor/rules/engram.mdc`):

The `alwaysApply: true` frontmatter tells Cursor to load this rule in every conversation, regardless of which files are open.

```text
---
alwaysApply: true
---

You have access to Engram persistent memory (mem_save, mem_search, mem_context).
Save proactively after significant work. After context resets, call mem_context to recover state.
```

**For Windsurf** (`.windsurfrules`):
```
You have access to Engram persistent memory (mem_save, mem_search, mem_context).
Save proactively after significant work. After context resets, call mem_context to recover state.
```

This is the **nuclear option** — system prompts survive everything, including compaction.

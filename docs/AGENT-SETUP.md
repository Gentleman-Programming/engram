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

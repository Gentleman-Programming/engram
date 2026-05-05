[← Back to README](../README.md)

# Agent Setup

Engram works with **any MCP-compatible agent**. Pick your agent below.

> Cloud bootstrap automation in agent scripts/plugins is intentionally deferred in this rollout. Use `engram cloud ...` manually for now.
>
> Deferred validation scope for this rollout:
> - Setup/plugin scripts are **not** yet validated as cloud enrollment/login orchestrators.
> - `engram setup ...` installs MCP/plugin integrations only; it does **not** auto-run `engram cloud config/enroll/upgrade`.
> - Cloud onboarding contract remains CLI-first until script-level cloud flows are explicitly implemented.

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

### Project auto-detection (important)

**Do not pass `project` to write tools during normal operation.** Engram auto-detects the project from the server's working directory (cwd) using `.engram/config.json`, git remote URL, repo root name, or directory basename. Agents that include `project` in `mem_save` or similar calls will have that argument ignored unless they are using the explicit ambiguous-project recovery flow below.

To lock write tools to the canonical project for a repo, add `.engram/config.json` at the repo root:

```json
{
  "project_name": "sias-app"
}
```

When present, `project_name` is used for writes from the repo and its subdirectories and overrides lower-confidence cwd/git detection. This is a write lock only: read tools can still use an explicit `project` filter when you need to query another existing project. Empty or invalid `project_name` values fail writes loudly instead of falling back silently.

**Recommended first call:** `mem_current_project` — confirms which project Engram detected before you start writing. Returns `project_source` (how it was detected) and `available_projects` (if cwd is ambiguous).

If a write tool returns `ambiguous_project`, the agent must not guess. This happens when the MCP server is started from a parent directory that contains multiple repositories, for example:

```text
/Users/you/work
├── alan-thegentleman/
├── angular-18-jest-playwright/
└── engram/
```

The first write fails with an error like:

```json
{
  "error_code": "ambiguous_project",
  "available_projects": [
    "alan-thegentleman",
    "angular-18-jest-playwright",
    "engram"
  ]
}
```

Ask the user to choose exactly one value from `available_projects`, then retry only `mem_save` or `mem_save_prompt` with both recovery fields:

```json
{
  "project": "chosen-project-from-available-projects",
  "project_choice_reason": "user_selected_after_ambiguous_project"
}
```

On success, Engram writes to the selected project and reports the recovery source:

```json
{
  "project": "engram",
  "project_source": "user_selected_after_ambiguous_project",
  "project_path": "/Users/you/work/engram"
}
```

### Ambiguous-project recovery rules

This is a narrow rescue path, not a free-form project override:

- Recovery is accepted only after cwd detection failed with `ambiguous_project`.
- `project_choice_reason` must be exactly `user_selected_after_ambiguous_project`.
- `project`, after trimming surrounding whitespace, must exactly match one of the reported `available_projects`.
- Normalized variants and guesses are rejected: if `available_projects` contains `foo--bar`, retry with `foo--bar`, not `foo-bar`.
- Empty or whitespace-only choices are rejected.
- In all non-ambiguous cases, `.engram/config.json`/git/cwd detection remains authoritative and the explicit `project` field is ignored.

Mental model:

```text
mem_save fails with ambiguous_project
        ↓
Engram returns available_projects
        ↓
agent asks the user to choose one exact value
        ↓
agent retries with project + project_choice_reason
        ↓
Engram validates the choice came from ambiguity
        ↓
Engram saves to the selected project
```

Alternatives: `cd` into the target repo before starting the MCP server, or add repo `.engram/config.json`.

**Read tools** (`mem_search`, `mem_context`, `mem_get_observation`, `mem_stats`, `mem_timeline`) accept an optional `project` override validated against the store. Omit it to auto-detect.

---

## OpenCode

> **Prerequisite**: Install the `engram` binary first (via [Homebrew](INSTALLATION.md#homebrew-macos--linux), [Windows binary](INSTALLATION.md#windows), [binary download](INSTALLATION.md#download-binary-all-platforms), or [source](INSTALLATION.md#install-from-source-macos--linux)). The plugin needs it for the MCP server and session tracking.

**Recommended: Full setup with one command** — installs the plugin AND registers the MCP server in `opencode.json` automatically:

```bash
engram setup opencode
```

This does three things:
1. Copies the plugin to `~/.config/opencode/plugins/engram.ts` (session tracking, Memory Protocol, compaction recovery)
2. Adds the `engram` MCP server entry to your `opencode.json` with `--tools=agent` (14 agent-facing tools)
3. Adds `opencode-subagent-statusline` to your `tui.json` or `tui.jsonc` so OpenCode shows sub-agent activity in the sidebar/home footer

The plugin auto-starts the HTTP server if needed for session tracking. If your environment blocks background processes, run it manually:

```bash
engram serve &
```

> **Windows**: OpenCode uses `~/.config/opencode/` on Windows too (it does not read `%APPDATA%\opencode\`). `engram setup opencode` writes to `~/.config/opencode/plugins/` and `~/.config/opencode/opencode.json`. To run the server in the background: `Start-Process engram -ArgumentList "serve" -WindowStyle Hidden` (PowerShell) or just run `engram serve` in a separate terminal.

**Alternative: Manual MCP-only setup** (no plugin, all 18 tools by default):

Add to your `opencode.json` (global: `~/.config/opencode/opencode.json` on all platforms, or project-level):

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

During setup, you'll be asked whether to add engram's agent-profile MCP tools to `~/.claude/settings.json` `permissions.allow`. The setup writes entries for both the durable user-level MCP server id (`mcp__engram__...`) and the plugin-scoped server id used by older Claude Code plugin installs, so re-running setup repairs stale or incomplete allowlists without adding startup delay.

**Option C: Bare MCP** — all 18 tools by default, no session management:

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

> **Windows note:** The Claude Code plugin hooks use bash scripts. On Windows, Claude Code runs hooks through Git Bash (bundled with [Git for Windows](https://gitforwindows.org/)) or WSL. The `UserPromptSubmit` hook automatically switches to a fork-light safe path under Git Bash/MSYS2: the first-prompt ToolSearch still runs, while later save-reminder checks are skipped so prompt submission does not block. If Git Bash itself is blocked by Defender/EDR, the plugin also ships `scripts/user-prompt-submit.ps1` as a native PowerShell fallback for local override/testing. **Option C (Bare MCP)** remains the no-hook fallback and works natively on Windows without any shell dependency.

PowerShell fallback test and local override example:

```powershell
'{"session_id":"edr/test:1"}' | pwsh -NoProfile -ExecutionPolicy Bypass -File "C:\path\to\engram\plugin\claude-code\scripts\user-prompt-submit.ps1"
```

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "pwsh -NoProfile -ExecutionPolicy Bypass -File \"C:\\path\\to\\engram\\plugin\\claude-code\\scripts\\user-prompt-submit.ps1\"",
            "timeout": 2
          }
        ]
      }
    ]
  }
}
```

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

---

## Conflict Surfacing (automatic)

When you save a memory with `mem_save`, Engram automatically scans for similar existing observations using FTS5 full-text search. If any candidates are found above a relevance threshold, the response includes a `candidates[]` array and `judgment_required: true`. Nothing to configure — this runs on every save.

### What the agent sees

`mem_save` returns an enriched envelope when candidates exist:

```json
{
  "result": "Memory saved: \"...\"\nCONFLICT REVIEW PENDING — 2 candidate(s); use mem_judge to record verdicts.",
  "id": 42,
  "sync_id": "obs_abc123",
  "judgment_required": true,
  "judgment_status": "pending",
  "judgment_id": "rel-<hex>",
  "candidates": [
    {
      "id": 18,
      "sync_id": "obs_xyz789",
      "title": "We use sessions for auth",
      "type": "decision",
      "score": -3.14,
      "judgment_id": "rel-<hex-for-this-pair>"
    }
  ]
}
```

When no candidates are found, `judgment_required` is `false` and no `candidates` field is present. The `result` string is unchanged.

### How the agent resolves conflicts

The agent iterates `candidates[]` and calls `mem_judge` once per entry, using that entry's own `judgment_id`. The agent does NOT use the top-level `judgment_id` for multiple candidates — each candidate has its own.

The agent's built-in heuristic (from `serverInstructions`) decides when to ask the user versus resolve autonomously:

- **Ask the user** when confidence is below 0.7, OR when the chosen relation is `supersedes` or `conflicts_with` AND the observation type is `architecture`, `policy`, or `decision`.
- **Resolve silently** when confidence >= 0.7 AND the relation is `related`, `compatible`, `scoped`, or `not_conflict`.

When asking, the agent raises it naturally in the conversation — not as a blocking CLI prompt or dashboard action.

### How the user sees this

The user sees it in the normal conversation flow. Example:

> "I noticed memory #18 ('We use sessions for auth') might conflict with what we just saved. Want me to mark the new one as superseding it, or are they about different scopes? I can also mark them as compatible if both still apply."

There is no separate dashboard or conflict list in Phase 1.

### What happens after judgment

Once the agent calls `mem_judge` with a verdict:
- The relation row is persisted with `judgment_status: "judged"` and the chosen `relation`.
- If the relation is `supersedes`, future `mem_search` results show `supersedes: #<id> (<title>)` and `superseded_by: #<id> (<title>)` annotations on the affected observations, including the related memory's title.
- If the relation is `conflicts_with`, future `mem_search` results show `conflicts: #<id> (<title>)` on both observations.
- If the relation is `compatible`, `related`, `scoped`, or `not_conflict`, the judgment is stored in `memory_relations` but no annotation appears in search results.

**Cloud sync**: when the project is enrolled in Engram Cloud and autosync is enabled, `mem_judge` verdicts propagate to other machines via the standard mutation push/pull cycle. The annotation appears in `mem_search` results on any machine that has pulled the relevant mutations. Relations that reference an observation not yet present locally are deferred and retried automatically on subsequent pull cycles — the verdict is never lost.

Nothing breaks if `mem_judge` is never called — pending relations accumulate unjudged but do not affect other operations.

### Proactive semantic comparison (mem_compare)

Agents can also proactively judge the relationship between any two memories using `mem_compare` (also available in the agent profile). Unlike `mem_judge`, which resolves a candidate surfaced by `mem_save`, `mem_compare` lets the agent compare any two observation IDs it has already read, and persist a verdict directly. This is useful for agent-initiated semantic audit workflows.

See [Plugins → mem_compare reference](PLUGINS.md#mcp-tool-reference--mem_compare) for parameters and behavior.

---

## Cloud Autosync toggle

`engram serve` and `engram mcp` support continuous background replication to an Engram Cloud server. This is **opt-in** and never fatal on missing config.

### Prerequisites

1. A running Engram Cloud server (see `docker-compose.cloud.yml` or `engram cloud serve`). The server must be a build that includes the mutation endpoints (`POST /sync/mutations/push`, `GET /sync/mutations/pull`). If the server is older, autosync enters `PhaseBackoff` with `reason_code: transport_failed` and logs `server_unsupported` to stderr.

2. A valid bearer token configured on the server.

### Enable autosync

```sh
export ENGRAM_CLOUD_AUTOSYNC=1          # exact "1" only
export ENGRAM_CLOUD_TOKEN=your-token    # bearer token
export ENGRAM_CLOUD_SERVER=https://cloud.engram.example.com

engram serve
# or
engram mcp
```

The process logs `[autosync] started (server=...)` on success. Missing token or server URL logs `[autosync] ERROR: ...` and the process starts normally without autosync.
For `engram mcp`, autosync runs for the lifetime of the stdio MCP process and is stopped when that process exits.

---

## Cloud dashboard (templ contributors)

If you are contributing to the cloud dashboard (`internal/cloud/dashboard/`), the HTML components are rendered via [templ](https://templ.guide/). Before committing changes to any `.templ` file, regenerate the Go output:

```sh
# Download pinned version (first time only)
go mod download

# Regenerate
make templ
# or directly:
go tool templ generate ./internal/cloud/dashboard/...
```

Commit the regenerated `components_templ.go`, `layout_templ.go`, and `login_templ.go` alongside your `.templ` source changes. CI will fail if they are missing or outdated (`TestTemplGeneratedFilesAreCheckedIn`).

[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Dashboard](dashboard.md) | [Next: Maintainer Playbook →](maintainer-playbook.md)

# Integrations

**Agent integrations should be thin adapters around Engram's Go behavior.** Setup and plugin code may connect agents to MCP, sessions, hooks, and local API, but core memory semantics belong in Go.

## Integration map

Engram supports agents through MCP and through plugins that add lifecycle/session management.

```text
Agent
  │
  ├── Bare MCP
  │     └── engram mcp --tools=agent
  │
  ├── OpenCode plugin
  │     ├── plugin/opencode/engram.ts
  │     └── internal/setup setup opencode
  │
  ├── Claude Code plugin
  │     ├── plugin/claude-code/.claude-plugin/plugin.json
  │     ├── plugin/claude-code/hooks/hooks.json
  │     ├── plugin/claude-code/scripts/*.sh / *.ps1
  │     └── plugin/claude-code/skills/memory/SKILL.md
  │
  └── Agent registry (declarative setup)
        ├── internal/setup/registry.go   (generic injectMCP / writeInstruction driver)
        └── internal/setup/agents.go     (one data entry per agent)
```

`internal/setup` exposes a data-driven agent registry (`agentAdapters()` in
`agents.go`). `Install()` and `SupportedAgents()` are derived from it. Each entry
is either:

- **bespoke** — a `custom` installer for agents with special needs (OpenCode embeds
  a TS plugin, Pi runs a package manager, Claude Code drives the `claude` CLI, Codex
  writes TOML, Gemini CLI cleans up legacy env state), or
- **declarative** — just an MCP path + format (`mcpServers` / `servers` / OpenCode's
  `mcp` object) and instruction surfaces; the generic `injectMCP` / `writeInstruction`
  driver in `registry.go` does the writes. Antigravity CLI, Windsurf, Qwen, Kiro,
  Cursor, VS Code Copilot, Kilo Code, Kimi Code, and CommandCode are all declarative.

Adding a declarative agent is normally just a new entry in `agentAdapters()` plus
its path helpers — no new install code path. Agents not in the registry remain
manual MCP configs documented in `docs/AGENT-SETUP.md`.

## Thin plugin principle

Plugins may:

- start or find `engram serve`,
- create sessions,
- import chunks,
- inject the Memory Protocol,
- persist summaries on compaction,
- strip private tags,
- register MCP.

Plugins **should not** implement core memory semantics. If there is a dedupe, prompt capture, relation judgment, or project resolution rule, it must be in Go.

For per-agent details, use [docs/AGENT-SETUP.md](../AGENT-SETUP.md) and [docs/PLUGINS.md](../PLUGINS.md).

## Runtime session identity

Adapters translate host runtime identity into an Engram session; they do not own
persistence or decide which session an omitted ID should select.

| Adapter | Runtime-to-Engram mapping | Registration before identity use |
| --- | --- | --- |
| Claude Code | SessionStart posts the host `session_id` with its resolved project and directory. | SessionStart alone does not confirm registration. Before binding a classified Engram write/session tool, PreToolUse confirms project-owned registration for the host ID and cwd with HTTP 201, matching `id`, and `status: "created"`; otherwise it denies the call. |
| Codex | SessionStart posts the host `session_id` with its resolved project and directory; its helper hands the same opaque ID to the model only after matching HTTP 201, `id`, and `status: "created"`. Failed confirmation withholds the active memory-tool protocol on startup, resume, clear, and post-compaction while preserving the warning and fetched context; independent CLI/manual saves are not a session-attribution fallback. | Prompt capture requires confirmed registration of the host session before posting; before allowing and binding a classified Engram write/session tool, PreToolUse re-confirms the host ID and cwd using shared session ownership, and failed or ended registration denies the call. |
| OpenCode | The plugin follows authoritative `parentID` links to the root host session; child sessions do not own top-level Engram sessions. | Before session-attributed tool writes it attempts root registration, rechecks ownership, and injects the root `session_id` into tool arguments. Injection requires an HTTP-success response with the exact root `id` and `status: "created"`; missing or mismatched acknowledgements block injection. |

Parent/root translation is specific to OpenCode's session hierarchy, not a
universal adapter rule. Hook-level binding cannot protect calls when a hook is
skipped, bypassed, or timed out; independent direct/manual MCP saves remain
outside these adapters. Go owns the HTTP session registration and persistence
contract ([Sessions](../../DOCS.md#sessions)), project/session validation and
omitted-ID cardinality ([Write tools: explicit/session/cwd project resolution](../../DOCS.md#write-tools-explicitsessioncwd-project-resolution)).

## Setup boundary

`internal/setup` owns idempotent installation of implemented agent integrations. It should not promise automatic cloud bootstrap or login if that flow is still CLI-first and documented elsewhere.

## Plugins/setup change checklist

- [ ] The plugin remains a thin adapter.
- [ ] Core behavior lives in Go, not duplicated shell/TypeScript.
- [ ] Setup is idempotent.
- [ ] Windows/macOS/Linux or documented paths remain correct.
- [ ] `docs/AGENT-SETUP.md` and `docs/PLUGINS.md` reflect the exact current flow.
- [ ] Do not promise automatic cloud bootstrap if it is still CLI-first.

---

[← Previous: Dashboard](dashboard.md) | [Next: Maintainer Playbook →](maintainer-playbook.md)

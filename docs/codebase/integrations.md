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
  ├── Gemini / Codex setup
  │     └── internal/setup/setup.go
  │
  └── VS Code / Antigravity / Cursor / Windsurf manual MCP
        └── JSON configuration documented in docs/AGENT-SETUP.md
```

`internal/setup` does not install every possible integration. VS Code, Antigravity, Cursor, and Windsurf are manual MCP configuration paths documented in `docs/AGENT-SETUP.md`.

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

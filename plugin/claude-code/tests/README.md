# Downstream integration test examples

This directory contains test scripts that exercise the Claude Code hook scripts
end-to-end. They're examples of how to verify hook behavior in a downstream
deployment.

## Files

- `example-pretooluse-hook-test.sh` — bash unit test for a `PreToolUse` hook
  that auto-injects `session_id` and `directory` into Engram write-tool calls.
  Demonstrates the integration pattern enabled by the `directory` parameter
  added in this PR. The test exercises:
  - Pass-through cases (non-Engram tools, read tools, stateless tools, explicit
    args)
  - Injection cases (`mem_save`, `mem_save_prompt`, `mem_capture_passive`,
    `mem_update`, `mem_session_summary`, `mem_session_end`, `mem_session_start`)
  - Edge cases (missing claude session_id, scope=personal, preserved fields)

## Why this matters for PR #289

The PR adds a `directory` parameter to write tools so remote/HTTP-mode MCP
servers can resolve project from a client-supplied path instead of the
server's own CWD (REQ-308). This test demonstrates the consumer side: a
PreToolUse hook reads `cwd` from the Claude Code hook input and injects it
as `directory` on every Engram write-tool call. With the server-side change
in this PR, observations land at the correct project.

## Sourced from

These tests come from a live downstream deployment running Engram on a
Synology NAS via mcp-proxy. The server-name regex (`mcp__engram-onprem__`)
matches the deployment's MCP server registration and may differ from the
default upstream `mcp__engram__` prefix.

## Run

```bash
bash plugin/claude-code/tests/example-pretooluse-hook-test.sh
```

Note: the test invokes a hook script at `~/.claude/hooks/engram/pretooluse-project-inject.sh`
that is NOT included here (it's the deployment-specific consumer). To run as-is,
you'd need to write a similar hook. The intent is to show the test pattern, not
to be runnable from this repo alone.

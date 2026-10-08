---
name: engram-plugin-thin
description: >
  Adapter boundary rules for plugin integrations.
  Trigger: Changes in plugin scripts/hooks for Claude, OpenCode, Pi, Gemini, or Codex.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Editing plugin hooks/scripts/adapters
- Adding passive/active memory capture integrations
- Wiring agent-specific setup behavior

---

## Boundary Rules

1. Keep adapters thin: parse input, call API/tool, return.
2. Put complex logic in Go core (`store/server/mcp`).
3. Avoid extra runtime dependencies in plugin scripts.
4. Reuse a shared contract across all supported agents.

---

## Narrow Pi Exception: Observation-Save Acknowledgement Recovery

`plugin/pi` native `mem_save` may resolve only an ambiguous acknowledgement because
the Go server cannot observe that a client lost one. Go remains the sole authority
for the durable ledger, canonical fingerprint, validation, conflicts, tombstones,
retention, and mutation effects.

- Generate one operation UUID before the initial dispatch and freeze the exact
  redacted payload and both destinations.
- On ambiguity, query only authoritative `GET /observations/save-result`.
  A typed `404` with code `observation_save_result_not_found` permits one exact
  replay after the cancellable 250 ms backoff. A positive replay receipt succeeds
  immediately; only an ambiguous replay, including a replay `409` or `410`, gets
  one final GET.
- Keep existing total-deadline and caller-cancellation bounds. A lookup confirms
  success only with a positive ID and `status: "committed"`; a successful initial
  POST retains legacy positive `{id}` acknowledgement compatibility.
- Initial received HTTP errors remain typed and never replay or self-heal.
  Caller cancellation is propagated; unresolved write uncertainty remains unknown,
  never saved.
- This exception grants no other route or tool retry authority, no general retry
  framework, no local business ledger decision, and no telemetry exemption. Timing
  telemetry may not log the operation UUID; it grants no broader logging permission
  or change to existing user-facing unknown-outcome diagnostics that expose it for
  read-only lookup.

---

## Compatibility Checklist

- [ ] Claude Code flow still works
- [ ] OpenCode flow still works
- [ ] Gemini/Codex config paths remain valid
- [ ] Docs reflect real integration behavior

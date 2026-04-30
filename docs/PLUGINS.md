[← Back to README](../README.md)

# Plugins

> Deferred scope note: plugin-level automatic cloud enrollment/login/upgrade orchestration is not part of this rollout yet. Current cloud flows are CLI-driven (`engram cloud ...`).
>
> Validation boundary (current): plugin scripts are validated for memory/session workflows, not as cloud bootstrap orchestrators. Use CLI for cloud config/auth/enrollment/upgrade.

- [OpenCode Plugin](#opencode-plugin)
- [Claude Code Plugin](#claude-code-plugin)
- [Privacy](#privacy)

---

## OpenCode Plugin

For [OpenCode](https://opencode.ai) users, a thin TypeScript plugin adds enhanced session management on top of the MCP tools:

```bash
# Install via engram (recommended — works from Homebrew or binary install)
engram setup opencode

# Or manually: cp plugin/opencode/engram.ts ~/.config/opencode/plugins/
```

The plugin auto-starts the HTTP server if it's not already running — no manual `engram serve` needed.

> **Local model compatibility:** The plugin works with all models, including local ones served via llama.cpp, Ollama, or similar. The Memory Protocol is concatenated into the existing system prompt (not added as a separate system message), so models with strict Jinja templates (Qwen, Mistral/Ministral) work correctly.

### What the Plugin Does

The plugin:
- **Auto-starts** the engram server if not running
- **Auto-imports** git-synced memories from `.engram/manifest.json` if present in the project
- **Creates sessions** on-demand via `ensureSession()` (resilient to restarts/reconnects)
- **Injects the Memory Protocol** into the agent's system prompt via `chat.system.transform` — strict rules for when to save, when to search, and a mandatory session close protocol. The protocol is concatenated into the existing system message (not pushed as a separate one), ensuring compatibility with models that only accept a single system block (Qwen, Mistral/Ministral via llama.cpp, etc.)
- **Injects previous session context** into the compaction prompt
- **Instructs the compressor** to tell the new agent to persist the compacted summary via `mem_session_summary`
- **Strips `<private>` tags** before sending data
- **Enables** `opencode-subagent-statusline` in `tui.json` or `tui.jsonc` during `engram setup opencode`, adding a live sub-agent monitor to OpenCode's sidebar/home footer. To disable it later, remove `"opencode-subagent-statusline"` from the `"plugin"` array in your TUI config and restart OpenCode.

**No raw tool call recording** — the agent handles all memory via `mem_save` and `mem_session_summary`.

### Memory Protocol (injected via system prompt)

The plugin injects a strict protocol into every agent message:

- **WHEN TO SAVE**: Mandatory after bugfixes, decisions, discoveries, config changes, patterns, preferences
- **WHEN TO SEARCH**: Reactive (user says "remember"/"recordar") + proactive (starting work that might overlap past sessions)
- **SESSION CLOSE**: Mandatory `mem_session_summary` before ending — "This is NOT optional. If you skip this, the next session starts blind."
- **AFTER COMPACTION**: Immediately call `mem_context` to recover state

### Three Layers of Memory Resilience

The OpenCode plugin uses a defense-in-depth strategy to ensure memories survive compaction:

| Layer | Mechanism | Survives Compaction? |
|-------|-----------|---------------------|
| **System Prompt** | `MEMORY_INSTRUCTIONS` concatenated into existing system prompt via `chat.system.transform` | Always present |
| **Compaction Hook** | Auto-saves checkpoint + injects context + reminds compressor | Fires during compaction |
| **Agent Config** | "After compaction, call `mem_context`" in agent prompt | Always present |

---

## Claude Code Plugin

For [Claude Code](https://docs.anthropic.com/en/docs/claude-code) users, a plugin adds enhanced session management using Claude's native hook and skill system:

```bash
# Install via Claude Code marketplace (recommended)
claude plugin marketplace add Gentleman-Programming/engram
claude plugin install engram

# Or via engram binary (works from Homebrew or binary install)
engram setup claude-code

# Or for local development/testing from the repo
claude --plugin-dir ./plugin/claude-code
```

### What the Plugin Provides (vs bare MCP)

| Feature | Bare MCP | Plugin |
|---------|----------|--------|
| MCP tools available | 18 default (`engram mcp`) | 14 agent-profile tools (`engram mcp --tools=agent`) |
| Session tracking (auto-start) | ✗ | ✓ |
| Auto-import git-synced memories | ✗ | ✓ |
| Compaction recovery | ✗ | ✓ |
| Memory Protocol skill | ✗ | ✓ |
| Previous session context injection | ✗ | ✓ |

### Plugin Structure

```
plugin/claude-code/
├── .claude-plugin/plugin.json     # Plugin manifest
├── .mcp.json                      # Registers engram MCP server
├── hooks/hooks.json               # SessionStart + SubagentStop + Stop lifecycle hooks
├── scripts/
│   ├── session-start.sh           # Ensures server, creates session, imports chunks, injects context
│   ├── post-compaction.sh         # Injects previous context + recovery instructions
│   ├── user-prompt-submit.sh      # Loads MCP tools on first prompt; Windows Git Bash safe mode
│   ├── user-prompt-submit.ps1     # Optional Windows-native fallback for locked-down endpoints
│   ├── subagent-stop.sh           # Passive capture trigger on subagent completion
│   └── session-stop.sh            # Logs end-of-session event
└── skills/memory/SKILL.md         # Memory Protocol (when to save, search, close, recover)
```

### How It Works

**On session start** (`startup`):
1. Ensures the engram HTTP server is running
2. Creates a new session via the API
3. Auto-imports git-synced chunks from `.engram/manifest.json` (if present)
4. Injects previous session context into Claude's initial context

**On compaction** (`compact`):
1. Injects the previous session context + compacted summary
2. Tells the agent: "FIRST ACTION REQUIRED — call `mem_session_summary` with this content before doing anything else"
3. This ensures no work is lost when context is compressed

**On user prompt submit**:
1. The first prompt injects a ToolSearch instruction so Claude Code loads Engram MCP tools before responding.
2. Later prompts may inject a save reminder if the local Engram API is fast and available.
3. On Windows Git Bash/MSYS2, the hook uses a bash-builtin-only safe path to avoid fork-heavy helpers (`jq`, `git`, `curl`, `date`). In that mode first-prompt ToolSearch still works, but later save reminders degrade to `{}` so prompt submission stays fast.

If Git Bash itself is blocked by enterprise security tooling, `scripts/user-prompt-submit.ps1` is provided as a native PowerShell fallback for manual hook testing or local override.

PowerShell local override/testing example for locked-down Windows endpoints:

```powershell
# Test the native fallback directly. First run emits ToolSearch; second run emits {}.
'{"session_id":"edr/test:1"}' | pwsh -NoProfile -ExecutionPolicy Bypass -File "C:\path\to\engram\plugin\claude-code\scripts\user-prompt-submit.ps1"

# Local Claude Code override in .claude/settings.json or user settings:
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

**Memory Protocol skill** (always available):
- Strict rules for **when to save** (mandatory after bugfixes, decisions, discoveries)
- **When to search** memory (reactive + proactive)
- **Session close protocol** — mandatory `mem_session_summary` before ending
- **After compaction** — 3-step recovery: persist summary → load context → continue

---

## MCP Tool Reference — mem_judge

`mem_judge` is available in the `agent` profile (`engram mcp --tools=agent`). It is NOT exposed in the `admin` profile.

### Purpose

Records a verdict on a pending memory conflict surfaced by `mem_save`. When `mem_save` returns `judgment_required: true`, the agent iterates `candidates[]` and calls `mem_judge` once per entry.

### Parameters

| Parameter | Required | Type | Description |
|-----------|----------|------|-------------|
| `judgment_id` | yes | string | From `candidates[].judgment_id` in the `mem_save` response. Format: `rel-<hex>`. |
| `relation` | yes | string | One of: `related`, `compatible`, `scoped`, `conflicts_with`, `supersedes`, `not_conflict` |
| `reason` | no | string | Free-text explanation of the verdict |
| `evidence` | no | string | Supporting evidence (JSON or free text; e.g., user's exact words) |
| `confidence` | no | float | 0.0..1.0 — default 1.0; clamped to range |
| `session_id` | no | string | Session ID for provenance (auto-detected if omitted) |

### Behavior

On success, `mem_judge`:
- Flips `judgment_status` from `pending` to `judged` on the matching `memory_relations` row
- Persists `relation`, `reason`, `evidence`, `confidence`, actor provenance (`actor="agent"`, `marked_by_kind="agent"`), and `session_id`
- Returns the updated relation row as JSON

On error (unknown `judgment_id` or invalid `relation`), returns `IsError: true`. The relation row is NOT mutated on error.

Re-judging an already-judged `judgment_id` overwrites the verdict (deliberate revision is allowed).

### Search annotation behavior (observed)

After a verdict is recorded, `mem_search` annotations surface as follows:

| Relation verdict | Annotation in `mem_search` results |
|-----------------|-----------------------------------|
| `supersedes` | `supersedes: #<id> (<title>)` on the source observation; `superseded_by: #<id> (<title>)` on the target |
| `supersedes` (target deleted) | `supersedes: #<id> (deleted)` — falls back to `(deleted)` when the related observation is missing |
| `conflicts_with` (judged) | `conflicts: #<id> (<title>)` on both observations — one line per conflict |
| `pending` (not yet judged) | `conflict: contested by #<id> (pending)` on both observations |
| `compatible`, `related`, `scoped`, `not_conflict` | No annotation line. Judgment is stored but not surfaced. |

### Multi-actor sync_id namespace

Multiple agents can independently analyze the same pair of observations and each produce a distinct `memory_relations` row — even if they refer to the same `(source_id, target_id)` pair. Each row receives its own unique `sync_id`, so there is **no uniqueness constraint** on `(source_id, target_id)`.

Consequences for annotation parsers:

- `mem_search` may return **duplicate annotation lines** with the `conflicts:` prefix for the same `(source, target)` pair — one line per relation row. This is intentional, not a bug.
- Each line represents a distinct verdict from a distinct actor.
- The provenance fields `marked_by_actor`, `marked_by_kind`, and `marked_by_model` on the `memory_relations` row identify which agent produced each verdict.
- When displaying or resolving conflicts, parsers MUST treat each `conflicts:` line as a separate entry keyed on the relation's `sync_id`, not on the observation pair.

Example: two agents both flag observations `#10` and `#20` as conflicting. The `mem_search` result for observation `#10` may include:

```
conflicts: #20 (Some title)
conflicts: #20 (Some title)
```

Both lines are valid. Match by prefix and process all entries — do not deduplicate based on target ID alone.

### Annotation format contract (REQ-012)

The annotation format is a stable, versioned contract. Agent parsers use prefix-based matching — these prefixes will not change in Phase 3:

```
supersedes: #<id> (<title>)
superseded_by: #<id> (<title>)
conflicts: #<id> (<title>)
conflict: contested by #<id> (pending)
```

Multiple entries appear on separate lines (one per related observation), in query-return order. The `<title>` is retrieved via JOIN at search time (no N+1 queries). When the related observation has been deleted, `(deleted)` replaces the title.

> **Parser note**: match by prefix (`supersedes:`, `superseded_by:`, `conflicts:`, `conflict:`). The format `#<integer-id> (<title>)` within parentheses is stable. Do not attempt to parse the title itself — it may contain any characters.

### Cloud sync for judgments

When a project is enrolled in Engram Cloud and autosync is enabled, `mem_judge` verdicts sync across machines. The `memory_relations` table propagates via the standard mutation push/pull cycle — the same pipeline used for observations and sessions. Judgments appear in `mem_search` annotations on any machine that has pulled the relevant mutations.

Relations where the referenced observation does not yet exist locally are deferred (see `sync_apply_deferred`) and retried automatically on subsequent pull cycles.

### mem_save envelope fields (conflict surfacing)

When `mem_save` detects candidates, the JSON response includes:

| Field | Type | Description |
|-------|------|-------------|
| `judgment_required` | bool | `true` when candidates were found; `false` otherwise |
| `judgment_status` | string | `"pending"` (only present when `judgment_required: true`) |
| `judgment_id` | string | Convenience: the first candidate's `judgment_id` (use `candidates[].judgment_id` for multi-candidate loops) |
| `candidates` | array | Each entry has `id`, `sync_id`, `title`, `type`, `score`, `judgment_id`, and optionally `topic_key` |
| `id` | int | Internal ID of the just-saved observation |
| `sync_id` | string | Stable sync ID of the just-saved observation |

Old clients that read only the `result` string continue to work — these fields are additive.

---

## Admin Observability (conflict layer)

Phase 3 adds an admin-facing observability layer over the conflict/relation system. This is NOT for end users — end users continue to interact with conflicts via the normal agent conversation flow (Phase 1). The tools below are for operators and maintainers who need to inspect or audit the `memory_relations` and `sync_apply_deferred` tables directly.

### engram conflicts CLI

The `engram conflicts <sub-command>` command provides read and scan access to the conflict layer from the terminal. It is intended for maintainers, not for agents or end users.

| Sub-command | What it does |
|-------------|-------------|
| `engram conflicts list` | List `memory_relations` rows with optional `--project`, `--status`, `--since`, `--limit` filters |
| `engram conflicts show <id>` | Show full detail for one relation row (source/target observation snippets) |
| `engram conflicts stats` | Aggregate counts grouped by relation type and judgment status; includes deferred and dead queue sizes |
| `engram conflicts scan` | Walk observations for a project, find conflict candidates, and (with `--apply`) insert new pending relation rows up to a `--max-insert` cap |
| `engram conflicts deferred` | Inspect and replay rows in `sync_apply_deferred`; supports `--status`, `--inspect <sync_id>`, and `--replay` |

When `--project` is omitted, the command falls back to the cwd-detected project (same resolution as all other `engram` commands).

`engram conflicts scan` also supports `--semantic` for LLM-judge semantic detection beyond FTS5 lexical candidates. This catches vocabulary-different concepts that share no keywords (e.g., "Hexagonal Architecture" vs "Ports and Adapters"). Set `ENGRAM_AGENT_CLI=claude` or `ENGRAM_AGENT_CLI=opencode` before running. Additional flags: `--concurrency N` (default 5), `--timeout-per-call N` seconds (default 60), `--max-semantic N` (default 100), `--yes` (skip confirmation).

> **Subscription note**: `--semantic` uses your existing agent CLI quota (Claude Pro/Max, OpenCode subscription). Engram itself adds no extra cost — you pay only what your LLM provider charges for the prompts.

For the full HTTP API reference and CLI flag details, see [DOCS.md](../DOCS.md).

### HTTP endpoints

All six `/conflicts/*` endpoints are served by `engram serve` on the local runtime (`127.0.0.1:7437`). They are not exposed on the cloud runtime. Full request/response documentation is in [DOCS.md](../DOCS.md).

| Route | Purpose |
|-------|---------|
| `GET /conflicts` | Paginated list of relation rows |
| `GET /conflicts/{relation_id}` | Single relation detail |
| `GET /conflicts/stats` | Aggregate counts |
| `POST /conflicts/scan` | Run scan (dry-run or apply) |
| `GET /conflicts/deferred` | List deferred queue |
| `POST /conflicts/deferred/replay` | Trigger ReplayDeferred cycle |

---

## MCP Tool Reference — mem_compare

`mem_compare` is available in the `agent` profile (`engram mcp --tools=agent`). It is NOT exposed in the `admin` profile.

### Purpose

Records a verdict on a semantic comparison between two memories. The agent reads both memories, judges their relationship using its LLM reasoning, and calls `mem_compare` to persist the verdict. Unlike `mem_judge` (which resolves a pre-existing `pending` candidate surfaced by `mem_save`), `mem_compare` creates a new relation row directly — useful for proactive analysis that goes beyond FTS5 lexical matching.

### Parameters

| Parameter | Required | Type | Description |
|-----------|----------|------|-------------|
| `memory_id_a` | yes | int | Observation ID of the first memory |
| `memory_id_b` | yes | int | Observation ID of the second memory |
| `relation` | yes | string | One of: `conflicts_with` | `supersedes` | `scoped` | `related` | `compatible` | `not_conflict` |
| `confidence` | yes | float | 0.0..1.0 |
| `reasoning` | yes | string | Explanation of the verdict (max 200 chars) |
| `model` | no | string | Model name for provenance (e.g. `"claude-haiku-4-5"`) |

### Behavior

On success, `mem_compare`:
- Persists a relation row with system provenance (`marked_by_kind="system"`, `marked_by_actor="engram"`)
- Is idempotent: the same `(source_id, target_id)` pair updates the existing row rather than inserting a duplicate
- Returns `{"sync_id": "<rel-hex>"}` on a persisted verdict

`not_conflict` verdicts are no-ops — the call succeeds and returns `{"sync_id": ""}` but no row is written, matching the scan flow contract.

Cross-project relations (where `memory_id_a` and `memory_id_b` belong to different projects) are rejected with an error.

### When to call mem_compare

`mem_compare` is intended for agent-initiated semantic audit workflows, not for routine memory saves. Typical usage:

```
# Agent reads two memories, judges their relation, calls mem_compare
mem_compare(memory_id_a=18, memory_id_b=42, relation="supersedes",
            confidence=0.85, reasoning="New arch decision replaces the older one",
            model="claude-haiku-4-5")
```

For the conflict surfacing flow triggered by `mem_save` (where candidates are surfaced automatically), use `mem_judge` instead.

---

## Privacy

Wrap sensitive content in `<private>` tags — it gets stripped at TWO levels:

```
Set up API with <private>sk-abc123</private> key
→ Set up API with [REDACTED] key
```

1. **Plugin layer** — stripped before data leaves the process
2. **Store layer** — `stripPrivateTags()` in Go before any DB write

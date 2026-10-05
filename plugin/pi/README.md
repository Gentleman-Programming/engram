# Engram for Pi

<p align="center">
  <img width="960" alt="Engram — One Brain. Local or Cloud." src="https://raw.githubusercontent.com/Gentleman-Programming/engram/main/assets/branding/engram-banner.png" />
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/gentle-engram"><img alt="npm" src="https://img.shields.io/npm/v/gentle-engram?color=blue" /></a>
  <a href="https://github.com/Gentleman-Programming/engram"><img alt="GitHub stars" src="https://img.shields.io/github/stars/Gentleman-Programming/engram?style=flat&color=yellow" /></a>
  <a href="https://github.com/Gentleman-Programming/engram/graphs/contributors"><img alt="Contributors" src="https://img.shields.io/github/contributors/Gentleman-Programming/engram?color=brightgreen" /></a>
  <a href="https://github.com/Gentleman-Programming/engram/actions"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/Gentleman-Programming/engram/ci.yml?label=CI" /></a>
  <a href="https://github.com/Gentleman-Programming/engram/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/github/license/Gentleman-Programming/engram" /></a>
  <a href="https://www.youtube.com/c/GentlemanProgramming"><img alt="YouTube" src="https://img.shields.io/badge/YouTube-Gentleman%20Programming-red?logo=youtube&logoColor=white" /></a>
</p>

**Give every Pi session the same brain — local by default, cloud when you want it, and searchable across agents.**

Pi is great at doing the work in front of it. The problem is everything around the work: what the agent learned yesterday, which architecture decision was accepted, why a bug was fixed a certain way, what the user prefers, and what should survive when the context window compacts.

Engram is persistent memory for AI coding agents. `gentle-engram` connects Pi to that memory so your agent can save the useful parts of a session and retrieve them later — without stuffing raw tool output back into the prompt.

## Local bridge ownership protocol

Native Engram exposes a session-scoped responder on the local `pi.events` bus after
`session_start`. This is routing context, **not authentication**, and does not itself
suppress any Claude hooks. Consumers must route to the bus for the exact Pi runtime
session; ownership of a parent never establishes ownership of a nested session.

- Request channel: `engram:bridge:claim:v1`
- Request fields: `{ runtimeSessionId: string, nonce: string, operation: "session-register" | "prompt-capture", promptDigest?: string }`
- Reply channel: `engram:bridge:decision:v1`
- Reply fields: the exact three required request fields, the supplied `promptDigest`
  when it is a string, and `status: "owned" | "unknown"`.

Runtime IDs are opaque and compared without trimming. Nonces must be nonblank
strings of at most 256 JavaScript string units; consumers generate a fresh nonce
per query and correlate all three fields. Invalid requests and foreign IDs receive
no reply. Since `emit` does not await listeners, subscribe before emitting and use
a bounded consumer timeout (allow for the responder's 1000 ms decision budget).
No reply is not proof that native Engram is absent.

An eligible `session-register` claim can confirm registration using the existing
native registration path before the provider spawns, joining a pending registration.
Only its validated server acknowledgement permits `owned`; UI readiness never does.

A `prompt-capture` claim **never registers or captures a prompt**. It requires
`promptDigest`: exactly 64 lowercase hexadecimal characters encoding SHA-256 of
UTF-8 bytes of the original prompt after JavaScript `.trim()`. Only a matching
current native turn with a confirmed `/prompts` reply `{ id: <positive safe integer>,
status: "saved" }` permits `owned`. Missing, invalid, or mismatched digests return
`unknown`. The proof stores only the digest and routing identities, not plaintext,
private content, or durable metadata. Every `before_agent_start` clears prior proof
before initialization or early returns; lifecycle changes also invalidate it.
Short prompts (trimmed length <=10) retain the existing native skip policy and
therefore cannot establish prompt ownership. Failed or ambiguous delivery cannot
reuse a previous turn's proof, even for repeated identical prompts.

Digest matching identifies the original input, **not full-fidelity storage**:
native persistence still redacts private tags before truncating to 2000 JavaScript
string units plus an ellipsis when needed. Consumers must account for this policy
when deciding which capture is duplicate; ownership does not assert that private
or truncated content was stored. Failed, unresolved, stale, closing, or timed-out
claims cannot return `owned`. Timeout does not cancel an existing registration or
trigger a retry, and never emits a late positive decision. Unknown retains hooks.

Listeners and pending reply delivery are removed on shutdown **and reload**;
reload still skips persistence shutdown. Each new native session must establish
its own registration. Cross-runtime bridge routing and hook suppression are
consumer responsibilities, not a process-global readiness signal.

## At a glance

| You want                    | Engram gives Pi                                  |
| --------------------------- | ------------------------------------------------ |
| Fewer repeated explanations | Searchable memories from previous sessions       |
| Lower context waste         | Curated saves instead of raw tool-call dumps     |
| Continuity after compaction | Required session summaries and recovery protocol |
| One memory across tools     | Shared MCP-backed memory for Pi and other agents |
| Team/project memory         | Optional Engram Cloud replication and dashboard  |

## The promise

Install it once. Keep coding. Pi remembers.

- **One brain for many agents** — Pi, Claude Code, OpenCode, Gemini CLI, Codex, VS Code/Copilot, Cursor, Windsurf, Antigravity, and any MCP-compatible agent can read/write the same Engram memory.
- **Local-first memory** — a single Go binary writes to SQLite + FTS5 on your machine. No Node service, Python stack, or hosted account required for the core path.
- **Cloud when the team needs it** — Engram Cloud adds opt-in, project-scoped replication, shared access, and a browser dashboard while keeping local SQLite authoritative.
- **Token-efficient by design** — Engram stores curated summaries, decisions, prompts, and session handoffs instead of a noisy firehose of raw tool calls. Agents search first, then fetch only the relevant memory.
- **Compaction survival** — before context resets, the Memory Protocol pushes summaries into Engram so the next session can recover what matters.
- **Simple Pi setup** — install the Pi package, retain the MCP adapter for other servers, run `pi-engram init`, restart Pi.
- **Built by Gentleman Programming** — Engram comes from the Gentleman Programming ecosystem: an open-source engineering community, YouTube channel, and hands-on agentic-coding workflow around real tools instead of toy demos.
- **Real open-source project** — Engram ships docs, releases, beta programs, contributor guidelines, issue templates, CI, and a growing contributor/community workflow around the main repository.

## Built with the community

Engram is not an abandoned side script or a black-box SaaS. It is built in public by **Gentleman Programming** for developers who are already using coding agents seriously.

- **YouTube channel**: tutorials, demos, and product thinking around AI coding workflows — <https://www.youtube.com/c/GentlemanProgramming>
- **Engram + Skills demo**: <https://www.youtube.com/watch?v=UoS_LP-PCG8>
- **Engram Cloud demo**: <https://www.youtube.com/watch?v=JPZkbGgJNUQ>
- **GitHub community**: issues, discussions, beta feedback, contributors, and transparent roadmap work — <https://github.com/Gentleman-Programming/engram>

The goal is simple: make agentic development feel like a real engineering system — memory, specs, skills, cloud sync, review discipline, and community learning all connected.

## Why this is different from “more context”

Context windows are temporary. Engram is memory.

| More context                        | Engram memory                                            |
| ----------------------------------- | -------------------------------------------------------- |
| Helps during the current run        | Helps across sessions, agents, machines, and compactions |
| Often includes raw logs/tool output | Stores curated, searchable knowledge                     |
| Gets summarized away                | Persists in SQLite + FTS5                                |
| Usually tied to one agent           | Works through MCP across agent clients                   |

Engram does not try to make the model read everything. It gives the model a disciplined memory protocol: save important knowledge, search before repeating work, and fetch full details only when needed.

## See the memory

<p align="center">
  <img width="380" alt="Engram TUI dashboard" src="https://raw.githubusercontent.com/Gentleman-Programming/engram/main/assets/tui-dashboard.png" />
  <img width="380" alt="Engram search results" src="https://raw.githubusercontent.com/Gentleman-Programming/engram/main/assets/tui-search.png" />
</p>

Engram includes a terminal UI for browsing sessions, observations, prompts, projects, timelines, and search results. Engram Cloud adds browser visibility for shared project memory.

## Core resume compatibility

`gentle-engram` 0.2.0 negotiates root session resume through `GET /health`:
`capabilities.root_session_resume: true` enables it; explicit false or malformed
capability data disables it. When the capability is absent, a stable core version
of **3.0.0 or newer** (optionally prefixed with `v`) is accepted. The tagged
v3.0.0 server implements core-selected continuation identities; earlier releases
are not inferred compatible. Unknown, malformed, and prerelease versions require
explicit support advertisement.

Each root registration flight retains its first resume-support decision and
rechecks health before every dispatch. If support changes (including unreadable
health after supported health), the retry stops before another POST. Retry once
core health is stable: the earlier registration may have succeeded despite a
lost acknowledgement, but no unacknowledged identity is adopted. Stable support
permits an idempotent retry with the same resume flag.

Without resume support, fresh root sessions and still-active persisted mappings
can register normally without resume. If an ended identity needs continuation,
attributed writes stop with a diagnostic naming the plugin and core versions,
the support requirement, and upgrade guidance. Upgrade the Engram binary and
restart the running server, then retry; updating only the Pi package is not
sufficient. The plugin never invents a continuation ID or adopts a failed
registration. Read-only memory tools do not require resume support. Cross-project
satellite saves still require `isolated_session_registration: true` separately.

## Quick start

```bash
pi install npm:gentle-engram@0.2.0
pi-engram init
```

Go's `engram setup pi` installs the same `0.2.0` pin. Earlier `0.1.16` still registers Engram MCP during `pi-engram init`, so do not use it for native-only setup.

Restart Pi after installation, then ask Pi what it remembers about the current project or call `mem_context`.

`gentle-engram` does not need an MCP extension. Pi 0.99.0 and later ship built-in MCP support that reads `mcp.json` in the Pi agent directory and is managed with `/mcp` or `pi mcp add`. An installed `pi-mcp-adapter` replaces that built-in support, so `pi-engram init` does not add it; an existing adapter entry in `settings.json` is left for you to keep or remove.

## What gets installed

`gentle-engram` connects Pi to Engram through Pi-native tools. Direct MCP is separate and opt-in:

| Path         | Purpose                                                                                                                                |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------- |
| Pi extension | Captures prompts/session events, injects the Memory Protocol, and exposes compact Pi-native `mem_*` tools over the Engram HTTP server. |
| Direct MCP (manual) | Standalone MCP clients may still run `engram mcp`; Pi setup does not register Engram MCP. Other MCP servers use Pi's built-in MCP (`mcp.json`). |

Pi-native `mem_session_end` accepts only the current Pi host session ID; a different, missing, or empty ID is refused before an end request. If a resumed conversation has a distinct persisted session ID, the matching host request ends that effective ID through the same coordination as session shutdown. A raw host-ID end requires locally confirmed registration; an uncertain registration cannot authorize it. A pending resumed ID whose end acknowledgement was lost can be reconciled without another end request only when Engram confirms that exact ID is already ended under the resolved local project. To end an independent/manual session, use a separate direct client rather than supplying its ID to the Pi-native tool.

```text
Pi events/tools -> gentle-engram extension -> ENGRAM_URL / engram serve -> SQLite
Standalone MCP client -> engram mcp -> SQLite (deliberate, separate setup)
```

Pi-native compact tools use the same HTTP server path as event capture, including project detection, diagnostics, passive capture, lifecycle review, conflict-judgment tools such as `mem_current_project`, `mem_doctor`, `mem_capture_passive`, `mem_review`, `mem_judge`, and `mem_compare`, cross-project discovery through `mem_list_projects`, and local pin curation through `mem_pin`/`mem_unpin`. MCP tools remain a separate stdio path, so direct MCP usage still needs an Engram binary even when `ENGRAM_URL` points at a remote HTTP server. Engram MCP is not registered by Pi setup. An existing Pi MCP entry remains active until you manually remove it and restart/reload Pi; native-only agent writes are not guaranteed while it remains.

## Compact memory tool rendering

`mem_context` accepts optional `max_bytes` and `compact` arguments and forwards supplied values to Engram's HTTP `/context` endpoint. Engram core owns the byte limit and context formatting; these options affect the context returned to the model, not Pi's separate compact/collapsed tool chrome. If either argument is omitted, Pi omits that query parameter and preserves the HTTP endpoint's existing behavior.

`gentle-engram` owns the Pi chrome for Engram memory tools by registering compact Pi-native `mem_*` tools in the companion package. When tools such as `mem_search`, `mem_context`, `mem_save`, `mem_session_summary`, `mem_get_observation`, `mem_review`, `mem_judge`, and `mem_doctor` run in Pi, the default collapsed view stays compact:

```text
🧠 search “auth model” …
↳ ✓ 4 results
```

For lifecycle review, `mem_review` keeps the collapsed output explicit without exposing raw tool payloads:

```text
🧠 review list “engram” limit 10 …
↳ ✓ 3 need review

🧠 review mark_reviewed #42 …
↳ ✓ reviewed #42
```

`action=list` shows memories whose local `review_after` timestamp is due. The optional `project` selector filters `list` and scopes `mark_reviewed`; omit it to list due memories across all projects. `action=mark_reviewed` asks Engram core to reset that observation's local review clock according to its memory type. That review reset is local-only today: it updates the local lifecycle metadata but is not treated as a cloud/git sync mutation until the sync wire format carries lifecycle review fields.

Normal memory activity also updates the status bar with short progress/result text such as `🧠 engram · search…` and `🧠 engram · ✓ 4 results`. The extension does not use notifications for normal memory operations.

Background capture failures use warning notifications in the owning Pi UI (interactive or RPC), rather than writing directly over the terminal editor. Print/JSON mode and calls without a UI context retain stderr diagnostics. If UI notification delivery fails, the diagnostic is safely discarded without falling back to terminal output or interrupting capture. Warnings are not injected into the model conversation; repeated session-project conflicts still warn only once per ownership conflict.

When a tool call fails because Engram cannot determine which project to use, the status bar shows an actionable label instead of the generic `error`:

| Status bar label           | Meaning                                                                                               |
| -------------------------- | ----------------------------------------------------------------------------------------------------- |
| `🧠 repos · ambiguous project` | Pi was started from a directory that contains multiple git repos. Run Pi from inside a single repo, or add `.engram/config.json` with `project_name` to the parent directory. |
| `🧠 repos · error`         | A different tool or network error occurred. Expand the tool output in Pi for the full error message.  |

Full tool details remain available by expanding the tool output in Pi. If `gentle-engram` or the Engram server is not installed/running, the compact tool reports an error instead of implying memory is available.

## What Pi can remember

Pi sends eligible non-Engram tool results to Engram for passive scanning after redaction. Only structured learnings recognized by Engram's parser are persisted; raw or general tool output is not saved as an observation.

- Architecture decisions and tradeoffs
- Bug fixes, root causes, and gotchas
- User preferences and project conventions
- Session goals, next steps, and handoff summaries
- Prompt context tied to meaningful saved observations
- Cross-machine/team memory once a project is enrolled in Engram Cloud

## Private blocks

`gentle-engram` redacts explicit private blocks before sending captured prompts, passive observations, or compaction summaries to Engram:

```text
<private>
this should not be persisted verbatim
</private>
```

The persisted payload keeps the surrounding text but replaces the private block with `[REDACTED]`. Redaction is applied recursively to string values in outgoing JSON payloads and to query values in Engram HTTP requests.

This is a lightweight convenience convention, not a full secret-scanning system. Do not rely on it to detect credentials automatically.

## Compaction recovery

When Pi emits a compaction lifecycle event, `gentle-engram` reads the current payload field `compactionEntry.summary` first, then falls back to supported legacy fields when that value is absent or blank. It uses the opaque Pi runtime session identity captured from a fresh lifecycle event; it never accepts a model-supplied session ID for compaction recovery.

Before archiving, the extension requires Engram to acknowledge registration for the effective session identity. Pi registers its runtime ID with `resume: true`; the Go core reuses a live continuation or creates the next `<runtimeID>:resume:N` identity when the root has ended. The extension validates and adopts the acknowledged ID rather than choosing a suffix; every ended row remains closed. Pi session entries retain the mapping for extension reloads, without appending duplicates on each renewal. If the host cannot append and read mapping entries, root registration does not request resume: an ended root is refused instead of creating an untrackable continuation. A valid existing continuation mapping can still be renewed without append support, but adopting any changed ID requires append support—even when fallback returns the runtime root. Root fallback supersedes the old mapping so writes and cleanup use the same identity. For read-only mappings, confirmed shutdown delivery is remembered in-process to avoid repeated `/end`; uncertain delivery remains retryable. Existing `:resume:<uuid>` mappings are registered as-is; when a mapped session has ended, Pi registers the runtime root with `resume: true` again. A fork uses its own runtime conversation ID and cannot inherit the parent's mapping. Unknown registration failures, invalid acknowledgements, and project ownership conflicts prevent attributed writes. An older server's `409 session_already_ended` is reported with that specific cause, without client-side guessing. It checks runtime-identity ambiguity against Pi's host ID, saves a `session_summary` observation with topic key `session/compaction-recovery` under the acknowledged effective ID (including resumed continuations), then requests `/context/compaction?session_id=...` for recovery guidance scoped to that same effective session.
After a second distinct or blank/missing runtime identity, compaction recovery permanently fails closed until the plugin process restarts.

The next turn receives outcome-specific guidance:

- **Confirmed archive:** the summary is already saved; no manual `mem_session_summary` call is needed.
- **Definite archive failure:** the manual `FIRST ACTION REQUIRED` fallback remains available.
- **Timeout or unknown archive outcome:** verify with `mem_search` or `mem_doctor` before retrying so a possibly completed write is not duplicated.
- **Unavailable session, project, or registration:** no attributed archive is attempted; verify the active Engram session and project before saving manually.

Unsupported event shapes fail gracefully and receive the same safe unavailable guidance rather than an attributed write.

## Local, sync, or cloud

Engram can grow with your workflow:

| Mode         | Use it when                                                                                 |
| ------------ | ------------------------------------------------------------------------------------------- |
| Local SQLite | You want fast private memory on one machine.                                                |
| Git sync     | You want portable compressed memory chunks without a hosted service.                        |
| Engram Cloud | You want shared project memory, browser visibility, and replication across machines/agents. |

Cloud is opt-in and project-scoped. Local SQLite remains the source of truth; cloud replicates and makes memory visible when you explicitly enroll a project.

### Importing Git-synced memories in Pi

Pi connects to Engram (starting a local server when needed) and detects the project without importing memories from a checkout's `.engram/manifest.json`. A manifest's presence does not establish that its chunks are new, valid, or appropriate for your local store. If you want to import Git-synced memories, run this command explicitly from the checkout:

```bash
engram sync --import
```

Run it again when new chunks are published and you want to import them. Opening Pi or restarting a session never imports new chunks automatically.

## Requirements

- Pi coding agent with npm package support.
- Engram installed as `engram` on `PATH`, or `ENGRAM_BIN` pointing at the binary.
- For Pi-native `mem_list_projects`, a running Engram core server v2.1.0 or later, which provides HTTP `GET /projects`. If `/health` returns 200 but this tool gets a 404, upgrade and restart the server; updating `gentle-engram` alone does not add the route.
- No MCP extension: Pi-native `mem_*` tools come from `gentle-engram`. Pi 0.99.0 and later provide built-in MCP for other servers.

If you only want HTTP session capture against an already running Engram server, set `ENGRAM_URL` and the extension will not auto-start a local `engram serve` process.

Background session registration reports exhausted timeout or unknown transport outcomes with safe-retry guidance, separately from invalid acknowledgement identities. Both failures block passive observations until registration is confirmed.

When `ENGRAM_URL` is unset, a confirmed local server that later refuses connections gets one bounded restart attempt per initialized runtime. Pi gives ordinary reads a bounded 10-second retry policy and `mem_doctor` a bounded 15-second retry policy. Session registration uses a separate bounded 5-second replay policy because Engram core implements that route as idempotent. Other writes are sent once with a short deadline: if their transport outcome is ambiguous, Pi reports it as **unknown** and tells you to verify before retrying rather than risking a duplicate mutation. Caller cancellation is propagated, not reported as a transport failure.

A local `/health` response without `instance_id` is treated as legacy only when it reports a recognized version older than `2.0.0-rc.11`, the release that introduced instance identity. Current, unknown, absent, or malformed versions without identity fail closed with identity-verification guidance; Pi does not adopt, terminate, or replace that server automatically.

### Local ownership mismatch

When the local health probe observes a different instance ID, Pi fails closed: it neither adopts the foreign server nor spawns or kills a server. The error includes the expected local ID, observed remote ID, and available CLI/server versions (`unknown` when unavailable). WSL2 shared-loopback can cause a server in another environment to answer the local port, but this is a possible cause, not a diagnosis.

`mem_doctor` returns a structured `LOCAL` error diagnostic with code `ownership_mismatch` and the retained evidence. It uses the initialization failure, without re-probing or sending `/doctor`, project, session, or memory requests to the foreign endpoint. Normal doctor calls still use the server and retain their project/session gates; other startup failures do not use this fallback. Startup retains its bounded retry cadence, so a later call can recheck after you fix the conflict.

Stop the foreign server from its owning environment, or choose a free local port and relaunch:

```bash
ENGRAM_PORT=17437 pi
```

This local-port remedy requires `ENGRAM_URL` to be unset. `ENGRAM_PORT` alone never overrides an explicit `ENGRAM_URL`; an explicit URL opts into an externally managed server and skips local ownership checks.

## Configuration

### Existing Engram server

Use an already running Engram HTTP server:

```bash
ENGRAM_URL=http://127.0.0.1:7437 pi
```

When `ENGRAM_URL` is set, the extension treats the server as externally managed and does not auto-start `engram serve`.

### Custom Engram binary

Use a custom Engram binary for MCP tools and local auto-start:

```bash
ENGRAM_BIN=/path/to/engram pi
```

If the binary is missing, Pi keeps running and memory degrades instead of crashing with `spawn engram ENOENT`.

## Environment variables

The Pi extension treats absent, empty, and whitespace-only `ENGRAM_URL`, `ENGRAM_BIN`, and `ENGRAM_PORT` values as unset. It detects blankness without trimming nonblank explicit values.

| Variable          | Default  | Effect                                                                                                                                                                                                                                                                                  |
| ----------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ENGRAM_URL`      | unset    | Adopt an already running Engram HTTP server (for example `http://127.0.0.1:7437`). When set, the extension skips spawning `engram serve` and skips local instance-identity ownership checks; the server is treated as externally managed.                                                |
| `ENGRAM_BIN`      | `engram` | Binary override. The named executable is resolved from `PATH` and used to auto-start the local server, resolve its instance identity (`instance-id`), and report its version (`version`). Binaries older than v2.0.0-rc.11 cannot resolve an identity and are reported with upgrade guidance. |
| `ENGRAM_PORT`     | `7437`   | Port of the local server the extension spawns and probes when `ENGRAM_URL` is unset.                                                                                                                                                                                                       |
| `ENGRAM_DATA_DIR` | unset    | Data directory inherited by the spawned `engram serve` process. When unset, the server stores memory in `~/.engram` (`%USERPROFILE%\.engram` on Windows).                                                                                                                               |

## Install command details

With this Pi package version, `pi-engram init` updates Pi-owned config in the Pi agent directory:

- `settings.json`: ensures `npm:gentle-engram@0.2.0` is declared, replacing affected `npm:gentle-engram@0.1.8`, `npm:gentle-engram@0.1.11`, `npm:gentle-engram@0.1.12`, `npm:gentle-engram@0.1.14`, `npm:gentle-engram@0.1.15`, and `npm:gentle-engram@0.1.16` pins when present. It does not add `npm:pi-mcp-adapter` and leaves an existing adapter entry untouched.
- `mcp.json`: never created or changed by init. Existing `mcpServers.engram` triggers a warning with its exact config path. Manually remove only that key and restart/reload Pi to guarantee native-only agent writes; preserve unrelated MCP servers.

`engram setup pi` also auto-pins `npmCommand` in Pi's `settings.json` when [mise](https://mise.jdx.dev/) is detected in `PATH`. It sets `npmCommand` to `["mise", "exec", "node@<version>", "--", "npm"]` so Pi always uses the mise-managed Node version. Existing `npmCommand` values are never overwritten; if mise is not found, this step is a no-op.

Existing `mcpServers.engram` entries are preserved even with `--force`; the flag does not bypass native-only safety guidance. The published `mcp-template.json` is a legacy **Pi MCP configuration shape** for explicit, optional Pi MCP clients; it is not a generic standalone MCP template and neither `pi-engram init` nor `engram setup pi` uses it.

The command respects `PI_CODING_AGENT_DIR`; otherwise it writes to `~/.pi/agent`.

## Project detection

The HTTP event-capture path mirrors Engram's normal project detection order as closely as a Pi adapter can:

1. nearest `.engram/config.json` inside the current git repo
2. git `origin` remote name
3. git root directory name
4. single child git repo name
5. current directory basename

MCP tool calls still use Engram core's canonical project resolver at call time. Pi-native tool calls ask the Engram HTTP server for `/project/current`; if that route is missing on an older running server, the adapter falls back to the nearest local `.engram/config.json` and returns a version-mismatch warning. For critical repos or monorepos, prefer an explicit `.engram/config.json`:

```json
{
  "project_name": "my-project"
}
```

### Cross-project saves

Each Pi runtime session is registered `project_owned` and keeps exactly one owning project: the project detected for Pi's working directory (or, when detection is unresolved, the first explicit project that registers it). Saving into another repository or worktree is explicit only:

- `mem_save`, `mem_save_prompt`, and `mem_session_summary` accept `project: "name"` or `cwd: "/path/inside/other/repo"`.
- `cwd` is resolved through the same server detection as `/project/current`. An ambiguous or unresolved `cwd` fails with the detection hint and available projects instead of guessing. When both `project` and `cwd` are set they must agree (case-insensitive), otherwise the call fails before anything is written.
- When the target differs from the runtime session's owning project, Pi registers a derived satellite session `<runtimeID>@<project>` as `project_owned` under the target project with no directory and writes with it. `cwd` only resolves the target project; newly created satellites have an empty stored directory and are never implicit directory-matched runtime candidates for other MCP agents. The runtime session is never re-registered under the target, so no `session_project_conflict` occurs. The satellite renews its lease on every write, concurrent writes share one registration, and an ended satellite resumes through the normal `<id>:resume:N` continuation. Pi ends its satellites when the runtime session quits; after an extension reload they stay unended and only their local lease lapses.
- Before every satellite registration dispatch (including transport retries and the single bounded recovery replay), Pi requires `GET /health` to advertise `capabilities.isolated_session_registration: true` (literal boolean). Missing, false, or malformed capabilities fail closed with upgrade guidance before any satellite POST; no version floor is guessed. Pi sends `isolated: true`, which makes the server validate the root and selected continuation's directories atomically before lease renewal or ownership repair. A previously persisted nonblank directory returns `409 session_isolation_conflict` without mutation; it is never silently cleared. Same-project runtime registration does not require this capability.
- Same-project writes, and writes without an explicit target, keep using the runtime session exactly as before.

There is no automatic inference: editing files in another repository never changes where memories go. Prompt and passive capture always target the detected project.

## Troubleshooting

| Symptom                                                      | Fix                                                                                                                                                                                                                                                                     |
| ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `mem_*` tools are missing                                    | Install/verify `npm:gentle-engram@0.2.0`, run `pi-engram init`, then restart Pi. Earlier `0.1.16` init still registers Engram MCP; do not use it for native-only setup.                                                                                                 |
| Pi cannot find `engram`                                      | Set `ENGRAM_BIN=/absolute/path/to/engram`.                                                                                                                                                                                                                              |
| Session capture should use another server                    | Set `ENGRAM_URL=http://host:7437`.                                                                                                                                                                                                                                      |
| Pi shows `error MCP: 0/N servers` but `mem_*` works          | That status covers Pi's MCP servers, not Engram's Pi-native HTTP tools. Check `~/.pi/agent/mcp.json` for stale/unreachable servers such as remote OAuth services. |
| Existing Pi `mcpServers.engram` entry                         | Manually remove only that key from the warned `mcp.json` path and restart/reload Pi; setup never replaces user-owned MCP config.                                                                                                                                                                                                                                           |
| `mem_current_project` reports `/project/current` unsupported | Restart or upgrade the running `engram serve`; check `ENGRAM_URL`/`ENGRAM_BIN`. If `.engram/config.json` exists, Pi uses it as a temporary fallback.                                                                                                                    |
| `mem_session_summary` cannot detect a project                | Ask the user which project should receive the summary, then retry `mem_session_summary` with `project: "name"`.                                                                                                                                                         |
| Pi warns that its runtime session belongs to another project | Pi registers runtime sessions as `project_owned`. If Engram already persists that session under another nonblank project, its structured `409 session_project_conflict` response suppresses prompt and passive capture even after Pi restarts. Start a fresh Pi session in the current project. To save into another project from this session, pass `project` or `cwd` explicitly (see [Cross-project saves](#cross-project-saves)). |
| Status bar shows `🧠 repos · ambiguous project`             | Pi was started from a parent directory that contains multiple git repos. Run Pi from inside a single repo, or add `.engram/config.json` with `"project_name": "my-project"` to the ambiguous directory.                                                                 |

## Next steps

- Run `engram tui` to inspect stored memories.
- Use `mem_current_project` to confirm project detection before writing memories.
- Read the main Engram setup guide: <https://github.com/Gentleman-Programming/engram/blob/main/docs/AGENT-SETUP.md>
- Explore Engram Cloud: <https://github.com/Gentleman-Programming/engram/blob/main/docs/engram-cloud/README.md>
- Watch Gentleman Programming on YouTube: <https://www.youtube.com/c/GentlemanProgramming>
- Join the project through issues, discussions, and beta feedback: <https://github.com/Gentleman-Programming/engram>

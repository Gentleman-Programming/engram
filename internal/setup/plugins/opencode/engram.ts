/**
 * Engram — OpenCode plugin adapter
 *
 * Thin layer that connects OpenCode's event system to the Engram Go binary.
 * The Go binary runs as a local HTTP server and handles all persistence.
 *
 * Flow:
 *   OpenCode events → this plugin → HTTP calls → engram serve → SQLite
 *
 * Session resilience:
 *   Uses `ensureSession()` before any DB write. This means sessions are
 *   created on-demand — even if the plugin was loaded after the session
 *   started (restart, reconnect, etc.). The session ID comes from OpenCode's
 *   hooks (input.sessionID) rather than relying on a session.created event.
 */

import type { Plugin } from "@opencode-ai/plugin"

// ─── Configuration ───────────────────────────────────────────────────────────

const ENGRAM_PORT = parseInt(process.env.ENGRAM_PORT ?? "7437")
const ENGRAM_URL = `http://127.0.0.1:${ENGRAM_PORT}`
const ENGRAM_BIN = process.env.ENGRAM_BIN ?? "engram"

// Engram's own MCP tools — don't count these as "tool calls" for session stats
const ENGRAM_TOOLS = new Set([
  "mem_search",
  "mem_save",
  "mem_save_prompt",
  "mem_session_summary",
  "mem_context",
  "mem_stats",
  "mem_timeline",
  "mem_get_observation",
  "mem_session_start",
  "mem_session_end",
])

// ─── Memory Instructions ─────────────────────────────────────────────────────
// These get injected into the agent's context so it knows to call mem_save.

const MEMORY_INSTRUCTIONS = `## Engram Persistent Memory — Protocol

You have access to Engram, a persistent memory system that survives across sessions and compactions.

### WHEN TO SAVE (mandatory — not optional)

Call \`mem_save\` IMMEDIATELY after any of these:
- Bug fix completed
- Architecture or design decision made
- Non-obvious discovery about the codebase
- Configuration change or environment setup
- Pattern established (naming, structure, convention)
- User preference or constraint learned

Format for \`mem_save\`:
- **title**: Verb + what — short, searchable (e.g. "Fixed N+1 query in UserList", "Chose Zustand over Redux")
- **type**: bugfix | decision | architecture | discovery | pattern | config | preference
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it (user request, bug, performance, etc.)
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you (omit if none)

### WHEN TO SEARCH MEMORY

When the user asks to recall something — any variation of "remember", "recall", "what did we do",
"how did we solve", "recordar", "acordate", "qué hicimos", or references to past work:
1. First call \`mem_context\` — checks recent session history (fast, cheap)
2. If not found, call \`mem_search\` with relevant keywords (FTS5 full-text search)
3. If you find a match, use \`mem_get_observation\` for full untruncated content

Also search memory PROACTIVELY when:
- Starting work on something that might have been done before
- The user mentions a topic you have no context on — check if past sessions covered it

### SESSION CLOSE PROTOCOL (mandatory)

Before ending a session or saying "done" / "listo" / "that's it", you MUST:
1. Call \`mem_session_summary\` with this structure:

## Goal
[What we were working on this session]

## Instructions
[User preferences or constraints discovered — skip if none]

## Discoveries
- [Technical findings, gotchas, non-obvious learnings]

## Accomplished
- [Completed items with key details]

## Next Steps
- [What remains to be done — for the next session]

## Relevant Files
- path/to/file — [what it does or what changed]

This is NOT optional. If you skip this, the next session starts blind.

### AFTER COMPACTION

If you see a message about compaction or context reset, or if you see "FIRST ACTION REQUIRED" in your context:
1. IMMEDIATELY call \`mem_session_summary\` with the compacted summary content — this persists what was done before compaction
2. Then call \`mem_context\` to recover any additional context from previous sessions
3. Only THEN continue working

Do not skip step 1. Without it, everything done before compaction is lost from memory.
`

// ─── HTTP Client ─────────────────────────────────────────────────────────────

async function engramFetch(
  path: string,
  opts: { method?: string; body?: any } = {}
): Promise<any> {
  try {
    const res = await fetch(`${ENGRAM_URL}${path}`, {
      method: opts.method ?? "GET",
      headers: opts.body ? { "Content-Type": "application/json" } : undefined,
      body: opts.body ? JSON.stringify(opts.body) : undefined,
    })
    return await res.json()
  } catch {
    // Engram server not running — silently fail
    return null
  }
}

async function isEngramRunning(): Promise<boolean> {
  try {
    const res = await fetch(`${ENGRAM_URL}/health`, {
      signal: AbortSignal.timeout(500),
    })
    return res.ok
  } catch {
    return false
  }
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function extractProjectName(directory: string): string {
  return directory.split("/").pop() ?? "unknown"
}

function truncate(str: string, max: number): string {
  if (!str) return ""
  return str.length > max ? str.slice(0, max) + "..." : str
}

/**
 * Strip <private>...</private> tags before sending to engram.
 * Double safety: the Go binary also strips, but we strip here too
 * so sensitive data never even hits the wire.
 */
function stripPrivateTags(str: string): string {
  if (!str) return ""
  return str.replace(/<private>[\s\S]*?<\/private>/gi, "[REDACTED]").trim()
}

// ─── Plugin Export ───────────────────────────────────────────────────────────

export const Engram: Plugin = async (ctx) => {
  const project = extractProjectName(ctx.directory)

  // Track tool counts per session (in-memory only, not critical)
  const toolCounts = new Map<string, number>()

  // Track which sessions we've already ensured exist in engram
  const knownSessions = new Set<string>()

  /**
   * Ensure a session exists in engram. Idempotent — calls POST /sessions
   * which uses INSERT OR IGNORE. Safe to call multiple times.
   */
  async function ensureSession(sessionId: string): Promise<void> {
    if (!sessionId || knownSessions.has(sessionId)) return
    knownSessions.add(sessionId)
    await engramFetch("/sessions", {
      method: "POST",
      body: {
        id: sessionId,
        project,
        directory: ctx.directory,
      },
    })
  }

  // Try to start engram server if not running
  const running = await isEngramRunning()
  if (!running) {
    try {
      Bun.spawn([ENGRAM_BIN, "serve"], {
        stdout: "ignore",
        stderr: "ignore",
        stdin: "ignore",
      })
      await new Promise((r) => setTimeout(r, 500))
    } catch {
      // Binary not found or can't start — plugin will silently no-op
    }
  }

  // Auto-import: if .engram/manifest.json exists in the project repo,
  // run `engram sync --import` to load any new chunks into the local DB.
  // This is how git-synced memories get loaded when cloning a repo or
  // pulling changes. Each chunk is imported only once (tracked by ID).
  try {
    const manifestFile = `${ctx.directory}/.engram/manifest.json`
    const file = Bun.file(manifestFile)
    if (await file.exists()) {
      Bun.spawn([ENGRAM_BIN, "sync", "--import"], {
        cwd: ctx.directory,
        stdout: "ignore",
        stderr: "ignore",
        stdin: "ignore",
      })
    }
  } catch {
    // Manifest doesn't exist or binary not found — silently skip
  }

  return {
    // ─── Event Listeners ───────────────────────────────────────────

    event: async ({ event }) => {
      // --- Session Created ---
      if (event.type === "session.created") {
        const sessionId = (event.properties as any)?.id
        if (sessionId) {
          await ensureSession(sessionId)
        }
      }

      // --- Session Deleted ---
      if (event.type === "session.deleted") {
        const sessionId = (event.properties as any)?.id
        if (sessionId) {
          toolCounts.delete(sessionId)
          knownSessions.delete(sessionId)
        }
      }

      // --- User Message: capture prompts ---
      if (event.type === "message.updated") {
        const msg = event.properties as any
        if (msg?.role === "user" && msg?.content) {
          // message.updated doesn't give sessionID directly,
          // use the most recently known session
          const sessionId =
            [...knownSessions].pop() ?? "unknown-session"

          const content =
            typeof msg.content === "string"
              ? msg.content
              : JSON.stringify(msg.content)

          // Only capture non-trivial prompts (>10 chars)
          if (content.length > 10) {
            await ensureSession(sessionId)
            await engramFetch("/prompts", {
              method: "POST",
              body: {
                session_id: sessionId,
                content: stripPrivateTags(truncate(content, 2000)),
                project,
              },
            })
          }
        }
      }
    },

    // ─── Tool Execution Hook ─────────────────────────────────────
    // Count tool calls per session (for session end stats).
    // Also ensures the session exists — handles plugin reload / reconnect.
    // No raw observation recording — the agent handles all memory via
    // mem_save and mem_session_summary.

    "tool.execute.after": async (input, _output) => {
      if (ENGRAM_TOOLS.has(input.tool.toLowerCase())) return

      // input.sessionID comes from OpenCode — always available
      const sessionId = input.sessionID
      if (sessionId) {
        await ensureSession(sessionId)
        toolCounts.set(sessionId, (toolCounts.get(sessionId) ?? 0) + 1)
      }
    },

    // ─── System Prompt: Always-on memory instructions ──────────
    // Injects MEMORY_INSTRUCTIONS into the system prompt of every message.
    // This ensures the agent ALWAYS knows about Engram, even after compaction.

    "experimental.chat.system.transform": async (_input, output) => {
      output.system.push(MEMORY_INSTRUCTIONS)
    },

    // ─── Compaction Hook: Persist memory + inject context ──────────
    // Compaction is triggered by the system (not the agent) when context
    // gets too long. The old agent "dies" and a new one starts with the
    // compacted summary. This is our chance to:
    // 1. Auto-save a session checkpoint (the agent can't do this itself)
    // 2. Inject context from previous sessions into the compaction prompt
    // 3. Tell the compressor to remind the new agent to save memories

    "experimental.session.compacting": async (input, output) => {
      if (input.sessionID) {
        await ensureSession(input.sessionID)
      }

      // Inject context from previous sessions
      const data = await engramFetch(
        `/context?project=${encodeURIComponent(project)}`
      )
      if (data?.context) {
        output.context.push(data.context)
      }

      // Tell the compressor to instruct the new agent to persist the
      // compacted summary to Engram. The new agent reads the compacted
      // summary and this instruction, then saves it as a session summary.
      output.context.push(
        `CRITICAL INSTRUCTION FOR COMPACTED SUMMARY:\n` +
        `The agent has access to Engram persistent memory via MCP tools.\n` +
        `You MUST include the following instruction at the TOP of the compacted summary:\n\n` +
        `"FIRST ACTION REQUIRED: Call mem_session_summary with the content of this compacted summary. ` +
        `Use project: '${project}'. This preserves what was accomplished before compaction. Do this BEFORE any other work."\n\n` +
        `This is NOT optional. Without this, everything done before compaction is lost from memory.`
      )
    },
  }
}

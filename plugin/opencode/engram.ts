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

const MEMORY_INSTRUCTIONS = `## Engram Memory System — Instructions

You have access to a persistent memory system (Engram). Use it PROACTIVELY:

### During the session:
After completing significant work (bug fix, architectural decision, new feature, config change),
call \`mem_save\` with a structured summary. Use this format:

- **title**: Short, searchable (e.g. "JWT auth middleware", "Fixed N+1 query")
- **type**: decision | architecture | bugfix | pattern | config | discovery
- **content**: Use this structure:
  **What**: [concise description of what was done]
  **Why**: [reasoning or user request that drove it]
  **Where**: [files/paths affected]
  **Learned**: [gotchas, edge cases, or decisions — omit if none]

### When the session is ending:
Call \`mem_session_summary\` with a comprehensive summary using this format:

## Goal
[What we were building/working on]

## Instructions
[User preferences or constraints discovered — skip if none]

## Discoveries
- [Technical findings, gotchas, non-obvious learnings]

## Accomplished
- ✅ [Completed task — with key details]
- 🔲 [Not yet done — for next session]

## Relevant Files
- path/to/file — [what it does or changed]

DO NOT wait to be asked. Save memories proactively. Future you will thank present you.
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

      // --- Session Idle (completed) ---
      if (event.type === "session.idle") {
        const sessionId = (event.properties as any)?.id
        if (sessionId) {
          const count = toolCounts.get(sessionId) ?? 0
          await engramFetch(`/sessions/${sessionId}/end`, {
            method: "POST",
            body: {
              summary: `Session on ${project} — ${count} tool calls`,
            },
          })
          toolCounts.delete(sessionId)
          knownSessions.delete(sessionId)
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

    // ─── Compaction Hook: Inject memory context + instructions ────

    "experimental.session.compacting": async (input, output) => {
      // Ensure session exists (handles reconnect scenario)
      if (input.sessionID) {
        await ensureSession(input.sessionID)
      }

      // 1. Inject memory instructions so the agent knows to save proactively
      output.context.push(MEMORY_INSTRUCTIONS)

      // 2. Inject context from previous sessions
      const data = await engramFetch(
        `/context?project=${encodeURIComponent(project)}`
      )
      if (data?.context) {
        output.context.push(data.context)
      }
    },
  }
}

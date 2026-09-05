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
 *   Resolves OpenCode's persisted session hierarchy before attributed writes,
 *   then uses `ensureSession()` so plugin reloads and reconnects remain safe
 *   even when no session.created event is replayed.
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
  "mem_update",
  "mem_delete",
  "mem_suggest_topic_key",
  "mem_save_prompt",
  "mem_session_summary",
  "mem_context",
  "mem_stats",
  "mem_timeline",
  "mem_get_observation",
  "mem_session_start",
  "mem_session_end",
  "mem_capture_passive",
])

const SESSION_ATTRIBUTED_WRITE_TOOLS = new Set([
  "mem_save",
  "mem_save_prompt",
  "mem_session_summary",
  "mem_capture_passive",
])

// OpenCode qualifies MCP tool IDs as <server>_<tool>; only normalize Engram's.
function canonicalEngramToolName(tool: string): string {
  const canonical = tool.toLowerCase()
  return canonical.startsWith("engram_") ? canonical.slice("engram_".length) : canonical
}

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
- **scope**: \`project\` (default) | \`personal\` | \`global\`
- **topic_key** (optional, recommended for evolving decisions): stable key like \`architecture/auth-model\`
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it (user request, bug, performance, etc.)
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you (omit if none)

Topic rules:
- Different topics must not overwrite each other (e.g. architecture vs bugfix)
- Reuse the same \`topic_key\` to update an evolving topic instead of creating new observations
- If unsure about the key, call \`mem_suggest_topic_key\` first and then reuse it
- Use \`mem_update\` when you have an exact observation ID to correct

### WHEN TO SEARCH MEMORY

When the user asks to recall something — any variation of "remember", "recall", "what did we do",
"how did we solve", or the equivalent in the user's language, or references to past work:
1. First call \`mem_context\` — checks recent session history (fast, cheap)
2. If not found, call \`mem_search\` with relevant keywords (FTS5 full-text search)
3. If you find a match, use \`mem_get_observation\` for full untruncated content

Also search memory PROACTIVELY when:
- Starting work on something that might have been done before
- The user mentions a topic you have no context on — check if past sessions covered it
- The user's FIRST message references the project, a feature, or a problem — call \`mem_search\` with keywords from their message to check for prior work before responding

### SESSION CLOSE PROTOCOL (mandatory)

Before ending a session or saying "done" / "that's it", you MUST:
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
2. The session-only compaction context has already been injected. Do not automatically call \`mem_context\`, which is project-scoped; use it only when explicitly requested.
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
      signal: AbortSignal.timeout(3000),
    })
    if (!res.ok) return null
    try {
      return await res.json()
    } catch {
      return {}
    }
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

async function resolveProjectName(directory: string): Promise<{ project: string; error?: string }> {
  const data = await engramFetch(`/project/current?cwd=${encodeURIComponent(directory)}`)
  const project = typeof data?.project === "string" ? data.project.trim() : ""
  if (project && project !== "unknown" && !data?.error_hint && !/[\\/]/.test(project)) {
    return { project }
  }
  const choices = Array.isArray(data?.available_projects) && data.available_projects.length > 0
    ? ` Available projects: ${data.available_projects.join(", ")}.`
    : ""
  const reason = typeof data?.error_hint === "string" && data.error_hint.trim()
    ? ` ${data.error_hint.trim()}`
    : ""
  return {
    project: "unknown",
    error: `gentle-engram could not resolve a safe project identity.${reason}${choices} Retry when project resolution is available.`,
  }
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

// SQLite datetime('now') returns "YYYY-MM-DD HH:MM:SS" in UTC with no zone
// suffix; new Date() would parse that as local time. Normalize to UTC first so
// the thresholds are correct in every timezone.
function toEpochSecs(ts: string): number | null {
  if (!ts) return null
  const normalized = ts.replace(" ", "T")
  const utcTimestamp = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(normalized) ? normalized : `${normalized}Z`
  const ms = new Date(utcTimestamp).getTime()
  return Number.isNaN(ms) ? null : Math.floor(ms / 1000)
}

// A successful empty list is the only response that proves a project has never
// saved an observation. Every other incomplete observation response fails closed.
export function shouldNudgeForObservations(
  observationsResponseOK: boolean,
  observations: unknown,
  nowSecs: number,
  sessionStartEpoch: number | null
): boolean {
  if (!observationsResponseOK || !Array.isArray(observations)) return false
  if (observations.length === 0) {
    return sessionStartEpoch !== null && sessionStartEpoch > 0 && nowSecs - sessionStartEpoch >= 900
  }

  const createdAt = observations[0]?.created_at
  if (typeof createdAt !== "string") return false

  const lastObsEpoch = toEpochSecs(createdAt)
  return lastObsEpoch !== null && nowSecs - lastObsEpoch >= 900
}

// ─── Plugin Export ───────────────────────────────────────────────────────────

export const Engram: Plugin = async (ctx) => {
	let project = "unknown"
	let projectResolutionError = ""
	let projectResolutionGeneration = 0

	async function ensureResolvedProject(): Promise<boolean> {
		if (project !== "unknown" && !projectResolutionError) return true
		const generation = ++projectResolutionGeneration
		const resolved = await resolveProjectName(ctx.directory)
		if (generation !== projectResolutionGeneration) return project !== "unknown" && !projectResolutionError
		project = resolved.project
		projectResolutionError = resolved.error ?? ""
		return projectResolutionError === ""
	}

  // Track tool counts per session (in-memory only, not critical)
  const toolCounts = new Map<string, number>()

  // Track last nudge time per session to debounce save reminders
  const lastNudgeTime = new Map<string, number>() // sessionID -> epoch seconds

  // Track which sessions we've already ensured exist in engram
  const knownSessions = new Set<string>()

  // Track child session IDs so we can suppress their tool-hook registrations.
  // OpenCode's parentID is the authoritative ownership signal; titles are not.
  // Children must not register as top-level Engram sessions because that causes
  // session inflation (e.g. 170 sessions for 1 real conversation, issue #116).
  const subAgentSessions = new Set<string>()

  // Authoritative runtime ownership from OpenCode events or SDK lookups.
  // A null parent marks a confirmed root; child sessions never own lifecycle.
  const parentSessions = new Map<string, string | null>()

  // Deleted sessions and descendants remain invalid for this plugin lifetime.
  // This prevents late hooks or events from reviving an expired runtime chain.
  const invalidSessions = new Set<string>()

  // Terminal root closures are retained after invalidation so duplicate deletion
  // events can retry a failed endpoint call without treating it as confirmed.
  const deletedRootSessions = new Set<string>()
  const closedSessions = new Set<string>()
  const closingSessions = new Map<string, Promise<boolean>>()

  function invalidateSessionTree(sessionId: string): void {
    const invalidated = new Set([sessionId])
    let foundDescendant = true
    while (foundDescendant) {
      foundDescendant = false
      for (const [childID, parentID] of parentSessions) {
        if (parentID && invalidated.has(parentID) && !invalidated.has(childID)) {
          invalidated.add(childID)
          foundDescendant = true
        }
      }
    }

    for (const invalidID of invalidated) {
      invalidSessions.add(invalidID)
      knownSessions.delete(invalidID)
      subAgentSessions.delete(invalidID)
      parentSessions.delete(invalidID)
      toolCounts.delete(invalidID)
      lastNudgeTime.delete(invalidID)
    }
  }

  function isKnownAuthoritativeRootSession(sessionId: string): boolean {
    return knownSessions.has(sessionId) && parentSessions.get(sessionId) === null
  }

  async function closeDeletedRootSession(sessionId: string): Promise<boolean> {
    if (closedSessions.has(sessionId)) return true
    if (!deletedRootSessions.has(sessionId)) {
      if (!isKnownAuthoritativeRootSession(sessionId)) return false
      deletedRootSessions.add(sessionId)
    }

    const inFlight = closingSessions.get(sessionId)
    if (inFlight) return inFlight

    const close = engramFetch(`/sessions/${encodeURIComponent(sessionId)}/end`, {
      method: "POST",
    }).then((acknowledgement) => {
      if (acknowledgement === null) return false
      closedSessions.add(sessionId)
      return true
    }).finally(() => {
      closingSessions.delete(sessionId)
    })
    closingSessions.set(sessionId, close)
    return close
  }

  function cacheSessionInfo(info: { id?: unknown; parentID?: unknown; projectID?: unknown } | undefined): boolean {
    const rawSessionID = info?.id
    const sessionId = typeof rawSessionID === "string" && rawSessionID ? rawSessionID : ""
    if (!sessionId) return false
    const rawParentID = info?.parentID
    const parentID = rawParentID === undefined
      ? null
      : typeof rawParentID === "string" && rawParentID
        ? rawParentID
        : undefined
    const rawProjectID = info?.projectID
    const invalidProjectID = (
      typeof rawProjectID !== "string" ||
      !rawProjectID ||
      (ctx.project?.id && rawProjectID !== ctx.project.id)
    )
    if (parentID === undefined || invalidProjectID) {
      parentSessions.delete(sessionId)
      subAgentSessions.delete(sessionId)
      return false
    }
    if (invalidSessions.has(sessionId) || (parentID && invalidSessions.has(parentID))) {
      invalidateSessionTree(sessionId)
      return false
    }
    parentSessions.set(sessionId, parentID)
    return true
  }

  async function resolveAuthoritativeSessionID(sessionId: string): Promise<string> {
    if (!sessionId || invalidSessions.has(sessionId)) return ""
    if (subAgentSessions.has(sessionId) && !parentSessions.has(sessionId)) return ""
    const visited = new Set<string>()
    const resolvedParents = new Map<string, string | null>()
    const publishResolvedParents = (): void => {
      for (const [resolvedID, resolvedParentID] of resolvedParents) {
        if (!parentSessions.has(resolvedID)) {
          parentSessions.set(resolvedID, resolvedParentID)
          if (resolvedParentID) subAgentSessions.add(resolvedID)
        }
      }
    }
    const invalidateResolvedTree = (invalidID: string): void => {
      publishResolvedParents()
      invalidateSessionTree(invalidID)
    }
    let current = sessionId
    while (true) {
      if (visited.has(current)) return ""
      if (invalidSessions.has(current)) {
        invalidateResolvedTree(current)
        return ""
      }
      visited.add(current)

      let parentID: string | null
      if (parentSessions.has(current)) {
        parentID = parentSessions.get(current) ?? null
      } else {
        let result: Awaited<ReturnType<typeof ctx.client.session.get>> | undefined
        try {
          result = await ctx.client.session.get({ path: { id: current } })
        } catch {
          return ""
        }
        if (invalidSessions.has(current)) {
          invalidateResolvedTree(current)
          return ""
        }
        if (parentSessions.has(current)) {
          parentID = parentSessions.get(current) ?? null
        } else {
          const info = result?.data
          const status = result?.response?.status
          if (
            result?.error ||
            (typeof status === "number" && status >= 400) ||
            !info ||
            typeof info.id !== "string" ||
            info.id !== current ||
            typeof info.projectID !== "string" ||
            !info.projectID ||
            (ctx.project?.id && info.projectID !== ctx.project.id) ||
            (info.parentID !== undefined && (typeof info.parentID !== "string" || !info.parentID))
          ) {
            return ""
          }
          parentID = info.parentID ?? null
          resolvedParents.set(current, parentID)
        }
      }

      if (parentID === null) {
        if (subAgentSessions.has(current)) return ""
        for (const [resolvedID, resolvedParentID] of resolvedParents) {
          const invalidID = invalidSessions.has(resolvedID)
            ? resolvedID
            : resolvedParentID && invalidSessions.has(resolvedParentID)
              ? resolvedParentID
              : ""
          if (invalidID) {
            invalidateResolvedTree(invalidID)
            return ""
          }
        }
        publishResolvedParents()
        return current
      }
      current = parentID
    }
  }

  /**
   * Ensure a session exists in engram. Idempotent — calls POST /sessions
   * which uses INSERT OR IGNORE. Safe to call multiple times.
   *
   * Silently skips sub-agent sessions (tracked in `subAgentSessions`).
   */
  async function ensureSession(sessionId: string): Promise<boolean> {
		if (!await ensureResolvedProject()) return false
    if (!sessionId || invalidSessions.has(sessionId)) return false
    if (knownSessions.has(sessionId)) return true
    // Do not register sub-agent sessions in Engram (issue #116).
    if (subAgentSessions.has(sessionId)) return false
    const acknowledgement = await engramFetch("/sessions", {
      method: "POST",
      body: {
        id: sessionId,
        project,
        directory: ctx.directory,
      },
    })
    if (acknowledgement === null || invalidSessions.has(sessionId)) return false
    knownSessions.add(sessionId)
    return true
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

	if (await ensureResolvedProject()) {
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
	}

  return {
    // ─── Event Listeners ───────────────────────────────────────────

    event: async ({ event }) => {
      // --- Session Created / Updated ---
      if (event.type === "session.created" || event.type === "session.updated") {
        // Bug fix (#116): session data is nested under event.properties.info,
        // not event.properties directly.
        const info = (event.properties as any)?.info
        const sessionId = info?.id
        const parentID = info?.parentID

        // Only an authoritative parentID makes this session a child. Titles are
        // descriptive and may legitimately resemble generated sub-agent titles.
        const isSubAgent = !!parentID

        if (!cacheSessionInfo(info)) return
        if (isSubAgent) subAgentSessions.add(sessionId)
        else subAgentSessions.delete(sessionId)

        if (event.type === "session.created" && sessionId && !isSubAgent) {
          await ensureSession(sessionId)
        }
      }

      // --- Session Deleted ---
      if (event.type === "session.deleted") {
        // Same properties.info path as session.created.
        const info = (event.properties as any)?.info
        const sessionId = info?.id
        if (sessionId) {
          // Only a registered, event-validated root owns an Engram lifecycle.
          // Await the best-effort endpoint before invalidating local ownership.
          await closeDeletedRootSession(sessionId)
          invalidateSessionTree(sessionId)
        }
      }

    },

    // ─── User Prompt Capture ──────────────────────────────────────
    // chat.message is called once per user message, before the LLM sees it.
    // input.sessionID is always reliable here (no knownSessions workaround).
    // output.message is typed as UserMessage (role:"user" already guaranteed).
    // output.parts contains TextPart[] with the actual message text.

    "chat.message": async (input, output) => {
      const sessionId = await resolveAuthoritativeSessionID(input.sessionID)
      // Skip child prompts even when ownership was discovered through the SDK.
      if (!sessionId || subAgentSessions.has(input.sessionID)) return

      // Extract text from parts (type:"text")
      const content = output.parts
        .filter((p) => p.type === "text")
        .map((p) => (p as any).text ?? "")
        .join("\n")
        .trim()

      // Also fallback to summary if parts yield nothing
      const fallback = !content && output.message.summary
        ? `${output.message.summary.title ?? ""}\n${output.message.summary.body ?? ""}`.trim()
        : ""

      const finalContent = content || fallback

      // Only capture non-trivial prompts (>10 chars)
      if (finalContent.length > 10) {
        const registered = await ensureSession(sessionId)
        const confirmedSessionID = await resolveAuthoritativeSessionID(input.sessionID)
        if (!registered || confirmedSessionID !== sessionId) return
        await engramFetch("/prompts", {
          method: "POST",
          body: {
            session_id: sessionId,
            content: stripPrivateTags(truncate(finalContent, 2000)),
            project,
          },
        })
      }
    },

    // ─── Tool Execution Hook ─────────────────────────────────────
    // Count tool calls per session (for session end stats).
    // Also ensures the session exists — handles plugin reload / reconnect.
    // Passive capture: when a Task tool completes, POST its output to
    // the passive capture endpoint so the server extracts learnings.

    "tool.execute.before": async (input, output) => {
      if (!SESSION_ATTRIBUTED_WRITE_TOOLS.has(canonicalEngramToolName(input.tool))) return
      const authoritativeSessionID = await resolveAuthoritativeSessionID(input.sessionID)
      if (!authoritativeSessionID) {
        throw new Error(`gentle-engram could not resolve an authoritative OpenCode runtime session for ${input.tool}`)
      }
      const registered = await ensureSession(authoritativeSessionID)
      const confirmedSessionID = await resolveAuthoritativeSessionID(input.sessionID)
      if (confirmedSessionID !== authoritativeSessionID) {
        throw new Error(`gentle-engram could not resolve an authoritative OpenCode runtime session for ${input.tool}`)
      }
      if (!registered) {
			if (projectResolutionError) throw new Error(projectResolutionError)
        throw new Error(`gentle-engram could not confirm Engram session registration for ${input.tool}; verify that the Engram server is available and retry`)
      }
      output.args.session_id = authoritativeSessionID
    },

    "tool.execute.after": async (input, output) => {
      if (ENGRAM_TOOLS.has(canonicalEngramToolName(input.tool))) return

      // input.sessionID comes from OpenCode — always available
      const sessionId = await resolveAuthoritativeSessionID(input.sessionID)
      if (!sessionId) return
      const registered = await ensureSession(sessionId)
      const confirmedSessionID = await resolveAuthoritativeSessionID(input.sessionID)
      if (!registered || confirmedSessionID !== sessionId) return
      toolCounts.set(sessionId, (toolCounts.get(sessionId) ?? 0) + 1)

      // Passive capture: extract learnings from Task tool output
      if (input.tool === "Task" && output) {
        const text = typeof output === "string" ? output : JSON.stringify(output)
        if (text.length > 50) {
          await engramFetch("/observations/passive", {
            method: "POST",
            body: {
              session_id: sessionId,
              content: stripPrivateTags(text),
              project,
              source: "task-complete",
            },
          })
        }
      }
    },

    // ─── System Prompt: Always-on memory instructions ──────────
    // Injects MEMORY_INSTRUCTIONS into the system prompt of every message.
    // This ensures the agent ALWAYS knows about Engram, even after compaction.
    //
    // We append to the last existing system entry instead of pushing a new one.
    // Some models (Qwen3.5, Mistral/Ministral via llama.cpp) reject multiple
    // system messages — their Jinja chat templates only allow a single system
    // block at the beginning. By concatenating, we avoid adding extra system
    // messages that would break these models. See: GitHub issue #23.

    "experimental.chat.system.transform": async (input, output) => {
      if (output.system.length > 0) {
        output.system[output.system.length - 1] += "\n\n" + MEMORY_INSTRUCTIONS
      } else {
        output.system.push(MEMORY_INSTRUCTIONS)
      }

      // ── Save nudge ──────────────────────────────────────────────────────────
      // If it has been a long time since the last mem_save, append a reminder
      // to the system prompt so the agent notices. All fetches are fire-and-
      // forget with short timeouts — any failure silently skips the nudge.
      try {
			if (!await ensureResolvedProject()) return
        const sessionID: string = input.sessionID ?? ""
        if (!sessionID || invalidSessions.has(sessionID) || subAgentSessions.has(sessionID)) return

        const cooldownSecs = parseInt(process.env.ENGRAM_NUDGE_COOLDOWN_SECS ?? "900", 10)
        const nowSecs = Math.floor(Date.now() / 1000)

        // Debounce: skip if we nudged recently this session
        const lastNudge = lastNudgeTime.get(sessionID)
        if (lastNudge !== undefined && nowSecs - lastNudge < cooldownSecs) return

        // Skip if the session is too young (< 5 minutes)
        let sessionStartEpoch: number | null = null
        try {
          const sessionRes = await fetch(`${ENGRAM_URL}/sessions/${encodeURIComponent(sessionID)}`, {
            signal: AbortSignal.timeout(200),
          })
          if (sessionRes.ok) {
            const sessionData = await sessionRes.json()
            const startedAt: string = sessionData?.started_at ?? ""
            if (startedAt) {
              sessionStartEpoch = toEpochSecs(startedAt)
            }
          }
        } catch {
          // Server unreachable or timed out — skip nudge
          return
        }
        if (sessionStartEpoch !== null && sessionStartEpoch > 0 && nowSecs - sessionStartEpoch < 300) return

        // Check when the last observation was saved for this project
        let obsData: unknown
        let observationsResponseOK = false
        try {
          const obsRes = await fetch(
            `${ENGRAM_URL}/observations?project=${encodeURIComponent(project)}&limit=1&sort=created_at:desc`,
            { signal: AbortSignal.timeout(200) }
          )
          if (obsRes.ok) {
            observationsResponseOK = true
            obsData = await obsRes.json()
          }
        } catch {
          // Server unreachable or timed out — skip nudge
          return
        }

        if (!shouldNudgeForObservations(observationsResponseOK, obsData, nowSecs, sessionStartEpoch)) return

        // Append the nudge to the last system message
        const nudge =
          "\n\nMEMORY REMINDER: It's been at least 15 minutes since your last memory save. " +
          "If you've made decisions, discoveries, completed significant work, or found non-obvious things, " +
          "call mem_save now."
        if (output.system.length > 0) {
          output.system[output.system.length - 1] += nudge
        } else {
          output.system.push(nudge)
        }
        lastNudgeTime.set(sessionID, nowSecs)
      } catch {
        // Any unexpected error — silently skip the nudge, never crash the hook
      }
    },

    // ─── Compaction Hook: Persist memory + inject context ──────────
    // Compaction is triggered by the system (not the agent) when context
    // gets too long. The old agent "dies" and a new one starts with the
    // compacted summary. This is our chance to:
    // 1. Auto-save a session checkpoint (the agent can't do this itself)
    // 2. Inject context from previous sessions into the compaction prompt
    // 3. Tell the compressor to remind the new agent to save memories

    "experimental.session.compacting": async (input, output) => {
		if (!await ensureResolvedProject()) {
			output.context.push(`${projectResolutionError} Automatic session, prompt, and passive-capture writes remain disabled.`)
			return
		}
      let sessionId = ""
      if (input.sessionID) {
        sessionId = await resolveAuthoritativeSessionID(input.sessionID)
      }

      // Runtime compaction context must never cross session boundaries. If the
      // authoritative session cannot be resolved or registered, skip this
      // injection rather than falling back to project-wide manual context.
      if (sessionId && await ensureSession(sessionId)) {
        const data = await engramFetch(
          `/context/compaction?session_id=${encodeURIComponent(sessionId)}`
        )
        if (data?.context) {
          output.context.push(data.context)
        }
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

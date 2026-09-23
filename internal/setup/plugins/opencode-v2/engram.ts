/**
 * Engram — OpenCode plugin adapter (OpenCode V2 plugin API)
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
 *
 * This file targets the V2 plugin API (`@opencode/plugin`), which loads a
 * default-exported definition with an `id` and a `setup(ctx)` function instead
 * of the V1 hook object. The 1.x adapter lives in plugin/opencode/engram.ts and
 * keeps the same behavior contract.
 *
 * Runtime portability: the plugin host does not guarantee a Bun global (see
 * issue #1218), so this adapter uses node:child_process and node:fs instead of
 * Bun.spawn/Bun.spawnSync/Bun.file.
 */

import { spawn, spawnSync } from "node:child_process"
import { existsSync } from "node:fs"
import { Plugin } from "@opencode/plugin"

// ─── Configuration ───────────────────────────────────────────────────────────

const PARSED_ENGRAM_PORT = parseInt(process.env.ENGRAM_PORT ?? "", 10)
const ENGRAM_PORT = Number.isInteger(PARSED_ENGRAM_PORT) && PARSED_ENGRAM_PORT > 0
  ? PARSED_ENGRAM_PORT
  : 7437
const CONFIGURED_ENGRAM_URL = process.env.ENGRAM_URL?.trim() || undefined
const ENGRAM_URL = CONFIGURED_ENGRAM_URL ?? `http://127.0.0.1:${ENGRAM_PORT}`
const ENGRAM_BIN = process.env.ENGRAM_BIN ?? "engram"
let localReady = CONFIGURED_ENGRAM_URL !== undefined

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

### DELIVERY GUARANTEE

Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

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
  if (!(await ensureLocalReady())) return null
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

function localInstanceID(): string {
  const result = spawnSync(ENGRAM_BIN, ["instance-id"], { encoding: "utf8" })
  const id = (result.stdout ?? "").trim()
  if (result.status !== 0 || !/^[a-f0-9]{32}$/.test(id)) {
    throw new Error("gentle-engram could not resolve its local server identity")
  }
  return id
}

async function isEngramRunning(expectedID = ""): Promise<boolean> {
  try {
    const res = await fetch(`${ENGRAM_URL}/health`, {
      signal: AbortSignal.timeout(500),
    })
    if (!res.ok || (expectedID && (await res.json())?.instance_id !== expectedID)) return false
    return true
  } catch {
    return false
  }
}

async function ensureLocalReady(): Promise<boolean> {
  if (!localReady) {
    // A missing/misbehaving binary must degrade to "not ready", never throw:
    // prompt hooks that reject can block prompt admission in OpenCode V2.
    try {
      localReady = await isEngramRunning(CONFIGURED_ENGRAM_URL ? "" : localInstanceID())
    } catch {
      localReady = false
    }
  }
  return localReady
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

/**
 * Extract plain text from a V2 tool result. Prefers text content parts,
 * falls back to a string content, then to the structured output value.
 */
function resultToText(
  result:
    | {
        content?: string | ReadonlyArray<{ type: string; text?: string }>
        output?: unknown
      }
    | undefined
): string {
  if (!result) return ""

  const content = result.content
  if (typeof content === "string") return content
  if (Array.isArray(content)) {
    const text = content
      .map((part) => (part.type === "text" && typeof part.text === "string" ? part.text : ""))
      .filter(Boolean)
      .join("\n")
    if (text) return text
  }

  if (result.output !== undefined) {
    try {
      return typeof result.output === "string" ? result.output : JSON.stringify(result.output)
    } catch {
      return String(result.output)
    }
  }

  return ""
}

/**
 * Append text to the last system part, or push a new one when there is none.
 *
 * The 1.x adapter concatenated into the last existing system entry instead of
 * pushing a new one. Some models (Qwen3.5, Mistral/Ministral via llama.cpp)
 * reject multiple system blocks — their Jinja chat templates only allow a
 * single system message at the beginning. See: GitHub issue #23.
 */
function appendSystem(system: Array<{ type: "text"; text: string }>, text: string): void {
  const last = system[system.length - 1]
  if (last && last.type === "text") {
    last.text += "\n\n" + text
  } else {
    system.push({ type: "text", text })
  }
}

// ─── Plugin Export ───────────────────────────────────────────────────────────

export default Plugin.define({
  id: "engram",

  async setup(ctx) {
    const locationDirectory = ctx.location.directory
    const locationProjectID = ctx.location.project?.id ?? ""

    let project = "unknown"
    let projectResolutionError = ""
    let projectResolutionGeneration = 0
    let disposed = false

    async function ensureResolvedProject(): Promise<boolean> {
      if (!(await ensureLocalReady())) return false
      if (project !== "unknown" && !projectResolutionError) return true
      const generation = ++projectResolutionGeneration
      const resolved = await resolveProjectName(locationDirectory)
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
    const registrationAttempts = new Set<string>()
    const registeringSessions = new Map<string, Promise<boolean>>()
    const closeRequestedSessions = new Set<string>()
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

    // Best-effort end of one Engram session. One POST per session lifetime:
    // closedSessions dedups confirmed closures, closingSessions dedups calls
    // that are still in flight.
    async function endSessionInEngram(sessionId: string): Promise<boolean> {
      if (closedSessions.has(sessionId)) return true

      const inFlight = closingSessions.get(sessionId)
      if (inFlight) return inFlight

      const close = engramFetch(`/sessions/${encodeURIComponent(sessionId)}/end`, {
        method: "POST",
      }).then((acknowledgement) => {
        if (acknowledgement === null) return false
        closedSessions.add(sessionId)
        for (const sessions of [knownSessions, registrationAttempts, deletedRootSessions, closeRequestedSessions])
          sessions.delete(sessionId)
        return true
      }).finally(() => {
        closingSessions.delete(sessionId)
      })
      closingSessions.set(sessionId, close)
      return close
    }

    async function closeDeletedRootSession(sessionId: string): Promise<boolean> {
      if (closedSessions.has(sessionId)) return true
      if (!deletedRootSessions.has(sessionId)) {
        if (!isKnownAuthoritativeRootSession(sessionId)) return false
        deletedRootSessions.add(sessionId)
      }

      return endSessionInEngram(sessionId)
    }

    // End a session the plugin attempted to register. Confirmed roots retain deletion retries;
    // other attempts, including late reclassifications and ambiguous responses, stay cleanup-eligible.
    async function closeKnownSession(sessionId: string): Promise<boolean> {
      if (closedSessions.has(sessionId)) return true
      if (!registrationAttempts.has(sessionId)) return false
      closeRequestedSessions.add(sessionId)
      const registration = registeringSessions.get(sessionId)
      if (registration) await registration
      if (isKnownAuthoritativeRootSession(sessionId) || deletedRootSessions.has(sessionId)) {
        return closeDeletedRootSession(sessionId)
      }
      return endSessionInEngram(sessionId)
    }

    function cacheSessionInfo(info: { id?: unknown; parentID?: unknown; projectID?: unknown } | undefined): boolean {
      const rawSessionID = info?.id
      const sessionId = typeof rawSessionID === "string" && rawSessionID ? rawSessionID : ""
      if (!sessionId || closedSessions.has(sessionId)) return false
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
        (locationProjectID && rawProjectID !== locationProjectID)
      )
      if (parentID === undefined || invalidProjectID) {
        parentSessions.delete(sessionId)
        subAgentSessions.delete(sessionId)
        return false
      }
      // A close-requested child may repeat its authoritative reclassification
      // to retry a failed end. Parentless events must not revive it as a root.
      if (closeRequestedSessions.has(sessionId)) {
        if (!parentID) return false
        parentSessions.set(sessionId, parentID)
        subAgentSessions.add(sessionId)
        return true
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
        if (invalidSessions.has(current) || closeRequestedSessions.has(current) || closedSessions.has(current)) {
          invalidateResolvedTree(current)
          return ""
        }
        visited.add(current)

        let parentID: string | null
        if (parentSessions.has(current)) {
          parentID = parentSessions.get(current) ?? null
        } else {
          let info: { id?: unknown; parentID?: unknown; projectID?: unknown } | undefined
          try {
            info = await ctx.session.get({ sessionID: current })
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
            if (
              !info ||
              typeof info.id !== "string" ||
              info.id !== current ||
              typeof info.projectID !== "string" ||
              !info.projectID ||
              (locationProjectID && info.projectID !== locationProjectID) ||
              (info.parentID !== undefined && (typeof info.parentID !== "string" || !info.parentID))
            ) {
              return ""
            }
            parentID = (info.parentID as string | undefined) ?? null
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
      if (disposed || !(await ensureResolvedProject()) || disposed) return false
      if (!sessionId || invalidSessions.has(sessionId) || closeRequestedSessions.has(sessionId) || closedSessions.has(sessionId)) return false
      if (knownSessions.has(sessionId)) return true
      // Do not register sub-agent sessions in Engram (issue #116).
      if (subAgentSessions.has(sessionId)) return false
      const inFlight = registeringSessions.get(sessionId)
      if (inFlight) return await inFlight && !closeRequestedSessions.has(sessionId)
      registrationAttempts.add(sessionId)
      const registration = engramFetch("/sessions", {
        method: "POST",
        body: { id: sessionId, project, directory: locationDirectory },
      }).then((acknowledgement) => {
        if (acknowledgement === null) return false
        knownSessions.add(sessionId)
        return true
      }).finally(() => registeringSessions.delete(sessionId))
      registeringSessions.set(sessionId, registration)
      return await registration && !invalidSessions.has(sessionId) && !closeRequestedSessions.has(sessionId)
    }

    // Try to start engram server if not running
    try {
      const expectedID = CONFIGURED_ENGRAM_URL ? "" : localInstanceID()
      localReady = await isEngramRunning(expectedID)
      if (!localReady && !CONFIGURED_ENGRAM_URL) {
        // A missing binary emits an async 'error' event; without a listener it
        // would become an unhandled error and can take the host process down.
        const child = spawn(ENGRAM_BIN, ["serve"], {
          detached: true,
          stdio: "ignore",
        })
        child.on("error", () => {})
        child.unref()
        await new Promise((r) => setTimeout(r, 500))
        localReady = await isEngramRunning(expectedID)
      }
    } catch {
      // Binary not found or can't start — plugin will silently no-op
    }

    if (await ensureResolvedProject()) {
      // Auto-import: if .engram/manifest.json exists in the project repo,
      // run `engram sync --import` to load any new chunks into the local DB.
      // This is how git-synced memories get loaded when cloning a repo or
      // pulling changes. Each chunk is imported only once (tracked by ID).
      try {
        const manifestFile = `${locationDirectory}/.engram/manifest.json`
        if (existsSync(manifestFile)) {
          const child = spawn(ENGRAM_BIN, ["sync", "--import"], {
            cwd: locationDirectory,
            detached: true,
            stdio: "ignore",
          })
          child.on("error", () => {})
          child.unref()
        }
      } catch {
        // Manifest doesn't exist or binary not found — silently skip
      }
    }

    // ─── Event Subscription: Session Lifecycle ───────────────────
    // The V1 `event` hook becomes a subscription to the server event stream.
    // V2 event payloads carry `data` (V1 nested session details under
    // `properties.info`).

    const controller = new AbortController()

    void (async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (!(await ensureLocalReady())) continue

        // --- Session Created ---
        if (event.type === "session.created") {
          const data = event.data as {
            sessionID?: unknown
            parentID?: unknown
            projectID?: unknown
          }
          const sessionId = typeof data.sessionID === "string" ? data.sessionID : ""
          // Only an authoritative parentID makes this session a child. Titles are
          // descriptive and may legitimately resemble generated sub-agent titles.
          const parentID = typeof data.parentID === "string" && data.parentID ? data.parentID : undefined
          const isSubAgent = !!parentID

          if (!cacheSessionInfo({ id: sessionId, parentID, projectID: data.projectID })) continue
          if (isSubAgent) subAgentSessions.add(sessionId)
          else subAgentSessions.delete(sessionId)

          // Issue #1131: a session registered as a root that now reveals a
          // parentID was misregistered. Await its closure, including an
          // in-flight registration, before this lifecycle callback returns.
          if (isSubAgent && registrationAttempts.has(sessionId)) {
            await closeKnownSession(sessionId)
          }

          if (sessionId && !isSubAgent) {
            await ensureSession(sessionId)
          }
        }

        // --- Durable user prompt admission (OpenCode v2.0.4) ---
        if (event.type === "session.inbox.enqueued") {
          const data = event.data as {
            sessionID?: unknown
            inboxID?: unknown
            item?: { type?: unknown; payload?: { text?: unknown } }
          }
          const sourceSessionID = typeof data.sessionID === "string" ? data.sessionID : ""
          const inboxID = typeof data.inboxID === "string" ? data.inboxID : ""
          if (sourceSessionID && inboxID && data.item?.type === "user" && typeof data.item.payload?.text === "string") {
            const sessionId = await resolveAuthoritativeSessionID(sourceSessionID)
            if (sessionId && !subAgentSessions.has(sourceSessionID)) {
              const finalContent = data.item.payload.text.trim()
              if (finalContent.length > 10) {
                const registered = await ensureSession(sessionId)
                const confirmedSessionID = await resolveAuthoritativeSessionID(sourceSessionID)
                if (registered && confirmedSessionID === sessionId) {
                  await engramFetch("/prompts", {
                    method: "POST",
                    body: {
                      session_id: sessionId,
                      source_inbox_id: inboxID,
                      content: stripPrivateTags(truncate(finalContent, 2000)),
                      project,
                    },
                  })
                }
              }
            }
          }
        }

        // --- Session Deleted ---
        if (event.type === "session.deleted") {
          const sessionId = typeof (event.data as { sessionID?: unknown }).sessionID === "string"
            ? (event.data as { sessionID: string }).sessionID
            : ""
          if (sessionId) {
            // Any registration attempt owns an Engram lifecycle (#1131):
            // confirmed roots keep the deletedRootSessions retry discipline.
            // Await an in-flight registration before invalidating local ownership.
            await closeKnownSession(sessionId)
            invalidateSessionTree(sessionId)
          }
        }
      }
    })().catch(() => {
      // Stream aborted on unload or server restart — nothing to do
    })

    // ─── Tool Execution Hooks ────────────────────────────────────
    // execute.before attributes memory-write calls to the authoritative
    // session. execute.after counts tool calls for session stats and captures
    // subagent output passively.

    await ctx.tool.hook("execute.before", async (event) => {
      if (!SESSION_ATTRIBUTED_WRITE_TOOLS.has(canonicalEngramToolName(event.tool))) return
      const authoritativeSessionID = await resolveAuthoritativeSessionID(event.sessionID)
      if (!authoritativeSessionID) {
        throw new Error(`gentle-engram could not resolve an authoritative OpenCode runtime session for ${event.tool}`)
      }
      const registered = await ensureSession(authoritativeSessionID)
      const confirmedSessionID = await resolveAuthoritativeSessionID(event.sessionID)
      if (confirmedSessionID !== authoritativeSessionID) {
        throw new Error(`gentle-engram could not resolve an authoritative OpenCode runtime session for ${event.tool}`)
      }
      if (!registered) {
        if (projectResolutionError) throw new Error(projectResolutionError)
        throw new Error(`gentle-engram could not confirm Engram session registration for ${event.tool}; verify that the Engram server is available and retry`)
      }
      if (event.input && typeof event.input === "object") {
        ;(event.input as Record<string, unknown>).session_id = authoritativeSessionID
      }
    })

    await ctx.tool.hook("execute.after", async (event) => {
      if (ENGRAM_TOOLS.has(canonicalEngramToolName(event.tool))) return

      // event.sessionID comes from OpenCode — always available
      const sessionId = await resolveAuthoritativeSessionID(event.sessionID)
      if (!sessionId) return
      const registered = await ensureSession(sessionId)
      const confirmedSessionID = await resolveAuthoritativeSessionID(event.sessionID)
      if (!registered || confirmedSessionID !== sessionId) return
      toolCounts.set(sessionId, (toolCounts.get(sessionId) ?? 0) + 1)

      // Passive capture: extract learnings from subagent tool output.
      // V1 exposed this tool as "Task"; V2 exposes it as "subagent".
      const toolName = event.tool.toLowerCase()
      if ((toolName === "task" || toolName === "subagent") && event.status === "completed") {
        const text = resultToText(event.result)
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
    })

    // ─── System Prompt: Always-on memory instructions ──────────
    // Injects MEMORY_INSTRUCTIONS into the system prompt of every primary
    // model request. This ensures the agent ALWAYS knows about Engram, even
    // after compaction.
    //
    // We append to the last existing system part instead of pushing a new one.
    // Some models (Qwen3.5, Mistral/Ministral via llama.cpp) reject multiple
    // system messages — their Jinja chat templates only allow a single system
    // block at the beginning. By concatenating, we avoid adding extra system
    // messages that would break these models. See: GitHub issue #23.

    await ctx.session.hook("context", async (event) => {
      appendSystem(event.system, MEMORY_INSTRUCTIONS)

      // ── Save nudge ──────────────────────────────────────────────────────────
      // If it has been a long time since the last mem_save, append a reminder
      // to the system prompt so the agent notices. All fetches are fire-and-
      // forget with short timeouts — any failure silently skips the nudge.
      try {
        if (!(await ensureResolvedProject())) return
        const sessionID: string = event.sessionID ?? ""
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
        appendSystem(event.system, nudge)
        lastNudgeTime.set(sessionID, nowSecs)
      } catch {
        // Any unexpected error — silently skip the nudge, never crash the hook
      }
    })

    // ─── Compaction Hook: Persist memory + inject context ──────────
    // Compaction is triggered by the system (not the agent) when context
    // gets too long. The old agent "dies" and a new one starts with the
    // compacted summary. This is our chance to:
    // 1. Auto-save a session checkpoint (the agent can't do this itself)
    // 2. Inject context from previous sessions into the compaction prompt
    // 3. Tell the compressor to remind the new agent to save memories
    //
    // Where V1 pushed strings onto output.context, V2 exposes the compaction
    // request's system parts; the injected text reaches the same model call.

    await ctx.session.hook("compaction", async (event) => {
      if (!(await ensureResolvedProject())) {
        appendSystem(event.system, `${projectResolutionError} Automatic session, prompt, and passive-capture writes remain disabled.`)
        return
      }
      let sessionId = ""
      if (event.sessionID) {
        sessionId = await resolveAuthoritativeSessionID(event.sessionID)
      }

      // Runtime compaction context must never cross session boundaries. If the
      // authoritative session cannot be resolved or registered, skip this
      // injection rather than falling back to project-wide manual context.
      if (sessionId && await ensureSession(sessionId)) {
        const data = await engramFetch(
          `/context/compaction?session_id=${encodeURIComponent(sessionId)}`
        )
        if (data?.context) {
          appendSystem(event.system, data.context)
        }
      }

      // Tell the compressor to instruct the new agent to persist the
      // compacted summary to Engram. The new agent reads the compacted
      // summary and this instruction, then saves it as a session summary.
      appendSystem(
        event.system,
        `CRITICAL INSTRUCTION FOR COMPACTED SUMMARY:\n` +
        `The agent has access to Engram persistent memory via MCP tools.\n` +
        `You MUST include the following instruction at the TOP of the compacted summary:\n\n` +
        `"FIRST ACTION REQUIRED: Call mem_session_summary with the content of this compacted summary. ` +
        `Use project: '${project}'. This preserves what was accomplished before compaction. Do this BEFORE any other work."\n\n` +
        `This is NOT optional. Without this, everything done before compaction is lost from memory.`
      )
    })

    // ─── Cleanup ─────────────────────────────────────────────────
    // Stop the event subscription and close every session this plugin
    // registered. Confirmed roots retain deletion retries through the same
    // close path used by session.deleted.
    return async () => {
      disposed = true
      controller.abort()
      if (!localReady) return
      await Promise.all([...registrationAttempts].map(closeKnownSession))
    }
  },
})

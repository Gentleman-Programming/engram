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

import { spawn, spawnSync } from "node:child_process"
import { existsSync } from "node:fs"
import type { Plugin } from "@opencode-ai/plugin"

// ─── Configuration ───────────────────────────────────────────────────────────

function optionalEnvironmentValue(value: string | undefined): string | undefined {
  return value?.trim() ? value : undefined
}

const ENGRAM_PORT = parseInt(optionalEnvironmentValue(process.env.ENGRAM_PORT) ?? "7437")
const CONFIGURED_ENGRAM_URL = optionalEnvironmentValue(process.env.ENGRAM_URL)
const ENGRAM_URL = CONFIGURED_ENGRAM_URL ?? `http://127.0.0.1:${ENGRAM_PORT}`
const ENGRAM_BIN = optionalEnvironmentValue(process.env.ENGRAM_BIN) ?? "engram"

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

type DegradedReason = "readiness" | "startup" | "import" | "transport" | "HTTP" | "parse" | "acknowledgement"

function createEngramClient(warn: (reason: DegradedReason) => void) {
let localReady = CONFIGURED_ENGRAM_URL !== undefined

async function engramFetch(
  path: string,
  opts: { method?: string; body?: any } = {}
): Promise<any> {
  const result = await engramFetchResult(path, opts)
  return result?.ok ? result.body : null
}

// Registration needs the refusal code; other callers retain null-on-failure.
async function engramFetchResult(
  path: string,
  opts: { method?: string; body?: any } = {},
  readRefusal = false
): Promise<{ ok: boolean; status: number; body: any } | null> {
	if (!await ensureLocalReady()) return null
  try {
    const res = await fetch(`${ENGRAM_URL}${path}`, {
      method: opts.method ?? "GET",
      headers: opts.body ? { "Content-Type": "application/json" } : undefined,
      body: opts.body ? JSON.stringify(opts.body) : undefined,
      signal: AbortSignal.timeout(3000),
    })
    if (!res.ok && !readRefusal) {
      warn("HTTP")
      return { ok: false, status: res.status, body: null }
    }
    let body: any
    try {
      const text = await res.text()
      body = text.trim() ? JSON.parse(text) : {}
    } catch {
      warn("parse")
      return { ok: false, status: res.status, body: null }
    }
    if (!res.ok && !(readRefusal && res.status === 409 &&
        ["session_project_conflict", "session_already_ended"].includes(body?.code))) warn("HTTP")
    return { ok: res.ok, status: res.status, body }
  } catch {
    warn("transport")
    return null
  }
}

function localInstanceID(): string {
  const result = spawnSync(ENGRAM_BIN, ["instance-id"], { encoding: "utf8", windowsHide: true })
  const id = (result.stdout ?? "").toString().trim()
  if (result.status !== 0 || !/^[a-f0-9]{32}$/.test(id)) throw new Error("gentle-engram could not resolve its local server identity")
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
    try {
      localReady = await isEngramRunning(CONFIGURED_ENGRAM_URL ? "" : localInstanceID())
    } catch {
      localReady = false
    }
  }
  if (!localReady) warn("readiness")
  return localReady
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

async function resolveProjectName(directory: string): Promise<{ project: string; error?: string; ambiguous?: boolean }> {
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
    ambiguous: data?.project_source === "ambiguous" && Array.isArray(data?.available_projects) && data.available_projects.length > 1,
    error: `gentle-engram could not resolve a safe project identity.${reason}${choices} Retry when project resolution is available.`,
  }
}

return { engramFetch, engramFetchResult, ensureLocalReady, resolveProjectName, localInstanceID, isEngramRunning,
  get localReady() { return localReady }, set localReady(value: boolean) { localReady = value } }
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

// Hidden hooks-object key through which the V2 adapter reaches the shared prompt
// capture. A symbol keeps it out of the V1 hook names OpenCode enumerates.
const CAPTURE_PROMPT = Symbol("engram.capturePrompt")
const DELIVER_WARNING = Symbol("engram.deliverWarning")
type CapturePrompt = (sourceSessionID: string, content: string, sourceInboxID?: string) => Promise<void>

export const Engram: Plugin = async (ctx) => {
  // Finite categories bound deduplication and pending memory per instance.
  const seenWarnings = new Set<DegradedReason>()
  const pendingWarnings = new Set<DegradedReason>()
  const warn = (reason: DegradedReason): void => {
    if (seenWarnings.has(reason)) return
    seenWarnings.add(reason)
    pendingWarnings.add(reason)
  }
  const client = createEngramClient(warn)
  const { engramFetch, engramFetchResult, ensureLocalReady, resolveProjectName, localInstanceID, isEngramRunning } = client
  const deliverWarning = (append: (text: string) => boolean): void => {
    if (!pendingWarnings.size) return
    const text = `Engram degraded (${[...pendingWarnings].join(", ")}); automatic memory operations may be incomplete. Verify Engram availability before relying on persistence.`
    if (append(text)) pendingWarnings.clear()
  }
	let project = "unknown"
	let projectResolutionError = ""
	let projectResolutionGeneration = 0
  let projectAmbiguous = false
    let disposed = false

	async function ensureResolvedProject(): Promise<boolean> {
		if (!await ensureLocalReady()) return false
		if (project !== "unknown" && !projectResolutionError) return true
		const generation = ++projectResolutionGeneration
		const resolved = await resolveProjectName(ctx.directory)
		if (generation !== projectResolutionGeneration) return project !== "unknown" && !projectResolutionError
		project = resolved.project
		projectResolutionError = resolved.error ?? ""
    projectAmbiguous = resolved.ambiguous === true
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
  // Ownership/lifecycle sets remain keyed by OpenCode root, never by a suffix.
  const effectiveSessions = new Map<string, { id: string }>()
  const cleanupSessions = new Map<string, Set<string>>()
  // Recovery never assumes the HTTP and MCP clients share a store.
  const recoverySessions = new Map<string, { id: string; project: string; completed: boolean }>()
  // A recovery acknowledgement authorizes only its runtime root, never the cwd.
  const recoveredProjects = new Map<string, string>()
  const projectForSession = (sessionId: string): string => recoveredProjects.get(sessionId) ?? project
  const registrationErrors = new Map<string, string>()
  const warnedSessions = new Set<string>()

  function registrationFailed(sessionId: string, cause: string): false {
    registrationErrors.set(sessionId, cause)
    if (!warnedSessions.has(sessionId)) {
      warnedSessions.add(sessionId)
      console.warn(`gentle-engram session ${sessionId}: ${cause}; verify Engram project ownership and server availability, or start a new OpenCode session and retry`)
    }
    return false
  }
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

    const close = Promise.all([...(cleanupSessions.get(sessionId) ?? [])].map(async (effectiveID) => {
      const acknowledgement = await engramFetch(`/sessions/${encodeURIComponent(effectiveID)}/end`, { method: "POST" })
      if (acknowledgement?.id !== effectiveID || acknowledgement?.status !== "completed") {
        warn("acknowledgement")
        return false
      }
      cleanupSessions.get(sessionId)?.delete(effectiveID)
      return true
    })).then((acknowledgements) => {
      if (acknowledgements.some((acknowledged) => !acknowledged)) return false
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
      (ctx.project?.id && rawProjectID !== ctx.project.id)
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
   * Register or renew the root through the core resume API, retaining only
   * its acknowledged effective identity for session-bound writes.
   *
   * Silently skips sub-agent sessions (tracked in `subAgentSessions`).
   */
  async function acknowledgeRecovery(sessionId: string): Promise<boolean> {
    const recovery = recoverySessions.get(sessionId)
    if (!recovery || !recovery.completed || !recovery.project || disposed || invalidSessions.has(sessionId) || closeRequestedSessions.has(sessionId)) return false
    const result = await engramFetchResult(`/sessions/${encodeURIComponent(recovery.id)}`)
    const body = result?.body
    if (disposed || invalidSessions.has(sessionId) || closeRequestedSessions.has(sessionId) || closedSessions.has(sessionId) || subAgentSessions.has(sessionId) || recoverySessions.get(sessionId) !== recovery) return false
    if (!result?.ok || body?.id !== recovery.id || body?.project !== recovery.project || body?.ended_at || body?.ownership_mode !== "project_owned") return false
    recoveredProjects.set(sessionId, recovery.project)
    effectiveSessions.set(sessionId, { id: recovery.id })
    knownSessions.add(sessionId)
    registrationAttempts.add(sessionId)
    const cleanup = cleanupSessions.get(sessionId) ?? new Set<string>()
    cleanup.add(recovery.id)
    cleanupSessions.set(sessionId, cleanup)
    recoverySessions.delete(sessionId)
    return true
  }

  async function ensureSession(sessionId: string, renew = false): Promise<boolean> {
    if (recoverySessions.has(sessionId)) return acknowledgeRecovery(sessionId)
    if (disposed || (!recoveredProjects.has(sessionId) && !await ensureResolvedProject()) || disposed) return false
    if (!sessionId || invalidSessions.has(sessionId) || closeRequestedSessions.has(sessionId) || closedSessions.has(sessionId)) return false
    if (!renew && knownSessions.has(sessionId)) return true
    // Do not register sub-agent sessions in Engram (issue #116).
    if (subAgentSessions.has(sessionId)) return false
    const inFlight = registeringSessions.get(sessionId)
    if (inFlight) return await inFlight && !closeRequestedSessions.has(sessionId)
    registrationAttempts.add(sessionId)
    const registration = (async () => {
      const cleanup = cleanupSessions.get(sessionId) ?? new Set<string>()
      cleanupSessions.set(sessionId, cleanup)
      knownSessions.delete(sessionId)
      registrationErrors.delete(sessionId)
      const result = await engramFetchResult("/sessions", {
        method: "POST",
        body: { id: sessionId, project: projectForSession(sessionId), directory: ctx.directory, resume: true },
      }, true)
      const id = result?.body?.id
      if (result?.ok && result.body?.status === "created" && typeof id === "string" &&
          (id === sessionId || id.startsWith(`${sessionId}:resume:`))) {
        effectiveSessions.set(sessionId, { id })
        cleanup.add(id)
        knownSessions.add(sessionId)
        return true
      }
      // Failed or uncertain renewals retain previously acknowledged cleanup
      // ownership. The server chooses new identities, so a newly created but
      // unacknowledged continuation may remain open; never guess its identity.
      const cause = result?.status === 409 &&
          (result.body?.code === "session_project_conflict" || result.body?.code === "session_already_ended")
        ? `HTTP 409 ${result.body.code}`
        : result && !result.ok ? `HTTP ${result.status} session registration refused` : "session registration was not acknowledged"
      return registrationFailed(sessionId, cause)
    })().finally(() => registeringSessions.delete(sessionId))
    registeringSessions.set(sessionId, registration)
    return await registration && !invalidSessions.has(sessionId) && !closeRequestedSessions.has(sessionId)
  }

  /**
   * Capture one user prompt for its authoritative root session. A durable
   * `sourceInboxID` lets the server treat replays as no-ops and refuse deleted
   * identities (HTTP 409); engramFetch drops that response like any other failure.
   */
  const capturePrompt: CapturePrompt = async (sourceSessionID, content, sourceInboxID) => {
    const sessionId = await resolveAuthoritativeSessionID(sourceSessionID)
    // Skip child prompts even when ownership was discovered through the SDK.
    if (!sessionId || subAgentSessions.has(sourceSessionID)) return

    // Only capture non-trivial prompts (>10 chars)
    if (content.length <= 10) return
    const registered = await ensureSession(sessionId, true)
    const confirmedSessionID = await resolveAuthoritativeSessionID(sourceSessionID)
    if (!registered || confirmedSessionID !== sessionId) return
    await engramFetch("/prompts", {
      method: "POST",
      body: {
        session_id: effectiveSessions.get(sessionId)!.id,
        // Redact before truncating: a <private> block straddling the
        // limit would otherwise lose its closing tag and leak.
        content: truncate(stripPrivateTags(content), 2000),
        project: projectForSession(sessionId),
        ...(sourceInboxID ? { source_inbox_id: sourceInboxID } : {}),
      },
    })
  }

  // Try to start engram server if not running
	try {
		const expectedID = CONFIGURED_ENGRAM_URL ? "" : localInstanceID()
		client.localReady = await isEngramRunning(expectedID)
		if (!client.localReady && !CONFIGURED_ENGRAM_URL) {
      const serverChild = spawn(ENGRAM_BIN, ["serve"], {
        detached: true,
        stdio: "ignore",
        windowsHide: true,
      })
      serverChild.on("error", () => warn("startup"))
      serverChild.on("exit", (code, signal) => { if (code !== null && code !== 0 || signal) warn("startup") })
      serverChild.unref()
			await new Promise((r) => setTimeout(r, 500))
			client.localReady = await isEngramRunning(expectedID)
		}
    if (!client.localReady) warn("readiness")
	} catch { warn("startup") }

	if (await ensureResolvedProject()) {
		// Auto-import: if .engram/manifest.json exists in the project repo,
		// run `engram sync --import` to load any new chunks into the local DB.
		// This is how git-synced memories get loaded when cloning a repo or
		// pulling changes. Each chunk is imported only once (tracked by ID).
		try {
			const manifestFile = `${ctx.directory}/.engram/manifest.json`
			if (existsSync(manifestFile)) {
        const importChild = spawn(ENGRAM_BIN, ["sync", "--import"], {
          cwd: ctx.directory,
          detached: true,
          stdio: "ignore",
          windowsHide: true,
        })
        importChild.on("error", () => warn("import"))
        importChild.on("exit", (code, signal) => { if (code !== null && code !== 0 || signal) warn("import") })
        importChild.unref()
			}
		} catch {
      warn("import")
		}
	}

  return {
    [CAPTURE_PROMPT]: capturePrompt,
    [DELIVER_WARNING]: deliverWarning,

		dispose: async () => {
      disposed = true
			if (!client.localReady) return
      // Every registration attempt owns an Engram lifecycle (#1131), including
      // children misregistered before their parentID was known.
      await Promise.all([...registrationAttempts].map(closeKnownSession))
    },

    // ─── Event Listeners ───────────────────────────────────────────

		event: async ({ event }) => {
			if (!await ensureLocalReady()) return
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

        // Issue #1131: a session registered as a root that now reveals a
        // parentID was misregistered. Await its closure, including an
        // in-flight registration, before this lifecycle callback returns.
        if (isSubAgent && registrationAttempts.has(sessionId)) {
          await closeKnownSession(sessionId)
        }

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
          // Any registration attempt owns an Engram lifecycle (#1131):
          // confirmed roots keep the deletedRootSessions retry discipline.
          // Await an in-flight registration before invalidating local ownership.
          await closeKnownSession(sessionId)
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

      await capturePrompt(input.sessionID, content || fallback)
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
      const registered = await ensureSession(authoritativeSessionID, true)
      const confirmedSessionID = await resolveAuthoritativeSessionID(input.sessionID)
      if (confirmedSessionID !== authoritativeSessionID) {
        throw new Error(`gentle-engram could not resolve an authoritative OpenCode runtime session for ${input.tool}`)
      }
      if (!registered) {
        const tool = canonicalEngramToolName(input.tool)
        if (!disposed && projectAmbiguous && ["mem_save", "mem_save_prompt", "mem_session_summary"].includes(tool)) {
          const id = effectiveSessions.get(authoritativeSessionID)?.id ?? authoritativeSessionID
          recoverySessions.set(authoritativeSessionID, { id, project: typeof output.args.project === "string" ? output.args.project : "", completed: false })
          output.args.session_id = id
          return
        }
			if (projectResolutionError) throw new Error(projectResolutionError)
        throw new Error(`gentle-engram could not confirm Engram session registration for ${input.tool}${registrationErrors.has(authoritativeSessionID) ? `: ${registrationErrors.get(authoritativeSessionID)}` : ""}; verify that the Engram server is available and retry`)
      }
      output.args.session_id = effectiveSessions.get(authoritativeSessionID)!.id
    },

    "tool.execute.after": async (input, output) => {
      try {
      if (ENGRAM_TOOLS.has(canonicalEngramToolName(input.tool))) {
        const root = await resolveAuthoritativeSessionID(input.sessionID)
        if (root && recoverySessions.has(root)) {
          recoverySessions.get(root)!.completed = true
          await acknowledgeRecovery(root)
        }
        return
      }

      // input.sessionID comes from OpenCode — always available
      const sessionId = await resolveAuthoritativeSessionID(input.sessionID)
      if (!sessionId) return
      const registered = await ensureSession(sessionId, true)
      const confirmedSessionID = await resolveAuthoritativeSessionID(input.sessionID)
      if (!registered || confirmedSessionID !== sessionId) return
      toolCounts.set(sessionId, (toolCounts.get(sessionId) ?? 0) + 1)

      // V1 emits `task`; the V2 adapter normalizes `subagent` to `Task`.
      if ((input.tool === "task" || input.tool === "Task") && output) {
        const text = typeof output === "string"
          ? output
          : typeof output?.output === "string"
            ? output.output
            : JSON.stringify(output)
        if (text.length > 50) {
          await engramFetch("/observations/passive", {
            method: "POST",
            body: {
              session_id: effectiveSessions.get(sessionId)!.id,
              content: stripPrivateTags(text),
              project: projectForSession(sessionId),
              source: "task-complete",
            },
          })
        }
      }
      } finally {
        deliverWarning((text) => {
          if (typeof output?.output !== "string") return false
          output.output += `\n\n${text}`
          return true
        })
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
        const rootID: string = input.sessionID ?? ""
        if (!recoveredProjects.has(rootID) && !await ensureResolvedProject()) return
        if (!rootID || invalidSessions.has(rootID) || subAgentSessions.has(rootID)) return
        // Read-only: never registers. A resumed root is looked up by the
        // effective session its writes already use.
        const sessionID = effectiveSessions.get(rootID)?.id ?? rootID

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
            let sessionData: any
            try { sessionData = await sessionRes.json() } catch { warn("parse"); return }
            const startedAt: string = sessionData?.started_at ?? ""
            if (startedAt) {
              sessionStartEpoch = toEpochSecs(startedAt)
            }
          } else warn("HTTP")
        } catch {
          warn("transport")
          // Server unreachable or timed out — skip nudge
          return
        }
        if (sessionStartEpoch !== null && sessionStartEpoch > 0 && nowSecs - sessionStartEpoch < 300) return

        // Check when the last observation was saved for this project
        let obsData: unknown
        let observationsResponseOK = false
        try {
          const obsRes = await fetch(
            `${ENGRAM_URL}/observations?project=${encodeURIComponent(projectForSession(rootID))}&limit=1&sort=created_at:desc`,
            { signal: AbortSignal.timeout(200) }
          )
          if (obsRes.ok) {
            observationsResponseOK = true
            try { obsData = await obsRes.json() } catch { warn("parse"); return }
          } else warn("HTTP")
        } catch {
          warn("transport")
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
      let sessionId = ""
      if (input.sessionID) {
        sessionId = await resolveAuthoritativeSessionID(input.sessionID)
      }
      if (!recoveredProjects.has(sessionId) && !await ensureResolvedProject()) {
        output.context.push(`${projectResolutionError} Automatic session, prompt, and passive-capture writes remain disabled.`)
        return
      }

      // Runtime compaction context must never cross session boundaries. If the
      // authoritative session cannot be resolved or registered, skip this
      // injection rather than falling back to project-wide manual context.
      if (sessionId && await ensureSession(sessionId, true)) {
        const data = await engramFetch(
          `/context/compaction?session_id=${encodeURIComponent(effectiveSessions.get(sessionId)!.id)}`
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
        `Use project: '${projectForSession(sessionId)}'. This preserves what was accomplished before compaction. Do this BEFORE any other work."\n\n` +
        `This is NOT optional. Without this, everything done before compaction is lost from memory.`
      )
    },
  }
}

// ─── OpenCode V2 Adapter ─────────────────────────────────────────────────────
// OpenCode V2 calls `setup(ctx)` instead of `server`. It exposes hooks through
// per-domain registrations and session lifecycle through an event stream, so
// this adapter translates them onto the V1 hooks above and adds no behavior.
// Types are declared structurally: V1 hosts may not ship `@opencode/plugin`.
//
// Not exported by name on purpose: older V1 loaders call every exported
// function as a plugin factory.

type V2SystemPart = { type: "text"; text: string }
type V2Registration = { dispose: () => Promise<void> }
type V2Hook = (name: string, callback: (input: any) => Promise<void> | void) => Promise<V2Registration>
type V2Context = {
  location: { directory: string; project?: { id?: string } }
  event: { subscribe: (options?: { signal?: AbortSignal }) => AsyncIterable<any> }
  session: { get: (input: { sessionID: string }) => Promise<any>; hook: V2Hook }
  tool: { hook: V2Hook }
}

// V1 hooks append to the last system string; V2 carries system text parts.
function appendSystemText(system: V2SystemPart[], text: string): void {
  const last = system[system.length - 1]
  if (last) system[system.length - 1] = { ...last, text: `${last.text}\n\n${text}` }
  else system.push({ type: "text", text })
}

async function withSystemStrings(system: V2SystemPart[], run: (texts: string[]) => Promise<void>): Promise<void> {
  const texts = system.map((part) => part.text)
  await run(texts)
  texts.forEach((text, index) => {
    if (index >= system.length) system.push({ type: "text", text })
    else if (text !== system[index].text) system[index] = { ...system[index], text }
  })
}

// V2 `Tool.Result.content` is `string | Content[]`; `subagent` returns a string.
function v2ToolResultText(result: any): string {
  const text = typeof result?.content === "string"
    ? result.content
    : Array.isArray(result?.content)
    ? result.content.filter((part: any) => part?.type === "text").map((part: any) => part.text ?? "").join("\n")
    : ""
  if (text) return text
  if (typeof result?.output === "string") return result.output
  return result?.output === undefined ? "" : JSON.stringify(result.output)
}

// V2 session events carry `data.sessionID`; V1 hooks expect `properties.info.id`.
function v1SessionEvent(event: any, directory: string): any {
  const data = event?.data
  if (typeof data?.sessionID !== "string") return undefined
  if (event.type === "session.created" || event.type === "session.updated") {
    // The V2 server is shared across locations; V1 only saw its own instance.
    if (data.location?.directory && data.location.directory !== directory) return undefined
    return { type: event.type, properties: { info: { id: data.sessionID, parentID: data.parentID, projectID: data.projectID } } }
  }
  if (event.type === "session.deleted") {
    return { type: event.type, properties: { info: { id: data.sessionID } } }
  }
  return undefined
}

// V2 admits each human prompt as a durable `user` inbox item. Its inboxID is the
// prompt's identity: replays reuse it, distinct items with equal text do not.
function v2InboxPrompt(event: any, directory: string): { sessionID: string; inboxID: string; text: string } | undefined {
  if (event?.type !== "session.inbox.enqueued") return undefined
  // The envelope location is optional; the authoritative session lookup still
  // rejects sessions outside this instance's project.
  if (event.location?.directory && event.location.directory !== directory) return undefined
  const data = event.data
  const item = data?.item
  if (typeof data?.sessionID !== "string" || !data.sessionID) return undefined
  if (typeof data.inboxID !== "string" || !data.inboxID) return undefined
  if (item?.type !== "user" || typeof item.payload?.text !== "string") return undefined
  return { sessionID: data.sessionID, inboxID: data.inboxID, text: item.payload.text.trim() }
}

// The V2 event stream ends or throws when the server restarts; reconnect with
// a bounded doubling delay that resets once events flow again.
const V2_EVENT_RETRY_MIN_MS = 50
const V2_EVENT_RETRY_MAX_MS = 5000

function delayUnlessAborted(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const done = () => {
      clearTimeout(timer)
      signal.removeEventListener("abort", done)
      resolve()
    }
    const timer = setTimeout(done, ms)
    signal.addEventListener("abort", done, { once: true })
  })
}

async function setupEngramV2(ctx: V2Context): Promise<() => Promise<void>> {
  const hooks: Record<string | symbol, any> = await Engram({
    directory: ctx.location.directory,
    project: { id: ctx.location.project?.id },
    client: {
      session: {
        // V1 SDK results carry `{ data, error }`; the V2 client throws instead.
        async get({ path }: { path: { id: string } }) {
          try {
            return { data: await ctx.session.get({ sessionID: path.id }) }
          } catch (error) {
            return { error }
          }
        },
      },
    },
  } as any)

  const abort = new AbortController()
  const registrations: V2Registration[] = []
  let listening: Promise<void> = Promise.resolve()
  const cleanup = async () => {
    abort.abort()
    await Promise.all(registrations.map((registration) => registration.dispose()))
    await listening
    await hooks.dispose?.()
  }

  try {
    // No `prompt` hook: it lacks a durable identity, so prompts are captured
    // from `session.inbox.enqueued` below instead.
    registrations.push(await ctx.session.hook("context", async (request) => {
      await withSystemStrings(request.system, (system) =>
        hooks["experimental.chat.system.transform"]({ sessionID: request.sessionID, model: request.model }, { system }))
    }))

    registrations.push(await ctx.session.hook("compaction", async (request) => {
      const context: string[] = []
      await hooks["experimental.session.compacting"]({ sessionID: request.sessionID }, { context })
      if (context.length > 0) appendSystemText(request.system, context.join("\n\n"))
    }))

    registrations.push(await ctx.tool.hook("execute.before", async (call) => {
      const args = call.input && typeof call.input === "object" ? call.input : {}
      await hooks["tool.execute.before"]({ tool: call.tool, sessionID: call.sessionID, callID: call.id }, { args })
      if (args !== call.input && Object.keys(args).length > 0) call.input = args
    }))

    registrations.push(await ctx.tool.hook("execute.after", async (call) => {
      // Normalize V2 `subagent` to the shared handler’s legacy `Task` alias.
      const tool = call.tool === "subagent" ? "Task" : call.tool
      const output = call.status === "completed" ? v2ToolResultText(call.result) : ""
      await hooks["tool.execute.after"]({ tool, sessionID: call.sessionID, callID: call.id }, output)
      if (call.status !== "completed") return
      hooks[DELIVER_WARNING]((text: string) => {
        const result = call.result
        if (!result || typeof result !== "object") return false
        if (typeof result.content === "string") {
          call.result = { ...result, content: `${result.content}\n\n${text}` }
        } else if (Array.isArray(result.content) && result.content.every((part: any) =>
          part && (part.type === "text" && typeof part.text === "string" ||
            part.type === "file" && typeof part.uri === "string" && typeof part.mime === "string"))) {
          call.result = { ...result, content: [...result.content, { type: "text", text }] }
        } else return false
        return true
      })
    }))

    listening = (async () => {
      let retryMs = V2_EVENT_RETRY_MIN_MS
      while (!abort.signal.aborted) {
        try {
          for await (const event of ctx.event.subscribe({ signal: abort.signal })) {
            if (abort.signal.aborted) break
            // Every subscription opens with a server.connected handshake; only a
            // real event proves the stream is healthy enough to reset the backoff.
            if (event?.type !== "server.connected") retryMs = V2_EVENT_RETRY_MIN_MS
            try {
              const prompt = v2InboxPrompt(event, ctx.location.directory)
              if (prompt) await (hooks[CAPTURE_PROMPT] as CapturePrompt)(prompt.sessionID, prompt.text, prompt.inboxID)
              const translated = v1SessionEvent(event, ctx.location.directory)
              if (translated) await hooks.event({ event: translated })
            } catch {
              // One failing event must not stop lifecycle tracking.
            }
          }
        } catch {
          // Events missed while disconnected are lost; hooks still bind sessions lazily.
        }
        if (abort.signal.aborted) break
        await delayUnlessAborted(retryMs, abort.signal)
        retryMs = Math.min(retryMs * 2, V2_EVENT_RETRY_MAX_MS)
      }
    })()
  } catch (cause) {
    await cleanup()
    throw cause
  }

  return cleanup
}

// V1 (1.18.29+) calls server(); V2 calls setup().
export default { id: "engram", server: Engram, setup: setupEngramV2 }

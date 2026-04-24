import type { ExtensionAPI } from "@mariozechner/pi-coding-agent"
import { Type } from "typebox"
import { spawn } from "node:child_process"

const ENGRAM_PORT = Number.parseInt(process.env.ENGRAM_PORT ?? "7437", 10)
const ENGRAM_URL = `http://127.0.0.1:${ENGRAM_PORT}`
const ENGRAM_BIN = process.env.ENGRAM_BIN ?? "engram"

const STARTUP_NOTICE =
  "Engram has relevant memory for this project. Use mem_context or mem_search when useful."

const COMPACTION_RECOVERY_NOTICE =
  "FIRST ACTION REQUIRED: Call mem_session_summary with the compacted summary first, then call mem_context before continuing."

const COMPACTION_SAVED_NOTICE =
  "Compaction summary saved to Engram. Use mem_context to restore continuity."

const COMPACTION_UNAVAILABLE_NOTICE =
  "Compaction summary unavailable. Use /engram-recovery, then mem_context manually."

const TOOL_GUIDELINES = [
  "Use mem_context for recent continuity before broad searches.",
  "Use mem_search with specific project/scope filters when possible.",
  "Use mem_save and mem_session_summary for durable continuity.",
]

type EngramHTTPResult = {
  ok: boolean
  path: string
  status?: number
  data?: unknown
  error?: string
}

type BackendReadiness = {
  ok: boolean
  startupAttempted: boolean
  startupError?: string
}

const BACKEND_STARTUP_POLL_MS = [120, 240, 360, 600, 900]

function redactPrivateTags(input: string): string {
  if (!input) return ""
  return input.replace(/<private>[\s\S]*?<\/private>/gi, "[REDACTED]").trim()
}

function redactValue(value: unknown): unknown {
  if (typeof value === "string") {
    return redactPrivateTags(value)
  }
  if (Array.isArray(value)) {
    return value.map((item) => redactValue(item))
  }
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {}
    for (const [key, raw] of Object.entries(value as Record<string, unknown>)) {
      out[key] = redactValue(raw)
    }
    return out
  }
  return value
}

function projectFromDirectory(directory: string): string {
  const parts = directory.split(/[\\/]/).filter(Boolean)
  return parts.at(-1) ?? "unknown"
}

function parseSessionID(sessionFile: string): string {
  const fileName = sessionFile.split(/[\\/]/).pop() ?? ""
  return fileName.replace(/\.[^.]+$/, "")
}

function deriveSessionId(ctx: any): string {
  if (ctx?.sessionManager && typeof ctx.sessionManager.getSessionFile === "function") {
    const sessionFile = ctx.sessionManager.getSessionFile()
    if (typeof sessionFile === "string" && sessionFile.length > 0) {
      return redactPrivateTags(parseSessionID(sessionFile))
    }
  }

  const fallbackProject = projectFromDirectory(String(ctx?.cwd ?? ""))
  return redactPrivateTags(`pi-${fallbackProject}`)
}

function deriveRuntime(ctx: any): { sessionId: string; project: string; directory: string } {
  const directory = redactPrivateTags(String(ctx.cwd ?? ""))
  const project = redactPrivateTags(projectFromDirectory(directory))
  const sessionId = deriveSessionId(ctx)
  return { sessionId, project, directory }
}

async function engramFetch(path: string, init?: RequestInit): Promise<EngramHTTPResult> {
  try {
    const response = await fetch(`${ENGRAM_URL}${path}`, init)
    const status = response.status
    const contentType = response.headers.get("content-type") ?? ""

    let data: unknown = null
    if (status !== 204) {
      if (contentType.includes("application/json")) {
        data = await response.json()
      } else {
        const text = await response.text()
        data = text.length > 0 ? text : null
      }
    }

    if (!response.ok) {
      return {
        ok: false,
        path,
        status,
        data,
        error: `HTTP ${status}`,
      }
    }

    return {
      ok: true,
      path,
      status,
      data: status === 204 ? { ok: true } : data,
    }
  } catch (error) {
    return {
      ok: false,
      path,
      error: error instanceof Error ? error.message : "network_error",
    }
  }
}

async function postJSON(path: string, body: Record<string, unknown>): Promise<EngramHTTPResult> {
  return engramFetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(redactValue(body)),
  })
}

function successToolResult(summary: string, details: unknown) {
  return {
    content: [{ type: "text", text: summary }],
    details,
  }
}

function failureToolResult(message: string, details: Record<string, unknown> = {}) {
  return {
    content: [{ type: "text", text: message }],
    details: {
      ok: false,
      ...details,
    },
    isError: true,
  }
}

function compactPreview(value: unknown): string {
  if (value === null || value === undefined) return ""
  if (typeof value === "string") return value.slice(0, 200)

  try {
    const encoded = JSON.stringify(value)
    return encoded.slice(0, 200)
  } catch {
    return String(value).slice(0, 200)
  }
}

function extractObservationSummaryFields(payload: unknown): { id: string; title: string; content: string } {
  if (!payload || typeof payload !== "object") {
    return { id: "", title: "", content: "" }
  }

  const record = payload as Record<string, unknown>
  const directID = typeof record.id === "string" || typeof record.id === "number" ? String(record.id) : ""
  const directTitle = typeof record.title === "string" ? record.title : ""
  const directContent = typeof record.content === "string" ? record.content : ""
  if (directID || directTitle || directContent) {
    return { id: directID, title: directTitle, content: directContent }
  }

  const nestedObservation = record.observation
  if (nestedObservation && typeof nestedObservation === "object") {
    return extractObservationSummaryFields(nestedObservation)
  }

  const nestedData = record.data
  if (nestedData && typeof nestedData === "object") {
    return extractObservationSummaryFields(nestedData)
  }

  return { id: "", title: "", content: "" }
}

function observationSummaryPreview(payload: unknown): string {
  const summary = extractObservationSummaryFields(payload)
  const titlePreview = summary.title ? redactPrivateTags(summary.title) : ""
  const contentPreview = summary.content ? redactPrivateTags(summary.content) : ""

  if (titlePreview && contentPreview) {
    return compactPreview(`${titlePreview} — ${contentPreview}`)
  }
  if (titlePreview) {
    return compactPreview(titlePreview)
  }
  if (contentPreview) {
    return compactPreview(contentPreview)
  }

  return compactPreview(payload)
}

function summarizeToolResult(toolName: string, result: EngramHTTPResult) {
  if (!result.ok) {
    return failureToolResult(`Engram request failed for ${toolName}.`, {
      path: result.path,
      status: result.status,
      error: result.error,
      response: result.data,
    })
  }

  if (toolName === "mem_save") {
    const payload = result.data as Record<string, unknown> | null
    const id = payload && payload.id ? String(payload.id) : ""
    const title = payload && payload.title ? String(payload.title) : ""

    if (id && title) {
      return successToolResult(`Memory saved (#${id}: ${title}).`, result)
    }
    if (id) {
      return successToolResult(`Memory saved (#${id}).`, result)
    }
    return successToolResult("Memory saved.", result)
  }

  if (toolName === "mem_search") {
    const count = Array.isArray(result.data) ? result.data.length : 0
    if (count > 0) {
      return successToolResult(`Found ${count} memory result(s).`, result)
    }
    const preview = compactPreview(result.data)
    return successToolResult(preview ? `Search complete. ${preview}` : "Search complete.", result)
  }

  if (toolName === "mem_context") {
    const preview = compactPreview(result.data)
    return successToolResult(preview ? `Memory context loaded. ${preview}` : "Memory context loaded.", result)
  }

  if (toolName === "mem_get_observation") {
    const summary = extractObservationSummaryFields(result.data)
    const id = summary.id
    const preview = observationSummaryPreview(result.data)
    if (id) {
      if (preview) {
        return successToolResult(`Loaded observation #${id}. ${preview}`, result)
      }
      return successToolResult(`Loaded observation #${id}.`, result)
    }
    return successToolResult(preview ? `Observation loaded. ${preview}` : "Observation loaded.", result)
  }

  if (toolName === "mem_session_summary") {
    return successToolResult("Session summary saved.", result)
  }

  if (toolName === "mem_save_prompt") {
    return successToolResult("Prompt saved.", result)
  }

  return successToolResult("Engram request completed.", result)
}

function queryString(params: Record<string, unknown>): string {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue
    query.set(key, String(redactValue(value)))
  }
  const qs = query.toString()
  return qs.length > 0 ? `?${qs}` : ""
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object"
}

function observationsFromRecentResult(result: unknown): unknown[] {
  if (Array.isArray(result)) return result
  if (!isRecord(result)) return []

  const directObservations = result.observations
  if (Array.isArray(directObservations)) return directObservations

  const wrappedData = result.data
  if (Array.isArray(wrappedData)) return wrappedData
  if (isRecord(wrappedData)) {
    const dataObservations = wrappedData.observations
    if (Array.isArray(dataObservations)) return dataObservations

    const nestedData = wrappedData.data
    if (Array.isArray(nestedData)) return nestedData
    if (isRecord(nestedData)) {
      const nestedObservations = nestedData.observations
      if (Array.isArray(nestedObservations)) return nestedObservations
    }
  }

  return []
}

async function isRunning(): Promise<boolean> {
  try {
    const response = await fetch(`${ENGRAM_URL}/health`, { signal: AbortSignal.timeout(500) })
    return response.ok
  } catch {
    return false
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function backendFailureToolResult(toolName: string, readiness: BackendReadiness) {
  return failureToolResult(`Engram auto-start failed for ${toolName}.`, {
    autoStartFailed: true,
    startupAttempted: readiness.startupAttempted,
    startupError: readiness.startupError,
  })
}

async function ensureBackend(): Promise<BackendReadiness> {
  if (await isRunning()) {
    return { ok: true, startupAttempted: false }
  }

  try {
    const child = spawn(ENGRAM_BIN, ["serve"], {
      stdio: "ignore",
      detached: true,
    })
    if (typeof child.unref === "function") {
      child.unref()
    }

    for (let attempt = 0; attempt < BACKEND_STARTUP_POLL_MS.length; attempt++) {
      if (await isRunning()) {
        return { ok: true, startupAttempted: true }
      }
      await sleep(BACKEND_STARTUP_POLL_MS[attempt])
    }
  } catch (error) {
    return {
      ok: false,
      startupAttempted: true,
      startupError: error instanceof Error ? error.message : "spawn_failed",
    }
  }

  return {
    ok: false,
    startupAttempted: true,
    startupError: "backend_not_ready_after_spawn",
  }
}

function registerMemoryTools(pi: any): void {
  if (typeof pi.registerTool !== "function") return

  pi.registerTool({
    name: "mem_search",
    label: "Engram Memory Search",
    description: "Search Engram observations using full text and filters.",
    parameters: Type.Object({
      query: Type.String({ minLength: 1 }),
      project: Type.Optional(Type.String()),
      scope: Type.Optional(Type.String()),
      type: Type.Optional(Type.String()),
      limit: Type.Optional(Type.Number({ minimum: 1, maximum: 100 })),
    }),
    promptSnippet: "Call mem_search for recall and continuity lookups.",
    promptGuidelines: ["Use mem_search by name when querying historical observations.", ...TOOL_GUIDELINES],
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      void toolCallId
      void signal
      void onUpdate
      const ready = await ensureBackend()
      if (!ready.ok) {
        return backendFailureToolResult("mem_search", ready)
      }
      const runtime = deriveRuntime(ctx)
      const result = await engramFetch(
        `/search${queryString({
          q: params.query,
          project: params.project ?? runtime.project,
          scope: params.scope,
          type: params.type,
          limit: params.limit ?? 10,
        })}`,
      )
      return summarizeToolResult("mem_search", result)
    },
  })

  pi.registerTool({
    name: "mem_context",
    label: "Engram Memory Context",
    description: "Fetch compact project context from Engram.",
    parameters: Type.Object({
      project: Type.Optional(Type.String()),
      scope: Type.Optional(Type.String()),
    }),
    promptSnippet: "Call mem_context for compact session continuity.",
    promptGuidelines: ["Use mem_context by name at startup or after compaction.", ...TOOL_GUIDELINES],
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      void toolCallId
      void signal
      void onUpdate
      const ready = await ensureBackend()
      if (!ready.ok) {
        return backendFailureToolResult("mem_context", ready)
      }
      const runtime = deriveRuntime(ctx)
      const result = await engramFetch(
        `/context${queryString({
          project: params.project ?? runtime.project,
          scope: params.scope,
        })}`,
      )
      return summarizeToolResult("mem_context", result)
    },
  })

  pi.registerTool({
    name: "mem_save",
    label: "Engram Memory Save",
    description: "Save an observation to Engram memory.",
    parameters: Type.Object({
      title: Type.String({ minLength: 1 }),
      content: Type.String({ minLength: 1 }),
      type: Type.Optional(Type.String()),
      project: Type.Optional(Type.String()),
      scope: Type.Optional(Type.String()),
      topic_key: Type.Optional(Type.String()),
      session_id: Type.Optional(Type.String()),
    }),
    promptSnippet: "Call mem_save immediately after decisions, bugfixes, and discoveries.",
    promptGuidelines: ["Use mem_save by name with structured What/Why/Where/Learned content.", ...TOOL_GUIDELINES],
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      void toolCallId
      void signal
      void onUpdate
      const ready = await ensureBackend()
      if (!ready.ok) {
        return backendFailureToolResult("mem_save", ready)
      }
      const runtime = deriveRuntime(ctx)
      const result = await postJSON("/observations", {
        session_id: params.session_id ?? runtime.sessionId,
        project: params.project ?? runtime.project,
        title: params.title,
        content: params.content,
        scope: params.scope ?? "project",
        type: params.type ?? "manual",
        topic_key: params.topic_key,
      })
      return summarizeToolResult("mem_save", result)
    },
  })

  pi.registerTool({
    name: "mem_session_summary",
    label: "Engram Session Summary",
    description: "Persist end-of-session summary for continuity.",
    parameters: Type.Object({
      content: Type.String({ minLength: 1 }),
      session_id: Type.Optional(Type.String()),
    }),
    promptSnippet: "Call mem_session_summary before ending work or after compaction.",
    promptGuidelines: ["Use mem_session_summary by name before saying done.", ...TOOL_GUIDELINES],
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      void toolCallId
      void signal
      void onUpdate
      const ready = await ensureBackend()
      if (!ready.ok) {
        return backendFailureToolResult("mem_session_summary", ready)
      }
      const runtime = deriveRuntime(ctx)
      const sessionId = redactPrivateTags(String(params.session_id ?? runtime.sessionId))
      if (!sessionId) {
        return failureToolResult("Engram request failed for mem_session_summary.", {
          path: "/sessions/{id}/end",
          error: "missing_session_id",
        })
      }

      const result = await postJSON(`/sessions/${encodeURIComponent(sessionId)}/end`, {
        summary: params.content,
      })
      return summarizeToolResult("mem_session_summary", result)
    },
  })

  pi.registerTool({
    name: "mem_get_observation",
    label: "Engram Get Observation",
    description: "Get a full observation by ID.",
    parameters: Type.Object({
      id: Type.Number({ minimum: 1 }),
    }),
    promptSnippet: "Call mem_get_observation for full untruncated memory content.",
    promptGuidelines: ["Use mem_get_observation by name after mem_search for full details.", ...TOOL_GUIDELINES],
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      void toolCallId
      void signal
      void onUpdate
      void ctx
      const ready = await ensureBackend()
      if (!ready.ok) {
        return backendFailureToolResult("mem_get_observation", ready)
      }
      const result = await engramFetch(`/observations/${encodeURIComponent(String(params.id))}`)
      return summarizeToolResult("mem_get_observation", result)
    },
  })

  pi.registerTool({
    name: "mem_save_prompt",
    label: "Engram Save Prompt",
    description: "Persist user prompt text to Engram for history.",
    parameters: Type.Object({
      content: Type.String({ minLength: 1 }),
      project: Type.Optional(Type.String()),
      session_id: Type.Optional(Type.String()),
    }),
    promptSnippet: "Call mem_save_prompt when explicit prompt archival is needed.",
    promptGuidelines: ["Use mem_save_prompt by name only for prompt capture use-cases.", ...TOOL_GUIDELINES],
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      void toolCallId
      void signal
      void onUpdate
      const ready = await ensureBackend()
      if (!ready.ok) {
        return backendFailureToolResult("mem_save_prompt", ready)
      }
      const runtime = deriveRuntime(ctx)
      const result = await postJSON("/prompts", {
        session_id: params.session_id ?? runtime.sessionId,
        project: params.project ?? runtime.project,
        content: params.content,
      })
      return summarizeToolResult("mem_save_prompt", result)
    },
  })
}

function registerRecoveryCommand(pi: any): void {
  if (typeof pi.registerCommand !== "function") {
    return
  }

  pi.registerCommand("engram-recovery", {
    description: "Shows the compacted-session recovery instructions for Engram memory protocol.",
    handler: (_args: unknown, ctx: any) => {
      if (ctx?.hasUI && ctx?.ui?.notify) {
        ctx.ui.notify(COMPACTION_RECOVERY_NOTICE, "info")
      }
      return COMPACTION_RECOVERY_NOTICE
    },
  })
}

function compactionInstruction(): string {
  return COMPACTION_RECOVERY_NOTICE
}

function prependInstruction(target: unknown, instruction: string): boolean {
  if (Array.isArray(target)) {
    target.unshift(instruction)
    return true
  }

  if (target && typeof target === "object") {
    const record = target as Record<string, unknown>
    if (typeof record.value === "string") {
      record.value = `${instruction}\n${record.value}`.trim()
      return true
    }
  }

  return false
}

function injectCompactionInstruction(event: any): boolean {
  const instruction = compactionInstruction()

  if (event && Array.isArray(event.customInstructions)) {
    event.customInstructions.unshift(instruction)
    return true
  }

  if (event && typeof event.customInstructions === "string") {
    event.customInstructions = `${instruction}\n${event.customInstructions}`.trim()
    return true
  }

  if (event && event.customInstructions && typeof event.customInstructions === "object") {
    if (Array.isArray(event.customInstructions.items)) {
      event.customInstructions.items.unshift(instruction)
      return true
    }
  }

  if (event?.compactionEntry && Array.isArray(event.compactionEntry.customInstructions)) {
    event.compactionEntry.customInstructions.unshift(instruction)
    return true
  }

  if (event?.compactionEntry && prependInstruction(event.compactionEntry.customInstructions, instruction)) {
    return true
  }

  if (event?.compaction && Array.isArray(event.compaction.customInstructions)) {
    event.compaction.customInstructions.unshift(instruction)
    return true
  }

  if (event?.compaction && prependInstruction(event.compaction.customInstructions, instruction)) {
    return true
  }

  return false
}

function extractSummaryText(value: unknown): string {
  if (typeof value === "string") {
    return redactPrivateTags(value).trim()
  }

  if (!value || typeof value !== "object") {
    return ""
  }

  const candidates = [
    "summary",
    "content",
    "text",
    "compactedSummary",
    "compactSummary",
    "finalSummary",
    "entry",
  ]

  for (const key of candidates) {
    const raw = (value as Record<string, unknown>)[key]
    if (typeof raw === "string") {
      const clean = redactPrivateTags(raw).trim()
      if (clean) return clean
    }
    if (raw && typeof raw === "object") {
      const nested = extractSummaryText(raw)
      if (nested) return nested
    }
  }

  return ""
}

function extractCompactionSummary(event: any): string {
  const candidates: unknown[] = [
    event?.compactionEntry,
    event?.compaction,
    event?.summary,
    event?.compactedSummary,
    event?.compactSummary,
    event?.content,
    event?.text,
    event,
  ]

  for (const candidate of candidates) {
    const summary = extractSummaryText(candidate)
    if (summary) return summary
  }

  return ""
}

function notifyCompactionRecovery(ctx: any): void {
  const recoveryInstruction = compactionInstruction()

  if (ctx?.hasUI && ctx?.ui?.notify) {
    ctx.ui.notify(recoveryInstruction, "info")
  }
}

async function persistCompactionSummary(event: any, ctx: any): Promise<boolean> {
  const runtime = deriveRuntime(ctx)
  const compactedSummary = extractCompactionSummary(event)
  if (!runtime.sessionId || !compactedSummary) {
    return false
  }

  const result = await postJSON(`/sessions/${encodeURIComponent(runtime.sessionId)}/end`, {
    summary: compactedSummary,
  })

  return Boolean(result.ok)
}

function notifyCompactionResult(saved: boolean, ctx: any): void {
  if (!ctx?.hasUI || !ctx?.ui?.notify) {
    return
  }

  if (saved) {
    ctx.ui.notify(COMPACTION_SAVED_NOTICE, "info")
    return
  }

  ctx.ui.notify(COMPACTION_UNAVAILABLE_NOTICE, "info")
}

export default function (pi: ExtensionAPI): void {
  registerMemoryTools(pi)
  registerRecoveryCommand(pi)

  pi.on("session_start", async (event, ctx) => {
    const ready = await ensureBackend()
    if (!ready.ok) return
    const runtime = deriveRuntime(ctx)
    const reason = redactPrivateTags(String(event.reason ?? ""))

    if (runtime.sessionId && runtime.project) {
      await postJSON("/sessions", {
        id: runtime.sessionId,
        project: runtime.project,
        directory: runtime.directory,
        reason,
      })
    }

    if (!runtime.project) return
    const result = await engramFetch(
      `/observations/recent${queryString({ project: runtime.project, scope: "project", limit: 1 })}`,
    )
    const observations = observationsFromRecentResult(result)

    if (observations.length > 0 && ctx.hasUI && ctx.ui?.notify) {
      ctx.ui.notify(STARTUP_NOTICE, "info")
    }
  })

  pi.on("session_shutdown", async (event, ctx) => {
    const ready = await ensureBackend()
    if (!ready.ok) return
    const runtime = deriveRuntime(ctx)
    const reason = redactPrivateTags(String(event.reason ?? ""))
    const target = redactPrivateTags(String(event.target ?? ""))

    if (!runtime.sessionId) return
    await postJSON(`/sessions/${encodeURIComponent(runtime.sessionId)}/end`, {
      summary: `shutdown reason=${reason} target=${target}`,
    })
  })

  pi.on("input", async (event, ctx) => {
    if (event.source === "extension") {
      return
    }

    const ready = await ensureBackend()
    if (!ready.ok) return
    const runtime = deriveRuntime(ctx)
    if (!runtime.sessionId || !runtime.project) return

    await postJSON("/prompts", {
      session_id: runtime.sessionId,
      project: runtime.project,
      content: String(event.text ?? ""),
      source: String(event.source ?? ""),
      images: event.images,
    })
  })

  pi.on("session_before_compact", async (event, ctx) => {
    const ready = await ensureBackend()
    if (!ready.ok) {
      notifyCompactionRecovery(ctx)
      return
    }
    if (!injectCompactionInstruction(event)) {
      notifyCompactionRecovery(ctx)
    }
  })

  pi.on("session_compact", async (event, ctx) => {
    const ready = await ensureBackend()
    if (!ready.ok) {
      notifyCompactionRecovery(ctx)
      return
    }

    const saved = await persistCompactionSummary(event, ctx)
    notifyCompactionResult(saved, ctx)
    if (!saved) {
      notifyCompactionRecovery(ctx)
    }
  })
}

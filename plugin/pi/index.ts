/**
 * Engram — Pi extension adapter
 *
 * Thin adapter that connects Pi session events to an Engram HTTP server.
 * Persistence remains owned by the Engram Go binary (`engram serve`). MCP tools
 * are configured separately through pi-mcp-adapter and `engram mcp`.
 */

import { spawn, type ChildProcess } from "node:child_process";
import { existsSync } from "node:fs";
import { basename, resolve } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Text } from "@earendil-works/pi-tui";
import { Type } from "typebox";
import { buildRecoveryNotice, extractCompactedSummary } from "./compaction-recovery.js";
import { compactResultStatus, humanToolName, renderCallText, renderResultText } from "./memory-tool-chrome.js";
import { redactPrivateTags, redactUrlPath, redactValue } from "./private-redaction.js";

const ENGRAM_PORT = Number.parseInt(process.env.ENGRAM_PORT ?? "7437", 10);
const CONFIGURED_ENGRAM_URL = process.env.ENGRAM_URL?.trim() || undefined;
const ENGRAM_URL = CONFIGURED_ENGRAM_URL || `http://127.0.0.1:${ENGRAM_PORT}`;
const ENGRAM_BIN = process.env.ENGRAM_BIN ?? "engram";

const ENGRAM_TOOLS = [
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
  "mem_current_project",
  "mem_doctor",
  "mem_capture_passive",
  "mem_judge",
  "mem_compare",
] as const;

const ENGRAM_TOOL_NAMES = new Set<string>(ENGRAM_TOOLS);

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
- **title**: Verb + what — short, searchable
- **type**: bugfix | decision | architecture | discovery | pattern | config | preference
- **scope**: \`project\` (default) | \`personal\`
- **topic_key**: stable key for evolving decisions when relevant
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you

### WHEN TO SEARCH MEMORY

When the user asks to recall past work, first call \`mem_context\`. If not found,
call \`mem_search\`, then \`mem_get_observation\` for full content.

### SESSION CLOSE PROTOCOL

Before ending a session or saying "done", call \`mem_session_summary\`
with Goal, Instructions, Discoveries, Accomplished, Next Steps, and Relevant Files.

### AFTER COMPACTION

If you see "FIRST ACTION REQUIRED" or a compacted summary, save it immediately
with \`mem_session_summary\`, then call \`mem_context\` before continuing.
`;

interface FetchOptions {
  method?: string;
  body?: unknown;
}

interface SessionBody {
  id: string;
  project: string;
  directory: string;
}

interface PromptBody {
  session_id: string;
  content: string;
  project: string;
}

interface PassiveCaptureBody {
  session_id: string;
  content: string;
  project: string;
  source: string;
}

interface MigrationBody {
  old_project: string;
  new_project: string;
}

interface CurrentProjectResponse {
  project?: string;
  available_projects?: string[] | null;
  warning?: string;
  error_hint?: string;
}

interface ContextResponse {
  context?: string;
}

interface SessionContext {
  cwd: string;
  sessionManager: {
    getSessionId(): string | undefined;
  };
}

interface AgentStartEvent {
  systemPrompt: string;
  prompt?: string;
}

interface ToolEndEvent {
  toolName?: string;
  result?: unknown;
}

class EngramHttpError extends Error {
  constructor(message: string, readonly status: number, readonly data: unknown) {
    super(message);
    this.name = "EngramHttpError";
  }
}

async function engramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  let res: Response;
  try {
    res = await fetch(`${ENGRAM_URL}${redactUrlPath(path)}`, {
      method: opts.method ?? "GET",
      headers: opts.body ? { "Content-Type": "application/json" } : undefined,
      body: opts.body ? JSON.stringify(redactValue(opts.body)) : undefined,
    });
  } catch {
    return null;
  }

  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    data = null;
  }

  if (!res.ok) {
    const message = data && typeof data === "object" && "error" in data && typeof data.error === "string"
      ? data.error
      : `Engram request failed with HTTP ${res.status}`;
    throw new EngramHttpError(message, res.status, data);
  }

  return data as TResponse;
}

async function bestEffortEngramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  try {
    return await engramFetch<TResponse>(path, opts);
  } catch {
    return null;
  }
}

async function ensureSessionBestEffort(sessionId: string, sessionProject = project): Promise<void> {
  try {
    await ensureSession(sessionId, sessionProject);
  } catch {}
}

async function isEngramRunning(): Promise<boolean> {
  try {
    const res = await fetch(`${ENGRAM_URL}/health`, {
      signal: AbortSignal.timeout(500),
    });
    return res.ok;
  } catch {
    return false;
  }
}

function rawBasenameProjectName(directory: string): string {
  const resolved = resolve(directory || ".");
  return basename(resolved).trim() || "unknown";
}

function fallbackProjectName(directory: string): string {
  return rawBasenameProjectName(directory).toLowerCase();
}

function truncate(str: string, max: number): string {
  return str.length > max ? `${str.slice(0, max)}...` : str;
}

function stripPrivateTags(str: string): string {
  return redactPrivateTags(str).trim();
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function spawnDetached(command: string, args: readonly string[], cwd?: string): Promise<boolean> {
  return new Promise((resolve) => {
    let proc: ChildProcess;
    try {
      proc = spawn(command, [...args], {
        cwd,
        detached: true,
        stdio: "ignore",
      });
    } catch {
      resolve(false);
      return;
    }

    let settled = false;
    const settle = (started: boolean) => {
      if (settled) return;
      settled = true;
      resolve(started);
    };

    proc.once("error", () => settle(false));
    proc.once("spawn", () => {
      proc.unref();
      settle(true);
    });
  });
}

let initialized = false;
let project = "unknown";
let directory = "";
let pendingRecoveryNotice: string | undefined;
let projectResolutionError: string | undefined;
let projectDetectionPending = false;

const knownSessions = new Set<string>();
const toolCounts = new Map<string, number>();

async function ensureSession(sessionId: string, sessionProject = project): Promise<void> {
  const key = `${sessionProject}:${sessionId}`;
  if (!sessionId || knownSessions.has(key)) return;
  knownSessions.add(key);
  const body: SessionBody = { id: sessionId, project: sessionProject, directory };
  await engramFetch("/sessions", { method: "POST", body });
}

async function detectServerProject(cwd: string): Promise<CurrentProjectResponse | undefined> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    const detected = await bestEffortEngramFetch<CurrentProjectResponse>(`/project/current${queryString({ cwd })}`);
    if (detected) return detected;
    if (attempt < 4) await wait(200);
  }
  return undefined;
}

function applyDetectedProject(detected: CurrentProjectResponse | undefined): boolean {
  if (!detected) {
    projectDetectionPending = true;
    return false;
  }
  projectDetectionPending = false;
  if (detected.project) {
    project = detected.project;
    projectResolutionError = undefined;
    return true;
  }
  const choices = detected.available_projects?.length ? ` Available projects: ${detected.available_projects.join(", ")}.` : "";
  projectResolutionError = detected.error_hint || detected.warning || `Engram project detection did not resolve a project.${choices}`;
  return false;
}

async function refreshProjectDetection(cwd: string): Promise<void> {
  if (!projectDetectionPending && !projectResolutionError) return;
  applyDetectedProject(await detectServerProject(cwd));
}

function forgetKnownSession(sessionId: string): void {
  knownSessions.delete(sessionId);
  for (const key of knownSessions) {
    if (key.endsWith(`:${sessionId}`)) knownSessions.delete(key);
  }
}

function requireResolvedProject(): void {
  if (projectResolutionError) throw new Error(projectResolutionError);
  if (projectDetectionPending) throw new Error("Engram project detection is unavailable; cannot safely choose a project");
}

async function initOnce(cwd: string): Promise<void> {
  if (initialized) return;
  initialized = true;
  directory = cwd;

  const oldProject = rawBasenameProjectName(cwd);
  project = fallbackProjectName(cwd);

  const running = await isEngramRunning();
  if (!running && CONFIGURED_ENGRAM_URL === undefined) {
    await spawnDetached(ENGRAM_BIN, ["serve"]);
    await wait(500);
  }

  applyDetectedProject(await detectServerProject(cwd));

  const migrationSources = new Set([oldProject, fallbackProjectName(cwd)]);
  for (const sourceProject of migrationSources) {
    if (sourceProject !== project) {
      const body: MigrationBody = { old_project: sourceProject, new_project: project };
      await bestEffortEngramFetch("/projects/migrate", { method: "POST", body });
    }
  }

  const manifestFile = `${cwd}/.engram/manifest.json`;
  if (existsSync(manifestFile)) {
    await spawnDetached(ENGRAM_BIN, ["sync", "--import"], cwd);
  }
}

function getSessionId(ctx: SessionContext): string | undefined {
  return ctx.sessionManager.getSessionId();
}

const optionalString = (description: string) => Type.Optional(Type.String({ description }));
const optionalNumber = (description: string) => Type.Optional(Type.Number({ description }));
const optionalBoolean = (description: string) => Type.Optional(Type.Boolean({ description }));

const MEMORY_TOOL_SCHEMAS: Record<string, ReturnType<typeof Type.Object>> = {
  mem_search: Type.Object({
    query: Type.String({ description: "Search query — natural language or keywords" }),
    type: optionalString("Filter by observation type"),
    project: optionalString("Filter by project name"),
    scope: optionalString("Filter by scope: project or personal"),
    limit: optionalNumber("Max results"),
  }),
  mem_save: Type.Object({
    title: Type.String({ description: "Short, searchable title" }),
    content: Type.String({ description: "Structured memory content" }),
    type: optionalString("Observation type/category"),
    session_id: optionalString("Session ID to associate with"),
    scope: optionalString("Scope: project or personal"),
    topic_key: optionalString("Stable topic key for upserts"),
    project: optionalString("Optional explicit project"),
    capture_prompt: optionalBoolean("Capture current prompt when available"),
  }),
  mem_update: Type.Object({
    id: Type.Number({ description: "Observation ID to update" }),
    title: optionalString("New title"),
    content: optionalString("New content"),
    type: optionalString("New type/category"),
    scope: optionalString("New scope"),
    topic_key: optionalString("New topic key"),
  }),
  mem_delete: Type.Object({
    id: Type.Number({ description: "Observation ID to delete" }),
    hard_delete: optionalBoolean("Permanently delete the observation"),
  }),
  mem_suggest_topic_key: Type.Object({
    type: optionalString("Observation type/category"),
    title: optionalString("Observation title"),
    content: optionalString("Observation content"),
  }),
  mem_save_prompt: Type.Object({
    content: Type.String({ description: "The user's prompt text" }),
    session_id: optionalString("Session ID to associate with"),
    project: optionalString("Optional project"),
  }),
  mem_session_summary: Type.Object({
    content: Type.String({ description: "Full session summary" }),
    session_id: optionalString("Session ID"),
  }),
  mem_context: Type.Object({
    project: optionalString("Filter by project"),
    scope: optionalString("Filter observations by scope"),
  }),
  mem_stats: Type.Object({
    project: optionalString("Project to echo in UI chrome"),
  }),
  mem_timeline: Type.Object({
    observation_id: Type.Number({ description: "Observation ID to center on" }),
    before: optionalNumber("Number of observations before"),
    after: optionalNumber("Number of observations after"),
    project: optionalString("Filter by project name"),
  }),
  mem_get_observation: Type.Object({
    id: Type.Number({ description: "Observation ID to retrieve" }),
  }),
  mem_session_start: Type.Object({
    id: Type.String({ description: "Unique session identifier" }),
    directory: optionalString("Working directory"),
  }),
  mem_session_end: Type.Object({
    id: Type.String({ description: "Session identifier to close" }),
    summary: optionalString("Summary of what was accomplished"),
  }),
  mem_current_project: Type.Object({
    cwd: optionalString("Working directory to inspect; defaults to Engram server cwd"),
  }),
  mem_doctor: Type.Object({
    check: optionalString("Optional diagnostic check code to run"),
    project: optionalString("Project to diagnose; defaults to current project"),
  }),
  mem_capture_passive: Type.Object({
    content: Type.String({ description: "Text output containing a ## Key Learnings section" }),
    session_id: optionalString("Session ID to associate with"),
    source: optionalString("Source identifier, e.g. subagent-stop or session-end"),
  }),
  mem_judge: Type.Object({
    judgment_id: Type.String({ description: "The relation judgment_id returned by mem_save candidates" }),
    relation: Type.String({ description: "Verdict: related | compatible | scoped | conflicts_with | supersedes | not_conflict" }),
    reason: optionalString("Free-text explanation of the verdict"),
    evidence: optionalString("Supporting evidence as JSON or text"),
    confidence: optionalNumber("Confidence score 0.0..1.0"),
    session_id: optionalString("Session ID for provenance"),
  }),
  mem_compare: Type.Object({
    memory_id_a: Type.Number({ description: "Integer id of the first observation" }),
    memory_id_b: Type.Number({ description: "Integer id of the second observation" }),
    relation: Type.String({ description: "Verdict: related | compatible | scoped | conflicts_with | supersedes | not_conflict" }),
    confidence: Type.Number({ description: "Confidence score 0.0..1.0" }),
    reasoning: Type.String({ description: "Brief explanation of the verdict" }),
    model: optionalString("Model identifier for provenance"),
  }),
};

function queryString(params: Record<string, unknown>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    query.set(key, String(value));
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

function textResult(data: unknown): string {
  if (typeof data === "string") return data;
  if (data && typeof data === "object" && "context" in data && typeof (data as ContextResponse).context === "string") {
    return (data as ContextResponse).context || "(empty context)";
  }
  return JSON.stringify(data ?? {}, null, 2);
}

function slugifyTopicKey(params: Record<string, unknown>): string {
  const source = String(params.title || params.content || params.type || "memory");
  const slug = source
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
  return slug || "memory";
}

async function callMemoryTool(toolName: string, params: Record<string, unknown>, ctx: SessionContext): Promise<unknown> {
  const sessionId = getSessionId(ctx);
  const requestedProject = typeof params.project === "string" && params.project ? params.project : undefined;
  const activeProject = requestedProject || project;
  const activeSessionId = String(params.session_id || (requestedProject ? `manual-save-${requestedProject}` : sessionId) || `manual-save-${project}`);

  switch (toolName) {
    case "mem_search":
      return engramFetch(`/search${queryString({ q: params.query, type: params.type, project: params.project, scope: params.scope, limit: params.limit })}`);
    case "mem_context":
      if (!params.project) requireResolvedProject();
      return engramFetch(`/context${queryString({ project: params.project || project, scope: params.scope })}`);
    case "mem_stats":
      return engramFetch("/stats");
    case "mem_timeline":
      return engramFetch(`/timeline${queryString({ observation_id: params.observation_id, before: params.before, after: params.after, project: params.project })}`);
    case "mem_get_observation":
      return engramFetch(`/observations/${encodeURIComponent(String(params.id))}`);
    case "mem_save":
      if (!requestedProject) requireResolvedProject();
      await ensureSession(activeSessionId, activeProject);
      return engramFetch("/observations", {
        method: "POST",
        body: {
          session_id: activeSessionId,
          title: params.title,
          content: params.content,
          type: params.type || "manual",
          project: activeProject,
          scope: params.scope || "project",
          topic_key: params.topic_key,
        },
      });
    case "mem_update":
      return engramFetch(`/observations/${encodeURIComponent(String(params.id))}`, {
        method: "PATCH",
        body: {
          title: params.title,
          content: params.content,
          type: params.type,
          scope: params.scope,
          topic_key: params.topic_key,
        },
      });
    case "mem_delete":
      return engramFetch(`/observations/${encodeURIComponent(String(params.id))}${queryString({ hard: params.hard_delete })}`, { method: "DELETE" });
    case "mem_suggest_topic_key":
      return { topic_key: slugifyTopicKey(params) };
    case "mem_save_prompt":
      if (!requestedProject) requireResolvedProject();
      await ensureSession(activeSessionId, activeProject);
      return engramFetch("/prompts", {
        method: "POST",
        body: { session_id: activeSessionId, content: params.content, project: activeProject },
      });
    case "mem_session_summary":
      requireResolvedProject();
      await ensureSession(activeSessionId);
      return engramFetch("/observations", {
        method: "POST",
        body: {
          session_id: activeSessionId,
          type: "session_summary",
          title: "Session summary",
          content: params.content,
          project,
          scope: "project",
        },
      });
    case "mem_session_start":
      requireResolvedProject();
      return engramFetch("/sessions", {
        method: "POST",
        body: { id: params.id, project, directory: params.directory || directory || ctx.cwd },
      });
    case "mem_session_end":
      return engramFetch(`/sessions/${encodeURIComponent(String(params.id))}/end`, {
        method: "POST",
        body: { summary: params.summary || "" },
      });
    case "mem_current_project":
      return engramFetch(`/project/current${queryString({ cwd: params.cwd || ctx.cwd })}`);
    case "mem_doctor":
      return engramFetch(`/doctor${queryString({ project: params.project, check: params.check, cwd: params.project ? undefined : ctx.cwd })}`);
    case "mem_capture_passive":
      requireResolvedProject();
      await ensureSession(activeSessionId);
      return engramFetch("/observations/passive", {
        method: "POST",
        body: {
          session_id: activeSessionId,
          content: params.content,
          project,
          source: params.source || "pi-tool",
        },
      });
    case "mem_judge":
      return engramFetch("/conflicts/judge", {
        method: "POST",
        body: {
          judgment_id: params.judgment_id,
          relation: params.relation,
          reason: params.reason,
          evidence: params.evidence,
          confidence: params.confidence,
          session_id: params.session_id || sessionId,
        },
      });
    case "mem_compare":
      return engramFetch("/conflicts/compare", {
        method: "POST",
        body: {
          memory_id_a: params.memory_id_a,
          memory_id_b: params.memory_id_b,
          relation: params.relation,
          confidence: params.confidence,
          reasoning: params.reasoning,
          model: params.model,
        },
      });
    default:
      throw new Error(`Unsupported Engram memory tool: ${toolName}`);
  }
}

async function executeMemoryTool(toolName: string, params: Record<string, unknown>, ctx: SessionContext & { hasUI?: boolean; ui?: { setStatus?: (key: string, text: string | undefined) => void } }) {
  await initOnce(ctx.cwd);
  await refreshProjectDetection(ctx.cwd);
  const action = humanToolName(toolName);
  ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${action}…`);

  try {
    const data = await callMemoryTool(toolName, params, ctx);
    if (data === null) throw new Error("Engram is unavailable");
    const result = { content: [{ type: "text" as const, text: textResult(data) }], details: { data } };
    if (toolName === "mem_doctor" && data && typeof data === "object" && "status" in data && data.status === "error") {
      const errorResult = { ...result, isError: true };
      ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${compactResultStatus(toolName, errorResult)}`);
      return errorResult;
    }
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${compactResultStatus(toolName, result)}`);
    return result;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    const details = error instanceof EngramHttpError
      ? { error: message, http_status: error.status, data: error.data }
      : { error: message };
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · error`);
    return { content: [{ type: "text" as const, text: message }], details, isError: true };
  }
}

function registerMemoryTools(pi: ExtensionAPI): void {
  for (const toolName of ENGRAM_TOOLS) {
    pi.registerTool({
      name: toolName,
      label: `Engram: ${humanToolName(toolName)}`,
      description: `Engram memory tool: ${humanToolName(toolName)}. Compact UI is provided by gentle-engram; persistence is handled by Engram when installed and running.`,
      promptSnippet: `Engram memory: ${humanToolName(toolName)}`,
      parameters: MEMORY_TOOL_SCHEMAS[toolName],
      renderShell: "self",
      async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
        return executeMemoryTool(toolName, params as Record<string, unknown>, ctx as SessionContext & { hasUI?: boolean; ui?: { setStatus?: (key: string, text: string | undefined) => void } });
      },
      renderCall(args) {
        return new Text(renderCallText(toolName, args), 0, 0);
      },
      renderResult(result, options, _theme, context) {
        return new Text(renderResultText(toolName, result, { expanded: options.expanded, isPartial: options.isPartial, isError: context.isError }), 0, 0);
      },
    });
  }
}

export default function registerEngram(pi: ExtensionAPI) {
  registerMemoryTools(pi);
  pi.on("session_start", async (_event: unknown, ctx: SessionContext) => {
    await initOnce(ctx.cwd);
  });

  pi.on("session_shutdown", async (_event: unknown, ctx: SessionContext) => {
    const sessionId = getSessionId(ctx);
    if (!sessionId) return;
    toolCounts.delete(sessionId);
    forgetKnownSession(sessionId);
  });

  pi.on("session_compact", async (event: unknown, ctx: SessionContext) => {
    await initOnce(ctx.cwd);
    await refreshProjectDetection(ctx.cwd);
    if (projectDetectionPending || projectResolutionError) return;
    const sessionId = getSessionId(ctx);
    if (sessionId) await ensureSessionBestEffort(sessionId);

    const summary = extractCompactedSummary(event);
    if (sessionId && summary) {
      await bestEffortEngramFetch("/observations", {
        method: "POST",
        body: {
          session_id: sessionId,
          type: "session_summary",
          title: "Compaction recovery summary",
          content: summary,
          project,
          scope: "project",
          topic_key: "session/compaction-recovery",
        },
      });
    }

    const data = await bestEffortEngramFetch<ContextResponse>(`/context?project=${encodeURIComponent(project)}`);
    pendingRecoveryNotice = buildRecoveryNotice(project, data?.context);
  });

  pi.on("before_agent_start", async (event: AgentStartEvent, ctx: SessionContext) => {
    await initOnce(ctx.cwd);
    await refreshProjectDetection(ctx.cwd);
    const sessionId = getSessionId(ctx);
    let systemPrompt = event.systemPrompt.length > 0 ? `${event.systemPrompt}\n\n${MEMORY_INSTRUCTIONS}` : MEMORY_INSTRUCTIONS;

    if (pendingRecoveryNotice !== undefined) {
      systemPrompt = `${systemPrompt}\n\n${pendingRecoveryNotice}`;
      pendingRecoveryNotice = undefined;
    }

    const finalContent = event.prompt?.trim();
    if ((projectDetectionPending || projectResolutionError) && sessionId && finalContent && finalContent.length > 10) {
      return { systemPrompt };
    }
    if (sessionId && finalContent && finalContent.length > 10) {
      await ensureSessionBestEffort(sessionId);
      const body: PromptBody = {
        session_id: sessionId,
        content: stripPrivateTags(truncate(finalContent, 2000)),
        project,
      };
      await bestEffortEngramFetch("/prompts", { method: "POST", body });
    }

    return { systemPrompt };
  });

  pi.on("tool_execution_end", async (event: ToolEndEvent, ctx: SessionContext) => {
    const toolName = event.toolName ?? "";
    if (ENGRAM_TOOL_NAMES.has(toolName.toLowerCase())) return;

    await initOnce(ctx.cwd);
    await refreshProjectDetection(ctx.cwd);
    const sessionId = getSessionId(ctx);
    if (!sessionId || projectDetectionPending || projectResolutionError) return;

    await ensureSessionBestEffort(sessionId);
    toolCounts.set(sessionId, (toolCounts.get(sessionId) ?? 0) + 1);

    if (toolName !== "Task" || event.result === undefined) return;
    const content = typeof event.result === "string" ? event.result : JSON.stringify(event.result);
    if (content.length <= 50) return;

    const body: PassiveCaptureBody = {
      session_id: sessionId,
      content: stripPrivateTags(content),
      project,
      source: "task-complete",
    };
    await bestEffortEngramFetch("/observations/passive", { method: "POST", body });
  });
}

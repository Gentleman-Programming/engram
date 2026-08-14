/**
 * Engram — Pi extension adapter
 *
 * Thin adapter that connects Pi session events to an Engram HTTP server.
 * Persistence remains owned by the Engram Go binary (`engram serve`). MCP tools
 * are configured separately through pi-mcp-adapter and `engram mcp`.
 */

import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { basename, dirname, resolve } from "node:path";
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

const ENGRAM_FETCH_TIMEOUT_MS = 3000;
const ENGRAM_FETCH_MAX_ATTEMPTS = 3;
const ENGRAM_FETCH_BACKOFF_BASE_MS = 250;
const ENGRAM_SELF_HEAL_INTERVAL_MS = 5000;
const ENGRAM_SELF_HEAL_MAX_ATTEMPTS = 6;

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
  "mem_review",
  "mem_judge",
  "mem_compare",
] as const;

const ENGRAM_TOOL_NAMES = new Set<string>(ENGRAM_TOOLS);

const MEMORY_INSTRUCTIONS = `## Engram Persistent Memory — Protocol

You have access to Engram, a persistent memory system that survives across sessions and compactions.
These instructions are injected by gentle-engram, the Pi-native memory provider. Use the memory tools named in this section as the authoritative Pi memory contract. Do not infer alternative Engram tool names from other integrations unless the user explicitly asks you to use them.

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
If \`mem_session_summary\` fails because Engram cannot detect a project, ask the user
which project should receive the summary, then retry with \`project: "<name>"\`.

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
  project_source?: string;
  project_path?: string;
  cwd?: string;
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

type MemoryToolContext = SessionContext & {
  hasUI?: boolean;
  ui?: { setStatus?: (key: string, text: string | undefined) => void };
};

interface AgentStartEvent {
  systemPrompt: string;
  prompt?: string;
}

interface ToolEndEvent {
  toolName?: string;
  result?: unknown;
}

class EngramHttpError extends Error {
  readonly status: number;
  readonly data: unknown;

  constructor(message: string, status: number, data: unknown) {
    super(message);
    this.name = "EngramHttpError";
    this.status = status;
    this.data = data;
  }
}

// Node rejects an AbortSignal.timeout() fetch with a DOMException named "TimeoutError",
// which is an instanceof Error; a caller-supplied abort surfaces as "AbortError".
function isTimeoutError(error: unknown): boolean {
  return error instanceof Error && (error.name === "TimeoutError" || error.name === "AbortError");
}

// engramFetch resolves to null on failure and ~20 call sites depend on that fallthrough —
// ensureSession in particular must not abort a mem_save just because session creation blipped.
// So the timeout detail travels out-of-band instead of changing what any caller receives,
// letting executeMemoryTool tell the truth about an ambiguous write without blast radius.
let lastFetchTimeoutMethod: string | undefined;

function takeLastFetchTimeoutMethod(): string | undefined {
  const method = lastFetchTimeoutMethod;
  lastFetchTimeoutMethod = undefined;
  return method;
}

async function engramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  const method = opts.method ?? "GET";
  // This call's outcome supersedes any earlier one. A tool call can issue several fetches
  // (mem_save creates the session, then writes the observation); without this reset a timeout
  // on the first leg would mislabel an unrelated failure on the second as "may already have
  // been applied", telling the agent not to retry a write that never left the machine.
  lastFetchTimeoutMethod = undefined;
  let res: Response | undefined;
  let timedOut = false;
  for (let attempt = 0; attempt < ENGRAM_FETCH_MAX_ATTEMPTS; attempt += 1) {
    try {
      res = await fetch(`${ENGRAM_URL}${redactUrlPath(path)}`, {
        method,
        headers: opts.body ? { "Content-Type": "application/json" } : undefined,
        body: opts.body ? JSON.stringify(redactValue(opts.body)) : undefined,
        signal: AbortSignal.timeout(ENGRAM_FETCH_TIMEOUT_MS),
      });
      break;
    } catch (error) {
      // A timeout means the request may already have reached the server, so re-sending it
      // could duplicate a non-idempotent write (mem_save and friends carry no idempotency
      // key). Only pre-send connection failures — the macOS wake-settle case this retry
      // exists for — are safe to repeat, and a hung server will not recover by retrying.
      if (isTimeoutError(error)) {
        timedOut = true;
        break;
      }
      if (attempt < ENGRAM_FETCH_MAX_ATTEMPTS - 1) await wait(ENGRAM_FETCH_BACKOFF_BASE_MS * 2 ** attempt);
    }
  }

  // A timeout is NOT the same failure as an unreachable server, and reporting both as "could
  // not reach" invites the caller to retry a write whose outcome is genuinely unknown. Record
  // which it was so the tool layer can say what we do and do not know.
  if (timedOut) lastFetchTimeoutMethod = method;
  if (!res) return null;

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

function detectLocalConfigProject(cwd: string): CurrentProjectResponse | undefined {
  let current = resolve(cwd || ".");
  while (true) {
    const configPath = `${current}/.engram/config.json`;
    if (existsSync(configPath)) {
      try {
        const parsed = JSON.parse(readFileSync(configPath, "utf8")) as { project_name?: unknown };
        const projectName = typeof parsed.project_name === "string" ? parsed.project_name.trim() : "";
        if (projectName) {
          return {
            project: projectName,
            project_source: "config",
            project_path: current,
            cwd,
            warning: `Engram server at ${ENGRAM_URL} does not support /project/current; using ${configPath}. Upgrade or restart Engram for canonical project detection.`,
          };
        }
        return {
          cwd,
          error_hint: `${configPath} exists but project_name is missing or empty. Fix the config or pass project explicitly.`,
        };
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        return { cwd, error_hint: `Could not read ${configPath}: ${message}` };
      }
    }

    const parent = dirname(current);
    if (parent === current) return undefined;
    current = parent;
  }
}

function projectCurrentUnsupportedError(cwd: string): CurrentProjectResponse {
  return {
    cwd,
    error_hint: `Engram server at ${ENGRAM_URL} does not support /project/current. Upgrade or restart the running Engram server, verify ENGRAM_URL/ENGRAM_BIN, or pass project explicitly to project-capable memory tools.`,
  };
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

function waitUnref(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    timer.unref?.();
  });
}

let engramSelfHealInFlight = false;
// Keyed by session so a session that fails repeatedly is tracked once, and so a session that
// shuts down mid-outage can be dropped instead of having its torn-down UI touched later.
const engramSelfHealContexts = new Map<string | MemoryToolContext, MemoryToolContext>();

// Pruning is by session id. A context with no resolvable session id is keyed by the ctx
// object itself and cannot be pruned here, but it self-expires: the probe clears the whole
// map within one cycle, and setStatus is optional-chained, so a torn-down UI is never a crash.
function forgetSelfHealContext(sessionId: string): void {
  engramSelfHealContexts.delete(sessionId);
}

function scheduleEngramSelfHeal(ctx: MemoryToolContext): void {
  // Track every session that observed the outage: this module is shared by all sessions in
  // the Pi process, so a single probe must clear the stale label on all of them, not just
  // whichever session happened to fail first.
  engramSelfHealContexts.set(getSessionId(ctx) ?? ctx, ctx);
  if (engramSelfHealInFlight) return;
  engramSelfHealInFlight = true;
  void (async () => {
    try {
      for (let attempt = 0; attempt < ENGRAM_SELF_HEAL_MAX_ATTEMPTS; attempt += 1) {
        await waitUnref(ENGRAM_SELF_HEAL_INTERVAL_MS);
        if (await isEngramRunning()) {
          for (const pending of engramSelfHealContexts.values()) pending.ui?.setStatus?.("engram", undefined);
          return;
        }
      }
    } finally {
      engramSelfHealContexts.clear();
      engramSelfHealInFlight = false;
    }
  })();
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

function errorStatusLabel(message: string): string {
  if (/ambiguous project/i.test(message)) return "ambiguous project";
  return "error";
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
        windowsHide: true,
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
    try {
      const detected = await engramFetch<CurrentProjectResponse>(`/project/current${queryString({ cwd })}`);
      if (detected) return detected;
    } catch (error) {
      if (error instanceof EngramHttpError && error.status === 404) {
        return detectLocalConfigProject(cwd) || projectCurrentUnsupportedError(cwd);
      }
    }
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
    all_projects: optionalBoolean("Search across every project; when true project is ignored"),
    match_mode: optionalString("Match mode: all (default) or any for broader recall"),
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
    project: optionalString("Optional project to use when automatic detection is unavailable"),
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
  mem_review: Type.Object({
    action: Type.String({ description: "Action: list | mark_reviewed" }),
    project: optionalString("Optional project filter for action=list"),
    limit: optionalNumber("Max results for action=list"),
    observation_id: optionalNumber("Observation id for action=mark_reviewed"),
    id: optionalNumber("Alias for observation_id"),
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
      return engramFetch(`/search${queryString({
        q: params.query,
        type: params.type,
        project: params.all_projects ? undefined : params.project,
        scope: params.scope,
        limit: params.limit,
        match_mode: params.match_mode,
        all_projects: params.all_projects,
      })}`);
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
      if (!requestedProject) requireResolvedProject();
      await ensureSession(activeSessionId, activeProject);
      return engramFetch("/observations", {
        method: "POST",
        body: {
          session_id: activeSessionId,
          type: "session_summary",
          title: "Session summary",
          content: params.content,
          project: activeProject,
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
    case "mem_current_project": {
      const cwd = String(params.cwd || ctx.cwd);
      try {
        return await engramFetch(`/project/current${queryString({ cwd })}`);
      } catch (error) {
        if (error instanceof EngramHttpError && error.status === 404) {
          return detectLocalConfigProject(cwd) || projectCurrentUnsupportedError(cwd);
        }
        throw error;
      }
    }
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
    case "mem_review": {
      const action = String(params.action || "").trim();
      if (action === "list") {
        return engramFetch(`/review${queryString({ project: params.project, limit: params.limit })}`);
      }
      if (action === "mark_reviewed") {
        return engramFetch("/review/mark_reviewed", {
          method: "POST",
          body: { observation_id: params.observation_id || params.id },
        });
      }
      throw new Error("action must be one of: list, mark_reviewed");
    }
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

function unreachableMessage(timedOutMethod: string | undefined): string {
  if (timedOutMethod && timedOutMethod !== "GET") {
    return `gentle-engram timed out after ${ENGRAM_FETCH_TIMEOUT_MS}ms waiting for the Engram HTTP server at ${ENGRAM_URL}. The ${timedOutMethod} request may already have been applied — do NOT blindly retry it, or you may duplicate the write. Verify with mem_search or mem_doctor first.`;
  }
  if (timedOutMethod) {
    return `gentle-engram timed out after ${ENGRAM_FETCH_TIMEOUT_MS}ms waiting for the Engram HTTP server at ${ENGRAM_URL}. The server accepted the connection but did not respond. Run mem_doctor or restart Engram.`;
  }
  return `gentle-engram could not reach the Engram HTTP server at ${ENGRAM_URL}. The Pi-native mem_* tools are registered, but the native memory provider is not currently responding. Run mem_doctor or restart Engram.`;
}

async function executeMemoryTool(toolName: string, params: Record<string, unknown>, ctx: MemoryToolContext) {
  await initOnce(ctx.cwd);
  await refreshProjectDetection(ctx.cwd);
  const action = humanToolName(toolName);
  ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${action}…`);

  try {
    const data = await callMemoryTool(toolName, params, ctx);
    if (data === null) {
      throw new Error(unreachableMessage(takeLastFetchTimeoutMethod()));
    }
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
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${errorStatusLabel(message)}`);
    if (!(error instanceof EngramHttpError)) scheduleEngramSelfHeal(ctx);
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
        return executeMemoryTool(toolName, params as Record<string, unknown>, ctx as MemoryToolContext);
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
    forgetSelfHealContext(sessionId);
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

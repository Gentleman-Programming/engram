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
const ENGRAM_STARTUP_TIMEOUT_MS = 10000;
const ENGRAM_STARTUP_POLL_MS = 100;
const ENGRAM_STARTUP_RETRY_BASE_MS = 1000;
const ENGRAM_STARTUP_RETRY_MAX_MS = 60000;

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
- **scope**: \`project\` (default) | \`personal\` | \`global\`
- **topic_key**: stable key for evolving decisions when relevant
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you

### WHEN TO SEARCH MEMORY

When the user asks to recall past work:
1. Start with \`mem_context\`, then search with 1–2 distinctive keywords.
2. Ordinary \`mem_search\` is scoped to the detected active project; its default
   \`match_mode:"all"\` means AND. For broad recall, use \`match_mode:"any"\` with
   \`all_projects:true\`. If a scoped search is empty, retry once this way before
   concluding no memory exists.
3. After hits, narrow follow-up searches by project, type, or \`match_mode:"all"\`,
   then use \`mem_get_observation\` for full content.

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

interface EngramFetchResult<TResponse> {
  data: TResponse | null;
  timedOutMethod?: string;
}

type EngramFetcher = <TResponse = unknown>(path: string, opts?: FetchOptions) => Promise<TResponse | null>;

function isSafeToReplay(path: string, method: string): boolean {
  // Session creation is explicitly idempotent on the server (INSERT OR IGNORE). Other writes
  // have no idempotency key, so only reads and this registration request can be replayed.
  return method === "GET" || (method === "POST" && path === "/sessions");
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

// A completed response with JSON null is a successful result. Timeout metadata travels with
// its request result so concurrent native tool calls cannot observe one another's outcomes.
async function engramFetchResult<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<EngramFetchResult<TResponse>> {
  const method = opts.method ?? "GET";
  let res: Response | undefined;
  let timedOut = false;
  let refused = false;
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
      refused = isConnectionRefusedError(error);
      if (!isSafeToReplay(path, method) || attempt === ENGRAM_FETCH_MAX_ATTEMPTS - 1) break;
      await wait(ENGRAM_FETCH_BACKOFF_BASE_MS * 2 ** attempt);
    }
  }

  if (!res) {
    if (timedOut) return { data: null, timedOutMethod: method };
    // A confirmed local refusal after successful initialization means the implicitly owned
    // server may have died. One generation-scoped recovery is shared by every caller. Only
    // operations whose idempotence is established above are replayed after it is healthy.
    if (refused && await recoverImplicitEngramServer() && isSafeToReplay(path, method)) {
      return engramFetchResult<TResponse>(path, opts);
    }
    throw new Error(unreachableMessage(undefined));
  }

  let data: unknown = null;
  if (res.status !== 204) {
    try {
      data = await res.json();
    } catch (error) {
      if (res.ok) throw error;
    }
  }

  if (!res.ok) {
    const message = data && typeof data === "object" && "error" in data && typeof data.error === "string"
      ? data.error
      : `Engram request failed with HTTP ${res.status}`;
    throw new EngramHttpError(message, res.status, data);
  }

  return { data: data as TResponse };
}

async function engramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  return (await engramFetchResult<TResponse>(path, opts)).data;
}

function createMemoryToolTransport(): { fetch: EngramFetcher; timedOutMethod: () => string | undefined } {
  let timedOutMethod: string | undefined;
  return {
    async fetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
      const result = await engramFetchResult<TResponse>(path, opts);
      if (result.timedOutMethod) timedOutMethod = result.timedOutMethod;
      return result.data;
    },
    timedOutMethod: () => timedOutMethod,
  };
}

// warnEngramFailure reports a background capture failure on stderr. These calls
// are best-effort by design, but discarding them without a trace means a user
// whose memories stopped being saved — an unowned session rejecting writes, for
// example — has no signal at all that anything is wrong.
function warnEngramFailure(path: string, error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  try {
    process.stderr.write(`[engram] background capture to ${redactUrlPath(path)} failed: ${message}\n`);
  } catch {
    // Diagnostics must never break the caller.
  }
}

async function bestEffortEngramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  try {
    return await engramFetch<TResponse>(path, opts);
  } catch (error) {
    warnEngramFailure(path, error);
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

// "refused" means we saw proof that nothing is listening; "indeterminate" means the probe
// told us nothing either way. Only "ready" is proof that a server is answering, so nothing
// but "ready" may be read as "a server is already there".
type EngramHealth = "ready" | "refused" | "indeterminate";

// Node reports a refused localhost connection through several shapes: a bare Error whose
// message is the refusal, a wrapper whose `cause` carries `code`, and — when the host
// resolves to both ::1 and 127.0.0.1 — an AggregateError whose per-address `errors` carry it
// while the aggregate itself carries none. Walk all of them, and read `code` through the
// prototype chain, so one unmatched shape cannot silently downgrade a plain refusal.
function hasConnectionRefusedCode(value: unknown, depth = 0): boolean {
  if (depth > 4 || typeof value !== "object" || value === null) return false;
  const record = value as Record<string, unknown>;
  if (record.code === "ECONNREFUSED") return true;
  const errors = record.errors;
  if (Array.isArray(errors) && errors.some((entry) => hasConnectionRefusedCode(entry, depth + 1))) return true;
  return hasConnectionRefusedCode(record.cause, depth + 1);
}

function isConnectionRefusedError(error: unknown): boolean {
  return (error instanceof Error && error.message === "connection refused") || hasConnectionRefusedCode(error);
}

async function probeEngramHealth(): Promise<EngramHealth> {
  try {
    const res = await fetch(`${ENGRAM_URL}/health`, {
      signal: AbortSignal.timeout(500),
    });
    return res.ok ? "ready" : "indeterminate";
  } catch (error) {
    if (isTimeoutError(error)) return "indeterminate";
    if (isConnectionRefusedError(error)) {
      return "refused";
    }
    return "indeterminate";
  }
}

async function isEngramRunning(): Promise<boolean> {
  return (await probeEngramHealth()) === "ready";
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

// A sleep that a cancelled readiness wait can cut short: without it an abandoned poll would
// hold a live timer — and with it the whole Pi process — until its next tick fired.
function waitCancellable(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      resolve();
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

// The deadline is absolute and passed in, so a spawn attempt and the fallback wait that
// follows it share one startup budget instead of each starting a fresh one.
async function waitForEngramReadiness(signal: AbortSignal, deadline: number): Promise<void> {
  while (Date.now() < deadline) {
    if (signal.aborted) throw new Error(`Engram startup readiness wait for ${ENGRAM_URL} was cancelled`);
    if (await probeEngramHealth() === "ready") return;
    // The probe itself can outlive the abort, so re-check before sleeping again.
    if (signal.aborted) throw new Error(`Engram startup readiness wait for ${ENGRAM_URL} was cancelled`);
    await waitCancellable(ENGRAM_STARTUP_POLL_MS, signal);
  }
  throw new Error(`Engram server at ${ENGRAM_URL} did not become ready before startup timeout`);
}

// A child we gave up on is terminated, not merely released. Unreffing alone only detaches it
// from our event loop: the process stays alive, detached, answering nothing — and because
// initialization is retried, every later attempt would add another one for the life of the
// session. Killing is best effort: on the `exit` path the child is already gone.
function stopAbandonedChild(proc: ChildProcess | undefined): void {
  if (proc === undefined) return;
  try {
    proc.kill("SIGTERM");
  } catch {}
  proc.unref();
}

function spawnAndWaitForEngram(deadline: number): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    let proc: ChildProcess | undefined;
    let settled = false;
    // One controller cancels the readiness poll from every terminal path, so a child that
    // errors, exits, or never becomes ready cannot leave a probe loop running behind it.
    const readiness = new AbortController();

    // Every terminal path — readiness, error, exit, timeout, abort — runs this exactly once:
    // it stops the poll and detaches the listeners. Only a child that reached readiness is
    // released to keep serving; any other outcome means we gave up on it, so it is killed.
    function settle(error?: Error): void {
      if (settled) return;
      settled = true;
      readiness.abort();
      proc?.removeListener("error", onError);
      proc?.removeListener("exit", onExit);
      if (error) {
        stopAbandonedChild(proc);
        rejectPromise(error);
        return;
      }
      proc?.unref();
      resolvePromise();
    }

    const onError = (error: Error): void =>
      settle(new Error(`Engram server failed before readiness: ${error.message}`));
    const onExit = (code: number | null, signal: NodeJS.Signals | null): void =>
      settle(new Error(`Engram server exited before readiness (code ${code ?? "unknown"}, signal ${signal ?? "none"})`));

    try {
      proc = spawn(ENGRAM_BIN, ["serve"], {
        windowsHide: true,
        detached: true,
        stdio: "ignore",
      });
    } catch (error) {
      settle(error instanceof Error ? error : new Error("Engram server could not start"));
      return;
    }
    proc.once("error", onError);
    proc.once("exit", onExit);
    proc.once("spawn", () => {
      void waitForEngramReadiness(readiness.signal, deadline).then(
        () => settle(),
        (error) => settle(error instanceof Error ? error : new Error(String(error))),
      );
    });
  });
}

async function initializeEngramServer(): Promise<void> {
  if (CONFIGURED_ENGRAM_URL !== undefined) return;
  const deadline = Date.now() + ENGRAM_STARTUP_TIMEOUT_MS;
  const health = await probeEngramHealth();
  if (health === "ready") return;

  // Only "ready" proves a server is answering. Every other outcome — a definitive refusal, an
  // aborted probe, a DNS failure, an error shape we do not recognize — means we have no
  // server, so launch one. Reading an inconclusive probe as "a server must be starting"
  // instead is what let a cold machine burn the whole startup budget polling a port nobody
  // was ever going to bind.
  try {
    await spawnAndWaitForEngram(deadline);
  } catch (error) {
    // An inconclusive probe leaves room for another Pi process to already own the port, which
    // is exactly what makes our child fail. Give that instance the rest of the shared
    // deadline before reporting failure. A definitive refusal gets no such grace: nothing was
    // listening when we looked, so there is no other instance to wait for.
    if (health !== "indeterminate") throw error;
    const readiness = new AbortController();
    try {
      await waitForEngramReadiness(readiness.signal, deadline);
    } catch {
      // The spawn failure is the actionable one; a readiness timeout only restates it.
      throw error;
    } finally {
      readiness.abort();
    }
  }
}

let initialization: Promise<void> | undefined;
let startupFailures = 0;
let startupRetryAt = 0;
let startupFailure: Error | undefined;
let initializationGeneration = 0;
let recoveredInitializationGeneration = 0;
let recoveryFlight: { generation: number; promise: Promise<boolean> } | undefined;

function startupBackoffMs(failures: number): number {
  return Math.min(ENGRAM_STARTUP_RETRY_MAX_MS, ENGRAM_STARTUP_RETRY_BASE_MS * 2 ** (failures - 1));
}

// A failed startup stays retryable — a transient outage must not disable memory for the rest
// of the session — but retrying it on every caller made a persistently unhealthy provider
// charge the full readiness budget once per tool call. Inside the backoff window the last
// failure is replayed immediately, so the cost of an unhealthy provider is bounded by the
// backoff rather than by how often the agent calls tools, and so is the number of children a
// failing session can spawn. A success clears the window and is cached for the session.
function sharedInitialization(start: () => Promise<void>): Promise<void> {
  if (initialization) return initialization;
  if (startupFailure !== undefined && Date.now() < startupRetryAt) return Promise.reject(startupFailure);
  initialization = start().then(
    () => {
      startupFailures = 0;
      startupRetryAt = 0;
      startupFailure = undefined;
      initializationGeneration += 1;
    },
    (error: unknown) => {
      initialization = undefined;
      startupFailures += 1;
      startupFailure = error instanceof Error ? error : new Error(String(error));
      startupRetryAt = Date.now() + startupBackoffMs(startupFailures);
      throw startupFailure;
    },
  );
  return initialization;
}

// Initialization remains fulfilled for the session, so a later refusal needs a separate,
// bounded recovery path. Marking a generation before starting prevents a failed restart from
// becoming a spawn storm; tagging the flight prevents callers from joining an older generation.
function recoverImplicitEngramServer(): Promise<boolean> {
  const generation = initializationGeneration;
  if (CONFIGURED_ENGRAM_URL !== undefined || generation === 0) return Promise.resolve(false);
  const activeFlight = recoveryFlight;
  if (activeFlight?.generation === generation) return activeFlight.promise;
  if (recoveredInitializationGeneration === generation) return Promise.resolve(false);

  recoveredInitializationGeneration = generation;
  const promise = initializeEngramServer().then(
    () => true,
    () => false,
  ).finally(() => {
    if (recoveryFlight?.generation === generation) recoveryFlight = undefined;
  });
  recoveryFlight = { generation, promise };
  return promise;
}

let project = "unknown";
let directory = "";
let pendingRecoveryNotice: string | undefined;
let projectResolutionError: string | undefined;
let projectDetectionPending = false;

const knownSessions = new Set<string>();
const sessionRegistrationsInFlight = new Map<string, Promise<void>>();
const toolCounts = new Map<string, number>();

async function ensureSession(sessionId: string, sessionProject = project, fetch: EngramFetcher = engramFetch): Promise<void> {
  const key = `${sessionProject}:${sessionId}`;
  if (!sessionId || knownSessions.has(key)) return;

  const existingRegistration = sessionRegistrationsInFlight.get(key);
  if (existingRegistration) return existingRegistration;

  const registration = (async () => {
    const body: SessionBody = { id: sessionId, project: sessionProject, directory };
    const acknowledgement = await fetch("/sessions", { method: "POST", body });
    if (acknowledgement === null) {
      throw new Error(`gentle-engram could not confirm session registration for Pi runtime session ${sessionId}`);
    }
    knownSessions.add(key);
  })();
  sessionRegistrationsInFlight.set(key, registration);

  try {
    await registration;
  } finally {
    if (sessionRegistrationsInFlight.get(key) === registration) {
      sessionRegistrationsInFlight.delete(key);
    }
  }
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

function isSafeDetectedProject(detected: CurrentProjectResponse): string | undefined {
  const candidate = typeof detected.project === "string" ? detected.project.trim() : "";
  if (
    !candidate ||
    candidate.toLowerCase() === "unknown" ||
    detected.error_hint !== undefined ||
    /[\\/\x00-\x1F\x7F]/.test(candidate)
  ) {
    return undefined;
  }
  return candidate;
}

function applyDetectedProject(detected: CurrentProjectResponse | undefined): boolean {
  if (!detected) {
    project = "unknown";
    projectDetectionPending = true;
    return false;
  }
  projectDetectionPending = false;
  const detectedProject = isSafeDetectedProject(detected);
  if (detectedProject) {
    project = detectedProject;
    projectResolutionError = undefined;
    return true;
  }
  project = "unknown";
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

async function initialize(cwd: string): Promise<void> {
  directory = cwd;

  project = fallbackProjectName(cwd);

  await initializeEngramServer();

  applyDetectedProject(await detectServerProject(cwd));

  const manifestFile = `${cwd}/.engram/manifest.json`;
  if (existsSync(manifestFile)) {
    await spawnDetached(ENGRAM_BIN, ["sync", "--import"], cwd);
  }
}

// Startup failures reach the agent as prose, so give every one of them the same shape and
// the same actionable prefix instead of leaking a raw spawn or readiness message.
function normalizeInitializationError(error: unknown): Error {
  const message = error instanceof Error ? error.message : String(error);
  return new Error(
    `gentle-engram could not initialize the Engram memory provider at ${ENGRAM_URL}: ${message}. Run mem_doctor or start Engram manually, and verify ENGRAM_URL/ENGRAM_BIN.`,
  );
}

function initOnce(cwd: string): Promise<void> {
  return sharedInitialization(() => initialize(cwd)).catch((error) => {
    throw normalizeInitializationError(error);
  });
}

// Session hooks have no error channel back to the agent, so a failed startup must stop here
// rather than escape the hook. sharedInitialization already cleared its cached promise, so
// the next mem_* tool call retries and reports the normalized failure to the model.
async function initOnceForHook(cwd: string): Promise<boolean> {
  try {
    await initOnce(cwd);
    return true;
  } catch {
    return false;
  }
}

function getSessionId(ctx: SessionContext): string | undefined {
  return ctx.sessionManager.getSessionId();
}

// The Pi runtime session ID is opaque: blankness is validated without normalizing it,
// so registration, writes, compaction, and shutdown cleanup all key off the exact same
// bytes. Trimming here would split that identity and strand cache entries at shutdown.
function requireRuntimeSessionID(ctx: SessionContext): string {
  const sessionId = getSessionId(ctx);
  if (!sessionId || sessionId.trim().length === 0) {
    throw new Error("Pi runtime session ID is unavailable; session-attributed writes require a native SessionContext ID");
  }
  return sessionId;
}

const optionalString = (description: string) => Type.Optional(Type.String({ description }));
const optionalNumber = (description: string) => Type.Optional(Type.Number({ description }));
const optionalBoolean = (description: string) => Type.Optional(Type.Boolean({ description }));

const MEMORY_TOOL_SCHEMAS: Record<string, ReturnType<typeof Type.Object>> = {
  mem_search: Type.Object({
    query: Type.String({ description: "Search query — natural language or keywords" }),
    type: optionalString("Filter by observation type"),
    project: optionalString("Filter by project name"),
    scope: optionalString("Filter by scope: project, personal, or global. Omit to apply no scope filter."),
    limit: optionalNumber("Max results"),
    all_projects: optionalBoolean("Search across every project; when true project is ignored"),
    match_mode: optionalString("Match mode: all (default) or any for broader recall"),
  }),
  mem_save: Type.Object({
    title: Type.String({ description: "Short, searchable title" }),
    content: Type.String({ description: "Structured memory content" }),
    type: optionalString("Observation type/category"),
    scope: optionalString("Scope: project, personal, or global"),
    topic_key: optionalString("Stable topic key for upserts"),
    project: optionalString("Optional explicit project"),
    capture_prompt: optionalBoolean("Capture current prompt when available"),
  }),
  mem_update: Type.Object({
    id: Type.Number({ description: "Observation ID to update" }),
    title: optionalString("New title"),
    content: optionalString("New content"),
    type: optionalString("New type/category"),
    scope: optionalString("New scope: project, personal, or global"),
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
    project: optionalString("Optional project"),
  }),
  mem_session_summary: Type.Object({
    content: Type.String({ description: "Full session summary" }),
    project: optionalString("Optional project to use when automatic detection is unavailable"),
  }),
  mem_context: Type.Object({
    project: optionalString("Filter by project"),
    scope: optionalString("Filter observations by scope: project, personal, or global. Omit to apply no scope filter."),
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
    source: optionalString("Source identifier, e.g. subagent-stop or session-end"),
  }),
  mem_review: Type.Object({
    action: Type.String({ description: "Action: list | mark_reviewed" }),
    project: optionalString("Optional project selector: filters list and scopes mark_reviewed; list remains global when omitted"),
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

function textResult(data: unknown, toolName?: string): string {
  if (toolName === "mem_search" && (data === null || (Array.isArray(data) && data.length === 0))) {
    return "No memories found";
  }
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

async function callMemoryTool(toolName: string, params: Record<string, unknown>, ctx: SessionContext, fetch: EngramFetcher = engramFetch): Promise<unknown> {
  const sessionId = getSessionId(ctx);
  const requestedProject = typeof params.project === "string" && params.project ? params.project : undefined;
  const activeProject = requestedProject || project;
  const runtimeSessionForWrite = () => requireRuntimeSessionID(ctx);

  switch (toolName) {
    case "mem_search":
      if (!params.all_projects && !requestedProject) requireResolvedProject();
      return fetch(`/search${queryString({
        q: params.query,
        type: params.type,
        project: params.all_projects ? undefined : activeProject,
        scope: params.scope,
        limit: params.limit,
        match_mode: params.match_mode,
        all_projects: params.all_projects,
      })}`);
    case "mem_context":
      if (!params.project) requireResolvedProject();
      return fetch(`/context${queryString({ project: params.project || project, scope: params.scope })}`);
    case "mem_stats":
      return fetch(`/stats${queryString({ all_projects: true })}`);
    case "mem_timeline":
      if (!requestedProject) requireResolvedProject();
      return fetch(`/timeline${queryString({ observation_id: params.observation_id, before: params.before, after: params.after, project: activeProject })}`);
    case "mem_get_observation":
      return fetch(`/observations/${encodeURIComponent(String(params.id))}`);
    case "mem_save": {
      if (!requestedProject) requireResolvedProject();
      const activeSessionId = runtimeSessionForWrite();
      await ensureSession(activeSessionId, activeProject, fetch);
      return fetch("/observations", {
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
    }
    case "mem_update":
      return fetch(`/observations/${encodeURIComponent(String(params.id))}`, {
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
      return fetch(`/observations/${encodeURIComponent(String(params.id))}${queryString({ hard: params.hard_delete })}`, { method: "DELETE" });
    case "mem_suggest_topic_key":
      return { topic_key: slugifyTopicKey(params) };
    case "mem_save_prompt": {
      if (!requestedProject) requireResolvedProject();
      const promptSessionId = runtimeSessionForWrite();
      await ensureSession(promptSessionId, activeProject, fetch);
      const response = await fetch<{ id: number }>("/prompts", {
        method: "POST",
        body: { session_id: promptSessionId, content: params.content, project: activeProject },
      });
      return response ? { prompt_id: response.id, status: "saved" } : response;
    }
    case "mem_session_summary": {
      if (!requestedProject) requireResolvedProject();
      const summarySessionId = runtimeSessionForWrite();
      await ensureSession(summarySessionId, activeProject, fetch);
      return fetch("/observations", {
        method: "POST",
        body: {
          session_id: summarySessionId,
          type: "session_summary",
          title: "Session summary",
          content: params.content,
          project: activeProject,
          scope: "project",
        },
      });
    }
    case "mem_session_start":
      requireResolvedProject();
      return fetch("/sessions", {
        method: "POST",
        body: { id: params.id, project, directory: params.directory || directory || ctx.cwd },
      });
    case "mem_session_end":
      return fetch(`/sessions/${encodeURIComponent(String(params.id))}/end`, {
        method: "POST",
        body: { summary: params.summary || "" },
      });
    case "mem_current_project": {
      const cwd = String(params.cwd || ctx.cwd);
      try {
        return await fetch(`/project/current${queryString({ cwd })}`);
      } catch (error) {
        if (error instanceof EngramHttpError && error.status === 404) {
          return detectLocalConfigProject(cwd) || projectCurrentUnsupportedError(cwd);
        }
        throw error;
      }
    }
    case "mem_doctor":
      if (!requestedProject) requireResolvedProject();
      return fetch(`/doctor${queryString({ project: activeProject, check: params.check })}`);
    case "mem_capture_passive": {
      requireResolvedProject();
      const passiveSessionId = runtimeSessionForWrite();
      await ensureSession(passiveSessionId, project, fetch);
      return fetch("/observations/passive", {
        method: "POST",
        body: {
          session_id: passiveSessionId,
          content: params.content,
          project,
          source: params.source || "pi-tool",
        },
      });
    }
    case "mem_review": {
      const action = String(params.action || "").trim();
      if (action === "list") {
        return fetch(`/review${queryString({ project: requestedProject, limit: params.limit, all_projects: requestedProject ? undefined : true })}`);
      }
      if (action === "mark_reviewed") {
        if (!requestedProject) requireResolvedProject();
        return fetch(`/review/mark_reviewed${queryString({ project: activeProject })}`, {
          method: "POST",
          body: { observation_id: params.observation_id || params.id },
        });
      }
      throw new Error("action must be one of: list, mark_reviewed");
    }
    case "mem_judge":
      return fetch("/conflicts/judge", {
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
      return fetch("/conflicts/compare", {
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
  const action = humanToolName(toolName);
  const transport = createMemoryToolTransport();

  try {
    // Initialization runs inside the guarded path: a rejected startup must reach the agent as
    // a normalized tool error, not as a rejection escaping the Pi tool boundary.
    await initOnce(ctx.cwd);
    await refreshProjectDetection(ctx.cwd);
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${action}…`);
    const data = await callMemoryTool(toolName, params, ctx, transport.fetch);
    const timedOutMethod = transport.timedOutMethod();
    if (timedOutMethod) throw new Error(unreachableMessage(timedOutMethod));
    const result = { content: [{ type: "text" as const, text: textResult(data, toolName) }], details: { data } };
    if (toolName === "mem_doctor" && data && typeof data === "object" && "status" in data && data.status === "error") {
      const errorResult = { ...result, isError: true };
      ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${compactResultStatus(toolName, errorResult)}`);
      return errorResult;
    }
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${compactResultStatus(toolName, result)}`);
    return result;
  } catch (error) {
    const timedOutMethod = transport.timedOutMethod();
    const message = timedOutMethod ? unreachableMessage(timedOutMethod) : error instanceof Error ? error.message : String(error);
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
    const ready = await initOnceForHook(ctx.cwd);
    try {
      (ctx as MemoryToolContext).ui?.setStatus?.("engram", `🧠 ${project} · ${ready ? "ready" : "offline"}`);
    } catch {}
  });

  pi.on("session_shutdown", async (_event: unknown, ctx: SessionContext) => {
    const sessionId = getSessionId(ctx);
    if (!sessionId) return;
    toolCounts.delete(sessionId);
    forgetKnownSession(sessionId);
    forgetSelfHealContext(sessionId);
  });

  pi.on("session_compact", async (event: unknown, ctx: SessionContext) => {
    if (!(await initOnceForHook(ctx.cwd))) return;
    await refreshProjectDetection(ctx.cwd);
    if (projectDetectionPending || projectResolutionError) return;
    const sessionId = getSessionId(ctx);
    if (sessionId) await ensureSession(sessionId);

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
    let systemPrompt = event.systemPrompt.length > 0 ? `${event.systemPrompt}\n\n${MEMORY_INSTRUCTIONS}` : MEMORY_INSTRUCTIONS;
    // The mem_* tools stay registered whether or not startup succeeded, so the agent still
    // needs the memory protocol; only the server-backed work below is skipped.
    if (!(await initOnceForHook(ctx.cwd))) return { systemPrompt };
    await refreshProjectDetection(ctx.cwd);
    const sessionId = getSessionId(ctx);

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

    if (!(await initOnceForHook(ctx.cwd))) return;
    await refreshProjectDetection(ctx.cwd);
    const sessionId = getSessionId(ctx);
    if (!sessionId || projectDetectionPending || projectResolutionError) return;

    await ensureSessionBestEffort(sessionId);
    toolCounts.set(sessionId, (toolCounts.get(sessionId) ?? 0) + 1);

    if (event.result === undefined) return;
    let content: string | undefined;
    try {
      content = typeof event.result === "string" ? event.result : JSON.stringify(event.result);
    } catch {
      return;
    }
    if (!content || content.length <= 50) return;

    const body: PassiveCaptureBody = {
      session_id: sessionId,
      content: stripPrivateTags(content),
      project,
      source: toolName,
    };
    await bestEffortEngramFetch("/observations/passive", { method: "POST", body });
  });
}

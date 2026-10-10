/**
 * Engram — Pi extension adapter
 *
 * Thin adapter that connects Pi session events to an Engram HTTP server.
 * Persistence remains owned by the Engram Go binary (`engram serve`). MCP tools
 * are configured separately through Pi's built-in MCP (`mcp.json`) and `engram mcp`.
 */

import { spawn, spawnSync, type ChildProcess, type SpawnSyncReturns } from "node:child_process";
import { existsSync, readFileSync, writeSync } from "node:fs";
import { createHash, randomUUID } from "node:crypto";
import { basename, dirname, resolve } from "node:path";
import type { ExtensionAPI, InputEvent } from "@earendil-works/pi-coding-agent";
import { Text } from "@earendil-works/pi-tui";
import { Type } from "typebox";
import { ArchiveOutcome, buildRecoveryNotice, extractCompactedSummary } from "./compaction-recovery.js";
import { compactResultStatus, humanToolName, renderCallText, renderResultText } from "./memory-tool-chrome.js";
import { redactPrivateTags, redactUrlPath, redactValue } from "./private-redaction.js";

function optionalEnvironmentValue(value: string | undefined): string | undefined {
  return value?.trim() ? value : undefined;
}

const ENGRAM_PORT = Number.parseInt(optionalEnvironmentValue(process.env.ENGRAM_PORT) ?? "7437", 10);
const CONFIGURED_ENGRAM_URL = optionalEnvironmentValue(process.env.ENGRAM_URL);
const ENGRAM_URL = CONFIGURED_ENGRAM_URL || `http://127.0.0.1:${ENGRAM_PORT}`;
const ENGRAM_BIN = optionalEnvironmentValue(process.env.ENGRAM_BIN) ?? "engram";

// Only allowlisted metadata is logged; never pass request bodies, identities, or errors.
type SessionTraceEvent =
  | { stage: "lifecycle"; event: "session_start"; identity: "first" | "same" | "different" | "missing" }
  | { stage: "lifecycle"; event: "session_shutdown"; reason: "reload" | "other" | "missing"; identity: "first" | "same" | "different" | "missing" }
  | { stage: "context"; append_entry: boolean; get_branch: boolean; persisted: "root" | "continuation" }
  | { stage: "dispatch"; requested: "root" | "continuation"; resume: boolean }
  | { stage: "acknowledgement" | "adoption"; effective: "root" | "continuation" | "invalid" }
  | { stage: "rejection"; http_status: number | null; reason: "session_already_ended" | "other" };

function traceSessionRegistration(event: SessionTraceEvent): void {
  if (process.env.ENGRAM_PI_SESSION_TRACE !== "1") return;
  // Descriptor writes surface pipe failures synchronously, without host-wide stream handlers.
  try { writeSync(2, `[engram:session-trace] ${JSON.stringify(event)}\n`); }
  catch { /* Diagnostic output must not change registration or write behavior. */ }
}

// Writes are single-attempt: once dispatched, their server-side outcome may be unknown.
const ENGRAM_WRITE_TIMEOUT_MS = 3000;
const ENGRAM_READ_TIMEOUT_MS = 10000;
const ENGRAM_OBSERVATION_RECOVERY_TIMEOUT_MS = 7500;
const ENGRAM_RECOVERY_LOOKUP_TIMEOUT_MS = 1000;
const ENGRAM_DOCTOR_TIMEOUT_MS = 15000;
const ENGRAM_SESSION_REGISTRATION_TIMEOUT_MS = 5000;
const ENGRAM_READ_MAX_ATTEMPTS = 3;
const ENGRAM_SESSION_REGISTRATION_MAX_ATTEMPTS = 2;
const ENGRAM_FETCH_BACKOFF_BASE_MS = 250;
const ENGRAM_SELF_HEAL_INTERVAL_MS = 5000;
const ENGRAM_SELF_HEAL_MAX_ATTEMPTS = 6;
const ENGRAM_STARTUP_TIMEOUT_MS = 10000;
const ENGRAM_STARTUP_POLL_MS = 100;
const ENGRAM_STARTUP_RETRY_BASE_MS = 1000;
const ENGRAM_STARTUP_RETRY_MAX_MS = 60000;
const ENGRAM_VERSION_PROBE_TIMEOUT_MS = 2000;
const ENGRAM_DETERMINISTIC_RETRY_MS = 5000;

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
  "mem_list_projects",
  "mem_pin",
  "mem_unpin",
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

### DELIVERY GUARANTEE

Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

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

Before explicitly closing or handing off the session, call \`mem_session_summary\`
with Goal, Instructions, Discoveries, Accomplished, Next Steps, and Relevant Files.
Ordinary task completion or merely saying "done" does not require a session summary.
If \`mem_session_summary\` fails because Engram cannot detect a project, ask the user
which project should receive the summary, then retry with \`project: "<name>"\`.

### AFTER COMPACTION

When outcome-specific compaction recovery guidance is present, follow it. If a
compacted summary appears without that guidance, save it immediately with
\`mem_session_summary\`, then call \`mem_context\` before continuing.
`;

interface FetchOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  observationSave?: boolean;
  // Capability validation before dispatch, including transport retries/reconnects.
  beforeDispatch?: () => Promise<void>;
  // Observational hook, separate from capability validation and recovery guards.
  onDispatch?: () => void;
}

type EngramOperation = "read" | "doctor" | "session-registration" | "write";
type EngramTransportOutcome = "timed_out" | "unknown";

interface EngramTransportFailure {
  operation: EngramOperation;
  outcome: EngramTransportOutcome;
  timeoutMs: number;
  operationID?: string;
}

interface EngramFetchResult<TResponse> {
  data: TResponse | null;
  transportFailure?: EngramTransportFailure;
}

interface EngramFetchPolicy {
  operation: EngramOperation;
  timeoutMs: number;
  maxAttempts: number;
  replaySafe: boolean;
}

type EngramFetcher = <TResponse = unknown>(path: string, opts?: FetchOptions) => Promise<TResponse | null>;

function isIdempotentSessionRegistration(path: string, method: string): boolean {
  // Core uses INSERT OR IGNORE for this registration identity; other POSTs have no replay key.
  return method === "POST" && path === "/sessions";
}

function isSafeToReplay(path: string, method: string): boolean {
  return method === "GET" || isIdempotentSessionRegistration(path, method);
}

function engramFetchPolicy(path: string, method: string): EngramFetchPolicy {
  if (isIdempotentSessionRegistration(path, method)) {
    return { operation: "session-registration", timeoutMs: ENGRAM_SESSION_REGISTRATION_TIMEOUT_MS, maxAttempts: ENGRAM_SESSION_REGISTRATION_MAX_ATTEMPTS, replaySafe: true };
  }
  if (method === "GET" && path.startsWith("/doctor")) {
    return { operation: "doctor", timeoutMs: ENGRAM_DOCTOR_TIMEOUT_MS, maxAttempts: ENGRAM_READ_MAX_ATTEMPTS, replaySafe: true };
  }
  if (method === "GET") {
    return { operation: "read", timeoutMs: ENGRAM_READ_TIMEOUT_MS, maxAttempts: ENGRAM_READ_MAX_ATTEMPTS, replaySafe: true };
  }
  return { operation: "write", timeoutMs: ENGRAM_WRITE_TIMEOUT_MS, maxAttempts: 1, replaySafe: false };
}

interface SessionBody {
  id: string;
  project: string;
  directory: string;
  ownership_mode: "project_owned";
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
  hasUI?: boolean;
  ui?: {
    notify?: (message: string, severity: "warning") => void;
    setStatus?: (key: string, text: string | undefined) => void;
  };
  cwd: string;
  sessionManager: {
    getSessionId(): string | undefined;
    getBranch?(): Array<{ type?: string; customType?: string; data?: unknown }>;
  };
}

type MemoryToolContext = SessionContext;

interface AgentStartEvent {
  systemPrompt: string;
  prompt?: string;
  // Present on Pi versions that expose structured, per-run cloned prompt options.
  systemPromptOptions?: { appendSystemPrompt?: string } | null;
}

function appendPromptSection(options: { appendSystemPrompt?: string }, section: string): void {
  const existing = options.appendSystemPrompt ?? "";
  if (existing.includes(section)) return;
  options.appendSystemPrompt = existing.length > 0 ? `${existing}\n\n${section}` : section;
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

class EngramUnavailableError extends Error {
  constructor() {
    super(unreachableMessage(undefined));
    this.name = "EngramUnavailableError";
  }
}

function shouldScheduleEngramSelfHeal(error: unknown, failure: EngramTransportFailure | undefined): boolean {
  return failure !== undefined || error instanceof EngramUnavailableError;
}

class SessionProjectConflictError extends Error {
  readonly sessionId: string;
  readonly ownerProject: string;
  readonly requestedProject: string;

  constructor(sessionId: string, ownerProject: string, requestedProject: string) {
    super(`Pi runtime session ${sessionId} belongs to Engram project ${ownerProject}, not ${requestedProject}. Start a fresh Pi session in ${requestedProject} before capturing memory.`);
    this.name = "SessionProjectConflictError";
    this.sessionId = sessionId;
    this.ownerProject = ownerProject;
    this.requestedProject = requestedProject;
  }
}

function sessionProjectConflictFromResponse(error: unknown, sessionId: string, requestedProject: string, resumeRoot?: string): SessionProjectConflictError | undefined {
  if (!(error instanceof EngramHttpError) || error.status !== 409 || !error.data || typeof error.data !== "object") return undefined;
  const data = error.data as Record<string, unknown>;
  const ownerProject = typeof data.owner_project === "string" ? data.owner_project : "";
  if (
    data.code !== "session_project_conflict"
    || !(data.session_id === sessionId || (resumeRoot !== undefined
      && typeof data.session_id === "string" && data.session_id.startsWith(`${resumeRoot}:resume:`)))
    || data.requested_project !== requestedProject
    || ownerProject.length === 0
    || ownerProject === requestedProject
  ) return undefined;
  return new SessionProjectConflictError(data.session_id as string, ownerProject, requestedProject);
}

// Node rejects an AbortSignal.timeout() fetch with a DOMException named "TimeoutError",
// which is an instanceof Error; a caller-supplied abort surfaces as "AbortError".
function isTimeoutError(error: unknown): boolean {
  return error instanceof Error && (error.name === "TimeoutError" || error.name === "AbortError");
}

// Socket failures after dispatch can leave writes committed without a readable response.
const DEFINITE_PRE_DISPATCH_CODES = new Set(["ENOTFOUND", "EAI_AGAIN", "ENETUNREACH", "EHOSTUNREACH", "UND_ERR_CONNECT_TIMEOUT", "ERR_INVALID_URL"]);

function isDefinitelyPreDispatchError(error: unknown): boolean {
  let current = error;
  for (let depth = 0; depth < 5; depth += 1) {
    if (current === null || (typeof current !== "object" && typeof current !== "function")) return false;
    try {
      const code = Reflect.get(current, "code");
      if (typeof code === "string" && DEFINITE_PRE_DISPATCH_CODES.has(code)) return true;
      current = Reflect.get(current, "cause");
    } catch {
      return false;
    }
  }
  return false;
}

// A completed response with JSON null is a successful result. Failure metadata belongs to
// the request, so concurrent native tools never share timeout outcomes.
async function engramFetchResult<TResponse = unknown>(path: string, opts: FetchOptions = {}, allowGuardedRecovery = true): Promise<EngramFetchResult<TResponse>> {
  const method = opts.method ?? "GET";
  const policy = engramFetchPolicy(path, method);
  let timedOut = false;
  let refused = false;
  let ambiguousTransport = false;
  for (let attempt = 0; attempt < policy.maxAttempts; attempt += 1) {
    // Validation errors must escape, not be classified/retried as transport failures.
    if (opts.beforeDispatch) await opts.beforeDispatch();
    opts.onDispatch?.();
    let res: Response;
    try {
      res = await fetch(`${ENGRAM_URL}${redactUrlPath(path)}`, {
        method,
        headers: opts.body ? { "Content-Type": "application/json" } : undefined,
        body: opts.body ? JSON.stringify(redactValue(opts.body)) : undefined,
        signal: opts.signal
          ? AbortSignal.any([AbortSignal.timeout(policy.timeoutMs), opts.signal])
          : AbortSignal.timeout(policy.timeoutMs),
      });
    } catch (error) {
      if (isTimeoutError(error)) {
        if (opts.signal?.aborted) throw error;
        timedOut = true;
      } else {
        refused = isConnectionRefusedError(error);
        ambiguousTransport ||= !refused && !isDefinitelyPreDispatchError(error);
      }
      if (!policy.replaySafe || attempt === policy.maxAttempts - 1) break;
      await wait(ENGRAM_FETCH_BACKOFF_BASE_MS * 2 ** attempt);
      continue;
    }

    let data: unknown = null;
    if (res.status !== 204) {
      try {
        data = await res.json();
      } catch (error) {
        if (isTimeoutError(error)) {
          if (opts.signal?.aborted) throw error;
          // Headers establish the HTTP failure even if its body never arrives.
          if (!res.ok) throw new EngramHttpError(`Engram request failed with HTTP ${res.status}`, res.status, null);
          timedOut = true;
          if (!policy.replaySafe || attempt === policy.maxAttempts - 1) break;
          await wait(ENGRAM_FETCH_BACKOFF_BASE_MS * 2 ** attempt);
          continue;
        }
        if (res.ok) {
          // A broken body does not prove a write was not applied. Even SyntaxError
          // may mean a cleanly ended but truncated write response, so fail safe.
          // Keep malformed read bodies as parsing errors. Idempotent session
          // registration may be retried even when its JSON was truncated.
          if (error instanceof SyntaxError && (policy.operation === "read" || policy.operation === "doctor")) throw error;
          if (opts.signal?.aborted) throw error;
          ambiguousTransport = true;
          if (!policy.replaySafe || attempt === policy.maxAttempts - 1) break;
          await wait(ENGRAM_FETCH_BACKOFF_BASE_MS * 2 ** attempt);
          continue;
        }
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

  if ((timedOut || ambiguousTransport) && (policy.operation === "write" || policy.operation === "session-registration")) {
    return { data: null, transportFailure: { operation: policy.operation, outcome: "unknown", timeoutMs: policy.timeoutMs } };
  }
  if (timedOut) return { data: null, transportFailure: { operation: policy.operation, outcome: "timed_out", timeoutMs: policy.timeoutMs } };
  if (refused && (!opts.beforeDispatch || allowGuardedRecovery) && await recoverImplicitEngramServer() && isSafeToReplay(path, method)) {
    return engramFetchResult<TResponse>(path, opts, false);
  }
  throw new EngramUnavailableError();
}

async function engramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  const result = await engramFetchResult<TResponse>(path, opts);
  // Background registration has no native-tool side channel for transport diagnostics.
  if (result.transportFailure?.operation === "session-registration") {
    throw new Error(unreachableMessage(result.transportFailure));
  }
  return result.data;
}

function throwIfAborted(signal?: AbortSignal): void {
  if (!signal?.aborted) return;
  throw signal.reason ?? new DOMException("Engram tool execution was cancelled", "AbortError");
}

function hasPositiveObservationID(data: unknown, status: "saved" | "committed"): boolean {
  if (!data || typeof data !== "object") return false;
  const receipt = data as Record<string, unknown>;
  if (!Number.isSafeInteger(receipt.id) || Number(receipt.id) <= 0) return false;
  // Older successful observation responses had only the numeric ID. Keep that established
  // POST acknowledgement shape, while lookup receipts require the new committed marker.
  return status === "saved" ? (receipt.status === undefined || receipt.status === "saved") : receipt.status === "committed";
}

function isObservationSaveResultMiss(response: Response, data: unknown): boolean {
  return response.status === 404
    && !!data && typeof data === "object"
    && (data as Record<string, unknown>).code === "observation_save_result_not_found"
    && (data as Record<string, unknown>).error === "no committed result found for operation_id";
}

function isAmbiguousObservationSaveReplayStatus(status: number): boolean {
  // The ledger reports a changed replay as 409 and an expired/deleted replay as 410.
  // Either can race an original write acknowledgement, so only these documented ledger
  // outcomes warrant the one final authoritative lookup.
  return status === 409 || status === 410;
}

// Only native mem_save opts in. Its request bytes and both destinations are frozen before
// the first dispatch, so recovery cannot pick up a changed server configuration or payload.
async function postObservationWithReplayRecovery<TResponse>(body: Record<string, unknown>, signal?: AbortSignal): Promise<EngramFetchResult<TResponse>> {
  const operationID = randomUUID();
  const destination = `${ENGRAM_URL}/observations`;
  const lookupDestination = `${ENGRAM_URL}/observations/save-result?${new URLSearchParams({ operation_id: operationID })}`;
  const serialized = JSON.stringify({ ...(redactValue(body) as Record<string, unknown>), operation_id: operationID });
  const deadline = performance.now() + ENGRAM_OBSERVATION_RECOVERY_TIMEOUT_MS;
  const unknown = (): EngramFetchResult<TResponse> => ({
    data: null,
    transportFailure: { operation: "write", outcome: "unknown", timeoutMs: ENGRAM_OBSERVATION_RECOVERY_TIMEOUT_MS, operationID },
  });

  type Attempt = { response?: Response; data?: unknown };
  async function attempt(method: "POST" | "GET"): Promise<Attempt> {
    throwIfAborted(signal);
    const remaining = deadline - performance.now();
    if (remaining <= 0) return {};
    const timeoutMs = Math.min(
      remaining,
      method === "GET" ? ENGRAM_RECOVERY_LOOKUP_TIMEOUT_MS : ENGRAM_WRITE_TIMEOUT_MS,
    );
    const controller = new AbortController();
    let rejectStop: (reason: unknown) => void = () => {};
    const stopped = new Promise<never>((_resolve, reject) => { rejectStop = reject; });
    const onCallerAbort = () => {
      const reason = signal?.reason ?? new DOMException("Engram tool execution was cancelled", "AbortError");
      controller.abort(reason);
      rejectStop(reason);
    };
    const timer = setTimeout(() => {
      const reason = new DOMException("Observation recovery request timed out", "TimeoutError");
      controller.abort(reason);
      rejectStop(reason);
    }, timeoutMs);
    signal?.addEventListener("abort", onCallerAbort, { once: true });
    try {
      const exchange = (async () => {
        const response = await fetch(method === "POST" ? destination : lookupDestination, {
          method,
          headers: method === "POST" ? { "Content-Type": "application/json" } : undefined,
          body: method === "POST" ? serialized : undefined,
          signal: controller.signal,
        });
        if (response.status === 204) return { response, data: null };
        try {
          return { response, data: await response.json() };
        } catch (error) {
          if (signal?.aborted) throw error;
          return { response };
        }
      })();
      return await Promise.race([exchange, stopped]);
    } catch (error) {
      if (signal?.aborted) throw error;
      return {};
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onCallerAbort);
      controller.abort();
    }
  }

  const initial = await attempt("POST");
  if (initial.response?.ok && hasPositiveObservationID(initial.data, "saved")) return { data: initial.data as TResponse };
  if (initial.response && !initial.response.ok) {
    const record = initial.data && typeof initial.data === "object" ? initial.data as Record<string, unknown> : {};
    throw new EngramHttpError(typeof record.error === "string" ? record.error : `Engram request failed with HTTP ${initial.response.status}`, initial.response.status, initial.data);
  }

  const recovered = await attempt("GET");
  if (recovered.response?.ok && hasPositiveObservationID(recovered.data, "committed")) return { data: recovered.data as TResponse };
  if (!recovered.response || !isObservationSaveResultMiss(recovered.response, recovered.data)) return unknown();

  throwIfAborted(signal);
  const backoff = new AbortController();
  const cancelBackoff = () => backoff.abort(signal?.reason);
  signal?.addEventListener("abort", cancelBackoff, { once: true });
  try {
    await waitCancellable(ENGRAM_FETCH_BACKOFF_BASE_MS, backoff.signal);
  } finally {
    signal?.removeEventListener("abort", cancelBackoff);
  }
  throwIfAborted(signal);

  // The typed miss authorizes exactly one byte-identical replay. Only the documented
  // ledger outcomes 409 and 410 remain ambiguous enough to warrant one final lookup.
  const replay = await attempt("POST");
  if (replay.response?.ok && hasPositiveObservationID(replay.data, "saved")) return { data: replay.data as TResponse };
  if (replay.response && !replay.response.ok && !isAmbiguousObservationSaveReplayStatus(replay.response.status)) {
    const record = replay.data && typeof replay.data === "object" ? replay.data as Record<string, unknown> : {};
    throw new EngramHttpError(typeof record.error === "string" ? record.error : `Engram request failed with HTTP ${replay.response.status}`, replay.response.status, replay.data);
  }
  const final = await attempt("GET");
  if (final.response?.ok && hasPositiveObservationID(final.data, "committed")) return { data: final.data as TResponse };
  return unknown();
}

function createMemoryToolTransport(signal?: AbortSignal): { fetch: EngramFetcher; transportFailure: () => EngramTransportFailure | undefined } {
  let failure: EngramTransportFailure | undefined;
  return {
    async fetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
      const result = opts.observationSave
        ? await postObservationWithReplayRecovery<TResponse>(opts.body as Record<string, unknown>, signal)
        : await engramFetchResult<TResponse>(path, { ...opts, signal });
      if (result.transportFailure) failure = result.transportFailure;
      return result.data;
    },
    transportFailure: () => failure,
  };
}

// Diagnostics belong to the capture's context, never the latest active session.
// Pi owns terminal rendering; RPC also supports notify through its UI protocol.
function warnEngramFailure(path: string, error: unknown, ctx?: SessionContext): void {
  try {
    const message = redactPrivateTags(error instanceof Error ? error.message : String(error));
    const warning = `[engram] background capture to ${redactUrlPath(path)} failed: ${message}`;
    if (ctx?.hasUI) {
      // A disposed or incomplete UI must not fall back to raw terminal output.
      ctx.ui?.notify?.(warning, "warning");
      return;
    }
    process.stderr.write(`${warning}\n`);
  } catch {
    // Diagnostics must never break the caller.
  }
}

async function bestEffortEngramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}, ctx?: SessionContext): Promise<TResponse | null> {
  try {
    const result = await engramFetchResult<TResponse>(path, opts);
    if (result.transportFailure) warnEngramFailure(path, new Error(unreachableMessage(result.transportFailure)), ctx);
    return result.data;
  } catch (error) {
    warnEngramFailure(path, error, ctx);
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

async function ensureSessionBestEffort(sessionId: string, sessionProject = project, renew = false, ctx?: SessionContext): Promise<boolean> {
  try {
    await ensureSession(sessionId, sessionProject, engramFetch, renew);
    return true;
  } catch (error) {
    warnSessionProjectConflictOnce(error, ctx);
    return false;
  }
}

// A deterministic startup failure has a cause that does not heal on its own — a foreign or
// legacy server already bound to the port, or a binary that cannot resolve an identity — so
// retrying it on the exponential curve only delays the recheck. Its retry cadence is a fixed
// short window instead; transient failures keep the exponential backoff.
class DeterministicStartupError extends Error {}

// "refused" means we saw proof that nothing is listening; "indeterminate" means the probe
// told us nothing either way. Only "ready" is proof that a server is answering, so nothing
// but "ready" may be read as "a server is already there". "foreign" is a live server owned
// by a different identity; "legacy" is a live server provably too old to report one.
type EngramHealth = "ready" | "refused" | "indeterminate" | "foreign" | "legacy" | "identity_missing";

interface EngramHealthResult {
  status: EngramHealth;
  localInstanceID: string;
  remoteInstanceID: string;
  remoteVersion: string;
}

interface OwnershipEvidence {
  localInstanceID: string;
  remoteInstanceID: string;
  localVersion: string;
  remoteVersion: string;
}

class ForeignOwnershipError extends DeterministicStartupError {
  readonly evidence: OwnershipEvidence;

  constructor(evidence: OwnershipEvidence, message?: string) {
    super(message ?? `Engram server ownership mismatch at ${ENGRAM_URL} (local ID ${evidence.localInstanceID}, remote ID ${evidence.remoteInstanceID}; local version ${evidence.localVersion}, remote version ${evidence.remoteVersion}). WSL2 shared-loopback is a possible cause, not a confirmed diagnosis. Stop the foreign server from its owning environment, or choose a free local port and relaunch Pi with ENGRAM_PORT=<port>. ENGRAM_PORT alone does not override an explicit ENGRAM_URL; unset ENGRAM_URL to use local auto-start. Nothing is spawned or terminated automatically for this mismatch.`);
    this.name = "ForeignOwnershipError";
    this.evidence = evidence;
  }
}

// Instance identity first shipped in v2.0.0-rc.11. A missing identity is legacy only when a
// valid SemVer health version is strictly older than that release; every other shape fails
// closed because it cannot establish ownership.
function isPreIdentityEngramVersion(version: string): boolean {
  const match = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.exec(version);
  if (!match) return false;

  const [major, minor, patch] = match.slice(1, 4).map(Number);
  if (major < 2) return true;
  if (major !== 2 || minor !== 0 || patch !== 0) return false;

  const prerelease = match[4]?.split(".");
  if (!prerelease || prerelease.some((identifier) => /^\d+$/.test(identifier) && identifier.length > 1 && identifier.startsWith("0"))) return false;

  const identityRelease = ["rc", "11"];
  for (let index = 0; index < Math.max(prerelease.length, identityRelease.length); index += 1) {
    const actual = prerelease[index];
    const expected = identityRelease[index];
    if (actual === undefined) return true;
    if (expected === undefined) return false;
    if (actual === expected) continue;

    const actualNumeric = /^\d+$/.test(actual);
    const expectedNumeric = /^\d+$/.test(expected);
    if (actualNumeric && expectedNumeric) return Number(actual) < Number(expected);
    if (actualNumeric !== expectedNumeric) return actualNumeric;
    return actual < expected;
  }
  return false;
}

function localInstanceID(timeoutMs = ENGRAM_STARTUP_TIMEOUT_MS): string {
  const result = spawnSync(ENGRAM_BIN, ["instance-id"], { encoding: "utf8", timeout: Math.max(1, timeoutMs) });
  const id = result.status === 0 ? result.stdout.trim() : "";
  if (!/^[a-f0-9]{32}$/.test(id)) throw new DeterministicStartupError(instanceIDFailureMessage(result));
  return id;
}

// Only a binary that actually runs "instance-id" and answers with an explicit unknown-command
// error predates v2.0.0-rc.11; every other failure shape gets its own diagnosis so a broken
// install is never misreported as an outdated version.
function instanceIDFailureMessage(result: SpawnSyncReturns<string>): string {
  const code = (result.error as NodeJS.ErrnoException | undefined)?.code;
  if (code === "ENOENT") {
    return `The Engram binary "${ENGRAM_BIN}" could not be found. Install Engram, or point ENGRAM_BIN at the current binary.`;
  }
  if (code === "ETIMEDOUT") {
    return `The Engram binary "${ENGRAM_BIN}" did not answer "instance-id" within the startup timeout. Check for a hung or very slow binary, then retry.`;
  }
  if (code !== undefined) {
    return `The Engram binary "${ENGRAM_BIN}" could not be started (spawn error ${code}). Check the binary's path and permissions, then retry.`;
  }
  if (result.status !== 0 && /unknown command/i.test(result.stderr) && /instance-id/i.test(result.stderr)) {
    return `The Engram binary "${ENGRAM_BIN}" does not support "instance-id" and predates v2.0.0-rc.11. Upgrade the binary, or point ENGRAM_BIN at the current one.`;
  }
  return `The Engram binary "${ENGRAM_BIN}" failed to resolve its instance id (exit ${result.status}). Run "${ENGRAM_BIN} instance-id" directly to see the underlying error.`;
}

// The CLI version is context for the legacy-server guidance message, never a gate: a binary
// that cannot report one degrades the message to "unknown" instead of failing startup.
function localEngramVersion(timeoutMs = ENGRAM_VERSION_PROBE_TIMEOUT_MS): string {
  const result = spawnSync(ENGRAM_BIN, ["version"], { encoding: "utf8", timeout: Math.max(1, timeoutMs) });
  const version = result.status === 0 ? result.stdout.trim() : "";
  return version.length > 0 ? version : "unknown";
}

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

async function probeEngramHealth(expectedID = ""): Promise<EngramHealthResult> {
  const result: EngramHealthResult = { status: "indeterminate", localInstanceID: expectedID || "unknown", remoteInstanceID: "unknown", remoteVersion: "unknown" };
  try {
    const res = await fetch(`${ENGRAM_URL}/health`, {
      signal: AbortSignal.timeout(500),
    });
    if (!res.ok) return result;
    if (!expectedID) return { ...result, status: "ready" };
    const health = await res.json() as { version?: unknown; instance_id?: unknown };
    result.remoteVersion = typeof health.version === "string" && health.version.trim().length > 0 ? health.version : "unknown";
    // Evidence is returned with this probe, never stored in shared mutable metadata.
    // A missing identity proves neither ownership nor age. Only a recognized release older
    // than v2.0.0-rc.11 is legacy; current, unknown, and malformed versions fail closed.
    if (typeof health.instance_id !== "string" || health.instance_id.length === 0) {
      return { ...result, status: isPreIdentityEngramVersion(result.remoteVersion) ? "legacy" : "identity_missing" };
    }
    return { ...result, remoteInstanceID: health.instance_id, status: health.instance_id === expectedID ? "ready" : "foreign" };
  } catch (error) {
    if (isTimeoutError(error)) return result;
    if (isConnectionRefusedError(error)) return { ...result, status: "refused" };
    return result;
  }
}

async function isEngramRunning(expectedID = ""): Promise<boolean> {
  return (await probeEngramHealth(expectedID)).status === "ready";
}

let localEngramInstanceID = "";

// Approved wording (engram#1255). Versions come from the /health body the probe already
// fetched and from a short `version` spawn; either side that cannot report one degrades to
// "unknown".
function legacyEngramServerMessage(engramServerVersion: string): string {
  return `Engram server at ${ENGRAM_URL} predates instance identity (server ${engramServerVersion}, CLI ${localEngramVersion()}). An older Engram left running by the upgrade is the likely cause: stop it and start the current binary. Nothing is terminated automatically and memory retries on its own. If nothing was upgraded recently, treat this port as occupied by an unrelated process.`;
}

function missingInstanceIdentityMessage(engramServerVersion: string): string {
  return `Engram server at ${ENGRAM_URL} did not report its instance identity (server ${engramServerVersion}). Its version is not proven older than v2.0.0-rc.11, so this response is incompatible with identity verification. Verify or upgrade the server, then retry. Nothing is terminated automatically and memory retries on its own.`;
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
  const sessionId = getSessionId(ctx);
  engramSelfHealContexts.set(typeof sessionId === "string" ? sessionId : ctx, ctx);
  if (engramSelfHealInFlight) return;
  engramSelfHealInFlight = true;
  void (async () => {
    try {
      for (let attempt = 0; attempt < ENGRAM_SELF_HEAL_MAX_ATTEMPTS; attempt += 1) {
        await waitUnref(ENGRAM_SELF_HEAL_INTERVAL_MS);
        if (await isEngramRunning(localEngramInstanceID)) {
          clearStartupRetryWindow();
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

// A tool call may stop awaiting shared startup work, but must not cancel it for other callers.
function awaitWithAbort<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise;
  const cancelled = () => {
    // Preserve an explicit caller abort reason through the outer tool wrapper. A bare
    // AbortController supplies only Node's generic DOMException, so retain the existing
    // stable message while still reporting it as an AbortError.
    if (signal.reason instanceof Error && signal.reason.message !== "This operation was aborted") return signal.reason;
    return new DOMException("Engram tool execution was cancelled", "AbortError");
  };
  if (signal.aborted) {
    void promise.then(
      () => undefined,
      () => undefined,
    );
    return Promise.reject(cancelled());
  }
  return new Promise<T>((resolve, reject) => {
    const cleanup = () => signal.removeEventListener("abort", onAbort);
    const onAbort = () => {
      cleanup();
      reject(cancelled());
    };
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (value) => {
        cleanup();
        resolve(value);
      },
      (error: unknown) => {
        cleanup();
        reject(error);
      },
    );
  });
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
async function waitForEngramReadiness(signal: AbortSignal, deadline: number, expectedID = ""): Promise<void> {
  while (Date.now() < deadline) {
    if (signal.aborted) throw new Error(`Engram startup readiness wait for ${ENGRAM_URL} was cancelled`);
    if ((await probeEngramHealth(expectedID)).status === "ready") return;
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

function spawnAndWaitForEngram(deadline: number, expectedID = ""): Promise<void> {
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
        // Opt into cloud autosync like the Claude Code and Codex launchers; the
        // server skips it on its own when no cloud server or token is configured.
        env: { ...process.env, ENGRAM_CLOUD_AUTOSYNC: "1" },
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
      void waitForEngramReadiness(readiness.signal, deadline, expectedID).then(
        () => settle(),
        (error) => settle(error instanceof Error ? error : new Error(String(error))),
      );
    });
  });
}

async function initializeEngramServer(): Promise<void> {
  if (CONFIGURED_ENGRAM_URL !== undefined) return;
  const deadline = Date.now() + ENGRAM_STARTUP_TIMEOUT_MS;
  const instanceID = localEngramInstanceID = localInstanceID(Math.max(1, deadline - Date.now()));
  const health = await probeEngramHealth(instanceID);
  if (health.status === "ready") return;
  // Both deterministic owner outcomes fail closed before any spawn: a server we do not own
  // is never adopted and never terminated. Only the message and the retry cadence differ.
  if (health.status === "foreign") throw new ForeignOwnershipError({
    localInstanceID: health.localInstanceID, remoteInstanceID: health.remoteInstanceID,
    localVersion: localEngramVersion(), remoteVersion: health.remoteVersion,
  });
  if (health.status === "legacy") throw new DeterministicStartupError(legacyEngramServerMessage(health.remoteVersion));
  if (health.status === "identity_missing") throw new DeterministicStartupError(missingInstanceIdentityMessage(health.remoteVersion));

  // Only "ready" proves a server is answering. Every other outcome — a definitive refusal, an
  // aborted probe, a DNS failure, an error shape we do not recognize — means we have no
  // server, so launch one. Reading an inconclusive probe as "a server must be starting"
  // instead is what let a cold machine burn the whole startup budget polling a port nobody
  // was ever going to bind.
  try {
    await spawnAndWaitForEngram(deadline, instanceID);
  } catch (error) {
    // An inconclusive probe leaves room for another Pi process to already own the port, which
    // is exactly what makes our child fail. Give that instance the rest of the shared
    // deadline before reporting failure. A definitive refusal gets no such grace: nothing was
    // listening when we looked, so there is no other instance to wait for.
    if (health.status !== "indeterminate") throw error;
    const readiness = new AbortController();
    try {
      await waitForEngramReadiness(readiness.signal, deadline, instanceID);
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
// Deterministic failures are bounded by a fixed short window instead: their cause does not
// heal with time, so doubling the wait would only delay the user's fix.
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
      startupRetryAt = Date.now() + (startupFailure instanceof DeterministicStartupError
        ? ENGRAM_DETERMINISTIC_RETRY_MS
        : startupBackoffMs(startupFailures));
      throw startupFailure;
    },
  );
  return initialization;
}

// The self-heal probe unifies with the startup retry window: once it observes the expected
// identity again, the window is cleared so the next tool call reconnects immediately instead
// of waiting out the rest of a fixed or exponential recheck.
function clearStartupRetryWindow(): void {
  startupFailures = 0;
  startupRetryAt = 0;
  startupFailure = undefined;
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
let pendingRecoveryNotice: { sessionId: string; content: string } | undefined;
let projectResolutionError: string | undefined;
let projectDetectionPending = false;
const observedRuntimeSessionIDs = new Set<string>();
let runtimeSessionIdentityAmbiguous = false;

const knownSessions = new Set<string>();
const registeredSessionProjects = new Map<string, string>();
const sessionRegistrationsInFlight = new Map<string, Promise<unknown>>();
const sessionRegistrationProjects = new Map<string, string>();
const sessionEndingsInFlight = new Map<string, Promise<unknown>>();
// Module graphs loaded by the same Pi realm share only active shutdown deliveries.
// Session-manager identity scopes opaque IDs; no outcome is cached after settlement.
const shutdownFlightsKey = Symbol.for("engram.pi.shutdown-flights");
const realm = globalThis as typeof globalThis & { [shutdownFlightsKey]?: WeakMap<object, Map<string, Promise<void>>> };
const shutdownFlights = realm[shutdownFlightsKey] ??= new WeakMap<object, Map<string, Promise<void>>>();
const lifecycleKey = Symbol.for("engram.pi.session-lifecycle");
type Lifecycle = { epoch: number; closing: boolean; confirmedShutdownID?: string; removeBridgeResponder?: () => void;
  awaitingInitialUserMessage?: boolean; promptTurn?: number; promptProof?: { digest: string; project: string; effectiveID: string };
  currentPrompt?: { content: string; project: string; effectiveID: string } };
const lifecycleRealm = globalThis as typeof globalThis & { [lifecycleKey]?: WeakMap<object, Map<string, Lifecycle>> };
const lifecycles = lifecycleRealm[lifecycleKey] ??= new WeakMap<object, Map<string, Lifecycle>>();
function lifecycle(ctx: SessionContext, id: string): Lifecycle {
  let sessions = lifecycles.get(ctx.sessionManager);
  if (!sessions) { sessions = new Map(); lifecycles.set(ctx.sessionManager, sessions); }
  let state = sessions.get(id);
  if (!state) { state = { epoch: 0, closing: false }; sessions.set(id, state); }
  return state;
}
function assertOpen(state: Lifecycle, epoch: number): void {
  if (state.closing || state.epoch !== epoch) throw new Error("Pi runtime session is closing");
}

function sharedShutdown(manager: object, id: string, deliver: () => Promise<void>): Promise<void> {
  let flights = shutdownFlights.get(manager);
  if (!flights) {
    flights = new Map();
    shutdownFlights.set(manager, flights);
  }
  const existing = flights.get(id);
  if (existing) return existing;
  const active = flights;
  const flight = Promise.resolve().then(deliver).finally(() => {
    if (active.get(id) === flight) active.delete(id);
    if (active.size === 0) shutdownFlights.delete(manager);
  });
  active.set(id, flight);
  return flight;
}
const warnedSessionProjectConflicts = new Set<string>();
const toolCounts = new Map<string, number>();

function sessionProjectConflict(sessionId: string, sessionProject: string): SessionProjectConflictError | undefined {
  const ownerProject = registeredSessionProjects.get(sessionId) || sessionRegistrationProjects.get(sessionId);
  return ownerProject && ownerProject !== sessionProject
    ? new SessionProjectConflictError(sessionId, ownerProject, sessionProject)
    : undefined;
}

function warnSessionProjectConflictOnce(error: unknown, ctx?: SessionContext): void {
  if (!(error instanceof SessionProjectConflictError)) return;
  const key = `${error.sessionId}\u0000${error.ownerProject}\u0000${error.requestedProject}`;
  if (warnedSessionProjectConflicts.has(key)) return;
  warnedSessionProjectConflicts.add(key);
  warnEngramFailure("/sessions", error, ctx);
}

const EFFECTIVE_SESSION_ENTRY = "engram-effective-session";
const REJECTED_SESSION_ENTRY = "engram-rejected-effective-session";
const effectiveSessionRegistrations = new Map<string, Promise<string>>();

function pendingEffectiveSession(ctx: SessionContext, runtimeID: string, effectiveID: string): boolean {
  const branch = ctx.sessionManager.getBranch?.() || [];
  for (let i = branch.length - 1; i >= 0; i--) {
    const entry = branch[i];
    if (entry.type !== "custom") continue;
    const data = entry.data as { runtimeID?: string; effectiveID?: string; pending?: boolean } | undefined;
    if (data?.runtimeID !== runtimeID || data.effectiveID !== effectiveID) continue;
    if (entry.customType === REJECTED_SESSION_ENTRY) return false;
    if (entry.customType === EFFECTIVE_SESSION_ENTRY) return data.pending === true;
  }
  return false;
}

function pendingEffectiveSessionProject(ctx: SessionContext, runtimeID: string, effectiveID: string): string | undefined {
  const branch = ctx.sessionManager.getBranch?.() || [];
  for (let i = branch.length - 1; i >= 0; i--) {
    const entry = branch[i];
    if (entry.type !== "custom" || entry.customType !== EFFECTIVE_SESSION_ENTRY) continue;
    const data = entry.data as { runtimeID?: string; effectiveID?: string; project?: string } | undefined;
    if (data?.runtimeID === runtimeID && data.effectiveID === effectiveID) return data.project;
  }
  return undefined;
}

function effectiveSessionID(ctx: SessionContext, runtimeID: string): string {
  const branch = ctx.sessionManager.getBranch?.() || [];
  for (let i = branch.length - 1; i >= 0; i--) {
    const entry = branch[i];
    if (entry.type !== "custom" || entry.customType !== EFFECTIVE_SESSION_ENTRY) continue;
    const data = entry.data as { runtimeID?: string; effectiveID?: string } | undefined;
    if (data?.runtimeID === runtimeID && typeof data.effectiveID === "string"
      && (data.effectiveID === runtimeID || data.effectiveID.startsWith(`${runtimeID}:resume:`))) return data.effectiveID;
  }
  return runtimeID;
}

// The v3.0.0 tagged server supports core-selected resume identities. Earlier
// releases need explicit advertisement rather than an unverified version guess.
function supportsRootSessionResume(health: unknown): boolean {
  if (!health || typeof health !== "object" || Array.isArray(health)) return false;
  const { capabilities, version } = health as { capabilities?: unknown; version?: unknown };
  if (capabilities !== undefined) {
    if (!capabilities || typeof capabilities !== "object" || Array.isArray(capabilities)) return false;
    if (Object.prototype.hasOwnProperty.call(capabilities, "root_session_resume")) {
      return (capabilities as Record<string, unknown>).root_session_resume === true;
    }
  }
  if (typeof version !== "string") return false;
  const release = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(version);
  return !!release && Number(release[1]) >= 3;
}

function rootResumeCompatibilityError(health: unknown): Error {
  const version = health && typeof health === "object" && typeof (health as { version?: unknown }).version === "string"
    ? (health as { version: string }).version : "unknown";
  return new Error(`gentle-engram 0.4.0 cannot resume this Pi session with Engram core ${version}. Requires capabilities.root_session_resume: true or a supported release >= 3.0.0 without an explicit negative or malformed capability. Upgrade the Engram core and restart its server, then retry. No resumed identity was adopted.`);
}

async function registerEffectiveSession(ctx: SessionContext, sessionProject: string, appendEntry: ExtensionAPI["appendEntry"] | undefined, fetch: EngramFetcher = engramFetch): Promise<string> {
  const runtimeID = requireRuntimeSessionID(ctx);
  const state = lifecycle(ctx, runtimeID);
  const epoch = state.epoch;
  assertOpen(state, epoch);
  if (knownSessions.has(`\u0000closing:${runtimeID}`)) throw new Error(`Pi runtime session ${runtimeID} is closing`);
  const registrationKey = `${sessionProject}\u0000${runtimeID}`;
  const existing = effectiveSessionRegistrations.get(registrationKey);
  if (existing) {
    const effectiveID = await existing;
    assertOpen(state, epoch);
    if (knownSessions.has(`\u0000closing:${runtimeID}`) || knownSessions.has(`\u0000closing:${effectiveID}`)) throw new Error(`Pi runtime session ${runtimeID} is closing`);
    return effectiveID;
  }
  const registration = (async () => {
    const persistedID = effectiveSessionID(ctx, runtimeID);
    const canPersist = !!appendEntry && !!ctx.sessionManager.getBranch;
    traceSessionRegistration({ stage: "context", append_entry: !!appendEntry, get_branch: !!ctx.sessionManager.getBranch, persisted: persistedID === runtimeID ? "root" : "continuation" });
    if (pendingEffectiveSession(ctx, runtimeID, persistedID)) {
      const owner = pendingEffectiveSessionProject(ctx, runtimeID, persistedID);
      if (!owner) throw new Error(`Cannot confirm project ownership for pending Pi session ${persistedID}`);
      if (owner !== sessionProject) throw new SessionProjectConflictError(persistedID, owner, sessionProject);
    }
    const register = async (id: string, resume: boolean, requireResume = false): Promise<string> => {
      const conflict = sessionProjectConflict(id, sessionProject);
      if (conflict) throw conflict;
      const key = `${sessionProject}:${id}`;
      sessionRegistrationProjects.set(id, sessionProject);
      const delivery = (async () => {
        let acknowledgement: { id?: unknown; status?: unknown } | null;
        let health: unknown;
        let resumeSupport: boolean | undefined;
        const body = { id, project: sessionProject, directory, ownership_mode: "project_owned", resume };
        const validateCapability = resume ? async () => {
          // Missing/unreadable health is not support evidence, but fresh registration
          // without resume remains safe. Registration itself retains transport errors.
          try { health = await fetch("/health"); }
          catch { health = undefined; }
          assertOpen(state, epoch);
          const supported = supportsRootSessionResume(health);
          if (resumeSupport !== undefined && supported !== resumeSupport) {
            throw new Error("Engram core root resume support changed during session registration. Retry once core health is stable; the previous registration may have succeeded, but its acknowledgement was not confirmed.");
          }
          if (requireResume && !supported) throw rootResumeCompatibilityError(health);
          resumeSupport = supported;
          body.resume = supported;
        } : undefined;
        try {
          acknowledgement = await fetch("/sessions", {
            method: "POST", beforeDispatch: validateCapability, body,
            onDispatch: () => traceSessionRegistration({ stage: "dispatch", requested: id === runtimeID ? "root" : "continuation", resume: body.resume }),
          });
        } catch (error) {
          traceSessionRegistration({ stage: "rejection", http_status: error instanceof EngramHttpError ? error.status : null, reason: error instanceof EngramHttpError && (error.data as { code?: string } | null)?.code === "session_already_ended" ? "session_already_ended" : "other" });
          if (resume && !body.resume && error instanceof EngramHttpError && error.status === 409
            && (error.data as { code?: string } | null)?.code === "session_already_ended") {
            throw rootResumeCompatibilityError(health);
          }
          throw sessionProjectConflictFromResponse(error, id, sessionProject, body.resume ? runtimeID : undefined) || error;
        }
        const effectiveID = acknowledgement?.id;
        traceSessionRegistration({ stage: "acknowledgement", effective: acknowledgement?.status === "created" && effectiveID === runtimeID ? "root" : acknowledgement?.status === "created" && typeof effectiveID === "string" && effectiveID.startsWith(`${runtimeID}:resume:`) ? "continuation" : "invalid" });
        if (acknowledgement?.status !== "created" || typeof effectiveID !== "string"
          || !(effectiveID === runtimeID || effectiveID.startsWith(`${runtimeID}:resume:`))) {
          throw new Error(`gentle-engram could not confirm session registration for Pi runtime session ${runtimeID}: invalid acknowledgement`);
        }
        if (effectiveID !== persistedID && !canPersist) {
          throw new Error("Cannot persist the acknowledged resumed Pi session identity");
        }
        registeredSessionProjects.set(effectiveID, sessionProject);
        knownSessions.add(`${sessionProject}:${effectiveID}`);
        state.confirmedShutdownID = undefined;
        // This marker tracks cleanup delivery, not a client-selected reservation.
        // Read again after acknowledgement so a peer graph's synchronous append is visible.
        const alreadyPersisted = effectiveSessionID(ctx, runtimeID) === effectiveID
          && pendingEffectiveSession(ctx, runtimeID, effectiveID)
          && pendingEffectiveSessionProject(ctx, runtimeID, effectiveID) === sessionProject;
        if ((effectiveID !== runtimeID || persistedID !== runtimeID) && appendEntry && !alreadyPersisted) appendEntry(EFFECTIVE_SESSION_ENTRY, {
          runtimeID, effectiveID, pending: true, project: sessionProject,
        });
        assertOpen(state, epoch);
        traceSessionRegistration({ stage: "adoption", effective: effectiveID === runtimeID ? "root" : "continuation" });
        return effectiveID;
      })();
      sessionRegistrationsInFlight.set(key, delivery);
      try { return await delivery; }
      finally {
        if (sessionRegistrationsInFlight.get(key) === delivery) {
          sessionRegistrationsInFlight.delete(key);
          if (!registeredSessionProjects.has(id)) sessionRegistrationProjects.delete(id);
        }
      }
    };
    try {
      // Re-register legacy UUID mappings as-is; never resume a continuation as a new root.
      return await register(persistedID, canPersist && persistedID === runtimeID);
    } catch (error) {
      if (error instanceof SessionProjectConflictError && persistedID !== runtimeID && appendEntry
        && error.ownerProject !== pendingEffectiveSessionProject(ctx, runtimeID, persistedID)) {
        appendEntry(REJECTED_SESSION_ENTRY, { runtimeID, effectiveID: persistedID });
      }
      if (persistedID === runtimeID || !(error instanceof EngramHttpError) || error.status !== 409
        || (error.data as { code?: string } | null)?.code !== "session_already_ended") throw error;
      assertOpen(state, epoch);
      return register(runtimeID, canPersist, true);
    }
  })();
  effectiveSessionRegistrations.set(registrationKey, registration);
  try {
    const effectiveID = await registration;
    assertOpen(state, epoch);
    if (knownSessions.has(`\u0000closing:${runtimeID}`) || knownSessions.has(`\u0000closing:${effectiveID}`)) throw new Error(`Pi runtime session ${runtimeID} is closing`);
    return effectiveID;
  } finally { if (effectiveSessionRegistrations.get(registrationKey) === registration) effectiveSessionRegistrations.delete(registrationKey); }
}

// A runtime session is owned by exactly one project. Explicitly targeted writes to another
// project use a derived satellite session owned by that project, so ownership never changes.
const satelliteRegistrations = new Map<string, Promise<string>>();
const satelliteSessions = new Map<string, Set<string>>();

function satelliteSessionID(runtimeID: string, targetProject: string): string {
  return `${runtimeID}@${targetProject}`;
}

// The project that owns (or will own) the runtime session. Undefined means ownership is not
// established, so an explicit project may still claim the runtime session itself.
function runtimeSessionOwner(ctx: SessionContext, runtimeID: string): string | undefined {
  const persistedID = effectiveSessionID(ctx, runtimeID);
  for (const id of [persistedID, runtimeID]) {
    const owner = registeredSessionProjects.get(id) || sessionRegistrationProjects.get(id);
    if (owner) return owner;
  }
  for (const key of effectiveSessionRegistrations.keys()) {
    if (key.endsWith(`\u0000${runtimeID}`)) return key.slice(0, key.length - runtimeID.length - 1);
  }
  // An ownerless pending reservation stays unresolved so registration keeps failing closed.
  if (pendingEffectiveSession(ctx, runtimeID, persistedID)) return pendingEffectiveSessionProject(ctx, runtimeID, persistedID);
  return projectDetectionPending || projectResolutionError || project === "unknown" ? undefined : project;
}

async function registerSatelliteSession(ctx: SessionContext, targetProject: string, fetch: EngramFetcher = engramFetch): Promise<string> {
  const runtimeID = requireRuntimeSessionID(ctx);
  const state = lifecycle(ctx, runtimeID);
  const epoch = state.epoch;
  const assertRuntimeOpen = () => {
    assertOpen(state, epoch);
    if (knownSessions.has(`\u0000closing:${runtimeID}`)) throw new Error(`Pi runtime session ${runtimeID} is closing`);
  };
  assertRuntimeOpen();
  const rootID = satelliteSessionID(runtimeID, targetProject);
  let registration = satelliteRegistrations.get(rootID);
  if (!registration) {
    registration = (async () => {
      // Old servers may ignore isolated and normalize omitted directory to their cwd.
      // Require an explicit capability on every registration flight, never a version guess.
      const validateCapability = async () => {
        const health = await fetch("/health") as { capabilities?: { isolated_session_registration?: unknown } } | null;
        if (health?.capabilities?.isolated_session_registration !== true) {
          throw new Error("Upgrade the Engram server to one advertising capabilities.isolated_session_registration: true in GET /health before saving to another project. No satellite session was registered.");
        }
        assertRuntimeOpen();
      };
      let acknowledgement: { id?: unknown; status?: unknown } | null;
      try {
        // Every write renews the lease; core validates isolation before selecting/renewing a continuation.
        acknowledgement = await fetch("/sessions", { method: "POST", beforeDispatch: validateCapability, body: {
          id: rootID, project: targetProject, ownership_mode: "project_owned", resume: true, isolated: true,
        } });
      } catch (error) {
        throw sessionProjectConflictFromResponse(error, rootID, targetProject, rootID) || error;
      }
      const effectiveID = acknowledgement?.id;
      if (acknowledgement?.status !== "created" || typeof effectiveID !== "string"
        || !(effectiveID === rootID || (effectiveID.startsWith(`${rootID}:resume:`)
          && /^[0-9]+$/.test(effectiveID.slice(`${rootID}:resume:`.length))))) {
        throw new Error(`gentle-engram could not confirm satellite session registration ${rootID} for project ${targetProject}: invalid acknowledgement`);
      }
      registeredSessionProjects.set(effectiveID, targetProject);
      knownSessions.add(`${targetProject}:${effectiveID}`);
      const satellites = satelliteSessions.get(runtimeID) || new Set<string>();
      satellites.add(effectiveID);
      satelliteSessions.set(runtimeID, satellites);
      return effectiveID;
    })();
    satelliteRegistrations.set(rootID, registration);
  }
  try {
    const effectiveID = await registration;
    assertRuntimeOpen();
    return effectiveID;
  } finally {
    if (satelliteRegistrations.get(rootID) === registration) satelliteRegistrations.delete(rootID);
  }
}

async function endSatelliteSessions(runtimeID: string, ctx: SessionContext): Promise<void> {
  await Promise.all([...satelliteRegistrations.entries()]
    .filter(([rootID]) => rootID.startsWith(`${runtimeID}@`))
    .map(([, registration]) => registration.catch(() => undefined)));
  const satellites = satelliteSessions.get(runtimeID);
  satelliteSessions.delete(runtimeID);
  for (const satelliteID of satellites || []) {
    await bestEffortEngramFetch(`/sessions/${encodeURIComponent(satelliteID)}/end`, { method: "POST", body: { summary: "" } }, ctx);
    forgetKnownSession(satelliteID);
  }
}

async function ensureSession(sessionId: string, sessionProject = project, fetch: EngramFetcher = engramFetch, renew = false): Promise<void> {
  const key = `${sessionProject}:${sessionId}`;
  if (!sessionId) return;
  if (knownSessions.has(`\u0000closing:${sessionId}`)) throw new Error(`Pi runtime session ${sessionId} is closing`);
  const conflict = sessionProjectConflict(sessionId, sessionProject);
  if (conflict) throw conflict;
  if (!renew && knownSessions.has(key)) return;

  const existingRegistration = sessionRegistrationsInFlight.get(key);
  if (existingRegistration) { await existingRegistration; return; }

  const registration = (async () => {
    const body: SessionBody = { id: sessionId, project: sessionProject, directory, ownership_mode: "project_owned" };
    let acknowledgement: unknown;
    try {
      acknowledgement = await fetch("/sessions", { method: "POST", body });
    } catch (error) {
      throw sessionProjectConflictFromResponse(error, sessionId, sessionProject) || error;
    }
    if (acknowledgement === null) {
      throw new Error(`gentle-engram could not confirm session registration for Pi runtime session ${sessionId}`);
    }
    registeredSessionProjects.set(sessionId, sessionProject);
    knownSessions.add(key);
  })();
  sessionRegistrationsInFlight.set(key, registration);
  sessionRegistrationProjects.set(sessionId, sessionProject);

  try {
    await registration;
  } finally {
    if (sessionRegistrationsInFlight.get(key) === registration) {
      sessionRegistrationsInFlight.delete(key);
      if (!registeredSessionProjects.has(sessionId)) sessionRegistrationProjects.delete(sessionId);
    }
  }
}

async function detectServerProject(cwd: string, fetch: EngramFetcher = engramFetch, signal?: AbortSignal): Promise<CurrentProjectResponse | undefined> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      const detected = await fetch<CurrentProjectResponse>(`/project/current${queryString({ cwd })}`, { signal });
      if (detected) return detected;
    } catch (error) {
      if (error instanceof EngramHttpError && error.status === 404) {
        return detectLocalConfigProject(cwd) || projectCurrentUnsupportedError(cwd);
      }
    }
    if (attempt < 4) await awaitWithAbort(wait(200), signal);
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
  projectResolutionError = projectResolutionMessage(detected);
  return false;
}

function projectResolutionMessage(detected: CurrentProjectResponse, listChoices = false): string {
  const choices = detected.available_projects?.length ? ` Available projects: ${detected.available_projects.join(", ")}.` : "";
  const hint = detected.error_hint || detected.warning;
  return hint ? `${hint}${listChoices ? choices : ""}` : `Engram project detection did not resolve a project.${choices}`;
}

const WRITE_TARGET_TOOLS = new Set(["mem_save", "mem_save_prompt", "mem_session_summary"]);

// Resolves an explicit `cwd` write target through the same server detection as /project/current.
// It never guesses: unresolved or ambiguous detection and a disagreeing `project` both fail.
async function resolveExplicitWriteTarget(params: Record<string, unknown>, fetch: EngramFetcher): Promise<{ project: string } | undefined> {
  const cwd = typeof params.cwd === "string" ? params.cwd.trim() : "";
  if (!cwd) return undefined;
  let detected: CurrentProjectResponse | null;
  try {
    detected = await fetch<CurrentProjectResponse>(`/project/current${queryString({ cwd })}`);
  } catch (error) {
    if (!(error instanceof EngramHttpError && error.status === 404)) throw error;
    detected = detectLocalConfigProject(cwd) || projectCurrentUnsupportedError(cwd);
  }
  const resolved = detected ? isSafeDetectedProject(detected) : undefined;
  if (!resolved) {
    const reason = detected ? projectResolutionMessage(detected, true) : "Engram project detection is unavailable.";
    throw new Error(`Cannot resolve a write project from cwd ${cwd}: ${reason}`);
  }
  const requested = typeof params.project === "string" ? params.project.trim() : "";
  if (requested && requested.toLowerCase() !== resolved.toLowerCase()) {
    throw new Error(`Explicit project ${requested} does not match project ${resolved} resolved from cwd ${cwd}. Pass only one of project or cwd, or make them agree.`);
  }
  return { project: resolved };
}

async function refreshProjectDetection(cwd: string, fetch: EngramFetcher = engramFetch, signal?: AbortSignal): Promise<void> {
  if (!projectDetectionPending && !projectResolutionError) return;
  applyDetectedProject(await detectServerProject(cwd, fetch, signal));
}

function hasKnownSession(sessionId: string): boolean {
  return [...knownSessions].some((key) => key !== `\u0000closing:${sessionId}` && key.endsWith(`:${sessionId}`));
}

function forgetKnownSession(sessionId: string): void {
  knownSessions.delete(sessionId);
  registeredSessionProjects.delete(sessionId);
  sessionRegistrationProjects.delete(sessionId);
  for (const key of knownSessions) {
    if (key !== `\u0000closing:${sessionId}` && key.endsWith(`:${sessionId}`)) knownSessions.delete(key);
  }
}

function hasSessionRegistrationInFlight(sessionId: string): boolean {
  return [...sessionRegistrationsInFlight.keys()].some((key) => key.endsWith(`:${sessionId}`));
}

async function waitForSessionRegistration(sessionId: string, propagateFailure = false): Promise<void> {
  const registrations = [...sessionRegistrationsInFlight.entries()]
    .filter(([key]) => key.endsWith(`:${sessionId}`))
    .map(([, registration]) => propagateFailure ? registration : registration.catch(() => undefined));
  await Promise.all(registrations);
}

async function endRegisteredSessionOnce(sessionId: string, end: () => Promise<unknown>, persistedPending = false, requireConfirmedRegistration = false): Promise<unknown> {
  const existing = sessionEndingsInFlight.get(sessionId);
  if (existing) return existing;

  const registrationWasInFlight = hasSessionRegistrationInFlight(sessionId);
  const ending = (async () => {
    await waitForSessionRegistration(sessionId, requireConfirmedRegistration);
    if (requireConfirmedRegistration && !registeredSessionProjects.has(sessionId)) {
      throw new Error(`Cannot end Pi session ${sessionId} without confirmed local project ownership`);
    }
    if (!registrationWasInFlight && !hasKnownSession(sessionId) && !persistedPending) return null;
    try {
      return await end();
    } finally {
      forgetKnownSession(sessionId);
    }
  })();
  sessionEndingsInFlight.set(sessionId, ending);
  try {
    return await ending;
  } finally {
    if (sessionEndingsInFlight.get(sessionId) === ending) sessionEndingsInFlight.delete(sessionId);
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
}

// Startup failures reach the agent as prose, so give every one of them the same shape and
// the same actionable prefix instead of leaking a raw spawn or readiness message.
function normalizeInitializationError(error: unknown): Error {
  const message = error instanceof Error ? error.message : String(error);
  const normalized = `gentle-engram could not initialize the Engram memory provider at ${ENGRAM_URL}: ${message}. Run mem_doctor or start Engram manually, and verify ENGRAM_URL/ENGRAM_PORT/ENGRAM_BIN.`;
  return error instanceof ForeignOwnershipError
    ? new ForeignOwnershipError(error.evidence, normalized)
    : new Error(normalized);
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

// Compaction may arrive with a stale context after Pi has rebuilt its lifecycle objects. Retain
// every observed opaque ID and permanently reject compaction once identity is uncertain.
const observedSessionContexts = new Map<string, SessionContext>();
function observeRuntimeSessionID(ctx: SessionContext): string | undefined {
  try {
    const sessionId = getSessionId(ctx);
    if (!sessionId || sessionId.trim().length === 0) {
      runtimeSessionIdentityAmbiguous = true;
      return undefined;
    }
    if (observedRuntimeSessionIDs.size > 0 && !observedRuntimeSessionIDs.has(sessionId)) {
      runtimeSessionIdentityAmbiguous = true;
    }
    observedRuntimeSessionIDs.add(sessionId);
    observedSessionContexts.set(sessionId, ctx);
    return sessionId;
  } catch {
    runtimeSessionIdentityAmbiguous = true;
    return undefined;
  }
}

function soleActiveRuntimeSessionID(): string | undefined {
  return !runtimeSessionIdentityAmbiguous && observedRuntimeSessionIDs.size === 1
    ? [...observedRuntimeSessionIDs][0]
    : undefined;
}

function soleObservedRuntimeSessionID(): string | undefined {
  return observedRuntimeSessionIDs.size === 1 ? [...observedRuntimeSessionIDs][0] : undefined;
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
    cwd: optionalString("Optional directory whose Engram project receives this write; must agree with project when both are set"),
    capture_prompt: optionalBoolean("Capture the available session/project prompt for this save (default true); does not disable lifecycle capture"),
  }),
  mem_update: Type.Object({
    id: Type.Number({ description: "Observation ID to update" }),
    expected_project: Type.String({ description: "Explicit expected owner of the observation" }),
    title: optionalString("New title"),
    content: optionalString("New content"),
    type: optionalString("New type/category"),
    scope: optionalString("New scope: project, personal, or global"),
    topic_key: optionalString("New topic key"),
  }),
  mem_delete: Type.Object({
    id: Type.Number({ description: "Observation ID to delete" }),
    expected_project: Type.String({ description: "Explicit expected owner of the observation" }),
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
    cwd: optionalString("Optional directory whose Engram project receives this prompt; must agree with project when both are set"),
  }),
  mem_session_summary: Type.Object({
    content: Type.String({ description: "Full session summary" }),
    project: optionalString("Optional project to use when automatic detection is unavailable"),
    cwd: optionalString("Optional directory whose Engram project receives this summary; must agree with project when both are set"),
  }),
  mem_context: Type.Object({
    project: optionalString("Filter by project"),
    scope: optionalString("Filter observations by scope: project, personal, or global. Omit to apply no scope filter."),
    max_bytes: Type.Optional(Type.Integer({ minimum: 1, description: "Maximum context output size in bytes; must be a positive integer; enforced by Engram core" })),
    compact: optionalBoolean("Use Engram core's compact context formatting"),
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
  mem_list_projects: Type.Object({}),
  mem_pin: Type.Object({
    id: Type.Number({ description: "Observation ID to pin" }),
  }),
  mem_unpin: Type.Object({
    id: Type.Number({ description: "Observation ID to unpin" }),
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

async function archiveCompactionSummary(sessionId: string, summary: string, runtimeID: string, ctx?: SessionContext): Promise<string> {
  try {
    // Ambiguity is checked against Pi's host identity; attribution uses Engram's effective ID.
    if (soleActiveRuntimeSessionID() !== runtimeID) return ArchiveOutcome.Unavailable;
    const result = await engramFetchResult("/observations", {
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
    return result.transportFailure?.outcome === "unknown" ? ArchiveOutcome.Unknown : ArchiveOutcome.Confirmed;
  } catch (error) {
    warnEngramFailure("/observations", error, ctx);
    return ArchiveOutcome.Failed;
  }
}

async function loadCompactionRecoveryContext(sessionId: string, ctx?: SessionContext): Promise<string | undefined> {
  try {
    return (await engramFetchResult<ContextResponse>(`/context/compaction${queryString({ session_id: sessionId })}`)).data?.context;
  } catch (error) {
    warnEngramFailure("/context/compaction", error, ctx);
    return undefined;
  }
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

// Per-invocation evidence stays separate from model parameters and host state.
const callerAppendAvailability = new WeakMap<Record<string, unknown>, boolean | null>();

const CALLER_NEXT_STEPS = [
  "No action required; a later write is not guaranteed.",
  "Collect caller context without registering a session.",
  "Verify support and availability of the caller diagnostic.",
  "Use the normal runtime registration path; diagnosis does not register sessions.",
  "Inspect the runtime binding; diagnosis does not repair it.",
  "Resume through a host that can persist effective identity; do not reopen the ended session.",
];
const CALLER_REASONS = ["caller_context_missing", "caller_binding_unavailable", "session_not_registered", "effective_session_missing", "session_project_conflict", "effective_mapping_invalid", "host_context_missing", "resume_required", "ended_session_without_persistence", "binding_observed"];

function callerBindingProjection(value: unknown) {
  const fallback = { status: "unknown", root_state: "unknown", effective_state: "unknown", reason_code: "caller_binding_unavailable", safe_next_step: CALLER_NEXT_STEPS[2], write_success_guaranteed: false };
  if (!value || typeof value !== "object" || Array.isArray(value)) return fallback;
  const data = value as Record<string, unknown>;
  const states = ["active", "ended", "missing", "unknown"];
  if (["status", "root_state", "effective_state", "reason_code", "safe_next_step"].some(key => typeof data[key] !== "string")
    || !["ok", "warning", "blocked", "unknown"].includes(String(data.status))
    || !states.includes(String(data.root_state)) || !states.includes(String(data.effective_state))
    || !CALLER_REASONS.includes(String(data.reason_code)) || !CALLER_NEXT_STEPS.includes(String(data.safe_next_step))
    || data.write_success_guaranteed !== false) return fallback;
  return { status: data.status, root_state: data.root_state, effective_state: data.effective_state,
    reason_code: data.reason_code, safe_next_step: data.safe_next_step, write_success_guaranteed: false };
}

function callerBindingStatus(data: unknown): string | undefined {
  return (data as { caller_binding?: { status?: string } } | null)?.caller_binding?.status;
}

function callerBindingContext(ctx: SessionContext, target: string, appendAvailable: boolean | null) {
  let runtimeID: string | undefined;
  let effectiveID: string | undefined;
  let branchAvailable: boolean | null = null;
  try {
    const id = ctx.sessionManager?.getSessionId?.();
    if (typeof id === "string" && id.trim()) runtimeID = id;
  } catch { /* Missing host evidence is not a healthy binding. */ }
  try {
    const manager = ctx.sessionManager;
    if (manager) {
      branchAvailable = typeof manager.getBranch === "function";
      if (runtimeID) {
        // Match the write path, but never mutate registration or mapping entries.
        effectiveID = effectiveSessionID(ctx, runtimeID);
        const owner = pendingEffectiveSessionProject(ctx, runtimeID, effectiveID);
        if ((owner && owner.toLowerCase() !== target.toLowerCase())
          || (!owner && project.toLowerCase() !== target.toLowerCase())) effectiveID = undefined;
      }
    }
  } catch { branchAvailable = null; effectiveID = undefined; }
  return { project: target, runtime_session_id: runtimeID, effective_session_id: effectiveID,
    host_context: { append_entry_available: appendAvailable, branch_available: branchAvailable } };
}

async function callMemoryTool(toolName: string, params: Record<string, unknown>, ctx: SessionContext, fetch: EngramFetcher = engramFetch, appendEntry?: ExtensionAPI["appendEntry"], transportFailure?: () => EngramTransportFailure | undefined): Promise<unknown> {
  const sessionId = toolName === "mem_doctor" ? undefined : getSessionId(ctx);
  const runtimeSessionForWrite = () => requireRuntimeSessionID(ctx);
  const writeState = sessionId ? lifecycle(ctx, sessionId) : undefined;
  const writeEpoch = writeState?.epoch;
  const currentPrompt = writeState?.currentPrompt;
  const writeTarget = WRITE_TARGET_TOOLS.has(toolName) ? await resolveExplicitWriteTarget(params, fetch) : undefined;
  const requestedProject = writeTarget?.project || (typeof params.project === "string" && params.project ? params.project : undefined);
  const activeProject = requestedProject || project;
  // Only an explicit target may route to a satellite; implicit writes keep the runtime session.
  const registeredSessionForWrite = async (sessionProject: string) => {
    const owner = requestedProject ? runtimeSessionOwner(ctx, requireRuntimeSessionID(ctx)) : undefined;
    return owner && owner !== sessionProject
      ? registerSatelliteSession(ctx, sessionProject, fetch)
      : registerEffectiveSession(ctx, sessionProject, appendEntry, fetch);
  };

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
      return fetch(`/context${queryString({
        project: params.project || project,
        scope: params.scope,
        max_bytes: params.max_bytes,
        compact: params.compact,
      })}`);
    case "mem_stats":
      return fetch(`/stats${queryString({ all_projects: true })}`);
    case "mem_timeline":
      if (!requestedProject) requireResolvedProject();
      return fetch(`/timeline${queryString({ observation_id: params.observation_id, before: params.before, after: params.after, project: activeProject })}`);
    case "mem_get_observation":
      return fetch(`/observations/${encodeURIComponent(String(params.id))}`);
    case "mem_save": {
      if (!requestedProject) requireResolvedProject();
      if (writeState) assertOpen(writeState, writeEpoch!);
      const activeSessionId = await registeredSessionForWrite(activeProject);
      if (writeState) assertOpen(writeState, writeEpoch!);
      return fetch("/observations", {
        method: "POST",
        observationSave: true,
        body: {
          session_id: activeSessionId,
          title: params.title,
          content: params.content,
          type: params.type || "manual",
          project: activeProject,
          scope: params.scope || "project",
          topic_key: params.topic_key,
          capture_prompt: params.capture_prompt,
          current_prompt: params.capture_prompt !== false && currentPrompt
            && writeState?.currentPrompt === currentPrompt
            && currentPrompt.project === activeProject && currentPrompt.effectiveID === activeSessionId
            ? currentPrompt.content : undefined,
        },
      });
    }
    case "mem_update":
      return fetch(`/observations/${encodeURIComponent(String(params.id))}${queryString({ expected_project: params.expected_project })}`, {
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
      return fetch(`/observations/${encodeURIComponent(String(params.id))}${queryString({ hard: params.hard_delete, expected_project: params.expected_project })}`, { method: "DELETE" });
    case "mem_suggest_topic_key":
      return { topic_key: slugifyTopicKey(params) };
    case "mem_save_prompt": {
      if (!requestedProject) requireResolvedProject();
      if (writeState) assertOpen(writeState, writeEpoch!);
      const promptSessionId = await registeredSessionForWrite(activeProject);
      if (writeState) assertOpen(writeState, writeEpoch!);
      const response = await fetch<{ id: number }>("/prompts", {
        method: "POST",
        body: { session_id: promptSessionId, content: params.content, project: activeProject },
      });
      return response ? { prompt_id: response.id, status: "saved" } : response;
    }
    case "mem_session_summary": {
      if (!requestedProject) requireResolvedProject();
      if (writeState) assertOpen(writeState, writeEpoch!);
      const summarySessionId = await registeredSessionForWrite(activeProject);
      if (writeState) assertOpen(writeState, writeEpoch!);
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
    case "mem_session_end": {
      if (typeof params.id !== "string" || !params.id || params.id !== sessionId) {
        throw new Error("Pi-native session end requires the current host session ID; end independent sessions directly outside Pi-native tools");
      }
      // Registration may select a continuation while this explicit end is waiting.
      await Promise.all([...effectiveSessionRegistrations.entries()]
        .filter(([key]) => key.endsWith(`\u0000${sessionId}`))
        .map(([, registration]) => registration));
      const endedSessionID = effectiveSessionID(ctx, sessionId);
      const persistedPending = pendingEffectiveSession(ctx, sessionId, endedSessionID);
      const owner = persistedPending ? pendingEffectiveSessionProject(ctx, sessionId, endedSessionID) : undefined;
      if (persistedPending) {
        const localOwner = registeredSessionProjects.get(endedSessionID) || sessionRegistrationProjects.get(endedSessionID);
        if (!appendEntry || !owner || owner !== (localOwner || (project !== "unknown" ? project : undefined))) {
          throw new Error("Cannot end a pending Pi session without confirmed local project ownership and entry persistence");
        }
      }
      const pendingEnd = sessionEndingsInFlight.get(endedSessionID);
      if (pendingEnd) return pendingEnd;
      // A persisted reservation from an earlier module graph is not local proof of
      // ownership. Confirm it with the server before allowing this graph to end it.
      if (persistedPending && !registeredSessionProjects.has(endedSessionID)) {
        try {
          await ensureSession(endedSessionID, owner, fetch, true);
        } catch (error) {
          const data = error instanceof EngramHttpError ? error.data as { code?: string; session_id?: string } | null : null;
          if (!(error instanceof EngramHttpError && error.status === 409
            && data?.code === "session_already_ended" && data.session_id === endedSessionID
            && owner === project && pendingEffectiveSession(ctx, sessionId, endedSessionID))) throw error;
          appendEntry!(EFFECTIVE_SESSION_ENTRY, {
            runtimeID: sessionId, effectiveID: endedSessionID, pending: false, project: owner,
          });
          return { status: "already_ended", session_id: endedSessionID };
        }
      }
      if (endedSessionID === sessionId && !registeredSessionProjects.has(endedSessionID)) {
        await waitForSessionRegistration(endedSessionID, true);
        if (!registeredSessionProjects.has(endedSessionID)) {
          throw new Error(`Cannot end Pi session ${endedSessionID} without confirmed local registration`);
        }
      }
      const end = async () => {
        const result = await fetch(`/sessions/${encodeURIComponent(endedSessionID)}/end`, {
          method: "POST",
          body: { summary: params.summary || "" },
        });
        if (persistedPending && !transportFailure?.()) appendEntry!(EFFECTIVE_SESSION_ENTRY, {
          runtimeID: sessionId, effectiveID: endedSessionID, pending: false, project: owner,
        });
        return result;
      };
      return endedSessionID !== sessionId || hasKnownSession(endedSessionID) || hasSessionRegistrationInFlight(endedSessionID)
        ? endRegisteredSessionOnce(endedSessionID, async () => {
          if (endedSessionID !== sessionId && (!registeredSessionProjects.has(endedSessionID)
            || (persistedPending && registeredSessionProjects.get(endedSessionID) !== owner))) {
            throw new Error(`Cannot end Pi session ${endedSessionID} without confirmed local project ownership`);
          }
          return end();
        }, persistedPending, endedSessionID !== sessionId)
        : end();
    }
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
    case "mem_doctor": {
      if (!requestedProject) requireResolvedProject();
      const report = await fetch(`/doctor${queryString({ project: activeProject, check: params.check })}`);
      let caller: unknown;
      try {
        // A separate transport isolates optional diagnostic failures from project checks.
        const response = await engramFetchResult("/doctor/caller-binding", {
          method: "POST", body: callerBindingContext(ctx, activeProject, callerAppendAvailability.has(params) ? callerAppendAvailability.get(params)! : !!appendEntry),
        });
        if (!response.transportFailure) caller = response.data;
      } catch { /* Old servers and errors receive only the literal safe fallback. */ }
      return { ...(report as Record<string, unknown>), caller_binding: callerBindingProjection(caller) };
    }
    case "mem_capture_passive": {
      requireResolvedProject();
      if (writeState) assertOpen(writeState, writeEpoch!);
      const passiveSessionId = await registeredSessionForWrite(project);
      if (writeState) assertOpen(writeState, writeEpoch!);
      const body = {
        session_id: passiveSessionId, content: params.content, project, source: params.source || "pi-tool",
      };
      if (writeState) assertOpen(writeState, writeEpoch!);
      return fetch("/observations/passive", { method: "POST", body });
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
    case "mem_list_projects":
      return fetch("/projects");
    case "mem_pin":
      return fetch(`/observations/${encodeURIComponent(String(params.id))}/pin`, { method: "PUT" });
    case "mem_unpin":
      return fetch(`/observations/${encodeURIComponent(String(params.id))}/pin`, { method: "DELETE" });
    default:
      throw new Error(`Unsupported Engram memory tool: ${toolName}`);
  }
}

function unreachableMessage(failure: EngramTransportFailure | undefined): string {
  if (failure?.operation === "session-registration" && failure.outcome === "unknown") {
    return `gentle-engram could not confirm session registration after ${failure.timeoutMs}ms. Registration is idempotent and was retried within its bounded policy, but its final outcome is unknown. No memory write was sent; retrying the memory operation is safe.`;
  }
  if (failure?.operation === "write" && failure.outcome === "unknown") {
    if (failure.operationID) return `gentle-engram could not confirm the observation save. Its outcome is unknown — do NOT blindly retry with a new operation. Operation ID: ${failure.operationID}. Read-only recovery: GET /observations/save-result?operation_id=${failure.operationID} on the original Engram server.`;
    return `gentle-engram could not confirm the write after ${failure.timeoutMs}ms. Its outcome is unknown because the server may already have applied it — do NOT blindly retry it, or you may duplicate the write. Verify with mem_search or mem_doctor first.`;
  }
  if (failure?.outcome === "timed_out") {
    return `gentle-engram ${failure.operation} timed out after ${failure.timeoutMs}ms waiting for the Engram HTTP server at ${ENGRAM_URL}. The bounded read policy was exhausted; run mem_doctor or retry the read when the server is healthy.`;
  }
  return `gentle-engram could not reach the Engram HTTP server at ${ENGRAM_URL}. The Pi-native mem_* tools are registered, but the native memory provider is not currently responding. Run mem_doctor or restart Engram.`;
}

async function executeMemoryTool(toolName: string, params: Record<string, unknown>, ctx: MemoryToolContext, signal?: AbortSignal, appendEntry?: ExtensionAPI["appendEntry"]) {
  const action = humanToolName(toolName);
  const transport = createMemoryToolTransport(signal);

  try {
    // Initialization runs inside the guarded path: a rejected startup must reach the agent as
    // a normalized tool error, not as a rejection escaping the Pi tool boundary.
    await awaitWithAbort(initOnce(ctx.cwd), signal);
    await refreshProjectDetection(ctx.cwd, engramFetch, signal);
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${action}…`);
    const data = await awaitWithAbort(callMemoryTool(toolName, params, ctx, transport.fetch, appendEntry, transport.transportFailure), signal);
    const failure = transport.transportFailure();
    if (failure) throw new Error(unreachableMessage(failure));

    const result = { content: [{ type: "text" as const, text: textResult(data, toolName) }], details: { data } };
    if (toolName === "mem_doctor" && data && typeof data === "object" && "status" in data && data.status === "error") {
      const errorResult = { ...result, isError: true };
      ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${compactResultStatus(toolName, errorResult)}`);
      return errorResult;
    }
    const callerStatus = toolName === "mem_doctor" && data && typeof data === "object" && "caller_binding" in data
      ? `project checks: ${compactResultStatus(toolName, result)} · caller: ${callerBindingStatus(data)}`
      : compactResultStatus(toolName, result);
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${callerStatus}`);
    return result;
  } catch (error) {
    if (signal?.aborted) throw error;
    if (toolName === "mem_doctor" && error instanceof ForeignOwnershipError) {
      const data = { source: "LOCAL", status: "error", code: "ownership_mismatch", endpoint: ENGRAM_URL, evidence: error.evidence, message: error.message };
      ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${errorStatusLabel(error.message)}`);
      return { content: [{ type: "text" as const, text: JSON.stringify(data, null, 2) }], details: { data }, isError: true };
    }
    const failure = transport.transportFailure();
    const message = failure ? unreachableMessage(failure) : error instanceof Error ? error.message : String(error);
    const details = error instanceof EngramHttpError
      ? { error: message, http_status: error.status, data: error.data }
      : failure ? { error: message, outcome: failure.outcome, operation: failure.operation, ...(failure.operationID ? { operation_id: failure.operationID } : {}) } : { error: message };
    ctx.ui?.setStatus?.("engram", `🧠 ${project} · ${errorStatusLabel(message)}`);
    if (shouldScheduleEngramSelfHeal(error, failure)) scheduleEngramSelfHeal(ctx);
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
      async execute(_toolCallId, params, signal, _onUpdate, ctx) {
        if (toolName === "mem_doctor") {
          let available: boolean | null = null;
          try { available = typeof pi.appendEntry === "function"; } catch { /* Host getter unavailable. */ }
          const callerParams = { ...params } as Record<string, unknown>;
          callerAppendAvailability.set(callerParams, available);
          return executeMemoryTool(toolName, callerParams, ctx as MemoryToolContext, signal);
        }
        return executeMemoryTool(toolName, params as Record<string, unknown>, ctx as MemoryToolContext, signal, pi.appendEntry?.bind(pi));
      },
      renderCall(args) {
        return new Text(renderCallText(toolName, args), 0, 0);
      },
      renderResult(result, options, _theme, context) {
        const rendered = renderResultText(toolName, result, { expanded: options.expanded, isPartial: options.isPartial, isError: context.isError });
        const caller = toolName === "mem_doctor" ? (result.details as { data?: { caller_binding?: { status: string } } } | undefined)?.data?.caller_binding : undefined;
        return new Text(caller && !options.expanded ? `Project checks: ${rendered}\nCaller binding: ${caller.status} (later writes not guaranteed)` : rendered, 0, 0);
      },
    });
  }
}

const BRIDGE_CLAIM = "engram:bridge:claim:v1";
const BRIDGE_DECISION = "engram:bridge:decision:v1";
const BRIDGE_CLAIM_TIMEOUT_MS = 1000;

function removeBridgeResponders(ctx: SessionContext): void {
  for (const state of lifecycles.get(ctx.sessionManager)?.values() || []) {
    state.removeBridgeResponder?.();
    state.removeBridgeResponder = undefined;
    state.promptProof = undefined;
    state.currentPrompt = undefined;
    state.awaitingInitialUserMessage = false;
    state.promptTurn = (state.promptTurn || 0) + 1;
  }
}

// A claim supplies routing context, never credentials or an arbitrary registration ID.
// Install only with an observed native lifecycle context, not from the extension factory.
function installBridgeResponder(pi: ExtensionAPI, ctx: SessionContext, runtimeID: string, state: Lifecycle): void {
  if (!pi.events?.on || !pi.events?.emit) return;
  const epoch = state.epoch;
  let active = true;
  const current = () => active && !state.closing && state.epoch === epoch
    && getSessionId(ctx) === runtimeID;
  const unsubscribe = pi.events.on(BRIDGE_CLAIM, async (data: unknown) => {
    if (!data || typeof data !== "object") return;
    const request = data as { runtimeSessionId?: unknown; nonce?: unknown; operation?: unknown; promptDigest?: unknown };
    if (request.runtimeSessionId !== runtimeID || typeof request.nonce !== "string"
      || request.nonce.trim().length === 0 || request.nonce.length > 256
      || (request.operation !== "session-register" && request.operation !== "prompt-capture")) return;
    // Copy fields before awaiting: another local extension may mutate its request object.
    const correlation = { runtimeSessionId: runtimeID, nonce: request.nonce, operation: request.operation,
      ...(typeof request.promptDigest === "string" ? { promptDigest: request.promptDigest } : {}) };
    if (request.operation === "prompt-capture") {
      const proof = state.promptProof;
      let owned = false;
      try {
        owned = typeof request.promptDigest === "string" && /^[a-f0-9]{64}$/.test(request.promptDigest)
          && current() && !!proof && proof.digest === request.promptDigest
          && !projectDetectionPending && !projectResolutionError && project === proof.project
          && effectiveSessionID(ctx, runtimeID) === proof.effectiveID
          && registeredSessionProjects.get(proof.effectiveID) === proof.project
          && !knownSessions.has(`\u0000closing:${runtimeID}`)
          && !knownSessions.has(`\u0000closing:${proof.effectiveID}`);
      } catch { /* A stale or unreadable native context cannot prove delivery. */ }
      if (active) pi.events.emit(BRIDGE_DECISION, { ...correlation, status: owned ? "owned" : "unknown" });
      return;
    }
    let status: "owned" | "unknown" = "unknown";
    let timer: ReturnType<typeof setTimeout> | undefined;
    let expired = false;
    try {
      const confirm = async () => {
        if (!current() || !(await initOnceForHook(ctx.cwd)) || expired || !current()) return false;
        await refreshProjectDetection(ctx.cwd);
        if (expired || !current() || project === "unknown" || projectDetectionPending || projectResolutionError) return false;
        const owner = project;
        let effectiveID = effectiveSessionID(ctx, runtimeID);
        if (registeredSessionProjects.get(effectiveID) !== owner) {
          // registerEffectiveSession joins any pending registration and validates its acknowledgement.
          effectiveID = await registerEffectiveSession(ctx, owner, pi.appendEntry?.bind(pi));
        }
        return current() && project === owner && registeredSessionProjects.get(effectiveID) === owner
          && !knownSessions.has(`\u0000closing:${runtimeID}`)
          && !knownSessions.has(`\u0000closing:${effectiveID}`);
      };
      const confirmed = await Promise.race([
        confirm(),
        new Promise<boolean>((resolve) => { timer = setTimeout(() => { expired = true; resolve(false); }, BRIDGE_CLAIM_TIMEOUT_MS); }),
      ]);
      if (confirmed && current()) status = "owned";
    } catch { /* Uncertain registration retains the consumer's existing hooks. */ }
    finally { if (timer !== undefined) clearTimeout(timer); }
    // Reload/shutdown cancel delivery as well as removing the listener.
    if (active) pi.events.emit(BRIDGE_DECISION, { ...correlation, status });
  });
  state.removeBridgeResponder = () => { active = false; unsubscribe(); };
}

export default function registerEngram(pi: ExtensionAPI) {
  // Trace-only comparison is local to this factory, never persistence or ownership state.
  let previousTraceIdentity: string | undefined;
  const traceLifecycle = (event: "session_start" | "session_shutdown", id: string | undefined | (() => string), reason?: unknown) => {
    if (process.env.ENGRAM_PI_SESSION_TRACE !== "1") {
      previousTraceIdentity = undefined;
      return;
    }
    let current: string | undefined;
    try {
      const value = typeof id === "function" ? id() : id;
      if (typeof value === "string" && value.trim()) current = value.trim();
    } catch { /* Reading identity for diagnostics must never affect reload cleanup. */ }
    const identity = !current ? "missing" : !previousTraceIdentity ? "first" : current === previousTraceIdentity ? "same" : "different";
    if (current) previousTraceIdentity = current;
    if (event === "session_start") traceSessionRegistration({ stage: "lifecycle", event, identity });
    else traceSessionRegistration({ stage: "lifecycle", event, identity, reason: reason === "reload" ? "reload" : reason == null || reason === "" ? "missing" : "other" });
  };
  // Pi's queued steer/follow-up inputs bypass before_agent_start. Only retain
  // the latest idle input; a FIFO would assign queued provenance to later runs.
  const pendingInput = new WeakMap<object, { sessionId: string; source: InputEvent["source"] }>();
  registerMemoryTools(pi);
  pi.on("input", (event: InputEvent, ctx: SessionContext) => {
    if (event.streamingBehavior) return { action: "continue" as const };
    const sessionId = observeRuntimeSessionID(ctx);
    if (sessionId) pendingInput.set(ctx.sessionManager, { sessionId, source: event.source });
    else pendingInput.delete(ctx.sessionManager);
    return { action: "continue" as const };
  });
  pi.on("message_start", (event: { message?: { role?: string } }, ctx: SessionContext) => {
    if (event.message?.role !== "user") return;
    const sessionId = observeRuntimeSessionID(ctx);
    for (const [runtimeID, state] of lifecycles.get(ctx.sessionManager) || []) {
      if (sessionId && sessionId !== runtimeID) continue;
      if (sessionId === runtimeID && !state.closing && state.awaitingInitialUserMessage) {
        // agent.prompt emits the first user message after before_agent_start.
        state.awaitingInitialUserMessage = false;
        continue;
      }
      // Later user messages are consumed queued turns, not enqueue notifications.
      // Never inspect their content or inherit the preceding native turn's proof.
      state.awaitingInitialUserMessage = false;
      state.currentPrompt = undefined;
      state.promptProof = undefined;
      state.promptTurn = (state.promptTurn || 0) + 1;
    }
  });
  pi.on("session_start", async (_event: unknown, ctx: SessionContext) => {
    pendingInput.delete(ctx.sessionManager);
    removeBridgeResponders(ctx);
    const sessionId = observeRuntimeSessionID(ctx);
    traceLifecycle("session_start", sessionId);
    if (sessionId) {
      const state = lifecycle(ctx, sessionId);
      state.epoch++;
      state.closing = false;
      knownSessions.delete(`\u0000closing:${sessionId}`);
      knownSessions.delete(`\u0000closing:${effectiveSessionID(ctx, sessionId)}`);
      installBridgeResponder(pi, ctx, sessionId, state);
    }
    const ready = await initOnceForHook(ctx.cwd);
    try {
      (ctx as MemoryToolContext).ui?.setStatus?.("engram", `🧠 ${project} · ${ready ? "ready" : "offline"}`);
    } catch {}
  });

  pi.on("session_shutdown", async (event: { reason?: string }, ctx: SessionContext) => {
    pendingInput.delete(ctx.sessionManager);
    removeBridgeResponders(ctx);
    // Pi reload replaces the extension runner but keeps its runtime session ID alive.
    if (event.reason === "reload") {
      traceLifecycle("session_shutdown", () => ctx.sessionManager.getSessionId(), event.reason);
      return;
    }
    const runtimeID = observeRuntimeSessionID(ctx);
    traceLifecycle("session_shutdown", runtimeID, event.reason);
    if (!runtimeID) return;
    const state = lifecycle(ctx, runtimeID);
    state.epoch++;
    state.closing = true;
    knownSessions.add(`\u0000closing:${runtimeID}`);
    const pending = [...effectiveSessionRegistrations.entries()]
      .filter(([key]) => key.endsWith(`\u0000${runtimeID}`))
      .map(([, registration]) => registration.catch(() => undefined));
    await Promise.all(pending);
    const sessionId = effectiveSessionID(ctx, runtimeID);
    knownSessions.add(`\u0000closing:${sessionId}`);
    try {
      // An ownerless legacy reservation alone does not authorize ending this session.
      const owner = pendingEffectiveSessionProject(ctx, runtimeID, sessionId);
      const localOwner = registeredSessionProjects.get(sessionId) || sessionRegistrationProjects.get(sessionId);
      // A freshly loaded graph has not necessarily detected its project yet.
      const detectedResponse = owner && !localOwner && project === "unknown"
        ? await detectServerProject(ctx.cwd, engramFetch, AbortSignal.timeout(ENGRAM_READ_TIMEOUT_MS)) : undefined;
      const detected = detectedResponse ? isSafeDetectedProject(detectedResponse) : undefined;
      const persistedPending = pendingEffectiveSession(ctx, runtimeID, sessionId)
        && !!owner && (owner === localOwner || owner === (detected || (project !== "unknown" ? project : undefined)));
      // A foreign graph may observe a reservation but never owns its shutdown;
      // still fall through to the common module-local cleanup below.
      if (state.confirmedShutdownID !== sessionId
        && !(pendingEffectiveSession(ctx, runtimeID, sessionId) && !persistedPending)
        && (persistedPending || hasKnownSession(sessionId) || hasSessionRegistrationInFlight(sessionId))) {
        await sharedShutdown(ctx.sessionManager, sessionId, async () => {
          const ended = await endRegisteredSessionOnce(sessionId, () => bestEffortEngramFetch(
            `/sessions/${encodeURIComponent(sessionId)}/end`,
            { method: "POST", body: { summary: "" } },
            ctx,
          ), persistedPending);
          if (ended !== null && ended !== undefined) {
            // Read-only session logs cannot clear pending; remember only confirmed delivery.
            // Renewal resets this marker, and uncertain delivery remains retryable.
            state.confirmedShutdownID = sessionId;
            if (persistedPending) pi.appendEntry?.(EFFECTIVE_SESSION_ENTRY, {
              runtimeID, effectiveID: sessionId, pending: false, project: owner,
            });
          }
        });
      }
    } catch (error) {
      warnEngramFailure(`/sessions/${encodeURIComponent(sessionId)}/end`, error, ctx);
    }
    await endSatelliteSessions(runtimeID, ctx);
    toolCounts.delete(sessionId);
    forgetKnownSession(sessionId);
    forgetSelfHealContext(runtimeID);
  });

  pi.on("session_compact", async (event: unknown) => {
    const summary = extractCompactedSummary(event);
    const sessionId = soleObservedRuntimeSessionID();
    const observed = sessionId ? observedSessionContexts.get(sessionId) : undefined;
    const state = sessionId && observed ? lifecycle(observed, sessionId) : undefined;
    const epoch = state?.epoch;
    const open = () => { if (state) assertOpen(state, epoch!); };

    // Queue a session-scoped safe fallback before every early exit. The intended next turn must
    // receive this even when startup, project resolution, or strict registration cannot reach Engram.
    if (sessionId) {
      pendingRecoveryNotice = {
        sessionId,
        content: buildRecoveryNotice(project, undefined, ArchiveOutcome.Unavailable),
      };
    }
    if (!summary || !(await initOnceForHook(directory))) return;
    await refreshProjectDetection(directory);
    if (projectDetectionPending || projectResolutionError) return;
    if (!sessionId || soleActiveRuntimeSessionID() !== sessionId) return;

    let effectiveID: string;
    try {
      effectiveID = await registerEffectiveSession(observedSessionContexts.get(sessionId) || { cwd: directory, sessionManager: { getSessionId: () => sessionId } }, project, pi.appendEntry?.bind(pi));
    } catch (error) {
      warnEngramFailure("/sessions", error, observed);
      return;
    }
    if (soleActiveRuntimeSessionID() !== sessionId || knownSessions.has(`\u0000closing:${effectiveID}`)) return;

    let outcome: string;
    try { open(); outcome = await archiveCompactionSummary(effectiveID, summary, sessionId, observed); }
    catch { outcome = ArchiveOutcome.Unavailable; }
    const context = !knownSessions.has(`\u0000closing:${effectiveID}`) && soleActiveRuntimeSessionID() === sessionId
      ? await loadCompactionRecoveryContext(effectiveID, observed)
      : undefined;
    pendingRecoveryNotice = { sessionId, content: buildRecoveryNotice(project, context, outcome) };
  });

  pi.on("before_agent_start", async (event: AgentStartEvent, ctx: SessionContext) => {
    // Consume before awaiting: another input must not change this turn's source.
    const sessionId = observeRuntimeSessionID(ctx);
    const input = pendingInput.get(ctx.sessionManager);
    pendingInput.delete(ctx.sessionManager);
    const syntheticInput = (input?.sessionId === sessionId && input?.source === "extension")
      || Boolean(process.env.GENTLE_PI_AGENTS_CHILD);
    // Invalidate before any initialization or early return, including short/empty turns.
    for (const prior of lifecycles.get(ctx.sessionManager)?.values() || []) {
      prior.promptProof = undefined;
      prior.currentPrompt = undefined;
      prior.awaitingInitialUserMessage = false;
      prior.promptTurn = (prior.promptTurn || 0) + 1;
    }
    let systemPrompt = event.systemPrompt.length > 0 ? `${event.systemPrompt}\n\n${MEMORY_INSTRUCTIONS}` : MEMORY_INSTRUCTIONS;
    const state = sessionId ? lifecycle(ctx, sessionId) : undefined;
    if (state) state.awaitingInitialUserMessage = true;
    const epoch = state?.epoch;
    const promptTurn = state?.promptTurn;
    // A returned systemPrompt becomes a forced prompt that turns skipping this hook never see
    // (pi#5581), so newer Pi receives the text through the per-run structured options instead.
    const options = event.systemPromptOptions && typeof event.systemPromptOptions === "object" ? event.systemPromptOptions : undefined;
    if (options) appendPromptSection(options, MEMORY_INSTRUCTIONS);
    if (pendingRecoveryNotice !== undefined && sessionId === pendingRecoveryNotice.sessionId) {
      if (options) appendPromptSection(options, pendingRecoveryNotice.content);
      else systemPrompt = `${systemPrompt}\n\n${pendingRecoveryNotice.content}`;
      pendingRecoveryNotice = undefined;
    }
    const result = options ? undefined : { systemPrompt };
    // The mem_* tools stay registered whether or not startup succeeded, so the agent still
    // needs the memory protocol; only the server-backed work below is skipped.
    if (!(await initOnceForHook(ctx.cwd))) return result;
    await refreshProjectDetection(ctx.cwd);

    // Unknown provenance and human-facing RPC retain the existing capture policy.
    if (syntheticInput) return result;
    const finalContent = event.prompt?.trim();
    if ((projectDetectionPending || projectResolutionError) && sessionId && finalContent && finalContent.length > 10) {
      return result;
    }
    if (sessionId && finalContent && finalContent.length > 10) {
      let effectiveID: string;
      try { effectiveID = await registerEffectiveSession(ctx, project, pi.appendEntry?.bind(pi)); }
      catch (error) { if (error instanceof SessionProjectConflictError) warnSessionProjectConflictOnce(error, ctx); else warnEngramFailure("/sessions", error, ctx); return result; }
      if (knownSessions.has(`\u0000closing:${effectiveID}`)) return result;
      const body: PromptBody = {
        session_id: effectiveID,
        // Redact before truncating: a <private> block straddling the limit
        // would otherwise lose its closing tag and leak.
        content: truncate(stripPrivateTags(finalContent), 2000),
        project,
      };
      if (state && (state.closing || state.epoch !== epoch || state.promptTurn !== promptTurn)) return result;
      // Retain only the already-redacted payload, scoped to the current owner.
      // Go owns per-save opt-out and dedupe, including recovery after a failed lifecycle write.
      if (state && getSessionId(ctx) === sessionId && project === body.project
        && !projectDetectionPending && !projectResolutionError) {
        state.currentPrompt = { content: body.content, project: body.project, effectiveID };
      }
      const acknowledgement = await bestEffortEngramFetch<{ id?: unknown; status?: unknown }>("/prompts", { method: "POST", body }, ctx);
      // null includes errors and successful JSON null/204: only the core's saved-ID
      // acknowledgement proves capture. The digest matches the original trimmed input,
      // while persistence retains its existing redaction/truncation policy.
      if (state && !state.closing && state.epoch === epoch && state.promptTurn === promptTurn
        && getSessionId(ctx) === sessionId && project === body.project
        && !projectDetectionPending && !projectResolutionError
        && effectiveSessionID(ctx, sessionId) === effectiveID
        && registeredSessionProjects.get(effectiveID) === body.project
        && !knownSessions.has(`\u0000closing:${effectiveID}`)
        && acknowledgement?.status === "saved" && typeof acknowledgement.id === "number"
        && Number.isSafeInteger(acknowledgement.id) && acknowledgement.id > 0) {
        state.promptProof = { digest: createHash("sha256").update(finalContent, "utf8").digest("hex"),
          project: body.project, effectiveID };
      }
    }

    return result;
  });

  pi.on("tool_execution_end", async (event: ToolEndEvent, ctx: SessionContext) => {
    const sessionId = observeRuntimeSessionID(ctx);
    const state = sessionId ? lifecycle(ctx, sessionId) : undefined;
    const epoch = state?.epoch;
    const toolName = event.toolName ?? "";
    if (ENGRAM_TOOL_NAMES.has(toolName.toLowerCase())) return;

    if (!(await initOnceForHook(ctx.cwd))) return;
    await refreshProjectDetection(ctx.cwd);
    if (!sessionId || projectDetectionPending || projectResolutionError) return;

    let effectiveID: string;
    try { effectiveID = await registerEffectiveSession(ctx, project, pi.appendEntry?.bind(pi)); }
    catch (error) { if (error instanceof SessionProjectConflictError) warnSessionProjectConflictOnce(error, ctx); else warnEngramFailure("/sessions", error, ctx); return; }
    if (knownSessions.has(`\u0000closing:${effectiveID}`)) return;
    toolCounts.set(effectiveID, (toolCounts.get(effectiveID) ?? 0) + 1);

    if (event.result === undefined) return;
    let content: string | undefined;
    try {
      content = typeof event.result === "string" ? event.result : JSON.stringify(event.result);
    } catch {
      return;
    }
    if (!content || content.length <= 50 || knownSessions.has(`\u0000closing:${effectiveID}`)) return;

    const body: PassiveCaptureBody = {
      session_id: effectiveID,
      content: stripPrivateTags(content),
      project,
      source: toolName,
    };
    if (state && (state.closing || state.epoch !== epoch)) return;
    await bestEffortEngramFetch("/observations/passive", { method: "POST", body }, ctx);
  });
}

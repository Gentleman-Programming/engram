/**
 * Engram — Pi extension adapter
 *
 * Thin adapter that connects Pi session events to an Engram HTTP server.
 * Persistence remains owned by the Engram Go binary (`engram serve`). MCP tools
 * are configured separately through pi-mcp-adapter and `engram mcp`.
 */

import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { buildRecoveryNotice, extractCompactedSummary } from "./compaction-recovery.js";
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

Before ending a session or saying "done" / "listo", call \`mem_session_summary\`
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

interface ContextResponse {
  context?: string;
}

interface ProjectConfig {
  project_name?: unknown;
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

async function engramFetch<TResponse = unknown>(path: string, opts: FetchOptions = {}): Promise<TResponse | null> {
  try {
    const res = await fetch(`${ENGRAM_URL}${redactUrlPath(path)}`, {
      method: opts.method ?? "GET",
      headers: opts.body ? { "Content-Type": "application/json" } : undefined,
      body: opts.body ? JSON.stringify(redactValue(opts.body)) : undefined,
    });
    return (await res.json()) as TResponse;
  } catch {
    return null;
  }
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

function normalizeProjectName(name: string): string {
  const normalized = name.trim().toLowerCase();
  return normalized || "unknown";
}

function normalizeConfigProjectName(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || /[/\\]/.test(trimmed) || /[\x00-\x1f\x7f]/.test(trimmed)) return undefined;
  return normalizeProjectName(trimmed);
}

function getGitRoot(directory: string): string | undefined {
  try {
    const result = spawnSync("git", ["-C", directory, "rev-parse", "--show-toplevel"], {
      encoding: "utf8",
    });
    if (result.status === 0) {
      return result.stdout?.trim() || undefined;
    }
  } catch {}
  return undefined;
}

function readConfigProjectName(projectDir: string): string | undefined {
  const configPath = join(projectDir, ".engram", "config.json");
  if (!existsSync(configPath)) return undefined;
  try {
    const parsed = JSON.parse(readFileSync(configPath, "utf8")) as ProjectConfig;
    return normalizeConfigProjectName(parsed.project_name);
  } catch {
    return undefined;
  }
}

function detectConfigProjectName(directory: string): string | undefined {
  const start = resolve(directory || ".");
  const gitRoot = getGitRoot(start);
  if (!gitRoot) return readConfigProjectName(start);

  let current = start;
  const stop = resolve(gitRoot);
  for (;;) {
    const projectName = readConfigProjectName(current);
    if (projectName) return projectName;
    if (current === stop) return undefined;
    const parent = dirname(current);
    if (parent === current) return undefined;
    current = parent;
  }
}

function detectGitRemoteProjectName(directory: string): string | undefined {
  try {
    const result = spawnSync("git", ["-C", directory, "remote", "get-url", "origin"], {
      encoding: "utf8",
    });
    if (result.status === 0) {
      const url = result.stdout?.trim();
      const name = url?.replace(/\.git$/, "").split(/[/:]/).pop();
      if (name) return normalizeProjectName(name);
    }
  } catch {}
  return undefined;
}

function detectSingleChildGitProjectName(directory: string): string | undefined {
  const noise = new Set(["node_modules", "vendor", ".venv", "__pycache__", "target", "dist", "build", ".idea", ".vscode"]);
  const repos: string[] = [];
  try {
    for (const entry of readdirSync(directory).slice(0, 20)) {
      if (entry.startsWith(".") || noise.has(entry)) continue;
      const childPath = join(directory, entry);
      try {
        if (!statSync(childPath).isDirectory()) continue;
        if (existsSync(join(childPath, ".git"))) repos.push(entry);
        if (repos.length > 1) return undefined;
      } catch {}
    }
  } catch {}
  return repos.length === 1 ? repos[0] : undefined;
}

function extractProjectName(directory: string): string {
  const configProject = detectConfigProjectName(directory);
  if (configProject) return configProject;

  const remoteProject = detectGitRemoteProjectName(directory);
  if (remoteProject) return remoteProject;

  const root = getGitRoot(directory);
  if (root) return normalizeProjectName(basename(root));

  const childProject = detectSingleChildGitProjectName(directory);
  if (childProject) return normalizeProjectName(childProject);

  return normalizeProjectName(basename(directory));
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

const knownSessions = new Set<string>();
const toolCounts = new Map<string, number>();

async function ensureSession(sessionId: string): Promise<void> {
  if (!sessionId || knownSessions.has(sessionId)) return;
  knownSessions.add(sessionId);
  const body: SessionBody = { id: sessionId, project, directory };
  await engramFetch("/sessions", { method: "POST", body });
}

async function initOnce(cwd: string): Promise<void> {
  if (initialized) return;
  initialized = true;
  directory = cwd;

  const oldProject = cwd.split("/").pop() ?? "unknown";
  project = extractProjectName(cwd);

  const running = await isEngramRunning();
  if (!running && CONFIGURED_ENGRAM_URL === undefined) {
    await spawnDetached(ENGRAM_BIN, ["serve"]);
    await wait(500);
  }

  if (oldProject !== project) {
    const body: MigrationBody = { old_project: oldProject, new_project: project };
    await engramFetch("/projects/migrate", { method: "POST", body });
  }

  const manifestFile = `${cwd}/.engram/manifest.json`;
  if (existsSync(manifestFile)) {
    await spawnDetached(ENGRAM_BIN, ["sync", "--import"], cwd);
  }
}

function getSessionId(ctx: SessionContext): string | undefined {
  return ctx.sessionManager.getSessionId();
}

export default function registerEngram(pi: ExtensionAPI) {
  pi.on("session_start", async (_event: unknown, ctx: SessionContext) => {
    await initOnce(ctx.cwd);
  });

  pi.on("session_shutdown", async (_event: unknown, ctx: SessionContext) => {
    const sessionId = getSessionId(ctx);
    if (!sessionId) return;
    toolCounts.delete(sessionId);
    knownSessions.delete(sessionId);
  });

  pi.on("session_compact", async (event: unknown, ctx: SessionContext) => {
    const sessionId = getSessionId(ctx);
    if (sessionId) await ensureSession(sessionId);

    const summary = extractCompactedSummary(event);
    if (sessionId && summary) {
      await engramFetch("/observations", {
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

    const data = await engramFetch<ContextResponse>(`/context?project=${encodeURIComponent(project)}`);
    pendingRecoveryNotice = buildRecoveryNotice(project, data?.context);
  });

  pi.on("before_agent_start", async (event: AgentStartEvent, ctx: SessionContext) => {
    await initOnce(ctx.cwd);
    const sessionId = getSessionId(ctx);
    let systemPrompt = event.systemPrompt.length > 0 ? `${event.systemPrompt}\n\n${MEMORY_INSTRUCTIONS}` : MEMORY_INSTRUCTIONS;

    if (pendingRecoveryNotice !== undefined) {
      systemPrompt = `${systemPrompt}\n\n${pendingRecoveryNotice}`;
      pendingRecoveryNotice = undefined;
    }

    const finalContent = event.prompt?.trim();
    if (sessionId && finalContent && finalContent.length > 10) {
      await ensureSession(sessionId);
      const body: PromptBody = {
        session_id: sessionId,
        content: stripPrivateTags(truncate(finalContent, 2000)),
        project,
      };
      await engramFetch("/prompts", { method: "POST", body });
    }

    return { systemPrompt };
  });

  pi.on("tool_execution_end", async (event: ToolEndEvent, ctx: SessionContext) => {
    const toolName = event.toolName ?? "";
    if (ENGRAM_TOOL_NAMES.has(toolName.toLowerCase())) return;

    const sessionId = getSessionId(ctx);
    if (!sessionId) return;

    await ensureSession(sessionId);
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
    await engramFetch("/observations/passive", { method: "POST", body });
  });
}

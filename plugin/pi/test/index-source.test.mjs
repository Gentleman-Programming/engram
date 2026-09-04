import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { readFileSync } from "node:fs";
import { createServer } from "node:net";
import { test } from "node:test";

const source = readFileSync(new URL("../index.ts", import.meta.url), "utf8").replaceAll("\r\n", "\n");

function extractFunctionBody(name, marker) {
  const signatureIndex = source.indexOf(`function ${name}`);
  assert.notEqual(signatureIndex, -1, `${name} signature not found`);
  const bodyStart = source.indexOf(marker, signatureIndex);
  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index];
    if (char === "{") depth += 1;
    if (char === "}") depth -= 1;
    if (depth === 0) return source.slice(bodyStart + 1, index);
  }
  throw new Error(`${name} body not found`);
}

function flush(times = 2) {
  return times <= 0
    ? Promise.resolve()
    : new Promise((resolve) => setTimeout(resolve, 0)).then(() => flush(times - 1));
}

function buildEngramFetchForTest({
  wait = () => Promise.resolve(),
  timeoutMs = 3000,
  maxAttempts = 3,
  backoffBaseMs = 150,
  recover = async () => false,
} = {}) {
  const body = extractFunctionBody("engramFetchResult", "{\n  const method")
    .replace("let res: Response | undefined;", "let res;")
    .replace("let data: unknown = null;", "let data = null;")
    .replace("return engramFetchResult<TResponse>(path, opts);", "return engramFetchResult(path, opts);")
    .replace("return { data: data as TResponse };", "return { data };");
  const factory = new Function(
    "fetch",
    "wait",
    "redactUrlPath",
    "redactValue",
    "ENGRAM_URL",
    "ENGRAM_FETCH_TIMEOUT_MS",
    "ENGRAM_FETCH_MAX_ATTEMPTS",
    "ENGRAM_FETCH_BACKOFF_BASE_MS",
    "recoverImplicitEngramServer",
    `
    class EngramHttpError extends Error {
      constructor(message, status, data) {
        super(message);
        this.name = "EngramHttpError";
        this.status = status;
        this.data = data;
      }
    }
    function isTimeoutError(error) {
      ${extractFunctionBody("isTimeoutError", "{\n  return error instanceof Error")}
    }
    function isSafeToReplay(path, method) {
      return method === "GET" || (method === "POST" && path === "/sessions");
    }
    function isConnectionRefusedError(error) {
      return error instanceof Error && error.message === "connection refused";
    }
    function unreachableMessage() {
      return "gentle-engram could not reach the Engram HTTP server";
    }
    const engramFetchResult = async function engramFetchResult(path, opts = {}) {
      ${body}
    };
    const engramFetch = async (path, opts = {}) => (await engramFetchResult(path, opts)).data;
    return { engramFetch, engramFetchResult };
  `,
  );
  return factory(
    globalThis.fetch,
    wait,
    (value) => value,
    (value) => value,
    "http://127.0.0.1:7437",
    timeoutMs,
    maxAttempts,
    backoffBaseMs,
    recover,
  );
}

function buildScheduleEngramSelfHealForTest({ waitUnref, isEngramRunning, maxAttempts = 6 }) {
  const body = extractFunctionBody("scheduleEngramSelfHeal", "{\n  // Track every session");
  const forgetBody = extractFunctionBody("forgetSelfHealContext", "{\n  engramSelfHealContexts.delete");
  const factory = new Function(
    "waitUnref",
    "isEngramRunning",
    "ENGRAM_SELF_HEAL_INTERVAL_MS",
    "ENGRAM_SELF_HEAL_MAX_ATTEMPTS",
    `
    let engramSelfHealInFlight = false;
    const engramSelfHealContexts = new Map();
    const getSessionId = (ctx) => ctx.sessionManager?.getSessionId();
    function forgetSelfHealContext(sessionId) {
      ${forgetBody}
    }
    function scheduleEngramSelfHeal(ctx) {
      ${body}
    }
    return {
      scheduleEngramSelfHeal,
      forgetSelfHealContext,
      isInFlight: () => engramSelfHealInFlight,
      trackedCount: () => engramSelfHealContexts.size,
    };
  `,
  );
  return factory(waitUnref, isEngramRunning, 1, maxAttempts);
}

function buildInitializeEngramServerForTest({
  configuredUrl = false,
  probeEngramHealth,
  spawnAndWaitForEngram,
  waitForEngramReadiness,
  timeoutMs = 10000,
}) {
  const body = extractFunctionBody("initializeEngramServer", "{\n  if (CONFIGURED_ENGRAM_URL");
  const factory = new Function(
    "CONFIGURED_ENGRAM_URL",
    "probeEngramHealth",
    "spawnAndWaitForEngram",
    "waitForEngramReadiness",
    "ENGRAM_STARTUP_TIMEOUT_MS",
    `
    async function initializeEngramServer() {
      ${body}
    }
    return initializeEngramServer;
    `,
  );
  return factory(
    configuredUrl ? "http://configured" : undefined,
    probeEngramHealth,
    spawnAndWaitForEngram,
    waitForEngramReadiness,
    timeoutMs,
  );
}

function buildProbeEngramHealthForTest({ fetch, isTimeoutError }) {
  const body = extractFunctionBody("probeEngramHealth", "{\n  try");
  const refusedBody = extractFunctionBody("hasConnectionRefusedCode", "{\n  if (depth")
    .replace("value as Record<string, unknown>", "value");
  const refusalBody = extractFunctionBody("isConnectionRefusedError", "{\n  return");
  const factory = new Function(
    "fetch",
    "isTimeoutError",
    "ENGRAM_URL",
    "AbortSignal",
    `
    function hasConnectionRefusedCode(value, depth = 0) {
      ${refusedBody}
    }
    function isConnectionRefusedError(error) {
      ${refusalBody}
    }
    async function probeEngramHealth() {
      ${body}
    }
    return probeEngramHealth;
    `,
  );
  return factory(fetch, isTimeoutError, "http://127.0.0.1:7437", { timeout: () => undefined });
}

// The retry backoff is clock-driven, so the test owns the clock: nothing here sleeps, and a
// backoff window is crossed by moving `clock.now` instead of by waiting for wall time.
function buildSharedInitializationForTest({ retryBaseMs = 1000, retryMaxMs = 60000 } = {}) {
  const clock = { now: 1_000_000 };
  const body = extractFunctionBody("sharedInitialization", "{\n  if (initialization)")
    .replace("(error: unknown) =>", "(error) =>");
  const backoffBody = extractFunctionBody("startupBackoffMs", "{\n  return Math.min");
  const factory = new Function(
    "Date",
    "ENGRAM_STARTUP_RETRY_BASE_MS",
    "ENGRAM_STARTUP_RETRY_MAX_MS",
    `
    let initialization;
    let startupFailures = 0;
    let startupRetryAt = 0;
    let startupFailure;
    let initializationGeneration = 0;
    function startupBackoffMs(failures) {
      ${backoffBody}
    }
    function sharedInitialization(start) {
      ${body}
    }
    return sharedInitialization;
    `,
  );
  const sharedInitialization = factory({ now: () => clock.now }, retryBaseMs, retryMaxMs);
  return { sharedInitialization, clock };
}

function buildRecoveryForTest({ configuredUrl = false, initializeEngramServer }) {
  const body = extractFunctionBody("recoverImplicitEngramServer", "{\n  const generation");
  const factory = new Function("CONFIGURED_ENGRAM_URL", "initializeEngramServer", `
    let initializationGeneration = 1;
    let recoveredInitializationGeneration = 0;
    let recoveryFlight;
    function recoverImplicitEngramServer() {
      ${body}
    }
    return {
      recoverImplicitEngramServer,
      setGeneration: (generation) => { initializationGeneration = generation; },
    };
  `);
  return factory(configuredUrl ? "http://configured" : undefined, initializeEngramServer);
}

// A fake child process: an EventEmitter with the ChildProcess members the startup path
// touches, plus counters so a test can prove the child was released or killed.
function createFakeChild() {
  const child = new EventEmitter();
  child.unrefCalls = 0;
  child.killCalls = 0;
  child.unref = () => {
    child.unrefCalls += 1;
  };
  child.kill = () => {
    child.killCalls += 1;
    return true;
  };
  return child;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Deadlines are absolute, so a test that must not time out gets a far one and a test that
// asserts the timeout gets a near one; neither depends on how loaded the runner is.
function deadlineIn(ms) {
  return Date.now() + ms;
}

function buildWaitForEngramReadinessForTest({ probeEngramHealth, pollMs = 5 }) {
  const factory = new Function(
    "probeEngramHealth",
    "ENGRAM_URL",
    "ENGRAM_STARTUP_POLL_MS",
    `
    function waitCancellable(ms, signal) {
      ${extractFunctionBody("waitCancellable", "{\n  return new Promise")}
    }
    async function waitForEngramReadiness(signal, deadline) {
      ${extractFunctionBody("waitForEngramReadiness", "{\n  while (Date.now()")}
    }
    return waitForEngramReadiness;
    `,
  );
  return factory(probeEngramHealth, "http://127.0.0.1:7437", pollMs);
}

function buildSpawnAndWaitForEngramForTest({ spawn, probeEngramHealth, pollMs = 5 }) {
  const spawnBody = extractFunctionBody("spawnAndWaitForEngram", "{\n  return new Promise")
    .replace("let proc: ChildProcess | undefined;", "let proc;")
    .replace("function settle(error?: Error): void {", "function settle(error) {")
    .replace("const onError = (error: Error): void =>", "const onError = (error) =>")
    .replace("const onExit = (code: number | null, signal: NodeJS.Signals | null): void =>", "const onExit = (code, signal) =>");
  const factory = new Function(
    "spawn",
    "probeEngramHealth",
    "ENGRAM_BIN",
    "ENGRAM_URL",
    "ENGRAM_STARTUP_POLL_MS",
    `
    function waitCancellable(ms, signal) {
      ${extractFunctionBody("waitCancellable", "{\n  return new Promise")}
    }
    async function waitForEngramReadiness(signal, deadline) {
      ${extractFunctionBody("waitForEngramReadiness", "{\n  while (Date.now()")}
    }
    function stopAbandonedChild(proc) {
      ${extractFunctionBody("stopAbandonedChild", "{\n  if (proc === undefined) return;")}
    }
    function spawnAndWaitForEngram(deadline) {
      ${spawnBody}
    }
    return spawnAndWaitForEngram;
    `,
  );
  return factory(spawn, probeEngramHealth, "engram", "http://127.0.0.1:7437", pollMs);
}

function buildEnsureSessionForTest(engramFetch) {
  const body = extractFunctionBody("ensureSession", "{\n  const key")
    .replace("const body: SessionBody", "const body");
  const factory = new Function("knownSessions", "sessionRegistrationsInFlight", "engramFetch", "project", "directory", `
    return async function ensureSession(sessionId, sessionProject = project, fetch = engramFetch) {
      ${body}
    };
  `);
  const knownSessions = new Set();
  const sessionRegistrationsInFlight = new Map();
  return {
    ensureSession: factory(knownSessions, sessionRegistrationsInFlight, engramFetch, "engram", "/work/engram"),
    knownSessions,
  };
}

function buildProjectDetectionBoundaryForTest() {
  const safeProjectBody = extractFunctionBody("isSafeDetectedProject", "{\n  const candidate");
  const applyBody = extractFunctionBody("applyDetectedProject", "{\n  if (!detected)");
  const requireBody = extractFunctionBody("requireResolvedProject", "{\n  if (projectResolutionError)");
  const factory = new Function(`
    let project = "fallback-project";
    let projectResolutionError;
    let projectDetectionPending = false;
    function isSafeDetectedProject(detected) {
      ${safeProjectBody}
    }
    function applyDetectedProject(detected) {
      ${applyBody}
    }
    function requireResolvedProject() {
      ${requireBody}
    }
    const requests = [];
    function writeMemory() {
      requireResolvedProject();
      requests.push("/sessions", "/observations");
    }
    return {
      applyDetectedProject,
      writeMemory,
      requests,
      state: () => ({ project, projectResolutionError, projectDetectionPending }),
    };
  `);
  return factory();
}

function sessionCtx(id, sink) {
  return {
    sessionManager: { getSessionId: () => id },
    ui: { setStatus: (key, text) => sink.push([key, text]) },
  };
}

test("mem_session_summary accepts explicit project fallback", () => {
  assert.match(source, /mem_session_summary: Type\.Object\(\{[\s\S]*project: optionalString\("Optional project to use when automatic detection is unavailable"\)/);
  assert.match(source, /case "mem_session_summary":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession\(summarySessionId, activeProject, fetch\)[\s\S]*project: activeProject/);
});

test("mem_save_prompt returns a prompt-scoped identity", () => {
  assert.match(source, /case "mem_save_prompt":[\s\S]*const response = await fetch<\{ id: number \}>\("\/prompts",/);
  assert.match(source, /case "mem_save_prompt":[\s\S]*return response \? \{ prompt_id: response\.id, status: "saved" \} : response;/);
});

test("mem_search exposes and forwards match_mode and all_projects", () => {
  assert.match(source, /mem_search: Type\.Object\(\{[\s\S]*all_projects: optionalBoolean\("Search across every project; when true project is ignored"\)/);
  assert.match(source, /mem_search: Type\.Object\(\{[\s\S]*match_mode: optionalString\("Match mode: all \(default\) or any for broader recall"\)/);
  assert.match(source, /case "mem_search":[\s\S]*if \(!params\.all_projects && !requestedProject\) requireResolvedProject\(\);[\s\S]*project: params\.all_projects \? undefined : activeProject[\s\S]*match_mode: params\.match_mode[\s\S]*all_projects: params\.all_projects/);
});

test("scope descriptions document values and unfiltered omission", () => {
  assert.match(source, /mem_search: Type\.Object\(\{[\s\S]*scope: optionalString\("Filter by scope: project, personal, or global\. Omit to apply no scope filter\."\)/);
  assert.match(source, /mem_save: Type\.Object\(\{[\s\S]*scope: optionalString\("Scope: project, personal, or global"\)/);
  assert.match(source, /mem_context: Type\.Object\(\{[\s\S]*scope: optionalString\("Filter observations by scope: project, personal, or global\. Omit to apply no scope filter\."\)/);
  assert.match(source, /mem_update: Type\.Object\(\{[\s\S]*scope: optionalString\("New scope: project, personal, or global"\)/);
});

test("project detection 404 falls back to local config or diagnostic", () => {
  assert.match(source, /function detectLocalConfigProject\(cwd: string\)/);
  assert.match(source, /project_name/);
  assert.match(source, /error\.status === 404[\s\S]*detectLocalConfigProject\(cwd\) \|\| projectCurrentUnsupportedError\(cwd\)/);
  assert.match(source, /does not support \/project\/current/);
});

test("unsafe detected projects do not reach session or memory writes", () => {
  assert.match(source, /case "mem_save":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession/);
  assert.match(source, /case "mem_save_prompt":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession/);
  assert.match(source, /case "mem_session_summary":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession/);

  for (const detected of [
    undefined,
    { project: "unknown" },
    { project: "alpha", error_hint: "" },
    { project: "nested/project" },
    { project: "C:\\workspace" },
    { project: "safe\u0000name" },
  ]) {
    const boundary = buildProjectDetectionBoundaryForTest();
    assert.equal(boundary.applyDetectedProject(detected), false);
    assert.equal(boundary.state().project, "unknown");
    assert.throws(() => boundary.writeMemory());
    assert.deepEqual(boundary.requests, []);
  }
});

test("safe detected projects continue through session and memory writes", () => {
  for (const project of ["engram", "c:compiler"]) {
    const boundary = buildProjectDetectionBoundaryForTest();
    assert.equal(boundary.applyDetectedProject({ project }), true);
    assert.equal(boundary.state().project, project);
    boundary.writeMemory();
    assert.deepEqual(boundary.requests, ["/sessions", "/observations"]);
  }
});

test("ambiguous_project error maps to actionable status label, not generic 'error'", () => {
  // The status bar must NOT show the generic 'error' label for ambiguous project conditions.
  // Instead it should show an actionable label such as 'ambiguous project'.
  assert.match(source, /function errorStatusLabel\(/);
  // Verify the function maps ambiguous project messages to the actionable label
  assert.match(source, /ambiguous project/);
  // Verify executeMemoryTool uses errorStatusLabel instead of the bare 'error' string
  assert.match(source, /errorStatusLabel\(message\)/);
  // The bare '· error' hardcoded string should no longer be present in the catch block
  assert.doesNotMatch(source, /setStatus\?\.\("engram",\s*`🧠 \$\{project\} · error`\)/);
});

test("memory protocol declares gentle-engram as the Pi-native provider", () => {
  assert.match(source, /These instructions are injected by gentle-engram, the Pi-native memory provider/);
  assert.match(source, /Use the memory tools named in this section as the authoritative Pi memory contract/);
  assert.match(source, /Do not infer alternative Engram tool names from other integrations/);
});

test("an inconclusive health probe still attempts the spawn", async () => {
  let probes = 0;
  let spawns = 0;
  let readinessWaits = 0;
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => {
      probes += 1;
      return "indeterminate";
    },
    spawnAndWaitForEngram: async () => { spawns += 1; },
    waitForEngramReadiness: async () => { readinessWaits += 1; },
  });

  await initializeEngramServer();
  assert.equal(probes, 1);
  assert.equal(spawns, 1, "no evidence of a live server means launch one, not wait for one");
  assert.equal(readinessWaits, 0, "the spawned child owns its readiness result");
});

test("an already-ready health endpoint neither spawns nor waits", async () => {
  let spawns = 0;
  let readinessWaits = 0;
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => "ready",
    spawnAndWaitForEngram: async () => { spawns += 1; },
    waitForEngramReadiness: async () => { readinessWaits += 1; },
  });

  await initializeEngramServer();
  assert.equal(spawns, 0);
  assert.equal(readinessWaits, 0);
});

test("an inconclusive probe falls back to an already-starting server when our child loses the port", async () => {
  let readinessWaits = 0;
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => "indeterminate",
    spawnAndWaitForEngram: async () => { throw new Error("Engram server exited before readiness (code 1)"); },
    waitForEngramReadiness: async () => { readinessWaits += 1; },
  });

  await initializeEngramServer();
  assert.equal(readinessWaits, 1, "an inconclusive probe leaves room for another instance to be coming up");
});

test("an inconclusive probe reports the child failure when nothing becomes ready", async () => {
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => "indeterminate",
    spawnAndWaitForEngram: async () => { throw new Error("Engram server exited before readiness (code 1)"); },
    waitForEngramReadiness: async () => { throw new Error("did not become ready before startup timeout"); },
  });

  await assert.rejects(initializeEngramServer(), /exited before readiness/, "the spawn failure is the actionable one");
});

test("a generic connection-refused health error is a definitive refusal", async () => {
  const probeEngramHealth = buildProbeEngramHealthForTest({
    fetch: async () => { throw new Error("connection refused"); },
    isTimeoutError: () => false,
  });

  assert.equal(await probeEngramHealth(), "refused");
});

test("a nested ECONNREFUSED health error is a definitive refusal", async () => {
  const probeEngramHealth = buildProbeEngramHealthForTest({
    fetch: async () => {
      throw Object.assign(new TypeError("fetch failed"), { cause: { code: "ECONNREFUSED" } });
    },
    isTimeoutError: () => false,
  });

  assert.equal(await probeEngramHealth(), "refused");
});

test("an aggregate of per-address ECONNREFUSED errors is a definitive refusal", async () => {
  const probeEngramHealth = buildProbeEngramHealthForTest({
    fetch: async () => {
      // What Node actually rejects with for a refused localhost that resolves to both ::1
      // and 127.0.0.1: the code lives on the per-address errors, not on the cause itself.
      const aggregate = new AggregateError([
        Object.assign(new Error("connect ECONNREFUSED ::1:7437"), { code: "ECONNREFUSED" }),
        Object.assign(new Error("connect ECONNREFUSED 127.0.0.1:7437"), { code: "ECONNREFUSED" }),
      ], "");
      throw Object.assign(new TypeError("fetch failed"), { cause: aggregate });
    },
    isTimeoutError: () => false,
  });

  assert.equal(await probeEngramHealth(), "refused");
});

test("the refusal classifier matches what Node actually rejects with on a closed port", async () => {
  // Exercises the real rejection shape rather than a hand-written stand-in for it: Node wraps
  // the refusal in a `cause`, and neither the message nor the code is where the outer error is.
  const port = await new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port: chosen } = probe.address();
      probe.close(() => resolve(chosen));
    });
  });
  const probeEngramHealth = buildProbeEngramHealthForTest({
    fetch: () => globalThis.fetch(`http://127.0.0.1:${port}/health`, { signal: AbortSignal.timeout(2000) }),
    isTimeoutError: (candidate) => candidate instanceof Error && (candidate.name === "TimeoutError" || candidate.name === "AbortError"),
  });

  assert.equal(await probeEngramHealth(), "refused");
});

test("an inherited ECONNREFUSED code on the cause is a definitive refusal", async () => {
  const probeEngramHealth = buildProbeEngramHealthForTest({
    fetch: async () => {
      // A cause whose `code` comes from its prototype has no own `code` key, so an
      // own-property check would misread a plain refusal as inconclusive.
      throw Object.assign(new TypeError("fetch failed"), { cause: Object.create({ code: "ECONNREFUSED" }) });
    },
    isTimeoutError: () => false,
  });

  assert.equal(await probeEngramHealth(), "refused");
});

test("timeout-shaped health errors remain indeterminate", async () => {
  for (const error of [
    Object.assign(new Error("timed out"), { name: "TimeoutError" }),
    Object.assign(new Error("aborted"), { name: "AbortError" }),
    new Error("request timeout"),
  ]) {
    const probeEngramHealth = buildProbeEngramHealthForTest({
      fetch: async () => { throw error; },
      isTimeoutError: (candidate) => candidate instanceof Error && (candidate.name === "TimeoutError" || candidate.name === "AbortError"),
    });
    assert.equal(await probeEngramHealth(), "indeterminate");
  }
});

test("a definitive refusal spawns once and awaits spawned-server readiness", async () => {
  let spawns = 0;
  let readinessWaits = 0;
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => "refused",
    spawnAndWaitForEngram: async () => { spawns += 1; },
    waitForEngramReadiness: async () => { readinessWaits += 1; },
  });

  await initializeEngramServer();
  assert.equal(spawns, 1);
  assert.equal(readinessWaits, 0, "the spawned child owns its readiness result");
});

test("a child error or bind-collision exit is a terminal initialization failure", async () => {
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => "refused",
    spawnAndWaitForEngram: async () => { throw new Error("Engram server exited before readiness (code 1)"); },
    waitForEngramReadiness: async () => assert.fail("a failed child must not fall through to readiness"),
  });

  await assert.rejects(initializeEngramServer(), /exited before readiness/);
});

test("concurrent initialization callers share one startup and its terminal result", async () => {
  const { sharedInitialization } = buildSharedInitializationForTest();
  let starts = 0;
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const start = async () => {
    starts += 1;
    await gate;
  };

  const first = sharedInitialization(start);
  const second = sharedInitialization(start);
  assert.strictEqual(first, second);
  assert.equal(starts, 1);
  release();
  await Promise.all([first, second]);
});

test("ENGRAM_URL bypasses Pi readiness and automatic spawn", async () => {
  let spawns = 0;
  let probes = 0;
  let readinessWaits = 0;
  const initializeEngramServer = buildInitializeEngramServerForTest({
    configuredUrl: true,
    probeEngramHealth: async () => { probes += 1; return "refused"; },
    spawnAndWaitForEngram: async () => { spawns += 1; },
    waitForEngramReadiness: async () => { readinessWaits += 1; },
  });

  await initializeEngramServer();
  assert.equal(spawns, 0);
  assert.equal(probes, 0);
  assert.equal(readinessWaits, 0);
});

test("mid-session recovery shares one flight per initialization generation", async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let restarts = 0;
  const recovery = buildRecoveryForTest({
    initializeEngramServer: async () => {
      restarts += 1;
      await gate;
    },
  });

  const first = recovery.recoverImplicitEngramServer();
  const second = recovery.recoverImplicitEngramServer();
  assert.strictEqual(first, second, "concurrent callers share the recovery flight");
  assert.equal(restarts, 1);
  release();
  assert.equal(await first, true);
  assert.equal(await recovery.recoverImplicitEngramServer(), false, "a generation gets one bounded recovery");

  recovery.setGeneration(2);
  assert.equal(await recovery.recoverImplicitEngramServer(), true, "a newer initialization does not reuse a stale flight");
  assert.equal(restarts, 2);
});

test("mid-session recovery never manages an explicit ENGRAM_URL", async () => {
  let restarts = 0;
  const recovery = buildRecoveryForTest({
    configuredUrl: true,
    initializeEngramServer: async () => { restarts += 1; },
  });

  assert.equal(await recovery.recoverImplicitEngramServer(), false);
  assert.equal(restarts, 0);
});

test("a child that reaches readiness is released to keep running, never killed", async () => {
  const child = createFakeChild();
  let probes = 0;
  const spawnAndWaitForEngram = buildSpawnAndWaitForEngramForTest({
    spawn: () => {
      queueMicrotask(() => child.emit("spawn"));
      return child;
    },
    probeEngramHealth: async () => {
      probes += 1;
      return probes >= 2 ? "ready" : "indeterminate";
    },
  });

  await spawnAndWaitForEngram(deadlineIn(30_000));

  assert.equal(child.unrefCalls, 1, "a ready child is released from the event loop");
  assert.equal(child.killCalls, 0, "the server we just started must survive initialization");
  const probesAtReadiness = probes;
  await delay(60);
  assert.equal(probes, probesAtReadiness, "readiness stops the health poll");
});

test("a child that errors before readiness is killed and stops health polling", async () => {
  const child = createFakeChild();
  let probes = 0;
  const spawnAndWaitForEngram = buildSpawnAndWaitForEngramForTest({
    spawn: () => {
      queueMicrotask(() => child.emit("spawn"));
      return child;
    },
    probeEngramHealth: async () => {
      probes += 1;
      return "indeterminate";
    },
  });

  const pending = spawnAndWaitForEngram(deadlineIn(30_000));
  await delay(20);
  child.emit("error", new Error("spawn ENOENT"));

  await assert.rejects(pending, /failed before readiness/);
  assert.equal(child.killCalls, 1, "the error path does not abandon a live child");
  assert.equal(child.unrefCalls, 1, "the error path releases the child");
  const probesAtRejection = probes;
  await delay(60);
  assert.equal(probes, probesAtRejection, "the error path cancels the health poll");
});

test("a child that exits before readiness is released and stops health polling", async () => {
  const child = createFakeChild();
  let probes = 0;
  const spawnAndWaitForEngram = buildSpawnAndWaitForEngramForTest({
    spawn: () => {
      queueMicrotask(() => child.emit("spawn"));
      return child;
    },
    probeEngramHealth: async () => {
      probes += 1;
      return "indeterminate";
    },
  });

  const pending = spawnAndWaitForEngram(deadlineIn(30_000));
  await delay(20);
  child.emit("exit", 1, null);

  await assert.rejects(pending, /exited before readiness \(code 1/);
  assert.equal(child.unrefCalls, 1, "the exit path releases the child");
  const probesAtRejection = probes;
  await delay(60);
  assert.equal(probes, probesAtRejection, "the exit path cancels the health poll");
});

test("a startup timeout kills the child instead of leaving it running", async () => {
  const child = createFakeChild();
  let probes = 0;
  const spawnAndWaitForEngram = buildSpawnAndWaitForEngramForTest({
    spawn: () => {
      queueMicrotask(() => child.emit("spawn"));
      return child;
    },
    probeEngramHealth: async () => {
      probes += 1;
      return "indeterminate";
    },
    pollMs: 5,
  });

  await assert.rejects(spawnAndWaitForEngram(deadlineIn(60)), /did not become ready before startup timeout/);
  assert.equal(child.killCalls, 1, "a child we gave up on is not left running detached");
  assert.equal(child.unrefCalls, 1, "the timeout path releases the child");
  const probesAtTimeout = probes;
  await delay(60);
  assert.equal(probes, probesAtTimeout, "the timeout path cancels the health poll");
});

test("a child that cannot be spawned rejects without leaving a health poll running", async () => {
  let probes = 0;
  const spawnAndWaitForEngram = buildSpawnAndWaitForEngramForTest({
    spawn: () => {
      throw new Error("EACCES");
    },
    probeEngramHealth: async () => {
      probes += 1;
      return "indeterminate";
    },
  });

  await assert.rejects(spawnAndWaitForEngram(deadlineIn(30_000)), /EACCES/);
  await delay(60);
  assert.equal(probes, 0, "a child that never spawned never starts a health poll");
});

test("repeated failed initializations do not accumulate live children", async () => {
  const children = [];
  const spawnAndWaitForEngram = buildSpawnAndWaitForEngramForTest({
    spawn: () => {
      const child = createFakeChild();
      children.push(child);
      queueMicrotask(() => {
        child.emit("spawn");
        // A server that starts and then never answers /health: the readiness budget, not the
        // child, is what ends the attempt — exactly the shape that leaked orphans.
      });
      return child;
    },
    probeEngramHealth: async () => "indeterminate",
    pollMs: 5,
  });
  const initializeEngramServer = buildInitializeEngramServerForTest({
    probeEngramHealth: async () => "refused",
    spawnAndWaitForEngram,
    waitForEngramReadiness: async () => assert.fail("a definitive refusal has no phantom server to wait for"),
    timeoutMs: 40,
  });
  const { sharedInitialization, clock } = buildSharedInitializationForTest();

  for (let attempt = 0; attempt < 25; attempt += 1) {
    await assert.rejects(sharedInitialization(() => initializeEngramServer()));
  }
  assert.equal(children.length, 1, "a hot retry loop must not spawn a child per call");

  clock.now += 5000;
  await assert.rejects(sharedInitialization(() => initializeEngramServer()));
  assert.equal(children.length, 2, "once the backoff expires the startup is attempted again");

  for (const child of children) {
    assert.equal(child.killCalls, 1, "every abandoned child is killed, not left detached");
  }
});

test("aborting the readiness wait cancels the health poll immediately", async () => {
  let probes = 0;
  const waitForEngramReadiness = buildWaitForEngramReadinessForTest({
    probeEngramHealth: async () => {
      probes += 1;
      return "indeterminate";
    },
    pollMs: 5,
  });

  const controller = new AbortController();
  const pending = waitForEngramReadiness(controller.signal, deadlineIn(30_000));
  await delay(20);
  controller.abort();

  await assert.rejects(pending, /cancelled/);
  const probesAtAbort = probes;
  await delay(60);
  assert.equal(probes, probesAtAbort, "an aborted readiness wait issues no further probes");
});

test("a failing startup backs off instead of re-running on every caller", async () => {
  const { sharedInitialization } = buildSharedInitializationForTest({ retryBaseMs: 1000, retryMaxMs: 60000 });
  let starts = 0;
  const start = async () => {
    starts += 1;
    throw new Error("did not become ready before startup timeout");
  };

  for (let call = 0; call < 20; call += 1) {
    await assert.rejects(sharedInitialization(start), /did not become ready/);
  }

  assert.equal(starts, 1, "callers inside the backoff window replay the failure instead of paying the budget");
});

test("the startup backoff expires so a transient failure is still retried", async () => {
  const { sharedInitialization, clock } = buildSharedInitializationForTest({ retryBaseMs: 1000, retryMaxMs: 60000 });
  let starts = 0;
  const start = async () => {
    starts += 1;
    if (starts === 1) throw new Error("did not become ready before startup timeout");
  };

  await assert.rejects(sharedInitialization(start));
  assert.equal(starts, 1);

  clock.now += 1000;
  await sharedInitialization(start);
  assert.equal(starts, 2, "a transient failure recovers once its backoff window closes");

  await sharedInitialization(start);
  assert.equal(starts, 2, "a successful startup is cached, not re-run");
});

test("the startup backoff grows with consecutive failures and stays capped", async () => {
  const { sharedInitialization, clock } = buildSharedInitializationForTest({ retryBaseMs: 1000, retryMaxMs: 4000 });
  const start = async () => {
    throw new Error("did not become ready before startup timeout");
  };
  const attemptAt = async (advanceMs) => {
    clock.now += advanceMs;
    let ran = false;
    await assert.rejects(sharedInitialization(async () => {
      ran = true;
      await start();
    }));
    return ran;
  };

  assert.equal(await attemptAt(0), true, "the first failure runs the startup");
  assert.equal(await attemptAt(999), false, "still inside the 1000ms window");
  assert.equal(await attemptAt(1), true, "the 1000ms window has closed");
  assert.equal(await attemptAt(1999), false, "the second failure doubled the window to 2000ms");
  assert.equal(await attemptAt(1), true);
  assert.equal(await attemptAt(3999), false, "the third failure doubled the window to 4000ms");
  assert.equal(await attemptAt(1), true);
  assert.equal(await attemptAt(3999), false, "the window is capped at 4000ms, it does not keep doubling");
  assert.equal(await attemptAt(1), true);
});

test("native tool fetches retry transient HTTP startup failures", async () => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls < 3) throw new Error("connection refused");
    return {
      ok: true,
      async json() {
        return { status: "ok" };
      },
    };
  };
  try {
    const { engramFetch } = buildEngramFetchForTest();
    assert.deepEqual(await engramFetch("/health"), { status: "ok" });
    assert.equal(calls, 3);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("native tool fetch backs off exponentially and attaches a per-request timeout", async () => {
  const originalFetch = globalThis.fetch;
  const originalAbortSignalTimeout = AbortSignal.timeout;
  const waits = [];
  let observedTimeoutMs;
  AbortSignal.timeout = (ms) => {
    observedTimeoutMs = ms;
    return originalAbortSignalTimeout(ms);
  };
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls < 3) throw new Error("connection refused");
    return {
      ok: true,
      async json() {
        return { status: "ok" };
      },
    };
  };
  try {
    const { engramFetch } = buildEngramFetchForTest({
      wait: (ms) => {
        waits.push(ms);
        return Promise.resolve();
      },
      timeoutMs: 2500,
      backoffBaseMs: 150,
    });
    assert.deepEqual(await engramFetch("/health"), { status: "ok" });
    assert.equal(calls, 3);
    assert.deepEqual(waits, [150, 300]);
    assert.equal(observedTimeoutMs, 2500);
  } finally {
    globalThis.fetch = originalFetch;
    AbortSignal.timeout = originalAbortSignalTimeout;
  }
});

test("a timed-out write is not re-sent, so a slow-but-applied mem_save cannot be duplicated", async () => {
  const originalFetch = globalThis.fetch;
  const sentBodies = [];
  globalThis.fetch = async (_url, init) => {
    sentBodies.push(init?.body);
    const timeout = new Error("The operation was aborted due to timeout");
    timeout.name = "TimeoutError";
    throw timeout;
  };
  try {
    const { engramFetch } = buildEngramFetchForTest();
    assert.equal(await engramFetch("/observations", { method: "POST", body: { title: "t" } }), null);
    assert.equal(sentBodies.length, 1, "a timeout must not re-send a non-idempotent write");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("a timeout resolves to null like any other failure, so callers keep their fallthrough", async () => {
  // engramFetch's return contract must NOT change: ensureSession and ~20 other call sites
  // rely on the null fallthrough, and throwing here once aborted a mem_save before the
  // observation write was ever attempted.
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    const timeout = new Error("The operation was aborted due to timeout");
    timeout.name = "TimeoutError";
    throw timeout;
  };
  try {
    const { engramFetch, engramFetchResult } = buildEngramFetchForTest();
    assert.equal(await engramFetch("/sessions", { method: "POST", body: { id: "s" } }), null);
    assert.deepEqual(await engramFetchResult("/sessions", { method: "POST", body: { id: "s" } }), { data: null, timedOutMethod: "POST" });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("an unsafe write connection failure reports the generic unavailable error", async () => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    throw new Error("connection refused");
  };
  try {
    const { engramFetch } = buildEngramFetchForTest();
    await assert.rejects(() => engramFetch("/observations", { method: "POST", body: { title: "t" } }), /could not reach/);
    assert.equal(calls, 1, "an unsafe write is sent once before reporting the failure");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("the tool layer reports unknown write outcome instead of inviting a blind retry", () => {
  const factory = new Function(
    "ENGRAM_FETCH_TIMEOUT_MS",
    "ENGRAM_URL",
    `return function unreachableMessage(timedOutMethod) {
      ${extractFunctionBody("unreachableMessage", "{\n  if (timedOutMethod")}
    };`,
  );
  const unreachableMessage = factory(3000, "http://127.0.0.1:7437");

  // A write whose outcome is genuinely unknown must not be presented as a plain outage.
  const write = unreachableMessage("POST");
  assert.match(write, /may already have been applied/);
  assert.match(write, /do NOT blindly retry/);
  assert.doesNotMatch(write, /could not reach/);

  // A read carries no duplicate-write risk, so it must not carry the scary warning.
  const read = unreachableMessage("GET");
  assert.match(read, /did not respond/);
  assert.doesNotMatch(read, /may already have been applied/);

  // A genuine unreachable server keeps the original wording other tests pin.
  const unreachable = unreachableMessage(undefined);
  assert.match(unreachable, /could not reach the Engram HTTP server/);
  assert.doesNotMatch(unreachable, /timed out/);
});

test("session registration requires acknowledgement and failed acknowledgement remains retryable", async () => {
  let calls = 0;
  const { ensureSession, knownSessions } = buildEnsureSessionForTest(async () => {
    calls += 1;
    return calls === 1 ? null : { status: "created" };
  });

  await assert.rejects(ensureSession("runtime"), /could not confirm session registration/);
  assert.equal(knownSessions.has("engram:runtime"), false);
  await ensureSession("runtime");
  assert.equal(knownSessions.has("engram:runtime"), true);
  await ensureSession("runtime");
  assert.equal(calls, 2);
});

test("session compaction strictly registers before forwarding its summary", () => {
  const compactStart = source.indexOf('pi.on("session_compact"');
  const compactEnd = source.indexOf('\n  pi.on("before_agent_start"', compactStart);
  assert.notEqual(compactStart, -1, "session_compact handler not found");
  assert.notEqual(compactEnd, -1, "session_compact handler end not found");
  const compactHandler = source.slice(compactStart, compactEnd);

  const registration = compactHandler.indexOf("await ensureSession(sessionId);");
  const summaryPost = compactHandler.indexOf("await archiveCompactionSummary(sessionId, summary);");
  assert.notEqual(registration, -1, "session_compact must await strict session registration");
  assert.notEqual(summaryPost, -1, "session_compact summary post not found");
  assert.ok(registration < summaryPost, "strict registration must precede summary forwarding");
  assert.doesNotMatch(compactHandler, /ensureSessionBestEffort/, "session_compact must not hide registration failure");
  assert.match(source, /async function archiveCompactionSummary[\s\S]*engramFetchResult\("\/observations"/);
});

test("session compaction never captures or reads stale Pi context", () => {
  const compactStart = source.indexOf('pi.on("session_compact"');
  const compactEnd = source.indexOf('\n  pi.on("before_agent_start"', compactStart);
  assert.notEqual(compactStart, -1, "session_compact handler not found");
  assert.notEqual(compactEnd, -1, "session_compact handler end not found");

  const compactHandler = source.slice(compactStart, compactEnd);
  assert.doesNotMatch(compactHandler, /\bctx\b/, "session_compact must not capture or access stale Pi context");
});

test("four session-attributed writes ignore model session_id and require the Pi runtime ID", () => {
  for (const tool of ["mem_save", "mem_save_prompt", "mem_session_summary", "mem_capture_passive"]) {
    const schema = source.match(new RegExp(`${tool}: Type\\.Object\\(\\{([\\s\\S]*?)\\n  \\}\\),`));
    assert.ok(schema, `${tool} schema not found`);
    assert.doesNotMatch(schema[1], /session_id:/, `${tool} must not invite model-supplied session identity`);
  }
  assert.match(source, /function requireRuntimeSessionID/);
  assert.match(source, /ctx\.sessionManager\.getSessionId\(\)/);
  assert.match(source, /Pi runtime session ID is unavailable/);
  assert.doesNotMatch(source, /const activeSessionId = String\(params\.session_id/);
  assert.doesNotMatch(source, /manual-save-\$\{requestedProject\}/);
});

test("a timeout on the session leg does not mislabel an unrelated failure on the write leg", async () => {
  // mem_save issues two fetches. If /sessions times out and /observations then fails for an
  // unrelated reason, the write genuinely never reached the server — telling the agent it
  // "may already have been applied" would suppress a retry that is both safe and necessary.
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => {
    if (new URL(url).pathname === "/sessions") {
      const timeout = new Error("The operation was aborted due to timeout");
      timeout.name = "TimeoutError";
      throw timeout;
    }
    throw new Error("connection refused");
  };
  try {
    const { engramFetch, engramFetchResult } = buildEngramFetchForTest();
    assert.deepEqual(
      await engramFetchResult("/sessions", { method: "POST", body: { id: "s" } }),
      { data: null, timedOutMethod: "POST" },
    );
    await assert.rejects(
      () => engramFetch("/observations", { method: "POST", body: { title: "t" } }),
      /could not reach/,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("isTimeoutError matches what Node actually rejects with on a real AbortSignal.timeout", async () => {
  // Pins the runtime contract the whole no-retry-on-timeout guarantee depends on: if Node
  // ever stopped rejecting with an Error named TimeoutError, detection would silently fall
  // through to the retry path and reactivate the duplicate-write bug.
  const { createServer } = await import("node:http");
  const server = createServer(() => {});
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  const factory = new Function(`
    return function isTimeoutError(error) {
      ${extractFunctionBody("isTimeoutError", "{\n  return error instanceof Error")}
    };
  `);
  const isTimeoutError = factory();
  try {
    await fetch(`http://127.0.0.1:${port}/health`, { signal: AbortSignal.timeout(150) });
    assert.fail("the hung server should have triggered the timeout");
  } catch (error) {
    assert.equal(isTimeoutError(error), true, `unrecognized timeout rejection: ${error.name}`);
  } finally {
    server.close();
  }
});

test("unsafe writes do not retry after a connection failure", async () => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls < 3) throw new Error("connection refused");
    return {
      ok: true,
      async json() {
        return { status: "ok" };
      },
    };
  };
  try {
    const { engramFetch } = buildEngramFetchForTest();
    await assert.rejects(() => engramFetch("/observations", { method: "POST", body: { title: "t" } }), /could not reach/);
    assert.equal(calls, 1, "a write without an idempotency contract is never replayed");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("a mid-session local refusal restarts once and replays a safe read", async () => {
  const originalFetch = globalThis.fetch;
  let fetches = 0;
  let recoveries = 0;
  globalThis.fetch = async () => {
    fetches += 1;
    if (fetches <= 3) throw new Error("connection refused");
    return { ok: true, async json() { return { observations: [] }; } };
  };
  try {
    const { engramFetch } = buildEngramFetchForTest({
      wait: () => Promise.resolve(),
      recover: async () => { recoveries += 1; return true; },
    });
    assert.deepEqual(await engramFetch("/search?q=recovered"), { observations: [] });
    assert.equal(recoveries, 1, "the failed initialized generation gets one recovery attempt");
    assert.equal(fetches, 4, "the safe read is replayed only after recovery reports readiness");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("self-heal clears the stale engram status once the server becomes reachable again", async () => {
  const statusCalls = [];
  const ctx = { ui: { setStatus: (key, text) => statusCalls.push([key, text]) } };
  const { scheduleEngramSelfHeal, isInFlight } = buildScheduleEngramSelfHealForTest({
    waitUnref: () => Promise.resolve(),
    isEngramRunning: async () => true,
  });
  scheduleEngramSelfHeal(ctx);
  assert.equal(isInFlight(), true);
  await flush();
  assert.deepEqual(statusCalls, [["engram", undefined]]);
  assert.equal(isInFlight(), false);
});

test("self-heal does not start a second probe while one is already in flight", async () => {
  let isEngramRunningCalls = 0;
  const { scheduleEngramSelfHeal, isInFlight } = buildScheduleEngramSelfHealForTest({
    waitUnref: () => Promise.resolve(),
    isEngramRunning: async () => {
      isEngramRunningCalls += 1;
      return isEngramRunningCalls >= 2;
    },
  });
  const ctx = { ui: { setStatus: () => {} } };
  scheduleEngramSelfHeal(ctx);
  scheduleEngramSelfHeal(ctx);
  await flush();
  assert.equal(isEngramRunningCalls, 2);
  assert.equal(isInFlight(), false);
});

test("self-heal clears the stale status on every session that observed the outage", async () => {
  const sessionA = [];
  const sessionB = [];
  const { scheduleEngramSelfHeal } = buildScheduleEngramSelfHealForTest({
    waitUnref: () => Promise.resolve(),
    isEngramRunning: async () => true,
  });
  scheduleEngramSelfHeal({ ui: { setStatus: (key, text) => sessionA.push([key, text]) } });
  scheduleEngramSelfHeal({ ui: { setStatus: (key, text) => sessionB.push([key, text]) } });
  await flush();
  assert.deepEqual(sessionA, [["engram", undefined]]);
  assert.deepEqual(sessionB, [["engram", undefined]], "second session must not keep a stale error label");
});

test("a session that shuts down mid-outage is dropped instead of having its dead UI touched", async () => {
  const alive = [];
  const shutDown = [];
  const { scheduleEngramSelfHeal, forgetSelfHealContext, trackedCount } = buildScheduleEngramSelfHealForTest({
    waitUnref: () => Promise.resolve(),
    isEngramRunning: async () => true,
  });
  scheduleEngramSelfHeal(sessionCtx("session-alive", alive));
  scheduleEngramSelfHeal(sessionCtx("session-gone", shutDown));
  forgetSelfHealContext("session-gone");
  await flush();
  assert.deepEqual(alive, [["engram", undefined]]);
  assert.deepEqual(shutDown, [], "a shut-down session must not have its status touched");
});

test("repeated failures in one session are tracked once, not accumulated per tool call", async () => {
  const sink = [];
  const { scheduleEngramSelfHeal, trackedCount } = buildScheduleEngramSelfHealForTest({
    waitUnref: () => Promise.resolve(),
    isEngramRunning: async () => false,
    maxAttempts: 1,
  });
  scheduleEngramSelfHeal(sessionCtx("session-a", sink));
  scheduleEngramSelfHeal(sessionCtx("session-a", sink));
  scheduleEngramSelfHeal(sessionCtx("session-a", sink));
  assert.equal(trackedCount(), 1, "one session must occupy one slot regardless of failure count");
});

test("self-heal gives up after exhausting its attempt budget without clearing the status", async () => {
  const statusCalls = [];
  const ctx = { ui: { setStatus: (key, text) => statusCalls.push([key, text]) } };
  const { scheduleEngramSelfHeal, isInFlight } = buildScheduleEngramSelfHealForTest({
    waitUnref: () => Promise.resolve(),
    isEngramRunning: async () => false,
    maxAttempts: 2,
  });
  scheduleEngramSelfHeal(ctx);
  await flush();
  assert.deepEqual(statusCalls, []);
  assert.equal(isInFlight(), false);
});

test("only reachability failures schedule self-heal, HTTP errors from a live server do not", () => {
  assert.match(source, /if \(!\(error instanceof EngramHttpError\)\) scheduleEngramSelfHeal\(ctx\);/);
});

test("waitUnref schedules a background timer that does not keep the process alive", async () => {
  const body = extractFunctionBody("waitUnref", "{\n  return new Promise");
  const factory = new Function(`
    return function waitUnref(ms) {
      ${body}
    };
  `);
  const waitUnref = factory();

  const originalSetTimeout = globalThis.setTimeout;
  let unrefCalled = false;
  globalThis.setTimeout = (fn, ms) => {
    const timer = originalSetTimeout(fn, ms);
    const originalUnref = timer.unref.bind(timer);
    timer.unref = () => {
      unrefCalled = true;
      return originalUnref();
    };
    return timer;
  };
  try {
    await waitUnref(0);
    assert.equal(unrefCalled, true);
  } finally {
    globalThis.setTimeout = originalSetTimeout;
  }
});

test("native tool fetch preserves HTTP error status", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false,
    status: 503,
    async json() {
      return { error: "server warming up" };
    },
  });
  try {
    const { engramFetch } = buildEngramFetchForTest();
    await assert.rejects(
      () => engramFetch("/search"),
      (error) => error.name === "EngramHttpError" && error.status === 503 && error.message === "server warming up",
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("native tool unavailable error names the Pi-native HTTP path", () => {
  assert.match(source, /gentle-engram could not reach the Engram HTTP server/);
  assert.match(source, /Pi-native mem_\* tools are registered/);
  assert.match(source, /Run mem_doctor or restart Engram/);
});

test("mem_review is registered as a Pi-native executable memory tool", () => {
  assert.match(source, /const ENGRAM_TOOLS = \[[\s\S]*"mem_review"/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*action: Type\.String\(\{ description: "Action: list \| mark_reviewed" \}\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*project: optionalString\("Optional project selector: filters list and scopes mark_reviewed; list remains global when omitted"\)/);
  assert.doesNotMatch(source, /project: optionalString\("Optional project filter for action=list"\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*observation_id: optionalNumber\("Observation id for action=mark_reviewed"\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*id: optionalNumber\("Alias for observation_id"\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "list"[\s\S]*fetch\(`\/review\$\{queryString\(\{ project: requestedProject, limit: params\.limit, all_projects: requestedProject \? undefined : true \}\)\}`\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "mark_reviewed"[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*fetch\(`\/review\/mark_reviewed\$\{queryString\(\{ project: activeProject \}\)\}`/);
  assert.match(source, /case "mem_review":[\s\S]*body: \{ observation_id: params\.observation_id \|\| params\.id \}/);
  assert.match(source, /for \(const toolName of ENGRAM_TOOLS\)[\s\S]*executeMemoryTool\(toolName/);
});

test("mem_stats explicitly requests its global aggregate contract", () => {
  assert.match(source, /case "mem_stats":[\s\S]*fetch\(`\/stats\$\{queryString\(\{ all_projects: true \}\)\}`\)/);
});

test("best-effort capture failures are surfaced instead of silently discarded", () => {
  // A passive capture that the server rejects (for example because the parent
  // session carries no project ownership) must not vanish: the operator has no
  // other signal that memories stopped being saved.
  assert.match(source, /function warnEngramFailure\(/);
  assert.match(source, /process\.stderr\.write/);
  assert.match(
    source,
    /async function bestEffortEngramFetch[\s\S]*catch \(error\) \{[\s\S]*warnEngramFailure\(path, error\)/,
  );
  assert.doesNotMatch(
    source,
    /async function bestEffortEngramFetch[\s\S]{0,200}catch \{\s*\n\s*return null;/,
  );
});

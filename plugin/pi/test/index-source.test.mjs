import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const source = readFileSync(new URL("../index.ts", import.meta.url), "utf8");

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
} = {}) {
  const body = extractFunctionBody("engramFetch", "{\n  const method")
    .replace("let res: Response | undefined;", "let res;")
    .replace("let data: unknown = null;", "let data = null;")
    .replace("return data as TResponse;", "return data;");
  const factory = new Function(
    "fetch",
    "wait",
    "redactUrlPath",
    "redactValue",
    "ENGRAM_URL",
    "ENGRAM_FETCH_TIMEOUT_MS",
    "ENGRAM_FETCH_MAX_ATTEMPTS",
    "ENGRAM_FETCH_BACKOFF_BASE_MS",
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
    let lastFetchTimeoutMethod;
    const engramFetch = async function engramFetch(path, opts = {}) {
      ${body}
    };
    return { engramFetch, timedOutMethod: () => lastFetchTimeoutMethod };
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

function sessionCtx(id, sink) {
  return {
    sessionManager: { getSessionId: () => id },
    ui: { setStatus: (key, text) => sink.push([key, text]) },
  };
}

test("mem_session_summary accepts explicit project fallback", () => {
  assert.match(source, /mem_session_summary: Type\.Object\(\{[\s\S]*project: optionalString\("Optional project to use when automatic detection is unavailable"\)/);
  assert.match(source, /case "mem_session_summary":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession\(activeSessionId, activeProject\)[\s\S]*project: activeProject/);
});

test("mem_search exposes and forwards match_mode and all_projects", () => {
  assert.match(source, /mem_search: Type\.Object\(\{[\s\S]*all_projects: optionalBoolean\("Search across every project; when true project is ignored"\)/);
  assert.match(source, /mem_search: Type\.Object\(\{[\s\S]*match_mode: optionalString\("Match mode: all \(default\) or any for broader recall"\)/);
  assert.match(source, /case "mem_search":[\s\S]*project: params\.all_projects \? undefined : params\.project[\s\S]*match_mode: params\.match_mode[\s\S]*all_projects: params\.all_projects/);
});

test("project detection 404 falls back to local config or diagnostic", () => {
  assert.match(source, /function detectLocalConfigProject\(cwd: string\)/);
  assert.match(source, /project_name/);
  assert.match(source, /error\.status === 404[\s\S]*detectLocalConfigProject\(cwd\) \|\| projectCurrentUnsupportedError\(cwd\)/);
  assert.match(source, /does not support \/project\/current/);
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
    const { engramFetch, timedOutMethod } = buildEngramFetchForTest();
    assert.equal(await engramFetch("/sessions", { method: "POST", body: { id: "s" } }), null);
    // The timeout detail travels out-of-band instead of through the return value.
    assert.equal(timedOutMethod(), "POST");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("a connection failure records no timeout method, so the generic message is used", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    throw new Error("connection refused");
  };
  try {
    const { engramFetch, timedOutMethod } = buildEngramFetchForTest();
    assert.equal(await engramFetch("/observations", { method: "POST", body: { title: "t" } }), null);
    assert.equal(timedOutMethod(), undefined);
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

test("a session-creation timeout still lets the observation write through", async () => {
  // Regression: when engramFetch threw on timeout, the unguarded ensureSession call in
  // mem_save aborted the whole tool call before /observations was ever attempted, silently
  // dropping the user's memory while telling the agent not to retry.
  assert.match(source, /await ensureSession\(activeSessionId, activeProject\);/);
  assert.doesNotMatch(source, /throw new EngramTimeoutError/);

  const originalFetch = globalThis.fetch;
  const paths = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    paths.push(path);
    if (path === "/sessions") {
      const timeout = new Error("The operation was aborted due to timeout");
      timeout.name = "TimeoutError";
      throw timeout;
    }
    return { ok: true, async json() { return { id: 1 }; } };
  };
  try {
    const { engramFetch } = buildEngramFetchForTest();
    // ensureSession's own call fails soft...
    assert.equal(await engramFetch("/sessions", { method: "POST", body: { id: "s" } }), null);
    // ...and the observation write that follows it still lands.
    assert.deepEqual(await engramFetch("/observations", { method: "POST", body: { title: "t" } }), { id: 1 });
    assert.deepEqual(paths, ["/sessions", "/observations"]);
  } finally {
    globalThis.fetch = originalFetch;
  }
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
    const { engramFetch, timedOutMethod } = buildEngramFetchForTest();
    assert.equal(await engramFetch("/sessions", { method: "POST", body: { id: "s" } }), null);
    assert.equal(timedOutMethod(), "POST", "the session leg did time out");
    assert.equal(await engramFetch("/observations", { method: "POST", body: { title: "t" } }), null);
    assert.equal(timedOutMethod(), undefined, "the write leg's own failure must supersede the stale timeout");
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

test("connection failures still retry, because they never reached the server", async () => {
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
    assert.deepEqual(await engramFetch("/observations", { method: "POST", body: { title: "t" } }), { status: "ok" });
    assert.equal(calls, 3);
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
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*observation_id: optionalNumber\("Observation id for action=mark_reviewed"\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*id: optionalNumber\("Alias for observation_id"\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "list"[\s\S]*engramFetch\(`\/review\$\{queryString\(\{ project: params\.project, limit: params\.limit \}\)\}`\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "mark_reviewed"[\s\S]*engramFetch\("\/review\/mark_reviewed"/);
  assert.match(source, /case "mem_review":[\s\S]*body: \{ observation_id: params\.observation_id \|\| params\.id \}/);
  assert.match(source, /for \(const toolName of ENGRAM_TOOLS\)[\s\S]*executeMemoryTool\(toolName/);
});

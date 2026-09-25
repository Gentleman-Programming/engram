import assert from "node:assert/strict";
import { test } from "node:test";
import { importPluginFromSandbox, PLUGIN_ROOT, withPluginSandbox } from "./plugin-sandbox.mjs";

// The runtime context these fixtures hand the plugin still points at the checkout, because the
// plugin only ever reads `cwd`. The module it loads comes from the sandbox, so nothing under the
// checkout is written to or removed.
const ROOT = PLUGIN_ROOT;

function deferred() {
  let resolve;
  const promise = new Promise((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

async function waitFor(promise, message) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(message)), 3000); }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

// Each sandbox lives at its own path, so every call already loads a fresh module graph and no
// cache-busting query string is needed.
async function loadPluginHarness(sandbox, appendEntry) {
  const registeredTools = new Map();
  const eventHandlers = new Map();
  const registerEngram = await importPluginFromSandbox(sandbox);
  registerEngram({
    registerTool(tool) {
      registeredTools.set(tool.name, tool);
    },
    appendEntry,
    on(event, handler) {
      eventHandlers.set(event, handler);
    },
  });
  return { registeredTools, eventHandlers };
}

function runtimeContext(sessionId) {
  return {
    cwd: ROOT,
    sessionManager: { getSessionId: typeof sessionId === "function" ? sessionId : () => sessionId },
    ui: { setStatus() {} },
  };
}

// Records every request the extension issues so a test can assert the wire contract the Engram
// HTTP server actually receives, instead of asserting over the extension source text.
function recordingFetch(routes) {
  const calls = [];
  const fetchStub = async (url, init = {}) => {
    const method = init.method ?? "GET";
    const path = new URL(url).pathname + new URL(url).search;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    const route = routes.find((candidate) => candidate.method === method && path.startsWith(candidate.path));
    const status = route?.status ?? 200;
    const payload = route?.body ?? {};
    return new Response(JSON.stringify(payload), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  };
  return { calls, fetchStub };
}

test("registered Pi-native mem_save_prompt persists through the Engram /prompts endpoint", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";

  // The server assigns prompt ids from user_prompts, a table whose sequence is independent of
  // observations. Issue #706 read one of these low ids as an observation id; the response must
  // therefore name the namespace it belongs to.
  const serverAssignedPromptID = 213;
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "GET", path: "/project/current", body: { project: "paidosdep" } },
    { method: "POST", path: "/sessions", body: { status: "ok" } },
    { method: "POST", path: "/prompts", status: 201, body: { id: serverAssignedPromptID, status: "saved" } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const memSavePrompt = registeredTools.get("mem_save_prompt");
      assert.ok(memSavePrompt, "mem_save_prompt tool should be registered");

      const result = await memSavePrompt.execute(
        "tool-call-prompt",
        { content: "preserve this exact user prompt", project: "paidosdep" },
        undefined,
        undefined,
        runtimeContext("test-session"),
      );

      assert.notEqual(result.isError, true, "a successful prompt save must not surface as a tool error");

      // The prompt must reach POST /prompts carrying the requested project scope, and the session it
      // references must have been created under that same project first.
      const promptCall = calls.find((call) => call.method === "POST" && call.path === "/prompts");
      assert.ok(promptCall, "mem_save_prompt must POST to /prompts");
      assert.equal(promptCall.body.project, "paidosdep");
      assert.equal(promptCall.body.content, "preserve this exact user prompt");
      assert.ok(promptCall.body.session_id, "the prompt must be attributed to a session");

      const sessionCall = calls.find((call) => call.method === "POST" && call.path === "/sessions");
      assert.ok(sessionCall, "mem_save_prompt must ensure its session exists before writing");
      assert.equal(sessionCall.body.project, "paidosdep");
      assert.equal(sessionCall.body.id, promptCall.body.session_id);
      assert.ok(
        calls.indexOf(sessionCall) < calls.indexOf(promptCall),
        "the session must be created before the prompt that references it",
      );

      // The returned identity is prompt-scoped: it echoes the id the server assigned, and it is not
      // offered under a name that mem_get_observation would accept.
      assert.deepEqual(result.details.data, { prompt_id: serverAssignedPromptID, status: "saved" });
      assert.equal(result.details.data.id, undefined, "an observation-shaped id must not be returned");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("one Pi runtime session cannot capture prompts or passive observations across projects", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  const originalStderrWrite = process.stderr.write;
  const warnings = [];
  process.stderr.write = (chunk) => {
    warnings.push(String(chunk));
    return true;
  };
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "GET", path: "/project/current", body: { project: "project-b" } },
    { method: "POST", path: "/sessions", body: { status: "created" } },
    { method: "POST", path: "/prompts", body: { id: 1 } },
    { method: "POST", path: "/observations/passive", body: { id: 2 } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const memSavePrompt = registeredTools.get("mem_save_prompt");
      const sessionId = "cross-project-runtime-session";
      const ctx = runtimeContext(sessionId);

      const firstPrompt = await memSavePrompt.execute(
        "project-a-first-prompt",
        { content: "first prompt under the persisted project owner", project: "project-a" },
        undefined,
        undefined,
        ctx,
      );
      const sameProjectPrompt = await memSavePrompt.execute(
        "project-a-second-prompt",
        { content: "normal same-project capture remains available", project: "project-a" },
        undefined,
        undefined,
        ctx,
      );
      assert.equal(firstPrompt.isError, undefined);
      assert.equal(sameProjectPrompt.isError, undefined, "same-project prompt capture must remain available");

      const crossProjectPrompt = await memSavePrompt.execute(
        "project-b-prompt",
        { content: "this prompt must not be sent under another project", project: "project-b" },
        undefined,
        undefined,
        ctx,
      );
      assert.equal(crossProjectPrompt.isError, true, "a cross-project prompt must fail before a write is attempted");
      assert.match(crossProjectPrompt.content[0].text, /fresh Pi session/i);

      const passiveEvent = { toolName: "shell", result: "this eligible passive observation must not cross the persisted project boundary" };
      await eventHandlers.get("tool_execution_end")(passiveEvent, ctx);
      await eventHandlers.get("tool_execution_end")(passiveEvent, ctx);

      const sessionProjects = calls
        .filter((call) => call.method === "POST" && call.path === "/sessions")
        .map((call) => call.body.project);
      assert.deepEqual(sessionProjects, ["project-a", "project-a"], "same-project activity renews without registering the identity under project-b");
      assert.equal(
        calls.filter((call) => call.method === "POST" && call.path === "/prompts").length,
        2,
        "only same-project prompts may be captured",
      );
      assert.equal(
        calls.filter((call) => call.method === "POST" && call.path === "/observations/passive").length,
        0,
        "passive capture must be suppressed for the cross-project conflict",
      );
      assert.equal(warnings.length, 1, "repeated passive events must not repeat the same conflict warning");
      assert.match(warnings[0], /fresh Pi session/i);
    });
  } finally {
    globalThis.fetch = originalFetch;
    process.stderr.write = originalStderrWrite;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("fresh Pi state honors a structured session-project conflict without capturing prompts or passive observations", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  const originalStderrWrite = process.stderr.write;
  const calls = [];
  const warnings = [];
  let phase = "project-a";
  process.stderr.write = (chunk) => {
    warnings.push(String(chunk));
    return true;
  };
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: phase }));
    if (path === "/sessions") {
      if (phase === "generic-error") return new Response(JSON.stringify({ error: "registration unavailable" }), { status: 500 });
      if (body.project === "project-b" && body.ownership_mode === "project_owned") {
        return new Response(JSON.stringify({
          error: "session ownership does not match write project",
          code: "session_project_conflict",
          session_id: "resumed-runtime-session",
          owner_project: "project-a",
          requested_project: "project-b",
        }), { status: 409 });
      }
      return new Response(JSON.stringify({ status: "created" }), { status: 201 });
    }
    if (path === "/prompts") return new Response(JSON.stringify({ id: 1 }), { status: 201 });
    if (path === "/observations/passive") return new Response(JSON.stringify({ id: 2 }));
    if (path === "/observations") return new Response(JSON.stringify({ id: 3 }), { status: 201 });
    throw new Error(`unexpected request: ${path}`);
  };

  try {
    const sessionId = "resumed-runtime-session";
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const saved = await registeredTools.get("mem_save_prompt").execute(
        "project-a-save",
        { content: "persisted under project-a", project: "project-a" },
        undefined,
        undefined,
        runtimeContext(sessionId),
      );
      assert.equal(saved.isError, undefined);
    });

    phase = "project-b";
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext(sessionId);
      await eventHandlers.get("before_agent_start")(
        { systemPrompt: "base", prompt: "this prompt must not cross the server-owned session boundary" },
        ctx,
      );
      const passiveEvent = { toolName: "shell", result: "this eligible passive observation must not cross the server-owned session boundary" };
      await eventHandlers.get("tool_execution_end")(passiveEvent, ctx);
      await eventHandlers.get("tool_execution_end")(passiveEvent, ctx);
    });

    const projectBSessionCalls = calls.filter((call) => call.method === "POST" && call.path === "/sessions" && call.body.project === "project-b");
    assert.ok(projectBSessionCalls.length >= 2, "fresh state must rely on the core conflict response, not a stale local cache");
    assert.ok(projectBSessionCalls.every((call) => call.body.ownership_mode === "project_owned"), "Pi registrations must opt into strict project ownership");
    assert.equal(calls.filter((call) => call.method === "POST" && call.path === "/prompts").length, 1, "the resumed project-b process must not capture a prompt");
    assert.equal(calls.filter((call) => call.method === "POST" && call.path === "/observations/passive").length, 0, "the resumed project-b process must not capture passive observations");
    assert.equal(warnings.length, 1, "repeated fresh-state conflict attempts must emit one actionable warning");
    assert.match(warnings[0], /fresh Pi session/i);

    phase = "generic-error";
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const result = await registeredTools.get("mem_save").execute(
        "generic-registration-error",
        { title: "must remain generic", content: "content" },
        undefined,
        undefined,
        runtimeContext("generic-registration-error"),
      );
      assert.equal(result.isError, true);
      assert.match(result.content[0].text, /registration unavailable/);
      assert.doesNotMatch(result.content[0].text, /fresh Pi session/i);
    });
  } finally {
    globalThis.fetch = originalFetch;
    process.stderr.write = originalStderrWrite;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("registered Pi-native mem_search reports native provider transport failure", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async () => {
    throw new Error("connection refused");
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ dir, sandbox }) => {
      const registeredTools = new Map();
      const registerEngram = await importPluginFromSandbox(sandbox);
      registerEngram({
        registerTool(tool) {
          registeredTools.set(tool.name, tool);
        },
        on() {},
      });

      const memSearch = registeredTools.get("mem_search");
      assert.ok(memSearch, "mem_search tool should be registered");

      const result = await memSearch.execute(
        "tool-call-1",
        { query: "state markers", project: "gentle-agent-state" },
        undefined,
        undefined,
        {
          cwd: dir,
          sessionManager: { getSessionId: () => "test-session" },
          ui: { setStatus() {} },
        },
      );

      assert.equal(result.isError, true);
      assert.match(result.content[0].text, /gentle-engram could not reach the Engram HTTP server/);
      assert.match(result.content[0].text, /Pi-native mem_\* tools are registered/);
      assert.match(result.details.error, /native memory provider is not currently responding/);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a malformed 200 Pi response is a tool error rather than a successful null or unavailable transport", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async (url) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "engram" }));
    if (path === "/search") return new Response('{"observations":', { status: 200, headers: { "Content-Type": "application/json" } });
    throw new Error(`unexpected request: ${url}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const result = await registeredTools.get("mem_search").execute(
        "malformed-json",
        { query: "malformed" },
        undefined,
        undefined,
        runtimeContext("malformed-json-session"),
      );

      assert.equal(result.isError, true);
      assert.equal(result.details.data, undefined, "a malformed body must not become successful JSON null");
      assert.doesNotMatch(result.content[0].text, /could not reach the Engram HTTP server/);
      assert.doesNotMatch(result.content[0].text, /native memory provider is not currently responding/);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a 204 Pi response is a successful null without JSON parsing", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async (url) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "engram" }));
    if (path === "/search") return new Response(null, { status: 204 });
    throw new Error(`unexpected request: ${url}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const result = await registeredTools.get("mem_search").execute(
        "no-content",
        { query: "no-content" },
        undefined,
        undefined,
        runtimeContext("no-content-session"),
      );

      assert.notEqual(result.isError, true);
      assert.equal(result.details.data, null);
      assert.equal(result.content[0].text, "No memories found");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a successful empty Pi search presents no memories found", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async (url) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "engram" }));
    if (path === "/search") return new Response(JSON.stringify([]), { headers: { "Content-Type": "application/json" } });
    throw new Error(`unexpected request: ${url}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const result = await registeredTools.get("mem_search").execute(
        "empty-search",
        { query: "not found" },
        undefined,
        undefined,
        runtimeContext("empty-search-session"),
      );

      assert.notEqual(result.isError, true);
      assert.deepEqual(result.details.data, []);
      assert.equal(result.content[0].text, "No memories found");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a timed-out Pi request cannot make a concurrent JSON-null request unavailable", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const nullResponse = deferred();
  const nullRequestStarted = deferred();
  globalThis.fetch = async (url) => {
    const request = new URL(url);
    if (request.pathname === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (request.pathname === "/project/current") return new Response(JSON.stringify({ project: "engram" }));
    if (request.pathname === "/search" && request.searchParams.get("q") === "json-null") {
      nullRequestStarted.resolve();
      return nullResponse.promise;
    }
    if (request.pathname === "/search" && request.searchParams.get("q") === "times-out") {
      nullResponse.resolve(new Response("null", { headers: { "Content-Type": "application/json" } }));
      const timeout = new Error("The operation was aborted due to timeout");
      timeout.name = "TimeoutError";
      throw timeout;
    }
    throw new Error(`unexpected request: ${url}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const memSearch = registeredTools.get("mem_search");
      const ctx = runtimeContext("parallel-transport-session");

      const nullCall = memSearch.execute("json-null", { query: "json-null" }, undefined, undefined, ctx);
      await nullRequestStarted.promise;
      const timeoutCall = memSearch.execute("times-out", { query: "times-out" }, undefined, undefined, ctx);
      const [nullResult, timeoutResult] = await Promise.all([nullCall, timeoutCall]);

      assert.notEqual(nullResult.isError, true, "a completed JSON null response is successful");
      assert.equal(nullResult.details.data, null);
      assert.equal(timeoutResult.isError, true, "the timed-out request remains an error");
      assert.match(timeoutResult.content[0].text, /timed out/);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi forwards its resolved project for review mutations while preserving global review and stats contracts", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "GET", path: "/project/current", body: { project: "override-project", project_source: "process_override" } },
    { method: "GET", path: "/search", body: [] },
    { method: "GET", path: "/doctor", body: { status: "ok" } },
    { method: "GET", path: "/review", body: { observations: [] } },
    { method: "GET", path: "/review", body: { observations: [] } },
    { method: "POST", path: "/review/mark_reviewed", body: { state: "active" } },
    { method: "GET", path: "/stats", body: { total_observations: 2 } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("resolved-project-session");

      await registeredTools.get("mem_search").execute("search", { query: "override" }, undefined, undefined, ctx);
      await registeredTools.get("mem_doctor").execute("doctor", {}, undefined, undefined, ctx);
      await registeredTools.get("mem_review").execute("review", { action: "list" }, undefined, undefined, ctx);
      await registeredTools.get("mem_review").execute("review-filtered", { action: "list", project: "override-project" }, undefined, undefined, ctx);
      await registeredTools.get("mem_review").execute("mark-reviewed", { action: "mark_reviewed", observation_id: 42 }, undefined, undefined, ctx);
      await registeredTools.get("mem_stats").execute("stats", {}, undefined, undefined, ctx);

      const search = calls.find((call) => call.path.startsWith("/search"));
      const doctor = calls.find((call) => call.path.startsWith("/doctor"));
      const reviews = calls.filter((call) => call.method === "GET" && call.path.startsWith("/review"));
      const markReviewed = calls.find((call) => call.method === "POST" && call.path.startsWith("/review/mark_reviewed"));
      const stats = calls.find((call) => call.path.startsWith("/stats"));
      assert.match(search.path, /project=override-project/);
      assert.match(doctor.path, /project=override-project/);
      assert.equal(reviews.length, 2);
      const globalReviewQuery = new URL(`http://test${reviews[0].path}`).searchParams;
      assert.equal(globalReviewQuery.get("all_projects"), "true");
      assert.equal(globalReviewQuery.has("project"), false);
      const filteredReviewQuery = new URL(`http://test${reviews[1].path}`).searchParams;
      assert.equal(filteredReviewQuery.get("project"), "override-project");
      assert.equal(filteredReviewQuery.has("all_projects"), false);
      assert.equal(new URL(`http://test${markReviewed.path}`).searchParams.get("project"), "override-project");
      assert.equal(new URL(`http://test${stats.path}`).searchParams.get("all_projects"), "true");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi review mutations honor an explicit project when automatic detection is ambiguous", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    {
      method: "GET",
      path: "/project/current",
      body: {
        project: "unknown",
        error_hint: "ambiguous project",
        available_projects: ["alpha", "selected-project"],
      },
    },
    { method: "POST", path: "/review/mark_reviewed", body: { state: "active" } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const result = await registeredTools.get("mem_review").execute(
        "mark-reviewed-explicit-project",
        { action: "mark_reviewed", observation_id: 42, project: "selected-project" },
        undefined,
        undefined,
        runtimeContext("ambiguous-project-session"),
      );

      assert.notEqual(result.isError, true, "an explicit project must bypass ambiguous automatic detection");
      const markReviewed = calls.find((call) => call.method === "POST" && call.path.startsWith("/review/mark_reviewed"));
      assert.ok(markReviewed, "mem_review must send the explicit-project mutation request");
      assert.equal(new URL(`http://test${markReviewed.path}`).searchParams.get("project"), "selected-project");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("session-attributed Pi writes bind to acknowledged runtime identity and retry failed registration", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  let registrationAttempts = 0;
  const observationBodies = [];
  const sessionBodies = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    if (path === "/health") return { ok: true, async json() { return { status: "ok" }; } };
    if (path === "/project/current") {
      return { ok: true, async json() { return { project: "pi", project_source: "dir_basename", project_path: ROOT }; } };
    }
    if (path === "/sessions") {
      registrationAttempts += 1;
      sessionBodies.push(JSON.parse(init.body));
      if (registrationAttempts === 1) {
        return { ok: false, status: 503, async json() { return { error: "registration unavailable" }; } };
      }
      return { ok: true, status: 201, async json() { return { status: "created" }; } };
    }
    if (path === "/observations") {
      observationBodies.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { id: observationBodies.length }; } };
    }
    return { ok: true, async json() { return {}; } };
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);

      const memSave = registeredTools.get("mem_save");
      const ctx = runtimeContext("runtime-session");
      const params = { title: "runtime binding", content: "content", session_id: "model-invented" };

      const failed = await memSave.execute("call-1", params, undefined, undefined, ctx);
      assert.equal(failed.isError, true);
      assert.equal(observationBodies.length, 0, "unacknowledged registration must stop the write");

      const succeeded = await memSave.execute("call-2", params, undefined, undefined, ctx);
      assert.equal(succeeded.isError, undefined);
      assert.equal(registrationAttempts, 2, "failed registration must remain retryable");
      assert.equal(sessionBodies[1].id, "runtime-session");
      assert.equal(observationBodies[0].session_id, "runtime-session");
      assert.notEqual(observationBodies[0].session_id, "model-invented");

      await memSave.execute("call-3", params, undefined, undefined, ctx);
      assert.equal(registrationAttempts, 3, "later session-attributed activity should renew the cached runtime session");

      const noRuntime = await memSave.execute(
        "call-4",
        params,
        undefined,
        undefined,
        { ...ctx, sessionManager: { getSessionId: () => undefined } },
      );
      assert.equal(noRuntime.isError, true);
      assert.match(noRuntime.content[0].text, /Pi runtime session ID is unavailable/);
      assert.equal(registrationAttempts, 3, "missing runtime identity must not synthesize or register a session");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("repeated Pi session-attributed writes renew the runtime lease and coalesce concurrent renewal", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const renewalGate = deferred();
  let registrations = 0;
  const observationBodies = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    if (path === "/health") return { ok: true, async json() { return { status: "ok" }; } };
    if (path === "/project/current") return { ok: true, async json() { return { project: "pi", project_source: "dir_basename", project_path: ROOT }; } };
    if (path === "/sessions") {
      registrations += 1;
      if (registrations === 2) await renewalGate.promise;
      return { ok: true, status: 201, async json() { return { status: "created" }; } };
    }
    if (path === "/observations") {
      observationBodies.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { id: observationBodies.length }; } };
    }
    return { ok: true, async json() { return {}; } };
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const memSave = registeredTools.get("mem_save");
      const ctx = runtimeContext("renewing-runtime-session");
      await eventHandlers.get("session_start")({}, ctx);

      const first = memSave.execute("renew-1", { title: "first", content: "one" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      const second = memSave.execute("renew-2", { title: "second", content: "two" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(registrations, 2, "session_start completes before concurrent activity shares one renewal request");

      renewalGate.resolve();
      const [firstResult, secondResult] = await Promise.all([first, second]);
      assert.equal(firstResult.isError, undefined);
      assert.equal(secondResult.isError, undefined);
      assert.equal(observationBodies.length, 2);

      const third = await memSave.execute("renew-3", { title: "third", content: "three" }, undefined, undefined, ctx);
      assert.equal(third.isError, undefined);
      assert.equal(registrations, 3, "later activity must renew before its attributed write");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("parallel first-use writes share one acknowledged registration and keep it cached", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const registrationGate = deferred();
  let registrationAttempts = 0;
  const writeRequests = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    if (path === "/health") return { ok: true, async json() { return { status: "ok" }; } };
    if (path === "/project/current") {
      return { ok: true, async json() { return { project: "pi", project_source: "dir_basename", project_path: ROOT }; } };
    }
    if (path === "/sessions") {
      registrationAttempts += 1;
      await registrationGate.promise;
      return { ok: true, status: 201, async json() { return { status: "created" }; } };
    }
    if (path === "/observations") {
      writeRequests.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { id: writeRequests.length }; } };
    }
    return { ok: true, async json() { return {}; } };
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const memSave = registeredTools.get("mem_save");
      const ctx = runtimeContext("parallel-success-session");
      await eventHandlers.get("session_start")({}, ctx);

      const firstWrite = memSave.execute("parallel-success-1", { title: "first", content: "one" }, undefined, undefined, ctx);
      const secondWrite = memSave.execute("parallel-success-2", { title: "second", content: "two" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(registrationAttempts, 1, "parallel first writes must share one registration request");

      registrationGate.resolve();
      const [firstResult, secondResult] = await Promise.all([firstWrite, secondWrite]);
      assert.equal(firstResult.isError, undefined);
      assert.equal(secondResult.isError, undefined);
      assert.deepEqual(writeRequests.map((request) => request.title).sort(), ["first", "second"]);
      assert.ok(writeRequests.every((request) => request.session_id === "parallel-success-session"));

      await memSave.execute("parallel-success-cached", { title: "cached", content: "three" }, undefined, undefined, ctx);
      assert.equal(registrationAttempts, 2, "later activity must renew the acknowledged registration");
      assert.equal(writeRequests.length, 3);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("concurrent explicit projects cannot share an in-flight effective registration", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const gate = deferred();
  const registrations = [];
  const writes = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions") {
      registrations.push(JSON.parse(init.body));
      if (registrations.length === 1) await gate.promise;
      return new Response(JSON.stringify({ status: "created" }), { status: 201 });
    }
    if (path === "/observations") {
      writes.push(JSON.parse(init.body));
      return new Response(JSON.stringify({ id: writes.length }), { status: 201 });
    }
    return new Response("{}");
  };
  try {
    await withPluginSandbox("engram-pi-project-race-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const save = registeredTools.get("mem_save");
      const ctx = runtimeContext("project-race");
      const first = save.execute("first", { title: "a", content: "a", project: "project-a" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(registrations.length, 1);
      const second = save.execute("second", { title: "b", content: "b", project: "project-b" }, undefined, undefined, ctx);
      gate.resolve();
      const [a, b] = await Promise.all([first, second]);
      assert.equal(a.isError, undefined);
      assert.equal(b.isError, true, "the second project must not inherit the first registration");
      assert.deepEqual(writes.map(({ project }) => project), ["project-a"]);
      assert.ok(registrations.every(({ project }) => project === "project-a"));
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("shared registration failure rejects parallel writes and a later call retries", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const registrationGate = deferred();
  let registrationAttempts = 0;
  let registrationShouldFail = true;
  const writeRequests = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    if (path === "/health") return { ok: true, async json() { return { status: "ok" }; } };
    if (path === "/project/current") {
      return { ok: true, async json() { return { project: "pi", project_source: "dir_basename", project_path: ROOT }; } };
    }
    if (path === "/sessions") {
      registrationAttempts += 1;
      if (registrationShouldFail) {
        await registrationGate.promise;
        return { ok: false, status: 503, async json() { return { error: "registration unavailable" }; } };
      }
      return { ok: true, status: 201, async json() { return { status: "created" }; } };
    }
    if (path === "/observations") {
      writeRequests.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { id: writeRequests.length }; } };
    }
    return { ok: true, async json() { return {}; } };
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const memSave = registeredTools.get("mem_save");
      const ctx = runtimeContext("parallel-failure-session");
      await eventHandlers.get("session_start")({}, ctx);

      const firstWrite = memSave.execute("parallel-failure-1", { title: "first", content: "one" }, undefined, undefined, ctx);
      const secondWrite = memSave.execute("parallel-failure-2", { title: "second", content: "two" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(registrationAttempts, 1, "parallel failed writes must share one registration request");

      registrationGate.resolve();
      const [firstResult, secondResult] = await Promise.all([firstWrite, secondWrite]);
      assert.equal(firstResult.isError, true);
      assert.equal(secondResult.isError, true);
      assert.match(firstResult.content[0].text, /registration unavailable/);
      assert.match(secondResult.content[0].text, /registration unavailable/);
      assert.equal(writeRequests.length, 0, "failed registration must stop every waiting write");

      registrationShouldFail = false;
      const retryResult = await memSave.execute("parallel-failure-retry", { title: "retry", content: "three" }, undefined, undefined, ctx);
      assert.equal(retryResult.isError, undefined);
      assert.equal(registrationAttempts, 2, "a later write must retry failed registration");
      assert.equal(writeRequests.length, 1);

      await memSave.execute("parallel-failure-cached", { title: "cached", content: "four" }, undefined, undefined, ctx);
      assert.equal(registrationAttempts, 3, "later activity must renew the successful retry");
      assert.equal(writeRequests.length, 2);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("an opaque runtime session ID stays byte-identical through registration, compaction, and cleanup", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  // Pi hands out an opaque session ID. Surrounding whitespace is part of that
  // identity, so normalizing it anywhere would split registration from
  // compaction and strand the cache entry that shutdown tries to clear.
  const runtimeSessionId = "  pi-runtime-session-id  ";
  const sessionEndBodies = [];
  const sessionEndMethods = [];
  let failSessionEndRequest = false;
  const sessionBodies = [];
  const observationBodies = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    if (path === "/health") return { ok: true, async json() { return { status: "ok" }; } };
    if (path === "/project/current") {
      return { ok: true, async json() { return { project: "pi", project_source: "dir_basename", project_path: ROOT }; } };
    }
    if (path === "/sessions") {
      sessionBodies.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { status: "created" }; } };
    }
    if (path === "/observations") {
      observationBodies.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { id: observationBodies.length }; } };
    }
    if (path === `/sessions/${encodeURIComponent(runtimeSessionId)}/end`) {
      sessionEndBodies.push(JSON.parse(init.body));
      sessionEndMethods.push(init.method ?? "GET");
      if (failSessionEndRequest) {
        const timeout = new Error("session end timed out");
        timeout.name = "TimeoutError";
        throw timeout;
      }
      return { ok: true, async json() { return { status: "ended" }; } };
    }
    if (path === "/context") return { ok: true, async json() { return { context: "" }; } };
    return { ok: true, async json() { return {}; } };
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const memSave = registeredTools.get("mem_save");
      const ctx = runtimeContext(runtimeSessionId);
      await eventHandlers.get("session_start")({}, ctx);

      const saved = await memSave.execute("exact-1", { title: "first", content: "one" }, undefined, undefined, ctx);
      assert.equal(saved.isError, undefined);
      assert.equal(sessionBodies.length, 1, "the first write registers the runtime session once");
      assert.equal(sessionBodies[0].id, runtimeSessionId, "registration must use the exact runtime identity");
      assert.equal(observationBodies[0].session_id, runtimeSessionId, "the write must use the exact runtime identity");

      await eventHandlers.get("session_compact")({ summary: "compacted work" }, ctx);
      assert.equal(sessionBodies.length, 2, "compaction must renew the cached exact identity before forwarding its summary");
      const compactionSummary = observationBodies.find((body) => body.type === "session_summary");
      assert.ok(compactionSummary, "compaction summary not forwarded");
      assert.equal(compactionSummary.session_id, runtimeSessionId, "compaction must attribute the summary to the exact identity");

      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.deepEqual(sessionEndBodies, [{ summary: "" }], "shutdown must end the exact registered runtime session");
      assert.deepEqual(sessionEndMethods, ["POST"], "shutdown must use the session-end POST contract");
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(sessionEndBodies.length, 1, "repeated shutdown must not end an already discarded session twice");

      await eventHandlers.get("session_start")({}, ctx);
      const afterShutdown = await memSave.execute("exact-2", { title: "second", content: "two" }, undefined, undefined, ctx);
      assert.equal(afterShutdown.isError, undefined);
      assert.equal(sessionBodies.length, 3, "shutdown must clear the cached entry so nothing is left behind");
      assert.equal(sessionBodies[2].id, runtimeSessionId, "re-registration must still use the exact runtime identity");

      const memSessionEnd = registeredTools.get("mem_session_end");
      const explicitlyEnded = await memSessionEnd.execute("explicit-end", { id: runtimeSessionId }, undefined, undefined, ctx);
      assert.equal(explicitlyEnded.isError, undefined, "an explicit session end should succeed");
      assert.equal(sessionEndBodies.length, 2, "the explicit end request must reach Engram once");
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(sessionEndBodies.length, 2, "shutdown must not repeat a successful explicit end");

      await eventHandlers.get("session_start")({}, ctx);
      const afterExplicitEnd = await memSave.execute("exact-3", { title: "third", content: "three" }, undefined, undefined, ctx);
      assert.equal(afterExplicitEnd.isError, undefined);
      assert.equal(sessionBodies.length, 4, "an explicitly ended session must re-register before later writes");

      failSessionEndRequest = true;
      const failedExplicitEnd = await memSessionEnd.execute("failed-explicit-end", { id: runtimeSessionId }, undefined, undefined, ctx);
      assert.equal(failedExplicitEnd.isError, true, "a failed explicit end must surface a tool error");
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(sessionEndBodies.length, 3, "shutdown must not retry an uncertain explicit end");
      await eventHandlers.get("session_start")({}, ctx);
      const afterFailedShutdown = await memSave.execute("exact-4", { title: "fourth", content: "four" }, undefined, undefined, ctx);
      assert.equal(afterFailedShutdown.isError, undefined, "a failed session end must not prevent cleanup");
      assert.equal(sessionBodies.length, 5, "failed shutdown delivery must still clear the registration cache");

      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(sessionEndBodies.length, 4, "a timed-out shutdown must still send only one end request");
      await eventHandlers.get("session_start")({}, ctx);
      const afterTimedOutShutdown = await memSave.execute("exact-5", { title: "fifth", content: "five" }, undefined, undefined, ctx);
      assert.equal(afterTimedOutShutdown.isError, undefined, "a timed-out shutdown must still clear the registration cache");
      assert.equal(sessionBodies.length, 6, "writes after a timed-out shutdown must re-register");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("separate plugin graphs converge after simultaneous ended-session responses", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const entered = deferred();
  const release = deferred();
  const failedRegistration = deferred();
  let originals = 0;
  let lostResponses = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "shared" }));
    if (path === "/sessions" && body.id === "dual") {
      const ordinal = ++originals;
      if (ordinal === 2) entered.resolve();
      await release.promise;
      if (ordinal === 2) await failedRegistration.promise;
      return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    }
    if (path === "/sessions" && lostResponses < 2) {
      if (++lostResponses === 2) failedRegistration.resolve();
      throw new Error("registration acknowledgement lost");
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  const entries = [];
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  const ctx = runtimeContext("dual");
  ctx.sessionManager.getBranch = () => entries;
  try {
    await withPluginSandbox("engram-pi-dual-resume-", async ({ sandbox }) => {
      await withPluginSandbox("engram-pi-dual-resume-peer-", async ({ sandbox: peer }) => {
      const first = await loadPluginHarness(sandbox, append);
      const second = await loadPluginHarness(peer, append);
      const save = (module, project) => module.registeredTools.get("mem_save").execute("dual", { title: "dual", content: "dual", project }, undefined, undefined, ctx);
      const a = save(first, "shared");
      const b = save(second, "shared");
      await waitFor(entered.promise, "original requests stalled");
      release.resolve();
      const results = await waitFor(Promise.all([a, b]), "initial registrations stalled");
      const failedIndex = results.findIndex((result) => result.isError === true);
      assert.notEqual(failedIndex, -1, "lost registration acknowledgement must fail its initial caller");
      assert.equal(results[1 - failedIndex].isError, undefined, "the other initial caller must succeed");
      assert.equal(lostResponses, 2, "the lost caller must exhaust the bounded registration retry");
      const replacements = entries.filter((entry) => entry.customType === "engram-effective-session");
      assert.equal(replacements.length, 1, "only one identity may be reserved");
      const id = replacements[0].data.effectiveID;
      assert.deepEqual([...new Set(calls.filter((call) => call.path === "/sessions" && call.body.id !== "dual").map((call) => call.body.id))], [id]);
      assert.equal(calls.filter((call) => call.path === "/observations").length, 1, "only the initially successful caller may write");
      const retry = await save([first, second][failedIndex], "shared");
      assert.equal(retry.isError, undefined, JSON.stringify(retry));
      assert.equal(calls.filter((call) => call.path === "/observations" && call.body.session_id === id).length, 2,
        "the failed caller must recover its pending identity and write on retry");
      assert.ok(calls.indexOf(calls.find((call) => call.path === "/sessions" && call.body.id === id)) < calls.indexOf(calls.find((call) => call.path === "/observations")));
      const fork = runtimeContext("fork-dual");
      fork.sessionManager.getBranch = () => entries;
      await save(second, "shared");
      await second.registeredTools.get("mem_save").execute("fork", { title: "fork", content: "fork", project: "shared" }, undefined, undefined, fork);
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, "fork-dual");
      });
    });
  } finally {
    release.resolve();
    failedRegistration.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("simultaneous foreign projects cannot each reserve a replacement", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const entered = deferred();
  const release = deferred();
  const calls = [];
  let originals = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "alpha" }));
    if (path === "/sessions" && body.id === "foreign-dual") {
      if (++originals === 2) entered.resolve();
      await release.promise;
      return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  const entries = [];
  const ctx = runtimeContext("foreign-dual");
  ctx.sessionManager.getBranch = () => entries;
  try {
    await withPluginSandbox("engram-pi-foreign-dual-", async ({ sandbox }) => {
      await withPluginSandbox("engram-pi-foreign-dual-peer-", async ({ sandbox: peer }) => {
      const append = (customType, data) => entries.push({ type: "custom", customType, data });
      const first = await loadPluginHarness(sandbox, append);
      const second = await loadPluginHarness(peer, append);
      const save = (module, project) => module.registeredTools.get("mem_save").execute(project, { title: project, content: project, project }, undefined, undefined, ctx);
      const a = save(first, "alpha");
      const b = save(second, "beta");
      await waitFor(entered.promise, "original requests stalled");
      release.resolve();
      const results = await Promise.all([a, b]);
      const writes = calls.filter((call) => call.path === "/observations");
      const replacements = entries.filter((entry) => entry.customType === "engram-effective-session");
      assert.equal(replacements.length, 1);
      assert.equal(writes.length, 1);
      const owner = replacements[0].data.project;
      assert.equal(writes[0].body.project, owner);
      assert.equal(writes[0].body.session_id, replacements[0].data.effectiveID);
      assert.equal(results[owner === "alpha" ? 0 : 1].isError, undefined, "the reservation owner must succeed");
      assert.equal(results[owner === "alpha" ? 1 : 0].isError, true);
      assert.equal(calls.filter((call) => call.path === "/sessions" && call.body.id !== "foreign-dual").length, 1);
      });
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("an ownerless pending replacement cannot be adopted or ended by a foreign project", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeID = "legacy-pending";
  const effectiveID = `${runtimeID}:resume:unowned`;
  const entered = deferred();
  const release = deferred();
  const calls = [];
  const entries = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions" && body.id === runtimeID) {
      entered.resolve();
      await release.promise;
      return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    }
    // The server would accept the unowned replacement for B; only the plugin can reject it.
    return new Response(JSON.stringify({ status: "created" }));
  };
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-ownerless-pending-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, append);
      const save = registeredTools.get("mem_save").execute("foreign", {
        title: "foreign", content: "foreign", project: "project-b",
      }, undefined, undefined, ctx);
      await waitFor(entered.promise, "original registration stalled");
      entries.push({ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID, pending: true } });
      release.resolve();
      const result = await waitFor(save, "foreign registration stalled");
      assert.equal(result.isError, true, "unknown ownership must fail closed");
      assert.equal(calls.filter(({ path, body }) => path === "/sessions" && body.id === effectiveID).length, 0);
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      assert.deepEqual(entries.map(({ customType }) => customType), ["engram-effective-session"],
        "a foreign caller must not reject the existing reservation");
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(calls.filter(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`).length, 0,
        "a foreign caller must not end an unowned replacement");
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("an ownerless pending replacement is not registered on first use", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeID = "legacy-first-use";
  const effectiveID = `${runtimeID}:resume:unowned`;
  const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID, pending: true } }];
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push({ path, body: init.body ? JSON.parse(init.body) : undefined });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    return new Response(JSON.stringify({ status: "created" }));
  };
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-ownerless-first-use-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox, append);
      const result = await registeredTools.get("mem_save").execute("foreign", {
        title: "foreign", content: "foreign", project: "project-b",
      }, undefined, undefined, ctx);
      assert.equal(result.isError, true, "unknown ownership must fail before registration");
      assert.equal(calls.filter(({ path }) => path === "/sessions" || path === "/observations").length, 0);
      assert.equal(entries.length, 1, "the unowned reservation must not be rejected");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("resumed ended conversation registers a distinct persistent identity before writes", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const ended = new Set();
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }), { status: 200 });
    if (path === "/sessions" && ended.has(body.id)) {
      return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    }
    if (path.startsWith("/sessions/") && path.endsWith("/end")) ended.add(decodeURIComponent(path.slice(10, -4)));
    return new Response(JSON.stringify({ status: "created" }), { status: 200 });
  };
  const entries = [];
  const ctx = runtimeContext("resumed");
  ctx.sessionManager.getBranch = () => entries;
  const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-resume-", async ({ sandbox }) => {
      const first = await loadPluginHarness(sandbox, appendEntry);
      await first.registeredTools.get("mem_save").execute("first", { title: "first", content: "first" }, undefined, undefined, ctx);
      await first.eventHandlers.get("session_shutdown")({}, ctx);
      const second = await loadPluginHarness(sandbox, appendEntry);
      await second.eventHandlers.get("session_start")({}, ctx);
      const result = await second.registeredTools.get("mem_save").execute("second", { title: "second", content: "second" }, undefined, undefined, ctx);
      assert.equal(result.isError, undefined, JSON.stringify(result));
      const identities = calls.filter((call) => call.path === "/sessions").map((call) => call.body.id);
      assert.equal(identities[0], "resumed");
      assert.notEqual(identities.at(-1), "resumed");
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, identities.at(-1));
      assert.ok(entries.length);
      const third = await loadPluginHarness(sandbox, appendEntry);
      await third.eventHandlers.get("session_start")({ reason: "reload" }, ctx);
      await third.registeredTools.get("mem_save_prompt").execute("reload", { content: "after reload" }, undefined, undefined, ctx);
      assert.equal(calls.filter((call) => call.path === "/prompts").at(-1).body.session_id, identities.at(-1));
      const fork = runtimeContext("forked");
      fork.sessionManager.getBranch = () => entries;
      await third.registeredTools.get("mem_save").execute("fork", { title: "fork", content: "fork" }, undefined, undefined, fork);
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, "forked");
      await third.eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some((call) => call.path === `/sessions/${encodeURIComponent(identities.at(-1))}/end`));
      const fourth = await loadPluginHarness(sandbox, appendEntry);
      await fourth.eventHandlers.get("session_start")({}, ctx);
      const afterSecondQuit = await fourth.registeredTools.get("mem_save").execute("third-save", { title: "third", content: "third" }, undefined, undefined, ctx);
      assert.equal(afterSecondQuit.isError, undefined, JSON.stringify(afterSecondQuit));
      const latestID = calls.filter((call) => call.path === "/sessions").at(-1).body.id;
      assert.notEqual(latestID, identities.at(-1), "another resume needs a new effective identity");
      assert.notEqual(latestID, "resumed");
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, latestID);
      assert.equal(entries.at(-1).data.effectiveID, latestID);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("ambiguous fresh registration reuses its submitted identity and ends it on shutdown", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const attempts = new Map();
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "ambiguous") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions") {
      const count = (attempts.get(body.id) || 0) + 1;
      attempts.set(body.id, count);
      if (count <= 4) throw new Error("response lost after server created session");
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-ambiguous-resume-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("ambiguous");
      ctx.sessionManager.getBranch = () => entries;
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = () => registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx);
      assert.equal((await save()).isError, true);
      const freshID = calls.filter(({ path }) => path === "/sessions").at(-1).body.id;
      assert.match(freshID, /^ambiguous:resume:/);
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      assert.equal((await save()).isError, true);
      assert.deepEqual([...attempts.keys()], [freshID], "retry must not generate another UUID");
      assert.equal((await save()).isError, undefined);
      assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.session_id, freshID);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(freshID)}/end`));
      assert.equal(calls.some(({ path }) => path === "/sessions/ambiguous/end"), false);
    });
    await withPluginSandbox("engram-pi-ambiguous-end-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("ambiguous");
      ctx.sessionManager.getBranch = () => entries;
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      assert.equal((await registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx)).isError, true);
      const submitted = entries.at(-1)?.data.effectiveID;
      assert.ok(submitted);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(submitted)}/end`), "submitted but unconfirmed session must be ended");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a reserved pending replacement cannot be claimed by another project after pre-dispatch failure", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  let failedReplacementAttempts = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions" && body.id === "reserved") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions" && failedReplacementAttempts < 2) {
      failedReplacementAttempts++;
      const failure = new Error("request did not reach server");
      failure.code = "ENETUNREACH";
      throw failure;
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-reserved-project-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("reserved");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, appendEntry);
      const save = (project) => registeredTools.get("mem_save").execute(project, { title: project, content: project, project }, undefined, undefined, ctx);
      assert.equal((await save("project-a")).isError, true);
      const effectiveID = entries.at(-1).data.effectiveID;
      const registrationCount = calls.filter(({ path }) => path === "/sessions").length;
      assert.equal((await save("project-b")).isError, true);
      assert.equal(calls.filter(({ path }) => path === "/sessions").length, registrationCount, "B must not POST the reserved ID");
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      assert.equal((await save("project-a")).isError, undefined);
      assert.deepEqual(calls.filter(({ path }) => path === "/observations").map(({ body }) => body.session_id), [effectiveID]);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("confirmed replacement end clears pending state across repeated shutdown and reload", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "repeat-end") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-repeat-end-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("repeat-end");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, appendEntry);
      assert.equal((await registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx)).isError, undefined);
      const effectiveID = entries.at(-1).data.effectiveID;
      await eventHandlers.get("session_shutdown")({}, ctx);
      await eventHandlers.get("session_shutdown")({}, ctx);
      await withPluginSandbox("engram-pi-repeat-end-next-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, appendEntry);
        await next.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.equal(calls.filter(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`).length, 1);
      assert.equal(entries.at(-1).data.pending, false);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("an unconfirmed replacement end retains pending state for a later shutdown", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "uncertain-end") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path.endsWith("/end") && calls.filter(({ path: requested }) => requested.endsWith("/end")).length === 1) throw new Error("end response lost");
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-uncertain-end-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("uncertain-end");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, appendEntry);
      assert.equal((await registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx)).isError, undefined);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(entries.at(-1).data.pending, true);
      await withPluginSandbox("engram-pi-uncertain-end-next-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, appendEntry);
        await next.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 2);
      assert.equal(entries.at(-1).data.pending, false);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a concurrent foreign-project caller cannot revoke the pending owner's shutdown", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const started = deferred();
  const gate = deferred();
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions" && body.id === "owner-race") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions" && body.project === "project-a") {
      started.resolve();
      await gate.promise;
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-owner-race-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("owner-race");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools } = await loadPluginHarness(sandbox, appendEntry);
      const save = registeredTools.get("mem_save");
      const owner = save.execute("a", { title: "a", content: "a", project: "project-a" }, undefined, undefined, ctx);
      await started.promise;
      const effectiveID = entries.at(-1).data.effectiveID;
      const foreign = await save.execute("b", { title: "b", content: "b", project: "project-b" }, undefined, undefined, ctx);
      assert.equal(foreign.isError, true);
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      gate.resolve();
      assert.equal((await owner).isError, undefined);
      assert.deepEqual(calls.filter(({ path }) => path === "/observations").map(({ body }) => body.session_id), [effectiveID]);
      await withPluginSandbox("engram-pi-owner-race-next-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, appendEntry);
        await next.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
      assert.ok(calls.filter(({ path }) => path === "/sessions").every(({ body }) => body.project === "project-a" || body.id === "owner-race"));
    });
  } finally {
    gate.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a later ownership conflict on an uncertain replacement revokes shutdown end across reload", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  let replacementAttempts = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "late-conflict") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions") {
      replacementAttempts++;
      if (replacementAttempts <= 2) throw new Error("registration response lost");
      return new Response(JSON.stringify({ code: "session_project_conflict", session_id: body.id, requested_project: body.project, owner_project: "other-project" }), { status: 409 });
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-late-conflict-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("late-conflict");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, appendEntry);
      const save = () => registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx);
      assert.equal((await save()).isError, true);
      const submitted = entries.at(-1).data.effectiveID;
      assert.equal((await save()).isError, true);
      assert.deepEqual([...new Set(calls.filter(({ path, body }) => path === "/sessions" && body.id !== "late-conflict").map(({ body }) => body.id))], [submitted]);
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      await eventHandlers.get("session_shutdown")({}, ctx);
      await withPluginSandbox("engram-pi-late-conflict-next-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, appendEntry);
        await next.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 0, "the conflicted replacement cannot be ended by either module");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a rejected replacement ownership never authorizes shutdown end", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "conflicted") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions") return new Response(JSON.stringify({ code: "session_project_conflict", session_id: body.id, requested_project: body.project, owner_project: "other-project" }), { status: 409 });
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-conflict-end-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("conflicted");
      ctx.sessionManager.getBranch = () => entries;
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const result = await registeredTools.get("mem_save").execute("conflict", { title: "conflict", content: "conflict" }, undefined, undefined, ctx);
      assert.equal(result.isError, true);
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      await eventHandlers.get("session_shutdown")({}, ctx);
      await withPluginSandbox("engram-pi-conflict-end-next-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
        await next.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 0, "ownership conflict forbids end across reload");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a persisted uncertain replacement ends after extension reload without another registration", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "uncertain-reload") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions") throw new Error("response lost after registration");
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-uncertain-reload-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("uncertain-reload");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const first = await loadPluginHarness(sandbox, appendEntry);
      assert.equal((await first.registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx)).isError, true);
      const submitted = entries.at(-1)?.data.effectiveID;
      assert.ok(submitted);
      await withPluginSandbox("engram-pi-uncertain-reload-next-", async ({ sandbox: nextSandbox }) => {
        const second = await loadPluginHarness(nextSandbox, appendEntry);
        await second.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(submitted)}/end`));
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("shutdown waits for resumed registration and rejects attributed writes", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const gate = deferred();
  const started = deferred();
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions" && body.id === "overlap") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions" && body.id.startsWith("overlap:resume:")) {
      started.resolve();
      await gate.promise;
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  const entries = [];
  const ctx = runtimeContext("overlap");
  ctx.sessionManager.getBranch = () => entries;
  try {
    await withPluginSandbox("engram-pi-resume-shutdown-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = registeredTools.get("mem_save");
      const pending = save.execute("pending", { title: "pending", content: "pending" }, undefined, undefined, ctx);
      await started.promise;
      const joined = registeredTools.get("mem_save_prompt").execute("joined", { content: "joined" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(calls.filter(({ path }) => path === "/sessions").length, 2, "both callers share the pending resume registration");
      const shutdown = eventHandlers.get("session_shutdown")({}, ctx);
      gate.resolve();
      const [first, second] = await Promise.all([pending, joined, shutdown]);
      assert.equal(first.isError, true);
      assert.equal(second.isError, true);
      const effectiveID = entries.at(-1)?.data.effectiveID;
      assert.ok(effectiveID);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      await save.execute("later", { title: "later", content: "later" }, undefined, undefined, ctx);
      await registeredTools.get("mem_save_prompt").execute("prompt", { content: "later" }, undefined, undefined, ctx);
      await registeredTools.get("mem_capture_passive").execute("passive", { content: "later" }, undefined, undefined, ctx);
      assert.equal(calls.filter(({ path }) => ["/observations", "/prompts", "/observations/passive"].includes(path)).length, 0);
    });
  } finally {
    gate.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("ended session fails closed when Pi entry persistence is unavailable", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    return new Response(JSON.stringify({ status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-no-entry-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("ended-without-entry");
      ctx.sessionManager.getBranch = () => [];
      const result = await registeredTools.get("mem_save").execute("no-entry", { title: "blocked", content: "blocked" }, undefined, undefined, ctx);
      assert.equal(result.isError, true);
      assert.equal(result.details.http_status, 409);
      assert.equal(calls.filter((path) => path === "/sessions").length, 1);
      assert.equal(calls.includes("/observations"), false);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi session shutdown serializes end delivery and waits for registration", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const endCalls = [];
  const endStarted = deferred();
  const endGate = deferred();
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") return new Response(JSON.stringify({ status: "created" }));
    if (path.endsWith("/end")) {
      endCalls.push({ method: init.method ?? "GET", body: JSON.parse(init.body) });
      endStarted.resolve();
      await endGate.promise;
      return new Response(JSON.stringify({ status: "ended" }));
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1 }));
    throw new Error(`unexpected request: ${path}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("concurrent-shutdown-session");
      await registeredTools.get("mem_save").execute("register", { title: "one", content: "one" }, undefined, undefined, ctx);
      const firstShutdown = eventHandlers.get("session_shutdown")({}, ctx);
      await endStarted.promise;
      const explicitEnd = registeredTools.get("mem_session_end").execute("concurrent-explicit-end", { id: "concurrent-shutdown-session" }, undefined, undefined, ctx);
      const secondShutdown = eventHandlers.get("session_shutdown")({}, ctx);
      endGate.resolve();
      const [, explicitEndResult] = await Promise.all([firstShutdown, explicitEnd, secondShutdown]);
      assert.equal(explicitEndResult.isError, undefined, "an explicit end must join shutdown delivery");
      assert.deepEqual(endCalls, [{ method: "POST", body: { summary: "" } }], "concurrent shutdown and explicit end must send one POST");

      await eventHandlers.get("session_start")({}, runtimeContext(undefined));
      await eventHandlers.get("session_shutdown")({}, runtimeContext(undefined));
      assert.equal(endCalls.length, 1, "missing runtime identity must not send an end request");
    });

    const registrationGate = deferred();
    const registrationStarted = deferred();
    const raceEndCalls = [];
    globalThis.fetch = async (url, init = {}) => {
      const path = new URL(url).pathname;
      if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
      if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
      if (path === "/sessions") {
        registrationStarted.resolve();
        await registrationGate.promise;
        return new Response(JSON.stringify({ status: "created" }));
      }
      if (path.endsWith("/end")) {
        raceEndCalls.push({ method: init.method ?? "GET", body: JSON.parse(init.body) });
        return new Response(JSON.stringify({ status: "ended" }));
      }
      if (path === "/observations") return new Response(JSON.stringify({ id: 1 }));
      throw new Error(`unexpected request: ${path}`);
    };
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("registration-race-session");
      const write = registeredTools.get("mem_save").execute("register-race", { title: "one", content: "one" }, undefined, undefined, ctx);
      await registrationStarted.promise;
      const shutdown = eventHandlers.get("session_shutdown")({}, ctx);
      registrationGate.resolve();
      await Promise.all([write, shutdown]);
      assert.deepEqual(raceEndCalls, [{ method: "POST", body: { summary: "" } }], "shutdown must end a registration that was already in flight");
    });

    const uncertainRegistration = deferred();
    const uncertainRegistrationStarted = deferred();
    const uncertainEndCalls = [];
    globalThis.fetch = async (url, init = {}) => {
      const path = new URL(url).pathname;
      if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
      if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
      if (path === "/sessions") {
        uncertainRegistrationStarted.resolve();
        await uncertainRegistration.promise;
        const timeout = new Error("registration timed out");
        timeout.name = "TimeoutError";
        throw timeout;
      }
      if (path.endsWith("/end")) {
        uncertainEndCalls.push({ method: init.method ?? "GET", body: JSON.parse(init.body) });
        return new Response(JSON.stringify({ status: "ended" }));
      }
      throw new Error(`unexpected request: ${path}`);
    };
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("uncertain-registration-session");
      const write = registeredTools.get("mem_save").execute("register-uncertain", { title: "one", content: "one" }, undefined, undefined, ctx);
      await uncertainRegistrationStarted.promise;
      const explicitEnd = registeredTools.get("mem_session_end").execute("end-uncertain", { id: "uncertain-registration-session" }, undefined, undefined, ctx);
      uncertainRegistration.resolve();
      const [writeResult, endResult] = await Promise.all([write, explicitEnd]);
      assert.equal(writeResult.isError, true, "the registration outcome is uncertain");
      assert.equal(endResult.isError, undefined, "explicit end must still attempt delivery");
      assert.deepEqual(uncertainEndCalls, [{ method: "POST", body: { summary: "" } }]);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("shutdown closes a paused hook until the same runtime session starts again", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const projectLookupStarted = deferred();
  const projectLookup = deferred();
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push({ method: init.method ?? "GET", path });
    if (path === "/project/current") {
      projectLookupStarted.resolve();
      return projectLookup.promise;
    }
    if (path === "/sessions") return new Response(JSON.stringify({ status: "created" }));
    if (path === "/prompts") return new Response(JSON.stringify({ id: 1 }));
    throw new Error(`unexpected request: ${path}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("closing-race-session");
      const pendingHook = eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "a prompt that should not be captured" }, ctx);
      await projectLookupStarted.promise;
      await eventHandlers.get("session_shutdown")({}, ctx);
      projectLookup.resolve(new Response(JSON.stringify({ project: "pi" })));
      await pendingHook;
      assert.equal(calls.filter((call) => call.method === "POST").length, 0, "a hook paused before registration must not write after shutdown");

      await eventHandlers.get("session_start")({}, ctx);
      await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "a prompt that may be captured now" }, ctx);
      assert.deepEqual(calls.filter((call) => call.method === "POST").map((call) => call.path), ["/sessions", "/prompts"], "same-ID session_start must reopen registration");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("shutdown stops passive capture after registration", async () => {
      const originalFetch = globalThis.fetch;
      const originalUrl = process.env.ENGRAM_URL;
      process.env.ENGRAM_URL = "http://127.0.0.1:17437";
      const calls = [];
      let shutdown;
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        calls.push({ method: init.method ?? "GET", path });
        if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
        if (path === "/sessions") return new Response(JSON.stringify({ status: "created" }));
        if (path.endsWith("/end")) return new Response(JSON.stringify({ status: "ended" }));
        if (path === "/observations/passive") return new Response(JSON.stringify({ id: 1 }));
        throw new Error(`unexpected request: ${path}`);
      };

      try {
        await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
          const { eventHandlers } = await loadPluginHarness(sandbox);
          const ctx = runtimeContext("post-registration-shutdown");
          let resultReads = 0;
          await eventHandlers.get("tool_execution_end")({
            toolName: "shell",
            get result() {
              resultReads += 1;
              if (resultReads === 1) shutdown = eventHandlers.get("session_shutdown")({}, ctx);
              return "this eligible tool result is long enough for passive capture";
            },
          }, ctx);
          await shutdown;
          assert.equal(calls.filter((call) => call.path === "/observations/passive").length, 0, "shutdown after registration must stop passive capture");
        });
      } finally {
        globalThis.fetch = originalFetch;
        if (originalUrl === undefined) delete process.env.ENGRAM_URL;
        else process.env.ENGRAM_URL = originalUrl;
      }
    });

    test("failed session registration stops prompt capture", async () => {
      const originalFetch = globalThis.fetch;
      const originalUrl = process.env.ENGRAM_URL;
      process.env.ENGRAM_URL = "http://127.0.0.1:17437";
      const calls = [];
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        calls.push({ method: init.method ?? "GET", path });
        if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
        if (path === "/sessions") return new Response(JSON.stringify({ error: "registration unavailable" }), { status: 503 });
        if (path === "/prompts") return new Response(JSON.stringify({ id: 1 }));
        throw new Error(`unexpected request: ${path}`);
      };

      try {
        await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
          const { eventHandlers } = await loadPluginHarness(sandbox);
          await eventHandlers.get("before_agent_start")(
            { systemPrompt: "base", prompt: "a prompt that must not follow failed registration" },
            runtimeContext("failed-registration-session"),
          );
          assert.deepEqual(calls.filter((call) => call.method === "POST").map((call) => call.path), ["/sessions"]);
        });
      } finally {
        globalThis.fetch = originalFetch;
        if (originalUrl === undefined) delete process.env.ENGRAM_URL;
        else process.env.ENGRAM_URL = originalUrl;
      }
    });

    test("compaction recovery notice stays scoped to the exact cached runtime session", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/project/current", body: { project: "pi" } },
    { method: "POST", path: "/sessions", body: { status: "created" } },
    { method: "POST", path: "/observations", body: { id: 1 } },
    { method: "GET", path: "/context/compaction", body: { context: "exact-session context" } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const runtimeSessionId = "  exact cached Pi identity  ";
      await eventHandlers.get("session_start")({}, runtimeContext(runtimeSessionId));
      await eventHandlers.get("session_compact")(
        { compactionEntry: { summary: "current shape" }, summary: "conflicting legacy shape" },
        { cwd: ROOT, sessionManager: { getSessionId: () => { throw new Error("stale context accessed"); } } },
      );
      const otherTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, runtimeContext("other-session"));
      const nextTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, runtimeContext(runtimeSessionId));
      const consumedTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, runtimeContext(runtimeSessionId));

      const registration = calls.find((call) => call.method === "POST" && call.path === "/sessions");
      const archive = calls.find((call) => call.method === "POST" && call.path === "/observations");
      const recovery = calls.find((call) => call.method === "GET" && call.path.startsWith("/context/compaction"));
      assert.ok(registration, "compaction must acknowledge registration first");
      assert.ok(archive, "compaction must archive its summary");
      assert.ok(recovery, "compaction must load exact-session recovery guidance");
      assert.ok(calls.indexOf(registration) < calls.indexOf(archive));
      assert.equal(registration.body.id, runtimeSessionId);
      assert.equal(archive.body.session_id, runtimeSessionId);
      assert.equal(archive.body.content, "current shape");
      assert.equal(new URL(`http://test${recovery.path}`).searchParams.get("session_id"), runtimeSessionId);
      assert.doesNotMatch(otherTurn.systemPrompt, /exact-session context/);
      assert.doesNotMatch(otherTurn.systemPrompt, /already saved/);
      assert.match(nextTurn.systemPrompt, /already saved/);
      assert.doesNotMatch(nextTurn.systemPrompt, /FIRST ACTION REQUIRED/);
      assert.doesNotMatch(consumedTurn.systemPrompt, /already saved/);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("compaction timeout does not repeat its archive and queues verification guidance", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const request = new URL(url);
    calls.push({ method: init.method ?? "GET", path: request.pathname + request.search });
    if (request.pathname === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (request.pathname === "/sessions") return new Response(JSON.stringify({ status: "created" }));
    if (request.pathname === "/observations") {
      const timeout = new Error("The operation was aborted due to timeout");
      timeout.name = "TimeoutError";
      throw timeout;
    }
    if (request.pathname === "/context/compaction") return new Response(JSON.stringify({ context: "recovery context" }));
    throw new Error(`unexpected request: ${request.pathname}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("timeout-session");
      await eventHandlers.get("session_start")({}, ctx);
      await eventHandlers.get("session_compact")({ compactionEntry: { summary: "summary" } }, ctx);
      const nextTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, ctx);

      assert.equal(calls.filter((call) => call.method === "POST" && call.path === "/observations").length, 1);
      assert.match(nextTurn.systemPrompt, /could not confirm/);
      assert.match(nextTurn.systemPrompt, /verify/i);
      assert.match(nextTurn.systemPrompt, /Do NOT retry/);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("ambiguous runtime identity history permanently blocks compaction writes", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const registrationGate = deferred();
  globalThis.fetch = async (url, init = {}) => {
    const request = new URL(url);
    calls.push({ method: init.method ?? "GET", path: request.pathname + request.search });
    if (request.pathname === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (request.pathname === "/sessions") {
      await registrationGate.promise;
      return new Response(JSON.stringify({ status: "created" }));
    }
    if (request.pathname === "/observations" || request.pathname === "/context/compaction") return new Response(JSON.stringify({ context: "must not load" }));
    throw new Error(`unexpected request: ${request.pathname}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const a = runtimeContext("A");
      const b = runtimeContext("B");
      await eventHandlers.get("session_start")({}, a);
      const compaction = eventHandlers.get("session_compact")({ summary: "summary" });
      await new Promise((resolve) => setImmediate(resolve));
      await eventHandlers.get("session_start")({}, b);
      registrationGate.resolve();
      await compaction;
      await eventHandlers.get("session_shutdown")({}, a);
      await eventHandlers.get("session_compact")({ summary: "delayed" });
      await eventHandlers.get("session_shutdown")({}, b);
      await eventHandlers.get("session_start")({}, a);
      await eventHandlers.get("session_compact")({ summary: "repeated" });
      const nextTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, a);
      assert.match(nextTurn.systemPrompt, /did not archive/);
      assert.equal(calls.filter((call) => call.method === "POST" && call.path === "/sessions").length, 1);
      assert.equal(calls.filter((call) => call.path.startsWith("/observations")).length, 0);
      assert.equal(calls.filter((call) => call.path.startsWith("/context/compaction")).length, 0);
    });
    for (const invalidIdentity of [undefined, "   ", () => { throw new Error("identity unavailable"); }]) await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const a = runtimeContext("A");
      await eventHandlers.get("session_start")({}, a);
      await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, runtimeContext(invalidIdentity));
      await eventHandlers.get("session_compact")({ summary: "unavailable identity" });
      const nextTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, a);
      assert.match(nextTurn.systemPrompt, /did not archive/);
      assert.equal(calls.filter((call) => call.path.startsWith("/observations")).length, 0);
      assert.equal(calls.filter((call) => call.path.startsWith("/context/compaction")).length, 0);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("pending compaction recovery is injected even when startup remains unavailable", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  const originalBin = process.env.ENGRAM_BIN;
  delete process.env.ENGRAM_URL;
  process.env.ENGRAM_BIN = "engram-pi-compaction-test-missing-binary";
  globalThis.fetch = async () => { throw new Error("connection refused"); };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("startup-failure-session");
      await eventHandlers.get("session_start")({}, ctx);
      await eventHandlers.get("session_compact")({ compactionEntry: { summary: "summary" } }, ctx);
      const nextTurn = await eventHandlers.get("before_agent_start")({ systemPrompt: "base prompt" }, ctx);

      assert.match(nextTurn.systemPrompt, /did not archive/);
      assert.match(nextTurn.systemPrompt, /verify/i);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    if (originalBin === undefined) delete process.env.ENGRAM_BIN;
    else process.env.ENGRAM_BIN = originalBin;
  }
});

test("registered Pi-native mem_list_projects enumerates every known project without scoping", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "GET", path: "/projects", body: { projects: [{ name: "engram", observation_count: 12 }], count: 1 } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("list-projects-session");

      const result = await registeredTools.get("mem_list_projects").execute("list-projects", {}, undefined, undefined, ctx);

      const listing = calls.find((call) => call.method === "GET" && call.path.startsWith("/projects"));
      assert.ok(listing, "mem_list_projects must call GET /projects");
      assert.equal(new URL(`http://test${listing.path}`).search, "");
      assert.ok(JSON.stringify(result).includes("engram"));
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("registered Pi-native mem_pin and mem_unpin target the observation pin routes", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "PUT", path: "/observations/42/pin", body: { id: 42, pinned: true } },
    { method: "DELETE", path: "/observations/42/pin", body: { id: 42, pinned: false } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("pin-session");

      await registeredTools.get("mem_pin").execute("pin", { id: 42 }, undefined, undefined, ctx);
      await registeredTools.get("mem_unpin").execute("unpin", { id: 42 }, undefined, undefined, ctx);

      const pin = calls.find((call) => call.method === "PUT" && call.path.startsWith("/observations/42/pin"));
      const unpin = calls.find((call) => call.method === "DELETE" && call.path.startsWith("/observations/42/pin"));
      assert.ok(pin, "mem_pin must call PUT /observations/{id}/pin");
      assert.ok(unpin, "mem_unpin must call DELETE /observations/{id}/pin");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

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

// Each sandbox lives at its own path, so every call already loads a fresh module graph and no
// cache-busting query string is needed.
async function loadPluginHarness(sandbox) {
  const registeredTools = new Map();
  const eventHandlers = new Map();
  const registerEngram = await importPluginFromSandbox(sandbox);
  registerEngram({
    registerTool(tool) {
      registeredTools.set(tool.name, tool);
    },
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

test("Pi session shutdown with reload reason does not end the session or block subsequent writes", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeSessionId = "reloadable-runtime-session";
  const endedSessions = new Set();
  const sessionCalls = [];
  const observationCalls = [];

  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") {
      return new Response(JSON.stringify({ project: "pi", project_source: "dir_basename", project_path: ROOT }));
    }
    if (path === "/sessions") {
      const body = JSON.parse(init.body);
      sessionCalls.push(body);
      if (endedSessions.has(body.id)) {
        return new Response(JSON.stringify({ error: "session has already ended", code: "session_already_ended" }), { status: 409 });
      }
      return new Response(JSON.stringify({ status: "created", id: body.id }), { status: 201 });
    }
    if (path === `/sessions/${encodeURIComponent(runtimeSessionId)}/end`) {
      endedSessions.add(runtimeSessionId);
      return new Response(JSON.stringify({ status: "ended", id: runtimeSessionId }));
    }
    if (path === "/observations") {
      const body = JSON.parse(init.body);
      observationCalls.push(body);
      if (endedSessions.has(body.session_id)) {
        return new Response(JSON.stringify({ error: "session has already ended", code: "session_already_ended" }), { status: 409 });
      }
      return new Response(JSON.stringify({ id: observationCalls.length, status: "saved" }), { status: 201 });
    }
    throw new Error(`unexpected request: ${path}`);
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const memSave = registeredTools.get("mem_save");
      const ctx = runtimeContext(runtimeSessionId);

      // 1. Start session and perform initial write
      await eventHandlers.get("session_start")({}, ctx);
      const initialSave = await memSave.execute("initial", { title: "before reload", content: "works before reload" }, undefined, undefined, ctx);
      assert.equal(initialSave.isError, undefined, "initial write should succeed");
      assert.equal(endedSessions.size, 0, "session should be active");

      // 2. Pi performs /reload: emits session_shutdown with reason "reload"
      await eventHandlers.get("session_shutdown")({ type: "session_shutdown", reason: "reload" }, ctx);
      assert.equal(endedSessions.size, 0, "reload shutdown must NOT end the session on the server");

      // 3. Extension is restarted in the same session: emits session_start with same session ID
      await eventHandlers.get("session_start")({ type: "session_start", reason: "reload" }, ctx);

      // 4. Perform write after reload in the same session
      const postReloadSave = await memSave.execute("after-reload", { title: "after reload", content: "works after reload" }, undefined, undefined, ctx);
      assert.equal(postReloadSave.isError, undefined, "write after reload must succeed and not be rejected as ended");
      assert.equal(endedSessions.size, 0, "session remains active across reload");

      // 5. Terminal shutdown (quit / no reload reason) DOES end the session
      await eventHandlers.get("session_shutdown")({ type: "session_shutdown", reason: "quit" }, ctx);
      assert.equal(endedSessions.has(runtimeSessionId), true, "terminal shutdown must end the session on the server");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

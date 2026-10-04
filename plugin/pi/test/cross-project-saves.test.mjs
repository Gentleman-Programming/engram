import assert from "node:assert/strict";
import { test } from "node:test";
import { importPluginFromSandbox, PLUGIN_ROOT, withPluginSandbox } from "./plugin-sandbox.mjs";

// A Pi runtime session is owned by exactly one Engram project. Explicitly targeted writes to
// another project must go through a derived, project-owned satellite session instead of
// re-registering (and conflicting) the runtime session under the foreign project.

const ROOT = PLUGIN_ROOT;

function deferred() {
  let resolve;
  const promise = new Promise((settle) => { resolve = settle; });
  return { promise, resolve };
}

async function loadPluginHarness(sandbox, appendEntry) {
  const registeredTools = new Map();
  const eventHandlers = new Map();
  const registerEngram = await importPluginFromSandbox(sandbox);
  registerEngram({
    registerTool(tool) { registeredTools.set(tool.name, tool); },
    appendEntry,
    on(event, handler) { eventHandlers.set(event, handler); },
  });
  return { registeredTools, eventHandlers };
}

function runtimeContext(sessionId) {
  return { cwd: ROOT, sessionManager: { getSessionId: () => sessionId }, ui: { setStatus() {} } };
}

// Fake Engram server: the runtime cwd resolves to project-a; other cwd values resolve through
// `projectsByCwd`. Every request is recorded so the wire contract can be asserted.
function fakeEngram({ projectsByCwd = {}, sessionGate, sessionAck, health = { status: "ok", capabilities: { isolated_session_registration: true } } } = {}) {
  const calls = [];
  const fetchStub = async (url, init = {}) => {
    const request = new URL(url);
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method, path: request.pathname, query: request.searchParams, body });
    const json = (payload, status = 200) => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
    if (request.pathname === "/health") return json(health);
    if (request.pathname === "/project/current") {
      const cwd = request.searchParams.get("cwd");
      return json(projectsByCwd[cwd] ?? { project: "project-a", project_path: ROOT });
    }
    if (request.pathname === "/sessions" && method === "POST") {
      if (sessionGate && body.id !== "runtime-a") await sessionGate.promise;
      return json(sessionAck?.(body) ?? { id: body.id, status: "created" }, 201);
    }
    if (request.pathname.startsWith("/sessions/") && request.pathname.endsWith("/end")) return json({ status: "ended" });
    if (request.pathname === "/observations") return json({ id: calls.length }, 201);
    if (request.pathname === "/prompts") return json({ id: calls.length }, 201);
    return json({});
  };
  const sessionPosts = () => calls.filter(({ method, path }) => method === "POST" && path === "/sessions").map(({ body }) => body);
  const writes = (path) => calls.filter(({ method, path: candidate }) => method === "POST" && candidate === path).map(({ body }) => body);
  return { calls, fetchStub, sessionPosts, writes };
}

async function withFakeEngram(server, body) {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = server.fetchStub;
  try {
    await withPluginSandbox("engram-pi-cross-project-", async ({ sandbox }) => body(await loadPluginHarness(sandbox)));
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
}

test("an explicit foreign project saves through a project-owned satellite session", async () => {
  const server = fakeEngram();
  await withFakeEngram(server, async ({ registeredTools }) => {
    const save = registeredTools.get("mem_save");
    const ctx = runtimeContext("runtime-a");
    const own = await save.execute("own", { title: "own", content: "owned project" }, undefined, undefined, ctx);
    assert.equal(own.isError, undefined, own.content?.[0]?.text);

    const foreign = await save.execute("foreign", { title: "foreign", content: "explicit target", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(foreign.isError, undefined, foreign.content?.[0]?.text);

    const registrations = server.sessionPosts();
    assert.deepEqual(registrations.map(({ id, project }) => [id, project]), [
      ["runtime-a", "project-a"],
      ["runtime-a@project-b", "project-b"],
    ]);
    const satellite = registrations[1];
    assert.equal(satellite.ownership_mode, "project_owned", "satellites keep strict single-project ownership");
    assert.equal(satellite.isolated, true, "server must atomically enforce satellite isolation");
    const satellitePost = server.calls.findIndex(({ body }) => body?.id === satellite.id);
    assert.ok(server.calls.slice(0, satellitePost).some(({ path }) => path === "/health"));
    assert.ok(!satellite.directory?.trim(), "project-targeted satellites must not become implicit runtime candidates");
    assert.ok(!registrations.some(({ id, project }) => id === "runtime-a" && project === "project-b"),
      "the runtime session must never be re-registered under the foreign project");

    const observations = server.writes("/observations");
    assert.deepEqual(observations.map(({ session_id, project }) => [session_id, project]), [
      ["runtime-a", "project-a"],
      ["runtime-a@project-b", "project-b"],
    ]);
  });
});

for (const suffix of ["0", "0002", "999999999999999999999999999999999999"]) {
  test(`satellite accepts core numeric continuation ${suffix}`, async () => {
    const server = fakeEngram({ sessionAck: (body) => ({ id: `${body.id}:resume:${suffix}`, status: "created" }) });
    await withFakeEngram(server, async ({ registeredTools }) => {
      const result = await registeredTools.get("mem_save").execute("foreign", { title: "t", content: "c", project: "project-b" }, undefined, undefined, runtimeContext("runtime-a"));
      assert.equal(result.isError, undefined, JSON.stringify(result));
      assert.equal(server.writes("/observations")[0].session_id, `runtime-a@project-b:resume:${suffix}`);
    });
  });
}

for (const suffix of ["", "abc", "2:extra", "2x", "-2"]) {
  test(`malformed satellite acknowledgement rejects suffix ${JSON.stringify(suffix)}`, async () => {
    const server = fakeEngram({ sessionAck: (body) => ({ id: `${body.id}:resume:${suffix}`, status: "created" }) });
    await withFakeEngram(server, async ({ registeredTools, eventHandlers }) => {
      const ctx = runtimeContext("runtime-a");
      const result = await registeredTools.get("mem_save").execute("foreign", { title: "t", content: "c", project: "project-b" }, undefined, undefined, ctx);
      assert.equal(result.isError, true);
      assert.match(result.content[0].text, /invalid acknowledgement/);
      assert.equal(server.writes("/observations").length, 0);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(server.calls.filter(({ path }) => path.includes("runtime-a@project-b") && path.endsWith("/end")).length, 0);
    });
  });
}

for (const health of [{ status: "ok" }, { capabilities: null }, { capabilities: [] },
  { capabilities: { isolated_session_registration: false } },
  { capabilities: { isolated_session_registration: "true" } },
  { capabilities: { isolated_session_registration: 1 } }]) {
  test(`unsupported isolation capability fails closed: ${JSON.stringify(health)}`, async () => {
    const server = fakeEngram({ health });
    await withFakeEngram(server, async ({ registeredTools }) => {
      for (const tool of ["mem_save", "mem_save_prompt", "mem_session_summary"]) {
        const result = await registeredTools.get(tool).execute("foreign", { title: "t", content: "c", project: "project-b" }, undefined, undefined, runtimeContext("runtime-a"));
        assert.equal(result.isError, true);
        assert.match(result.content[0].text, /upgrade[\s\S]*isolated_session_registration/i);
      }
      assert.equal(server.sessionPosts().length, 0, "no satellite POST may reach an unsupported server");
      assert.equal(server.writes("/observations").length + server.writes("/prompts").length, 0);
      const own = await registeredTools.get("mem_save").execute("own", { title: "t", content: "c" }, undefined, undefined, runtimeContext("runtime-a"));
      assert.equal(own.isError, undefined, "same-project saves do not require the capability");
    });
  });
}

test("satellite renewal rechecks capability instead of trusting a previous supported server", async () => {
  const health = { capabilities: { isolated_session_registration: true } };
  const server = fakeEngram({ health });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const save = registeredTools.get("mem_save");
    const ctx = runtimeContext("runtime-a");
    assert.equal((await save.execute("first", { title: "t", content: "c", project: "project-b" }, undefined, undefined, ctx)).isError, undefined);
    health.capabilities.isolated_session_registration = false;
    const renewal = await save.execute("renew", { title: "t", content: "c", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(renewal.isError, true);
    assert.equal(server.sessionPosts().length, 1, "unsupported replacement cannot renew the satellite");
    assert.equal(server.writes("/observations").length, 1);
  });
});

test("a first explicit foreign save never claims the runtime session for the foreign project", async () => {
  const server = fakeEngram();
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    const foreign = await registeredTools.get("mem_save").execute("foreign", { title: "f", content: "c", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(foreign.isError, undefined, foreign.content?.[0]?.text);
    const own = await registeredTools.get("mem_save").execute("own", { title: "o", content: "c" }, undefined, undefined, ctx);
    assert.equal(own.isError, undefined, own.content?.[0]?.text, "the detected project still owns the runtime session");
    assert.deepEqual(server.sessionPosts().map(({ id, project }) => [id, project]), [
      ["runtime-a@project-b", "project-b"],
      ["runtime-a", "project-a"],
    ]);
  });
});

test("repeated foreign saves share one satellite registration flight and keep the same identity", async () => {
  const gate = deferred();
  const server = fakeEngram({ sessionGate: gate });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const save = registeredTools.get("mem_save");
    const ctx = runtimeContext("runtime-a");
    const first = save.execute("one", { title: "one", content: "c", project: "project-b" }, undefined, undefined, ctx);
    const second = save.execute("two", { title: "two", content: "c", project: "project-b" }, undefined, undefined, ctx);
    for (let i = 0; i < 20 && server.sessionPosts().length === 0; i++) await new Promise((resolve) => setImmediate(resolve));
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(server.sessionPosts().length, 1, "concurrent foreign saves must share one registration flight");
    gate.resolve();
    const results = await Promise.all([first, second]);
    for (const result of results) assert.equal(result.isError, undefined, result.content?.[0]?.text);

    const third = await save.execute("three", { title: "three", content: "c", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(third.isError, undefined, third.content?.[0]?.text);
    const registrations = server.sessionPosts();
    assert.equal(registrations.length, 2, "a later write renews the satellite lease once");
    assert.ok(registrations.every(({ id, project }) => id === "runtime-a@project-b" && project === "project-b"));
    assert.deepEqual(server.writes("/observations").map(({ session_id }) => session_id),
      ["runtime-a@project-b", "runtime-a@project-b", "runtime-a@project-b"]);
  });
});

test("same-project explicit saves keep the runtime session identity", async () => {
  const server = fakeEngram();
  await withFakeEngram(server, async ({ registeredTools }) => {
    const save = registeredTools.get("mem_save");
    const ctx = runtimeContext("runtime-a");
    for (const params of [{ project: "project-a" }, {}]) {
      const result = await save.execute("same", { title: "same", content: "c", ...params }, undefined, undefined, ctx);
      assert.equal(result.isError, undefined, result.content?.[0]?.text);
    }
    assert.ok(server.sessionPosts().every(({ id, project }) => id === "runtime-a" && project === "project-a"));
    assert.deepEqual(server.writes("/observations").map(({ session_id, project }) => [session_id, project]), [
      ["runtime-a", "project-a"],
      ["runtime-a", "project-a"],
    ]);
  });
});

test("explicit foreign prompts and session summaries also use the satellite session", async () => {
  const server = fakeEngram();
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    const prompt = await registeredTools.get("mem_save_prompt").execute("prompt", { content: "foreign prompt", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(prompt.isError, undefined, prompt.content?.[0]?.text);
    const summary = await registeredTools.get("mem_session_summary").execute("summary", { content: "foreign summary", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(summary.isError, undefined, summary.content?.[0]?.text);
    assert.deepEqual(server.writes("/prompts").map(({ session_id, project }) => [session_id, project]), [["runtime-a@project-b", "project-b"]]);
    assert.deepEqual(server.writes("/observations").map(({ session_id, project }) => [session_id, project]), [["runtime-a@project-b", "project-b"]]);
    assert.ok(!server.sessionPosts().some(({ id }) => id === "runtime-a"), "foreign writes must not register the runtime session");
  });
});

test("a satellite adopts the server-selected continuation after its root ended", async () => {
  const server = fakeEngram({
    sessionAck: (body) => body.id === "runtime-a@project-b" ? { id: "runtime-a@project-b:resume:2", status: "created" } : undefined,
  });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    const result = await registeredTools.get("mem_save").execute("foreign", { title: "f", content: "c", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(result.isError, undefined, result.content?.[0]?.text);
    assert.equal(server.sessionPosts()[0].resume, true, "satellite registration lets core resume an ended root");
    assert.equal(server.writes("/observations")[0].session_id, "runtime-a@project-b:resume:2");
  });
});

test("a satellite acknowledgement for another identity stops the write", async () => {
  const server = fakeEngram({ sessionAck: () => ({ id: "someone-else", status: "created" }) });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const result = await registeredTools.get("mem_save").execute("foreign", { title: "f", content: "c", project: "project-b" }, undefined, undefined, runtimeContext("runtime-a"));
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /satellite session/);
    assert.equal(server.writes("/observations").length, 0);
  });
});

test("runtime shutdown ends its satellite sessions and blocks further satellite writes", async () => {
  const server = fakeEngram();
  await withFakeEngram(server, async ({ registeredTools, eventHandlers }) => {
    const ctx = runtimeContext("runtime-a");
    await eventHandlers.get("session_start")({}, ctx);
    const own = await registeredTools.get("mem_save").execute("own", { title: "o", content: "c" }, undefined, undefined, ctx);
    assert.equal(own.isError, undefined, own.content?.[0]?.text);
    const foreign = await registeredTools.get("mem_save").execute("foreign", { title: "f", content: "c", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(foreign.isError, undefined, foreign.content?.[0]?.text);
    await eventHandlers.get("session_shutdown")({}, ctx);
    const ends = server.calls.filter(({ path }) => path.endsWith("/end")).map(({ path }) => decodeURIComponent(path));
    assert.deepEqual(ends.sort(), ["/sessions/runtime-a/end", "/sessions/runtime-a@project-b/end"]);

    const afterShutdown = await registeredTools.get("mem_save").execute("late", { title: "late", content: "c", project: "project-b" }, undefined, undefined, ctx);
    assert.equal(afterShutdown.isError, true, "a closing runtime must not register new satellites");
    assert.equal(server.writes("/observations").length, 2);
  });
});

const PROJECT_B_CWD = "/repos/project-b/packages/core";
const CWD_PROJECTS = {
  [PROJECT_B_CWD]: { project: "project-b", project_path: "/repos/project-b" },
  "/repos/ambiguous": { project: "unknown", error_hint: "ambiguous project: pass project explicitly", available_projects: ["alpha", "beta"] },
};

test("an explicit cwd resolves only the write project and leaves the satellite directory empty", async () => {
  const server = fakeEngram({ projectsByCwd: CWD_PROJECTS });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    const result = await registeredTools.get("mem_save").execute("cwd", { title: "t", content: "c", cwd: PROJECT_B_CWD }, undefined, undefined, ctx);
    assert.equal(result.isError, undefined, result.content?.[0]?.text);
    assert.ok(server.calls.some(({ path, query }) => path === "/project/current" && query.get("cwd") === PROJECT_B_CWD));
    const [satellite] = server.sessionPosts();
    assert.deepEqual([satellite.id, satellite.project], ["runtime-a@project-b", "project-b"]);
    assert.ok(!satellite.directory?.trim(), "cwd-targeted satellites must not become implicit runtime candidates");
    assert.deepEqual(server.writes("/observations").map(({ session_id, project }) => [session_id, project]), [["runtime-a@project-b", "project-b"]]);
  });
});

test("an explicit cwd in the runtime project keeps the runtime session", async () => {
  const server = fakeEngram({ projectsByCwd: CWD_PROJECTS });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const result = await registeredTools.get("mem_save").execute("cwd", { title: "t", content: "c", cwd: ROOT }, undefined, undefined, runtimeContext("runtime-a"));
    assert.equal(result.isError, undefined, result.content?.[0]?.text);
    assert.deepEqual(server.writes("/observations").map(({ session_id, project }) => [session_id, project]), [["runtime-a", "project-a"]]);
  });
});

test("cwd and project must agree before any registration or write", async () => {
  const server = fakeEngram({ projectsByCwd: CWD_PROJECTS });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    const conflict = await registeredTools.get("mem_save").execute("disagree", { title: "t", content: "c", cwd: PROJECT_B_CWD, project: "project-c" }, undefined, undefined, ctx);
    assert.equal(conflict.isError, true);
    assert.match(conflict.content[0].text, /project-c[\s\S]*project-b/);
    assert.equal(server.sessionPosts().length, 0);
    assert.equal(server.writes("/observations").length, 0);

    const agreed = await registeredTools.get("mem_save").execute("agree", { title: "t", content: "c", cwd: PROJECT_B_CWD, project: "project-b" }, undefined, undefined, ctx);
    assert.equal(agreed.isError, undefined, agreed.content?.[0]?.text);
    assert.deepEqual(server.writes("/observations").map(({ session_id, project }) => [session_id, project]), [["runtime-a@project-b", "project-b"]]);
  });
});

test("an ambiguous cwd surfaces the project choice instead of guessing", async () => {
  const server = fakeEngram({ projectsByCwd: CWD_PROJECTS });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    for (const tool of ["mem_save", "mem_save_prompt", "mem_session_summary"]) {
      const result = await registeredTools.get(tool).execute(tool, { title: "t", content: "c", cwd: "/repos/ambiguous" }, undefined, undefined, ctx);
      assert.equal(result.isError, true, `${tool} must not guess an ambiguous cwd`);
      assert.match(result.content[0].text, /ambiguous project/);
      assert.match(result.content[0].text, /alpha, beta/);
    }
    assert.equal(server.sessionPosts().length, 0);
    assert.equal(server.writes("/observations").length + server.writes("/prompts").length, 0);
  });
});

test("cwd routes prompts and session summaries to the resolved project", async () => {
  const server = fakeEngram({ projectsByCwd: CWD_PROJECTS });
  await withFakeEngram(server, async ({ registeredTools }) => {
    const ctx = runtimeContext("runtime-a");
    for (const tool of ["mem_save_prompt", "mem_session_summary"]) {
      const result = await registeredTools.get(tool).execute(tool, { content: "c", cwd: PROJECT_B_CWD }, undefined, undefined, ctx);
      assert.equal(result.isError, undefined, result.content?.[0]?.text);
    }
    assert.deepEqual([...server.writes("/prompts"), ...server.writes("/observations")].map(({ session_id, project }) => [session_id, project]), [
      ["runtime-a@project-b", "project-b"],
      ["runtime-a@project-b", "project-b"],
    ]);
  });
});

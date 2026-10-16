import assert from "node:assert/strict";
import { test } from "node:test";
import { withPluginSandbox, importPluginFromSandbox } from "./plugin-sandbox.mjs";

async function fixture(detection, run) {
  const oldFetch = globalThis.fetch;
  const oldURL = process.env.ENGRAM_URL;
  const oldChild = process.env.GENTLE_PI_AGENTS_CHILD;
  delete process.env.GENTLE_PI_AGENTS_CHILD;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, method: init.method || "GET", body });
    let data = {};
    if (path === "/health") data = { capabilities: { isolated_session_registration: true } };
    if (path === "/project/current") data = detection;
    if (path === "/sessions") data = { id: body.id, status: "created" };
    if (path === "/observations" || path === "/prompts") data = { id: 1, status: "saved" };
    return new Response(JSON.stringify(data), { status: body ? 201 : 200 });
  };
  try {
    await withPluginSandbox("engram-provenance-", async ({ sandbox, dir }) => {
      const tools = new Map();
      const events = new Map();
      (await importPluginFromSandbox(sandbox))({ registerTool(t) { tools.set(t.name, t); }, on(n, h) { events.set(n, h); } });
      const ctx = { cwd: dir, sessionManager: { getSessionId: () => "runtime" }, ui: { setStatus() {} } };
      const execute = (name, params) => tools.get(name).execute("call", params, undefined, undefined, ctx);
      await run({ calls, ctx, execute, events });
    });
  } finally {
    globalThis.fetch = oldFetch;
    if (oldChild === undefined) delete process.env.GENTLE_PI_AGENTS_CHILD; else process.env.GENTLE_PI_AGENTS_CHILD = oldChild;
    if (oldURL === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = oldURL;
  }
}

for (const source of ["git_root", "git_remote", "dir_basename", "config", "process_override", undefined]) {
  test(`capture transports honest ${source ?? "unknown"} evidence without changing attribution`, async () => {
    await fixture({ project: "same-name", project_source: source, cwd: "/unrelated/server" }, async ({ calls, ctx, execute }) => {
      assert.equal((await execute("mem_save", { title: "Title", content: "Content", capture_prompt: false })).isError, undefined);
      assert.equal((await execute("mem_save_prompt", { content: "Prompt" })).isError, undefined);
      assert.equal((await execute("mem_session_start", { id: "manual", directory: "preserved" })).isError, undefined);
      const writes = calls.filter(c => c.body);
      for (const { body } of writes) assert.deepEqual(body.project_provenance, { source: source ?? "unknown", directory: ctx.cwd });
      const observation = writes.find(c => c.path === "/observations").body;
      assert.equal(observation.project, "same-name");
      assert.equal(observation.session_id, "runtime");
      assert.equal(observation.content, "Content");
      assert.equal(observation.capture_prompt, false);
      assert.match(observation.operation_id, /^[0-9a-f-]{36}$/);
      assert.equal(writes.find(c => c.body.id === "manual").body.directory, "preserved");
    });
  });
}

test("per-call explicit project retains raw input and satellite contract", async () => {
  await fixture({ project: "home", project_source: "git_root" }, async ({ calls, ctx, execute }) => {
    assert.equal((await execute("mem_save", { title: "Title", content: "Content", project: "Other Name" })).isError, undefined);
    for (const { body } of calls.filter(c => c.body)) assert.deepEqual(body.project_provenance, { source: "explicit_override", directory: ctx.cwd, raw_project: "Other Name" });
    const registration = calls.find(c => c.path === "/sessions").body;
    assert.equal(registration.id, "runtime@Other Name");
    assert.equal(registration.isolated, true);
    assert.equal(registration.ownership_mode, "project_owned");
    assert.equal(registration.directory, undefined);
  });
});

test("cwd evidence stays derived and does not fabricate raw project", async () => {
  await fixture({ project: "home", project_source: "dir_basename" }, async ({ calls, ctx, execute }) => {
    assert.equal((await execute("mem_save", { title: "Title", content: "Content", cwd: ctx.cwd })).isError, undefined);
    assert.deepEqual(calls.find(c => c.path === "/observations").body.project_provenance, { source: "dir_basename", directory: ctx.cwd });
  });
});

test("lifecycle registration and automatic prompt retain optional evidence and content", async () => {
  await fixture({ project: "home", project_source: "git_root" }, async ({ calls, ctx, events }) => {
    await events.get("session_start")({}, ctx);
    await events.get("before_agent_start")({ systemPrompt: "", prompt: "A sufficiently long human prompt" }, ctx);
    const writes = calls.filter(c => c.body && ["/sessions", "/prompts"].includes(c.path));
    assert.ok(writes.some(c => c.path === "/sessions"));
    assert.ok(writes.some(c => c.path === "/prompts"));
    for (const { body } of writes) assert.deepEqual(body.project_provenance, { source: "git_root", directory: ctx.cwd });
    assert.equal(writes.find(c => c.path === "/prompts").body.content, "A sufficiently long human prompt");
    assert.equal(writes.find(c => c.path === "/prompts").body.session_id, "runtime");
  });
});

test("summary and passive tools keep content and attribution with evidence", async () => {
  await fixture({ project: "home" }, async ({ calls, ctx, execute }) => {
    for (const name of ["mem_session_summary", "mem_capture_passive"]) {
      assert.equal((await execute(name, { content: "Structured learning", source: "manual" })).isError, undefined);
    }
    const summary = calls.find(c => c.path === "/observations").body;
    const passive = calls.find(c => c.path === "/observations/passive").body;
    for (const body of [summary, passive]) {
      assert.equal(body.session_id, "runtime");
      assert.equal(body.project, "home");
      assert.equal(body.content, "Structured learning");
      assert.deepEqual(body.project_provenance, { source: "unknown", directory: ctx.cwd });
    }
    assert.equal(summary.type, "session_summary");
    assert.equal(passive.source, "manual");
  });
});

test("session end carries evidence without changing its summary or identity", async () => {
  await fixture({ project: "home", project_source: "git_root" }, async ({ calls, ctx, execute }) => {
    await execute("mem_save", { title: "Title", content: "Content" });
    const result = await execute("mem_session_end", { id: "runtime", summary: "Closing summary" });
    assert.equal(result.isError, undefined);
    const end = calls.find(c => c.path === "/sessions/runtime/end").body;
    assert.equal(end.summary, "Closing summary");
    assert.deepEqual(end.project_provenance, { source: "git_root", directory: ctx.cwd });
  });
});

test("reliable raw detection evidence is preserved without reconstructing it", async () => {
  await fixture({ project: "normalized", project_source: "config", raw_project: " Original Name " }, async ({ calls, ctx, execute }) => {
    assert.equal((await execute("mem_save", { title: "Title", content: "Content" })).isError, undefined);
    assert.deepEqual(calls.find(c => c.path === "/observations").body.project_provenance,
      { source: "config", directory: ctx.cwd, raw_project: " Original Name " });
  });
});

test("mismatched explicit project and cwd rejects without capture requests", async () => {
  await fixture({ project: "home", project_source: "git_root" }, async ({ calls, ctx, execute }) => {
    const result = await execute("mem_save", { title: "Title", content: "Content", project: "other", cwd: ctx.cwd });
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /Explicit project other does not match project home/);
    assert.equal(calls.filter(c => c.body).length, 0);
  });
});

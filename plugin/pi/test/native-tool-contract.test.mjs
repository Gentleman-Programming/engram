import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { createInterface } from "node:readline";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
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

// Mode labels model documented hasUI capabilities; this is not a live RPC client test.
for (const scenario of ["tui-server", "rpc-transport", "print", "json", "throwing-notifier", "missing-notifier", "incomplete-ui"]) {
  test(`background warning routing: ${scenario}`, async () => {
    const originalFetch = globalThis.fetch;
    const originalUrl = process.env.ENGRAM_URL;
    const originalWrite = process.stderr.write;
    const stderr = [];
    const notifications = [];
    process.env.ENGRAM_URL = "http://127.0.0.1:17437";
    process.stderr.write = (chunk) => { stderr.push(String(chunk)); return true; };
    const gate = deferred();
    const started = deferred();
    globalThis.fetch = async (url, init = {}) => {
      const path = new URL(url).pathname;
      if (path === "/project/current") return new Response('{"project":"engram"}');
      if (path === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }), { status: 201 });
      if (path === "/prompts") {
        started.resolve();
        await gate.promise;
        if (scenario === "rpc-transport") throw Object.assign(new Error("timeout"), { name: "TimeoutError" });
        return new Response('{"error":"capture rejected"}', { status: 503 });
      }
      return new Response('{}');
    };
    try {
      await withPluginSandbox("engram-pi-warning-", async ({ sandbox }) => {
        const { eventHandlers } = await loadPluginHarness(sandbox);
        const ctx = runtimeContext("warning-owner");
        ctx.mode = scenario.startsWith("rpc") ? "rpc" : scenario === "json" ? "json" : scenario === "print" ? "print" : "tui";
        ctx.hasUI = !["print", "json", "missing-notifier"].includes(scenario);
        ctx.ui.notify = (message, severity) => {
          notifications.push([message, severity]);
          if (scenario === "throwing-notifier") throw new Error("UI disposed");
        };
        if (["missing-notifier", "incomplete-ui"].includes(scenario)) delete ctx.ui.notify;
        const capture = eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "a sufficiently long captured prompt" }, ctx);
        await started.promise;
        // Another session event must not steal the suspended capture's diagnostic UI.
        const unrelated = runtimeContext("unrelated-session");
        unrelated.hasUI = true;
        unrelated.ui.notify = () => assert.fail("warning reached unrelated session");
        await eventHandlers.get("session_start")({}, unrelated);
        gate.resolve();
        const result = await capture;
        assert.doesNotMatch(result.systemPrompt, /capture rejected|outcome is unknown/);
        const headless = ["print", "json", "missing-notifier"].includes(scenario);
        assert.equal(stderr.length, headless ? 1 : 0);
        assert.equal(notifications.length, headless || scenario === "incomplete-ui" ? 0 : 1);
        if (scenario === "incomplete-ui") return;
        const message = headless ? stderr[0] : notifications[0][0];
        assert.match(message, /background capture to \/prompts failed/);
        assert.match(message, scenario === "rpc-transport" ? /outcome is unknown/ : /capture rejected/);
        if (!headless) assert.equal(notifications[0][1], "warning");
      });
    } finally {
      gate.resolve();
      globalThis.fetch = originalFetch;
      process.stderr.write = originalWrite;
      if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
    }
  });
}

for (const failure of ["shutdown", "archive", "recovery"]) {
  test(`lifecycle UI warning uses owning context: ${failure}`, async () => {
    const originalFetch = globalThis.fetch;
    const originalUrl = process.env.ENGRAM_URL;
    const originalWrite = process.stderr.write;
    const stderr = [];
    const notifications = [];
    const calls = [];
    const sessionId = `warning-${failure}`;
    const failedPath = failure === "shutdown" ? `/sessions/${sessionId}/end`
      : failure === "archive" ? "/observations" : "/context/compaction";
    process.env.ENGRAM_URL = "http://127.0.0.1:17437";
    process.stderr.write = (chunk) => { stderr.push(String(chunk)); return true; };
    globalThis.fetch = async (url, init = {}) => {
      const request = new URL(url);
      calls.push({ path: request.pathname, method: init.method ?? "GET", query: request.searchParams, body: init.body ? JSON.parse(init.body) : undefined });
      if (request.pathname === failedPath) return new Response(JSON.stringify({ error: `${failure} rejected` }), { status: 503 });
      if (request.pathname === "/project/current") return new Response('{"project":"engram"}');
      if (request.pathname === "/context/compaction") return new Response('{"context":"recovery context"}');
      if (request.pathname === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }), { status: 201 });
      return new Response('{"id":1,"status":"created"}');
    };
    try {
      await withPluginSandbox("engram-pi-lifecycle-warning-", async ({ sandbox }) => {
        const { eventHandlers } = await loadPluginHarness(sandbox);
        const owner = runtimeContext(sessionId);
        owner.hasUI = true;
        owner.ui.notify = (message, severity) => notifications.push([message, severity]);
        await eventHandlers.get("session_start")({}, owner);
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "register this owning session first" }, owner);
        if (failure === "shutdown") {
          await eventHandlers.get("session_shutdown")({}, owner);
          await eventHandlers.get("session_shutdown")({}, owner);
          assert.equal(calls.filter(({ path }) => path === failedPath).length, 1, "cleanup prevents repeated terminal delivery");
        } else {
          // Pi can supply a stale compaction context: only the observed identity owns diagnostics.
          const stale = runtimeContext("stale-unrelated-session");
          stale.hasUI = true;
          stale.ui.notify = () => assert.fail("compaction warning reached stale UI");
          await eventHandlers.get("session_compact")({ compactionEntry: { summary: "preserve this compacted summary" } }, stale);
          const archives = calls.filter(({ path }) => path === "/observations");
          assert.equal(archives.length, 1, "archive is never retried");
          assert.equal(archives[0].body.session_id, sessionId);
          const recovery = calls.find(({ path }) => path === "/context/compaction");
          assert.equal(recovery.query.get("session_id"), sessionId);
          const next = await eventHandlers.get("before_agent_start")({ systemPrompt: "base" }, owner);
          assert.match(next.systemPrompt, failure === "archive" ? /FIRST ACTION REQUIRED/ : /already saved/);
          assert.doesNotMatch(next.systemPrompt, /archive rejected|recovery rejected/);
          const consumed = await eventHandlers.get("before_agent_start")({ systemPrompt: "base" }, owner);
          assert.doesNotMatch(consumed.systemPrompt, /FIRST ACTION REQUIRED|already saved/);
        }
        assert.equal(stderr.length, 0);
        assert.equal(notifications.length, 1);
        assert.equal(notifications[0][1], "warning");
        assert.ok(notifications[0][0].includes(`background capture to ${failedPath} failed`));
        assert.match(notifications[0][0], new RegExp(`${failure} rejected`));
      });
    } finally {
      globalThis.fetch = originalFetch;
      process.stderr.write = originalWrite;
      if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
    }
  });
}

function runtimeContext(sessionId) {
  return {
    cwd: ROOT,
    sessionManager: { getSessionId: typeof sessionId === "function" ? sessionId : () => sessionId },
    ui: { setStatus() {} },
  };
}

for (const capture of [undefined, true, false]) {
  test(`mem_save prompt capture forwarding: ${capture}`, async () => {
    const originalFetch = globalThis.fetch;
    const keys = ["ENGRAM_URL", "ENGRAM_PROJECT", "GENTLE_PI_AGENTS_CHILD"];
    const originalEnv = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
    process.env.ENGRAM_URL = "http://127.0.0.1:17437";
    delete process.env.ENGRAM_PROJECT;
    delete process.env.GENTLE_PI_AGENTS_CHILD;
    const { calls, fetchStub } = recordingFetch([
      { method: "GET", path: "/health", body: { capabilities: { isolated_session_registration: true } } },
      { method: "GET", path: "/project/current", body: { project: "engram" } },
      { method: "POST", path: "/sessions", body: {} },
      { method: "POST", path: "/prompts", status: 201, body: { id: 1, status: "saved" } },
      { method: "POST", path: "/observations", status: 201, body: { id: 2, status: "saved" } },
    ]);
    globalThis.fetch = fetchStub;
    try {
      await withPluginSandbox("engram-pi-save-capture-", async ({ sandbox }) => {
        const { eventHandlers, registeredTools } = await loadPluginHarness(sandbox);
        const ctx = runtimeContext(`capture-${capture}`);
        await eventHandlers.get("session_start")({}, ctx);
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "A user prompt with <private>secret</private> safe content" }, ctx);
        const save = async (params = {}, context = ctx) => {
          const result = await registeredTools.get("mem_save").execute("capture", { title: "saved", content: "observation", ...params }, undefined, undefined, context);
          assert.equal(result.isError, undefined, JSON.stringify(result));
          return result;
        };
        const result = await save({ capture_prompt: capture });
        assert.equal(result.isError, undefined, JSON.stringify(result));
        const body = calls.filter(({ path }) => path === "/observations").at(-1).body;
        assert.equal(body.capture_prompt, capture);
        assert.equal(body.current_prompt, capture === false ? undefined : "A user prompt with [REDACTED] safe content");
        assert.equal(calls.filter(({ path }) => path === "/prompts").length, 1, "per-save opt-out must not disable lifecycle capture");
        assert.doesNotMatch(JSON.stringify(body), /secret/);
        // An explicit satellite project never inherits the owner's prompt.
        await save({ project: "other" });
        assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, undefined);
        // A different runtime never inherits this prompt.
        await save({}, runtimeContext("another-capture-runtime"));
        assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, undefined);
        // Short and synthetic turns invalidate the previous prompt rather than reusing it.
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "short" }, ctx);
        await save();
        assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, undefined);
        await eventHandlers.get("input")({ source: "extension", text: "Synthetic extension prompt", images: [] }, ctx);
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "Synthetic extension prompt" }, ctx);
        await save();
        assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, undefined);
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "Another eligible human prompt" }, ctx);
        await save();
        assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, "Another eligible human prompt");
        await eventHandlers.get("session_shutdown")({ reason: "reload" }, ctx);
        await eventHandlers.get("session_start")({}, ctx);
        await save();
        assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, undefined);
      });
    } finally {
      globalThis.fetch = originalFetch;
      for (const key of keys) { if (originalEnv[key] === undefined) delete process.env[key]; else process.env[key] = originalEnv[key]; }
    }
  });
}

for (const streamingBehavior of ["steer", "followUp"]) {
  test(`mem_save prompt capture queued consumption: ${streamingBehavior}`, async () => {
    const keys = ["ENGRAM_URL", "ENGRAM_PROJECT", "GENTLE_PI_AGENTS_CHILD"];
    const originalEnv = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
    const originalFetch = globalThis.fetch;
    process.env.ENGRAM_URL = "http://127.0.0.1:17437";
    delete process.env.ENGRAM_PROJECT;
    delete process.env.GENTLE_PI_AGENTS_CHILD;
    const { calls, fetchStub } = recordingFetch([
      { method: "GET", path: "/project/current", body: { project: "engram" } },
      { method: "POST", path: "/sessions", body: {} },
      { method: "POST", path: "/prompts", status: 201, body: { id: 1, status: "saved" } },
      { method: "POST", path: "/observations", status: 201, body: { id: 2, status: "saved" } },
    ]);
    globalThis.fetch = fetchStub;
    try {
      await withPluginSandbox("engram-pi-queued-capture-", async ({ sandbox }) => {
        const { eventHandlers, registeredTools } = await loadPluginHarness(sandbox);
        const ctx = runtimeContext(`queued-${streamingBehavior}`);
        const prompt = "Repeated user prompt with <private>queued-secret</private> safe content";
        const redacted = "Repeated user prompt with [REDACTED] safe content";
        const message = async (role) => eventHandlers.get("message_start")?.({ message: { role, content: [{ type: "text", text: prompt }] } }, ctx);
        const save = async (expected) => {
          const result = await registeredTools.get("mem_save").execute("queued", { title: "saved", content: "observation" }, undefined, undefined, ctx);
          assert.equal(result.isError, undefined, JSON.stringify(result));
          assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.current_prompt, expected);
        };
        await eventHandlers.get("session_start")({}, ctx);
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt }, ctx);
        await message("assistant");
        await message("user"); // Initial agent.prompt message must preserve the native capture.
        await save(redacted);
        const foreign = { ...ctx, sessionManager: { getSessionId: () => "foreign-queued-runtime" } };
        await eventHandlers.get("input")({ source: "interactive", text: prompt, images: [], streamingBehavior }, foreign);
        await eventHandlers.get("message_start")?.({ message: { role: "user" } }, foreign);
        await save(redacted);
        for (let queued = 0; queued < 2; queued++) {
          await eventHandlers.get("input")({ source: "interactive", text: prompt, images: [], streamingBehavior }, ctx);
          await save(queued === 0 ? redacted : undefined); // Enqueue alone cannot invalidate an active run.
          await message("user"); // Identical text is still a distinct consumed queued turn.
          await save(undefined);
        }
        assert.equal(calls.filter(({ path }) => path === "/prompts").length, 1, "queued content is never captured");
        await eventHandlers.get("input")({ source: "interactive", text: "Next normal user prompt", images: [] }, ctx);
        await eventHandlers.get("before_agent_start")({ systemPrompt: "base", prompt: "Next normal user prompt" }, ctx);
        await message("user");
        await save("Next normal user prompt");
        await eventHandlers.get("session_shutdown")({ reason: "reload" }, ctx);
        await eventHandlers.get("session_start")({}, ctx);
        await message("user");
        await save(undefined);
        assert.doesNotMatch(JSON.stringify(calls), /queued-secret/);
      });
    } finally {
      globalThis.fetch = originalFetch;
      for (const key of keys) { if (originalEnv[key] === undefined) delete process.env[key]; else process.env[key] = originalEnv[key]; }
    }
  });
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
    const payload = path === "/sessions" && status < 300
      ? { ...route?.body, id: body.id, status: "created" } : route?.body ?? {};
    return new Response(JSON.stringify(payload), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  };
  return { calls, fetchStub };
}

// Issue #1673: drive the registered input/start hooks and inspect HTTP requests.
// The input hook is optional here because the buggy plugin does not subscribe to it.
for (const scenario of [
  { name: "interactive", source: "interactive", capture: true },
  { name: "human RPC host", source: "rpc", capture: true },
  { name: "unknown provenance", capture: true },
  { name: "expanded interactive input", source: "interactive", inputText: "/explain", capture: true },
  { name: "extension", source: "extension", capture: false },
  { name: "extension wake", source: "extension", wake: true, capture: false },
  { name: "delegated-child", source: "rpc", child: true, capture: false },
  { name: "queued steer before interactive", source: "interactive", queuedBefore: "extension", capture: true },
  { name: "queued follow-up after extension", source: "extension", queuedAfter: "interactive", capture: false },
  { name: "handled extension before interactive", source: "interactive", staleInput: true, capture: true },
  { name: "session start clears provenance", staleInput: true, restart: true, capture: true },
  { name: "runtime switch isolates provenance", staleInput: true, switchSession: true, capture: true },
  { name: "reload clears provenance", staleInput: true, reload: true, capture: true },
]) {
  test(`prompt provenance regression: ${scenario.name}`, async () => {
    const keys = ["ENGRAM_URL", "ENGRAM_PROJECT", "GENTLE_PI_AGENTS_CHILD"];
    const originalEnv = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
    const originalFetch = globalThis.fetch;
    process.env.ENGRAM_URL = "http://127.0.0.1:17437";
    delete process.env.ENGRAM_PROJECT;
    if (scenario.child) process.env.GENTLE_PI_AGENTS_CHILD = "1";
    else delete process.env.GENTLE_PI_AGENTS_CHILD;
    const { calls, fetchStub } = recordingFetch([
      { method: "GET", path: "/project/current", body: { project: "engram" } },
      { method: "POST", path: "/sessions", body: {} },
      { method: "POST", path: "/prompts", status: 201, body: { id: 1, status: "saved" } },
    ]);
    globalThis.fetch = fetchStub;
    try {
      await withPluginSandbox("engram-pi-provenance-", async ({ sandbox }) => {
        const { eventHandlers } = await loadPluginHarness(sandbox);
        let sessionId = `provenance-${scenario.name}`;
        const ctx = runtimeContext(() => sessionId);
        await eventHandlers.get("session_start")({}, ctx);
        const input = async (source, text, streamingBehavior) => {
          await eventHandlers.get("input")?.({ text, source, streamingBehavior, images: [] }, ctx);
        };
        if (scenario.staleInput) await input("extension", "An extension input that never starts an agent run.");
        if (scenario.restart) await eventHandlers.get("session_start")({}, ctx);
        if (scenario.reload) {
          await eventHandlers.get("session_shutdown")({ reason: "reload" }, ctx);
          await eventHandlers.get("session_start")({}, ctx);
        }
        if (scenario.switchSession) sessionId += "-switched";
        const prompt = scenario.wake
          ? "[System-generated Gentle Agents notification, not written by the user] Subagent output was delivered to this session above. Review it and continue. [gentle-agents wake: regression]"
          : scenario.child
            ? "Independent read-only verification of the uncommitted changes."
            : scenario.source === "extension"
              ? "Continue automatically after receiving an extension update."
              : "Please explain the root cause of this reported bug.";
        if (scenario.queuedBefore) await input(scenario.queuedBefore, "Queued automatic steering update.", "steer");
        if (scenario.source) await input(scenario.source, scenario.inputText ?? prompt);
        if (scenario.queuedAfter) await input(scenario.queuedAfter, "Queued human follow-up request.", "followUp");
        await eventHandlers.get("before_agent_start")({ prompt, systemPrompt: "base" }, ctx);
        const writes = calls.filter(({ method, path }) => method === "POST" && path === "/prompts");
        assert.deepEqual(writes.map(({ body }) => body), scenario.capture ? [{
          session_id: sessionId, content: prompt, project: "engram",
        }] : [], "only human-authored prompts may be persisted, exactly once");
      });
    } finally {
      globalThis.fetch = originalFetch;
      for (const key of keys) {
        if (originalEnv[key] === undefined) delete process.env[key];
        else process.env[key] = originalEnv[key];
      }
    }
  });
}

test("Pi native saves persist under separate host sessions and stop on failed registration", async () => {
  await withPluginSandbox("engram-pi-real-", async ({ dir, sandbox }) => {
    const original = Object.fromEntries(["ENGRAM_URL", "ENGRAM_PROJECT", "ENGRAM_DATA_DIR", "ENGRAM_CLOUD_AUTOSYNC", "HOME"].map((key) => [key, process.env[key]]));
    const executable = join(dir, process.platform === "win32" ? "real-server.exe" : "real-server");
    const build = spawnSync("go", ["build", "-o", executable, "./plugin/pi/test/support/real-server"], {
      cwd: join(ROOT, "../.."), timeout: 60000, encoding: "utf8",
      // Keep Go's toolchain and module caches outside the disposable server sandbox.
      env: process.env,
    });
    assert.ifError(build.error);
    assert.equal(build.status, 0, build.stderr);
    const child = spawn(executable, [join(dir, "store")], {
      cwd: join(ROOT, "../.."),
      env: { ...process.env, HOME: dir, ENGRAM_DATA_DIR: join(dir, "data"), ENGRAM_PROJECT: "pi-persistence-test", ENGRAM_CLOUD_AUTOSYNC: "0" },
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stderr = "";
    child.on("error", (error) => { stderr += error.message; });
    child.stderr.setEncoding("utf8").on("data", (chunk) => { stderr += chunk; });
    const lines = createInterface({ input: child.stdout });
    try {
      const url = await new Promise((resolve, reject) => {
        const finish = (error, value) => {
          clearTimeout(timer);
          lines.off("line", onLine);
          child.off("error", onError);
          child.off("exit", onExit);
          if (error) reject(error); else resolve(value);
        };
        const onLine = (line) => finish(null, line);
        const onError = (error) => finish(error);
        const onExit = (code) => finish(new Error(`server exited ${code}: ${stderr}`));
        const timer = setTimeout(() => finish(new Error(`server startup timed out: ${stderr}`)), 60000);
        lines.once("line", onLine);
        child.once("error", onError);
        child.once("exit", onExit);
      });
      process.env.ENGRAM_URL = url;
      process.env.ENGRAM_PROJECT = "pi-persistence-test";
      process.env.ENGRAM_DATA_DIR = join(dir, "data");
      process.env.ENGRAM_CLOUD_AUTOSYNC = "0";
      process.env.HOME = dir;
      const entries = [];
      const append = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, append);
      const save = registeredTools.get("mem_save");
      const ids = ["pi-host-alpha", "pi-host-beta"];
      for (const [index, host] of [ids[0], ids[1], ids[0], ids[1]].entries()) {
        const result = await save.execute(`real-${index}`, {
          title: `persisted-${index}`, content: `real persistence ${index}`,
          project: "pi-persistence-test", session_id: "model-foreign-session",
        }, undefined, undefined, runtimeContext(host));
        assert.notEqual(result.isError, true, result.content?.[0]?.text);
      }
      const persisted = async () => {
        const response = await fetch(`${url}/observations?project=pi-persistence-test&limit=20`);
        assert.equal(response.status, 200);
        return response.json();
      };
      const rows = await persisted();
      assert.equal(rows.length, 4, JSON.stringify(rows));
      for (let index = 0; index < 4; index++) {
        const row = rows.find(({ title }) => title === `persisted-${index}`);
        assert.ok(row, `missing persisted row ${index}`);
        assert.equal(row.session_id, ids[index % 2]);
        assert.notEqual(row.session_id, "model-foreign-session");
      }
      // Exercise cross-project routing against the same real core, not a wire-only stub.
      const runtimeBefore = await (await fetch(`${url}/sessions/${ids[0]}`)).json();
      const foreign = await save.execute("real-foreign", {
        title: "real-project-b", content: "cross-project persistence", project: "pi-target-b",
      }, undefined, undefined, runtimeContext(ids[0]));
      assert.equal(foreign.isError, undefined, JSON.stringify(foreign));
      const foreignRows = await (await fetch(`${url}/observations?project=pi-target-b&limit=20`)).json();
      assert.equal(foreignRows.length, 1);
      assert.equal(foreignRows[0].project, "pi-target-b");
      assert.equal(foreignRows[0].session_id, `${ids[0]}@pi-target-b`);
      const satellite = await (await fetch(`${url}/sessions/${encodeURIComponent(foreignRows[0].session_id)}`)).json();
      assert.equal(satellite.project, "pi-target-b");
      assert.equal(satellite.ownership_mode, "project_owned");
      assert.equal(satellite.directory, "", "no directory binding means no satellite runtime candidacy (core regression tests assert ActiveRuntimeSessions)");
      assert.deepEqual(await (await fetch(`${url}/sessions/${ids[0]}`)).json(), runtimeBefore, "foreign save must not mutate principal runtime owner A");
      // Exercise core resume against the real server, including a fresh module graph reload.
      const resumedCtx = runtimeContext(ids[0]);
      resumedCtx.sessionManager.getBranch = () => entries;
      await eventHandlers.get("session_shutdown")({}, resumedCtx);
      await eventHandlers.get("session_start")({}, resumedCtx);
      const resumed = await save.execute("real-resume", { title: "real-resumed", content: "server continuation" }, undefined, undefined, resumedCtx);
      assert.equal(resumed.isError, undefined, JSON.stringify(resumed));
      const effectiveID = `${ids[0]}:resume:2`;
      assert.equal(entries.at(-1).data.effectiveID, effectiveID);
      assert.equal((await persisted()).find(({ title }) => title === "real-resumed").session_id, effectiveID);
      await eventHandlers.get("session_shutdown")({ reason: "reload" }, resumedCtx);
      await withPluginSandbox("engram-pi-real-resume-reload-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, append);
        await next.eventHandlers.get("session_start")({}, resumedCtx);
        const result = await next.registeredTools.get("mem_save").execute("real-reload", { title: "real-reloaded", content: "persisted continuation" }, undefined, undefined, resumedCtx);
        assert.equal(result.isError, undefined, JSON.stringify(result));
        assert.equal((await persisted()).find(({ title }) => title === "real-reloaded").session_id, effectiveID);
        await next.eventHandlers.get("session_shutdown")({}, resumedCtx);
      });
      const endedResponse = await fetch(`${url}/sessions/${encodeURIComponent(effectiveID)}`);
      assert.ok((await endedResponse.json()).ended_at, "quit ends the real effective session");
      // A failed registration is intercepted before any observation endpoint can be reached.
      const realFetch = globalThis.fetch;
      let observationPosts = 0;
      globalThis.fetch = (request, init = {}) => {
        const path = new URL(request).pathname;
        if (path === "/sessions" && init.method === "POST") {
          return Promise.resolve(new Response(JSON.stringify({ error: "registration unavailable" }), { status: 503 }));
        }
        if (path === "/observations" && init.method === "POST") observationPosts++;
        return realFetch(request, init);
      };
      try {
        const failed = await save.execute("real-denied", {
          title: "must-not-persist", content: "registration failed", project: "pi-persistence-test",
        }, undefined, undefined, runtimeContext("pi-host-unregistered"));
        assert.equal(failed.isError, true);
        assert.equal(observationPosts, 0);
      } finally {
        globalThis.fetch = realFetch;
      }
      assert.equal((await persisted()).length, 6, "failed registration must not create a persisted row");
    } finally {
      for (const [key, value] of Object.entries(original)) {
        if (value === undefined) delete process.env[key]; else process.env[key] = value;
      }
      lines.close();
      if (child.exitCode === null && child.signalCode === null && child.pid) {
        await new Promise((resolve, reject) => {
          let timer = setTimeout(() => {
            if (!child.kill()) {
              reject(new Error("test server did not stop and could not be killed"));
              return;
            }
            timer = setTimeout(() => reject(new Error("test server did not exit after kill")), 5000);
          }, 5000);
          child.once("exit", () => { clearTimeout(timer); resolve(); });
          child.stdin.end();
        });
      }
    }
  });
});

test("Pi-native mem_session_end refuses an existing foreign session without an end request", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "GET", path: "/project/current", body: { project: "paidosdep" } },
    { method: "POST", path: "/sessions/foreign-existing-session/end", body: { status: "ended" } },
  ]);
  globalThis.fetch = fetchStub;
  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const result = await registeredTools.get("mem_session_end").execute(
        "foreign-end", { id: "foreign-existing-session" }, undefined, undefined, runtimeContext("host-session"),
      );
      assert.equal(result.isError, true);
      assert.equal(calls.filter((call) => call.method === "POST" && call.path.endsWith("/end")).length, 0);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

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

test("one Pi runtime session routes explicit foreign prompts to a satellite and never captures passive observations across projects", async () => {
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
    { method: "GET", path: "/health", body: { status: "ok", capabilities: { isolated_session_registration: true } } },
    { method: "GET", path: "/project/current", body: { project: "project-b" } },
    { method: "POST", path: "/sessions", body: { status: "created" } },
    { method: "POST", path: "/prompts", body: { id: 1 } },
    { method: "POST", path: "/observations/passive", body: { id: 2 } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const sessionId = "cross-project-runtime-session";
      // The runtime identity is persisted as owned by project-a while this cwd detects project-b.
      const entries = [{ type: "custom", customType: "engram-effective-session", data: {
        runtimeID: sessionId, effectiveID: sessionId, pending: true, project: "project-a",
      } }];
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const memSavePrompt = registeredTools.get("mem_save_prompt");
      const ctx = runtimeContext(sessionId);
      ctx.sessionManager.getBranch = () => entries;
      const notifications = [];
      ctx.hasUI = true;
      ctx.ui.notify = (message, severity) => notifications.push([message, severity]);

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
        { content: "this explicit prompt goes to a project-b satellite session", project: "project-b" },
        undefined,
        undefined,
        ctx,
      );
      assert.equal(crossProjectPrompt.isError, undefined, "an explicit cross-project prompt uses a satellite session");

      // Automatic capture targets the detected project and is never routed to a satellite.
      const passiveEvent = { toolName: "shell", result: "this eligible passive observation must not cross the persisted project boundary" };
      await eventHandlers.get("tool_execution_end")(passiveEvent, ctx);
      await eventHandlers.get("tool_execution_end")(passiveEvent, ctx);

      const sessionRegistrations = calls
        .filter((call) => call.method === "POST" && call.path === "/sessions")
        .map((call) => [call.body.id, call.body.project]);
      assert.deepEqual(sessionRegistrations, [
        [sessionId, "project-a"],
        [sessionId, "project-a"],
        [`${sessionId}@project-b`, "project-b"],
      ], "the runtime identity is never registered under project-b");
      assert.deepEqual(
        calls.filter((call) => call.method === "POST" && call.path === "/prompts").map((call) => [call.body.session_id, call.body.project]),
        [[sessionId, "project-a"], [sessionId, "project-a"], [`${sessionId}@project-b`, "project-b"]],
        "explicit foreign prompts are attributed to the satellite session",
      );
      assert.equal(
        calls.filter((call) => call.method === "POST" && call.path === "/observations/passive").length,
        0,
        "passive capture must be suppressed for the cross-project conflict",
      );
      assert.equal(warnings.length, 0, "UI conflicts must not write to stderr");
      assert.equal(notifications.length, 1, "repeated passive events must not repeat the same conflict warning");
      assert.equal(notifications[0][1], "warning");
      assert.match(notifications[0][0], /fresh Pi session/i);
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
    if (path === "/health") return new Response(JSON.stringify({ capabilities: { root_session_resume: true } }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: phase }));
    if (path === "/sessions") {
      if (phase === "generic-error") return new Response(JSON.stringify({ error: "registration unavailable" }), { status: 500 });
      if (body.project === "project-b" && body.ownership_mode === "project_owned") {
        return new Response(JSON.stringify({
          error: "session ownership does not match write project",
          code: "session_project_conflict",
          session_id: "resumed-runtime-session:resume:2",
          owner_project: "project-a",
          requested_project: "project-b",
        }), { status: 409 });
      }
      return new Response(JSON.stringify({ id: body.id, status: "created" }), { status: 201 });
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
      const entries = [];
      const { eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const ctx = runtimeContext(sessionId);
      ctx.sessionManager.getBranch = () => entries;
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

test("registered Pi-native mem_context forwards optional bounds and compact mode only when supplied", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/health", body: { status: "ok" } },
    { method: "GET", path: "/project/current", body: { project: "detected-project" } },
    { method: "GET", path: "/context", body: { context: "# Compact context\n- bounded observation" } },
  ]);
  globalThis.fetch = async (url, init) => {
    const response = await fetchStub(url, init);
    if (new URL(url).pathname !== "/context") return response;
    const query = new URL(url).searchParams;
    const context = query.get("compact") === "true"
      ? "# Compact context\n- bounded observation"
      : query.get("compact") === "false" ? "# Expanded context\n- explicit false" : "# Default context\n- compact omitted";
    return new Response(JSON.stringify({ context }), { headers: { "Content-Type": "application/json" } });
  };

  try {
    await withPluginSandbox("engram-pi-context-bounds-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox);
      const memContext = registeredTools.get("mem_context");
      assert.ok(memContext, "mem_context tool should be registered");
      const ctx = runtimeContext("context-bounds-session");
      const executeContext = (id, params) => memContext.execute(id, params, undefined, undefined, ctx);

      const explicit = await executeContext(
        "context-explicit-options",
        { project: "selected-project", scope: "global", max_bytes: 2048, compact: true },
      );
      const withoutCompact = await executeContext(
        "context-max-bytes-only",
        { project: "selected-project", scope: "project", max_bytes: 1024 },
      );
      const withoutMaxBytes = await executeContext(
        "context-compact-only",
        { project: "selected-project", scope: "personal", compact: false },
      );
      const omitted = await executeContext("context-no-options", { scope: "personal" });
      const oversized = await executeContext("context-oversized", { project: "selected-project", max_bytes: 1e20 });
      for (const result of [explicit, withoutCompact, withoutMaxBytes, omitted, oversized]) {
        assert.notEqual(result.isError, true, "mem_context should preserve successful HTTP responses");
      }

      const compactText = "# Compact context\n- bounded observation";
      assert.equal(explicit.content[0].text, compactText, "HTTP context must be visible to the model");
      assert.ok(Buffer.byteLength(explicit.content[0].text, "utf8") <= 2048, "compact fixture must fit the requested byte budget");
      assert.match(explicit.content[0].text, /^# Compact context\n- bounded observation$/);
      assert.equal(withoutCompact.content[0].text, "# Default context\n- compact omitted");
      assert.equal(withoutMaxBytes.content[0].text, "# Expanded context\n- explicit false");
      assert.equal(omitted.content[0].text, "# Default context\n- compact omitted");

      const contextCalls = calls.filter((call) => call.method === "GET" && call.path.startsWith("/context?"));
      assert.equal(contextCalls.length, 5, "each invocation must make one GET /context request");
      const queries = contextCalls.map((call) => new URL(`http://test${call.path}`).searchParams);

      assert.equal(queries[0].get("project"), "selected-project");
      assert.equal(queries[0].get("scope"), "global");
      assert.equal(queries[0].get("max_bytes"), "2048");
      assert.equal(queries[0].get("compact"), "true");

      assert.equal(queries[1].get("max_bytes"), "1024");
      assert.equal(queries[1].has("compact"), false, "omitted compact must not be sent");

      assert.equal(queries[2].has("max_bytes"), false, "omitted max_bytes must not be sent");
      assert.equal(queries[2].get("compact"), "false", "an explicit false must remain distinguishable from omission");

      assert.equal(queries[3].get("project"), "detected-project");
      assert.equal(queries[3].get("scope"), "personal");
      assert.equal(queries[3].has("max_bytes"), false, "omitted bounds must preserve the existing HTTP request");
      assert.equal(queries[3].has("compact"), false, "omitted compact mode must preserve the existing HTTP request");
      assert.equal(queries[4].get("max_bytes"), "100000000000000000000", "oversized budget must reach Go unchanged");

      const contextProperties = memContext.parameters.args[0];
      assert.equal(contextProperties.max_bytes.kind, "Optional", "max_bytes must be optional in the registered schema");
      assert.equal(contextProperties.max_bytes.args[0].kind, "Integer", "max_bytes must be an integer");
      assert.equal(contextProperties.max_bytes.args[0].args[0].minimum, 1, "max_bytes must be positive");
      assert.equal(contextProperties.compact.kind, "Optional", "compact must be optional in the registered schema");
      assert.equal(contextProperties.compact.args[0].kind, "Boolean", "compact must be a boolean");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi doctor forwards its resolved project while preserving global review and stats contracts", async () => {
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
      const diagnostic = await registeredTools.get("mem_doctor").execute("doctor", {}, undefined, undefined, ctx);
      assert.equal(diagnostic.details.data.status, "ok");
      assert.equal(diagnostic.details.data.caller_binding.status, "unknown");
      assert.equal(diagnostic.details.data.caller_binding.reason_code, "caller_binding_unavailable");
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
      return { ok: true, status: 201, async json() { return { id: JSON.parse(init.body).id, status: "created" }; } };
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
      return { ok: true, status: 201, async json() { return { id: JSON.parse(init.body).id, status: "created" }; } };
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
      return { ok: true, status: 201, async json() { return { id: JSON.parse(init.body).id, status: "created" }; } };
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

test("concurrent explicit projects cannot share an in-flight effective registration; the foreign one uses a satellite", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const gate = deferred();
  const registrations = [];
  const writes = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ status: "ok", capabilities: { isolated_session_registration: true } }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions") {
      registrations.push(JSON.parse(init.body));
      if (registrations.length === 1) await gate.promise;
      return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }), { status: 201 });
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
      assert.equal(b.isError, undefined, b.content?.[0]?.text);
      assert.deepEqual(writes.map(({ project, session_id }) => [project, session_id]).sort(), [
        ["project-a", "project-race"],
        ["project-b", "project-race@project-b"],
      ], "the second project must not inherit the first registration");
      assert.deepEqual(registrations.map(({ id, project }) => [id, project]), [
        ["project-race", "project-a"],
        ["project-race@project-b", "project-b"],
      ]);
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
      return { ok: true, status: 201, async json() { return { id: JSON.parse(init.body).id, status: "created" }; } };
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
      return { ok: true, status: 201, async json() { return { id: JSON.parse(init.body).id, status: "created" }; } };
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

test("separate plugin graphs converge on a server-selected continuation", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const entered = deferred();
  const release = deferred();
  let originals = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/health") return new Response(JSON.stringify({ capabilities: { root_session_resume: true } }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "shared" }));
    if (path === "/sessions" && body.id === "dual") {
      if (++originals === 2) entered.resolve();
      await release.promise;
      assert.equal(body.resume, true);
      return new Response(JSON.stringify({ id: "dual:resume:2", status: "created" }));
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id, status: "created" }));
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
      assert.ok(results.every((result) => result.isError === undefined));
      const id = "dual:resume:2";
      assert.ok(entries.every((entry) => entry.data.effectiveID === id));
      assert.equal(entries.length, 1, "concurrent graphs must persist only one pending mapping");
      assert.deepEqual(calls.filter((call) => call.path === "/sessions").map((call) => call.body.id), ["dual", "dual"]);
      assert.equal(calls.filter((call) => call.path === "/observations" && call.body.session_id === id).length, 2);
      const fork = runtimeContext("fork-dual");
      fork.sessionManager.getBranch = () => entries;
      await save(second, "shared");
      await second.registeredTools.get("mem_save").execute("fork", { title: "fork", content: "fork", project: "shared" }, undefined, undefined, fork);
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, "fork-dual");
      });
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("simultaneous foreign projects honor core continuation ownership", async () => {
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
    // Without a resolved owner, each explicit project may claim the runtime identity itself.
    if (path === "/project/current") return new Response(JSON.stringify({ project: "unknown", error_hint: "ambiguous project", available_projects: ["alpha", "beta"] }));
    if (path === "/sessions" && body.id === "foreign-dual") {
      if (++originals === 2) entered.resolve();
      await release.promise;
      return body.project === "alpha"
        ? new Response(JSON.stringify({ id: "foreign-dual:resume:2", status: "created" }))
        : new Response(JSON.stringify({ code: "session_project_conflict", session_id: body.id, owner_project: "alpha", requested_project: "beta" }), { status: 409 });
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id, status: "created" }));
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
      assert.equal(calls.filter((call) => call.path === "/sessions" && call.body.id !== "foreign-dual").length, 0);
      });
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("shutdown bounds project lookup for an owned pending replacement in a fresh graph", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeID = "shutdown-stalled-project";
  const effectiveID = `${runtimeID}:resume:owned`;
  const entries = [{ type: "custom", customType: "engram-effective-session", data: {
    runtimeID, effectiveID, pending: true, project: "owner",
  } }];
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path !== "/project/current") throw new Error(`Unexpected shutdown request: ${path}`);
    if (!init.signal) return null; // RED: the old lookup repeats five times without a shutdown signal.
    return new Promise((_, reject) => {
      if (init.signal.aborted) return reject(init.signal.reason);
      init.signal.addEventListener("abort", () => reject(init.signal.reason), { once: true });
    });
  };
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  try {
    await withPluginSandbox("engram-pi-shutdown-project-timeout-", async ({ sandbox }) => {
      // Only shorten the copied module's timeout; production constants and the checkout stay intact.
      const sourcePath = join(sandbox, "index.ts");
      const source = await readFile(sourcePath, "utf8");
      const shortened = source.replace("const ENGRAM_READ_TIMEOUT_MS = 10000;", "const ENGRAM_READ_TIMEOUT_MS = 30;");
      assert.notEqual(shortened, source, "the sandbox must retain the expected read timeout seam");
      await writeFile(sourcePath, shortened);
      const { eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(calls.filter((path) => path === "/project/current").length, 1,
        "shutdown must attempt project detection once, then stop at its deadline");
      assert.equal(calls.filter((path) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`).length, 0);
      assert.equal(entries.at(-1).data.pending, true, "unconfirmed ownership must remain retryable");
      await eventHandlers.get("session_shutdown")({ reason: "reload" }, ctx);
    });
  } finally {
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
    // Without a resolved owner, the explicit project may try to claim the runtime identity itself.
    if (path === "/project/current") return new Response(JSON.stringify({ project: "unknown", error_hint: "ambiguous project", available_projects: ["project-a", "project-b"] }));
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

test("root resume compatibility negotiates support and fails closed", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const cases = [
    { health: { version: "2.2.1" }, supported: false },
    { health: { version: "3.0.0" }, supported: true },
    { health: { version: "3.2.1" }, supported: true },
    { health: { version: "v3.0.1" }, supported: true },
    { health: { capabilities: { root_session_resume: true } }, supported: true },
    { health: { version: "3.0.0", capabilities: { root_session_resume: false } }, supported: false },
    { health: { version: "3.0.0", capabilities: { root_session_resume: "true" } }, supported: false },
    { health: { version: "3.0.0", capabilities: [] }, supported: false },
    { health: { version: "3.0.0-rc.1" }, supported: false },
    { health: { version: "3.invalid" }, supported: false },
    { health: {}, supported: false },
    { health: null, supported: false },
    { health: {}, healthStatus: 404, supported: false },
    { health: "not an object", supported: false },
    { health: { version: "3.0.0", capabilities: null }, supported: false },
  ];
  try {
    for (const { health, healthStatus = 200, supported } of cases) {
      for (const mode of ["root-ended", "mapped-ended", "fresh", "mapped-live"]) {
        const mapped = mode.startsWith("mapped-");
        const fresh = mode === "fresh";
        const activeMapping = mode === "mapped-live";
        const runtimeID = "compat-root";
        const persistedID = `${runtimeID}:resume:2`;
        const entries = mapped ? [{ type: "custom", customType: "engram-effective-session", data: {
          runtimeID, effectiveID: persistedID, pending: true, project: "compat-project",
        } }] : [];
        const calls = [];
        globalThis.fetch = async (url, init = {}) => {
          const path = new URL(url).pathname;
          const body = init.body ? JSON.parse(init.body) : undefined;
          calls.push({ path, body });
          if (path === "/health") return new Response(JSON.stringify(health), { status: healthStatus });
          if (path === "/project/current") return new Response(JSON.stringify({ project: "compat-project" }));
          if (path === "/sessions") {
            // Model an old core that ignores resume and rejects ended identities.
            if (!fresh && !activeMapping && (body.id !== runtimeID || !supported || !body.resume)) {
              return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
            }
            return new Response(JSON.stringify({ id: activeMapping ? persistedID : fresh ? runtimeID : `${runtimeID}:resume:3`, status: "created" }));
          }
          if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
          return new Response(JSON.stringify({ status: "ok" }));
        };
        const ctx = runtimeContext(runtimeID);
        ctx.sessionManager.getBranch = () => entries;
        await withPluginSandbox("engram-pi-root-compat-", async ({ sandbox }) => {
          const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
          const result = await registeredTools.get("mem_save").execute("compat", { title: "compat", content: "compat" }, undefined, undefined, ctx);
          assert.equal(!!result.isError, !supported && !fresh && !activeMapping, JSON.stringify({ health, mode, result }));
          if (!supported && !fresh && !activeMapping) {
            assert.match(JSON.stringify(result), /gentle-engram 0\.4\.1/);
            assert.match(JSON.stringify(result), /core .*root_session_resume.*3\.0\.0.*[Uu]pgrade/);
            assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
            assert.ok(calls.filter(({ path }) => path === "/sessions").every(({ body }) => !body.resume));
            assert.equal(entries.length, mapped ? 1 : 0);
          } else {
            assert.equal(calls.findLast(({ path }) => path === "/observations").body.session_id, activeMapping ? persistedID : fresh ? runtimeID : `${runtimeID}:resume:3`);
            if (!supported) assert.equal(calls.findLast(({ path }) => path === "/sessions").body.resume, false);
          }
        });
      }
    }
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("root resume compatibility pins retry support after lost acknowledgement", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  try {
    for (const [initial, next, mandatory] of [
      [true, false, false], [false, true, false], [true, null, false],
      [true, true, false], [false, false, false], [false, null, false],
      [true, false, true], [true, true, true],
    ]) {
      const runtimeID = "retry-compat";
      const mappedID = `${runtimeID}:resume:2`;
      const entries = mandatory ? [{ type: "custom", customType: "engram-effective-session", data: {
        runtimeID, effectiveID: mappedID, pending: true, project: "retry-project",
      } }] : [];
      const initialEntries = entries.length;
      const posts = [];
      const writes = [];
      let healthChecks = 0;
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        const body = init.body ? JSON.parse(init.body) : undefined;
        if (path === "/health") {
          const support = healthChecks++ === 0 ? initial : next;
          return new Response(support === null ? "unreadable" : JSON.stringify({ capabilities: { root_session_resume: support } }));
        }
        if (path === "/project/current") return new Response(JSON.stringify({ project: "retry-project" }));
        if (path === "/sessions") {
          if (body.id === mappedID) return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
          posts.push(body);
          // The core applied the first registration, but its successful body was lost.
          if (posts.length === 1) return new Response("truncated acknowledgement");
          return new Response(JSON.stringify({ id: initial ? `${runtimeID}:resume:3` : runtimeID, status: "created" }));
        }
        if (path === "/observations") {
          writes.push(body);
          return new Response(JSON.stringify({ id: 1, status: "saved" }));
        }
        return new Response(JSON.stringify({ status: "ok" }));
      };
      const ctx = runtimeContext(runtimeID);
      ctx.sessionManager.getBranch = () => entries;
      await withPluginSandbox("engram-pi-retry-compat-", async ({ sandbox }) => {
        const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
        const result = await registeredTools.get("mem_save").execute("retry-compat", { title: "retry", content: "retry" }, undefined, undefined, ctx);
        const changed = initial !== (next === true);
        assert.equal(!!result.isError, changed, JSON.stringify({ initial, next, mandatory, result }));
        assert.ok(healthChecks >= 2, "each dispatch attempt must recheck health (other probes may also run)");
        assert.deepEqual(posts.map(({ resume }) => resume), changed ? [initial] : [initial, initial]);
        if (changed) {
          assert.match(JSON.stringify(result), /resume support changed.*retry/i);
          assert.equal(writes.length, 0);
          assert.equal(entries.length, initialEntries, "lost acknowledgement must not adopt an identity");
        } else {
          assert.equal(writes.at(-1).session_id, initial ? `${runtimeID}:resume:3` : runtimeID);
        }
      });
    }
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("resumed quit adopts core numeric identities and reload retains the persisted ID", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const ended = new Set();
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/health") return new Response(JSON.stringify({ version: "3.0.0" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }), { status: 200 });
    if (path === "/sessions") {
      if (ended.has(body.id) && !body.resume) return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
      const id = ended.has(body.id) ? (ended.has("resumed:resume:2") ? "resumed:resume:3" : "resumed:resume:2") : body.id;
      return new Response(JSON.stringify({ id, status: "created" }), { status: 201 });
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }), { status: 201 });
    if (path.startsWith("/sessions/") && path.endsWith("/end")) ended.add(decodeURIComponent(path.slice(10, -4)));
    return new Response(JSON.stringify({ status: "ok" }), { status: 200 });
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
      assert.equal(identities.at(-1), "resumed");
      assert.equal(calls.filter((call) => call.path === "/sessions").at(-1).body.resume, true);
      const effectiveID = "resumed:resume:2";
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, effectiveID);
      for (let index = 0; index < 5; index++) {
        const renewed = await second.registeredTools.get("mem_save").execute(`renew-${index}`, { title: "renew", content: "renew" }, undefined, undefined, ctx);
        assert.equal(renewed.isError, undefined);
        await second.eventHandlers.get("tool_execution_end")({ toolName: "shell", result: "x".repeat(80) }, ctx);
      }
      assert.equal(entries.filter(({ customType, data }) => customType === "engram-effective-session" && data.effectiveID === effectiveID).length, 1,
        "repeated writes and hook renewals must not grow the mapping log");
      await second.eventHandlers.get("session_compact")({ summary: "resumed compaction summary" });
      const archive = calls.find((call) => call.path === "/observations" && call.body.type === "session_summary");
      assert.ok(archive, "resumed compaction must archive its summary");
      assert.equal(archive.body.session_id, effectiveID);
      const recovered = await second.eventHandlers.get("before_agent_start")({ systemPrompt: "base" }, ctx);
      assert.match(recovered.systemPrompt, /already saved/);
      assert.ok(entries.length);
      const third = await loadPluginHarness(sandbox, appendEntry);
      await third.eventHandlers.get("session_start")({ reason: "reload" }, ctx);
      await third.registeredTools.get("mem_save_prompt").execute("reload", { content: "after reload" }, undefined, undefined, ctx);
      assert.equal(calls.filter((call) => call.path === "/sessions").at(-1).body.id, effectiveID, "reload must re-register the persisted ID");
      assert.equal(calls.filter((call) => call.path === "/prompts").at(-1).body.session_id, effectiveID);
      const fork = runtimeContext("forked");
      fork.sessionManager.getBranch = () => entries;
      await third.registeredTools.get("mem_save").execute("fork", { title: "fork", content: "fork" }, undefined, undefined, fork);
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, "forked");
      await third.eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some((call) => call.path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
      const fourth = await loadPluginHarness(sandbox, appendEntry);
      await fourth.eventHandlers.get("session_start")({}, ctx);
      const afterSecondQuit = await fourth.registeredTools.get("mem_save").execute("third-save", { title: "third", content: "third" }, undefined, undefined, ctx);
      assert.equal(afterSecondQuit.isError, undefined, JSON.stringify(afterSecondQuit));
      const latestID = "resumed:resume:3";
      assert.equal(calls.filter((call) => call.path === "/sessions").at(-1).body.id, "resumed", "ended mapping falls back to the root");
      assert.equal(calls.filter((call) => call.path === "/observations").at(-1).body.session_id, latestID);
      assert.equal(entries.at(-1).data.effectiveID, latestID);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi-native explicit end targets the registered resumed identity, not the host ID", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const entries = [];
  const ctx = runtimeContext("explicit-resume");
  ctx.sessionManager.getBranch = () => entries;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method: init.method ?? "GET", path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions") return new Response(JSON.stringify({ id: "explicit-resume:resume:2", status: "created" }));
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ status: "ok" }));
  };
  try {
    await withPluginSandbox("engram-pi-explicit-resume-end-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = await registeredTools.get("mem_save").execute("save", { title: "resume", content: "resume" }, undefined, undefined, ctx);
      assert.equal(save.isError, undefined, JSON.stringify(save));
      const effectiveID = entries.at(-1).data.effectiveID;
      assert.equal(effectiveID, "explicit-resume:resume:2");
      const endTool = registeredTools.get("mem_session_end");
      for (const id of [effectiveID, "foreign", "", undefined]) {
        const refused = await endTool.execute("refused", { id }, undefined, undefined, ctx);
        assert.equal(refused.isError, true);
      }
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 0);
      const ended = await endTool.execute("host-end", { id: "explicit-resume" }, undefined, undefined, ctx);
      assert.equal(ended.isError, undefined, JSON.stringify(ended));
      assert.deepEqual(calls.filter(({ path }) => path.endsWith("/end")).map(({ path }) => path),
        [`/sessions/${encodeURIComponent(effectiveID)}/end`]);
      assert.equal(entries.at(-1).data.pending, false, "explicit end must clear its persisted reservation");
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 1, "shutdown must not repeat explicit end");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("ambiguous resumed explicit end leaves its pending reservation intact; JSON null success clears it", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const entries = [];
  const ctx = runtimeContext("ambiguous-end");
  const effectiveID = "ambiguous-end:resume:owned";
  ctx.sessionManager.getBranch = () => entries;
  entries.push({ type: "custom", customType: "engram-effective-session", data: {
    runtimeID: "ambiguous-end", effectiveID, pending: true, project: "resume-project",
  } });
  let endAttempts = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/project/current") return new Response(JSON.stringify({ project: "resume-project" }));
    if (path === "/sessions") return new Response(JSON.stringify({ id: effectiveID, status: "created" }));
    if (path === "/observations") return new Response(JSON.stringify({ id: 1 }));
    if (path === `/sessions/${encodeURIComponent(effectiveID)}/end`) {
      endAttempts++;
      if (endAttempts === 1) throw new Error("response lost after dispatch");
      return new Response("null", { status: 200 });
    }
    throw new Error(`unexpected request: ${path} ${init.method}`);
  };
  try {
    await withPluginSandbox("engram-pi-ambiguous-explicit-end-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = await registeredTools.get("mem_save").execute("save", { title: "owned", content: "owned" }, undefined, undefined, ctx);
      assert.equal(save.isError, undefined, JSON.stringify(save));
      const end = registeredTools.get("mem_session_end");
      const ambiguous = await end.execute("end-unknown", { id: "ambiguous-end" }, undefined, undefined, ctx);
      assert.equal(ambiguous.isError, true);
      assert.equal(entries.at(-1).data.pending, true, "unknown delivery must not clear the persisted marker");
      const renewed = await registeredTools.get("mem_save").execute("renew", { title: "owned", content: "owned" }, undefined, undefined, ctx);
      assert.equal(renewed.isError, undefined, JSON.stringify(renewed));
      const confirmed = await end.execute("end-confirmed", { id: "ambiguous-end" }, undefined, undefined, ctx);
      assert.equal(confirmed.isError, undefined, JSON.stringify(confirmed));
      assert.equal(entries.at(-1).data.pending, false, "HTTP JSON null is confirmed delivery");
      assert.equal(endAttempts, 2);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi-native explicit end in a new graph confirms prior pending ownership before closing", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeID = "prior-graph-end";
  const effectiveID = `${runtimeID}:resume:owned`;
  const entries = [{ type: "custom", customType: "engram-effective-session", data: {
    runtimeID, effectiveID, pending: true, project: "local-project",
  } }];
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  const calls = [];
  let conflict = false;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "local-project" }));
    if (path === "/sessions") return conflict
      ? new Response(JSON.stringify({ code: "session_project_conflict", session_id: effectiveID,
        owner_project: "foreign-project", requested_project: "local-project" }), { status: 409 })
      : new Response(JSON.stringify({ id: effectiveID, status: "created" }));
    if (path === `/sessions/${encodeURIComponent(effectiveID)}/end`) return new Response(JSON.stringify({ status: "ended" }));
    throw new Error(`unexpected request: ${path} ${init.method}`);
  };
  try {
    for (const shouldConflict of [true, false]) {
      conflict = shouldConflict;
      await withPluginSandbox("engram-pi-prior-graph-end-", async ({ sandbox }) => {
        const graph = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
        const before = calls.length;
        const result = await graph.registeredTools.get("mem_session_end").execute("end", { id: runtimeID }, undefined, undefined, ctx);
        assert.deepEqual(calls.slice(before).filter((path) => path === "/sessions"), ["/sessions"], "new graph must confirm server ownership");
        assert.equal(result.isError, shouldConflict ? true : undefined, JSON.stringify(result));
        assert.equal(calls.slice(before).filter((path) => path.endsWith("/end")).length, shouldConflict ? 0 : 1);
        assert.equal(entries.at(-1).data.pending, shouldConflict);
      });
    }
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi-native explicit end refuses a foreign-owned pending reservation", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { calls, fetchStub } = recordingFetch([
    { method: "GET", path: "/project/current", body: { project: "local-project" } },
  ]);
  globalThis.fetch = fetchStub;
  const ctx = runtimeContext("foreign-reservation");
  ctx.sessionManager.getBranch = () => [{ type: "custom", customType: "engram-effective-session",
    data: { runtimeID: "foreign-reservation", effectiveID: "foreign-reservation:resume:other", pending: true, project: "other-project" } }];
  try {
    await withPluginSandbox("engram-pi-foreign-explicit-end-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      await eventHandlers.get("session_start")({}, ctx);
      const result = await registeredTools.get("mem_session_end").execute("foreign-reservation-end",
        { id: "foreign-reservation" }, undefined, undefined, ctx);
      assert.equal(result.isError, true);
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 0);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("ambiguous legacy mapping registration reuses its persisted identity and ends it on shutdown", async () => {
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
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id, status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-ambiguous-resume-", async ({ sandbox }) => {
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "ambiguous", effectiveID: "ambiguous:resume:legacy-uuid", project: "resume-project", pending: true } }];
      const ctx = runtimeContext("ambiguous");
      ctx.sessionManager.getBranch = () => entries;
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = () => registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx);
      assert.equal((await save()).isError, true);
      const freshID = calls.filter(({ path }) => path === "/sessions").at(-1).body.id;
      assert.equal(freshID, "ambiguous:resume:legacy-uuid");
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      assert.equal((await save()).isError, true);
      assert.deepEqual([...attempts.keys()], [freshID], "retry must use the persisted legacy ID");
      assert.equal((await save()).isError, undefined);
      assert.equal(calls.filter(({ path }) => path === "/observations").at(-1).body.session_id, freshID);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(freshID)}/end`));
      assert.equal(calls.some(({ path }) => path === "/sessions/ambiguous/end"), false);
    });
    await withPluginSandbox("engram-pi-ambiguous-end-", async ({ sandbox }) => {
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "ambiguous", effectiveID: "ambiguous:resume:unconfirmed-uuid", project: "resume-project", pending: true } }];
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

test("a legacy pending mapping cannot be claimed by another project after pre-dispatch failure", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  let failedReplacementAttempts = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/health") return new Response(JSON.stringify({ status: "ok", capabilities: { isolated_session_registration: true } }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions" && body.id === "reserved") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions" && failedReplacementAttempts < 2) {
      failedReplacementAttempts++;
      const failure = new Error("request did not reach server");
      failure.code = "ENETUNREACH";
      throw failure;
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id, status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-reserved-project-", async ({ sandbox }) => {
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "reserved", effectiveID: "reserved:resume:legacy-uuid", project: "project-a", pending: true } }];
      const ctx = runtimeContext("reserved");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, appendEntry);
      const save = (project) => registeredTools.get("mem_save").execute(project, { title: project, content: project, project }, undefined, undefined, ctx);
      assert.equal((await save("project-a")).isError, true);
      const effectiveID = entries.at(-1).data.effectiveID;
      const registrationCount = calls.filter(({ path }) => path === "/sessions").length;
      assert.equal((await save("project-b")).isError, undefined, "B saves through its own satellite session");
      assert.deepEqual(calls.filter(({ path }) => path === "/sessions").slice(registrationCount).map(({ body }) => body.id), ["reserved@project-b"],
        "B must not POST the reserved ID");
      assert.equal((await save("project-a")).isError, undefined);
      assert.deepEqual(calls.filter(({ path }) => path === "/observations").map(({ body }) => body.session_id), ["reserved@project-b", effectiveID]);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("two module graphs coalesce concurrent pending replacement shutdown", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const entered = deferred();
  const release = deferred();
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "owner" }));
    if (path === "/sessions") return new Response(JSON.stringify({ id: body.id === "parallel" ? "parallel:resume:2" : body.id, status: "created" }));
    if (path.endsWith("/end")) { entered.resolve(); await release.promise; }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ status: "ok" }));
  };
  const entries = [];
  const secondBranchRead = deferred();
  let watchingSecond = false;
  const ctx = runtimeContext("parallel");
  ctx.sessionManager.getBranch = () => {
    if (watchingSecond) secondBranchRead.resolve();
    return entries;
  };
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-parallel-a-", async ({ sandbox }) => {
      await withPluginSandbox("engram-pi-parallel-b-", async ({ sandbox: peer }) => {
        const a = await loadPluginHarness(sandbox, append);
        const b = await loadPluginHarness(peer, append);
        assert.equal((await a.registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx)).isError, undefined);
        const id = entries.at(-1).data.effectiveID;
        await b.eventHandlers.get("session_start")({}, ctx);
        const first = a.eventHandlers.get("session_shutdown")({}, ctx);
        await waitFor(entered.promise, "first end did not start");
        watchingSecond = true;
        const second = b.eventHandlers.get("session_shutdown")({}, ctx);
        await waitFor(secondBranchRead.promise, "second graph did not read the shared branch before release");
        watchingSecond = false;
        assert.equal(calls.filter(({ path }) => path === `/sessions/${encodeURIComponent(id)}/end`).length, 1);
        release.resolve();
        await waitFor(Promise.all([first, second]), "shutdowns did not settle");
        assert.equal(calls.filter(({ path }) => path === `/sessions/${encodeURIComponent(id)}/end`).length, 1);
        assert.equal(entries.filter(({ customType, data }) => customType === "engram-effective-session" && data.effectiveID === id && data.pending === false).length, 1);
      });
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("a peer graph cannot revive a closed replacement without explicit session_start", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  const entered = deferred();
  const release = deferred();
  let pausePeer = false;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "owner" }));
    if (pausePeer && path === "/sessions") { entered.resolve(); await release.promise; }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id === "closed-peer" ? "closed-peer:resume:2" : body?.id, status: "created" }));
  };
  const entries = [];
  const ctx = runtimeContext("closed-peer");
  ctx.sessionManager.getBranch = () => entries;
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-closed-a-", async ({ sandbox }) => {
      await withPluginSandbox("engram-pi-closed-b-", async ({ sandbox: peer }) => {
        const a = await loadPluginHarness(sandbox, append);
        const b = await loadPluginHarness(peer, append);
        assert.equal((await a.registeredTools.get("mem_save").execute("save", { title: "initial", content: "initial" }, undefined, undefined, ctx)).isError, undefined);
        const id = entries.at(-1).data.effectiveID;
        await a.eventHandlers.get("session_shutdown")({}, ctx);
        assert.equal(entries.at(-1).data.pending, false);
        const before = calls.length;
        const count = entries.length;
        const rejected = await b.registeredTools.get("mem_save").execute("save", { title: "late", content: "late" }, undefined, undefined, ctx);
        assert.equal(rejected.isError, true);
        assert.equal(entries.length, count, "closed conversation must not reserve another identity");
        assert.equal(calls.slice(before).filter(({ path }) => path !== "/project/current").length, 0, "closed conversation must not issue any write");
        await b.eventHandlers.get("session_start")({}, ctx);
        pausePeer = true;
        const inFlight = b.registeredTools.get("mem_capture_passive").execute("capture", { content: "## Key Learnings\nPaused passive registration", source: "race" }, undefined, undefined, ctx);
        await waitFor(entered.promise, "peer registration did not reach server");
        const shutdown = a.eventHandlers.get("session_shutdown")({}, ctx);
        release.resolve();
        const [pausedResult] = await Promise.all([inFlight, shutdown]);
        assert.equal(pausedResult.isError, true, "passive registration paused across shutdown cannot dispatch an observation");
        assert.equal(calls.filter(({ path }) => path === "/observations/passive").length, 0);
        const after = calls.length;
        const afterEntries = entries.length;
        const late = await b.registeredTools.get("mem_save").execute("save", { title: "after", content: "after" }, undefined, undefined, ctx);
        assert.equal(late.isError, true);
        assert.equal(entries.length, afterEntries);
        assert.equal(calls.slice(after).filter(({ path }) => path !== "/project/current").length, 0);
        await b.eventHandlers.get("session_start")({}, ctx);
        assert.equal((await b.registeredTools.get("mem_save").execute("save", { title: "resumed", content: "resumed" }, undefined, undefined, ctx)).isError, undefined);
        assert.ok(calls.slice(after).some(({ path }) => path === "/observations"), "explicit resume permits writes again");
      });
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("passive capture does not dispatch after a peer closes during body construction", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "owner" }));
    return new Response(JSON.stringify({ id: body?.id === "passive-close" ? "passive-close:resume:2" : body?.id, status: "created" }));
  };
  const entries = [];
  const ctx = runtimeContext("passive-close");
  ctx.sessionManager.getBranch = () => entries;
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-passive-a-", async ({ sandbox }) => {
      await withPluginSandbox("engram-pi-passive-b-", async ({ sandbox: peer }) => {
        const a = await loadPluginHarness(sandbox, append);
        const b = await loadPluginHarness(peer, append);
        let shutdown;
        const params = { content: "## Key Learnings\nA passive capture that must not dispatch" };
        Object.defineProperty(params, "source", { get() { shutdown = a.eventHandlers.get("session_shutdown")({}, ctx); return "race"; } });
        const result = await b.registeredTools.get("mem_capture_passive").execute("capture", params, undefined, undefined, ctx);
        await shutdown;
        assert.equal(result.isError, true);
        assert.equal(calls.filter((path) => path === "/observations/passive").length, 0);
        await b.eventHandlers.get("session_start")({}, ctx);
        let hookShutdown;
        const hookResult = { toJSON() { hookShutdown = a.eventHandlers.get("session_shutdown")({}, ctx); return { content: "x".repeat(80) }; } };
        await b.eventHandlers.get("tool_execution_end")({ toolName: "shell", result: hookResult }, ctx);
        await hookShutdown;
        assert.equal(calls.filter((path) => path === "/observations/passive").length, 0, "hook must not dispatch after peer shutdown during serialization");
      });
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("concurrent uncertain replacement end stays pending and a fresh graph retries", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const entered = deferred();
  const release = deferred();
  const calls = [];
  const runtimeID = "parallel-uncertain";
  const effectiveID = `${runtimeID}:resume:reserved`;
  globalThis.fetch = async (url) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "owner" }));
    if (path === `/sessions/${encodeURIComponent(effectiveID)}/end`) {
      if (calls.filter((requested) => requested === path).length === 1) {
        entered.resolve();
        await release.promise;
        return new Response("null");
      }
    }
    return new Response(JSON.stringify({ status: "created" }));
  };
  const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID, project: "owner", pending: true } }];
  const secondBranchRead = deferred();
  let watchingSecond = false;
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => {
    if (watchingSecond) secondBranchRead.resolve();
    return entries;
  };
  const append = (customType, data) => entries.push({ type: "custom", customType, data });
  try {
    await withPluginSandbox("engram-pi-uncertain-parallel-a-", async ({ sandbox }) => {
      await withPluginSandbox("engram-pi-uncertain-parallel-b-", async ({ sandbox: peer }) => {
        const a = await loadPluginHarness(sandbox, append);
        const b = await loadPluginHarness(peer, append);
        await Promise.all([a.eventHandlers.get("session_start")({}, ctx), b.eventHandlers.get("session_start")({}, ctx)]);
        const first = a.eventHandlers.get("session_shutdown")({}, ctx);
        await waitFor(entered.promise, "first uncertain end did not start");
        watchingSecond = true;
        const second = b.eventHandlers.get("session_shutdown")({}, ctx);
        await waitFor(secondBranchRead.promise, "second graph did not read the branch before release");
        watchingSecond = false;
        release.resolve();
        await waitFor(Promise.all([first, second]), "uncertain shutdowns did not settle");
        assert.equal(calls.filter((path) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`).length, 1);
        assert.equal(entries.at(-1).data.pending, true);
        assert.equal(entries.filter(({ data }) => data.pending === false).length, 0);
        await withPluginSandbox("engram-pi-uncertain-parallel-next-", async ({ sandbox: nextSandbox }) => {
          const next = await loadPluginHarness(nextSandbox, append);
          await next.eventHandlers.get("session_shutdown")({}, ctx);
        });
        assert.equal(calls.filter((path) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`).length, 2);
        assert.equal(entries.filter(({ data }) => data.pending === false).length, 1);
      });
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("foreign resolved graph cannot end an explicitly owned pending replacement", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "foreign" }));
    return new Response(JSON.stringify({ status: "created" }));
  };
  const runtimeID = "foreign-owned";
  const effectiveID = `${runtimeID}:resume:owner`;
  const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID, project: "owner", pending: true } }];
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  try {
    await withPluginSandbox("engram-pi-foreign-owned-", async ({ sandbox }) => {
      const graph = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      await graph.eventHandlers.get("session_start")({}, ctx);
      await graph.eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(calls.filter((path) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`).length, 0);
      assert.equal(entries.at(-1).data.pending, true);
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
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id === "repeat-end" ? "repeat-end:resume:2" : body?.id, status: "created" }));
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
    if (path.endsWith("/end") && calls.filter(({ path: requested }) => requested.endsWith("/end")).length === 1) throw new Error("end response lost");
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id === "uncertain-end" ? "uncertain-end:resume:2" : body?.id, status: "created" }));
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
    if (path === "/health") return new Response(JSON.stringify({ status: "ok", capabilities: { isolated_session_registration: true } }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "project-a" }));
    if (path === "/sessions" && body.id === "owner-race") return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
    if (path === "/sessions" && body.project === "project-a") {
      started.resolve();
      await gate.promise;
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: body?.id, status: "created" }));
  };
  try {
    await withPluginSandbox("engram-pi-owner-race-", async ({ sandbox }) => {
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "owner-race", effectiveID: "owner-race:resume:legacy-uuid", project: "project-a", pending: true } }];
      const ctx = runtimeContext("owner-race");
      ctx.sessionManager.getBranch = () => entries;
      const appendEntry = (customType, data) => entries.push({ type: "custom", customType, data });
      const { registeredTools } = await loadPluginHarness(sandbox, appendEntry);
      const save = registeredTools.get("mem_save");
      const owner = save.execute("a", { title: "a", content: "a", project: "project-a" }, undefined, undefined, ctx);
      await started.promise;
      const effectiveID = entries.at(-1).data.effectiveID;
      const foreign = await save.execute("b", { title: "b", content: "b", project: "project-b" }, undefined, undefined, ctx);
      assert.equal(foreign.isError, undefined, "the foreign caller writes through its own satellite session");
      assert.deepEqual(calls.filter(({ path }) => path === "/observations").map(({ body }) => body.session_id), ["owner-race@project-b"]);
      gate.resolve();
      assert.equal((await owner).isError, undefined);
      assert.deepEqual(calls.filter(({ path }) => path === "/observations").map(({ body }) => body.session_id), ["owner-race@project-b", effectiveID]);
      await withPluginSandbox("engram-pi-owner-race-next-", async ({ sandbox: nextSandbox }) => {
        const next = await loadPluginHarness(nextSandbox, appendEntry);
        await next.eventHandlers.get("session_shutdown")({}, ctx);
      });
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
      assert.ok(calls.filter(({ path }) => path === "/sessions").every(({ body }) => body.project === "project-a" || body.id === "owner-race"
        || (body.id === "owner-race@project-b" && body.project === "project-b")));
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
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "late-conflict", effectiveID: "late-conflict:resume:legacy-uuid", project: "resume-project", pending: true } }];
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
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "uncertain-reload", effectiveID: "uncertain-reload:resume:legacy-uuid", project: "resume-project", pending: true } }];
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
    if (path === "/sessions") {
      started.resolve();
      await gate.promise;
      return new Response(JSON.stringify({ id: "overlap:resume:2", status: "created" }));
    }
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ status: "ok" }));
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
      assert.equal(calls.filter(({ path }) => path === "/sessions").length, 1, "both callers share the pending resume registration");
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

test("fallback from ended mapping to live root supersedes identity for writes and cleanup", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  try {
    for (const mode of ["shutdown", "explicit", "readonly"]) {
      const runtimeID = `fallback-${mode}`;
      const legacyID = `${runtimeID}:resume:legacy-uuid`;
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID: legacyID, project: "pi", pending: true } }];
      const calls = [];
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        const body = init.body ? JSON.parse(init.body) : undefined;
        calls.push({ path, body });
        if (path === "/health") return new Response(JSON.stringify({ version: "3.0.0" }));
        if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
        if (path === "/sessions") return body.id === legacyID
          ? new Response(JSON.stringify({ code: "session_already_ended", session_id: legacyID }), { status: 409 })
          : new Response(JSON.stringify({ id: runtimeID, status: "created" }));
        if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
        return new Response(JSON.stringify({ id: 1, status: "ended" }));
      };
      await withPluginSandbox("engram-pi-fallback-root-", async ({ sandbox }) => {
        const append = mode === "readonly" ? undefined : (customType, data) => entries.push({ type: "custom", customType, data });
        const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, append);
        const ctx = runtimeContext(runtimeID);
        ctx.sessionManager.getBranch = () => entries;
        const saved = await registeredTools.get("mem_save").execute("save", { title: "root", content: "root" }, undefined, undefined, ctx);
        if (mode === "readonly") {
          assert.equal(saved.isError, true, "changed root mapping cannot be adopted without append support");
          assert.match(saved.content[0].text, /Cannot persist/);
          assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
          assert.equal(entries.length, 1);
          return;
        }
        assert.equal(saved.isError, undefined, JSON.stringify(saved));
        assert.equal(calls.find(({ path }) => path === "/observations").body.session_id, runtimeID);
        assert.equal(entries.at(-1).data.effectiveID, runtimeID, "root acknowledgement supersedes ended mapping");
        if (mode === "explicit") {
          const ended = await registeredTools.get("mem_session_end").execute("end", { id: runtimeID }, undefined, undefined, ctx);
          assert.equal(ended.isError, undefined, JSON.stringify(ended));
        }
        await eventHandlers.get("session_shutdown")({}, ctx);
        await eventHandlers.get("session_shutdown")({}, ctx);
        assert.deepEqual(calls.filter(({ path }) => path.endsWith("/end")).map(({ path }) => path), [`/sessions/${runtimeID}/end`]);
      });
    }
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("read-only legacy shutdown suppresses confirmed delivery but retries uncertain delivery", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  try {
    for (const uncertain of [false, true]) {
      const runtimeID = `readonly-shutdown-${uncertain}`;
      const effectiveID = `${runtimeID}:resume:legacy-uuid`;
      const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID, project: "pi", pending: true } }];
      let ends = 0;
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
        if (path === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
        if (path.endsWith("/end")) {
          ends++;
          if (uncertain && ends === 1) throw new Error("end acknowledgement lost");
        }
        if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
        return new Response(JSON.stringify({ status: "ended", id: 1 }));
      };
      await withPluginSandbox("engram-pi-readonly-shutdown-", async ({ sandbox }) => {
        const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
        const ctx = runtimeContext(runtimeID);
        ctx.sessionManager.getBranch = () => entries;
        assert.equal((await registeredTools.get("mem_save").execute("save", { title: "legacy", content: "legacy" }, undefined, undefined, ctx)).isError, undefined);
        for (let index = 0; index < 3; index++) await eventHandlers.get("session_shutdown")({}, ctx);
        assert.equal(ends, uncertain ? 2 : 1);
        assert.equal(entries.at(-1).data.pending, true, "read-only log cannot clear its marker");
      });
    }
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("hosts without mapping persistence never request core resume", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const registrations = [];
  let children = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") {
      registrations.push(body);
      if (body.id === "ended-no-persistence") {
        if (!body.resume) return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
        children++;
        return new Response(JSON.stringify({ id: `${body.id}:resume:2`, status: "created" }));
      }
      return new Response(JSON.stringify({ id: body.id, status: "created" }));
    }
    return new Response(JSON.stringify({ id: 1, status: "saved" }));
  };
  try {
    for (const missing of ["appendEntry", "getBranch"]) {
      await withPluginSandbox("engram-pi-no-persistence-", async ({ sandbox }) => {
        const { registeredTools } = await loadPluginHarness(sandbox, missing === "appendEntry" ? undefined : () => {});
        const endedCtx = runtimeContext("ended-no-persistence");
        const liveCtx = runtimeContext("live-no-persistence");
        if (missing !== "getBranch") for (const ctx of [endedCtx, liveCtx]) ctx.sessionManager.getBranch = () => [];
        const save = (ctx) => registeredTools.get("mem_save").execute("no-persistence", { title: "save", content: "save" }, undefined, undefined, ctx);
        const ended = await save(endedCtx);
        assert.equal(ended.isError, true);
        assert.equal(ended.details.data?.code, "session_already_ended");
        assert.equal((await save(liveCtx)).isError, undefined);
      });
    }
    assert.ok(registrations.every(({ resume }) => resume === false));
    assert.equal(children, 0, "core must never create an untrackable continuation");
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("session registration trace is opt-in, metadata-only, and preserves resume behavior", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  const originalTrace = process.env.ENGRAM_PI_SESSION_TRACE;
  const originalWrite = process.stderr.write;
  const originalDescriptorWrite = fs.writeSync;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  try {
    for (const scenario of ["disabled", "appendEntry", "getBranch", "resume", "retry"]) {
      const enabled = scenario !== "disabled";
      if (enabled) process.env.ENGRAM_PI_SESSION_TRACE = "1";
      else delete process.env.ENGRAM_PI_SESSION_TRACE;
      const stderr = [];
      const registrations = [];
      const writes = [];
      const entries = [];
      const runtimeID = `private-runtime-${scenario}`;
      const effectiveID = `${runtimeID}:resume:2`;
      const canPersist = !["appendEntry", "getBranch"].includes(scenario);
      process.stderr.write = (chunk) => { stderr.push(String(chunk)); return true; };
      fs.writeSync = (fd, ...args) => {
        if (fd !== 2) return originalDescriptorWrite(fd, ...args);
        stderr.push(String(args[0])); return Buffer.byteLength(String(args[0]));
      };
      syncBuiltinESMExports();
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        const body = init.body ? JSON.parse(init.body) : undefined;
        if (path === "/project/current") return new Response('{"project":"private-project"}');
        if (path === "/health") return new Response('{"version":"3.0.0","capabilities":{"root_session_resume":true}}');
        if (path === "/sessions") {
          registrations.push(body);
          if (scenario === "retry" && registrations.length === 1) throw new Error("private transport detail");
          if (!body.resume) return new Response(JSON.stringify({ code: "session_already_ended", error: "session has already ended", session_id: runtimeID }), { status: 409 });
          return new Response(JSON.stringify({ id: effectiveID, status: "created" }));
        }
        if (path === "/observations") writes.push(body);
        return new Response('{"id":1}');
      };
      await withPluginSandbox("engram-pi-session-trace-", async ({ sandbox }) => {
        const append = scenario === "appendEntry" ? undefined : (customType, data) => entries.push({ type: "custom", customType, data });
        const { registeredTools } = await loadPluginHarness(sandbox, append);
        const ctx = runtimeContext(runtimeID);
        if (scenario !== "getBranch") ctx.sessionManager.getBranch = () => entries;
        const result = await registeredTools.get("mem_save").execute("trace", { title: "private title", content: "private content" }, undefined, undefined, ctx);
        assert.equal(result.isError, canPersist ? undefined : true);
        if (!canPersist) assert.equal(result.details.data?.code, "session_already_ended");
        assert.equal(registrations.length, scenario === "retry" ? 2 : 1);
        assert.ok(registrations.every((body) => body.resume === canPersist));
        assert.equal(writes.length, canPersist ? 1 : 0);
        if (canPersist) assert.equal(writes[0].session_id, effectiveID);
        assert.equal(entries.length, canPersist ? 1 : 0);
        const lines = stderr.filter((line) => line.startsWith("[engram:session-trace] "));
        if (!enabled) assert.deepEqual(lines, []);
        else {
          const events = lines.map((line) => JSON.parse(line.slice("[engram:session-trace] ".length)));
          assert.deepEqual(events, [
            { stage: "context", append_entry: scenario !== "appendEntry", get_branch: scenario !== "getBranch", persisted: "root" },
            ...registrations.map(() => ({ stage: "dispatch", requested: "root", resume: canPersist })),
            canPersist ? { stage: "acknowledgement", effective: "continuation" }
              : { stage: "rejection", http_status: 409, reason: "session_already_ended" },
            ...(canPersist ? [{ stage: "adoption", effective: "continuation" }] : []),
          ]);
          assert.doesNotMatch(lines.join(""), /private|\/|session_id|title|content/);
        }
      });
    }
  } finally {
    globalThis.fetch = originalFetch;
    process.stderr.write = originalWrite;
    fs.writeSync = originalDescriptorWrite;
    syncBuiltinESMExports();
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
    if (originalTrace === undefined) delete process.env.ENGRAM_PI_SESSION_TRACE; else process.env.ENGRAM_PI_SESSION_TRACE = originalTrace;
  }
});

test("session lifecycle trace is private, opt-in, and preserves shutdown behavior", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  const originalTrace = process.env.ENGRAM_PI_SESSION_TRACE;
  const originalWrite = process.stderr.write;
  const originalDescriptorWrite = fs.writeSync;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  try {
    for (const mode of ["enabled", "disabled", "broken-stderr"]) {
      process.env.ENGRAM_PI_SESSION_TRACE = mode === "disabled" ? "0" : "1";
      const stderr = [];
      const calls = [];
      process.stderr.write = (chunk) => {
        if (mode === "broken-stderr") throw new Error("private stderr failure");
        stderr.push(String(chunk)); return true;
      };
      fs.writeSync = (fd, ...args) => {
        if (fd !== 2) return originalDescriptorWrite(fd, ...args);
        if (mode === "broken-stderr") throw new Error("private stderr failure");
        stderr.push(String(args[0])); return Buffer.byteLength(String(args[0]));
      };
      syncBuiltinESMExports();
      globalThis.fetch = async (url, init = {}) => {
        const path = new URL(url).pathname;
        calls.push(path);
        if (path === "/project/current") return new Response('{"project":"pi"}');
        if (path === "/health") return new Response('{"version":"3.0.0"}');
        if (path === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
        if (path === "/observations") return new Response('{"id":1,"status":"saved"}');
        return new Response('{"id":1,"status":"ended"}');
      };
      await withPluginSandbox("engram-pi-lifecycle-trace-", async ({ sandbox }) => {
        const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
        const start = eventHandlers.get("session_start");
        const shutdown = eventHandlers.get("session_shutdown");
        const alpha = runtimeContext("private-alpha");
        const beta = runtimeContext("private-beta");
        const save = async (ctx) => {
          const result = await registeredTools.get("mem_save").execute("trace", { title: "private title", content: "private content" }, undefined, undefined, ctx);
          assert.equal(result.isError, undefined);
        };
        await start({}, alpha); await save(alpha);
        await shutdown({ reason: "reload" }, alpha);
        assert.equal(calls.filter((path) => path.endsWith("/end")).length, 0);
        await start({}, alpha); await save(alpha);
        await shutdown({ reason: "private arbitrary reason" }, alpha);
        assert.equal(calls.filter((path) => path.endsWith("/end")).length, 1);
        await start({}, beta); await save(beta);
        await shutdown({}, beta);
        await shutdown({}, runtimeContext(""));
        await shutdown({ reason: "reload" }, runtimeContext(() => { throw new Error("private identity failure"); }));
        assert.equal(calls.filter((path) => path === "/observations").length, 3);
        assert.equal(calls.filter((path) => path.endsWith("/end")).length, 2);
        const events = stderr.filter((line) => line.startsWith("[engram:session-trace] "))
          .map((line) => JSON.parse(line.slice("[engram:session-trace] ".length)));
        assert.deepEqual(events.filter(({ stage }) => stage === "lifecycle"), mode === "enabled" ? [
          { stage: "lifecycle", event: "session_start", identity: "first" },
          { stage: "lifecycle", event: "session_shutdown", reason: "reload", identity: "same" },
          { stage: "lifecycle", event: "session_start", identity: "same" },
          { stage: "lifecycle", event: "session_shutdown", reason: "other", identity: "same" },
          { stage: "lifecycle", event: "session_start", identity: "different" },
          { stage: "lifecycle", event: "session_shutdown", reason: "missing", identity: "same" },
          { stage: "lifecycle", event: "session_shutdown", reason: "missing", identity: "missing" },
          { stage: "lifecycle", event: "session_shutdown", reason: "reload", identity: "missing" },
        ] : []);
        assert.doesNotMatch(stderr.join(""), /private|arbitrary|\/|title|content/);
      });
    }
  } finally {
    globalThis.fetch = originalFetch; process.stderr.write = originalWrite;
    fs.writeSync = originalDescriptorWrite;
    syncBuiltinESMExports();
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
    if (originalTrace === undefined) delete process.env.ENGRAM_PI_SESSION_TRACE; else process.env.ENGRAM_PI_SESSION_TRACE = originalTrace;
  }
});

test("session trace survives a closed stderr pipe without changing host error handling", async () => {
  for (const hostHandler of [false, true]) {
    await withPluginSandbox("engram-pi-closed-trace-pipe-", async ({ sandbox }) => {
      const script = `
        import { importPluginFromSandbox } from ${JSON.stringify(new URL("./plugin-sandbox.mjs", import.meta.url).href)};
        const handlers = new Map();
        const register = await importPluginFromSandbox(${JSON.stringify(sandbox)});
        register({ registerTool() {}, on(event, handler) { handlers.set(event, handler); } });
        let hostErrors = 0;
        if (${hostHandler}) process.stderr.on('error', () => { hostErrors++; });
        const listeners = process.stderr.listenerCount('error');
        let requests = 0;
        globalThis.fetch = async () => { requests++; throw new Error('unexpected request'); };
        process.stdout.write('ready\\n');
        await new Promise(resolve => process.stdin.once('data', resolve));
        await handlers.get('session_shutdown')({ reason: 'reload' }, {
          sessionManager: { getSessionId: () => 'synthetic-session' },
        });
        process.stdin.pause();
        await new Promise(resolve => setImmediate(resolve));
        process.stdout.write(JSON.stringify({ listeners, remaining: process.stderr.listenerCount('error'), hostErrors, requests }) + '\\n');
      `;
      const child = spawn(process.execPath, ["--input-type=module", "-e", script], {
        env: { ...process.env, ENGRAM_PI_SESSION_TRACE: "1" },
        stdio: ["pipe", "pipe", "pipe"],
      });
      const ready = deferred();
      const closed = deferred();
      const exited = deferred();
      const output = [];
      const lines = createInterface({ input: child.stdout });
      lines.on("line", (line) => { output.push(line); if (line === "ready") ready.resolve(); });
      child.stderr.once("close", () => closed.resolve());
      child.once("exit", (code, signal) => exited.resolve({ code, signal }));
      child.once("error", (error) => exited.resolve({ error }));
      try {
        await waitFor(ready.promise, "child did not prepare the trace");
        child.stderr.destroy();
        await waitFor(closed.promise, "stderr reader did not close");
        child.stdin.end("trace\n");
        const result = await waitFor(exited.promise, "child did not exit after the trace");
        assert.deepEqual(result, { code: 0, signal: null });
        assert.deepEqual(output, ["ready", JSON.stringify({ listeners: hostHandler ? 1 : 0, remaining: hostHandler ? 1 : 0, hostErrors: 0, requests: 0 })]);
      } finally {
        child.kill();
        lines.close();
      }
    });
  }
});

test("persisted legacy continuation renews without appendEntry", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const effectiveID = "legacy-read-only:resume:bb1fd7b6-4816-4b42-b40c-81902893f955";
  const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "legacy-read-only", effectiveID, pending: true, project: "pi" } }];
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") return new Response(JSON.stringify({ id: body.id, status: "created" }));
    return new Response(JSON.stringify({ id: 1, status: "saved" }));
  };
  try {
    await withPluginSandbox("engram-pi-read-only-mapping-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("legacy-read-only");
      ctx.sessionManager.getBranch = () => entries;
      const result = await registeredTools.get("mem_save").execute("legacy", { title: "save", content: "save" }, undefined, undefined, ctx);
      assert.equal(result.isError, undefined, JSON.stringify(result));
      assert.equal(calls.find(({ path }) => path === "/observations").body.session_id, effectiveID);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.some(({ path }) => path === `/sessions/${encodeURIComponent(effectiveID)}/end`));
      assert.equal(entries.length, 1);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("explicit end overlapping core resume awaits and ends the acknowledged continuation", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const started = deferred();
  const release = deferred();
  const calls = [];
  const entries = [];
  const effectiveID = "explicit-overlap:resume:2";
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") { started.resolve(); await release.promise; return new Response(JSON.stringify({ id: effectiveID, status: "created" })); }
    return new Response(JSON.stringify({ id: 1, status: "ended" }));
  };
  try {
    await withPluginSandbox("engram-pi-explicit-overlap-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const ctx = runtimeContext("explicit-overlap");
      ctx.sessionManager.getBranch = () => entries;
      const save = registeredTools.get("mem_save").execute("save", { title: "save", content: "save" }, undefined, undefined, ctx);
      await waitFor(started.promise, "resume registration stalled");
      const end = registeredTools.get("mem_session_end").execute("end", { id: "explicit-overlap" }, undefined, undefined, ctx);
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(calls.filter((path) => path.endsWith("/end")).length, 0);
      release.resolve();
      const [, ended] = await Promise.all([save, end]);
      assert.equal(ended.isError, undefined, JSON.stringify(ended));
      assert.deepEqual(calls.filter((path) => path.endsWith("/end")), [`/sessions/${encodeURIComponent(effectiveID)}/end`]);
      assert.equal(entries.at(-1).data.pending, false);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(calls.filter((path) => path.endsWith("/end")).length, 1);
    });
  } finally {
    release.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("malformed persisted mapping never authorizes foreign writes or cleanup", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID: "root", effectiveID: "foreign", project: "pi", pending: true } }];
  const ctx = runtimeContext("root");
  ctx.sessionManager.getBranch = () => entries;
  const calls = [];
  let foreignAck = true;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, body });
    if (path === "/health") return new Response(JSON.stringify({ version: "3.0.0" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") return new Response(JSON.stringify({ id: foreignAck ? "foreign" : body.id, status: "created" }));
    if (path === "/observations") return new Response(JSON.stringify({ id: 1, status: "saved" }));
    return new Response(JSON.stringify({ id: 1, status: "ok" }));
  };
  try {
    await withPluginSandbox("engram-pi-malformed-mapping-", async ({ sandbox }) => {
      const { registeredTools, eventHandlers } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = () => registeredTools.get("mem_save").execute("malformed", { title: "blocked", content: "blocked" }, undefined, undefined, ctx);
      assert.equal((await save()).isError, true, "foreign acknowledgement must be refused even when persisted");
      assert.equal(calls.filter(({ path }) => path === "/observations").length, 0);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.equal(calls.filter(({ path }) => path.endsWith("/end")).length, 0);
      foreignAck = false;
      await eventHandlers.get("session_start")({}, ctx);
      assert.equal((await save()).isError, undefined);
      await eventHandlers.get("session_shutdown")({}, ctx);
      assert.ok(calls.filter(({ path }) => path === "/sessions").every(({ body }) => body.id === "root" && body.resume === true));
      assert.deepEqual(calls.filter(({ path }) => path === "/observations").map(({ body }) => body.session_id), ["root"]);
      assert.deepEqual(calls.filter(({ path }) => path.endsWith("/end")).map(({ path }) => path), ["/sessions/root/end"]);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("legacy UUID mapping is registered as-is until ended, then resumes the runtime root", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeID = "legacy-root";
  const legacyID = `${runtimeID}:resume:bb1fd7b6-4816-4b42-b40c-81902893f955`;
  const entries = [{ type: "custom", customType: "engram-effective-session", data: { runtimeID, effectiveID: legacyID, project: "pi", pending: true } }];
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  const registrations = [];
  const writes = [];
  let ended = false;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/health") return new Response(JSON.stringify({ version: "3.0.0" }));
    if (path === "/sessions") {
      registrations.push(body);
      if (ended && body.id === legacyID) return new Response(JSON.stringify({ code: "session_already_ended" }), { status: 409 });
      return new Response(JSON.stringify({ id: body.id === runtimeID ? `${runtimeID}:resume:2` : body.id, status: "created" }));
    }
    if (path === "/observations") { writes.push(body.session_id); return new Response(JSON.stringify({ id: writes.length })); }
    throw new Error(`unexpected request: ${path}`);
  };
  try {
    await withPluginSandbox("engram-pi-legacy-core-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = () => registeredTools.get("mem_save").execute("legacy", { title: "legacy", content: "legacy" }, undefined, undefined, ctx);
      assert.equal((await save()).isError, undefined);
      ended = true;
      assert.equal((await save()).isError, undefined);
      assert.deepEqual(registrations.map(({ id, resume }) => ({ id, resume })), [
        { id: legacyID, resume: false }, { id: legacyID, resume: false }, { id: runtimeID, resume: true },
      ]);
      assert.deepEqual(writes, [legacyID, `${runtimeID}:resume:2`]);
      assert.equal(entries.at(-1).data.effectiveID, `${runtimeID}:resume:2`);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("resume registration refuses invalid acknowledgements before persisting or writing", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  let acknowledgement;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/health") return new Response(JSON.stringify({ version: "3.0.0" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") return new Response(JSON.stringify(acknowledgement));
    throw new Error(`unexpected write: ${path}`);
  };
  try {
    await withPluginSandbox("engram-pi-invalid-resume-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("ack-root");
      ctx.sessionManager.getBranch = () => entries;
      const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      for (acknowledgement of [null, {}, { status: "created" }, { id: "ack-root", status: "ok" },
        { id: "foreign:resume:2", status: "created" }, { id: 42, status: "created" }]) {
        const result = await registeredTools.get("mem_save").execute("invalid", { title: "blocked", content: "blocked" }, undefined, undefined, ctx);
        assert.equal(result.isError, true);
        assert.match(result.content[0].text, /invalid acknowledgement/);
      }
      assert.equal(entries.length, 0);
      assert.equal(calls.filter((path) => path !== "/sessions" && path !== "/project/current" && path !== "/health").length, 0);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("lost core acknowledgement retries the root without guessing a continuation", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const registrations = [];
  let writes = 0;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/health") return new Response(JSON.stringify({ version: "3.0.0" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "pi" }));
    if (path === "/sessions") {
      registrations.push(JSON.parse(init.body));
      if (registrations.length <= 2) throw new Error("response lost after core selected continuation");
      return new Response(JSON.stringify({ id: "lost-root:resume:2", status: "created" }));
    }
    if (path === "/observations") { writes++; return new Response(JSON.stringify({ id: 1 })); }
    throw new Error(`unexpected request: ${path}`);
  };
  try {
    await withPluginSandbox("engram-pi-lost-core-", async ({ sandbox }) => {
      const entries = [];
      const ctx = runtimeContext("lost-root");
      ctx.sessionManager.getBranch = () => entries;
      const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = () => registeredTools.get("mem_save").execute("lost", { title: "lost", content: "lost" }, undefined, undefined, ctx);
      assert.equal((await save()).isError, true);
      assert.equal(entries.length, 0, "unknown core identity must not be guessed or persisted");
      assert.equal(writes, 0);
      assert.equal((await save()).isError, undefined);
      assert.ok(registrations.every(({ id, resume }) => id === "lost-root" && resume === true));
      assert.equal(entries.at(-1).data.effectiveID, "lost-root:resume:2");
      assert.equal(writes, 1);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("old server ended-session refusal explains compatibility without client guessing", async () => {
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
      const entries = [];
      const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const ctx = runtimeContext("ended-without-entry");
      ctx.sessionManager.getBranch = () => entries;
      const result = await registeredTools.get("mem_save").execute("no-entry", { title: "blocked", content: "blocked" }, undefined, undefined, ctx);
      assert.equal(result.isError, true);
      assert.match(result.details.error, /core unknown.*root_session_resume.*Upgrade/);
      assert.equal(entries.length, 0);
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
    if (path === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
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
        return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
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
      assert.equal(endResult.isError, true, "uncertain registration cannot authorize explicit end");
      assert.deepEqual(uncertainEndCalls, []);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("Pi-native explicit end awaits a pending effective registration conflict before sending end", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const gate = deferred();
  const started = deferred();
  const calls = [];
  const entries = [];
  const runtimeID = "effective-conflict";
  const effectiveID = `${runtimeID}:resume:reserved`;
  const ctx = runtimeContext(runtimeID);
  ctx.sessionManager.getBranch = () => entries;
  entries.push({ type: "custom", customType: "engram-effective-session", data: {
    runtimeID, effectiveID, pending: true, project: "local-project",
  } });
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "local-project" }));
    if (path === "/sessions") {
      started.resolve();
      await gate.promise;
      return new Response(JSON.stringify({ code: "session_project_conflict", session_id: effectiveID,
        owner_project: "foreign-project", requested_project: "local-project" }), { status: 409 });
    }
    if (path.endsWith("/end")) return new Response(JSON.stringify({ status: "ended" }));
    throw new Error(`unexpected request: ${path} ${init.method}`);
  };
  try {
    await withPluginSandbox("engram-pi-effective-conflict-end-", async ({ sandbox }) => {
      const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
      const save = registeredTools.get("mem_save").execute("save", { title: "conflict", content: "conflict" }, undefined, undefined, ctx);
      await started.promise;
      const end = registeredTools.get("mem_session_end").execute("end", { id: runtimeID }, undefined, undefined, ctx);
      assert.equal(calls.filter((path) => path.endsWith("/end")).length, 0);
      gate.resolve();
      const [saveResult, endResult] = await Promise.all([save, end]);
      assert.equal(saveResult.isError, true);
      assert.equal(endResult.isError, true, "registration conflict must propagate to explicit end");
      assert.match(endResult.content[0].text, /belongs to Engram project foreign-project/);
      assert.equal(calls.filter((path) => path.endsWith("/end")).length, 0);
      assert.equal(entries.at(-1).customType, "engram-rejected-effective-session");
    });
  } finally {
    gate.resolve();
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("explicit end reconciles only an owned pending effective ID with matching already-ended evidence", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const runtimeID = "pending-explicit";
  const effectiveID = `${runtimeID}:resume:reserved`;
  const calls = [];
  let responseID = effectiveID;
  let owner = "local-project";
  globalThis.fetch = async (url) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (path === "/project/current") return new Response(JSON.stringify({ project: "local-project" }));
    if (path === "/sessions") return new Response(JSON.stringify({ code: "session_already_ended", session_id: responseID }), { status: 409 });
    if (path.endsWith("/end")) return new Response(JSON.stringify({ status: "ended" }));
    throw new Error(`unexpected request: ${path}`);
  };
  try {
    for (const [response, markerOwner, success] of [
      [effectiveID, "local-project", true], ["other-id", "local-project", false],
      [undefined, "local-project", false], [effectiveID, "foreign-project", false],
    ]) {
      responseID = response;
      owner = markerOwner;
      const entries = [{ type: "custom", customType: "engram-effective-session", data: {
        runtimeID, effectiveID, pending: true, project: owner,
      } }];
      const ctx = runtimeContext(runtimeID);
      ctx.sessionManager.getBranch = () => entries;
      await withPluginSandbox("engram-pi-pending-explicit-", async ({ sandbox }) => {
        const { registeredTools } = await loadPluginHarness(sandbox, (customType, data) => entries.push({ type: "custom", customType, data }));
        const result = await registeredTools.get("mem_session_end").execute("end", { id: runtimeID }, undefined, undefined, ctx);
        assert.equal(result.isError, success ? undefined : true);
        assert.equal(entries.at(-1).data.pending, !success);
        assert.equal(calls.filter((path) => path.endsWith("/end") || path.includes(":resume:")).length, 0);
      });
    }
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
    if (path === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
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
        if (path === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
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

test("structured system prompt options receive memory instructions and the one-shot recovery notice", async () => {
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
    await withPluginSandbox("engram-pi-append-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const beforeAgentStart = eventHandlers.get("before_agent_start");
      const sessionId = "append-owner";
      await eventHandlers.get("session_start")({}, runtimeContext(sessionId));

      // Pi clones these options per run, so mutating them is how text reaches every turn,
      // including turns that skip before_agent_start; a returned systemPrompt would be forced.
      const options = { appendSystemPrompt: "existing" };
      const first = await beforeAgentStart(
        { systemPrompt: "base", systemPromptOptions: options, prompt: "a sufficiently long captured prompt" },
        runtimeContext(sessionId),
      );
      assert.equal(first, undefined, "no forced systemPrompt replacement is returned");
      assert.match(options.appendSystemPrompt, /^existing\n\n## Engram Persistent Memory — Protocol/);
      assert.ok(calls.some((call) => call.method === "POST" && call.path === "/prompts"), "prompt capture still runs");

      const repeated = await beforeAgentStart({ systemPrompt: "base", systemPromptOptions: options }, runtimeContext(sessionId));
      assert.equal(repeated, undefined);
      assert.equal(options.appendSystemPrompt.split("## Engram Persistent Memory — Protocol").length, 2, "memory block is appended once");

      const empty = {};
      await beforeAgentStart({ systemPrompt: "base", systemPromptOptions: empty, prompt: "hi" }, runtimeContext(sessionId));
      assert.match(empty.appendSystemPrompt, /^## Engram Persistent Memory — Protocol/, "empty append text gets no leading separator");

      await eventHandlers.get("session_compact")({ compactionEntry: { summary: "compacted summary" } }, runtimeContext(sessionId));
      const recoveryOptions = { appendSystemPrompt: "" };
      const recovered = await beforeAgentStart({ systemPrompt: "base", systemPromptOptions: recoveryOptions }, runtimeContext(sessionId));
      assert.equal(recovered, undefined);
      assert.match(recoveryOptions.appendSystemPrompt, /## Engram Persistent Memory — Protocol[\s\S]*\n\n[\s\S]*already saved/);
      assert.equal(recoveryOptions.appendSystemPrompt.split("already saved").length, 2, "recovery notice lands once");

      const consumedOptions = { appendSystemPrompt: "" };
      const consumed = await beforeAgentStart({ systemPrompt: "base", systemPromptOptions: consumedOptions }, runtimeContext(sessionId));
      assert.equal(consumed, undefined);
      assert.doesNotMatch(consumedOptions.appendSystemPrompt, /already saved/, "recovery notice is consumed");
      assert.match(consumedOptions.appendSystemPrompt, /## Engram Persistent Memory — Protocol/);
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

test("structured system prompt options suppress the replacement on early-return paths", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const { fetchStub } = recordingFetch([
    { method: "GET", path: "/project/current", body: { project: "pi" } },
    { method: "POST", path: "/sessions", status: 503, body: { error: "registration rejected" } },
  ]);
  globalThis.fetch = fetchStub;

  try {
    await withPluginSandbox("engram-pi-append-early-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      const ctx = runtimeContext("append-early");
      ctx.hasUI = true;
      ctx.ui.notify = () => {};
      await eventHandlers.get("session_start")({}, ctx);
      const options = { appendSystemPrompt: "existing" };
      const failed = await eventHandlers.get("before_agent_start")(
        { systemPrompt: "base", systemPromptOptions: options, prompt: "a prompt that must not follow failed registration" },
        ctx,
      );
      assert.equal(failed, undefined, "failed registration returns no replacement");
      assert.match(options.appendSystemPrompt, /^existing\n\n## Engram Persistent Memory — Protocol/);

      // A null options value is not an object to mutate, so the legacy replacement stays.
      const legacy = await eventHandlers.get("before_agent_start")({ systemPrompt: "base", systemPromptOptions: null }, ctx);
      assert.match(legacy.systemPrompt, /^base\n\n## Engram Persistent Memory — Protocol/);
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
    if (request.pathname === "/sessions") return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
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
      return new Response(JSON.stringify({ id: JSON.parse(init.body).id, status: "created" }));
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

test("automatic prompt capture redacts a private block that straddles the truncation limit", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  const calls = [];
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method: init.method ?? "GET", path, body });
    if (path === "/health") return new Response(JSON.stringify({ status: "ok" }));
    if (path === "/project/current") return new Response(JSON.stringify({ project: "engram" }));
    if (path === "/sessions") return new Response(JSON.stringify({ id: body.id, status: "created" }), { status: 201 });
    if (path === "/prompts") return new Response(JSON.stringify({ id: 1 }), { status: 201 });
    return new Response(JSON.stringify({}));
  };

  try {
    await withPluginSandbox("engram-pi-contract-", async ({ sandbox }) => {
      const { eventHandlers } = await loadPluginHarness(sandbox);
      await eventHandlers.get("before_agent_start")(
        { systemPrompt: "base", prompt: `${"a".repeat(1980)}<private>PIN=42</private> trailing` },
        runtimeContext("straddle-session"),
      );
    });

    const prompts = calls.filter((call) => call.method === "POST" && call.path === "/prompts");
    assert.equal(prompts.length, 1, "the prompt must still be captured");
    assert.equal(JSON.stringify(prompts[0].body).includes("PIN=42"), false, "private content must never reach the wire");
    assert.match(prompts[0].body.content, /\[REDACTED\]/);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
});

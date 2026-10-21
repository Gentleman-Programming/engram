import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { existsSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { createInterface } from "node:readline";
import { join } from "node:path";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

const repositoryRoot = join(PLUGIN_ROOT, "../..");
const repositoryFixtureAvailable = [
  "go.mod", "internal/server/server.go", "internal/store/store.go",
  "plugin/pi/test/support/real-server/main.go",
].every(path => existsSync(join(repositoryRoot, path)));
const realServerSkip = !process.env.ENGRAM_PI_TEST_SERVER_BINARY && !repositoryFixtureAvailable
  ? "requires the Engram Go checkout or ENGRAM_PI_TEST_SERVER_BINARY" : false;

async function harness(sandbox) {
  const tools = new Map();
  const register = await importPluginFromSandbox(sandbox);
  register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
  const tool = tools.get("mem_update");
  return { tool, execute: params => tool.execute("update-contract", params, undefined, undefined, {
    cwd: sandbox, sessionManager: { getSessionId: () => "update-contract" }, ui: { setStatus() {} },
  }) };
}

// Mocked registration/transport evidence is separate from the Go-backed test below.
test("registered mem_update exposes optional strings and forwards exact supplied fields", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const requests = [];
  let error;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    if (path === "/health") return Response.json({ status: "ok" });
    if (path === "/project/current") return Response.json({ project: "update-contract" });
    assert.equal(path, "/observations/42");
    requests.push({ path: new URL(url).pathname + new URL(url).search, method: init.method, body: JSON.parse(init.body) });
    return Response.json(error ? { error } : { id: 42, content: "server-owned", revision_count: 7 }, { status: error ? 400 : 200 });
  };
  try {
    await withPluginSandbox("engram-update-mock-", async ({ sandbox }) => {
      const { tool, execute } = await harness(sandbox);
      const fields = tool.parameters.args[0];
      for (const name of ["find", "replace"]) {
        assert.ok(fields[name], `${name} must be model-visible`);
        assert.equal(fields[name].kind, "Optional");
        assert.equal(fields[name].args[0].kind, "String");
      }
      for (const body of [
        { find: "old", replace: "new" }, { find: "", replace: "" },
        { find: "old", replace: "" }, { find: "" }, { replace: "" },
        { find: "old", replace: "new", content: "" },
        { content: "full content", title: "Full", type: "note", scope: "global", topic_key: "topic" },
        { type: "decision", topic_key: "" }, {},
      ]) {
        requests.length = 0;
        const result = await execute({ id: 42, expected_project: "update-contract", ...body });
        assert.notEqual(result.isError, true);
        assert.deepEqual(result.details.data, { id: 42, content: "server-owned", revision_count: 7 });
        assert.deepEqual(JSON.parse(result.content[0].text), result.details.data);
        assert.deepEqual(requests, [{ path: "/observations/42?expected_project=update-contract", method: "PATCH", body }]);
      }
      error = "find and replace must be provided together";
      requests.length = 0;
      const rejected = await execute({ id: 42, expected_project: "update-contract", find: "old" });
      assert.equal(rejected.isError, true);
      assert.ok(rejected.content[0].text.includes(error));
      assert.equal(requests.length, 1, "validation belongs to Go; errors never replay PATCH");
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

test("mem_update legacy full-content metadata and omission forwarding", async (t) => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const requests = [], results = [];
  const payload = { id: 42, content: "server-owned", revision_count: 7 };
  globalThis.fetch = async (url, init = {}) => {
    const request = new URL(url);
    if (request.pathname === "/health") return Response.json({ status: "ok" });
    if (request.pathname === "/project/current") return Response.json({ project: "update-contract" });
    assert.equal(request.pathname, "/observations/42");
    const body = JSON.parse(init.body);
    requests.push({ path: request.pathname + request.search, method: init.method, body });
    return Object.keys(body).length ? Response.json(payload)
      : Response.json({ error: "at least one field is required" }, { status: 400 });
  };
  try {
    await withPluginSandbox("engram-update-legacy-", async ({ sandbox }) => {
      const { execute } = await harness(sandbox);
      for (const body of [
        { content: "Full body", title: "Full title", type: "note", scope: "global", topic_key: "topic" },
        { type: "decision", topic_key: "" }, { content: "" }, {},
      ]) {
        const result = await execute({ id: 42, expected_project: "update-contract", ...body });
        assert.deepEqual(requests.at(-1), { path: "/observations/42?expected_project=update-contract", method: "PATCH", body });
        if (Object.keys(body).length) {
          assert.notEqual(result.isError, true);
          assert.deepEqual(result.details.data, payload);
          assert.deepEqual(JSON.parse(result.content[0].text), payload);
        } else {
          assert.equal(result.isError, true);
          assert.equal(result.content[0].text, "at least one field is required");
        }
        results.push({ isError: result.isError === true, content: result.content, data: result.details.data });
      }
      assert.equal(requests.length, 4);
      t.diagnostic(JSON.stringify({ requests, results }));
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
});

// Same isolated server/EOF lifecycle as real-agent-workflows.test.mjs. No user store is opened.
for (const mode of ["legacy", "find/replace"]) {
test(`mem_update real Go HTTP ${mode} mutation semantics`, { skip: realServerSkip }, async (t) => {
  await withPluginSandbox("engram-update-real-", async ({ dir, sandbox }) => {
    const keys = ["ENGRAM_URL", "ENGRAM_PROJECT", "ENGRAM_HTTP_TOKEN", "ENGRAM_DATA_DIR", "ENGRAM_CLOUD_AUTOSYNC", "HOME"];
    const previous = Object.fromEntries(keys.map(key => [key, process.env[key]]));
    const token = randomBytes(32).toString("hex");
    const executable = process.env.ENGRAM_PI_TEST_SERVER_BINARY ?? join(dir, process.platform === "win32" ? "server.exe" : "server");
    if (!process.env.ENGRAM_PI_TEST_SERVER_BINARY) {
      const build = spawnSync("go", ["build", "-o", executable, "./plugin/pi/test/support/real-server"],
        { cwd: repositoryRoot, encoding: "utf8", timeout: 60000 });
      assert.ifError(build.error);
      assert.equal(build.status, 0, build.stderr);
    }
    const env = { ...process.env, HOME: dir, ENGRAM_PROJECT: "update-contract", ENGRAM_HTTP_TOKEN: token,
      ENGRAM_DATA_DIR: join(dir, "data"), ENGRAM_CLOUD_AUTOSYNC: "0" };
    const child = spawn(executable, [join(dir, "store")], { env, stdio: ["pipe", "pipe", "pipe"] });
    let stderr = "";
    child.stderr.on("data", value => { stderr += value; });
    const lines = createInterface({ input: child.stdout });
    let succeeded = false;
    try {
      const url = await new Promise((resolve, reject) => {
        const timer = setTimeout(() => finish(new Error(`server readiness timed out: ${stderr}`)), 5000);
        const onLine = value => finish(undefined, value);
        const onError = error => finish(error);
        const onExit = code => finish(new Error(`server exited ${code}: ${stderr}`));
        const finish = (error, value) => {
          clearTimeout(timer); lines.off("line", onLine); child.off("error", onError); child.off("exit", onExit);
          if (error) reject(error); else resolve(value);
        };
        lines.once("line", onLine); child.once("error", onError); child.once("exit", onExit);
      });
      Object.assign(process.env, env, { ENGRAM_URL: url });
      const { execute } = await harness(sandbox);
      const request = async (path, body) => {
        const response = await fetch(url + path, { method: body === undefined ? "GET" : "POST",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
          body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) });
        assert.ok(response.ok, `${path}: ${response.status}`);
        return response.json();
      };
      await request("/sessions", { id: "update-seed", project: "update-contract", directory: dir });
      const seed = await request("/observations", { session_id: "update-seed", project: "update-contract", type: "note",
        title: "Original", content: "old old OLD café", scope: "project", topic_key: "update/topic" });
      const id = seed.id;
      assert.ok(id > 0);
      const read = () => request(`/observations/${id}?include_history=true`);
      const update = (body, expected_project = "update-contract") => execute({ id, expected_project, ...body });
      // Compare the complete public observation/history and global public counters, not only content.
      const snapshot = async () => ({ observation: await read(), stats: await request("/stats") });
      if (mode === "legacy") {
        const observed = [];
        for (const body of [{ content: "Full body", title: "Full title" }, { type: "decision", scope: "global", topic_key: "" }, {}, { content: "" }]) {
          const before = await snapshot();
          const result = await update(body);
          const after = await snapshot();
          if (Object.keys(body).length === 0 || body.content === "") {
            assert.equal(result.isError, true);
            assert.deepEqual(after, before);
          } else {
            assert.notEqual(result.isError, true, result.content[0].text);
            assert.equal(after.observation.revision_count, before.observation.revision_count + 1);
            assert.equal(after.observation.version_count, before.observation.version_count + (body.content ? 1 : 0));
          }
          const row = after.observation;
          observed.push({ body, isError: result.isError === true, error: result.isError ? result.content[0].text : undefined,
            content: row.content, title: row.title, type: row.type, scope: row.scope, topic_key: row.topic_key,
            revision_count: row.revision_count, version_count: row.version_count });
        }
        t.diagnostic(JSON.stringify({ observed }));
        succeeded = true;
        return;
      }
      for (const [body, message, owner] of [
        [{ find: "old" }, "find and replace must be provided together"],
        [{ replace: "" }, "find and replace must be provided together"],
        [{ find: "old", replace: "new", content: "" }, "find and replace cannot be combined with content"],
        [{ find: "x".repeat(50001), replace: "x" }, "find and replace inputs exceed maximum observation length"],
        [{ find: "old", replace: "x".repeat(50001) }, "find and replace inputs exceed maximum observation length"],
        [{}, "at least one field is required"],
        [{ find: "old", replace: "new" }, "expected_project does not match observation owner", "other-project"],
      ]) {
        const before = await snapshot();
        const result = await update(body, owner);
        assert.equal(result.isError, true);
        assert.ok(result.content[0].text.includes(message), result.content[0].text);
        assert.deepEqual(await snapshot(), before, message);
      }
      const success = async (body, content, revisionDelta, historyDelta) => {
        const before = await read();
        const result = await update(body);
        assert.notEqual(result.isError, true, result.content[0].text);
        const after = await read();
        assert.equal(after.content, content);
        assert.equal(after.revision_count, before.revision_count + revisionDelta);
        assert.equal(after.version_count, before.version_count + historyDelta);
        assert.equal(result.details.data.content, after.content);
        return after;
      };
      await success({ find: "old", replace: "new" }, "new new OLD café", 1, 1);
      await success({ find: "café", replace: "té" }, "new new OLD té", 1, 1);
      await success({ find: "new ", replace: "" }, "OLD té", 1, 1);
      for (const body of [{ find: "", replace: "insert" }, { find: "absent", replace: "x" }, { find: "OLD", replace: "OLD" }]) {
        const before = await snapshot();
        await success(body, "OLD té", 0, 0);
        assert.deepEqual(await snapshot(), before, "content-only no-op preserves public state and counters");
      }
      const metadata = await success({ find: "absent", replace: "x", type: "decision", topic_key: "update/new", scope: "global" }, "OLD té", 1, 0);
      assert.equal(metadata.type, "decision");
      assert.equal(metadata.scope, "global");
      assert.equal(metadata.topic_key, "update/new");
      await success({ content: "a", title: "Full replacement" }, "a", 1, 1);
      await success({ type: "note" }, "a", 1, 0);
      // Accepted exact byte boundary, then an in-transaction growth/empty-result rejection.
      await success({ find: "a", replace: "x".repeat(50000) }, "x".repeat(50000), 1, 1);
      for (const [body, message] of [
        [{ find: "x", replace: "xx" }, "replacement result exceeds maximum observation length"],
        [{ find: "x", replace: "" }, "observation content is required"],
      ]) {
        const before = await snapshot();
        const result = await update(body);
        assert.equal(result.isError, true);
        assert.ok(result.content[0].text.includes(message), result.content[0].text);
        assert.deepEqual(await snapshot(), before, "these Go transaction failures roll back; not a universal error guarantee");
      }
      succeeded = true;
    } finally {
      for (const [key, value] of Object.entries(previous)) {
        if (value === undefined) delete process.env[key]; else process.env[key] = value;
      }
      lines.close();
      let timedOut = false;
      if (child.exitCode === null && child.signalCode === null) {
        const exit = once(child, "exit");
        const timer = setTimeout(() => { timedOut = true; child.kill(); }, 5000);
        try { child.stdin.end(); await exit; } finally { clearTimeout(timer); }
      }
      if (succeeded) {
        assert.equal(timedOut, false, stderr);
        assert.equal(child.exitCode, 0, stderr);
      }
    }
  });
});
}

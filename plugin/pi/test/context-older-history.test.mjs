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
const reason = "requires the Engram Go checkout or ENGRAM_PI_TEST_SERVER_BINARY";
const realServerSkip = !process.env.ENGRAM_PI_TEST_SERVER_BINARY && !repositoryFixtureAvailable ? reason : false;

async function harness(sandbox) {
  const tools = new Map();
  const register = await importPluginFromSandbox(sandbox);
  register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
  const tool = tools.get("mem_context");
  return { tool, execute: params => tool.execute("context-contract", params, undefined, undefined, {
    cwd: sandbox, sessionManager: { getSessionId: () => "context-contract" }, ui: { setStatus() {} },
  }) };
}

async function mocked(run) {
  const previousFetch = globalThis.fetch, previousUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const requests = [];
  let payload = { context: "Go context unchanged" }, status = 200;
  globalThis.fetch = async (url, init = {}) => {
    const request = new URL(url);
    if (request.pathname === "/health") return Response.json({ status: "ok" });
    if (request.pathname === "/project/current") return Response.json({ project: "context-alpha" });
    assert.equal(request.pathname, "/context");
    requests.push({ path: request.pathname + request.search, method: init.method ?? "GET" });
    return Response.json(payload, { status });
  };
  try {
    await withPluginSandbox("engram-context-mock-", async ({ sandbox }) => {
      await run({ ...await harness(sandbox), requests, reply(data, code = 200) { payload = data; status = code; } });
    });
  } finally {
    globalThis.fetch = previousFetch;
    if (previousUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = previousUrl;
  }
}

test("mem_context exposes optional boolean older_history and forwards explicit values", async () => {
  await mocked(async ({ tool, execute, requests }) => {
    const field = tool.parameters.args[0].older_history;
    assert.ok(field, "older_history must be model-visible");
    assert.equal(field.kind, "Optional");
    assert.equal(field.args[0].kind, "Boolean");
    for (const older_history of [false, true]) {
      requests.length = 0;
      const result = await execute({ project: "context-beta", scope: "personal", max_bytes: 1024, compact: false, older_history });
      assert.notEqual(result.isError, true);
      assert.deepEqual(requests, [{ path: `/context?project=context-beta&scope=personal&max_bytes=1024&compact=false&older_history=${older_history}`, method: "GET" }]);
      assert.deepEqual(result.details.data, { context: "Go context unchanged" });
      assert.equal(result.content[0].text, result.details.data.context);
    }
  });
});

test("mem_context legacy project scope budget compact omission and errors", async (t) => {
  await mocked(async ({ execute, requests, reply }) => {
    const results = [];
    for (const [params, query] of [
      [{}, "?project=context-alpha"],
      [{ project: "context-beta", scope: "global", max_bytes: 512, compact: true }, "?project=context-beta&scope=global&max_bytes=512&compact=true"],
      [{ scope: "personal", compact: false }, "?project=context-alpha&scope=personal&compact=false"],
    ]) {
      const result = await execute(params);
      assert.notEqual(result.isError, true);
      assert.deepEqual(requests.at(-1), { path: `/context${query}`, method: "GET" });
      assert.deepEqual(result.details.data, { context: "Go context unchanged" });
      results.push({ content: result.content, data: result.details.data });
    }
    reply({ error: "context request rejected" }, 400);
    const rejected = await execute({});
    assert.equal(rejected.isError, true);
    assert.equal(rejected.content[0].text, "context request rejected");
    assert.equal(requests.length, 4);
    results.push({ isError: rejected.isError, content: rejected.content, data: rejected.details.data });
    t.diagnostic(JSON.stringify({ requests, results }));
  });
});

// Uses the existing isolated server and EOF lifecycle, never a user's data directory.
test("mem_context real Go older overview filters budgets defaults and read-only state", { skip: realServerSkip }, async () => {
  await withPluginSandbox("engram-context-real-", async ({ dir, sandbox }) => {
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
    const env = { ...process.env, HOME: dir, ENGRAM_PROJECT: "context-alpha", ENGRAM_HTTP_TOKEN: token,
      ENGRAM_DATA_DIR: join(dir, "data"), ENGRAM_CLOUD_AUTOSYNC: "0" };
    const child = spawn(executable, [join(dir, "store")], { env, stdio: ["pipe", "pipe", "pipe"] });
    let stderr = "", succeeded = false;
    child.stderr.on("data", value => { stderr += value; });
    const lines = createInterface({ input: child.stdout });
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
        assert.equal(response.status, body === undefined ? 200 : 201, `${path}: ${response.status}`);
        return response.json();
      };
      const ids = [];
      // More than the default 20 recent rows in each filter, all through public writes.
      for (const [project, scope, label] of [
        ["context-alpha", "personal", "Personal"], ["context-alpha", "project", "Project"], ["context-beta", "personal", "Foreign"],
      ]) {
        const session_id = `seed-${label}`;
        await request("/sessions", { id: session_id, project, directory: dir });
        for (let i = 1; i <= 25; i++) {
          const row = await request("/observations", { session_id, project, scope, type: "note",
            title: `${label} title ${i}`, content: `${label} body ${i}\n${"detail ".repeat(35)}` });
          ids.push(row.id);
        }
      }
      const snapshot = async () => ({
        rows: await Promise.all(ids.map(id => request(`/observations/${id}?include_history=true`))),
        stats: await request("/stats?all_projects=true"),
      });
      const before = await snapshot();
      const context = async params => {
        const result = await execute(params);
        assert.notEqual(result.isError, true, result.content[0].text);
        assert.equal(result.content[0].text, result.details.data.context);
        return result.details.data.context;
      };
      const params = { project: "context-alpha", scope: "personal" };
      const absent = await context(params);
      assert.equal(await context({ ...params, older_history: false }), absent);
      assert.doesNotMatch(absent, /Older History|Project title|Foreign title/);
      const enabled = await context({ ...params, older_history: true });
      assert.match(enabled, /### Older History\n/);
      const overview = enabled.slice(enabled.indexOf("### Older History"));
      assert.match(overview, /: 5 observations\n/);
      assert.match(overview, /Sample #\d+: Personal title/);
      assert.doesNotMatch(overview, /body|detail|Foreign title|Project title/);
      assert.equal(enabled, (await request("/context?project=context-alpha&scope=personal&older_history=true")).context);
      for (const [project, scope, label, excluded] of [
        ["context-alpha", "project", "Project", /Personal title|Foreign title/],
        ["context-beta", "personal", "Foreign", /Personal title|Project title/],
      ]) {
        const filtered = await context({ project, scope, older_history: true });
        assert.match(filtered, new RegExp(`Sample #\\d+: ${label} title`));
        assert.doesNotMatch(filtered, excluded);
      }
      const compact = await context({ ...params, older_history: true, compact: true });
      assert.notEqual(compact, enabled);
      assert.equal(compact.slice(compact.indexOf("### Older History")), overview);
      for (const max_bytes of [32, 1024]) {
        const bounded = await context({ ...params, older_history: true, compact: true, max_bytes });
        assert.ok(Buffer.byteLength(bounded) <= max_bytes);
        assert.equal(bounded, (await request(`/context?project=context-alpha&scope=personal&older_history=true&compact=true&max_bytes=${max_bytes}`)).context);
        if (max_bytes === 32) assert.doesNotMatch(bounded, /Older History/);
        else assert.match(bounded, /### Older History/);
      }
      // HTTP uses strconv.ParseBool: malformed/empty values fall back to false, not 4xx.
      for (const value of ["", "invalid", "0", "f", "F", "FALSE", "False"]) {
        assert.equal((await request(`/context?project=context-alpha&scope=personal&older_history=${value}`)).context, absent);
      }
      for (const value of ["1", "t", "T", "TRUE", "True"]) {
        assert.equal((await request(`/context?project=context-alpha&scope=personal&older_history=${value}`)).context, enabled);
      }
      assert.deepEqual(await snapshot(), before, "context reads preserve full public observations/history and counters");
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

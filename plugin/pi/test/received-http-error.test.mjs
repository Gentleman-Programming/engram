import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { fixtureContract } from "./support/runtime-contract-fixture.mjs";
import { withPluginSandbox, importPluginFromSandbox } from "./plugin-sandbox.mjs";

const cantopen = "SQLite CANTOPEN during conn.exec_context (code 14)";

// Use native fetch and actual flushed response headers. The body never finishes
// unless the scenario explicitly completes or destroys it; no timing sleeps.
async function saveWithResponse({ status = 500, body, abortCaller = false, breakBody = false, replayStatus } = {}) {
  const previousFetch = globalThis.fetch;
  const previousURL = process.env.ENGRAM_URL;
  const controller = new AbortController();
  const calls = [];
  const bodies = [];
  const headers = [];
  let pendingResponse;
  let lookups = 0;
  const server = createServer((req, res) => {
    const path = new URL(req.url, "http://fixture.invalid").pathname;
    calls.push([req.method, path]);
    res.setHeader("Content-Type", "application/json");
    if (path === "/observations") {
      const chunks = [];
      req.on("data", chunk => chunks.push(chunk));
      req.on("end", () => {
        bodies.push(Buffer.concat(chunks).toString());
        if (replayStatus && bodies.length === 1) {
          res.statusCode = 201;
          res.end("incomplete acknowledgement");
          return;
        }
        res.writeHead(replayStatus ?? status);
        if (body !== undefined) res.end(JSON.stringify(body));
        else { pendingResponse = res; res.flushHeaders(); }
      });
      return;
    }
    req.resume();
    let data;
    if (path === "/runtime-contract") data = fixtureContract();
    else if (path === "/project/current") data = { project: "fixture" };
    else if (path === "/sessions") data = { id: "fixture-session", status: "created" };
    else if (path === "/observations/save-result") {
      lookups += 1;
      if (replayStatus && lookups > 1) data = { id: 42, status: "committed" };
      else {
        res.statusCode = 404;
        data = { error: "no committed result found for operation_id", code: "observation_save_result_not_found" };
      }
    } else { res.statusCode = 503; data = { error: "unexpected fixture request" }; }
    res.end(JSON.stringify(data));
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  process.env.ENGRAM_URL = `http://127.0.0.1:${server.address().port}`;
  globalThis.fetch = async (url, options) => {
    const response = await previousFetch(url, options);
    if (new URL(url).pathname === "/observations") {
      headers.push(response.status);
      if (abortCaller) controller.abort(new DOMException("caller stopped after headers", "AbortError"));
      if (breakBody) pendingResponse.destroy();
    }
    return response;
  };
  try {
    return await withPluginSandbox("engram-received-http-error-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const execute = () => tools.get("mem_save").execute("fixture", { title: "Synthetic fixture", content: "Synthetic fixture" }, controller.signal, undefined,
        { cwd: sandbox, sessionManager: { getSessionId: () => "fixture-session" }, ui: { setStatus() {} } });
      if (abortCaller) {
        await assert.rejects(execute, error => error.name === "AbortError" && error.message === "caller stopped after headers");
        assert.equal(lookups, 0, "caller cancellation must not initiate recovery");
        return { calls, bodies, headers };
      }
      return { result: await execute(), calls, bodies, headers, lookups };
    });
  } finally {
    globalThis.fetch = previousFetch;
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    if (previousURL === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = previousURL;
  }
}

for (const breakBody of [false, true]) {
  test(`received HTTP 500 with ${breakBody ? "aborted" : "pending"} body stays typed without lookup or replay`, { timeout: 12000 }, async () => {
    const { result, bodies, headers, lookups } = await saveWithResponse({ breakBody });
    assert.equal(headers[0], 500, "native fetch received failure headers");
    assert.equal(lookups, 0, "a typed receipt miss must not authorize replay after received HTTP 500");
    assert.equal(bodies.length, 1);
    assert.equal(result.isError, true);
    assert.equal(result.details.http_status, 500);
    assert.equal(result.content[0].text, "Engram request failed with HTTP 500");
    assert.equal(result.details.data, null, "an incomplete body supplies no diagnostic data");
  });
}

test("complete CANTOPEN HTTP error retains its text and data", async () => {
  const { result, bodies, lookups } = await saveWithResponse({ body: { error: cantopen } });
  assert.equal(result.details.http_status, 500);
  assert.equal(result.content[0].text, cantopen);
  assert.deepEqual(result.details.data, { error: cantopen });
  assert.equal(bodies.length, 1);
  assert.equal(lookups, 0);
});

test("caller cancellation after failure headers wins over typed HTTP error", async () => {
  const { bodies, headers } = await saveWithResponse({ abortCaller: true });
  assert.deepEqual(headers, [500]);
  assert.equal(bodies.length, 1);
});

test("incomplete HTTP 201 retains unknown-outcome recovery", { timeout: 12000 }, async () => {
  const { result, bodies, lookups } = await saveWithResponse({ status: 201 });
  assert.equal(result.details.outcome, "unknown");
  assert.equal(result.details.http_status, undefined);
  assert.equal(lookups, 2);
  assert.equal(bodies.length, 2, "typed miss still authorizes a replay after an incomplete success");
  assert.equal(bodies[1], bodies[0], "the authorized replay preserves frozen bytes");
});

for (const replayStatus of [409, 410]) {
  test(`authorized replay HTTP ${replayStatus} with pending body retains final lookup`, { timeout: 12000 }, async () => {
    const { result, bodies, lookups, headers } = await saveWithResponse({ replayStatus });
    assert.deepEqual(headers, [201, replayStatus]);
    assert.equal(result.isError, undefined);
    assert.deepEqual(result.details.data, { id: 42, status: "committed" });
    assert.equal(lookups, 2);
    assert.equal(bodies.length, 2);
    assert.equal(bodies[1], bodies[0]);
  });
}

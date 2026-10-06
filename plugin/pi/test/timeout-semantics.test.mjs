import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { stripTypeScriptTypes } from "node:module";
import { randomUUID } from "node:crypto";
import { redactValue, redactUrlPath } from "../private-redaction.js";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

function runtimeContext(sessionId) {
  return { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => sessionId }, ui: { setStatus() {} } };
}

// Runs one or more memory tools against a controlled server. `observationBehavior` drives
// POST /observations and GET /observations/save-result; `promptBehavior` drives POST /prompts.
// Each behavior function may return:
//   - `"hang"` to leave the connection open until the client fetch times out
//   - `{ status, body }` to respond immediately with JSON
async function runTimeoutScenario({ observationBehavior, promptBehavior }) {
  let observationPosts = 0;
  let promptPosts = 0;
  const committed = new Map();
  const server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    const query = new URL(request.url, "http://127.0.0.1").searchParams;
    if (path === "/project/current") { response.end(JSON.stringify({ project: "pi" })); return; }
    if (path === "/sessions") {
      let body = "";
      for await (const chunk of request) body += chunk;
      response.end(JSON.stringify({ id: JSON.parse(body).id, status: "created" }));
      return;
    }
    if (path === "/observations" && request.method === "POST") {
      observationPosts += 1;
      let body = "";
      for await (const chunk of request) body += chunk;
      const parsed = JSON.parse(body);
      const instruction = observationBehavior({ path, attempt: observationPosts, operationId: parsed.operation_id, body: parsed, response });
      if (instruction?.id !== undefined) {
        committed.set(parsed.operation_id, instruction.id);
      }
      if (instruction?.respondWith) {
        response.writeHead(instruction.respondWith.status, { "Content-Type": "application/json" });
        response.end(JSON.stringify(instruction.respondWith.body));
      }
      return;
    }
    if (path === "/observations/save-result") {
      const operationId = query.get("operation_id");
      const instruction = observationBehavior({ path, operationId, id: committed.get(operationId), attempt: observationPosts });
      if (instruction?.respondWith) {
        response.writeHead(instruction.respondWith.status, { "Content-Type": "application/json" });
        response.end(JSON.stringify(instruction.respondWith.body));
        return;
      }
      response.statusCode = 404;
      response.end(JSON.stringify({ error: "no committed result found for operation_id" }));
      return;
    }
    if (path === "/prompts" && request.method === "POST") {
      promptPosts += 1;
      const instruction = promptBehavior({ attempt: promptPosts, response });
      if (instruction === "hang") return;
      response.writeHead(instruction.status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(instruction.body));
      return;
    }
    response.statusCode = 404; response.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = `http://127.0.0.1:${server.address().port}`;
  try {
    return await withPluginSandbox("engram-pi-timeout-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const observationResult = await tools.get("mem_save").execute("timeout-write", { title: "committed", content: "once" }, undefined, undefined, runtimeContext("timeout-session"));
      const promptResult = await tools.get("mem_save_prompt").execute("timeout-prompt", { content: "prompt" }, undefined, undefined, runtimeContext("timeout-session"));
      return { observationResult, promptResult, observationPosts, promptPosts };
    });
  } finally {
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await new Promise((resolve, reject) => {
      server.close((error) => error ? reject(error) : resolve());
      server.closeAllConnections?.();
    });
  }
}

// Evaluate the real transport in memory, following the extracted-source fixtures in
// index-source.test.mjs. No sandbox writes or real sleeps are needed for deadline tests.
async function recoveryClockHarness(failure = "timeout", confirmation) {
  const source = await readFile(new URL("../index.ts", import.meta.url), "utf8");
  const transport = stripTypeScriptTypes(source.slice(source.indexOf("function optionalEnvironmentValue"), source.indexOf("function detectLocalConfigProject")));
  const clock = { now: 0, calls: [], waits: [], recoveries: 0 };
  const AbortSignal = { timeout: (ms) => ({ timeoutMs: ms }) };
  const wait = async (ms) => { clock.waits.push(ms); clock.now += ms; };
  const fetch = async (url, init) => {
    const request = new URL(url);
    clock.calls.push({ path: request.pathname, operationId: request.searchParams.get("operation_id"), body: init.body, timeoutMs: init.signal.timeoutMs });
    if ((confirmation === "lookup" && request.pathname === "/observations/save-result")
      || (confirmation === "replay" && request.pathname === "/observations" && clock.calls.length > 1)) {
      return new Response(JSON.stringify({ id: 42, status: confirmation === "lookup" ? "committed" : "saved" }));
    }
    if (failure === "timeout" || request.pathname === "/observations") {
      clock.now += init.signal.timeoutMs;
      throw Object.assign(new Error("deadline elapsed"), { name: "TimeoutError" });
    }
    throw Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" });
  };
  const factory = new Function("fetch", "AbortSignal", "wait", "randomUUID", "redactValue", "redactUrlPath", "recoverImplicitEngramServer", "isConnectionRefusedError", "unreachableMessage", `${transport}\nreturn { postObservationWithReplayRecovery, engramFetchResult };`);
  const api = factory(fetch, AbortSignal, wait, randomUUID, redactValue, redactUrlPath,
    async () => { clock.recoveries += 1; return false; }, (error) => error.code === "ECONNREFUSED", () => "unreachable");
  return { ...api, clock };
}

test("recovery lookups have a one-second single-attempt deadline and bounded unknown outcome", async () => {
  const { postObservationWithReplayRecovery, clock } = await recoveryClockHarness();
  const result = await postObservationWithReplayRecovery({ session_id: "clock-session", title: "title", content: "content" });
  assert.deepEqual(result.transportFailure, { operation: "write", outcome: "unknown", timeoutMs: 3000 });
  assert.equal(result.data, null);
  const lookups = clock.calls.filter(({ path }) => path === "/observations/save-result");
  const writes = clock.calls.filter(({ path }) => path === "/observations");
  assert.equal(lookups.length, 4, "one lookup per replay plus the final lookup; no nested GET retries");
  assert.deepEqual(lookups.map(({ timeoutMs }) => timeoutMs), [1000, 1000, 1000, 1000]);
  assert.equal(clock.now, 16750, "four 3s POSTs, four 1s lookups and 250/500ms backoff");
  assert.equal(writes.length, 4);
  assert.equal(new Set(writes.map(({ body }) => body)).size, 1, "exact payload, including operation_id, stays identical");
  const operationId = JSON.parse(writes[0].body).operation_id;
  assert.ok(operationId);
  assert.ok(lookups.every((lookup) => lookup.operationId === operationId));
  assert.deepEqual(clock.waits, [250, 500]);
});

test("recovery lookup refusal cannot trigger server startup or additional effects", async () => {
  const { postObservationWithReplayRecovery, clock } = await recoveryClockHarness("refused");
  const result = await postObservationWithReplayRecovery({ title: "title", content: "content" });
  assert.equal(result.transportFailure.outcome, "unknown");
  assert.equal(clock.calls.filter(({ path }) => path === "/observations/save-result").length, 4);
  assert.equal(clock.recoveries, 0, "lookup budget must not expand into implicit server recovery");
});

for (const confirmation of ["lookup", "replay"]) {
  test(`bounded recovery still accepts ${confirmation} confirmation`, async () => {
    const { postObservationWithReplayRecovery, clock } = await recoveryClockHarness("timeout", confirmation);
    const result = await postObservationWithReplayRecovery({ title: "title", content: "content" });
    assert.equal(result.data.id, 42);
    assert.equal(result.transportFailure, undefined);
    const writes = clock.calls.filter(({ path }) => path === "/observations");
    assert.equal(writes.length, confirmation === "lookup" ? 1 : 2);
    assert.equal(new Set(writes.map(({ body }) => body)).size, 1);
    assert.equal(clock.now, confirmation === "lookup" ? 3000 : 4000);
  });
}

test("normal GET retains its ten-second timeout and three attempts", async () => {
  const { engramFetchResult, clock } = await recoveryClockHarness();
  const result = await engramFetchResult("/observations/recent?project=pi");
  assert.deepEqual(result.transportFailure, { operation: "read", outcome: "timed_out", timeoutMs: 10000 });
  assert.deepEqual(clock.calls.map(({ timeoutMs }) => timeoutMs), [10000, 10000, 10000]);
  assert.deepEqual(clock.waits, [250, 500]);
  assert.equal(clock.now, 30750);
});

test("observation write timeout with no server confirmation stays unknown through bounded replay", async () => {
  const { observationResult, observationPosts } = await runTimeoutScenario({
    observationBehavior: ({ path }) => {
      if (path === "/observations/save-result") {
        return { respondWith: { status: 404, body: { error: "no committed result found for operation_id" } } };
      }
      return "hang";
    },
    promptBehavior: () => ({ status: 201, body: { id: 1, status: "saved" } }),
  });
  assert.equal(observationResult.isError, true);
  assert.equal(observationResult.details.outcome, "unknown");
  assert.equal(observationResult.details.operation, "write");
  assert.match(observationResult.content[0].text, /do NOT blindly retry/);
  // One initial POST plus ENGRAM_OBSERVATION_REPLAY_MAX_ATTEMPTS replay attempts.
  assert.equal(observationPosts, 4);
});

test("a confirmed observation write within the deadline succeeds without replay", async () => {
  const { observationResult, observationPosts } = await runTimeoutScenario({
    observationBehavior: () => ({ respondWith: { status: 201, body: { id: 1, status: "saved" } } }),
    promptBehavior: () => ({ status: 201, body: { id: 2, status: "saved" } }),
  });
  assert.notEqual(observationResult.isError, true, "a confirmed observation write must not be labeled unknown");
  assert.equal(observationResult.details.data.id, 1);
  assert.equal(observationPosts, 1);
});

test("non-observation write timeout performs one request and stays unknown", async () => {
  const { promptResult, promptPosts } = await runTimeoutScenario({
    observationBehavior: () => ({ respondWith: { status: 201, body: { id: 1, status: "saved" } } }),
    promptBehavior: () => "hang",
  });
  assert.equal(promptResult.isError, true);
  assert.equal(promptResult.details.outcome, "unknown");
  assert.equal(promptResult.details.operation, "write");
  assert.match(promptResult.content[0].text, /do NOT blindly retry/);
  assert.equal(promptPosts, 1);
});

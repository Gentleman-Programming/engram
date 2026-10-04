import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

function runtimeContext(sessionId) {
  return { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => sessionId }, ui: { setStatus() {} } };
}

// Runs mem_save against a controlled server. `onObservationPost` receives each POST to
// /observations and may return `{ id }` to record a committed id, plus optionally
// `{ respondWith: { status, body } }` to send a response. Returning only `{ id }` or
// `null` leaves the connection hanging so the client times out. `onSaveResult` receives
// each GET to /observations/save-result and may return `{ respondWith: { status, body } }`;
// returning nothing sends a 404 lookup miss.
async function runObservationScenario({ onObservationPost, onSaveResult }) {
  const operationIds = [];
  let observationPosts = 0;
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
      operationIds.push(parsed.operation_id);
      const instruction = onObservationPost({ attempt: observationPosts, operationId: parsed.operation_id, body: parsed });
      if (instruction?.id !== undefined) {
        committed.set(parsed.operation_id, instruction.id);
      }
      if (instruction?.respondWith) {
        response.writeHead(instruction.respondWith.status, { "Content-Type": "application/json" });
        response.end(JSON.stringify(instruction.respondWith.body));
      }
      // No respondWith: leave the connection open so the client fetch times out.
      return;
    }
    if (path === "/observations/save-result") {
      const operationId = query.get("operation_id");
      const instruction = onSaveResult({ operationId, id: committed.get(operationId), attempt: observationPosts });
      if (instruction?.respondWith) {
        response.writeHead(instruction.respondWith.status, { "Content-Type": "application/json" });
        response.end(JSON.stringify(instruction.respondWith.body));
        return;
      }
      response.statusCode = 404;
      response.end(JSON.stringify({ error: "no committed result found for operation_id" }));
      return;
    }
    response.statusCode = 404; response.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = `http://127.0.0.1:${server.address().port}`;
  try {
    return await withPluginSandbox("engram-pi-replay-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const result = await tools.get("mem_save").execute("replay-write", { title: "title", content: "content" }, undefined, undefined, runtimeContext("replay-session"));
      return { result, operationIds, observationPosts };
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

test("committed result recovered via lookup without an extra mutation", async () => {
  const committedId = 42;
  const { result, operationIds, observationPosts } = await runObservationScenario({
    onObservationPost: ({ attempt }) => {
      // Commit on the first (and only) POST but never send a response body.
      if (attempt === 1) return { id: committedId };
      return { id: committedId, respondWith: { status: 201, body: { id: committedId, status: "saved" } } };
    },
    onSaveResult: ({ id }) => {
      if (id !== undefined) return { respondWith: { status: 200, body: { id, status: "committed" } } };
      return { respondWith: { status: 404, body: { error: "no committed result found for operation_id" } } };
    },
  });
  assert.notEqual(result.isError, true);
  assert.equal(result.details.data.id, committedId);
  assert.equal(observationPosts, 1);
  assert.equal(operationIds.length, 1);
});

test("server that loses the first response but has committed recovers the original id on exact replay", async () => {
  const committedId = 99;
  let lookupMissOnce = true;
  const { result, operationIds, observationPosts } = await runObservationScenario({
    onObservationPost: ({ attempt }) => {
      if (attempt === 1) return { id: committedId };
      return { id: committedId, respondWith: { status: 201, body: { id: committedId, status: "saved" } } };
    },
    onSaveResult: ({ id }) => {
      if (lookupMissOnce) { lookupMissOnce = false; return { respondWith: { status: 404, body: { error: "no committed result found for operation_id" } } }; }
      if (id !== undefined) return { respondWith: { status: 200, body: { id, status: "committed" } } };
      return { respondWith: { status: 404, body: { error: "no committed result found for operation_id" } } };
    },
  });
  assert.notEqual(result.isError, true);
  assert.equal(result.details.data.id, committedId);
  assert.equal(observationPosts, 2);
  assert.equal(new Set(operationIds).size, 1);
});

test("server that never commits eventually stays unknown and reports the uncertain outcome", async () => {
  const { result, operationIds, observationPosts } = await runObservationScenario({
    onObservationPost: () => null,
    onSaveResult: () => ({ respondWith: { status: 404, body: { error: "no committed result found for operation_id" } } }),
  });
  assert.equal(result.isError, true);
  assert.equal(result.details.outcome, "unknown");
  assert.equal(result.details.operation, "write");
  assert.match(result.content[0].text, /do NOT blindly retry/);
  // One initial POST plus ENGRAM_OBSERVATION_REPLAY_MAX_ATTEMPTS replay attempts.
  assert.equal(observationPosts, 4);
  assert.equal(new Set(operationIds).size, 1);
});

test("same operation_id is reused across POST attempts and never regenerated", async () => {
  const { operationIds, observationPosts } = await runObservationScenario({
    onObservationPost: () => null,
    onSaveResult: () => ({ respondWith: { status: 404, body: { error: "no committed result found for operation_id" } } }),
  });
  assert.equal(observationPosts, 4);
  assert.equal(operationIds.length, 4);
  assert.equal(new Set(operationIds).size, 1);
  assert.ok(operationIds[0]);
  assert.match(operationIds[0], /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i);
});

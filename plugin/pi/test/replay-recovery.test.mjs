import assert from "node:assert/strict";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

const committed = { id: 42, status: "committed" };
const saved = { id: 42, status: "saved" };
const typedMiss = () => Response.json({
  error: "no committed result found for operation_id",
  code: "observation_save_result_not_found",
}, { status: 404 });

function runtimeContext() {
  return { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => "recovery-session" }, ui: { setStatus() {} } };
}

async function saveWith(handler, signal) {
  const originalFetch = globalThis.fetch;
  const originalURL = process.env.ENGRAM_URL;
  const calls = [];
  let healthChecks = 0;
  process.env.ENGRAM_URL = "http://recovery.invalid";
  globalThis.fetch = async (url, options = {}) => {
    const request = new URL(url);
    if (request.pathname === "/project/current") return Response.json({ project: "pi" });
    if (request.pathname === "/sessions") return Response.json({ id: "recovery-session", status: "created" });
    if (request.pathname === "/health") {
      healthChecks += 1;
      return Response.json({ status: "ok" });
    }
    calls.push({ url: request, options });
    return handler(request, options, calls);
  };
  try {
    return await withPluginSandbox("engram-pi-replay-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const result = await tools.get("mem_save").execute(
        "save",
        { title: "recovery", content: "payload" },
        signal,
        undefined,
        runtimeContext(),
      );
      return { result, calls, healthChecks };
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalURL === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalURL;
  }
}

for (const [name, status, response] of [
  ["parsed 5xx", 503, () => Response.json({ error: "server rejected save" }, { status: 503 })],
  ["malformed 4xx", 400, () => new Response("not JSON", { status: 400 })],
  ["malformed 5xx", 503, () => new Response("not JSON", { status: 503 })],
]) {
  test(`${name} initial save response preserves its HTTP status without recovery`, { concurrency: false }, async () => {
    const { result, calls, healthChecks } = await saveWith(() => response());

    assert.equal(result.isError, true);
    assert.equal(result.details.http_status, status);
    assert.equal(calls.length, 1, "a received HTTP error must not start a lookup or replay");
    assert.deepEqual(calls.map(({ options }) => options.method), ["POST"]);
    assert.equal(healthChecks, 0, "a received HTTP error must not start self-heal probing");
  });
}

test("typed receipt miss authorizes one byte-identical replay to frozen canonical destinations", { concurrency: false }, async () => {
  const { result, calls } = await saveWith((_request, options, seen) => {
    if (seen.length === 1) throw new TypeError("acknowledgement lost");
    if (seen.length === 2) return typedMiss();
    return Response.json(saved, { status: 201 });
  });

  assert.notEqual(result.isError, true, JSON.stringify(result));
  assert.deepEqual(result.details.data, saved);
  assert.deepEqual(calls.map(({ url, options }) => [options.method, url.pathname]), [
    ["POST", "/observations"],
    ["GET", "/observations/save-result"],
    ["POST", "/observations"],
  ]);
  assert.equal(calls[0].options.body, calls[2].options.body, "replay preserves the complete serialized payload");
  const operationID = JSON.parse(calls[0].options.body).operation_id;
  assert.match(operationID, /^[0-9a-f-]{36}$/i);
  assert.equal(calls[1].url.searchParams.get("operation_id"), operationID);
  assert.equal(calls[0].url.origin, "http://recovery.invalid");
  assert.equal(calls[1].url.origin, "http://recovery.invalid");
  assert.equal(calls[2].url.origin, "http://recovery.invalid");
});

for (const [name, lookup] of [
  ["legacy 400", () => Response.json({ error: "legacy route" }, { status: 400 })],
  ["generic 404", () => Response.json({ error: "not found" }, { status: 404 })],
  ["malformed typed 404", () => Response.json({ code: "observation_save_result_not_found" }, { status: 404 })],
]) {
  test(`${name} lookup stays unknown and does not replay`, { concurrency: false }, async () => {
    const { result, calls } = await saveWith((_request, _options, seen) => seen.length === 1
      ? Promise.reject(new TypeError("acknowledgement lost"))
      : lookup());
    assert.equal(result.isError, true);
    assert.equal(result.details.outcome, "unknown");
    assert.equal(calls.length, 2);
    assert.match(result.content[0].text, /GET \/observations\/save-result\?operation_id=/);
  });
}

for (const status of [409, 410]) {
  test(`ambiguous replay HTTP ${status} receives one final lookup and never another POST`, { concurrency: false }, async () => {
    const { result, calls } = await saveWith((_request, _options, seen) => {
      if (seen.length === 1) throw new TypeError("acknowledgement lost");
      if (seen.length === 2) return typedMiss();
      if (seen.length === 3) return Response.json({ error: "replay response unavailable" }, { status });
      return Response.json(committed);
    });
    assert.notEqual(result.isError, true);
    assert.deepEqual(result.details.data, committed);
    assert.deepEqual(calls.map(({ options }) => options.method), ["POST", "GET", "POST", "GET"]);
    assert.equal(calls.filter(({ options }) => options.method === "POST").length, 2);
  });
}

for (const [name, response] of [
  ["parsed", () => Response.json({ error: "replay validation failed" }, { status: 400 })],
  ["malformed", () => new Response("not JSON", { status: 400 })],
]) {
  test(`definitive ${name} replay HTTP 400 remains typed without final recovery`, { concurrency: false }, async () => {
    const { result, calls, healthChecks } = await saveWith((_request, _options, seen) => {
      if (seen.length === 1) throw new TypeError("acknowledgement lost");
      if (seen.length === 2) return typedMiss();
      return response();
    });

    assert.equal(result.isError, true);
    assert.equal(result.details.http_status, 400);
    assert.deepEqual(calls.map(({ options }) => options.method), ["POST", "GET", "POST"]);
    assert.equal(healthChecks, 0, "a received replay HTTP error must not start self-heal probing");
  });
}

test("invalid committed receipt remains unknown instead of claiming a save", { concurrency: false }, async () => {
  const { result, calls } = await saveWith((_request, _options, seen) => seen.length === 1
    ? Promise.reject(new TypeError("acknowledgement lost"))
    : Response.json({ id: 0, status: "committed" }));
  assert.equal(result.isError, true);
  assert.equal(result.details.outcome, "unknown");
  assert.equal(calls.length, 2);
});

test("invalid saved receipt and lookup transport failure remain unknown without a duplicate mutation", { concurrency: false }, async () => {
  const invalidSaved = await saveWith((_request, _options, seen) => seen.length === 1
    ? Response.json({ id: 0, status: "saved" }, { status: 201 })
    : Response.json({ error: "legacy route" }, { status: 404 }));
  assert.equal(invalidSaved.result.isError, true);
  assert.equal(invalidSaved.result.details.outcome, "unknown");
  assert.equal(invalidSaved.calls.filter(({ options }) => options.method === "POST").length, 1);

  const transportFailure = await saveWith((_request, _options) => Promise.reject(new TypeError("lookup transport failed")));
  assert.equal(transportFailure.result.isError, true);
  assert.equal(transportFailure.result.details.outcome, "unknown");
  assert.equal(transportFailure.calls.length, 2);
});

for (const stage of ["initial", "lookup", "backoff", "replay", "final lookup"]) {
  test(`caller abort propagates during ${stage} recovery stage`, { concurrency: false }, async () => {
    const controller = new AbortController();
    const abort = () => controller.abort(new DOMException("caller stopped", "AbortError"));
    await assert.rejects(
      () => saveWith((_request, _options, seen) => {
        if (stage === "initial" && seen.length === 1) { abort(); return new Promise(() => {}); }
        if (seen.length === 1) throw new TypeError("acknowledgement lost");
        if (stage === "lookup" && seen.length === 2) { abort(); return new Promise(() => {}); }
        if (seen.length === 2) {
          if (stage === "backoff") setTimeout(abort, 10);
          return typedMiss();
        }
        if (stage === "replay" && seen.length === 3) { abort(); return new Promise(() => {}); }
        if (seen.length === 3) return Response.json({ error: "replay conflict" }, { status: 409 });
        if (stage === "final lookup" && seen.length === 4) { abort(); return new Promise(() => {}); }
        return Response.json(committed);
      }, controller.signal),
      (error) => error?.name === "AbortError" && error.message === "caller stopped",
    );
  });
}

test("recovery has one bounded total timeout and leaves no false saved result", { concurrency: false }, async () => {
  const started = performance.now();
  const { result, calls } = await saveWith((_request, _options, seen) => {
    if (seen.length === 2) return typedMiss();
    return new Promise(() => {});
  });
  assert.equal(result.isError, true);
  assert.equal(result.details.outcome, "unknown");
  assert.ok(performance.now() - started < 8000, "the complete recovery is bounded by its 7.5-second timeout");
  assert.deepEqual(calls.map(({ options }) => options.method), ["POST", "GET", "POST", "GET"]);
});

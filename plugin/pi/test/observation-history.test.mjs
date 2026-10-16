import assert from "node:assert/strict";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

async function withObservation(body) {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const requests = [];
  let payload = {};
  let status = 200;
  globalThis.fetch = async (url, options) => {
    const request = new URL(url);
    if (request.pathname === "/health") return Response.json({ status: "ok" });
    if (request.pathname === "/project/current") return Response.json({ project: "engram" });
    if (request.pathname.startsWith("/observations/")) {
      requests.push({ path: request.pathname + request.search, method: options?.method ?? "GET" });
      return Response.json(payload, { status });
    }
    throw new Error(`unexpected request: ${url}`);
  };
  try {
    await withPluginSandbox("engram-pi-observation-history-", async ({ dir, sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const tool = tools.get("mem_get_observation");
      assert.ok(tool);
      await body({
        tool, requests,
        reply(data, code = 200) { payload = data; status = code; },
        execute(params) {
          return tool.execute("observation-contract", params, undefined, undefined, {
            cwd: dir,
            sessionManager: { getSessionId: () => "observation-contract-session" },
            ui: { setStatus() {} },
          });
        },
      });
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
}

function assertSuccess(result, payload) {
  assert.notEqual(result.isError, true);
  assert.deepEqual(result.details.data, payload);
  assert.deepEqual(JSON.parse(result.content[0].text), payload);
}

const current = { id: 42, title: "Current title", content: "Current content", project: "engram", revision: 76 };
// Representative HTTP payloads exercise adapter transparency, not real Go pagination
// or durable state/counter preservation. Those guarantees require Go public API tests.
function snapshots(first, count) {
  return Array.from({ length: count }, (_, index) => ({
    version: first + index, title: `Title ${first + index}`, content: `Content ${first + index}`,
    created_at: "2026-10-01T00:00:00Z",
  }));
}
const latest = {
  ...current, version_count: 75, history: snapshots(26, 50),
  history_truncated: true, history_from_version: 26, history_cursor: 26,
};
const terminal = { ...current, version_count: 75, history: snapshots(1, 25) };

test("mem_get_observation exposes optional boolean history and numeric cursor without adapter validation", async () => {
  await withObservation(async ({ tool }) => {
    const fields = tool.parameters.args[0];
    assert.equal(fields.id.kind, "Number");
    for (const [name, kind] of [["include_history", "Boolean"], ["history_cursor", "Number"]]) {
      assert.ok(fields[name], `${name} must be exposed`);
      assert.equal(fields[name].kind, "Optional");
      assert.equal(fields[name].args[0].kind, kind);
    }
  });
});

for (const [label, params, query, payload] of [
  ["omitted history retains bare ID retrieval", { id: 42 }, "", current],
  ["explicit false", { id: 42, include_history: false }, "?include_history=false", current],
  ["explicit zero", { id: 42, include_history: true, history_cursor: 0 }, "?include_history=true&history_cursor=0", latest],
  ["cursor only is forwarded for Go to ignore", { id: 42, history_cursor: 26 }, "?history_cursor=26", current],
  ["false with zero", { id: 42, include_history: false, history_cursor: 0 }, "?include_history=false&history_cursor=0", current],
  ["empty history", { id: 42, include_history: true }, "?include_history=true", { ...current, version_count: 0, history: [] }],
  ["populated history", { id: 42, include_history: true }, "?include_history=true", { ...current, version_count: 2, history: snapshots(1, 2) }],
  ["exact 50 boundary", { id: 42, include_history: true }, "?include_history=true", { ...current, version_count: 50, history: snapshots(1, 50) }],
  ["multiple-page metadata", { id: 42, include_history: true }, "?include_history=true", latest],
]) {
  test(`${label} preserves exact server payload and request`, async () => {
    await withObservation(async ({ execute, reply, requests }) => {
      reply(payload);
      const result = await execute(params);
      assert.deepEqual(requests, [{ path: `/observations/42${query}`, method: "GET" }]);
      assertSuccess(result, payload);
      for (const key of ["history", "version_count", "history_truncated", "history_from_version", "history_cursor"]) {
        assert.equal(Object.hasOwn(result.details.data, key), Object.hasOwn(payload, key), key);
      }
    });
  });
}

test("emitted cursor follow-up preserves terminal metadata without automatic paging", async () => {
  await withObservation(async ({ execute, reply, requests }) => {
    reply(latest);
    const first = await execute({ id: 42, include_history: true });
    assertSuccess(first, latest);
    assert.equal(requests.length, 1, "the adapter must not fetch older pages itself");
    reply(terminal);
    const second = await execute({ id: 42, include_history: true, history_cursor: first.details.data.history_cursor });
    assertSuccess(second, terminal);
    assert.deepEqual(requests, [
      { path: "/observations/42?include_history=true", method: "GET" },
      { path: "/observations/42?include_history=true&history_cursor=26", method: "GET" },
    ]);
    for (const key of ["history_truncated", "history_from_version", "history_cursor"]) {
      assert.equal(Object.hasOwn(second.details.data, key), false, key);
    }
    // Only proves the adapter does not omit or duplicate the supplied snapshots.
    const versions = [...second.details.data.history, ...first.details.data.history].map(({ version }) => version);
    assert.deepEqual(versions, Array.from({ length: 75 }, (_, index) => index + 1));
  });
});

for (const [label, params, path, code, message] of [
  ["negative cursor", { id: 42, include_history: true, history_cursor: -1 }, "/observations/42?include_history=true&history_cursor=-1", 400, "history_cursor must be a non-negative integer"],
  ["fractional cursor", { id: 42, include_history: true, history_cursor: 1.5 }, "/observations/42?include_history=true&history_cursor=1.5", 400, "history_cursor must be a non-negative integer"],
  ["missing observation", { id: 999, include_history: true }, "/observations/999?include_history=true", 404, "observation not found"],
  ["legacy missing observation", { id: 999 }, "/observations/999", 404, "observation not found"],
  ["omitted ID existing error", {}, "/observations/undefined", 400, "invalid observation id"],
  ["legacy server error", { id: 42 }, "/observations/42", 500, "retrieval failed"],
]) {
  test(`${label} uses the existing server error path without local rejection or retries`, async () => {
    await withObservation(async ({ execute, reply, requests }) => {
      reply({ error: message }, code);
      const result = await execute(params);
      assert.deepEqual(requests, [{ path, method: "GET" }]);
      assert.equal(result.isError, true);
      assert.equal(result.content[0].text, message);
      assert.equal(result.details.error, message);
      assert.equal(result.details.http_status, code);
      assert.deepEqual(result.details.data, { error: message });
      // Public read-back fixture checks adapter transparency after rejection only;
      // it does not establish unchanged stored data or counters in the real server.
      reply(latest);
      assertSuccess(await execute({ id: 42, include_history: true }), latest);
      assert.equal(requests.length, 2);
      assert.deepEqual(requests[1], { path: "/observations/42?include_history=true", method: "GET" });
    });
  });
}

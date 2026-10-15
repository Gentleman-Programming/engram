import assert from "node:assert/strict";
import { runtimeMetadataResponse } from "./support/runtime-contract-fixture.mjs";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

async function fixture(token, mode, run) {
  const keys = ["ENGRAM_HTTP_TOKEN", "ENGRAM_URL", "ENGRAM_SERVER_MODE"];
  const original = Object.fromEntries(keys.map(key => [key, process.env[key]]));
  const previousFetch = globalThis.fetch;
  const calls = [];
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  if (token === undefined) delete process.env.ENGRAM_HTTP_TOKEN; else process.env.ENGRAM_HTTP_TOKEN = token;
  if (mode === undefined) delete process.env.ENGRAM_SERVER_MODE; else process.env.ENGRAM_SERVER_MODE = mode;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push({ path, method: init.method ?? "GET", headers: new Headers(init.headers), body: init.body && JSON.parse(init.body) });
    if (path === "/runtime-contract") return runtimeMetadataResponse(path);
    if (path === "/health") return Response.json({ status: "ok" });
    if (path === "/project/current") return Response.json({ project: "engram" });
    if (path === "/topic-keys/suggest") return Response.json({ topic_key: "architecture/auth" });
    if (path === "/observations/consolidate") return Response.json({ status: "preview", plan: { fingerprint: "opaque" } });
    if (path === "/conflicts/judge-many") return Response.json({ outcomes: [] });
    throw new Error(`Unexpected request: ${path}`);
  };
  try {
    await withPluginSandbox("engram-shared-auth-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const ctx = { cwd: sandbox, sessionManager: { getSessionId: () => "auth-host" }, ui: { setStatus() {} } };
      const tool = (name, params) => tools.get(name).execute("auth", params, undefined, undefined, ctx);
      await run({ tool, calls });
    });
  } finally {
    globalThis.fetch = previousFetch;
    for (const [key, value] of Object.entries(original)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
  }
}

for (const token of ["fixture-shared-token", "fixture-token-with-trailing-space "]) {
  test(`shared HTTP forwards the configured opaque token without exposing it (${token.endsWith(" ") ? "opaque spacing" : "plain"})`, async () => {
    await fixture(token, undefined, async ({ tool, calls }) => {
      const results = [await tool("mem_suggest_topic_key", { title: "Auth" }),
        await tool("mem_consolidate", { ids_to_delete: [1, 2], new_title: "Merge", new_content: "Summary", expected_project: "engram" }),
        await tool("mem_judge_many", { judgments: [{ judgment_id: "one", relation: "related" }] })];
      assert.ok(results.every(result => result.isError !== true));
      assert.ok(calls.some(call => call.method === "GET"));
      assert.ok(calls.some(call => call.method === "POST"));
      for (const call of calls) {
        // Fetch normalizes HTTP header whitespace; the adapter must not otherwise rewrite the opaque token.
        assert.equal(call.headers.get("Authorization"), `Bearer ${token}`.trim());
        if (call.method === "POST") assert.equal(call.headers.get("Content-Type"), "application/json");
        assert.equal(call.body !== undefined && JSON.stringify(call.body).includes(token), false);
      }
      assert.equal(JSON.stringify(results).includes(token), false);
    });
  });
}

for (const token of [undefined, ""]) {
  test(`shared HTTP without a token preserves zero-config headers (${token === undefined ? "absent" : "empty"})`, async () => {
    await fixture(token, undefined, async ({ tool, calls }) => {
      assert.notEqual((await tool("mem_suggest_topic_key", { title: "Auth" })).isError, true);
      for (const call of calls) assert.equal(call.headers.has("Authorization"), false);
      assert.equal(calls.find(call => call.method === "POST").headers.get("Content-Type"), "application/json");
    });
  });
}

test("a shared token cannot bypass session-owned transport conflict validation", async () => {
  await fixture("fixture-shared-token", "session-owned", async ({ tool, calls }) => {
    const result = await tool("mem_suggest_topic_key", { title: "Auth" });
    assert.equal(result.isError, true);
    assert.deepEqual(calls, []);
    assert.equal(JSON.stringify(result).includes("fixture-shared-token"), false);
  });
});

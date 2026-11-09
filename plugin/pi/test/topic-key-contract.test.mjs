import assert from "node:assert/strict";
import { runtimeMetadataResponse } from "./support/runtime-contract-fixture.mjs";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

async function runSuggestion(params, status, payload, check) {
  const previousFetch = globalThis.fetch;
  const previousURL = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    calls.push({ path, method: init.method ?? "GET", body: init.body && JSON.parse(init.body) });
    if (path === "/runtime-contract") return runtimeMetadataResponse(path);
    if (path === "/health") return Response.json({ status: "ok" });
    if (path === "/project/current") return Response.json({ project: "engram" });
    if (path === "/topic-keys/suggest") return Response.json(payload, { status });
    throw new Error(`Unexpected request: ${path}`);
  };
  try {
    await withPluginSandbox("engram-topic-key-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const result = await tools.get("mem_suggest_topic_key").execute("suggest", params, undefined, undefined, {
        cwd: sandbox, sessionManager: { getSessionId: () => "host-topic-session" }, ui: { setStatus() {} },
      });
      await check(result, calls);
    });
  } finally {
    globalThis.fetch = previousFetch;
    if (previousURL === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = previousURL;
  }
}

test("Pi topic-key suggestions forward shared inputs and preserve the Go response", async () => {
  // The family-based value is also pinned by TestHandleSuggestTopicKeyReturnsFamilyBasedKey.
  const params = { type: "architecture", title: "Auth model", content: "fallback", ignored: "not part of contract" };
  await runSuggestion(params, 200, { topic_key: "architecture/auth-model" }, (result, calls) => {
    assert.notEqual(result.isError, true);
    assert.deepEqual(result.details.data, { topic_key: "architecture/auth-model" });
    assert.deepEqual(calls.filter(({ path }) => path === "/topic-keys/suggest"), [{
      path: "/topic-keys/suggest", method: "POST", body: { type: params.type, title: params.title, content: params.content },
    }]);
    assert.equal(calls.some(({ path }) => path.startsWith("/sessions") || path === "/observations"), false);
  });
});

test("Pi topic-key suggestions preserve Go validation errors without local fallback", async () => {
  await runSuggestion({}, 400, { error: "provide title or content to suggest a topic_key" }, (result, calls) => {
    assert.equal(result.isError, true);
    assert.equal(result.details.http_status, 400);
    assert.equal(result.content[0].text, "provide title or content to suggest a topic_key");
    assert.equal(calls.filter(({ path }) => path === "/topic-keys/suggest").length, 1);
    assert.equal(calls.some(({ path }) => path.startsWith("/sessions") || path === "/observations"), false);
  });
});

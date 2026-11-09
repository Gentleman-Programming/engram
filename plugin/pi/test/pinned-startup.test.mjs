import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { fixtureContract } from "./support/runtime-contract-fixture.mjs";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

test("a pinned server binding late captures the first prompt exactly once", async () => {
  const calls = [];
  const prompts = [];
  const server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://localhost").pathname;
    let text = "";
    for await (const chunk of request) text += chunk;
    const body = text ? JSON.parse(text) : {};
    calls.push({ path, method: request.method });
    response.setHeader("content-type", "application/json");
    if (path === "/runtime-contract") response.end(JSON.stringify(fixtureContract()));
    else if (path === "/project/current") response.end('{"project":"pinned-startup"}');
    else if (path === "/sessions") response.end(JSON.stringify({ id: body.id, status: "created" }));
    else if (path === "/prompts") { prompts.push(body); response.end('{"id":1,"status":"saved"}'); }
    else { response.statusCode = 404; response.end('{}'); }
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  const keys = ["ENGRAM_URL", "ENGRAM_BIN", "ENGRAM_SERVER_MODE", "ENGRAM_HTTP_TOKEN"];
  const previous = keys.map(key => process.env[key]);
  process.env.ENGRAM_URL = `http://127.0.0.1:${port}`;
  process.env.ENGRAM_BIN = "must-not-spawn-a-pinned-server";
  delete process.env.ENGRAM_SERVER_MODE;
  delete process.env.ENGRAM_HTTP_TOKEN;
  let ready;
  try {
    await withPluginSandbox("engram-pinned-startup-", async ({ sandbox }) => {
      const hooks = new Map();
      (await importPluginFromSandbox(sandbox))({ registerTool() {}, on(name, handler) { hooks.set(name, handler); } });
      const warnings = [];
      const ctx = { cwd: sandbox, sessionManager: { getSessionId: () => "pinned-host" },
        ui: { setStatus() {}, notify(message) { warnings.push(message); } } };
      ready = new Promise((resolve, reject) => {
        setTimeout(() => { server.once("error", reject); server.listen(port, "127.0.0.1", resolve); }, 1500);
      });
      // Observe a bind failure immediately; the original promise still rejects
      // when cleanup awaits it, even if the hook has not finished yet.
      void ready.catch(() => {});
      try {
        await hooks.get("before_agent_start")({ systemPrompt: "", prompt: "Preserve the first synthetic startup prompt." }, ctx);
        assert.equal(prompts.length, 1, "the original hook must wait and capture its prompt");
        assert.equal(prompts[0].content, "Preserve the first synthetic startup prompt.");
        assert.equal(prompts[0].session_id, "pinned-host");
        assert.equal(calls.filter(call => call.path === "/sessions" && call.method === "POST").length, 1);
        assert.deepEqual(warnings, []);
      } finally {
        await ready;
      }
    });
  } finally {
    keys.forEach((key, i) => { if (previous[i] === undefined) delete process.env[key]; else process.env[key] = previous[i]; });
    if (server.listening) await new Promise(resolve => server.close(resolve));
  }
});

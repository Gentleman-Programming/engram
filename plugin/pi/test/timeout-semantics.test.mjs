import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

function runtimeContext(sessionId) {
  return { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => sessionId }, ui: { setStatus() {} } };
}

async function scenario(headersFirst) {
  let requests = 0;
  const server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    if (path === "/project/current") { response.end(JSON.stringify({ project: "pi" })); return; }
    if (path === "/sessions") { response.end(JSON.stringify({ status: "created" })); return; }
    if (path === "/observations") {
      requests++;
      for await (const _chunk of request) { /* Ensure the server receives the complete write. */ }
      if (headersFirst) { response.writeHead(200, { "Content-Type": "application/json" }); response.write('{"id":'); }
      setTimeout(() => response.end(headersFirst ? "1}" : '{"id":1}'), 1500);
      return;
    }
    response.statusCode = 404; response.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = `http://127.0.0.1:${server.address().port}`;
  try {
    await withPluginSandbox("engram-pi-timeout-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const result = await tools.get("mem_save").execute("timeout-write", { title: "committed", content: "once" }, undefined, undefined, runtimeContext("timeout-session"));
      assert.equal(result.isError, true);
      assert.equal(result.details.outcome, "unknown");
      assert.equal(result.details.operation, "write");
      assert.match(result.content[0].text, /do NOT blindly retry/);
      assert.equal(requests, 1);
    });
  } finally {
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
}

test("committed write before response headers has unknown outcome without replay", () => scenario(false));
test("committed write during response body has unknown outcome without replay", () => scenario(true));

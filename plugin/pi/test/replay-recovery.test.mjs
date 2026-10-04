import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

function runtimeContext(sessionId) {
  return { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => sessionId }, ui: { setStatus() {} } };
}

test("ambiguous observation write recovers committed result from replay ledger", async () => {
  let savedOperationID;
  const server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    if (path === "/project/current") { response.end(JSON.stringify({ project: "pi" })); return; }
    if (path === "/sessions") {
      let body = "";
      for await (const chunk of request) body += chunk;
      response.end(JSON.stringify({ id: JSON.parse(body).id, status: "created" }));
      return;
    }
    if (path === "/observations") {
      let text = "";
      for await (const chunk of request) text += chunk;
      const body = JSON.parse(text);
      savedOperationID = body.operation_id;
      // Never respond: the client sees an ambiguous transport failure.
      return;
    }
    if (path === "/observations/save-result") {
      const query = new URL(request.url, "http://127.0.0.1").searchParams;
      if (query.get("operation_id") === savedOperationID) {
        response.end(JSON.stringify({ id: 42, status: "committed" }));
        return;
      }
      response.statusCode = 404;
      response.end(JSON.stringify({ error: "not found" }));
      return;
    }
    response.statusCode = 404;
    response.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = `http://127.0.0.1:${server.address().port}`;
  try {
    await withPluginSandbox("engram-pi-replay-", async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const result = await tools.get("mem_save").execute("replay-write", { title: "committed", content: "once" }, undefined, undefined, runtimeContext("replay-session"));
      assert.notEqual(result.isError, true, "recovery must turn ambiguous write into success");
      assert.match(result.content[0].text, /42/);
    });
  } finally {
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});

import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

function runtimeContext(sessionId) {
  return {
    cwd: PLUGIN_ROOT,
    sessionManager: { getSessionId: () => sessionId },
    ui: { setStatus() {} },
  };
}

function listen(server) {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
}

function close(server) {
  return new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
}

test("a delayed committed memory write is sent once and reports an unknown outcome", async () => {
  const committedWrites = [];
  let observationRequests = 0;
  let delayedResponseSent;
  const delayedResponse = new Promise((resolve) => { delayedResponseSent = resolve; });
  const server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    if (path === "/project/current") {
      response.end(JSON.stringify({ project: "pi" }));
      return;
    }
    if (path === "/sessions") {
      response.end(JSON.stringify({ status: "created" }));
      return;
    }
    if (path === "/observations") {
      observationRequests += 1;
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      committedWrites.push(JSON.parse(Buffer.concat(chunks).toString("utf8")));
      setTimeout(() => {
        response.end(JSON.stringify({ id: committedWrites.length }));
        delayedResponseSent();
      }, 1500);
      return;
    }
    response.statusCode = 404;
    response.end();
  });
  await listen(server);
  const { port } = server.address();
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = `http://127.0.0.1:${port}`;

  try {
    await withPluginSandbox("engram-pi-timeout-", async ({ sandbox }) => {
      const registeredTools = new Map();
      const registerEngram = await importPluginFromSandbox(sandbox);
      registerEngram({ registerTool(tool) { registeredTools.set(tool.name, tool); }, on() {} });
      const result = await registeredTools.get("mem_save").execute(
        "delayed-commit",
        { title: "committed once", content: "the server committed before delaying" },
        undefined,
        undefined,
        runtimeContext("delayed-commit-session"),
      );

      assert.equal(result.isError, true);
      assert.equal(result.details.outcome, "unknown");
      assert.equal(result.details.operation, "write");
      assert.match(result.content[0].text, /outcome is unknown/i);
      assert.match(result.content[0].text, /do NOT blindly retry/i);
    });
    await delayedResponse;
    assert.equal(observationRequests, 1, "a delayed committed write must not be replayed");
    assert.equal(committedWrites.length, 1);
  } finally {
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await close(server);
  }
});

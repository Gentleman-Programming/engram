import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { createPluginSandbox, importPluginFromSandbox } from "./plugin-sandbox.mjs";

async function withPassiveCaptureFixture(run, registrationFailure) {
  const dir = await mkdtemp(join(tmpdir(), "engram-pi-passive-capture-"));
  const originalUrl = process.env.ENGRAM_URL;
  const originalBin = process.env.ENGRAM_BIN;
  const captures = [];
  const warnings = [];
  const registrationAttempts = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    if (registrationFailure && new URL(url).pathname === "/sessions") {
      registrationAttempts.push(JSON.parse(options.body));
      if (registrationFailure === "timeout") throw new DOMException("Fixture timeout", "TimeoutError");
      if (registrationFailure === "unknown") throw new TypeError("Fixture connection lost");
      return new Response(JSON.stringify({ id: "unrelated-session", status: "created" }), {
        status: 201, headers: { "content-type": "application/json" },
      });
    }
    return originalFetch(url, options);
  };
  const server = createServer(async (request, response) => {
    if (request.url?.startsWith("/project/current")) {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ project: "fixture-project" }));
      return;
    }
    if (request.url === "/sessions") {
      let body = "";
      for await (const chunk of request) body += chunk;
      response.writeHead(201, { "content-type": "application/json" });
      response.end(JSON.stringify({ id: JSON.parse(body).id, status: "created" }));
      return;
    }
    if (request.url === "/observations/passive") {
      let body = "";
      for await (const chunk of request) body += chunk;
      captures.push({ method: request.method, body: JSON.parse(body) });
    }
    response.writeHead(200, { "content-type": "application/json" });
    response.end("{}");
  });

  try {
    await new Promise((resolve, reject) => server.listen(0, "127.0.0.1", (error) => error ? reject(error) : resolve()));
    const { port } = server.address();
    process.env.ENGRAM_URL = `http://127.0.0.1:${port}`;
    process.env.ENGRAM_BIN = join(dir, "not-used-when-engram-url-is-configured");

    const registerEngram = await importPluginFromSandbox(await createPluginSandbox(dir));
    const hooks = new Map();
    registerEngram({ registerTool() {}, on(name, handler) { hooks.set(name, handler); } });
    await run({ captures, warnings, registrationAttempts, toolExecutionEnd: hooks.get("tool_execution_end"), ctx: {
      cwd: dir,
      hasUI: true,
      ui: { notify: (message, level) => warnings.push({ message, level }) },
      sessionManager: { getSessionId: () => "passive-capture-session" },
    } });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
    if (originalBin === undefined) delete process.env.ENGRAM_BIN; else process.env.ENGRAM_BIN = originalBin;
    if (server.listening) await new Promise((resolve) => server.close(resolve));
    await rm(dir, { recursive: true, force: true });
  }
}

for (const failure of ["timeout", "unknown", "identity"]) {
  test(`background registration ${failure} fails closed with a distinct diagnosis`, async () => {
    await withPassiveCaptureFixture(async ({ captures, warnings, registrationAttempts, toolExecutionEnd, ctx }) => {
      await toolExecutionEnd({ toolName: "Bash", result: "An eligible standard tool result long enough for passive scanning." }, ctx);

      assert.deepEqual(captures, []);
      assert.equal(registrationAttempts.length, failure === "identity" ? 1 : 2);
      assert.ok(registrationAttempts.every(({ id }) => id === "passive-capture-session"));
      assert.equal(warnings.length, 1);
      assert.equal(warnings[0].level, "warning");
      if (failure === "identity") {
        assert.match(warnings[0].message, /invalid acknowledgement/);
      } else {
        assert.match(warnings[0].message, /could not confirm session registration after 5000ms/);
        assert.match(warnings[0].message, /final outcome is unknown/);
        assert.match(warnings[0].message, /No memory write was sent; retrying the memory operation is safe/);
        assert.doesNotMatch(warnings[0].message, /invalid acknowledgement/);
      }
    }, failure);
  });
}

test("an eligible standard tool result is redacted and submitted for passive scanning", async () => {
  await withPassiveCaptureFixture(async ({ captures, toolExecutionEnd, ctx }) => {
    const result = "General command output that is long enough to be scanned but is not an observation. <private>secret</private>";

    await toolExecutionEnd({ toolName: "Bash", result }, ctx);

    assert.deepEqual(captures, [{
      method: "POST",
      body: {
        session_id: "passive-capture-session",
        content: "General command output that is long enough to be scanned but is not an observation. [REDACTED]",
        project: "fixture-project",
        source: "Bash",
      },
    }]);
  });
});

test("Engram-native tool results are excluded from passive scanning", async () => {
  await withPassiveCaptureFixture(async ({ captures, toolExecutionEnd, ctx }) => {
    await toolExecutionEnd({ toolName: "mem_search", result: "This long native-tool result must not reach passive capture at all." }, ctx);

    assert.deepEqual(captures, []);
  });
});

test("serializable object results are submitted for passive scanning", async () => {
  await withPassiveCaptureFixture(async ({ captures, toolExecutionEnd, ctx }) => {
    const result = { output: "A serializable object result that is long enough to be submitted for passive scanning." };

    await toolExecutionEnd({ toolName: "Read", result }, ctx);

    assert.equal(captures.length, 1);
    assert.equal(captures[0].body.content, JSON.stringify(result));
    assert.equal(captures[0].body.source, "Read");
  });
});

test("undefined, short, and unserializable tool results do not reach passive capture", async () => {
  await withPassiveCaptureFixture(async ({ captures, toolExecutionEnd, ctx }) => {
    const cyclic = {};
    cyclic.self = cyclic;

    await assert.doesNotReject(toolExecutionEnd({ toolName: "Read" }, ctx));
    await assert.doesNotReject(toolExecutionEnd({ toolName: "Read", result: "too short" }, ctx));
    await assert.doesNotReject(toolExecutionEnd({ toolName: "Read", result: () => {} }, ctx));
    await assert.doesNotReject(toolExecutionEnd({ toolName: "Read", result: cyclic }, ctx));
    await assert.doesNotReject(toolExecutionEnd({ toolName: "Read", result: 1n }, ctx));

    assert.deepEqual(captures, []);
  });
});

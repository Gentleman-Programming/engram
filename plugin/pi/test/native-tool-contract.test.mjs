import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const NODE_MODULES = join(ROOT, "node_modules");

async function installRuntimeStubs() {
  await mkdir(join(NODE_MODULES, "@earendil-works", "pi-tui"), { recursive: true });
  await writeFile(
    join(NODE_MODULES, "@earendil-works", "pi-tui", "package.json"),
    JSON.stringify({ type: "module", exports: "./index.js" }),
  );
  await writeFile(
    join(NODE_MODULES, "@earendil-works", "pi-tui", "index.js"),
    "export class Text { constructor(text) { this.text = text; } }\n",
  );

  await mkdir(join(NODE_MODULES, "typebox"), { recursive: true });
  await writeFile(
    join(NODE_MODULES, "typebox", "package.json"),
    JSON.stringify({ type: "module", exports: "./index.js" }),
  );
  await writeFile(
    join(NODE_MODULES, "typebox", "index.js"),
    `const schema = (kind) => (...args) => ({ kind, args });
export const Type = new Proxy({}, { get: (_target, prop) => schema(String(prop)) });
`,
  );
}

test("registered Pi-native mem_search reports native provider transport failure", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async () => {
    throw new Error("connection refused");
  };

  try {
    await installRuntimeStubs();
    const registeredTools = new Map();
    const pluginUrl = pathToFileURL(join(ROOT, "index.ts"));
    pluginUrl.search = `?contract=${Date.now()}`;
    const { default: registerEngram } = await import(pluginUrl.href);
    registerEngram({
      registerTool(tool) {
        registeredTools.set(tool.name, tool);
      },
      on() {},
    });

    const memSearch = registeredTools.get("mem_search");
    assert.ok(memSearch, "mem_search tool should be registered");

    const result = await memSearch.execute(
      "tool-call-1",
      { query: "state markers", project: "gentle-agent-state" },
      undefined,
      undefined,
      {
        cwd: ROOT,
        sessionManager: { getSessionId: () => "test-session" },
        ui: { setStatus() {} },
      },
    );

    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /gentle-engram could not reach the Engram HTTP server/);
    assert.match(result.content[0].text, /Pi-native mem_\* tools are registered/);
    assert.match(result.details.error, /native memory provider is not currently responding/);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await rm(NODE_MODULES, { recursive: true, force: true });
  }
});

test("session-attributed Pi writes bind to acknowledged runtime identity and retry failed registration", async () => {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  let registrationAttempts = 0;
  const observationBodies = [];
  const sessionBodies = [];
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname;
    if (path === "/health") return { ok: true, async json() { return { status: "ok" }; } };
    if (path === "/project/current") {
      return { ok: true, async json() { return { project: "pi", project_source: "dir_basename", project_path: ROOT }; } };
    }
    if (path === "/sessions") {
      registrationAttempts += 1;
      sessionBodies.push(JSON.parse(init.body));
      if (registrationAttempts === 1) {
        return { ok: false, status: 503, async json() { return { error: "registration unavailable" }; } };
      }
      return { ok: true, status: 201, async json() { return { status: "created" }; } };
    }
    if (path === "/observations") {
      observationBodies.push(JSON.parse(init.body));
      return { ok: true, status: 201, async json() { return { id: observationBodies.length }; } };
    }
    return { ok: true, async json() { return {}; } };
  };

  try {
    await installRuntimeStubs();
    const registeredTools = new Map();
    const pluginUrl = pathToFileURL(join(ROOT, "index.ts"));
    pluginUrl.search = `?binding=${Date.now()}`;
    const { default: registerEngram } = await import(pluginUrl.href);
    registerEngram({
      registerTool(tool) { registeredTools.set(tool.name, tool); },
      on() {},
    });

    const memSave = registeredTools.get("mem_save");
    const ctx = {
      cwd: ROOT,
      sessionManager: { getSessionId: () => "runtime-session" },
      ui: { setStatus() {} },
    };
    const params = { title: "runtime binding", content: "content", session_id: "model-invented" };

    const failed = await memSave.execute("call-1", params, undefined, undefined, ctx);
    assert.equal(failed.isError, true);
    assert.equal(observationBodies.length, 0, "unacknowledged registration must stop the write");

    const succeeded = await memSave.execute("call-2", params, undefined, undefined, ctx);
    assert.equal(succeeded.isError, undefined);
    assert.equal(registrationAttempts, 2, "failed registration must remain retryable");
    assert.equal(sessionBodies[1].id, "runtime-session");
    assert.equal(observationBodies[0].session_id, "runtime-session");
    assert.notEqual(observationBodies[0].session_id, "model-invented");

    await memSave.execute("call-3", params, undefined, undefined, ctx);
    assert.equal(registrationAttempts, 2, "successful acknowledgement should be cached");

    const noRuntime = await memSave.execute(
      "call-4",
      params,
      undefined,
      undefined,
      { ...ctx, sessionManager: { getSessionId: () => undefined } },
    );
    assert.equal(noRuntime.isError, true);
    assert.match(noRuntime.content[0].text, /Pi runtime session ID is unavailable/);
    assert.equal(registrationAttempts, 2, "missing runtime identity must not synthesize or register a session");
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await rm(NODE_MODULES, { recursive: true, force: true });
  }
});

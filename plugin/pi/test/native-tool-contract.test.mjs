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

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

// Runs mem_search against a stubbed Engram HTTP server. `searchResponder` returns the Response-like
// object for the /search request; every other endpoint the extension touches during init/project
// detection gets a permissive 200 so the search path is what the assertions actually exercise.
async function runMemSearch(searchResponder) {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.includes("/search")) return searchResponder();
    if (target.includes("/project/current")) return jsonResponse(200, { project: "gentle-agent-state" });
    return jsonResponse(200, {});
  };

  try {
    await installRuntimeStubs();
    const registeredTools = new Map();
    const pluginUrl = pathToFileURL(join(ROOT, "index.ts"));
    pluginUrl.search = `?contract=${Date.now()}-${Math.random()}`;
    const { default: registerEngram } = await import(pluginUrl.href);
    registerEngram({
      registerTool(tool) {
        registeredTools.set(tool.name, tool);
      },
      on() {},
    });

    const memSearch = registeredTools.get("mem_search");
    assert.ok(memSearch, "mem_search tool should be registered");

    return await memSearch.execute(
      "tool-call-search",
      { query: "state markers", project: "gentle-agent-state" },
      undefined,
      undefined,
      {
        cwd: ROOT,
        sessionManager: { getSessionId: () => "test-session" },
        ui: { setStatus() {} },
      },
    );
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
    await rm(NODE_MODULES, { recursive: true, force: true });
  }
}

test("mem_search treats a JSON null (no-hit) response as an empty result, not an outage", async () => {
  const result = await runMemSearch(() => jsonResponse(200, null));

  assert.notEqual(result.isError, true);
  assert.doesNotMatch(result.content[0].text, /could not reach the Engram HTTP server/);
  assert.deepEqual(result.details.data, []);
});

test("mem_search preserves an existing empty array response", async () => {
  const result = await runMemSearch(() => jsonResponse(200, []));

  assert.notEqual(result.isError, true);
  assert.doesNotMatch(result.content[0].text, /could not reach the Engram HTTP server/);
  assert.deepEqual(result.details.data, []);
});

test("mem_search returns populated results unchanged", async () => {
  const hits = [{ id: 1, title: "state markers", type: "discovery" }];
  const result = await runMemSearch(() => jsonResponse(200, hits));

  assert.notEqual(result.isError, true);
  assert.deepEqual(result.details.data, hits);
});

test("mem_search surfaces an HTTP non-2xx response as an error", async () => {
  const result = await runMemSearch(() => jsonResponse(500, { error: "search index offline" }));

  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /search index offline/);
});

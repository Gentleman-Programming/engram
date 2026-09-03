// Behavioral coverage for Pi startup: a real fake `engram` binary is spawned as a child
// process, so these tests exercise the actual spawn/readiness/failure lifecycle instead of
// re-implementing it with stubs.
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { createServer as createHTTPServer } from "node:http";
import { createServer } from "node:net";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { PLUGIN_ROOT as ROOT, RUNTIME_STUB_MARKER, createPluginSandbox, importPluginFromSandbox } from "./plugin-sandbox.mjs";

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => resolve(port));
    });
  });
}

// A fake `engram serve` that logs every invocation, then either dies before readiness or
// starts answering /health after `readyAfterMs` — the slow-health window under test.
async function writeFakeEngramBin(dir, { spawnLog, port, readyAfterMs, exitCode }) {
  const binPath = join(dir, "fake-engram.mjs");
  const script = `#!/usr/bin/env node
import { appendFileSync } from "node:fs";
import { createServer } from "node:http";

if (process.argv[2] === "serve") {
  appendFileSync(${JSON.stringify(spawnLog)}, "serve\\n");
}
${exitCode === undefined
      ? `const server = createServer((req, res) => {
  if (req.url.startsWith("/project/current")) {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ project: "fake-project" }));
    return;
  }
  res.writeHead(200, { "content-type": "application/json" });
  res.end("{}");
});
// The port was picked by a probe socket that has since closed, so another process can win it
// in between. Retry the bind for a bounded window instead of dying on a lost race.
let bindAttempts = 0;
function listen() {
  server.listen(${port}, "127.0.0.1");
}
server.on("error", (error) => {
  if (error.code !== "EADDRINUSE" || bindAttempts >= 40) throw error;
  bindAttempts += 1;
  setTimeout(listen, 25);
});
setTimeout(() => {
  listen();
  // Never outlive the test run, even if the parent forgets this detached child.
  setTimeout(() => { server.close(); process.exit(0); }, 5000);
}, ${readyAfterMs});`
      : `process.exit(${exitCode});`}
`;
  await writeFile(binPath, script, "utf8");
  await chmod(binPath, 0o755);
  return binPath;
}

async function loadPlugin({ engramBin, port, cwd, sandbox }) {
  delete process.env.ENGRAM_URL;
  process.env.ENGRAM_BIN = engramBin;
  process.env.ENGRAM_PORT = String(port);

  const registerEngram = await importPluginFromSandbox(sandbox);

  const tools = new Map();
  const hooks = new Map();
  registerEngram({
    registerTool(tool) {
      tools.set(tool.name, tool);
    },
    on(name, handler) {
      hooks.set(name, handler);
    },
  });

  const statusCalls = [];
  const ctx = {
    cwd,
    sessionManager: { getSessionId: () => "session-startup" },
    ui: { setStatus: (key, text) => statusCalls.push([key, text]) },
  };
  return { tools, hooks, ctx, statusCalls };
}

async function withFixture(options, run) {
  const dir = await mkdtemp(join(tmpdir(), "engram-pi-startup-"));
  const originalBin = process.env.ENGRAM_BIN;
  const originalPort = process.env.ENGRAM_PORT;
  const originalUrl = process.env.ENGRAM_URL;
  let readyServer;
  try {
    const spawnLog = join(dir, "spawns.log");
    await writeFile(spawnLog, "", "utf8");
    const port = await freePort();
    readyServer = options.readyServer && createHTTPServer((request, response) => {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify(request.url.startsWith("/project/current") ? { project: "fake-project" } : {}));
    });
    if (readyServer) await new Promise((resolve, reject) => {
      readyServer.once("error", reject);
      readyServer.listen(port, "127.0.0.1", () => {
        readyServer.off("error", reject);
        resolve();
      });
    });
    const engramBin = options.missingBin
      ? join(dir, "engram-does-not-exist")
      : await writeFakeEngramBin(dir, { spawnLog, port, readyAfterMs: options.readyAfterMs ?? 0, exitCode: options.exitCode });
    const sandbox = await createPluginSandbox(dir);
    const plugin = await loadPlugin({ engramBin, port, cwd: dir, sandbox });
    await run({ ...plugin, spawnLog, dir, port });
  } finally {
    if (originalBin === undefined) delete process.env.ENGRAM_BIN; else process.env.ENGRAM_BIN = originalBin;
    if (originalPort === undefined) delete process.env.ENGRAM_PORT; else process.env.ENGRAM_PORT = originalPort;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
    if (readyServer?.listening) await new Promise((resolve) => readyServer.close(resolve));
    await rm(dir, { recursive: true, force: true });
  }
}

async function countSpawns(spawnLog) {
  const log = await readFile(spawnLog, "utf8");
  return log.split("\n").filter((line) => line === "serve").length;
}

test("an initially healthy Engram provider publishes ready status", async () => {
  await withFixture({ readyServer: true }, async ({ hooks, ctx, statusCalls }) => {
    await hooks.get("session_start")({}, ctx);

    assert.deepEqual(statusCalls, [["engram", "🧠 fake-project · ready"]]);
  });
});

test("an initially unavailable Engram provider publishes offline status", async () => {
  await withFixture({ exitCode: 1 }, async ({ hooks, ctx, statusCalls, dir }) => {
    await hooks.get("session_start")({}, ctx);

    assert.deepEqual(statusCalls, [["engram", `🧠 ${dir.split(/[\\/]/).at(-1).toLowerCase()} · offline`]]);
  });
});

test("a slow health probe never authorizes a duplicate spawn", async () => {
  await withFixture({ readyAfterMs: 600 }, async ({ hooks, ctx, spawnLog }) => {
    const sessionStart = hooks.get("session_start");
    const beforeAgentStart = hooks.get("before_agent_start");
    assert.ok(sessionStart && beforeAgentStart, "startup hooks are registered");

    // Both hooks race into initialization while the child is still starting up.
    await Promise.all([
      sessionStart({}, ctx),
      beforeAgentStart({ systemPrompt: "base", prompt: "hi" }, ctx),
    ]);

    assert.equal(await countSpawns(spawnLog), 1, "concurrent hooks share one spawned server");
  });
});

test("a child that exits before readiness surfaces a normalized tool error", async () => {
  await withFixture({ exitCode: 1 }, async ({ tools, ctx }) => {
    const memSearch = tools.get("mem_search");
    assert.ok(memSearch, "mem_search is registered");

    const result = await memSearch.execute("call-1", { query: "startup" }, undefined, undefined, ctx);

    assert.equal(result.isError, true, "a failed startup is a tool error, not a rejection");
    assert.match(result.content[0].text, /could not initialize the Engram memory provider/);
  });
});

test("a child that exits before readiness never escapes the session hooks", async () => {
  await withFixture({ exitCode: 1 }, async ({ hooks, ctx }) => {
    await assert.doesNotReject(hooks.get("session_start")({}, ctx));
    await assert.doesNotReject(hooks.get("session_compact")({}, ctx));
    await assert.doesNotReject(hooks.get("tool_execution_end")({ toolName: "Read" }, ctx));

    const result = await hooks.get("before_agent_start")({ systemPrompt: "base", prompt: "hello there" }, ctx);
    assert.match(result.systemPrompt, /^base\n\n/, "memory instructions still reach the agent");
  });
});

test("before_agent_start injects actionable mem_search recall guidance", async () => {
  await withFixture({ readyServer: true }, async ({ hooks, ctx }) => {
    const beforeAgentStart = hooks.get("before_agent_start");
    assert.ok(beforeAgentStart, "before_agent_start is registered");

    const result = await beforeAgentStart({ systemPrompt: "base", prompt: "recall past work" }, ctx);
    assert.match(result.systemPrompt, /Start with `mem_context`, then search with 1–2 distinctive keywords/);
    assert.match(result.systemPrompt, /Ordinary `mem_search` is scoped to the detected active project/);
    assert.match(result.systemPrompt, /`match_mode:"all"` means AND/);
    assert.match(result.systemPrompt, /`match_mode:"any"` with\s+`all_projects:true`/);
    assert.match(result.systemPrompt, /If a scoped search is empty, retry once this way/);
    assert.match(result.systemPrompt, /After hits, narrow follow-up searches by project, type, or `match_mode:"all"`/);
    assert.match(result.systemPrompt, /then use `mem_get_observation` for full content/);
  });
});

test("a child that cannot be spawned surfaces a normalized error through tools and hooks", async () => {
  await withFixture({ missingBin: true }, async ({ tools, hooks, ctx }) => {
    await assert.doesNotReject(hooks.get("session_start")({}, ctx));

    const result = await tools.get("mem_save").execute("call-2", { content: "x" }, undefined, undefined, ctx);
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /could not initialize the Engram memory provider/);
  });
});

test("a persistently failing provider does not restart Engram on every tool call", async () => {
  await withFixture({ exitCode: 1 }, async ({ tools, ctx, spawnLog }) => {
    const memSearch = tools.get("mem_search");

    for (let call = 0; call < 50; call += 1) {
      const result = await memSearch.execute(`call-${call}`, { query: "startup" }, undefined, undefined, ctx);
      assert.equal(result.isError, true);
      assert.match(result.content[0].text, /could not initialize the Engram memory provider/);
    }

    const spawns = await countSpawns(spawnLog);
    assert.ok(spawns >= 1, "the first call still attempts a real startup");
    assert.ok(spawns <= 2, `50 failing tool calls produced ${spawns} startup attempts, not a bounded retry`);
  });
});

test("loading the plugin leaves the checkout's node_modules untouched", async () => {
  await withFixture({ exitCode: 1 }, async ({ hooks, ctx }) => {
    await assert.doesNotReject(hooks.get("session_start")({}, ctx));
  });

  for (const stub of [
    join(ROOT, "node_modules", "typebox", "index.js"),
    join(ROOT, "node_modules", "@earendil-works", "pi-tui", "index.js"),
  ]) {
    if (!existsSync(stub)) continue;
    assert.doesNotMatch(
      await readFile(stub, "utf8"),
      new RegExp(RUNTIME_STUB_MARKER),
      `${stub} was replaced by a test double; the suite must not write into the real node_modules`,
    );
  }
});

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
const digest = (prompt) => createHash("sha256").update(prompt.trim(), "utf8").digest("hex");
import { test } from "node:test";
import { withPluginSandbox, importPluginFromSandbox } from "./plugin-sandbox.mjs";

const CLAIM = "engram:bridge:claim:v1";
const DECISION = "engram:bridge:decision:v1";
function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}
function bus() {
  const listeners = new Map();
  return {
    on(channel, handler) {
      const set = listeners.get(channel) || new Set();
      listeners.set(channel, set); set.add(handler);
      return () => set.delete(handler);
    },
    emit(channel, data) { for (const handler of listeners.get(channel) || []) handler(data); },
    count(channel) { return listeners.get(channel)?.size || 0; },
  };
}
async function fixture(body) {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  let registration = async (id) => ({ id, status: "created" });
  let detectedProject = "engram";
  let promptResponse = async () => new Response('{"id":1,"status":"saved"}', { status: 201 });
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const payload = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ path, payload });
    if (path === "/project/current") return new Response(JSON.stringify({ project: detectedProject }));
    if (path === "/sessions") return new Response(JSON.stringify(await registration(payload.id)));
    if (path === "/prompts") return promptResponse(payload);
    return new Response('{}');
  };
  try {
    await withPluginSandbox("engram-pi-ownership-", async ({ sandbox }) => {
      const hooks = new Map();
      const events = bus();
      const entries = [];
      const register = await importPluginFromSandbox(sandbox);
      register({ events, registerTool() {}, on(name, handler) { hooks.set(name, handler); },
        appendEntry(customType, data) { entries.push({ type: "custom", customType, data }); } });
      let id = " opaque Pi ID ";
      const ctx = { cwd: sandbox, sessionManager: { getSessionId: () => id, getBranch: () => entries } };
      const replies = [];
      events.on(DECISION, (reply) => replies.push(reply));
      const claim = (overrides = {}) => events.emit(CLAIM, { runtimeSessionId: id, nonce: "query-1", operation: "session-register", ...overrides });
      const reply = () => new Promise((resolve, reject) => {
        const timer = setTimeout(() => { off(); reject(new Error("no ownership decision")); }, 1500);
        const off = events.on(DECISION, (data) => { clearTimeout(timer); off(); resolve(data); });
      });
      await body({ hooks, events, ctx, calls, replies, claim, reply,
        setId(value) { id = value; }, setRegistration(value) { registration = value; },
        setProject(value) { detectedProject = value; },
        setPromptResponse(value) { promptResponse = value; } });
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalUrl;
  }
}

test("claim confirms native registration, correlates opaque identity, and joins pending registration", async () => {
  await fixture(async ({ hooks, ctx, events, calls, replies, claim, reply, setRegistration }) => {
    await hooks.get("session_start")({}, ctx);
    assert.equal(events.count(CLAIM), 1, "session lifecycle installs ownership responder");
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 0, "UI initialization is not registration");
    const gate = deferred();
    setRegistration(async (id) => { await gate.promise; return { id, status: "created" }; });
    const first = reply(); claim();
    const second = reply(); claim({ nonce: "query-2", operation: "session-register" });
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(replies.length, 0, "pending acknowledgement cannot claim ownership");
    gate.resolve();
    assert.deepEqual(await first, { runtimeSessionId: " opaque Pi ID ", nonce: "query-1", operation: "session-register", status: "owned" });
    await second;
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 1, "queries join the existing registration flight");
    assert.equal(replies[1].nonce, "query-2");
    assert.equal(replies[1].operation, "session-register");
  });
});

test("foreign identity and invalid wire fields cannot register; reload removes listener without ending persistence", async () => {
  await fixture(async ({ hooks, ctx, events, calls, claim, replies }) => {
    claim(); assert.equal(events.count(CLAIM), 0, "factory owns no responder");
    await hooks.get("session_start")({}, ctx);
    claim({ runtimeSessionId: "opaque Pi ID" });
    claim({ nonce: "" }); claim({ nonce: 7 }); claim({ nonce: "x".repeat(257) }); claim({ operation: "anything" });
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(replies.length, 0);
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 0);
    await hooks.get("session_shutdown")({ reason: "reload" }, ctx);
    assert.equal(events.count(CLAIM), 0);
    assert.equal(calls.filter((call) => call.path.endsWith("/end")).length, 0);
  });
});

test("failed registration is unknown and stale native context never registers foreign ID", async () => {
  await fixture(async ({ hooks, ctx, calls, claim, reply, setRegistration, setId, events }) => {
    await hooks.get("session_start")({}, ctx);
    setRegistration(async () => ({ status: "created" }));
    const failure = reply(); claim();
    assert.equal((await failure).status, "unknown");
    setId("foreign-new-ID");
    const stale = reply(); claim({ runtimeSessionId: " opaque Pi ID " });
    assert.equal((await stale).status, "unknown");
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 1);
    await hooks.get("session_shutdown")({}, ctx);
    assert.equal(events.count(CLAIM), 0);
  });
});

test("new native session independently confirms ownership and quit removes responder", async () => {
  await fixture(async ({ hooks, ctx, calls, claim, reply, setId, events }) => {
    await hooks.get("session_start")({}, ctx);
    const first = reply(); claim(); assert.equal((await first).status, "owned");
    await hooks.get("session_shutdown")({}, ctx);
    assert.equal(events.count(CLAIM), 0);
    setId("independent Pi ID");
    await hooks.get("session_start")({}, ctx);
    const second = reply(); claim(); assert.equal((await second).status, "owned");
    assert.deepEqual(calls.filter((call) => call.path === "/sessions").map((call) => call.payload.id), [" opaque Pi ID ", "independent Pi ID"]);
    await hooks.get("session_shutdown")({}, ctx);
    assert.equal(events.count(CLAIM), 0);
  });
});

test("unresolved project cannot claim ownership or start registration", async () => {
  await fixture(async ({ hooks, ctx, calls, claim, reply, setProject }) => {
    setProject("unknown");
    await hooks.get("session_start")({}, ctx);
    const decision = reply(); claim(); assert.equal((await decision).status, "unknown");
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 0);
  });
});

test("bounded pending registration returns unknown without late positive delivery", async () => {
  await fixture(async ({ hooks, ctx, replies, calls, claim, reply, setRegistration }) => {
    await hooks.get("session_start")({}, ctx);
    const gate = deferred();
    setRegistration(async (id) => { await gate.promise; return { id, status: "created" }; });
    const decision = reply(); claim();
    assert.equal((await decision).status, "unknown");
    gate.resolve();
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(replies.length, 1);
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 1);
  });
});

test("prompt ownership requires confirmed current-turn matching capture, never registration alone", async () => {
  await fixture(async ({ hooks, ctx, calls, claim, reply, setPromptResponse }) => {
    await hooks.get("session_start")({}, ctx);
    const ask = async (prompt, extra = {}) => {
      const decision = reply();
      claim({ operation: "prompt-capture", promptDigest: digest(prompt), ...extra });
      return decision;
    };
    await hooks.get("before_agent_start")({ prompt: "continue", systemPrompt: "" }, ctx);
    assert.equal((await ask("continue")).status, "unknown");
    assert.equal(calls.filter((call) => call.path === "/prompts").length, 0);
    assert.equal(calls.filter((call) => call.path === "/sessions").length, 0, "capture claim must not register");
    const long = "  a sufficiently long native prompt <private>secret</private> " + "x".repeat(2100);
    await hooks.get("before_agent_start")({ prompt: long, systemPrompt: "" }, ctx);
    const captured = calls.find((call) => call.path === "/prompts").payload.content;
    assert.ok(!captured.includes("secret"));
    assert.equal(captured.length, 2003, "existing truncation keeps 2000 units plus ellipsis");
    const owned = await ask(long);
    assert.equal(owned.status, "owned");
    assert.equal(owned.promptDigest, digest(long));
    assert.equal((await ask("another sufficiently long prompt")).status, "unknown");
    assert.equal((await ask(long, { promptDigest: undefined })).status, "unknown");
    assert.equal((await ask(long, { promptDigest: "bad" })).status, "unknown");
    await hooks.get("before_agent_start")({ prompt: "continue", systemPrompt: "" }, ctx);
    assert.equal((await ask(long)).status, "unknown", "short turn clears prior proof");
    await hooks.get("before_agent_start")({ prompt: long, systemPrompt: "" }, ctx);
    assert.equal((await ask(long)).status, "owned");
    setPromptResponse(async () => new Response('{"error":"rejected"}', { status: 400 }));
    await hooks.get("before_agent_start")({ prompt: long, systemPrompt: "" }, ctx);
    assert.equal((await ask(long)).status, "unknown", "failed repeated prompt cannot reuse proof");
    setPromptResponse(async () => new Response('{}'));
    await hooks.get("before_agent_start")({ prompt: long, systemPrompt: "" }, ctx);
    assert.equal((await ask(long)).status, "unknown", "empty success is not a positive saved acknowledgement");
  });
});

test("late prompt acknowledgement cannot restore prior-turn proof", async () => {
  await fixture(async ({ hooks, ctx, claim, reply, setPromptResponse }) => {
    await hooks.get("session_start")({}, ctx);
    const started = deferred();
    const gate = deferred();
    setPromptResponse(async () => { started.resolve(); await gate.promise; return new Response('{"id":1,"status":"saved"}', { status: 201 }); });
    const prompt = "a repeated sufficiently long prompt";
    const oldTurn = hooks.get("before_agent_start")({ prompt, systemPrompt: "" }, ctx);
    await started.promise;
    setPromptResponse(async () => new Response('null'));
    await hooks.get("before_agent_start")({ prompt, systemPrompt: "" }, ctx);
    gate.resolve(); await oldTurn;
    const decision = reply(); claim({ operation: "prompt-capture", promptDigest: digest(prompt) });
    assert.equal((await decision).status, "unknown");
  });
});

test("prompt proof is invalidated by reload and shutdown lifecycle changes", async () => {
  await fixture(async ({ hooks, ctx, claim, reply, events }) => {
    const prompt = "a long prompt for lifecycle confirmation";
    const ask = async () => { const decision = reply(); claim({ operation: "prompt-capture", promptDigest: digest(prompt) }); return decision; };
    await hooks.get("session_start")({}, ctx);
    await hooks.get("before_agent_start")({ prompt, systemPrompt: "" }, ctx);
    assert.equal((await ask()).status, "owned");
    await hooks.get("session_shutdown")({ reason: "reload" }, ctx);
    assert.equal(events.count(CLAIM), 0);
    await hooks.get("session_start")({}, ctx);
    assert.equal((await ask()).status, "unknown");
    await hooks.get("before_agent_start")({ prompt, systemPrompt: "" }, ctx);
    assert.equal((await ask()).status, "owned");
    await hooks.get("session_shutdown")({}, ctx);
    await hooks.get("session_start")({}, ctx);
    assert.equal((await ask()).status, "unknown");
  });
});

test("reload cancels pending response and new lifecycle installs only one responder", async () => {
  await fixture(async ({ hooks, ctx, events, replies, claim, setRegistration }) => {
    await hooks.get("session_start")({}, ctx);
    const gate = deferred();
    setRegistration(async (id) => { await gate.promise; return { id, status: "created" }; });
    claim();
    await new Promise((resolve) => setTimeout(resolve, 20));
    await hooks.get("session_shutdown")({ reason: "reload" }, ctx);
    gate.resolve();
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(replies.length, 0);
    await hooks.get("session_start")({}, ctx);
    assert.equal(events.count(CLAIM), 1);
    await hooks.get("session_shutdown")({ reason: "reload" }, ctx);
  });
});

import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// Fixed registration substitute, not an installed Pi SDK. Original HTTP assertions
// come from plugin/pi/test/real-agent-workflows.test.mjs; no fixture server is used.
export async function runTopicVectors(register) {
  const project = "integration-1348";
  mkdirSync(join(process.cwd(), ".engram"), { mode: 0o700 });
  writeFileSync(join(process.cwd(), ".engram", "config.json"), JSON.stringify({ project_name: project }), { mode: 0o600, flag: "wx" });
  const tools = new Map(), events = new Map(), entries = [];
  register({ registerTool(tool) { tools.set(tool.name, tool); }, on(name, callback) { events.set(name, callback); },
    appendEntry(customType, data) { entries.push({ type: "custom", customType, data }); } });
  const ctx = { cwd: process.cwd(), sessionManager: { getSessionId: () => "real-host", getBranch: () => entries }, ui: { setStatus() {} } };
  const tool = async (name, params) => {
    assert.ok(tools.has(name), `${name} is registered`);
    return tools.get(name).execute("real-workflow", params, undefined, undefined, ctx);
  };
  const request = async (path, body, authorized = true) => {
    const response = await fetch(new URL(path, process.env.ENGRAM_URL), { method: body === undefined ? "GET" : "POST",
      headers: { "Content-Type": "application/json", ...(authorized ? { Authorization: `Bearer ${process.env.ENGRAM_HTTP_TOKEN}` } : {}) },
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000), redirect: "error" });
    return { status: response.status, data: await response.json() };
  };
  const rows = async () => {
    const result = await request("/observations?all_projects=true&limit=100");
    assert.equal(result.status, 200); return result.data;
  };
  for (const params of [{ type: "architecture", title: "Auth model" }, { type: "bugfix", title: "Fix café / Unicode" },
    { type: "decision", content: "Use SQLite for local state" }, { title: "X".repeat(120) }]) {
    const expected = await request("/topic-keys/suggest", params);
    assert.equal(expected.status, 200);
    const actual = await tool("mem_suggest_topic_key", params);
    assert.notEqual(actual.isError, true, JSON.stringify(actual));
    assert.deepEqual(actual.details.data, expected.data);
    if (params.title === "Auth model") assert.equal(expected.data.topic_key, "architecture/auth-model");
  }
  const invalid = await tool("mem_suggest_topic_key", {});
  assert.equal(invalid.isError, true); assert.equal(invalid.details.http_status, 400);
  assert.equal(invalid.content[0].text, "provide title or content to suggest a topic_key");
  assert.deepEqual(await rows(), []);
  assert.equal((await request("/sessions/real-host")).status, 404);
}

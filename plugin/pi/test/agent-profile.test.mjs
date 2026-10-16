import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

async function registeredTools(sandbox) {
  const tools = new Map();
  const register = await importPluginFromSandbox(sandbox);
  register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
  return tools;
}

// Read the current Go profile rather than freezing a second inventory in JavaScript.
async function goAgentNames() {
  const source = await readFile(new URL("../../../internal/mcp/mcp.go", import.meta.url), "utf8");
  const block = source.match(/var ProfileAgent = map\[string\]bool\{([\s\S]*?)\n\}/);
  assert.ok(block, "Go ProfileAgent declaration must be identifiable");
  return [...block[1].matchAll(/"(mem_[^"]+)":\s*true/g)].map((match) => match[1]).sort();
}

test("Pi default registration exactly matches Go ProfileAgent", async () => {
  await withPluginSandbox("engram-agent-profile-", async ({ sandbox }) => {
    const tools = await registeredTools(sandbox);
    assert.deepEqual([...tools.keys()].sort(), await goAgentNames());
    for (const tool of tools.values()) {
      assert.equal(typeof tool.execute, "function", `${tool.name} remains executable`);
      assert.ok(tool.parameters, `${tool.name} retains its schema`);
      assert.equal(typeof tool.renderCall, "function");
      assert.equal(typeof tool.renderResult, "function");
    }
  });
});

test("Pi default registration exposes no administrative tools or hard delete", async () => {
  await withPluginSandbox("engram-agent-admin-", async ({ sandbox }) => {
    const tools = await registeredTools(sandbox);
    for (const name of ["mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects"]) {
      assert.equal(tools.has(name), false, `${name} must require explicit administrative access`);
    }
    for (const tool of tools.values()) {
      assert.equal(Object.hasOwn(tool.parameters.args[0], "hard_delete"), false);
    }
  });
});

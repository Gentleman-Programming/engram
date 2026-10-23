import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const artifactPath = join(root, "docs/contracts/agent-contract-v1.json");
const hash = (raw) => createHash("sha256").update(raw).digest("hex");

test("existing CLI verifies real Pi registration without writing", async (t) => {
  const go = spawnSync("go", ["version"], {
    cwd: root, encoding: "utf8", timeout: 5_000,
    env: { ...process.env, GOPROXY: "off", GOSUMDB: "off" },
  });
  if (go.error?.code === "ENOENT") {
    t.skip("Go toolchain unavailable");
    return;
  }
  assert.ifError(go.error);
  assert.equal(go.status, 0, `Go toolchain check failed: ${go.stderr}`);
  let artifact;
  try {
    artifact = await readFile(artifactPath);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
    t.skip("repository contract artifact unavailable");
    return;
  }
  await withPluginSandbox("engram-pi-cli-", async ({ dir, sandbox }) => {
    const binary = join(dir, process.platform === "win32" ? "agent-contract.exe" : "agent-contract");
    const build = spawnSync("go", ["build", "-o", binary, "./tools/agent-contract"], {
      cwd: root, encoding: "utf8", timeout: 60_000,
      env: { ...process.env, GOPROXY: "off", GOSUMDB: "off" },
    });
    assert.ifError(build.error);
    assert.deepEqual([build.status, build.stdout, build.stderr], [0, "", ""]);
    const registrations = [];
    const register = await importPluginFromSandbox(sandbox);
    register({ registerTool({ name, parameters }) { registrations.push({ name, parameters }); }, on() {} });
    const capture = JSON.stringify(registrations);
    const workspace = join(dir, "workspace");
    await mkdir(join(workspace, "docs/contracts"), { recursive: true });
    const localArtifact = join(workspace, "docs/contracts/agent-contract-v1.json");
    await writeFile(localArtifact, artifact);
    const path = join(workspace, "capture.json");
    const run = async (raw, code, stderr) => {
      await writeFile(path, raw);
      const result = spawnSync(binary, ["-verify-pi", path], {
        cwd: workspace, encoding: "utf8", timeout: 20_000,
        env: { ...process.env, ENGRAM_DATA_DIR: join(workspace, "must-not-exist") },
      });
      assert.ifError(result.error);
      assert.deepEqual([result.status, result.stdout, result.stderr], [code, "", stderr]);
      assert.equal(hash(await readFile(path)), hash(raw), "capture unchanged");
      assert.deepEqual(await readFile(localArtifact), artifact, "local artifact unchanged");
      assert.deepEqual((await readdir(workspace)).sort(), ["capture.json", "docs"], "no store or other writes");
      assert.deepEqual(await readdir(join(workspace, "docs")), ["contracts"]);
      assert.deepEqual(await readdir(join(workspace, "docs/contracts")), ["agent-contract-v1.json"]);
    };
    await t.test("accept", () => run(capture, 0, ""));
    for (const [name, tool, mutate] of [
      ["missing-field", "mem_search", (s) => { delete s.properties.query; }],
      ["extra-description-property", "mem_search", (s) => { s.properties.description = { type: "string" }; }],
      ["type", "mem_search", (s) => { s.properties.query.type = "number"; }],
      ["required", "mem_search", (s) => { delete s.required; }],
      ["max-bytes", "mem_context", (s) => { s.properties.max_bytes.minimum = 0; }],
      ["batch-bound", "mem_judge_many", (s) => { s.properties.judgments.maxItems = 33; }],
      ["consolidation-constraint", "mem_consolidate", (s) => { delete s.properties.ids_to_delete.uniqueItems; }],
      ["unapproved-keyword", "mem_search", (s) => { s.additionalProperties = false; }],
      ["host-session-injection", "mem_session_finalize", (s) => { s.properties.session_id = { type: "string" }; }],
    ]) {
      await t.test(name, async () => {
        const tools = JSON.parse(capture);
        const registration = tools.find(({ name }) => name === tool);
        assert.ok(registration);
        mutate(registration.parameters);
        await run(JSON.stringify(tools), 1, `pi input: ${tool} schema differs\n`);
      });
    }
    const tools = JSON.parse(capture);
    for (const [name, value, error] of [
      ["missing-tool", JSON.stringify(tools.slice(1)), "inventory differs"],
      ["extra-tool", JSON.stringify([...tools, { name: "mem_unknown", parameters: {} }]), "inventory differs"],
      ["duplicate", JSON.stringify([...tools, tools[0]]), "duplicate registration"],
      ["invalid-json", "[", "invalid capture"],
      ["trailing-json", capture + " null", "invalid capture"],
    ]) await t.test(name, () => run(value, 1, `pi input: ${error}\n`));
    t.diagnostic(`real TypeBox 1.3.34 capture SHA256=${hash(capture)} artifact SHA256=${hash(artifact)}`);
  }, { realTypeBox: true });
  assert.deepEqual(await readFile(artifactPath), artifact, "checkout artifact unchanged");
});

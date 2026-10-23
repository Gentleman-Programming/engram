import assert from "node:assert/strict";
import { test } from "node:test";
import { spawnSync } from "node:child_process";
import { readFile, readdir, mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { withPluginSandbox, importPluginFromSandbox } from "./plugin-sandbox.mjs";

const cli = new URL("../../../tools/pi-schema-diagnostic.mjs", import.meta.url);
const run = (...args) => spawnSync(process.execPath, [fileURLToPath(cli), ...args], { encoding: "utf8", timeout: 8000 });

test("public diagnostic is deterministic, real, and read-only", async () => {
  const baseline = "../../../docs/contracts/agent-contract-v1.json";
  const paths = ["../index.ts", "../package.json", "../package-lock.json", "../node_modules/typebox/package.json", baseline];
  const snapshot = () => Promise.all(paths.map((p) => readFile(new URL(p, import.meta.url), "utf8")));
  const before = await snapshot();
  const temps = (await readdir(tmpdir())).filter((p) => p.startsWith("engram-pi-schema-"));
  // Live schemas can converge; assert the complete report, not permanent gaps.
  // Independent controlled cases below pin the comparator's expected differences.
  const { compareSchemas } = await import(cli);
  const contract = JSON.parse(await readFile(new URL(baseline, import.meta.url), "utf8"));
  const registrations = await withPluginSandbox("engram-pi-schema-", async ({ sandbox }) => {
    const tools = [];
    (await importPluginFromSandbox(sandbox))({ registerTool: (tool) => tools.push(tool), on() {} });
    return tools;
  }, { realTypeBox: true });
  const lines = compareSchemas(contract, registrations);
  const expected = `Diagnostic only; not parity or release approval.\nStructural differences: ${lines.length}\n${lines.length ? lines.join("\n") + "\n" : ""}`;
  for (let attempt = 0; attempt < 2; attempt++) {
    const result = run();
    assert.equal(result.status, lines.length ? 1 : 0);
    assert.equal(result.stderr, "");
    assert.equal(result.stdout, expected);
  }
  assert.deepEqual(await snapshot(), before);
  assert.deepEqual((await readdir(tmpdir())).filter((p) => p.startsWith("engram-pi-schema-")), temps);
});

test("help and unsupported options have exact public boundaries", () => {
  const help = run("--help");
  assert.equal(help.status, 0);
  assert.equal(help.stderr, "");
  assert.equal(help.stdout, "Usage: node tools/pi-schema-diagnostic.mjs [--help]\nDiagnostic only; not parity or release approval.\nExit 0: no structural differences (or help); 1: differences; 2: failure.\n");
  for (const args of [["--root", "/private"], ["--help", "--help"], ["--json"], ["positional"]]) {
    const result = run(...args);
    assert.equal(result.status, 2);
    assert.equal(result.stdout, "");
    assert.equal(result.stderr, "Pi schema diagnostic failed: unsupported arguments.\n");
  }
});

test("structural comparison retains constraints, duplicates and inventory", async () => {
  const { compareSchemas } = await import(cli);
  const schema = { type: "object", properties: { id: { type: "number" }, lost: { default: false }, older_history: { type: "boolean", description: "Go wording" } }, required: ["id"], additionalProperties: false };
  const contract = { operations: { mem_a: { input_schema: schema }, mem_missing: { input_schema: {} } } };
  const tools = [{ name: "mem_a", parameters: { type: "object", properties: { id: { type: "integer", enum: [1], anyOf: [{ const: 1 }] }, older_history: { type: "boolean", description: "Pi wording" } } } }, { name: "mem_a", parameters: {} }, { name: "mem_extra", parameters: {} }];
  for (const [baseline, capture] of [[null, tools], [{ operations: {} }, tools], [contract, []], [contract, [{ name: "private/path", parameters: {} }]], [contract, [{ name: "mem_a" }]]]) {
    assert.throws(() => compareSchemas(baseline, capture));
  }
  const lines = compareSchemas(contract, tools);
  for (const part of ["/additionalProperties", "/required", "/properties/lost", "/properties/id/type", "/properties/id/enum", "/properties/id/anyOf", "duplicate", "mem_missing", "mem_extra"]) assert.ok(lines.some((line) => line.includes(part)), part);
  assert.ok(lines.includes('mem_a /registrations/0/input_schema/properties/id/type: Go="number" Pi="integer"'));
  assert.ok(lines.includes('mem_a /registrations/0/input_schema/properties/lost: Go={"default":false} Pi=<missing>'));
  assert.ok(lines.includes('mem_a /registrations/0/input_schema/properties/older_history/description: Go="Go wording" Pi="Pi wording"'));
  const missing = compareSchemas({ operations: { mem_a: { input_schema: schema } } }, [{ name: "mem_a", parameters: { properties: {} } }]);
  assert.ok(missing.includes('mem_a /input_schema/properties/older_history: Go={"type":"boolean","description":"Go wording"} Pi=<missing>'));
  assert.deepEqual(compareSchemas({ operations: { mem_a: { input_schema: { b: 1, a: 2 } } } }, [{ name: "mem_a", parameters: { a: 2, b: 1 } }]), []);
});

test("default sandbox remains lightweight stub; explicit option uses real schema", async () => {
  for (const realTypeBox of [false, true]) await withPluginSandbox("engram-pi-schema-", async ({ sandbox }) => {
    const tools = [];
    (await importPluginFromSandbox(sandbox))({ registerTool: (tool) => tools.push(tool), on() {} });
    const schema = tools.find((tool) => tool.name === "mem_context").parameters;
    assert.equal(realTypeBox ? schema.type : schema.kind, realTypeBox ? "object" : "Object");
    if (realTypeBox) assert.equal(schema.properties.max_bytes.type, "integer");
  }, { realTypeBox });
});

test("CI installs test dependencies safely before Pi tests and retains candidate safeguards", async () => {
  for (const name of ["ci", "publish-pi"]) {
    const text = await readFile(new URL(`../../../.github/workflows/${name}.yml`, import.meta.url), "utf8");
    assert.match(text, /working-directory: plugin\/pi\s+run: npm ci --ignore-scripts --legacy-peer-deps/);
    assert.ok(text.indexOf("npm ci --ignore-scripts --legacy-peer-deps") < text.indexOf("run: npm test", name === "ci" ? text.indexOf("  plugin-tests:") : 0));
    if (name === "publish-pi") {
      assert.match(text, /run: npm publish --provenance --access public --tag candidate/);
      assert.match(text, /id-token: write/);
      assert.match(text, /run: node test\/release-contract.mjs "\$RELEASE_REF"/);
      assert.match(text, /- "pi-v\*"/);
    }
  }
});

test("public diagnostic accepts only known numeric baseline revisions", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "engram-pi-revision-"));
  try {
    await mkdir(join(dir, "tools"));
    await mkdir(join(dir, "docs", "contracts"), { recursive: true });
    const source = await readFile(cli, "utf8");
    const originalImport = '"../plugin/pi/test/plugin-sandbox.mjs"';
    assert.equal(source.split(originalImport).length, 2);
    // Relocate only the fixture's helper import; run the actual CLI logic and
    // actual installed TypeBox against an isolated baseline file.
    const fixtureCLI = join(dir, "tools", "pi-schema-diagnostic.mjs");
    await writeFile(fixtureCLI, source.replace(originalImport,
      JSON.stringify(new URL("./plugin-sandbox.mjs", import.meta.url).href)));
    const contract = JSON.parse(await readFile(new URL("../../../docs/contracts/agent-contract-v1.json", import.meta.url), "utf8"));
    const { compareSchemas } = await import(cli);
    const registrations = await withPluginSandbox("engram-pi-revision-capture-", async ({ sandbox }) => {
      const tools = [];
      (await importPluginFromSandbox(sandbox))({ registerTool: (tool) => tools.push(tool), on() {} });
      return tools;
    }, { realTypeBox: true });
    const lines = compareSchemas(contract, registrations);
    const expected = `Diagnostic only; not parity or release approval.\nStructural differences: ${lines.length}\n${lines.length ? lines.join("\n") + "\n" : ""}`;
    const baseline = join(dir, "docs", "contracts", "agent-contract-v1.json");
    for (const revision of [1, 2, 3, "2"]) {
      await t.test(`baseline revision ${JSON.stringify(revision)}`, async () => {
        const raw = JSON.stringify({ ...contract, revision });
        await writeFile(baseline, raw);
        const result = spawnSync(process.execPath, [fixtureCLI], { encoding: "utf8", timeout: 8000 });
        if (revision === 1 || revision === 2) {
          assert.equal(result.status, lines.length ? 1 : 0);
          assert.equal(result.stderr, "");
          assert.equal(result.stdout, expected);
        } else {
          assert.equal(result.status, 2);
          assert.equal(result.stdout, "");
          assert.equal(result.stderr, "Pi schema diagnostic failed: baseline, real TypeBox, capture or cleanup unavailable.\n");
        }
        assert.equal(await readFile(baseline, "utf8"), raw, "diagnostic must not rewrite its baseline");
      });
    }
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

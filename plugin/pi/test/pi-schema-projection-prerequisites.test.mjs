import assert from "node:assert/strict";
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { test } from "node:test";
import { createPluginSandbox } from "./plugin-sandbox.mjs";

// Exercise the actual test entry point in package-shaped, isolated directories.
// A copied Node executable stands in for Go; no Go install or network is needed.
for (const scenario of [
  { name: "missing Go", go: "missing", artifact: "file", code: 0, skip: "Go toolchain unavailable" },
  { name: "missing repository artifact", go: "available", artifact: "missing", code: 0, skip: "repository contract artifact unavailable" },
  { name: "failed Go command is not absence", go: "failed", artifact: "file", code: 1 },
  { name: "artifact read error is not absence", go: "available", artifact: "directory", code: 1 },
]) {
  test(`projection prerequisite: ${scenario.name}`, async () => {
    const dir = await mkdtemp(join(tmpdir(), "engram-pi-prerequisite-"));
    try {
      const tests = join(dir, "plugin/pi/test");
      const bin = join(dir, "bin");
      await mkdir(join(dir, "plugin"));
      const sandbox = await createPluginSandbox(join(dir, "plugin"));
      await rename(sandbox, join(dir, "plugin/pi"));
      await mkdir(tests, { recursive: true });
      await mkdir(bin);
      for (const name of ["pi-schema-projections.test.mjs", "plugin-sandbox.mjs"]) {
        await copyFile(new URL(name, import.meta.url), join(tests, name));
      }
      const artifact = join(dir, "docs/contracts/agent-contract-v1.json");
      if (scenario.artifact !== "missing") {
        await mkdir(join(dir, "docs/contracts"), { recursive: true });
        if (scenario.artifact === "directory") await mkdir(artifact);
        else await writeFile(artifact, "{}\n");
      }
      if (scenario.go !== "missing") {
        const go = join(bin, process.platform === "win32" ? "go.exe" : "go");
        await copyFile(process.execPath, go);
        await chmod(go, 0o700);
        await writeFile(join(dir, "version"), `process.exit(${scenario.go === "failed" ? 1 : 0});\n`);
      }
      const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => name.toUpperCase() !== "PATH" && name !== "NODE_TEST_CONTEXT"));
      env.PATH = bin;
      env.GOPROXY = "off";
      env.GOSUMDB = "off";
      const result = spawnSync(process.execPath, ["--test", "--test-reporter=tap", join(tests, "pi-schema-projections.test.mjs")], {
        cwd: dir, env, encoding: "utf8", timeout: 20_000,
      });
      assert.ifError(result.error);
      assert.equal(result.status, scenario.code, result.stdout + result.stderr);
      assert.equal(result.stderr, "");
      if (scenario.skip) assert.ok(result.stdout.includes(`# SKIP ${scenario.skip}`), result.stdout);
      else assert.ok(!result.stdout.includes("# SKIP"), result.stdout);
      if (scenario.artifact === "file") assert.equal(await readFile(artifact, "utf8"), "{}\n");
      const expected = ["bin", "plugin", ...(scenario.artifact === "missing" ? [] : ["docs"]), ...(scenario.go === "missing" ? [] : ["version"])];
      assert.deepEqual((await readdir(dir)).sort(), expected.sort(), "no build or store writes");
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
}

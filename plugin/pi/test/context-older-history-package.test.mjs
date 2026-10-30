import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { PLUGIN_ROOT } from "./plugin-sandbox.mjs";

const reason = "requires the Engram Go checkout or ENGRAM_PI_TEST_SERVER_BINARY";
const fixtures = ["go.mod", "internal/server/server.go", "internal/store/store.go", "plugin/pi/test/support/real-server/main.go"];

function scenario(run) {
  const dir = mkdtempSync(join(tmpdir(), "engram-context-package-"));
  try {
    const consumer = join(dir, "consumer"), pkg = join(consumer, "node_modules", "gentle-engram");
    mkdirSync(pkg, { recursive: true });
    writeFileSync(join(consumer, "go.mod"), "module example.com/consumer\n");
    const metadata = JSON.parse(readFileSync(join(PLUGIN_ROOT, "package.json"), "utf8"));
    for (const entry of metadata.files) {
      const source = join(PLUGIN_ROOT, entry);
      if (existsSync(source)) cpSync(source, join(pkg, entry), { recursive: true });
    }
    const temp = join(dir, "temp"), calls = join(dir, "calls.json"), hook = join(dir, "build-hook.mjs");
    mkdirSync(temp);
    writeFileSync(hook, `import cp from "node:child_process";
import fs from "node:fs";
import fsp from "node:fs/promises";
import { basename } from "node:path";
import { syncBuiltinESMExports } from "node:module";
function record(kind, value) {
  const file = process.env.ENGRAM_BUILD_CALLS;
  const calls = JSON.parse(fs.readFileSync(file, "utf8"));
  calls[kind].push(value);
  fs.writeFileSync(file, JSON.stringify(calls));
}
const originalSpawn = cp.spawnSync;
cp.spawnSync = (command, ...args) => {
  if (command !== "go") return originalSpawn(command, ...args);
  record("builds", { args: args[0], cwd: args[1]?.cwd });
  return { status: 77, stdout: "", stderr: "fixture Go build failure sentinel", error: undefined };
};
const originalTemp = fsp.mkdtemp;
fsp.mkdtemp = (prefix, ...args) => {
  record("sandboxes", basename(prefix));
  return originalTemp(prefix, ...args);
};
syncBuiltinESMExports();
`);
    const execute = (root, binary, selection = ["test/context-older-history.test.mjs"]) => {
      writeFileSync(calls, JSON.stringify({ builds: [], sandboxes: [] }));
      const env = { ...process.env, ENGRAM_BUILD_CALLS: calls, TEMP: temp, TMP: temp, TMPDIR: temp };
      delete env.NODE_TEST_CONTEXT;
      delete env.ENGRAM_PI_TEST_SERVER_BINARY;
      if (binary !== undefined) env.ENGRAM_PI_TEST_SERVER_BINARY = binary;
      const result = spawnSync(process.execPath, ["--import", pathToFileURL(hook).href,
        "--test", "--test-reporter=tap", ...selection],
      { cwd: root, env, encoding: "utf8", timeout: 60000, maxBuffer: 4000000 });
      assert.ifError(result.error);
      assert.equal(result.signal, null);
      assert.equal(result.stderr, "");
      assert.deepEqual(readdirSync(temp), []);
      return { ...result, ...JSON.parse(readFileSync(calls, "utf8")) };
    };
    run({ pkg, consumer, execute });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function summary(result, pass, fail, skipped) {
  for (const [name, count] of Object.entries({ tests: 3, pass, fail, skipped, cancelled: 0 })) {
    assert.match(result.stdout, new RegExp(`^# ${name} ${count}$`, "m"));
  }
}

test("packaged context older-history skips real fixture before build or sandbox", () => {
  scenario(({ pkg, execute }) => {
    const result = execute(pkg);
    assert.equal(result.status, 0, result.stdout);
    summary(result, 2, 0, 1);
    assert.ok(result.stdout.includes(`# SKIP ${reason}`));
    assert.deepEqual(result.builds, []);
    assert.deepEqual(result.sandboxes, ["engram-context-mock-", "engram-context-mock-"]);
  });
});

test("packaged context older-history invalid binary remains a hard failure", () => {
  scenario(({ pkg, execute }) => {
    const result = execute(pkg, process.execPath);
    assert.equal(result.status, 1, result.stdout);
    summary(result, 2, 1, 0);
    assert.match(result.stdout, /server exited 1/);
    assert.deepEqual(result.builds, []);
    assert.deepEqual(result.sandboxes, ["engram-context-mock-", "engram-context-mock-", "engram-context-real-"]);
  });
});

for (const missing of fixtures) {
  test(`partial checkout context older-history skips build scenario without ${missing}`, () => {
    scenario(({ pkg, consumer, execute }) => {
      for (const fixture of fixtures) {
        const path = join(consumer, fixture);
        if (fixture === missing) {
          if (existsSync(path)) rmSync(path);
        } else {
          mkdirSync(join(path, ".."), { recursive: true });
          writeFileSync(path, "fixture availability sentinel\n");
        }
      }
      const result = execute(pkg, undefined, ["--test-name-pattern=^checkout context older-history preserves build failure$", "test/context-older-history-package.test.mjs"]);
      assert.equal(result.status, 0, result.stdout);
      for (const [name, count] of Object.entries({ tests: 1, pass: 0, fail: 0, skipped: 1, cancelled: 0 })) {
        assert.match(result.stdout, new RegExp(`^# ${name} ${count}$`, "m"));
      }
      assert.ok(result.stdout.includes(`# SKIP ${reason}`));
      assert.deepEqual(result.builds, []);
      assert.deepEqual(result.sandboxes, []);
    });
  });
}

test("checkout context older-history preserves build failure", {
  skip: fixtures.every(path => existsSync(join(PLUGIN_ROOT, "../..", path))) ? false : reason,
}, () => {
  scenario(({ execute }) => {
    const result = execute(PLUGIN_ROOT);
    assert.equal(result.status, 1, result.stdout);
    summary(result, 2, 1, 0);
    assert.match(result.stdout, /fixture Go build failure sentinel/);
    assert.equal(result.builds.length, 1);
    assert.equal(result.builds[0].args.at(-1), "./plugin/pi/test/support/real-server");
    assert.equal(result.builds[0].cwd, join(PLUGIN_ROOT, "../.."));
    assert.deepEqual(result.sandboxes, ["engram-context-mock-", "engram-context-mock-", "engram-context-real-"]);
  });
});

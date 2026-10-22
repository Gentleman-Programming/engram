import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { PLUGIN_ROOT } from "./plugin-sandbox.mjs";

const reason = "requires the Engram Go checkout or ENGRAM_PI_TEST_SERVER_BINARY";

function scenario(run) {
  const dir = mkdtempSync(join(tmpdir(), "engram-workflows-package-"));
  try {
    const consumer = join(dir, "consumer"), pkg = join(consumer, "node_modules", "gentle-engram");
    mkdirSync(pkg, { recursive: true });
    // Model a consumer that has its own unrelated Go module, not the Engram checkout.
    writeFileSync(join(consumer, "go.mod"), "module example.com/consumer\n");
    const metadata = JSON.parse(readFileSync(join(PLUGIN_ROOT, "package.json"), "utf8"));
    for (const entry of metadata.files) {
      const source = join(PLUGIN_ROOT, entry);
      if (existsSync(source)) cpSync(source, join(pkg, entry), { recursive: true });
    }
    const temp = join(dir, "temp"), calls = join(dir, "calls.json"), hook = join(dir, "build-hook.mjs");
    mkdirSync(temp);
    writeFileSync(calls, "[]");
    writeFileSync(hook, `import cp from "node:child_process";
import fs from "node:fs";
import { syncBuiltinESMExports } from "node:module";
const original = cp.spawnSync;
cp.spawnSync = (command, ...args) => {
  if (command !== "go") return original(command, ...args);
  const file = process.env.ENGRAM_BUILD_CALLS;
  const calls = JSON.parse(fs.readFileSync(file, "utf8"));
  calls.push({ command, args: args[0] });
  fs.writeFileSync(file, JSON.stringify(calls));
  return { status: 77, stdout: "", stderr: "fixture Go build failure sentinel", error: undefined };
};
syncBuiltinESMExports();
`);
    const execute = (root, binary) => {
      writeFileSync(calls, "[]");
      const env = { ...process.env, ENGRAM_BUILD_CALLS: calls, TEMP: temp, TMP: temp, TMPDIR: temp };
      // The child is a separate public test runner, not a worker of this runner.
      delete env.NODE_TEST_CONTEXT;
      delete env.ENGRAM_PI_TEST_SERVER_BINARY;
      if (binary !== undefined) env.ENGRAM_PI_TEST_SERVER_BINARY = binary;
      const result = spawnSync(process.execPath, ["--import", pathToFileURL(hook).href,
        "--test", "--test-reporter=tap", "test/real-agent-workflows.test.mjs"],
      { cwd: root, env, encoding: "utf8", timeout: 60000, maxBuffer: 4000000 });
      assert.ifError(result.error);
      assert.equal(result.signal, null);
      assert.equal(result.stderr, "");
      return { ...result, builds: JSON.parse(readFileSync(calls, "utf8")) };
    };
    run({ pkg, temp, execute });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function summary(result, { pass, fail, skipped }) {
  assert.match(result.stdout, /^# tests 8$/m);
  for (const [name, count] of Object.entries({ pass, fail, skipped, cancelled: 0 })) {
    assert.match(result.stdout, new RegExp(`^# ${name} ${count}$`, "m"));
  }
}

test("packaged real workflows skip before building Go or creating a sandbox", () => {
  scenario(({ pkg, temp, execute }) => {
    const result = execute(pkg);
    assert.equal(result.status, 0, result.stdout);
    summary(result, { pass: 0, fail: 0, skipped: 8 });
    assert.equal(result.stdout.split(`# SKIP ${reason}`).length - 1, 8);
    assert.deepEqual(result.builds, []);
    assert.deepEqual(readdirSync(temp), []);
  });
});

test("a configured but invalid binary fails all real workflows instead of skipping", () => {
  scenario(({ pkg, execute }) => {
    // A real executable that cannot serve the fixture must not be treated as unavailable.
    const result = execute(pkg, process.execPath);
    assert.equal(result.status, 1, result.stdout);
    summary(result, { pass: 0, fail: 8, skipped: 0 });
    assert.match(result.stdout, /server exited 1/);
    assert.deepEqual(result.builds, []);
  });
});

test("checkout real workflows preserve Go build failures instead of skipping", {
  skip: !existsSync(join(PLUGIN_ROOT, "../../internal/server/server.go")),
}, () => {
  scenario(({ execute }) => {
    const result = execute(PLUGIN_ROOT);
    assert.equal(result.status, 1, result.stdout);
    summary(result, { pass: 0, fail: 8, skipped: 0 });
    assert.match(result.stdout, /fixture Go build failure sentinel/);
    assert.equal(result.builds.length, 8);
    for (const build of result.builds) assert.equal(build.args.at(-1), "./plugin/pi/test/support/real-server");
  });
});

test("packaged real workflows run all eight tests with the prebuilt fixture binary", {
  skip: !process.env.ENGRAM_PI_TEST_SERVER_BINARY,
}, () => {
  scenario(({ pkg, execute }) => {
    const result = execute(pkg, process.env.ENGRAM_PI_TEST_SERVER_BINARY);
    assert.equal(result.status, 0, result.stdout);
    summary(result, { pass: 8, fail: 0, skipped: 0 });
    assert.deepEqual(result.builds, []);
  });
});

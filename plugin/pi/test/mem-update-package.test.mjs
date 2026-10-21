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
  const dir = mkdtempSync(join(tmpdir(), "engram-update-package-"));
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
    // Observe build attempts and sandbox creation without changing fixture helpers.
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
  record("builds", { command, args: args[0], cwd: args[1]?.cwd });
  return { status: 77, stdout: "", stderr: "fixture Go build failure sentinel", error: undefined };
};
const originalTemp = fsp.mkdtemp;
fsp.mkdtemp = (prefix, ...args) => {
  record("sandboxes", basename(prefix));
  return originalTemp(prefix, ...args);
};
syncBuiltinESMExports();
`);
    const execute = (root, binary) => {
      writeFileSync(calls, JSON.stringify({ builds: [], sandboxes: [] }));
      const env = { ...process.env, ENGRAM_BUILD_CALLS: calls, TEMP: temp, TMP: temp, TMPDIR: temp };
      delete env.NODE_TEST_CONTEXT;
      delete env.ENGRAM_PI_TEST_SERVER_BINARY;
      if (binary !== undefined) env.ENGRAM_PI_TEST_SERVER_BINARY = binary;
      const result = spawnSync(process.execPath, ["--import", pathToFileURL(hook).href,
        "--test", "--test-reporter=tap", "test/mem-update.test.mjs"],
      { cwd: root, env, encoding: "utf8", timeout: 60000, maxBuffer: 4000000 });
      assert.ifError(result.error);
      assert.equal(result.signal, null);
      assert.equal(result.stderr, "");
      return { ...result, ...JSON.parse(readFileSync(calls, "utf8")) };
    };
    run({ pkg, temp, execute });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function summary(result, { pass, fail, skipped }) {
  assert.match(result.stdout, /^# tests 4$/m);
  for (const [name, count] of Object.entries({ pass, fail, skipped, cancelled: 0 })) {
    assert.match(result.stdout, new RegExp(`^# ${name} ${count}$`, "m"));
  }
  assert.match(result.stdout, /ok 1 - registered mem_update exposes optional strings and forwards exact supplied fields/);
  assert.match(result.stdout, /ok 2 - mem_update legacy full-content metadata and omission forwarding/);
}

test("packaged mem_update skips only real tests before Go build or real sandbox creation", () => {
  scenario(({ pkg, temp, execute }) => {
    const result = execute(pkg);
    assert.equal(result.status, 0, result.stdout);
    summary(result, { pass: 2, fail: 0, skipped: 2 });
    assert.equal(result.stdout.split(`# SKIP ${reason}`).length - 1, 2);
    assert.deepEqual(result.builds, []);
    assert.deepEqual(result.sandboxes, ["engram-update-mock-", "engram-update-legacy-"]);
    assert.deepEqual(readdirSync(temp), []);
  });
});

test("packaged mem_update configured invalid binary fails real tests without Go build or skip", () => {
  scenario(({ pkg, temp, execute }) => {
    // A real executable that cannot serve the fixture must fail, not become unavailable.
    const result = execute(pkg, process.execPath);
    assert.equal(result.status, 1, result.stdout);
    summary(result, { pass: 2, fail: 2, skipped: 0 });
    assert.match(result.stdout, /server exited 1/);
    assert.deepEqual(result.builds, []);
    assert.deepEqual(result.sandboxes, ["engram-update-mock-", "engram-update-legacy-", "engram-update-real-", "engram-update-real-"]);
    assert.deepEqual(readdirSync(temp), []);
  });
});

test("checkout mem_update preserves Go build failure sentinel without skipping", {
  skip: !existsSync(join(PLUGIN_ROOT, "../../internal/server/server.go")),
}, () => {
  scenario(({ temp, execute }) => {
    const result = execute(PLUGIN_ROOT);
    assert.equal(result.status, 1, result.stdout);
    summary(result, { pass: 2, fail: 2, skipped: 0 });
    assert.match(result.stdout, /fixture Go build failure sentinel/);
    assert.equal(result.builds.length, 2);
    for (const build of result.builds) {
      assert.equal(build.command, "go");
      assert.deepEqual(build.args.slice(0, 2), ["build", "-o"]);
      assert.equal(build.args.at(-1), "./plugin/pi/test/support/real-server");
      assert.equal(build.cwd, join(PLUGIN_ROOT, "../.."));
    }
    assert.deepEqual(result.sandboxes, ["engram-update-mock-", "engram-update-legacy-", "engram-update-real-", "engram-update-real-"]);
    assert.deepEqual(readdirSync(temp), []);
  });
});

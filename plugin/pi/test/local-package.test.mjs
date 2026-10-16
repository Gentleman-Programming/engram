import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const cli = join(root, "tools/validate-pi-package.mjs");
const failure = "Local Pi package validation failed; no release approval.\n";
const success = "Local Pi package validated; not release approval.\n";
const refs = ["--core-ref", "refs/tags/v1.2.3", "--pi-ref", "refs/tags/pi-v1.2.3"];
const runtime = ["cli.js", "index.ts", "compaction-recovery.js", "memory-tool-chrome.js", "private-redaction.js", "mcp-template.json", "README.md"];

function snapshot(dir, prefix = "") {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const name = join(prefix, entry.name);
    return entry.isDirectory() ? snapshot(join(dir, entry.name), name) : [[name, readFileSync(join(dir, entry.name)).toString("base64")]];
  }).sort(([a], [b]) => a.localeCompare(b));
}

function fixture(t, modify = () => {}) {
  const dir = mkdtempSync(join(root, ".package-cli-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const pkg = join(dir, "plugin/pi");
  mkdirSync(join(pkg, "assets"), { recursive: true });
  mkdirSync(join(pkg, "test"));
  mkdirSync(join(dir, "internal/setup"), { recursive: true });
  const manifest = {
    name: "gentle-engram", version: "1.2.3", type: "module",
    files: [...runtime, "package.json", "assets/", "test/"],
    // A real pack must not run candidate lifecycle code.
    scripts: { prepack: "node -e \"require('fs').writeFileSync('HOOK_RAN', 'unsafe')\"" },
  };
  modify(manifest);
  writeFileSync(join(pkg, "package.json"), JSON.stringify(manifest));
  for (const name of runtime) writeFileSync(join(pkg, name), `fixture ${name}\n`);
  writeFileSync(join(pkg, "assets/logo.txt"), "asset\n");
  writeFileSync(join(pkg, "test/example.test.mjs"), "fixture test, never executed\n");
  writeFileSync(join(dir, "internal/setup/setup.go"), 'package setup\nconst piGentleEngramPackage = "npm:gentle-engram@1.2.3"\n');
  return dir;
}

function run(dir, args, env = {}) {
  const before = snapshot(dir);
  const result = spawnSync(process.execPath, [cli, ...args], {
    cwd: dir, encoding: "utf8", timeout: 60_000,
    env: { ...process.env, NODE_OPTIONS: "", ...env },
  });
  assert.equal(result.error, undefined);
  assert.equal(result.signal, null);
  assert.deepEqual(snapshot(dir), before, "candidate unchanged, no lifecycle code, temp/cache/tarball cleaned");
  return result;
}

test("local package help describes scope without invoking tools", t => {
  const dir = fixture(t);
  const result = run(dir, ["--help"], { PATH: "" });
  assert.equal(result.status, 0);
  assert.equal(result.stderr, "");
  for (const text of ["--core-ref", "--pi-ref", "offline", "not release approval", "current directory"]) assert.ok(result.stdout.includes(text), text);
});

test("local package CLI packs real fixture offline without hooks and validates it through Go", t => {
  const result = run(fixture(t), refs);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, success);
  assert.equal(result.stderr, "");
});

for (const [name, args, modify, env] of [
  ["missing refs", [], undefined, undefined],
  ["unsupported publishing", ["--publish"], undefined, undefined],
  ["duplicate ref", [...refs, "--pi-ref", "refs/tags/pi-v1.2.3"], undefined, undefined],
  ["invalid refs", ["--core-ref", "v1.2.3", "--pi-ref", "refs/tags/pi-v1.2.3"], undefined, undefined],
  ["wrong name", refs, pkg => { pkg.name = "other"; }, undefined],
  ["wrong version", refs, pkg => { pkg.version = "1.2.4"; }, undefined],
  ["missing declared runtime", refs, pkg => { pkg.files = ["package.json", "README.md"]; }, undefined],
  ["tools unavailable", refs, undefined, { PATH: "" }],
]) test(`local package CLI rejects ${name} without stdout or mutations`, t => {
  const result = run(fixture(t, modify), args, env);
  assert.equal(result.status, 1);
  assert.equal(result.stdout, "");
  assert.equal(result.stderr, failure);
});

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const command = fileURLToPath(new URL("../../../tools/release-candidate.mjs", import.meta.url));
const guard = fileURLToPath(new URL("./release-contract.mjs", import.meta.url));
const manifestPath = new URL("../package.json", import.meta.url);
const protectedPaths = [
  manifestPath,
  new URL("../../../internal/setup/setup.go", import.meta.url),
  new URL("./release-contract.mjs", import.meta.url),
];
const metadata = JSON.parse(await readFile(manifestPath, "utf8"));
const coreRef = `refs/tags/v${metadata.version}`;
const piRef = `refs/tags/pi-v${metadata.version}`;

function invoke(path, args) {
  const result = spawnSync(process.execPath, [path, ...args], {
    cwd: root,
    encoding: "utf8",
    timeout: 30_000,
    shell: false,
    // Do not inherit a loader/preload that could bypass the public entry point.
    env: { ...process.env, NODE_OPTIONS: "", GOPROXY: "off", GOTOOLCHAIN: "local" },
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null, `CLI terminated by ${result.signal}`);
  return result;
}

async function snapshot() {
  return Promise.all(protectedPaths.map((path) => readFile(path)));
}

async function rejected(args, diagnostic) {
  const before = await snapshot();
  const result = invoke(command, args);
  assert.notEqual(result.status, 0, "rejected input must not report candidate readiness");
  assert.equal(result.stdout, "", "rejection must not emit a success/export document");
  assert.match(result.stderr, diagnostic);
  assert.deepEqual(await snapshot(), before, "rejection must preserve candidate metadata and legacy guard");
  return result;
}

// These assertions call the public command in a subprocess. Parent observed
// missing-production RED before implementation; no internal helper is imported.
test("release candidate help describes conservative, non-publishing validation", () => {
  const result = invoke(command, ["--help"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, "");
  assert.match(result.stdout, /--core-ref/);
  assert.match(result.stdout, /--pi-ref/);
  assert.match(result.stdout, /refs\/tags\/v/);
  assert.match(result.stdout, /refs\/tags\/pi-v/);
  assert.match(result.stdout, /semantic|parity/i);
  assert.match(result.stdout, /sandbox|registration/i);
  assert.match(result.stdout, /not.*(?:proof|approval|parity)|(?:synthetic|foundation)/i);
  assert.match(result.stdout, /no publication|not publish|does not publish/i);
});

for (const [label, args, diagnostic] of [
  ["both refs are mandatory", [], /core-ref.*pi-ref|required/i],
  ["Pi ref is mandatory", ["--core-ref", coreRef], /pi-ref|required/i],
  ["core ref is mandatory", ["--pi-ref", piRef], /core-ref|required/i],
  ["core ref must be full", ["--core-ref", `v${metadata.version}`, "--pi-ref", piRef], /core-ref|full.*ref/i],
  ["Pi ref must be full", ["--core-ref", coreRef, "--pi-ref", `pi-v${metadata.version}`], /pi-ref|full.*ref/i],
  ["strict SemVer rejects leading zeros", ["--core-ref", "refs/tags/v01.2.3", "--pi-ref", "refs/tags/pi-v01.2.3"], /semver|core-ref|invalid/i],
  ["strict SemVer rejects numeric prerelease zeros", ["--core-ref", "refs/tags/v1.2.3-01", "--pi-ref", "refs/tags/pi-v1.2.3-01"], /semver|core-ref|invalid/i],
  ["Pi manifest must match selected Pi version", ["--core-ref", coreRef, "--pi-ref", "refs/tags/pi-v99.0.0"], /Package version must match candidate version/],
  ["positional arguments are rejected", ["--core-ref", coreRef, "--pi-ref", piRef, "unexpected"], /positional|unexpected|argument/i],
]) {
  test(`release candidate: ${label}; source unchanged`, async () => rejected(args, diagnostic));
}

// S7 override cases added after the original RED; parent validation pending.
for (const flag of ["--skip", "--skip-go", "--skip-pi", "--skip-pack", "--bypass", "--publish", "--root", "--proof", "--status"]) {
  test(`release candidate exposes no ${flag} escape hatch`, async () => {
    await rejected(["--core-ref", coreRef, "--pi-ref", piRef, flag], /unknown|unsupported|unexpected|flag|option/i);
  });
}

test("current source remains rejected despite matching ProfileAgent registrations", async () => {
  await withPluginSandbox("engram-candidate-registrations-", async ({ sandbox }) => {
    const registrations = [];
    const register = await importPluginFromSandbox(sandbox);
    register({
      registerTool(tool) { registrations.push(tool); },
      on() {},
    });
    // Capture calls, not source text or a Map that would hide duplicates. The
    // existing TypeBox stub proves registration only, NOT JSON-schema parity.
    const names = registrations.map(({ name }) => name);
    assert.equal(new Set(names).size, names.length, "actual registrations must be unique");
    for (const name of ["mem_consolidate", "mem_judge_many", "mem_session_finalize"]) assert.ok(names.includes(name));
    for (const name of ["mem_delete", "mem_stats", "mem_timeline"]) assert.ok(!names.includes(name));
    assert.ok(registrations.every(({ parameters }) => parameters && typeof parameters === "object"));
  });
  // This invokes the agreed live Go exporter via the public Node CLI, not a
  // checked-in canonical vector masquerading as live runtime evidence. Matching
  // inventories still reject: unknown schema/semantic evidence is never approval.
  const disposableBefore = (await readdir(root)).filter((name) => name.startsWith(".release-candidate-"));
  const result = await rejected(["--core-ref", coreRef, "--pi-ref", piRef], /inventory|parity|mcp-foundation|semantic|mem_consolidate|mem_delete/i);
  assert.equal(result.status, 1);
  assert.doesNotMatch(result.stderr, /Inventory Go-only:|Inventory Pi-only:|Duplicate Pi registrations:/);
  assert.match(result.stderr, /mcp-foundation/);
  assert.match(result.stderr, /Declared Pi semantic differences/);
  assert.match(result.stderr, /TypeBox stub captures registration only; JSON-schema equivalence is unproven/);
  assert.match(result.stderr, /even matching inventories remain rejected/);
  assert.match(result.stderr, /suite orchestration and pack\/tarball validation are deferred, not passed/);
  assert.deepEqual((await readdir(root)).filter((name) => name.startsWith(".release-candidate-")), disposableBefore);
});

test("independent core/Pi pair reaches semantic blockers and still rejects", async () => {
  const result = await rejected(["--core-ref", "refs/tags/v3.0.0", "--pi-ref", piRef], /Candidate rejected \(rejection-only checker; no release approval\)/);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /JSON-schema equivalence is unproven/);
  assert.match(result.stderr, /even matching inventories remain rejected/);
});

test("legacy pi-v guard still accepts the matching Pi-only ref", async () => {
  const before = await snapshot();
  const result = invoke(guard, [piRef]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, "");
  assert.equal(result.stderr, "");
  assert.deepEqual(await snapshot(), before);
});

test("legacy pi-v guard still rejects a core ref without changing source", async () => {
  const before = await snapshot();
  const result = invoke(guard, [coreRef]);
  assert.notEqual(result.status, 0);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /Pi release ref.*must match package version tag/);
  assert.deepEqual(await snapshot(), before);
});
